#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
umask 077

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TARGET="${PHASE5_RESTORE_CONTRACT_TARGET:-$ROOT/scripts/phase5-restore-verify.sh}"
MAKEFILE="$ROOT/Makefile"
BACKUP_ID='11111111-1111-4111-8111-111111111111'
SNAPSHOT_ID='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
BACKUP_IMAGE='happylearn-backup:phase5'
SECRET_MARKER='phase5-restore-secret-marker'
DATABASE_SECRET_MARKER='fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210'
OBJECT_SECRET_MARKER='123456789abcdeffedcba98765432100123456789abcdeffedcba98765432100'
SIGNING_SECRET_MARKER='abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd'
TEACHER_CREDENTIAL_SECRET='{"username":"restore-probe-teacher","password":"Restore Probe Teacher Secret 42!"}'
MANIFEST_TEXT='{"schemaVersion":1,"batchId":"11111111-1111-4111-8111-111111111111","createdAt":"2026-07-29T01:02:03.000000004Z","databaseMigrationVersion":42,"databaseDumpSha256":"1111111111111111111111111111111111111111111111111111111111111111","objectSnapshotId":"2222222222222222222222222222222222222222222222222222222222222222","objectCount":1,"referencedBytes":41}'
readonly TIMEOUT_CONTRACT_LIMIT_MILLISECONDS=12000

contract_sha256_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256
  else
    return 1
  fi
}

contract_time_milliseconds() {
  local nanoseconds milliseconds_length
  nanoseconds="$(date +%s%N 2>/dev/null)" || nanoseconds=''
  if [[ "$nanoseconds" =~ ^[0-9]{13,}$ ]]; then
    milliseconds_length=$((${#nanoseconds} - 6))
    printf '%s\n' "${nanoseconds:0:milliseconds_length}"
    return 0
  fi
  /usr/bin/perl \
    -MTime::HiRes=clock_gettime,CLOCK_MONOTONIC \
    -e 'printf "%.0f\n", 1000 * clock_gettime(CLOCK_MONOTONIC)'
}

MANIFEST_SHA256="$(
  printf '%s' "$MANIFEST_TEXT" |
    contract_sha256_stdin |
    sed -n '1s/[[:space:]].*$//p'
)"
[[ "$MANIFEST_SHA256" =~ ^[a-f0-9]{64}$ ]] || exit 1
VERIFICATION_REPORT_SHA256='3333333333333333333333333333333333333333333333333333333333333333'

contract_hex_to_bytes() {
  local value="$1"
  [[ -n "$value" &&
    "$value" =~ ^[a-f0-9]+$ &&
    $((${#value} % 2)) -eq 0 ]] ||
    return 1
  while [[ -n "$value" ]]; do
    printf '%b' "\\x${value:0:2}"
    value="${value:2}"
  done
}

EVIDENCE_SHA256="$(
  {
    contract_hex_to_bytes "${BACKUP_ID//-/}"
    contract_hex_to_bytes "$MANIFEST_SHA256"
    contract_hex_to_bytes "$VERIFICATION_REPORT_SHA256"
  } |
    contract_sha256_stdin |
    sed -n '1s/[[:space:]].*$//p'
)"
[[ "$EVIDENCE_SHA256" =~ ^[a-f0-9]{64}$ ]] || exit 1
CONTRACT_TEMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/phase5-restore-contract.XXXXXX")"
ACTIVE_FIXTURE_PID=''
CONTRACT_CLEANING_UP=false

fail() {
  printf 'phase5 restore contract: %s\n' "$1" >&2
  exit 1
}

active_fixture_is_direct_job() {
  local wanted_pid="$1"
  local child
  [[ "$wanted_pid" =~ ^[1-9][0-9]*$ ]] || return 1
  for child in $(jobs -pr); do
    [[ "$child" == "$wanted_pid" ]] && return 0
  done
  return 1
}

stop_active_fixture() {
  local pid="$ACTIVE_FIXTURE_PID"
  local attempts=0
  if active_fixture_is_direct_job "$pid"; then
    kill -TERM "$pid" 2>/dev/null || true
    while active_fixture_is_direct_job "$pid" &&
      [[ "$attempts" -lt 100 ]]; do
      sleep 0.01
      attempts=$((attempts + 1))
    done
    if active_fixture_is_direct_job "$pid"; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
    wait "$pid" 2>/dev/null || true
  fi
  ACTIVE_FIXTURE_PID=''
}

cleanup_contract() {
  [[ "$CONTRACT_CLEANING_UP" == false ]] || return 0
  CONTRACT_CLEANING_UP=true
  stop_active_fixture
  chmod -R u+rwX "$CONTRACT_TEMP_ROOT" 2>/dev/null || true
  rm -rf "$CONTRACT_TEMP_ROOT"
}

handle_contract_signal() {
  local status="$1"
  [[ "$status" =~ ^(129|130|143)$ ]] || status=1
  trap '' HUP INT TERM
  if [[ -n "${PHASE5_CONTRACT_SIGNAL_HANDLER_MARKER:-}" ]]; then
    : >"$PHASE5_CONTRACT_SIGNAL_HANDLER_MARKER"
  fi
  cleanup_contract
  exit "$status"
}

trap cleanup_contract EXIT
trap 'handle_contract_signal 129' HUP
trap 'handle_contract_signal 130' INT
trap 'handle_contract_signal 143' TERM

if [[ "${PHASE5_CONTRACT_SIGNAL_SELF_TEST:-}" == true ]]; then
  signal_marker="${PHASE5_CONTRACT_SIGNAL_MARKER:?}"
  signal_root_marker="${PHASE5_CONTRACT_SIGNAL_ROOT_MARKER:?}"
  signal_child_marker="${PHASE5_CONTRACT_SIGNAL_CHILD_MARKER:?}"
  (
    trap '' HUP INT TERM
    while :; do
      sleep 0.05
    done
  ) &
  ACTIVE_FIXTURE_PID="$!"
  printf '%s\n' "$ACTIVE_FIXTURE_PID" >"$signal_child_marker"
  printf '%s\n' "$CONTRACT_TEMP_ROOT" >"$signal_root_marker"
  : >"$signal_marker"
  while :; do
    sleep 0.05
  done
fi

assert_no_resources() {
  local fixture="$1"
  local kind
  for kind in containers volumes networks; do
    if find "$fixture/state/$kind" -mindepth 1 -maxdepth 1 -print -quit |
      grep -q .; then
      find "$fixture/state/$kind" -mindepth 1 -maxdepth 1 -print >&2
      fail "restore left disposable $kind"
    fi
  done
}

assert_failure_before_restore() {
  local fixture="$1"
  grep -Fq ' restic --no-cache check --read-data' "$fixture/docker.log" ||
    fail 'repository failure did not execute read-data check'
  if grep -Fq ' restic restore ' "$fixture/docker.log"; then
    fail 'repository failure restored data after check failure'
  fi
  assert_no_resources "$fixture"
}

log_line() {
  local fixture="$1"
  local value="$2"
  grep -nF -- "$value" "$fixture/docker.log" | head -n1 | cut -d: -f1
}

assert_before() {
  local fixture="$1"
  local first="$2"
  local second="$3"
  local first_line second_line
  first_line="$(log_line "$fixture" "$first")"
  second_line="$(log_line "$fixture" "$second")"
  [[ -n "$first_line" && -n "$second_line" &&
    "$first_line" -lt "$second_line" ]] ||
    fail "restore order invalid: $first must precede $second"
}

wait_for_file() {
  local path="$1"
  local attempts=0
  while [[ ! -f "$path" && "$attempts" -lt 500 ]]; do
    sleep 0.01
    attempts=$((attempts + 1))
  done
  [[ -f "$path" ]]
}

wait_for_file_long() {
  local path="$1"
  local attempts=0
  while [[ ! -f "$path" && "$attempts" -lt 5000 ]]; do
    sleep 0.01
    attempts=$((attempts + 1))
  done
  [[ -f "$path" ]]
}

wait_for_direct_child() {
  local pid="$1"
  local attempts=0
  local running
  while [[ "$attempts" -lt 5000 ]]; do
    running=false
    for child in $(jobs -pr); do
      [[ "$child" == "$pid" ]] && running=true
    done
    [[ "$running" == false ]] && break
    sleep 0.01
    attempts=$((attempts + 1))
  done
  [[ "$running" == false ]] || return 1
  wait "$pid"
}

wait_for_pid_absent() {
  local pid="$1"
  local attempts=0
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  while kill -0 "$pid" 2>/dev/null && [[ "$attempts" -lt 500 ]]; do
    sleep 0.01
    attempts=$((attempts + 1))
  done
  ! kill -0 "$pid" 2>/dev/null
}

fake_resource_identity() {
  local name="$1"
  local checksum
  checksum="$(printf '%s' "$name" | cksum | awk '{print $1}')"
  printf '%064x' "$checksum"
}

make_fixture() {
  local fixture
  fixture="$(mktemp -d "$CONTRACT_TEMP_ROOT/fixture.XXXXXX")"
  mkdir -p \
    "$fixture/bin" \
    "$fixture/repository" \
    "$fixture/secrets" \
    "$fixture/control" \
    "$fixture/reports" \
    "$fixture/state/containers" \
    "$fixture/state/volumes" \
    "$fixture/state/networks"
  chmod 0700 \
    "$fixture/repository" \
    "$fixture/secrets" \
    "$fixture/control" \
    "$fixture/reports" \
    "$fixture/state" \
    "$fixture/state/containers" \
    "$fixture/state/volumes" \
    "$fixture/state/networks"
  printf 'repository fixture\n' >"$fixture/repository/config"
  printf '%s\n' "$SECRET_MARKER" >"$fixture/secrets/local_password"
  chmod 0400 "$fixture/secrets/local_password"
  printf 'license fixture\n' >"$fixture/minio.license"
  chmod 0400 "$fixture/minio.license"
  printf '%s\n' "$TEACHER_CREDENTIAL_SECRET" \
    >"$fixture/restore-probe-teacher.json"
  chmod 0400 "$fixture/restore-probe-teacher.json"
  : >"$fixture/docker.log"
  : >"$fixture/random.counter"

  cat >"$fixture/bin/od" <<'FAKE_OD'
#!/usr/bin/env bash
set -euo pipefail
counter="$(<"$PHASE5_FAKE_RANDOM_COUNTER")"
counter="${counter:-0}"
counter=$((counter + 1))
printf '%s\n' "$counter" >"$PHASE5_FAKE_RANDOM_COUNTER"
case "$counter" in
  1) printf ' 0123456789abcdeff123456789abcdef0123456789abcdef0123456789abcdef\n' ;;
  2) printf ' fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210\n' ;;
  3) printf ' 123456789abcdeffedcba98765432100123456789abcdeffedcba98765432100\n' ;;
  4) printf ' abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd\n' ;;
  *) printf ' 789abcdeffedcba0123456789abcdeff789abcdeffedcba0123456789abcdef0\n' ;;
esac
FAKE_OD
  chmod 0700 "$fixture/bin/od"
  cat >"$fixture/bin/chmod" <<'FAKE_CHMOD'
#!/usr/bin/env bash
set -euo pipefail
target="${*: -1}"
if [[ "$target" =~ /phase5-restore-verify\.[A-Za-z0-9]+$ ]]; then
  printf '%s\n' "$target" >"$PHASE5_FAKE_WORKSPACE_MARKER"
fi
if [[ "${PHASE5_FAKE_MODE:-}" == workspace_failure &&
  "$target" =~ /phase5-restore-verify\.[A-Za-z0-9]+$ ]]; then
  exit 76
fi
exec /bin/chmod "$@"
FAKE_CHMOD
  chmod 0700 "$fixture/bin/chmod"
  cat >"$fixture/bin/stat" <<'FAKE_STAT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${PHASE5_FAKE_MODE:-}" == credential_wrong_owner &&
  "${*: -1}" == "$PHASE5_FAKE_TEACHER_CREDENTIAL_FILE" ]]; then
  case "${1:-}:${2:-}" in
    -c:%a\|%u\|%s)
      if /usr/bin/stat --version >/dev/null 2>&1; then
        mode="$(/usr/bin/stat -c '%a' "${3:-}")"
        size="$(/usr/bin/stat -c '%s' "${3:-}")"
      else
        mode="$(/usr/bin/stat -f '%Lp' "${3:-}")"
        size="$(/usr/bin/stat -f '%z' "${3:-}")"
      fi
      printf '%s|%s|%s\n' \
        "$mode" "$((PHASE5_FAKE_HOST_UID + 1))" "$size"
      exit 0
      ;;
    -f:%Lp\|%u\|%z)
      mode="$(/usr/bin/stat -f '%Lp' "${3:-}")"
      size="$(/usr/bin/stat -f '%z' "${3:-}")"
      printf '%s|%s|%s\n' \
        "$mode" "$((PHASE5_FAKE_HOST_UID + 1))" "$size"
      exit 0
      ;;
    -c:%u|-f:%u)
      printf '%s\n' "$((PHASE5_FAKE_HOST_UID + 1))"
      exit 0
      ;;
  esac
fi
if [[ "${PHASE5_FAKE_MODE:-}" == gnu_stat ]]; then
  case "${1:-}:${2:-}" in
    --version:)
      printf '%s\n' 'stat (GNU coreutils) 9.5'
      exit 0
      ;;
    -c:%a\|%u\|%s)
      if /usr/bin/stat --version >/dev/null 2>&1; then
        exec /usr/bin/stat -c '%a|%u|%s' "${3:-}"
      fi
      printf '%s|%s|%s\n' \
        "$(/usr/bin/stat -f '%Lp' "${3:-}")" \
        "$(/usr/bin/stat -f '%u' "${3:-}")" \
        "$(/usr/bin/stat -f '%z' "${3:-}")"
      exit 0
      ;;
    -c:%a\|%u\|%s\|%h\|%i)
      if /usr/bin/stat --version >/dev/null 2>&1; then
        exec /usr/bin/stat -c '%a|%u|%s|%h|%i' "${3:-}"
      fi
      printf '%s|%s|%s|%s|%s\n' \
        "$(/usr/bin/stat -f '%Lp' "${3:-}")" \
        "$(/usr/bin/stat -f '%u' "${3:-}")" \
        "$(/usr/bin/stat -f '%z' "${3:-}")" \
        "$(/usr/bin/stat -f '%l' "${3:-}")" \
        "$(/usr/bin/stat -f '%i' "${3:-}")"
      exit 0
      ;;
    -c:%a)
      if /usr/bin/stat --version >/dev/null 2>&1; then
        exec /usr/bin/stat -c '%a' "${3:-}"
      fi
      exec /usr/bin/stat -f '%Lp' "${3:-}"
      ;;
    -c:%u)
      if /usr/bin/stat --version >/dev/null 2>&1; then
        exec /usr/bin/stat -c '%u' "${3:-}"
      fi
      exec /usr/bin/stat -f '%u' "${3:-}"
      ;;
    -c:%g)
      if /usr/bin/stat --version >/dev/null 2>&1; then
        exec /usr/bin/stat -c '%g' "${3:-}"
      fi
      exec /usr/bin/stat -f '%g' "${3:-}"
      ;;
    -c:%s)
      if /usr/bin/stat --version >/dev/null 2>&1; then
        exec /usr/bin/stat -c '%s' "${3:-}"
      fi
      exec /usr/bin/stat -f '%z' "${3:-}"
      ;;
    -f:*)
      printf '%s\n' poisoned-bsd-probe
      exit 0
      ;;
  esac
fi
exec /usr/bin/stat "$@"
FAKE_STAT
  chmod 0700 "$fixture/bin/stat"
  cat >"$fixture/bin/ln" <<'FAKE_LN'
#!/usr/bin/env bash
set -euo pipefail
/bin/ln "$@"
target="${*: -1}"
case "${PHASE5_FAKE_MODE:-}:$(basename "$target")" in
  supervisor_ready_hardlink:ready | \
    supervisor_identity_hardlink:identity | \
    supervisor_status_hardlink:status | \
    supervisor_ack_hardlink:ack)
    /bin/ln "$target" "${target}.injected-hardlink"
    ;;
esac
FAKE_LN
  chmod 0700 "$fixture/bin/ln"
  cat >"$fixture/bin/flock" <<'FAKE_FLOCK'
#!/usr/bin/env bash
set -euo pipefail
[[ "$#" -eq 3 &&
  "$1" == '--exclusive' &&
  "$2" == '--nonblock' &&
  "$3" =~ ^[0-9]+$ ]] ||
  exit 64
expected_restore_lock="${PHASE5_FAKE_EXPECTED_RESTORE_LOCK:?}"
expected_report_lock="${PHASE5_FAKE_EXPECTED_REPORT_LOCK:?}"
actual_lock=''
if [[ -L "/proc/$PPID/fd/$3" ]]; then
  actual_lock="$(readlink "/proc/$PPID/fd/$3")"
elif [[ -x /usr/sbin/lsof ]]; then
  actual_lock="$(
    /usr/sbin/lsof -a -p "$PPID" -d "$3" -Fn |
      sed -n 's/^n//p'
  )"
fi
case "$actual_lock" in
  "$expected_restore_lock")
    release_file="${PHASE5_FAKE_FLOCK_RELEASE_FILE:-}"
    ;;
  "$expected_report_lock")
    release_file="${PHASE5_FAKE_REPORT_FLOCK_RELEASE_FILE:-}"
    ;;
  *) exit 64 ;;
esac
contract_lock="${actual_lock}.contract-held"
mkdir "$contract_lock" 2>/dev/null || exit 75
trap 'rmdir "$contract_lock"' EXIT
if [[ -n "$release_file" ]]; then
  printf '%s\n' started >"${release_file}.started"
  while [[ ! -e "$release_file" ]]; do
    sleep 0.01
  done
fi
FAKE_FLOCK
  chmod 0700 "$fixture/bin/flock"

  cat >"$fixture/bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -euo pipefail

log="$PHASE5_FAKE_DOCKER_LOG"
state="$PHASE5_FAKE_DOCKER_STATE"
mode="${PHASE5_FAKE_MODE:-success}"
printf 'docker %s\n' "$*" >>"$log"

resource_file() {
  local kind="$1"
  local identifier="$2"
  local candidate observed_id matches=0
  if [[ "$identifier" =~ ^happylearn-phase5-restore-[a-f0-9]{12}[-a-z0-9]*$ ]]; then
    printf '%s/%s/%s' "$state" "$kind" "$identifier"
    return 0
  fi
  [[ "$identifier" =~ ^[a-f0-9]{64}$ ]] || return 1
  for candidate in "$state/$kind"/*; do
    [[ -f "$candidate" ]] || continue
    IFS='|' read -r observed_id _ <"$candidate"
    if [[ "$observed_id" == "$identifier" ]]; then
      matches=$((matches + 1))
      printf '%s' "$candidate"
    fi
  done
  [[ "$matches" -eq 1 ]]
}

fake_identity() {
  local name="$1"
  local checksum
  checksum="$(printf '%s' "$name" | cksum | awk '{print $1}')"
  printf '%064x' "$checksum"
}

arg_after() {
  local wanted="$1"
  shift
  while [[ "$#" -gt 0 ]]; do
    if [[ "$1" == "$wanted" ]]; then
      [[ "$#" -ge 2 ]] || exit 64
      printf '%s' "$2"
      return 0
    fi
    shift
  done
  return 1
}

label_value() {
  local wanted="$1"
  shift
  local value
  while [[ "$#" -gt 0 ]]; do
    if [[ "$1" == '--label' ]]; then
      [[ "$#" -ge 2 ]] || exit 64
      value="$2"
      if [[ "$value" == "$wanted="* ]]; then
        printf '%s' "${value#*=}"
        return 0
      fi
      shift 2
      continue
    fi
    shift
  done
  return 1
}

inspect_resource() {
  local kind="$1"
  shift
  local name="${*: -1}"
  local file
  if ! file="$(resource_file "$kind" "$name")"; then
    case "$kind" in
      containers) printf 'Error: No such container: %s\n' "$name" >&2 ;;
      volumes)
        if [[ "$mode" == modern_resource_not_found ]]; then
          printf 'Error response from daemon: get %s: no such volume\n' \
            "$name" >&2
        else
          printf 'Error: No such volume: %s\n' "$name" >&2
        fi
        ;;
      networks)
        if [[ "$mode" == modern_resource_not_found ]]; then
          printf 'Error response from daemon: network %s not found\n' \
            "$name" >&2
        else
          printf 'Error: No such network: %s\n' "$name" >&2
        fi
        ;;
      *) exit 64 ;;
    esac
    exit 1
  fi
  if [[ "$mode" == inspect_unknown && "$kind" == networks &&
    ! -f "$file" ]]; then
    printf '%s\n' 'Error response from daemon: temporarily unavailable' >&2
    exit 70
  fi
  if [[ "$mode" == cleanup_inspect_unknown &&
    "$kind:$name" == containers:*-app && -f "$file" &&
    "$*" == *'--format'* ]]; then
    printf '%s\n' 'Error response from daemon: temporarily unavailable' >&2
    exit 70
  fi
  if [[ ! -f "$file" ]]; then
    case "$kind" in
      containers) printf 'Error: No such container: %s\n' "$name" >&2 ;;
      volumes)
        if [[ "$mode" == modern_resource_not_found ]]; then
          printf 'Error response from daemon: get %s: no such volume\n' \
            "$name" >&2
        else
          printf 'Error: No such volume: %s\n' "$name" >&2
        fi
        ;;
      networks)
        if [[ "$mode" == modern_resource_not_found ]]; then
          printf 'Error response from daemon: network %s not found\n' \
            "$name" >&2
        else
          printf 'Error: No such network: %s\n' "$name" >&2
        fi
        ;;
      *) exit 64 ;;
    esac
    exit 1
  fi
  if [[ "$*" == *'--format'* ]]; then
    cat "$file"
  else
    printf '[{}]\n'
  fi
}

create_resource() {
  local kind="$1"
  shift
  local name="${*: -1}"
  local project owner type backup file identity
  project="$(label_value com.docker.compose.project "$@")" || exit 64
  owner="$(label_value io.happylearn.phase5.restore-owner "$@")" || exit 64
  type="$(label_value io.happylearn.phase5.restore-kind "$@")" || exit 64
  backup="$(label_value io.happylearn.phase5.restore-backup-id "$@")" || exit 64
  [[ "$project" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ &&
    "$name" == "$project-"* &&
    "$owner" =~ ^[a-f0-9]{64}$ &&
    "$backup" == "$PHASE5_FAKE_BACKUP_ID" &&
    "$type" =~ ^(postgres|aistor|aistor-license|secrets|network)$ ]] ||
    exit 64
  file="$(resource_file "$kind" "$name")"
  [[ ! -e "$file" ]] || exit 1
  identity="$name"
  [[ "$kind" == volumes ]] || identity="$(fake_identity "$name")"
  printf '%s|%s|%s|%s|%s\n' \
    "$identity" "$project" "$owner" "$type" "$backup" >"$file"
  if [[ "$mode" == "ambiguous_${kind%?}_create" ]] ||
    [[ "$mode" == ambiguous_network_create && "$kind" == networks ]] ||
    [[ "$mode" == ambiguous_volume_create &&
      "$kind:$type" == volumes:postgres ]]; then
    exit 70
  fi
  printf '%s\n' "$name"
}

list_resources() {
  local kind="$1"
  shift
  local wanted_backup=''
  local previous=''
  local argument file identity project owner type backup
  local output_format=''
  for argument in "$@"; do
    if [[ "$previous" == '--filter' &&
      "$argument" == "label=io.happylearn.phase5.restore-backup-id="* ]]; then
      wanted_backup="${argument#*=}"
      wanted_backup="${wanted_backup#*=}"
    fi
    if [[ "$previous" == '--format' ]]; then
      output_format="$argument"
    fi
    previous="$argument"
  done
  [[ "$wanted_backup" == "$PHASE5_FAKE_BACKUP_ID" ]] || exit 64
  for file in "$state/$kind"/*; do
    [[ -f "$file" ]] || continue
    IFS='|' read -r identity project owner type backup <"$file"
    [[ "$backup" == "$wanted_backup" ]] || continue
    case "$kind:$output_format" in
      containers:'{{.Names}}' | volumes:'{{.Name}}' | networks:'{{.Name}}')
        basename "$file"
        ;;
      containers:'' | networks:'') printf '%s\n' "$identity" ;;
      volumes:'') basename "$file" ;;
      *) exit 64 ;;
    esac
  done
}

remove_resource() {
  local kind="$1"
  local name="$2"
  local file resource_name
  file="$(resource_file "$kind" "$name")" || exit 1
  resource_name="$(basename "$file")"
  [[ -f "$file" ]] || exit 1
  rm -f "$file"
  if [[ "$mode" == cleanup_failure &&
    "$kind:$resource_name" == containers:*-app ]]; then
    exit 70
  fi
}

case "${1:-} ${2:-}" in
  'volume ls')
    shift 2
    list_resources volumes "$@"
    exit
    ;;
  'volume inspect')
    shift 2
    inspect_resource volumes "$@"
    exit
    ;;
  'volume create')
    shift 2
    create_resource volumes "$@"
    exit
    ;;
  'volume rm')
    remove_resource volumes "${3:-}"
    exit
    ;;
  'network inspect')
    shift 2
    inspect_resource networks "$@"
    exit
    ;;
  'network ls')
    shift 2
    list_resources networks "$@"
    exit
    ;;
  'network create')
    shift 2
    create_resource networks "$@"
    exit
    ;;
  'network rm')
    remove_resource networks "${3:-}"
    exit
    ;;
  'container inspect')
    shift 2
    inspect_resource containers "$@"
    exit
    ;;
  'container ls')
    shift 2
    list_resources containers "$@"
    exit
    ;;
  'rm --force')
    remove_resource containers "${3:-}"
    exit
    ;;
  'exec '*)
    case "$*" in
      *pg_isready*|*'/proc/1/comm'*'psql'*'SELECT 1'*|\
        *minio/health/ready*|*api/v1/health/ready*)
        exit 0
        ;;
      *) exit 64 ;;
    esac
    ;;
  'run '*)
    ;;
  *)
    exit 64
    ;;
esac

name="$(arg_after --name "$@")" || exit 64
project="$(label_value com.docker.compose.project "$@")" || exit 64
owner="$(label_value io.happylearn.phase5.restore-owner "$@")" || exit 64
kind="$(label_value io.happylearn.phase5.restore-kind "$@")" || exit 64
backup="$(label_value io.happylearn.phase5.restore-backup-id "$@")" || exit 64
[[ "$project" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ &&
  "$name" == "$project-"* &&
  "$owner" =~ ^[a-f0-9]{64}$ &&
  "$backup" == "$PHASE5_FAKE_BACKUP_ID" ]] ||
  exit 64
[[ "$*" == *'--cpus 0.25'* &&
  "$*" == *'--memory 512m'* &&
  "$*" == *'--memory-swap 512m'* &&
  "$*" == *'--pids-limit 256'* ]] ||
  exit 64
if [[ "$mode" == sigterm_delayed_create && "$kind" == restic-check ]]; then
  : >"$PHASE5_FAKE_SIGNAL_MARKER"
  trap '' HUP INT TERM
  while [[ ! -e "$PHASE5_FAKE_DELAYED_CREATE_RELEASE" ]]; do
    sleep 0.01
  done
fi
if [[ "$*" == *"$PHASE5_FAKE_SECRET_MARKER"* ||
  "$*" == *"$PHASE5_FAKE_TEACHER_CREDENTIAL_SECRET"* ||
  "$*" == *'POSTGRES_PASSWORD='* ||
  "$*" == *'MINIO_ROOT_PASSWORD='* ||
  "$*" == *'HAPPYLEARN_DATABASE_URL='* ||
  "$*" == *'HAPPYLEARN_LOGIN_THROTTLE_SECRET='* ||
  "$*" == *'HAPPYLEARN_MINIO_SECRET_KEY='* ]]; then
  exit 64
fi
container_file="$(resource_file containers "$name")"
[[ ! -e "$container_file" ]] || exit 1
container_identity="$(fake_identity "$name")"
printf '%s|%s|%s|%s|%s\n' \
  "$container_identity" "$project" "$owner" "$kind" "$backup" \
  >"$container_file"
if [[ "$mode" == sigterm_delayed_create && "$kind" == restic-check ]]; then
  : >"$PHASE5_FAKE_DELAYED_CREATE_FINISHED"
fi
if [[ "$mode" == ambiguous_container_create && "$kind" == restic-check ]]; then
  exit 70
fi

case "$kind" in
  restic-*|postgres-restore|revoke-sessions|restore-check|restore-http-probe)
    [[ "$*" == *"--user ${PHASE5_FAKE_HOST_UID}:${PHASE5_FAKE_HOST_GID}"* ]] ||
      exit 64
    ;;
  restore-ownership)
    [[ "$*" == *'--network none'* &&
      "$*" == *'--read-only'* &&
      "$*" == *'--user 0:0'* &&
      "$*" == *'--cap-drop ALL'* &&
      "$*" == *'--cap-add CHOWN'* &&
      "$*" == *'--cap-add DAC_READ_SEARCH'* &&
      "$*" == *'--security-opt no-new-privileges:true'* &&
      "$*" == *'dst=/restore'* &&
      "$*" == *'chown --recursive --no-dereference'* &&
      "$*" == *'find /restore -xdev'* ]] ||
      exit 64
    ;;
esac

[[ "$*" != *'--env-file'* ]] || exit 64

if [[ "$*" == *'--detach'* ]]; then
  case "$kind" in
    postgres)
      [[ "$*" == *'--tmpfs /var/run/postgresql:rw,noexec,nosuid,size=8m'* &&
        "$*" == *'volume-subpath=postgres,readonly'* &&
        "$*" == *'set -a; . /run/restore-secrets/runtime.env; set +a; exec /usr/local/bin/docker-entrypoint.sh postgres'* ]] ||
        exit 71
      printf '%064d\n' 1
      exit 0
      ;;
    aistor)
      [[ "$*" == *'dst=/minio-license,readonly'* &&
        "$*" != *'type=bind,src='*'minio.license'* &&
        "$*" == *'volume-subpath=aistor,readonly'* &&
        "$*" == *'set -a; . /run/restore-secrets/runtime.env; set +a; exec minio server'* ]] ||
        exit 71
      printf '%064d\n' 1
      exit 0
      ;;
    redis)
      printf '%064d\n' 1
      exit 0
      ;;
    app)
      grep -Fq 'PHASE5_RESTORE_SESSIONS_REVOKED' "$log" || exit 71
      [[ "$*" == *'volume-subpath=app,readonly'* &&
        "$*" == *'set -a; . /run/restore-secrets/runtime.env; set +a; exec /app/happylearn'* ]] ||
        exit 71
      if [[ "$mode" == cleanup_notfound ]]; then
        rm -f "$container_file"
      fi
      printf '%064d\n' 2
      exit 0
      ;;
    *) exit 64 ;;
  esac
fi

case "$kind" in
  volume-probe-postgres)
    [[ "$mode" != nonempty_postgres ]] || exit 72
    ;;
  volume-probe-aistor)
    [[ "$mode" != nonempty_aistor ]] || exit 72
    ;;
  volume-probe-aistor-license)
    ;;
  volume-probe-secrets)
    ;;
  restic-check)
    [[ "$*" == *' restic --no-cache check --read-data'* &&
      "$*" != *'--no-lock'* ]] ||
      exit 64
    case "$mode" in
      supervisor_identity_timeout|supervisor_identity_term)
        trap '' HUP INT TERM
        printf '%s\n' "$$" >"$PHASE5_FAKE_SUPERVISOR_DIRECT_PID"
        (
          trap '' HUP INT TERM
          while :; do
            printf 'x' >>"$PHASE5_FAKE_SUPERVISOR_HEARTBEAT"
            IFS= read -r -t 1 -u 7 _ || true
          done
        ) &
        descendant_pid="$!"
        printf '%s\n' "$descendant_pid" \
          >"$PHASE5_FAKE_SUPERVISOR_DESCENDANT_PID"
        : >"$PHASE5_FAKE_SUPERVISOR_DESCENDANT_MARKER"
        while :; do
          printf 'x' >>"$PHASE5_FAKE_SUPERVISOR_HEARTBEAT"
          IFS= read -r -t 1 -u 7 _ || true
        done
        ;;
      supervisor_identity_midrun)
        printf '%s\n' "$$" >"$PHASE5_FAKE_SUPERVISOR_DIRECT_PID"
        : >"$PHASE5_FAKE_SUPERVISOR_DESCENDANT_MARKER"
        while [[ ! -e "$PHASE5_FAKE_SUPERVISOR_DIRECT_RELEASE" ]]; do
          sleep 0.01
        done
        : >"$PHASE5_FAKE_SUPERVISOR_DIRECT_EXITING"
        exit 0
        ;;
      supervisor_descendant)
        printf '%s\n' "$$" >"$PHASE5_FAKE_SUPERVISOR_DIRECT_PID"
        trap ': >"$PHASE5_FAKE_SUPERVISOR_DIRECT_CONT_MARKER"' CONT
        (
          trap '' HUP INT TERM
          trap ': >"$PHASE5_FAKE_SUPERVISOR_DESCENDANT_CONT_MARKER"' CONT
          while :; do
            sleep 0.05
          done
        ) &
        descendant_pid="$!"
        printf '%s\n' "$descendant_pid" \
          >"$PHASE5_FAKE_SUPERVISOR_DESCENDANT_PID"
        : >"$PHASE5_FAKE_SUPERVISOR_DESCENDANT_MARKER"
        while [[ ! -e "$PHASE5_FAKE_SUPERVISOR_DIRECT_RELEASE" ]]; do
          sleep 0.01
        done
        : >"$PHASE5_FAKE_SUPERVISOR_DIRECT_EXITING"
        exit 0
        ;;
      wrong_secret|tampered_pack|check_failure) exit 73 ;;
      timeout)
        trap '' TERM
        while :; do sleep 0.1; done
        ;;
      sigterm)
        : >"$PHASE5_FAKE_SIGNAL_MARKER"
        while [[ -f "$container_file" ]]; do
          sleep 0.01
        done
        exit 143
        ;;
    esac
    ;;
  restic-select)
    [[ "$*" == *" restic --no-cache snapshots --json --tag happylearn-batch:${PHASE5_FAKE_BACKUP_ID}"* &&
      "$*" != *'--no-lock'* ]] ||
      exit 64
    case "$mode" in
      missing_snapshot) printf '[]\n' ;;
      duplicate_snapshot)
        printf '[{"id":"%s"},{"id":"%s"}]\n' \
          "$PHASE5_FAKE_SNAPSHOT_ID" \
          'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
        ;;
      extra_snapshot_tag)
        printf '[{"id":"%s","tags":["happylearn-batch:%s","happylearn-manifest-sha256:%s","unexpected-tag"]}]\n' \
          "$PHASE5_FAKE_SNAPSHOT_ID" "$PHASE5_FAKE_BACKUP_ID" \
          "$PHASE5_FAKE_MANIFEST_SHA256"
        ;;
      duplicate_manifest_tag)
        printf '[{"id":"%s","tags":["happylearn-batch:%s","happylearn-manifest-sha256:%s","happylearn-manifest-sha256:%s"]}]\n' \
          "$PHASE5_FAKE_SNAPSHOT_ID" "$PHASE5_FAKE_BACKUP_ID" \
          "$PHASE5_FAKE_MANIFEST_SHA256" "$PHASE5_FAKE_MANIFEST_SHA256"
        ;;
      *)
        printf '[{"id":"%s","tags":["happylearn-batch:%s","happylearn-manifest-sha256:%s"]}]\n' \
          "$PHASE5_FAKE_SNAPSHOT_ID" "$PHASE5_FAKE_BACKUP_ID" \
          "$PHASE5_FAKE_MANIFEST_SHA256"
        ;;
    esac
    ;;
  restic-restore)
    grep -Fq ' restic --no-cache check --read-data' "$log" || exit 74
    [[ "$*" == *" restic --no-cache restore ${PHASE5_FAKE_SNAPSHOT_ID} --target /restore"* &&
      "$*" != *'--no-lock'* ]] ||
      exit 64
    restore_mount=''
    previous=''
    for argument in "$@"; do
      if [[ "$previous" == '--mount' &&
        "$argument" == type=bind,src=*,dst=/restore ]]; then
        restore_mount="${argument#type=bind,src=}"
        restore_mount="${restore_mount%,dst=/restore}"
      fi
      previous="$argument"
    done
    [[ -n "$restore_mount" ]] || exit 64
    printf 'database dump fixture\n' >"$restore_mount/database.dump"
    printf '%s' "$PHASE5_FAKE_MANIFEST_TEXT" >"$restore_mount/manifest.json"
    if [[ "$mode" != restore_layout_invalid ]]; then
      mkdir -p "$restore_mount/source/aistor"
      printf 'object fixture\n' >"$restore_mount/source/aistor/object"
    fi
    if [[ "$mode" == manifest_hash_mismatch ]]; then
      printf 'x' >>"$restore_mount/manifest.json"
    fi
    ;;
  restore-ownership)
    [[ "$mode" != restore_ownership_failure ]] || exit 75
    ;;
  object-restore)
    [[ "$*" == *'PHASE5_RESTORE_OBJECT_DATA'* ]] || exit 64
    ;;
  aistor-license-init)
    [[ "$*" == *'PHASE5_RESTORE_AISTOR_LICENSE'* &&
      "$*" == *'dst=/license-source/minio.license,readonly'* &&
      "$*" == *'dst=/license-target'* &&
      "$*" == *'--cap-drop ALL --cap-add CHOWN --cap-add DAC_READ_SEARCH'* &&
      "$*" != *'DAC_OVERRIDE'* &&
      "$*" != *'FOWNER'* ]] ||
      exit 64
    ;;
  secret-init)
    [[ "$*" == *'--network none'* &&
      "$*" == *'--read-only'* &&
      "$*" == *'--user 0:0'* &&
      "$*" == *'--cap-drop ALL'* &&
      "$*" == *'--cap-add CHOWN'* &&
      "$*" == *'--cap-add DAC_READ_SEARCH'* &&
      "$*" == *'dst=/secret-source,readonly'* &&
      "$*" == *'dst=/secret-target'* &&
      "$*" == *'PHASE5_RESTORE_SECRET_INIT'* &&
      "$*" == *'chmod 0400'* &&
      "$*" == *'chmod 0500'* ]] ||
      exit 64
    ;;
  postgres-restore)
    [[ "$*" == *'volume-subpath=client,readonly'* &&
      "$*" == *'PGPASSFILE=/run/restore-secrets/pgpass'* &&
      "$*" == *' pg_restore '* ]] ||
      exit 64
    ;;
  revoke-sessions)
    [[ "$*" == *'volume-subpath=client,readonly'* &&
      "$*" == *'PGPASSFILE=/run/restore-secrets/pgpass'* ]] ||
      exit 64
    sql="$(arg_after --command "$@")" || exit 64
    [[ "$sql" == "BEGIN; UPDATE sessions "*"restore_verification"*"UPDATE operational_modes "*"mode='normal'"*"owner_id=NULL"*"lease_token_hash=NULL"*"lease_expires_at=NULL"*"entered_at=NULL"*"version=version+1"*"WHERE singleton_id=true; COMMIT; SELECT 'PHASE5_RESTORE_SESSIONS_REVOKED';" ]] ||
      exit 64
    ;;
  restore-check)
    [[ "$*" == *'volume-subpath=restore-check,readonly'* &&
      "$*" == *'set -a; . /run/restore-secrets/runtime.env; set +a; exec /app/happylearn-backup restore-check --backup-id "$1" --report-file /work/restore-check.report'* &&
      "$*" == *"restore-check ${PHASE5_FAKE_BACKUP_ID}"* ]] ||
      exit 64
    report_mount=''
    manifest_mount=''
    previous=''
    for argument in "$@"; do
      if [[ "$previous" == '--mount' &&
        "$argument" == type=bind,src=*,dst=/work ]]; then
        report_mount="${argument#type=bind,src=}"
        report_mount="${report_mount%,dst=/work}"
      fi
      if [[ "$previous" == '--mount' &&
        "$argument" == type=bind,src=*,dst=/run/restore/manifest.json,readonly ]]; then
        manifest_mount="${argument#type=bind,src=}"
        manifest_mount="${manifest_mount%,dst=/run/restore/manifest.json,readonly}"
      fi
      previous="$argument"
    done
    [[ -n "$report_mount" &&
      "$report_mount" == */check-output &&
      -d "$report_mount" &&
      ! -L "$report_mount" &&
      ! -e "$report_mount/cleanup.intent" ]] ||
      exit 64
    [[ -f "$manifest_mount" && ! -L "$manifest_mount" ]] || exit 64
    if command -v sha256sum >/dev/null 2>&1; then
      observed_manifest_sha256="$(
        sha256sum "$manifest_mount" |
          sed -n '1s/[[:space:]].*$//p'
      )"
    elif command -v shasum >/dev/null 2>&1; then
      observed_manifest_sha256="$(
        shasum -a 256 "$manifest_mount" |
          sed -n '1s/[[:space:]].*$//p'
      )"
    else
      exit 64
    fi
    if [[ "$mode" != manifest_hash_mismatch ]]; then
      [[ "$observed_manifest_sha256" == "$PHASE5_FAKE_MANIFEST_SHA256" ]] ||
        exit 64
    fi
    active=0
    missing=0
    unexpected=0
    report_backup_id="$PHASE5_FAKE_BACKUP_ID"
    report_manifest_sha256="$PHASE5_FAKE_MANIFEST_SHA256"
    report_row_count_total=136
    report_evidence_sha256="$PHASE5_FAKE_EVIDENCE_SHA256"
    report_table_users_count=1
    report_table_sessions_count=2
    report_checked_object_count=7
    case "$mode" in
      stale_session) active=1 ;;
      missing_object) missing=1 ;;
      report_wrong_backup)
        report_backup_id='22222222-2222-4222-8222-222222222222'
        ;;
      report_wrong_manifest)
        report_manifest_sha256='ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
        ;;
      report_wrong_evidence)
        report_evidence_sha256='ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
        ;;
      report_row_total_mismatch) report_row_count_total=135 ;;
      report_leading_zero) report_checked_object_count=07 ;;
      report_negative) report_checked_object_count=-1 ;;
      report_int64_overflow)
        report_checked_object_count=9223372036854775808
        ;;
      report_table_sum_overflow)
        report_table_users_count=9223372036854775807
        report_table_sessions_count=1
        report_row_count_total=9223372036854775807
        ;;
    esac
    {
      printf '%s\n' 'schema_version=1'
      [[ "$mode" != report_duplicate_field ]] ||
        printf '%s\n' 'schema_version=1'
      [[ "$mode" != report_unknown_field ]] ||
        printf '%s\n' 'unknown_count=1'
      printf '%s\n' \
        "backup_id=$report_backup_id" \
        "manifest_sha256=$report_manifest_sha256" \
        'migration_version=42' \
        "table_users_count=$report_table_users_count" \
        "table_sessions_count=$report_table_sessions_count" \
        'table_subjects_count=3' \
        'table_grades_count=4' \
        'table_terms_count=5' \
        'table_chapters_count=6' \
        'table_lessons_count=7' \
        'table_lesson_revisions_count=8' \
        'table_files_count=9' \
        'table_file_versions_count=10' \
        'table_file_previews_count=11' \
        'table_qa_threads_count=12' \
        'table_qa_messages_count=13' \
        'table_ai_threads_count=14' \
        'table_ai_messages_count=15'
      [[ "$mode" == report_missing_field ]] ||
        printf '%s\n' 'table_ai_runs_count=16'
      printf '%s\n' \
        "row_count_total=$report_row_count_total" \
        "checked_object_count=$report_checked_object_count" \
        "missing_object_count=$missing" \
        "unexpected_object_count=$unexpected" \
        "active_session_count=$active" \
        "verification_report_sha256=$PHASE5_FAKE_VERIFICATION_REPORT_SHA256" \
        "evidence_sha256=$report_evidence_sha256"
    } >"$report_mount/restore-check.report"
    ;;
  restore-http-probe)
    credential_mount="type=bind,src=$PHASE5_FAKE_TEACHER_CREDENTIAL_FILE,dst=/run/secrets/restore-probe-teacher.json,readonly"
    mount_count=0
    previous=''
    for argument in "$@"; do
      [[ "$argument" != '--env' &&
        "$argument" != '--env-file' &&
        "$argument" != '--publish' &&
        "$argument" != '-p' ]] ||
        exit 64
      if [[ "$previous" == '--mount' ]]; then
        mount_count=$((mount_count + 1))
        [[ "$argument" == "$credential_mount" ]] || exit 64
      fi
      previous="$argument"
    done
    [[ "$name" == "$project-restore-http-probe" &&
      "$mount_count" -eq 1 &&
      "$(arg_after --network "$@")" == "$project-network" &&
      "$(arg_after --user "$@")" == "${PHASE5_FAKE_HOST_UID}:${PHASE5_FAKE_HOST_GID}" &&
      "$(arg_after --entrypoint "$@")" == /usr/bin/timeout &&
      "$*" == *"--entrypoint /usr/bin/timeout $PHASE5_FAKE_BACKUP_IMAGE --foreground --kill-after=10s "* &&
      "$*" == *'--read-only'* &&
      "$*" != *'/run/secrets/pgpass'* &&
      "$*" != *'PGPASSFILE'* &&
      "$*" != *'HAPPYLEARN_DATABASE'* &&
      "$*" != *'HAPPYLEARN_MINIO'* &&
      "$*" != *'/run/restore/manifest'* &&
      "$*" != *'dst=/work'* &&
      -f "$PHASE5_FAKE_TEACHER_CREDENTIAL_FILE" &&
      ! -L "$PHASE5_FAKE_TEACHER_CREDENTIAL_FILE" &&
      "${@: -2:1}" == /app/happylearn-backup &&
      "${@: -1}" == restore-http-probe ]] ||
      exit 64
    if [[ "$mode" == ledger_missing ||
      "$mode" == ledger_empty ||
      "$mode" == ledger_truncated ||
      "$mode" == ledger_reordered ||
      "$mode" == ledger_duplicate ||
      "$mode" == ledger_oversized ]]; then
      : >"$PHASE5_FAKE_LEDGER_PAUSE_MARKER"
      while [[ ! -e "$PHASE5_FAKE_LEDGER_PAUSE_RELEASE" ]]; do
        sleep 0.01
      done
    fi
    if [[ "$mode" == report_race ]]; then
      : >"$PHASE5_FAKE_REPORT_PAUSE_MARKER"
      while [[ ! -e "$PHASE5_FAKE_REPORT_PAUSE_RELEASE" ]]; do
        sleep 0.01
      done
    fi
    if [[ "$mode" == http_probe_failure ]]; then
      exit 75
    fi
    ;;
  *)
    exit 64
    ;;
esac
FAKE_DOCKER
  chmod 0700 "$fixture/bin/docker"
  printf '%s\n' "$fixture"
}

run_fixture() {
  local fixture="$1"
  local mode="${2:-success}"
  local -a fixture_environment=(
    "PATH=$fixture/bin:$PATH"
    "PHASE5_FAKE_DOCKER_LOG=$fixture/docker.log"
    "PHASE5_FAKE_DOCKER_STATE=$fixture/state"
    "PHASE5_FAKE_RANDOM_COUNTER=$fixture/random.counter"
    "PHASE5_FAKE_MODE=$mode"
    "PHASE5_FAKE_BACKUP_ID=$BACKUP_ID"
    "PHASE5_FAKE_BACKUP_IMAGE=$BACKUP_IMAGE"
    "PHASE5_FAKE_SNAPSHOT_ID=$SNAPSHOT_ID"
    "PHASE5_FAKE_SECRET_MARKER=$SECRET_MARKER"
    "PHASE5_FAKE_DATABASE_SECRET_MARKER=$DATABASE_SECRET_MARKER"
    "PHASE5_FAKE_OBJECT_SECRET_MARKER=$OBJECT_SECRET_MARKER"
    "PHASE5_FAKE_SIGNING_SECRET_MARKER=$SIGNING_SECRET_MARKER"
    "PHASE5_FAKE_TEACHER_CREDENTIAL_FILE=$fixture/restore-probe-teacher.json"
    "PHASE5_FAKE_TEACHER_CREDENTIAL_SECRET=$TEACHER_CREDENTIAL_SECRET"
    "PHASE5_FAKE_MANIFEST_TEXT=$MANIFEST_TEXT"
    "PHASE5_FAKE_MANIFEST_SHA256=$MANIFEST_SHA256"
    "PHASE5_FAKE_VERIFICATION_REPORT_SHA256=$VERIFICATION_REPORT_SHA256"
    "PHASE5_FAKE_EVIDENCE_SHA256=$EVIDENCE_SHA256"
    "PHASE5_FAKE_HOST_UID=$(id -u)"
    "PHASE5_FAKE_HOST_GID=$(id -g)"
    "PHASE5_FAKE_EXPECTED_RESTORE_LOCK=$(cd "$fixture/repository" && pwd -P)/.phase5-restore-${BACKUP_ID}.lock"
    "PHASE5_FAKE_EXPECTED_REPORT_LOCK=$(cd "${HAPPYLEARN_RESTORE_REPORT_DIRECTORY:-$fixture/reports}" && pwd -P)/.restore-${BACKUP_ID}.lock"
    "PHASE5_FAKE_FLOCK_RELEASE_FILE=${PHASE5_FAKE_FLOCK_RELEASE_FILE:-}"
    "PHASE5_FAKE_REPORT_FLOCK_RELEASE_FILE=${PHASE5_FAKE_REPORT_FLOCK_RELEASE_FILE:-}"
    "PHASE5_FAKE_WORKSPACE_MARKER=$fixture/workspace.path"
    "PHASE5_FAKE_SIGNAL_MARKER=$fixture/signal.started"
    "PHASE5_FAKE_DELAYED_CREATE_RELEASE=$fixture/delayed-create.release"
    "PHASE5_FAKE_DELAYED_CREATE_FINISHED=$fixture/delayed-create.finished"
    "PHASE5_FAKE_LEDGER_PAUSE_MARKER=$fixture/ledger-pause.started"
    "PHASE5_FAKE_LEDGER_PAUSE_RELEASE=$fixture/ledger-pause.release"
    "PHASE5_FAKE_REPORT_PAUSE_MARKER=$fixture/report-pause.started"
    "PHASE5_FAKE_REPORT_PAUSE_RELEASE=$fixture/report-pause.release"
    "PHASE5_FAKE_SUPERVISOR_DIRECT_PID=$fixture/supervisor-direct.pid"
    "PHASE5_FAKE_SUPERVISOR_DESCENDANT_PID=$fixture/supervisor-descendant.pid"
    "PHASE5_FAKE_SUPERVISOR_DESCENDANT_MARKER=$fixture/supervisor-descendant.started"
    "PHASE5_FAKE_SUPERVISOR_HEARTBEAT=$fixture/supervisor.heartbeat"
    "PHASE5_FAKE_SUPERVISOR_DIRECT_CONT_MARKER=$fixture/supervisor-direct.cont"
    "PHASE5_FAKE_SUPERVISOR_DESCENDANT_CONT_MARKER=$fixture/supervisor-descendant.cont"
    "PHASE5_FAKE_SUPERVISOR_DIRECT_RELEASE=$fixture/supervisor-direct.release"
    "PHASE5_FAKE_SUPERVISOR_DIRECT_EXITING=$fixture/supervisor-direct.exiting"
    "HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY=$fixture/repository"
    "HAPPYLEARN_BACKUP_SECRET_DIRECTORY=$fixture/secrets"
    "HAPPYLEARN_AISTOR_LICENSE_FILE=$fixture/minio.license"
    "HAPPYLEARN_BACKUP_IMAGE=${HAPPYLEARN_BACKUP_IMAGE:-$BACKUP_IMAGE}"
    "HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE=${HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE-$fixture/restore-probe-teacher.json}"
    "HAPPYLEARN_RESTORE_CONTROL_DIRECTORY=${HAPPYLEARN_RESTORE_CONTROL_DIRECTORY:-$fixture/control}"
    "HAPPYLEARN_RESTORE_REPORT_DIRECTORY=${HAPPYLEARN_RESTORE_REPORT_DIRECTORY:-$fixture/reports}"
    "HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS=${HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS:-3}"
    "HAPPYLEARN_RESTORE_READY_TIMEOUT_SECONDS=${HAPPYLEARN_RESTORE_READY_TIMEOUT_SECONDS:-3}"
    'HAPPYLEARN_RESTORE_POLL_INTERVAL_SECONDS=0.01'
  )
  if [[ "${PHASE5_FAKE_EXEC_FIXTURE:-}" == true ]]; then
    exec env "${fixture_environment[@]}" \
      "$TARGET" --backup-id "$BACKUP_ID"
  fi
  env "${fixture_environment[@]}" \
    "$TARGET" --backup-id "$BACKUP_ID"
}

FIXTURE_BACKGROUND_PID=''

start_fixture_background() {
  local fixture="$1"
  local mode="$2"
  PHASE5_FAKE_EXEC_FIXTURE=true \
    run_fixture "$fixture" "$mode" \
      >"$fixture/background.stdout" \
      2>"$fixture/background.stderr" &
  FIXTURE_BACKGROUND_PID="$!"
}

run_tampered_supervisor_case() {
  local mode="$1"
  local expected_status="$2"
  local fixture outer_pid workspace identity_path supervisor_pid
  local direct_pid descendant_pid status=0
  local heartbeat_before heartbeat_after
  local cleanup_started cleanup_finished cleanup_duration
  local direct_alive=false descendant_alive=false failed=false

  fixture="$(make_fixture)"
  if [[ "$mode" == supervisor_identity_timeout ]]; then
    HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS=1 \
      start_fixture_background "$fixture" "$mode"
  else
    HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS=10 \
      start_fixture_background "$fixture" "$mode"
  fi
  outer_pid="$FIXTURE_BACKGROUND_PID"
  ACTIVE_FIXTURE_PID="$outer_pid"
  wait_for_file_long "$fixture/supervisor-descendant.started" ||
    fail "$mode did not reach its bounded command"
  wait_for_file "$fixture/supervisor.heartbeat" ||
    fail "$mode did not publish its command heartbeat"
  wait_for_file "$fixture/workspace.path" ||
    fail "$mode did not expose its private workspace"
  workspace="$(<"$fixture/workspace.path")"
  identity_path="$(
    find "$workspace/control" -type f -name identity -print
  )"
  [[ -n "$identity_path" &&
    "$identity_path" != *$'\n'* &&
    ! -L "$identity_path" ]] ||
    fail "$mode exposed an invalid identity handshake"
  supervisor_pid="$(<"$identity_path")"
  direct_pid="$(<"$fixture/supervisor-direct.pid")"
  descendant_pid="$(<"$fixture/supervisor-descendant.pid")"
  [[ "$supervisor_pid" =~ ^[1-9][0-9]*$ &&
    "$direct_pid" =~ ^[1-9][0-9]*$ &&
    "$descendant_pid" =~ ^[1-9][0-9]*$ ]] ||
    fail "$mode exposed an invalid process identity"
  /bin/ln "$identity_path" \
    "${identity_path}.injected-hardlink"
  cleanup_started="$(contract_time_milliseconds)" ||
    fail "$mode monotonic cleanup clock is unavailable"
  if [[ "$mode" == supervisor_identity_term ]]; then
    kill -TERM "$outer_pid"
  fi
  if wait_for_direct_child "$outer_pid"; then
    status=0
  else
    status=$?
  fi
  ACTIVE_FIXTURE_PID=''
  cleanup_finished="$(contract_time_milliseconds)" ||
    fail "$mode monotonic cleanup clock failed"
  cleanup_duration=$((cleanup_finished - cleanup_started))

  heartbeat_before="$(
    wc -c <"$fixture/supervisor.heartbeat" |
      tr -d '[:space:]'
  )"
  sleep 0.2
  heartbeat_after="$(
    wc -c <"$fixture/supervisor.heartbeat" |
      tr -d '[:space:]'
  )"
  kill -0 "$direct_pid" 2>/dev/null && direct_alive=true
  kill -0 "$descendant_pid" 2>/dev/null && descendant_alive=true
  [[ "$status" -eq "$expected_status" ]] || failed=true
  [[ "$cleanup_duration" -lt "$TIMEOUT_CONTRACT_LIMIT_MILLISECONDS" ]] ||
    failed=true
  [[ "$heartbeat_after" -eq "$heartbeat_before" ]] || failed=true
  [[ "$direct_alive" == false && "$descendant_alive" == false ]] ||
    failed=true
  assert_no_resources "$fixture"
  test ! -e "$fixture/reports/restore-$BACKUP_ID.json" ||
    fail "$mode published a final report"
  test ! -e "$fixture/reports/.restore-${BACKUP_ID}.new" ||
    fail "$mode left a temporary report"
  [[ ! -e "$workspace" ]] ||
    fail "$mode left its private workspace"

  kill -KILL "$direct_pid" "$descendant_pid" 2>/dev/null || true
  wait_for_pid_absent "$direct_pid" || failed=true
  wait_for_pid_absent "$descendant_pid" || failed=true
  [[ "$failed" == false ]] ||
    fail "$mode failed bounded cleanup after identity tampering (status=$status cleanup_duration=${cleanup_duration}ms heartbeat=${heartbeat_before}:${heartbeat_after} direct_alive=$direct_alive descendant_alive=$descendant_alive)"
}

run_timeout_contract_case() {
  local fixture outer_pid started finished duration status=0
  local workspace direct_pid descendant_pid
  local heartbeat_before heartbeat_after
  local direct_alive=false descendant_alive=false failed=false
  fixture="$(make_fixture)"
  HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS=1 \
    start_fixture_background "$fixture" supervisor_identity_timeout
  outer_pid="$FIXTURE_BACKGROUND_PID"
  ACTIVE_FIXTURE_PID="$outer_pid"
  wait_for_file_long "$fixture/supervisor-descendant.started" ||
    fail 'restore timeout did not reach its bounded command'
  wait_for_file "$fixture/supervisor.heartbeat" ||
    fail 'restore timeout did not publish its command heartbeat'
  wait_for_file "$fixture/workspace.path" ||
    fail 'restore timeout did not expose its workspace'
  wait_for_file "$fixture/supervisor-direct.pid" ||
    fail 'restore timeout did not expose its direct command PID'
  wait_for_file "$fixture/supervisor-descendant.pid" ||
    fail 'restore timeout did not expose its descendant command PID'
  started="$(contract_time_milliseconds)" ||
    fail 'restore timeout monotonic clock is unavailable'
  if wait_for_direct_child "$outer_pid"; then
    status=0
  else
    status=$?
  fi
  ACTIVE_FIXTURE_PID=''
  finished="$(contract_time_milliseconds)" ||
    fail 'restore timeout monotonic clock failed'
  duration=$((finished - started))
  workspace="$(<"$fixture/workspace.path")"
  direct_pid="$(<"$fixture/supervisor-direct.pid")"
  descendant_pid="$(<"$fixture/supervisor-descendant.pid")"
  [[ "$direct_pid" =~ ^[1-9][0-9]*$ ]] ||
    fail 'restore timeout exposed an invalid direct command PID'
  [[ "$descendant_pid" =~ ^[1-9][0-9]*$ ]] ||
    fail 'restore timeout exposed an invalid descendant command PID'
  heartbeat_before="$(
    wc -c <"$fixture/supervisor.heartbeat" |
      tr -d '[:space:]'
  )"
  sleep 0.2
  heartbeat_after="$(
    wc -c <"$fixture/supervisor.heartbeat" |
      tr -d '[:space:]'
  )"
  kill -0 "$direct_pid" 2>/dev/null && direct_alive=true
  kill -0 "$descendant_pid" 2>/dev/null && descendant_alive=true
  [[ "$status" -eq 124 ]] || failed=true
  [[ "$duration" -lt "$TIMEOUT_CONTRACT_LIMIT_MILLISECONDS" ]] ||
    failed=true
  [[ "$heartbeat_after" -eq "$heartbeat_before" ]] || failed=true
  [[ "$direct_alive" == false && "$descendant_alive" == false ]] ||
    failed=true
  assert_no_resources "$fixture"
  [[ ! -e "$workspace" ]] || failed=true
  [[ ! -e "$fixture/reports/restore-$BACKUP_ID.json" ]] || failed=true
  [[ ! -e "$fixture/reports/.restore-${BACKUP_ID}.new" ]] || failed=true
  kill -KILL "$direct_pid" "$descendant_pid" 2>/dev/null || true
  wait_for_pid_absent "$direct_pid" || failed=true
  wait_for_pid_absent "$descendant_pid" || failed=true
  [[ "$failed" == false ]] ||
    fail "isolated restore timeout cleanup failed (status=$status duration=${duration}ms heartbeat=${heartbeat_before}:${heartbeat_after} direct_alive=$direct_alive descendant_alive=$descendant_alive)"
  printf 'phase5 restore timeout contract: PASS status=%s duration_ms=%s heartbeat=%s:%s direct_alive=%s descendant_alive=%s resources=0 workspace=0 reports=0\n' \
    "$status" "$duration" "$heartbeat_before" "$heartbeat_after" \
    "$direct_alive" "$descendant_alive"
}

if [[ "${PHASE5_CONTRACT_TIMEOUT_SELF_TEST:-}" == once ]]; then
  test -x "$TARGET" || fail 'restore harness is absent'
  bash -n "$TARGET"
  run_timeout_contract_case
  exit 0
fi

test -x "$TARGET" || fail 'restore harness is absent'
bash -n "$TARGET"
assert_secret_transport_contract() {
  local candidate="$1"
  local initializer file_chmod_line file_chown_line
  local directory_chmod_line directory_chown_line
  if grep -Fq -- '--env-file' "$candidate"; then
    fail 'restore secret entered Docker configured environment'
  fi
  initializer="$(
    sed -n '/^initialize_secret_volume()/,/^}/p' "$candidate"
  )"
  file_chmod_line="$(
    grep -nF 'chmod 0400 "$target"' <<<"$initializer" |
      head -n1 |
      cut -d: -f1
  )"
  file_chown_line="$(
    grep -nF 'chown "$owner" "$target"' <<<"$initializer" |
      head -n1 |
      cut -d: -f1
  )"
  directory_chmod_line="$(
    grep -nF 'chmod 0500 /secret-target' <<<"$initializer" |
      head -n1 |
      cut -d: -f1
  )"
  directory_chown_line="$(
    grep -nF 'chown 1000:0 /secret-target/aistor' <<<"$initializer" |
      head -n1 |
      cut -d: -f1
  )"
  [[ -n "$file_chmod_line" && -n "$file_chown_line" &&
    -n "$directory_chmod_line" && -n "$directory_chown_line" &&
    "$file_chmod_line" -lt "$file_chown_line" &&
    "$directory_chmod_line" -lt "$directory_chown_line" ]] ||
    fail 'restore secret permissions are changed after ownership handoff'
  for literal in \
    'SECRET_VOLUME="$PROJECT-secrets"' \
    'create_volume "$SECRET_VOLUME" secrets' \
    'assert_new_empty_volume "$SECRET_VOLUME" secrets' \
    'initialize_secret_volume' \
    '"$PROJECT-secret-init" secret-init' \
    '--network none' \
    '--read-only' \
    '--user 0:0' \
    '--cap-drop ALL' \
    '--cap-add CHOWN' \
    'type=bind,src=$CONTROL_DIRECTORY,dst=/secret-source,readonly' \
    'type=volume,src=$SECRET_VOLUME,dst=/secret-target' \
    'chmod 0400' \
    'test "$(stat -c %u:%g:%a /secret-target/aistor)" = 1000:0:500' \
    'test "$(stat -c %u:%g:%a /secret-target/app)" = 10001:10001:500' \
    'PHASE5_RESTORE_SECRET_INIT' \
    'type=volume,src=$SECRET_VOLUME,dst=/run/restore-secrets,volume-subpath=postgres,readonly' \
    'type=volume,src=$SECRET_VOLUME,dst=/run/restore-secrets,volume-subpath=aistor,readonly' \
    'type=volume,src=$SECRET_VOLUME,dst=/run/restore-secrets,volume-subpath=app,readonly' \
    'type=volume,src=$SECRET_VOLUME,dst=/run/restore-secrets,volume-subpath=client,readonly' \
    'type=volume,src=$SECRET_VOLUME,dst=/run/restore-secrets,volume-subpath=restore-check,readonly' \
    'set -a; . /run/restore-secrets/runtime.env; set +a; exec'; do
    grep -Fq -- "$literal" "$candidate" ||
      fail "restore secret transport omitted: $literal"
  done
}
assert_secret_transport_contract "$TARGET"
if [[ "${PHASE5_CONTRACT_SECRET_TRANSPORT_SELF_TEST:-}" == true ]]; then
  exit 0
fi
run_secret_transport_mutant() {
  local name="$1"
  local expression="$2"
  local expected_failure="$3"
  local mutant="$CONTRACT_TEMP_ROOT/phase5-restore-$name.sh"
  local stdout="$CONTRACT_TEMP_ROOT/phase5-restore-$name.stdout"
  local stderr="$CONTRACT_TEMP_ROOT/phase5-restore-$name.stderr"
  sed "$expression" "$TARGET" >"$mutant"
  chmod 0700 "$mutant"
  cmp -s "$TARGET" "$mutant" &&
    fail "restore secret transport mutant was not applied: $name"
  if PHASE5_RESTORE_CONTRACT_TARGET="$mutant" \
    PHASE5_CONTRACT_SECRET_TRANSPORT_SELF_TEST=true \
    bash "$0" >"$stdout" 2>"$stderr"; then
    fail "restore secret transport mutant survived: $name"
  fi
  grep -Fq "$expected_failure" "$stderr" ||
    fail "restore secret transport mutant failed for the wrong reason: $name"
}
run_secret_transport_mutant \
  env-file \
  's@--mount "type=volume,src=$SECRET_VOLUME,dst=/run/restore-secrets,volume-subpath=app,readonly"@--env-file "$APP_ENV_FILE"@' \
  'restore secret entered Docker configured environment'
run_secret_transport_mutant \
  writable-app-secret \
  's@volume-subpath=app,readonly@volume-subpath=app@' \
  'restore secret transport omitted: type=volume,src=$SECRET_VOLUME,dst=/run/restore-secrets,volume-subpath=app,readonly'
run_secret_transport_mutant \
  file-chown-before-chmod \
  's@chmod 0400 "$target"@chown "$owner" "$target"; chmod 0400 "$target"@' \
  'restore secret permissions are changed after ownership handoff'
run_secret_transport_mutant \
  directory-chown-before-chmod \
  's@chmod 0500 /secret-target@chown 1000:0 /secret-target/aistor; chmod 0500 /secret-target@' \
  'restore secret permissions are changed after ownership handoff'
if grep -Eq \
  '^[[:space:]]*trap[[:space:]]+cleanup_contract[[:space:]]+EXIT[[:space:]]+HUP' \
  "$0"; then
  fail 'contract signal traps reuse the ordinary EXIT cleanup handler'
fi
contract_signal_handler_source="$(
  sed -n '/^handle_contract_signal()/,/^}/p' "$0"
)"
grep -Fq "trap '' HUP INT TERM" <<<"$contract_signal_handler_source" ||
  fail 'contract signal cleanup does not ignore repeated signals'
grep -Fq 'handle_contract_signal 129' "$0" ||
  fail 'contract HUP trap does not preserve 128+signal status'
grep -Fq 'handle_contract_signal 130' "$0" ||
  fail 'contract INT trap does not preserve 128+signal status'
grep -Fq 'handle_contract_signal 143' "$0" ||
  fail 'contract TERM trap does not preserve 128+signal status'
grep -Fq 'active_fixture_is_direct_job' "$0" ||
  fail 'contract cleanup does not verify the active direct job'
grep -Fq 'mkfifo "$supervisor_guard"' "$TARGET" ||
  fail 'bounded commands have no inherited descendant liveness guard'
grep -Fq 'publish_supervisor_handshake' "$TARGET" ||
  fail 'bounded commands have no owner-only status/ack handshake'
grep -Fq 'direct_running_job "$pid"' "$TARGET" ||
  fail 'negative PGID signaling is not fenced by the direct supervisor job'
grep -Fq 'stat --version' "$TARGET" ||
  fail 'restore harness does not prefer GNU stat explicitly'
grep -Fq -- '--cpus "$CONTAINER_CPUS"' "$TARGET" ||
  fail 'restore containers do not share an explicit CPU budget'
grep -Fq -- '--memory-swap "$CONTAINER_MEMORY_SWAP"' "$TARGET" ||
  fail 'restore containers do not cap swap'
grep -Fq -- '--pids-limit "$CONTAINER_PIDS_LIMIT"' "$TARGET" ||
  fail 'restore containers do not cap process count'
grep -Fq -- '--tmpfs /var/run/postgresql:rw,noexec,nosuid,size=8m' "$TARGET" ||
  fail 'PostgreSQL socket directory is not tmpfs-backed'
grep -Fq \
  'RESTORE_LOCK_FILE="$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY/.phase5-restore-${BACKUP_ID}.lock"' \
  "$TARGET" ||
  fail 'restore lock is not shared through the backup repository'
grep -Fq \
  'type=bind,src=$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY,dst=/repository"' \
  "$TARGET" ||
  fail 'restic repository is not mounted read-write for internal locks'
if grep -Fq \
  'type=bind,src=$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY,dst=/repository,readonly' \
  "$TARGET"; then
  fail 'restic repository is read-only while internal locks remain enabled'
fi
if grep -Eq 'restic[[:space:]]+[^[:cntrl:]]*--no-lock' "$TARGET"; then
  fail 'read-only repository bypassed restic locks without proven exclusion'
fi
grep -Fq 'cleanup.intent' "$TARGET" ||
  fail 'restore harness has no cleanup intent ledger'
grep -Fq 'CHECK_OUTPUT_DIRECTORY="$WORK_DIRECTORY/check-output"' "$TARGET" ||
  fail 'restore-check output has no independent private directory'
grep -Fq \
  'type=bind,src=$CHECK_OUTPUT_DIRECTORY,dst=/work' "$TARGET" ||
  fail 'restore-check output directory is not mounted independently'
if grep -Fq 'type=bind,src=$CONTROL_DIRECTORY,dst=/work' "$TARGET"; then
  fail 'restore-check can mutate its cleanup control directory'
fi
grep -Fq -- '--report-file /work/restore-check.report' "$TARGET" ||
  fail 'restore-check report does not target its independent output directory'
grep -Fq \
  'type=bind,src=$RESTORED_MANIFEST,dst=/run/restore/manifest.json,readonly' \
  "$TARGET" ||
  fail 'restore-check does not receive the verified manifest read-only'
grep -Fq "'HAPPYLEARN_MINIO_USE_TLS=false'" "$TARGET" ||
  fail 'restore-check does not explicitly disable TLS for minio:9000'
grep -Fq 'verify_bound_evidence_sha256' "$TARGET" ||
  fail 'restore report evidence is not independently recomputed'
grep -Fq \
  'REPORT_LOCK_FILE="$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/.restore-${BACKUP_ID}.lock"' \
  "$TARGET" ||
  fail 'restore report publication has no shared directory lock'
grep -Fq 'ln "$REPORT_TEMPORARY" "$REPORT_FILE"' "$TARGET" ||
  fail 'restore report publication is not atomic and no-clobber'
if grep -Fq 'mv "$REPORT_TEMPORARY" "$REPORT_FILE"' "$TARGET"; then
  fail 'restore report publication can overwrite a raced final artifact'
fi
grep -Fq "docker container ls --all --format '{{.Names}}'" "$TARGET" ||
  fail 'container orphan listing does not request canonical names'
grep -Fq "docker volume ls --format '{{.Name}}'" "$TARGET" ||
  fail 'volume orphan listing does not request canonical names'
grep -Fq "docker network ls --format '{{.Name}}'" "$TARGET" ||
  fail 'network orphan listing does not request canonical names'
grep -Fq '{{.Id}}|{{index .Config.Labels' "$TARGET" ||
  fail 'container inspection omits immutable identity'
grep -Fq '{{.Id}}|{{index .Labels' "$TARGET" ||
  fail 'network inspection omits immutable identity'
grep -Fq '{{.Name}}|{{index .Labels' "$TARGET" ||
  fail 'volume inspection omits canonical identity'
grep -Fq 'label=$BACKUP_LABEL=$BACKUP_ID' "$TARGET" ||
  fail 'restore orphan reaper lacks the exact backup label'
grep -Fq 'owned_resources_absent' "$TARGET" ||
  fail 'restore report is not fenced on zero owned resources'
grep -Fq 'HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE' "$TARGET" ||
  fail 'restore harness does not require a teacher credential file'
grep -Fq \
  'type=bind,src=$HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE,dst=/run/secrets/restore-probe-teacher.json,readonly' \
  "$TARGET" ||
  fail 'restore HTTP probe lacks its exact read-only teacher credential mount'
grep -Fq '/app/happylearn-backup restore-http-probe' "$TARGET" ||
  fail 'restore harness does not invoke the fixed HTTP isolation probe'
if grep -Fq -- '--student-isolation-probe' "$TARGET"; then
  fail 'restore harness retained the legacy fake student isolation probe'
fi
grep -Fq 'owner_only_secret "$HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE"' \
  "$TARGET" ||
  fail 'restore harness does not enforce the teacher credential ownership contract'
grep -Fq 'HTTP_PROBE_SUCCEEDED=true' "$TARGET" ||
  fail 'restore harness does not fence its report on HTTP probe success'
grep -Fq 'UPDATE operational_modes' "$TARGET" ||
  fail 'restored operational mode is not normalized before app startup'
grep -Fq '/proc/1/comm' "$TARGET" ||
  fail 'PostgreSQL readiness does not wait for the final server process'
grep -Fq 'psql --no-psqlrc' "$TARGET" ||
  fail 'PostgreSQL readiness does not prove a final-server SQL query'

run_tampered_supervisor_case supervisor_identity_timeout 124
run_tampered_supervisor_case supervisor_identity_term 143

parent_termination_source="$(
  sed -n \
    -e '/^terminate_external_pid()/,/^}/p' \
    -e '/^terminate_external_group()/,/^}/p' \
    "$TARGET"
)"
if grep -Eq \
  'kill[[:space:]]+-[^[:space:]]+[[:space:]]+--[[:space:]]+"?-\$' \
  <<<"$parent_termination_source"; then
  fail 'parent termination still signals a negative PGID'
fi
grep -Fq 'terminate_external_pid "$pid"' <<<"$parent_termination_source" ||
  fail 'parent termination does not fall back to its direct supervisor PID'
grep -Fq 'kill -TERM "$pid"' <<<"$parent_termination_source" ||
  fail 'parent termination does not signal its direct supervisor PID'

if [[ "${PHASE5_RESTORE_SUPERVISOR_FOCUSED:-}" == true ]]; then
  printf '%s\n' 'phase5 restore supervisor contract: PASS'
  exit 0
fi

contract_signal_fixture="$(mktemp -d "$CONTRACT_TEMP_ROOT/contract-signal.XXXXXX")"
contract_signal_ready="$contract_signal_fixture/ready"
contract_signal_root="$contract_signal_fixture/root"
contract_signal_child="$contract_signal_fixture/child"
contract_signal_handler="$contract_signal_fixture/handler"
PHASE5_CONTRACT_SIGNAL_SELF_TEST=true \
  PHASE5_CONTRACT_SIGNAL_MARKER="$contract_signal_ready" \
  PHASE5_CONTRACT_SIGNAL_ROOT_MARKER="$contract_signal_root" \
  PHASE5_CONTRACT_SIGNAL_CHILD_MARKER="$contract_signal_child" \
  PHASE5_CONTRACT_SIGNAL_HANDLER_MARKER="$contract_signal_handler" \
  bash "$0" >/dev/null 2>&1 &
contract_signal_pid="$!"
ACTIVE_FIXTURE_PID="$contract_signal_pid"
wait_for_file "$contract_signal_ready" ||
  fail 'contract signal self-test did not start'
wait_for_file "$contract_signal_root" ||
  fail 'contract signal self-test did not expose its temporary root'
wait_for_file "$contract_signal_child" ||
  fail 'contract signal self-test did not start its cleanup child'
contract_signal_temp_root="$(<"$contract_signal_root")"
contract_signal_child_pid="$(<"$contract_signal_child")"
kill -TERM "$contract_signal_pid"
wait_for_file "$contract_signal_handler" ||
  fail 'contract signal handler did not begin cleanup'
kill -TERM "$contract_signal_pid" 2>/dev/null || true
contract_signal_status=0
if wait_for_direct_child "$contract_signal_pid"; then
  fail 'double-TERM contract self-test unexpectedly succeeded'
else
  contract_signal_status=$?
fi
ACTIVE_FIXTURE_PID=''
[[ "$contract_signal_status" -eq 143 ]] ||
  fail "double-TERM contract exited with $contract_signal_status"
[[ ! -e "$contract_signal_temp_root" ]] ||
  fail 'double-TERM contract left its temporary root'
wait_for_pid_absent "$contract_signal_child_pid" ||
  fail 'double-TERM contract left its active child'

unsafe_license_mode_fixture="$(make_fixture)"
chmod 0600 "$unsafe_license_mode_fixture/minio.license"
if run_fixture "$unsafe_license_mode_fixture" \
  >"$unsafe_license_mode_fixture/stdout" \
  2>"$unsafe_license_mode_fixture/stderr"; then
  fail 'group-readable AIStor license was accepted'
fi
test ! -s "$unsafe_license_mode_fixture/docker.log" ||
  fail 'unsafe AIStor license accessed Docker'

oversized_license_fixture="$(make_fixture)"
chmod 0600 "$oversized_license_fixture/minio.license"
dd if=/dev/zero of="$oversized_license_fixture/minio.license" \
  bs=4097 count=1 >/dev/null 2>&1
chmod 0400 "$oversized_license_fixture/minio.license"
if run_fixture "$oversized_license_fixture" \
  >"$oversized_license_fixture/stdout" \
  2>"$oversized_license_fixture/stderr"; then
  fail 'oversized AIStor license was accepted'
fi
test ! -s "$oversized_license_fixture/docker.log" ||
  fail 'oversized AIStor license accessed Docker'

unsafe_credential_mode_fixture="$(make_fixture)"
chmod 0600 "$unsafe_credential_mode_fixture/restore-probe-teacher.json"
if run_fixture "$unsafe_credential_mode_fixture" \
  >"$unsafe_credential_mode_fixture/stdout" \
  2>"$unsafe_credential_mode_fixture/stderr"; then
  fail 'group-readable restore teacher credential was accepted'
fi
test ! -s "$unsafe_credential_mode_fixture/docker.log" ||
  fail 'unsafe restore teacher credential accessed Docker'

empty_credential_fixture="$(make_fixture)"
chmod 0600 "$empty_credential_fixture/restore-probe-teacher.json"
: >"$empty_credential_fixture/restore-probe-teacher.json"
chmod 0400 "$empty_credential_fixture/restore-probe-teacher.json"
if run_fixture "$empty_credential_fixture" \
  >"$empty_credential_fixture/stdout" \
  2>"$empty_credential_fixture/stderr"; then
  fail 'empty restore teacher credential was accepted'
fi
test ! -s "$empty_credential_fixture/docker.log" ||
  fail 'empty restore teacher credential accessed Docker'

oversized_credential_fixture="$(make_fixture)"
chmod 0600 "$oversized_credential_fixture/restore-probe-teacher.json"
dd if=/dev/zero \
  of="$oversized_credential_fixture/restore-probe-teacher.json" \
  bs=4097 count=1 >/dev/null 2>&1
chmod 0400 "$oversized_credential_fixture/restore-probe-teacher.json"
if run_fixture "$oversized_credential_fixture" \
  >"$oversized_credential_fixture/stdout" \
  2>"$oversized_credential_fixture/stderr"; then
  fail 'oversized restore teacher credential was accepted'
fi
test ! -s "$oversized_credential_fixture/docker.log" ||
  fail 'oversized restore teacher credential accessed Docker'

symlink_credential_fixture="$(make_fixture)"
mv "$symlink_credential_fixture/restore-probe-teacher.json" \
  "$symlink_credential_fixture/restore-probe-teacher.target"
ln -s "$symlink_credential_fixture/restore-probe-teacher.target" \
  "$symlink_credential_fixture/restore-probe-teacher.json"
if run_fixture "$symlink_credential_fixture" \
  >"$symlink_credential_fixture/stdout" \
  2>"$symlink_credential_fixture/stderr"; then
  fail 'symlinked restore teacher credential was accepted'
fi
test ! -s "$symlink_credential_fixture/docker.log" ||
  fail 'symlinked restore teacher credential accessed Docker'

relative_credential_fixture="$(make_fixture)"
if HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE=restore-probe-teacher.json \
  run_fixture "$relative_credential_fixture" \
  >"$relative_credential_fixture/stdout" \
  2>"$relative_credential_fixture/stderr"; then
  fail 'relative restore teacher credential path was accepted'
fi
test ! -s "$relative_credential_fixture/docker.log" ||
  fail 'relative restore teacher credential accessed Docker'

directory_credential_fixture="$(make_fixture)"
if HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE="$directory_credential_fixture/control" \
  run_fixture "$directory_credential_fixture" \
  >"$directory_credential_fixture/stdout" \
  2>"$directory_credential_fixture/stderr"; then
  fail 'directory restore teacher credential was accepted'
fi
test ! -s "$directory_credential_fixture/docker.log" ||
  fail 'directory restore teacher credential accessed Docker'

wrong_owner_credential_fixture="$(make_fixture)"
if run_fixture "$wrong_owner_credential_fixture" credential_wrong_owner \
  >"$wrong_owner_credential_fixture/stdout" \
  2>"$wrong_owner_credential_fixture/stderr"; then
  fail 'non-owner restore teacher credential was accepted'
fi
test ! -s "$wrong_owner_credential_fixture/docker.log" ||
  fail 'non-owner restore teacher credential accessed Docker'

missing_credential_fixture="$(make_fixture)"
if HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE='' \
  run_fixture "$missing_credential_fixture" \
  >"$missing_credential_fixture/stdout" \
  2>"$missing_credential_fixture/stderr"; then
  fail 'missing restore teacher credential was accepted'
fi
test ! -s "$missing_credential_fixture/docker.log" ||
  fail 'missing restore teacher credential accessed Docker'

invalid_fixture="$(make_fixture)"
if "$TARGET" --backup-id 'NOT-A-CANONICAL-UUID' \
  >"$invalid_fixture/stdout" 2>"$invalid_fixture/stderr"; then
  fail 'invalid backup ID was accepted'
fi
test ! -s "$invalid_fixture/docker.log" ||
  fail 'invalid backup ID accessed Docker'
if "$TARGET" --backup-id "$BACKUP_ID" --backup-id "$BACKUP_ID" \
  >/dev/null 2>&1; then
  fail 'duplicate backup ID was accepted'
fi
unsafe_image_fixture="$(make_fixture)"
if HAPPYLEARN_BACKUP_IMAGE='--privileged' \
  run_fixture "$unsafe_image_fixture" \
  >"$unsafe_image_fixture/stdout" 2>"$unsafe_image_fixture/stderr"; then
  fail 'unsafe restore image reference was accepted'
fi
test ! -s "$unsafe_image_fixture/docker.log" ||
  fail 'unsafe restore image reference accessed Docker'
workspace_fixture="$(make_fixture)"
if run_fixture "$workspace_fixture" workspace_failure \
  >"$workspace_fixture/stdout" 2>"$workspace_fixture/stderr"; then
  fail 'restore workspace initialization failure was accepted'
fi
workspace_path="$(<"$workspace_fixture/workspace.path")"
[[ -n "$workspace_path" && ! -e "$workspace_path" ]] ||
  fail 'failed restore workspace was not cleaned'
test ! -s "$workspace_fixture/docker.log" ||
  fail 'workspace initialization failure accessed Docker'

for occupied_supervisor_fd in 7 8 9; do
  occupied_fd_fixture="$(make_fixture)"
  occupied_fd_sentinel="$occupied_fd_fixture/fd-${occupied_supervisor_fd}.sentinel"
  printf 'fd-%s\n' "$occupied_supervisor_fd" >"$occupied_fd_sentinel"
  case "$occupied_supervisor_fd" in
    7)
      (
        exec 7<"$occupied_fd_sentinel"
        if run_fixture "$occupied_fd_fixture" success; then
          exit 70
        fi
        IFS= read -r occupied_fd_value <&7
        [[ "$occupied_fd_value" == fd-7 ]]
      ) >/dev/null 2>&1 ||
        fail 'pre-opened fd7 was accepted or altered'
      ;;
    8)
      (
        exec 8<"$occupied_fd_sentinel"
        if run_fixture "$occupied_fd_fixture" success; then
          exit 70
        fi
        IFS= read -r occupied_fd_value <&8
        [[ "$occupied_fd_value" == fd-8 ]]
      ) >/dev/null 2>&1 ||
        fail 'pre-opened fd8 was accepted or altered'
      ;;
    9)
      (
        exec 9<"$occupied_fd_sentinel"
        if run_fixture "$occupied_fd_fixture" success; then
          exit 70
        fi
        IFS= read -r occupied_fd_value <&9
        [[ "$occupied_fd_value" == fd-9 ]]
      ) >/dev/null 2>&1 ||
        fail 'pre-opened fd9 was accepted or altered'
      ;;
  esac
  test ! -s "$occupied_fd_fixture/docker.log" ||
    fail "pre-opened fd$occupied_supervisor_fd accessed Docker"
  cmp -s "$occupied_fd_sentinel" \
    <(printf 'fd-%s\n' "$occupied_supervisor_fd") ||
    fail "pre-opened fd$occupied_supervisor_fd changed its sentinel"
done

for supervisor_handshake in ready identity status ack; do
  hardlink_fixture="$(make_fixture)"
  hardlink_mode="supervisor_${supervisor_handshake}_hardlink"
  if run_fixture "$hardlink_fixture" "$hardlink_mode" \
    >"$hardlink_fixture/stdout" 2>"$hardlink_fixture/stderr"; then
    fail "hardlinked supervisor $supervisor_handshake handshake was accepted"
  fi
  assert_no_resources "$hardlink_fixture"
  test ! -e "$hardlink_fixture/reports/restore-$BACKUP_ID.json" ||
    fail "hardlinked supervisor $supervisor_handshake published a final report"
  test ! -e "$hardlink_fixture/reports/.restore-${BACKUP_ID}.new" ||
    fail "hardlinked supervisor $supervisor_handshake left a temporary report"
  if [[ -f "$hardlink_fixture/workspace.path" ]]; then
    hardlink_workspace="$(<"$hardlink_fixture/workspace.path")"
    [[ -n "$hardlink_workspace" && ! -e "$hardlink_workspace" ]] ||
      fail "hardlinked supervisor $supervisor_handshake left its workspace"
  fi
done

midrun_identity_fixture="$(make_fixture)"
start_fixture_background \
  "$midrun_identity_fixture" supervisor_identity_midrun
midrun_identity_pid="$FIXTURE_BACKGROUND_PID"
ACTIVE_FIXTURE_PID="$midrun_identity_pid"
wait_for_file_long \
  "$midrun_identity_fixture/supervisor-descendant.started" ||
  fail 'mid-run identity fixture did not reach the bounded command'
wait_for_file "$midrun_identity_fixture/workspace.path" ||
  fail 'mid-run identity fixture did not expose its private workspace'
midrun_identity_workspace="$(<"$midrun_identity_fixture/workspace.path")"
midrun_identity_path="$(
  find "$midrun_identity_workspace/control" \
    -type f -name identity -print
)"
[[ -n "$midrun_identity_path" &&
  "$midrun_identity_path" != *$'\n'* &&
  ! -L "$midrun_identity_path" ]] ||
  fail 'mid-run identity fixture exposed an invalid identity handshake'
/bin/ln "$midrun_identity_path" \
  "${midrun_identity_path}.injected-hardlink"
touch "$midrun_identity_fixture/supervisor-direct.release"
midrun_identity_status=0
if wait_for_direct_child "$midrun_identity_pid"; then
  fail 'mid-run hardlinked supervisor identity was accepted'
else
  midrun_identity_status=$?
fi
ACTIVE_FIXTURE_PID=''
[[ "$midrun_identity_status" -ne 0 ]] ||
  fail 'mid-run hardlinked supervisor identity returned success'
assert_no_resources "$midrun_identity_fixture"
test ! -e "$midrun_identity_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'mid-run hardlinked supervisor identity published a final report'
test ! -e \
  "$midrun_identity_fixture/reports/.restore-${BACKUP_ID}.new" ||
  fail 'mid-run hardlinked supervisor identity left a temporary report'
[[ ! -e "$midrun_identity_workspace" ]] ||
  fail 'mid-run hardlinked supervisor identity left its workspace'

grep -Fq "gnu) stat -c '%a|%u|%s|%h|%i'" "$TARGET" ||
  fail 'GNU supervisor metadata omits link count or inode'
grep -Fq "bsd) stat -f '%Lp|%u|%z|%l|%i'" "$TARGET" ||
  fail 'BSD supervisor metadata omits link count or inode'
grep -Fq 'supervisor_identity_matches "$pid" "$pgid"' "$TARGET" ||
  fail 'supervisor identity is not revalidated before group termination'

supervisor_fixture="$(make_fixture)"
start_fixture_background "$supervisor_fixture" supervisor_descendant
supervisor_target_pid="$FIXTURE_BACKGROUND_PID"
ACTIVE_FIXTURE_PID="$supervisor_target_pid"
if ! wait_for_file_long "$supervisor_fixture/supervisor-descendant.started"; then
  supervisor_early_status=0
  wait "$supervisor_target_pid" || supervisor_early_status=$?
  ACTIVE_FIXTURE_PID=''
  printf 'supervisor fixture early status: %s\n' \
    "$supervisor_early_status" >&2
  tail -n 80 "$supervisor_fixture/background.stderr" >&2
  fail 'supervisor descendant fixture did not start'
fi
supervisor_direct_pid="$(<"$supervisor_fixture/supervisor-direct.pid")"
supervisor_descendant_pid="$(<"$supervisor_fixture/supervisor-descendant.pid")"
[[ "$supervisor_direct_pid" =~ ^[1-9][0-9]*$ &&
  "$supervisor_descendant_pid" =~ ^[1-9][0-9]*$ ]] ||
  fail 'fake direct command or descendant exposed an invalid PID'
wait_for_file "$supervisor_fixture/workspace.path" ||
  fail 'supervisor fixture did not expose its private workspace'
supervisor_workspace="$(<"$supervisor_fixture/workspace.path")"
supervisor_identity_paths="$(
  find "$supervisor_workspace/control" \
    -type f -name identity -print
)"
[[ -n "$supervisor_identity_paths" &&
  "$supervisor_identity_paths" != *$'\n'* &&
  ! -L "$supervisor_identity_paths" ]] ||
  fail 'supervisor did not expose one regular identity handshake'
supervisor_pid="$(<"$supervisor_identity_paths")"
[[ "$supervisor_pid" =~ ^[1-9][0-9]*$ &&
  "$supervisor_pid" != "$supervisor_direct_pid" &&
  "$supervisor_pid" != "$supervisor_descendant_pid" ]] ||
  fail 'supervisor identity was invalid or reused a command PID'
(
  trap 'exit 0' HUP INT TERM
  while :; do
    sleep 0.05
  done
) &
supervisor_canary_pid="$!"
active_fixture_is_direct_job "$supervisor_canary_pid" ||
  fail 'supervisor canary was not a direct contract job'
touch "$supervisor_fixture/supervisor-direct.release"
wait_for_file "$supervisor_fixture/supervisor-direct.exiting" ||
  fail 'supervisor direct command did not reach exit'
wait_for_pid_absent "$supervisor_direct_pid" ||
  fail 'supervisor direct command did not actually exit'
kill -0 "$supervisor_descendant_pid" 2>/dev/null ||
  fail 'supervisor descendant exited before bounded cleanup'
kill -TERM "$supervisor_target_pid"
supervisor_status=0
if wait_for_direct_child "$supervisor_target_pid"; then
  fail 'signaled descendant supervisor unexpectedly succeeded'
else
  supervisor_status=$?
fi
ACTIVE_FIXTURE_PID=''
if [[ "$supervisor_status" -ne 143 ]]; then
  grep -E \
    'handle_restore_signal|cleanup_restore|cleanup_ledger_valid|status=|return 143|exit 143|return 1' \
    "$supervisor_fixture/background.stderr" |
    tail -n 160 >&2 ||
    true
  fail "descendant supervisor exited with $supervisor_status instead of 143"
fi
wait_for_pid_absent "$supervisor_descendant_pid" ||
  fail 'same-PGID TERM-ignoring descendant survived supervisor cleanup'
active_fixture_is_direct_job "$supervisor_canary_pid" ||
  fail 'different-PGID canary was killed by supervisor cleanup'
kill -TERM "$supervisor_canary_pid"
wait "$supervisor_canary_pid" 2>/dev/null || true
assert_no_resources "$supervisor_fixture"
test ! -e "$supervisor_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'signaled descendant supervisor published a final report'
test ! -e "$supervisor_fixture/reports/.restore-${BACKUP_ID}.new" ||
  fail 'signaled descendant supervisor left a temporary report'
[[ ! -e "$supervisor_workspace" ]] ||
  fail 'signaled descendant supervisor left its private workspace'

same_directory_fixture="$(make_fixture)"
if HAPPYLEARN_RESTORE_CONTROL_DIRECTORY="$same_directory_fixture/reports" \
  run_fixture "$same_directory_fixture" \
  >"$same_directory_fixture/stdout" 2>"$same_directory_fixture/stderr"; then
  fail 'restore accepted a shared control/report directory'
fi
test ! -s "$same_directory_fixture/docker.log" ||
  fail 'shared control/report directory accessed Docker'

shared_lock_fixture="$(make_fixture)"
mkdir "$shared_lock_fixture/control-one" "$shared_lock_fixture/control-two"
chmod 0700 \
  "$shared_lock_fixture/control-one" "$shared_lock_fixture/control-two"
shared_lock_release="$shared_lock_fixture/shared-lock.release"
HAPPYLEARN_RESTORE_CONTROL_DIRECTORY="$shared_lock_fixture/control-one" \
  PHASE5_FAKE_FLOCK_RELEASE_FILE="$shared_lock_release" \
  start_fixture_background "$shared_lock_fixture" success
shared_lock_pid="$FIXTURE_BACKGROUND_PID"
ACTIVE_FIXTURE_PID="$shared_lock_pid"
wait_for_file "${shared_lock_release}.started" ||
  fail 'shared repository lock holder did not start'
if HAPPYLEARN_RESTORE_CONTROL_DIRECTORY="$shared_lock_fixture/control-two" \
  run_fixture "$shared_lock_fixture"; then
  fail 'different control directories bypassed the shared repository lock'
fi
test ! -s "$shared_lock_fixture/docker.log" ||
  fail 'shared repository lock rejection accessed Docker'
touch "$shared_lock_release"
if ! wait_for_direct_child "$shared_lock_pid"; then
  fail 'shared repository lock holder failed after release'
fi
ACTIVE_FIXTURE_PID=''
assert_no_resources "$shared_lock_fixture"

report_lock_fixture_one="$(make_fixture)"
report_lock_fixture_two="$(make_fixture)"
shared_report_directory="$CONTRACT_TEMP_ROOT/shared-reports"
mkdir "$shared_report_directory"
chmod 0700 "$shared_report_directory"
report_lock_release="$CONTRACT_TEMP_ROOT/report-lock.release"
HAPPYLEARN_RESTORE_REPORT_DIRECTORY="$shared_report_directory" \
  PHASE5_FAKE_REPORT_FLOCK_RELEASE_FILE="$report_lock_release" \
  start_fixture_background "$report_lock_fixture_one" success
report_lock_pid="$FIXTURE_BACKGROUND_PID"
ACTIVE_FIXTURE_PID="$report_lock_pid"
wait_for_file "${report_lock_release}.started" ||
  fail 'shared report lock holder did not start'
if HAPPYLEARN_RESTORE_REPORT_DIRECTORY="$shared_report_directory" \
  run_fixture "$report_lock_fixture_two"; then
  fail 'different repositories bypassed the shared report lock'
fi
test ! -s "$report_lock_fixture_two/docker.log" ||
  fail 'shared report lock rejection accessed Docker'
touch "$report_lock_release"
wait_for_direct_child "$report_lock_pid" ||
  fail 'shared report lock holder failed after release'
ACTIVE_FIXTURE_PID=''
assert_no_resources "$report_lock_fixture_one"
test -f "$shared_report_directory/restore-$BACKUP_ID.json" ||
  fail 'shared report lock holder did not publish after release'

success_fixture="$(make_fixture)"
if ! run_fixture "$success_fixture" modern_resource_not_found \
  >"$success_fixture/stdout" 2>"$success_fixture/stderr"; then
  sed -n '1,120p' "$success_fixture/stdout" >&2
  sed -n '1,120p' "$success_fixture/stderr" >&2
  sed -n '1,240p' "$success_fixture/docker.log" >&2
  find "$success_fixture/state" -mindepth 1 -maxdepth 2 -print >&2
  fail 'strict success restore fixture failed'
fi
assert_no_resources "$success_fixture"

preexisting_report_fixture="$(make_fixture)"
preexisting_report="$preexisting_report_fixture/reports/restore-$BACKUP_ID.json"
printf '%s\n' sentinel >"$preexisting_report"
chmod 0600 "$preexisting_report"
if run_fixture "$preexisting_report_fixture"; then
  fail 'preexisting final report was accepted'
fi
grep -Fxq sentinel "$preexisting_report" ||
  fail 'preexisting final report was replaced'
test ! -s "$preexisting_report_fixture/docker.log" ||
  fail 'preexisting final report rejection accessed Docker'

report_race_fixture="$(make_fixture)"
start_fixture_background "$report_race_fixture" report_race
report_race_pid="$FIXTURE_BACKGROUND_PID"
ACTIVE_FIXTURE_PID="$report_race_pid"
wait_for_file_long "$report_race_fixture/report-pause.started" ||
  fail 'report race fixture did not reach its final external probe'
report_race_final="$report_race_fixture/reports/restore-$BACKUP_ID.json"
printf '%s\n' sentinel >"$report_race_final"
chmod 0600 "$report_race_final"
touch "$report_race_fixture/report-pause.release"
if wait_for_direct_child "$report_race_pid"; then
  fail 'raced final report was accepted'
fi
ACTIVE_FIXTURE_PID=''
assert_no_resources "$report_race_fixture"
grep -Fxq sentinel "$report_race_final" ||
  fail 'raced final report was replaced'
test ! -e "$report_race_fixture/reports/.restore-${BACKUP_ID}.new" ||
  fail 'raced final report left a temporary artifact'

manifest_mismatch_fixture="$(make_fixture)"
if run_fixture "$manifest_mismatch_fixture" manifest_hash_mismatch \
  >"$manifest_mismatch_fixture/stdout" \
  2>"$manifest_mismatch_fixture/stderr"; then
  fail 'restored manifest bytes were not bound to the snapshot tag'
fi
grep -Fxq \
  'phase5_restore: snapshot_restore_manifest_hash_invalid' \
  "$manifest_mismatch_fixture/stderr" ||
  fail 'manifest hash failure did not publish its safe category'
assert_no_resources "$manifest_mismatch_fixture"
test ! -e "$manifest_mismatch_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'manifest hash mismatch published a success report'

restore_layout_fixture="$(make_fixture)"
if run_fixture "$restore_layout_fixture" restore_layout_invalid \
  >"$restore_layout_fixture/stdout" 2>"$restore_layout_fixture/stderr"; then
  fail 'invalid restored snapshot layout was accepted'
fi
grep -Fxq \
  'phase5_restore: snapshot_restore_layout_invalid' \
  "$restore_layout_fixture/stderr" ||
  fail 'snapshot layout failure did not publish its safe category'
assert_no_resources "$restore_layout_fixture"
test ! -e "$restore_layout_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'invalid restored snapshot layout published a success report'

restore_ownership_fixture="$(make_fixture)"
if run_fixture "$restore_ownership_fixture" restore_ownership_failure \
  >"$restore_ownership_fixture/stdout" \
  2>"$restore_ownership_fixture/stderr"; then
  fail 'failed restore ownership normalization was accepted'
fi
grep -Fxq \
  'phase5_restore: snapshot_restore_ownership_failed' \
  "$restore_ownership_fixture/stderr" ||
  fail 'restore ownership failure did not publish its safe category'
assert_no_resources "$restore_ownership_fixture"
test ! -e "$restore_ownership_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'restore ownership failure published a success report'

for mode in extra_snapshot_tag duplicate_manifest_tag; do
  manifest_tag_fixture="$(make_fixture)"
  if run_fixture "$manifest_tag_fixture" "$mode"; then
    fail "$mode snapshot tag set was accepted"
  fi
  assert_no_resources "$manifest_tag_fixture"
done

gnu_stat_fixture="$(make_fixture)"
if ! run_fixture "$gnu_stat_fixture" gnu_stat; then
  fail 'GNU stat fixture did not use the GNU owner/mode/size interface'
fi
assert_no_resources "$gnu_stat_fixture"

project="$(
  sed -n \
    's/.*--label com\.docker\.compose\.project=\(happylearn-phase5-restore-[a-f0-9]\{12\}\).*/\1/p' \
    "$success_fixture/docker.log" |
    head -n1
)"
[[ "$project" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ ]] ||
  fail 'restore project name was not unique and canonical'
success_repository="$(cd "$success_fixture/repository" && pwd -P)"
grep -Fq -- \
  "--mount type=bind,src=$success_repository,dst=/repository" \
  "$success_fixture/docker.log" ||
  fail 'source repository was not mounted read-write for restic locks'
if grep -Fq -- \
  "--mount type=bind,src=$success_repository,dst=/repository,readonly" \
  "$success_fixture/docker.log"; then
  fail 'source repository remained read-only with restic locks enabled'
fi
grep -Fq "restic --no-cache snapshots --json --tag happylearn-batch:$BACKUP_ID" \
  "$success_fixture/docker.log" ||
  fail 'exact backup tag was not selected'
grep -Fq "restic --no-cache restore $SNAPSHOT_ID --target /restore" \
  "$success_fixture/docker.log" ||
  fail 'selected snapshot was not restored exactly'
grep -Fq 'restore-kind=restore-ownership' "$success_fixture/docker.log" ||
  fail 'restore did not normalize restored ownership'
grep -Fq -- '--cap-drop ALL --cap-add CHOWN --cap-add DAC_READ_SEARCH' \
  "$success_fixture/docker.log" ||
  fail 'restore ownership normalization capabilities are not minimal'
grep -Fq -- '--security-opt no-new-privileges:true' \
  "$success_fixture/docker.log" ||
  fail 'restore ownership normalization omitted no-new-privileges'
if grep -Fq "$SECRET_MARKER" "$success_fixture/docker.log" ||
  grep -Fq "$DATABASE_SECRET_MARKER" "$success_fixture/docker.log" ||
  grep -Fq "$OBJECT_SECRET_MARKER" "$success_fixture/docker.log" ||
  grep -Fq "$SIGNING_SECRET_MARKER" "$success_fixture/docker.log" ||
  grep -Fq "$TEACHER_CREDENTIAL_SECRET" "$success_fixture/docker.log"; then
  fail 'restore secret appeared in Docker argv'
fi
if grep -Fq -- '--env-file' "$success_fixture/docker.log"; then
  fail 'restore secret entered Docker configured environment at runtime'
fi
grep -Fq 'restore-kind=secret-init' "$success_fixture/docker.log" ||
  fail 'restore secrets were not copied through a dedicated init container'
grep -Fq -- '--network none --read-only --user 0:0 --cap-drop ALL --cap-add CHOWN --cap-add DAC_READ_SEARCH --security-opt no-new-privileges:true' \
  "$success_fixture/docker.log" ||
  fail 'restore secret init isolation or capabilities are not minimal'
grep -Fq -- \
  "type=bind,src=$success_fixture/" \
  "$success_fixture/docker.log" ||
  fail 'restore secret init source mount was absent'
grep -Fq -- 'dst=/secret-source,readonly' "$success_fixture/docker.log" ||
  fail 'restore secret init source was not mounted read-only'
grep -Fq -- 'dst=/secret-target' "$success_fixture/docker.log" ||
  fail 'restore secret init target volume was absent'
grep -Fq 'install_secret /secret-source/postgres.env' \
  "$success_fixture/docker.log" ||
  fail 'PostgreSQL secret was not installed into its consumer directory'
grep -Fq '/secret-target/postgres/runtime.env 0:0' \
  "$success_fixture/docker.log" ||
  fail 'PostgreSQL secret ownership was not root-only'
grep -Fq '/secret-target/aistor/runtime.env 1000:0' \
  "$success_fixture/docker.log" ||
  fail 'AIStor secret ownership did not match its runtime UID'
grep -Fq '/secret-target/app/runtime.env 10001:10001' \
  "$success_fixture/docker.log" ||
  fail 'app secret ownership did not match its runtime UID'
grep -Fq 'chmod 0400 "$target"' "$success_fixture/docker.log" ||
  fail 'restore secret files were not made owner-readable only'
grep -Fq 'PHASE5_RESTORE_SECRET_INIT' "$success_fixture/docker.log" ||
  fail 'restore secret init did not publish its completion marker'
for secret_subpath in postgres aistor app restore-check; do
  grep -Fq "volume-subpath=$secret_subpath,readonly" \
    "$success_fixture/docker.log" ||
    fail "$secret_subpath did not consume a read-only secret subpath"
done
grep -Fq 'volume-subpath=client,readonly' \
  "$success_fixture/docker.log" ||
  fail 'database clients did not consume the read-only client secret subpath'
grep -Fq 'PGPASSFILE=/run/restore-secrets/pgpass' \
  "$success_fixture/docker.log" ||
  fail 'database clients did not use the fixed pgpass path'
grep -Fq '/proc/1/comm' "$success_fixture/docker.log" ||
  fail 'PostgreSQL readiness did not wait for the final server process'
grep -Fq 'psql --no-psqlrc' "$success_fixture/docker.log" ||
  fail 'PostgreSQL readiness did not prove a final-server SQL query'
assert_before "$success_fixture" \
  '/proc/1/comm' \
  ' pg_restore '
for secret_consumer in \
  '/usr/local/bin/docker-entrypoint.sh postgres' \
  'minio server /data --license /minio-license/minio.license' \
  '/app/happylearn' \
  '/app/happylearn-backup restore-check'; do
  grep -Fq \
    "set -a; . /run/restore-secrets/runtime.env; set +a; exec $secret_consumer" \
    "$success_fixture/docker.log" ||
    fail "secret consumer did not source its fixed file before exec: $secret_consumer"
done
success_credential="$success_fixture/restore-probe-teacher.json"
credential_path_count="$(
  grep -Fc "$success_credential" "$success_fixture/docker.log" ||
    true
)"
[[ "$credential_path_count" -eq 1 ]] ||
  fail 'restore teacher credential path escaped its single probe mount'
probe_run_count="$(
  grep -F -- \
    '--label io.happylearn.phase5.restore-kind=restore-http-probe' \
    "$success_fixture/docker.log" |
    grep -Fc '/app/happylearn-backup restore-http-probe' ||
    true
)"
[[ "$probe_run_count" -eq 1 ]] ||
  fail 'restore HTTP probe did not run exactly once with its immutable kind'
grep -Fq -- \
  "--mount type=bind,src=$success_credential,dst=/run/secrets/restore-probe-teacher.json,readonly" \
  "$success_fixture/docker.log" ||
  fail 'restore HTTP probe did not use the exact read-only credential mount'
if grep -Fq -- '--student-isolation-probe' "$success_fixture/docker.log"; then
  fail 'legacy fake student isolation probe reached Docker'
fi
grep -Fq 'restore-kind=aistor-license-init' "$success_fixture/docker.log" ||
  fail 'AIStor license was not copied through a dedicated init container'
grep -Fq -- \
  'cp /license-source/minio.license /license-target/minio.license; chmod 0400 /license-target/minio.license; chown 1000:0 /license-target/minio.license; test "$(stat -c %u:%g:%a /license-target/minio.license)" = 1000:0:400' \
  "$success_fixture/docker.log" ||
  fail 'AIStor license initialization did not set mode before handing off ownership'
grep -Fq 'dst=/minio-license,readonly' "$success_fixture/docker.log" ||
  fail 'AIStor did not consume its owner-only license volume'

assert_before "$success_fixture" \
  ' restic --no-cache check --read-data' \
  ' restic --no-cache restore '
assert_before "$success_fixture" \
  ' restic --no-cache restore ' \
  ' pg_restore '
assert_before "$success_fixture" \
  ' pg_restore ' \
  'UPDATE operational_modes'
assert_before "$success_fixture" \
  'PHASE5_RESTORE_OBJECT_DATA' \
  'PHASE5_RESTORE_SESSIONS_REVOKED'
assert_before "$success_fixture" \
  'PHASE5_RESTORE_SESSIONS_REVOKED' \
  'restore-kind=app'
assert_before "$success_fixture" \
  'api/v1/health/ready' \
  '/app/happylearn-backup restore-check --backup-id'
assert_before "$success_fixture" \
  '--report-file /work/restore-check.report' \
  '/app/happylearn-backup restore-http-probe'

report="$success_fixture/reports/restore-$BACKUP_ID.json"
test -f "$report" || fail 'sanitized restore report was not written'
grep -Eq \
  '^\{"schemaVersion":2,"verificationId":"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-b[0-9a-f]{3}-[0-9a-f]{12}",' \
  "$report" ||
  fail 'restore report omitted its canonical verification identity'
grep -Fq \
  '"databaseRowCounts":{"users":1,"sessions":2,"subjects":3,"grades":4,"terms":5,"chapters":6,"lessons":7,"lesson_revisions":8,"files":9,"file_versions":10,"file_previews":11,"qa_threads":12,"qa_messages":13,"ai_threads":14,"ai_messages":15,"ai_runs":16}' \
  "$report" ||
  fail 'restore report omitted its fixed allowlisted row counts'
grep -Eq '"durationSeconds":[0-9]+' "$report" ||
  fail 'restore report omitted duration'
duration="$(sed -n 's/.*"durationSeconds":\([0-9][0-9]*\).*/\1/p' "$report")"
[[ "$duration" =~ ^[0-9]+$ && "$duration" -lt 14400 ]] ||
  fail 'restore report violated the four-hour RTO'
grep -Eq '"checkedObjectCount":7' "$report" ||
  fail 'restore report omitted checked object count'
grep -Eq '"rowCountTotal":136' "$report" ||
  fail 'restore report omitted safe row counts'
grep -Fq "\"manifestSHA256\":\"$MANIFEST_SHA256\"" "$report" ||
  fail 'restore report omitted its bound manifest hash'
grep -Fq \
  "\"verificationReportSHA256\":\"$VERIFICATION_REPORT_SHA256\"" \
  "$report" ||
  fail 'restore report omitted its verification report hash'
grep -Fq "\"evidenceSHA256\":\"$EVIDENCE_SHA256\"" "$report" ||
  fail 'restore report omitted its independently checked evidence hash'
grep -Eq '"reportSHA256":"[a-f0-9]{64}"' "$report" ||
  fail 'restore report omitted its safe hash'
grep -Fq '"isolation404ProbeCount":2' "$report" ||
  fail 'successful HTTP probe did not publish both isolation observations'
if grep -Fq "$SECRET_MARKER" "$report" ||
  grep -Fq "$TEACHER_CREDENTIAL_SECRET" "$report" ||
  grep -Fq "$success_credential" "$report" ||
  grep -Fq "$TEACHER_CREDENTIAL_SECRET" "$success_fixture/stdout" ||
  grep -Fq "$TEACHER_CREDENTIAL_SECRET" "$success_fixture/stderr" ||
  grep -Fq "$success_credential" "$success_fixture/stdout" ||
  grep -Fq "$success_credential" "$success_fixture/stderr" ||
  grep -Fq "$success_fixture" "$report" ||
  grep -Eiq '(password|secret|repository|objectKey|student|cookie|token|path)' \
    "$report"; then
  fail 'restore report leaked a secret, path, or identifier'
fi

second_success_fixture="$(make_fixture)"
printf '1\n' >"$second_success_fixture/random.counter"
if ! run_fixture "$second_success_fixture"; then
  sed -n '1,240p' "$second_success_fixture/docker.log" >&2
  fail 'second strict success restore fixture failed'
fi
second_project="$(
  sed -n \
    's/.*--label com\.docker\.compose\.project=\(happylearn-phase5-restore-[a-f0-9]\{12\}\).*/\1/p' \
    "$second_success_fixture/docker.log" |
    head -n1
)"
[[ "$second_project" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ &&
  "$second_project" != "$project" ]] ||
  fail 'restore projects were not unique'
assert_no_resources "$second_success_fixture"

inspect_unknown_fixture="$(make_fixture)"
if run_fixture "$inspect_unknown_fixture" inspect_unknown; then
  fail 'unknown pre-create inspect error was treated as not-found'
fi
if grep -Fq 'docker network create' "$inspect_unknown_fixture/docker.log"; then
  fail 'unknown pre-create inspect error reached resource creation'
fi
assert_no_resources "$inspect_unknown_fixture"

for ambiguous_mode in \
  ambiguous_network_create \
  ambiguous_volume_create \
  ambiguous_container_create
do
  ambiguous_fixture="$(make_fixture)"
  if run_fixture "$ambiguous_fixture" "$ambiguous_mode"; then
    fail "$ambiguous_mode was accepted"
  fi
  assert_no_resources "$ambiguous_fixture"
done

cleanup_notfound_fixture="$(make_fixture)"
if ! run_fixture "$cleanup_notfound_fixture" cleanup_notfound; then
  fail 'cleanup treated an exact not-found result as unknown'
fi
assert_no_resources "$cleanup_notfound_fixture"

cleanup_unknown_fixture="$(make_fixture)"
if run_fixture "$cleanup_unknown_fixture" cleanup_inspect_unknown; then
  fail 'cleanup hid an unknown inspect error'
fi
test ! -e "$cleanup_unknown_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'unknown cleanup state published a restore report'
find "$cleanup_unknown_fixture/state/containers" -mindepth 1 -maxdepth 1 \
  -print -quit | grep -q . ||
  fail 'unknown inspect fixture did not preserve its ambiguous orphan'
cleanup_unknown_orphan="$(
  find "$cleanup_unknown_fixture/state/containers" \
    -mindepth 1 -maxdepth 1 -type f -print -quit
)"
cleanup_unknown_orphan_name="$(basename "$cleanup_unknown_orphan")"
IFS='|' read -r cleanup_unknown_orphan_id _ <"$cleanup_unknown_orphan"
[[ "$cleanup_unknown_orphan_id" =~ ^[a-f0-9]{64}$ ]] ||
  fail 'unknown inspect fixture recorded an invalid immutable ID'
: >"$cleanup_unknown_fixture/docker.log"
if ! run_fixture "$cleanup_unknown_fixture"; then
  fail 'next run did not reap the exact-label orphan'
fi
assert_no_resources "$cleanup_unknown_fixture"
assert_before "$cleanup_unknown_fixture" \
  'docker container ls --all --format {{.Names}} --filter label=io.happylearn.phase5.restore-backup-id=' \
  'docker network create'
grep -Fq "docker rm --force $cleanup_unknown_orphan_id" \
  "$cleanup_unknown_fixture/docker.log" ||
  fail 'exact-label orphan was not removed by immutable container ID'
if grep -Fq "docker rm --force $cleanup_unknown_orphan_name" \
  "$cleanup_unknown_fixture/docker.log"; then
  fail 'exact-label orphan was removed through its reusable container name'
fi
orphan_id_inspections="$(
  grep -F 'docker container inspect --format' \
    "$cleanup_unknown_fixture/docker.log" |
    grep -Fc " $cleanup_unknown_orphan_id" ||
    true
)"
[[ "$orphan_id_inspections" -ge 2 ]] ||
  fail 'orphan immutable ID was not verified before and after removal'

sigterm_fixture="$(make_fixture)"
start_fixture_background "$sigterm_fixture" sigterm
sigterm_pid="$FIXTURE_BACKGROUND_PID"
ACTIVE_FIXTURE_PID="$sigterm_pid"
if ! wait_for_file_long "$sigterm_fixture/signal.started"; then
  kill -KILL "$sigterm_pid" 2>/dev/null || true
  fail 'SIGTERM fixture did not reach its bounded external action'
fi
kill -TERM "$sigterm_pid"
sigterm_status=0
if wait_for_direct_child "$sigterm_pid"; then
  fail 'SIGTERM restore unexpectedly succeeded'
else
  sigterm_status=$?
fi
ACTIVE_FIXTURE_PID=''
[[ "$sigterm_status" -eq 143 ]] ||
  fail "SIGTERM restore exited with $sigterm_status instead of 143"
assert_no_resources "$sigterm_fixture"
test ! -e "$sigterm_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'SIGTERM restore published a success report'

sigterm_delayed_fixture="$(make_fixture)"
start_fixture_background "$sigterm_delayed_fixture" sigterm_delayed_create
sigterm_delayed_pid="$FIXTURE_BACKGROUND_PID"
ACTIVE_FIXTURE_PID="$sigterm_delayed_pid"
if ! wait_for_file_long "$sigterm_delayed_fixture/signal.started"; then
  kill -KILL "$sigterm_delayed_pid" 2>/dev/null || true
  fail 'delayed create fixture did not reach its external Docker action'
fi
kill -TERM "$sigterm_delayed_pid"
sigterm_delayed_status=0
if wait_for_direct_child "$sigterm_delayed_pid"; then
  fail 'delayed create restore unexpectedly succeeded after SIGTERM'
else
  sigterm_delayed_status=$?
fi
ACTIVE_FIXTURE_PID=''
[[ "$sigterm_delayed_status" -eq 143 ]] ||
  fail "delayed create restore exited with $sigterm_delayed_status instead of 143"
touch "$sigterm_delayed_fixture/delayed-create.release"
delayed_create_attempts=0
while [[ ! -e "$sigterm_delayed_fixture/delayed-create.finished" &&
  "$delayed_create_attempts" -lt 100 ]]; do
  sleep 0.01
  delayed_create_attempts=$((delayed_create_attempts + 1))
done
assert_no_resources "$sigterm_delayed_fixture"
test ! -e "$sigterm_delayed_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'delayed create SIGTERM published a success report'

ledger_missing_fixture="$(make_fixture)"
start_fixture_background "$ledger_missing_fixture" ledger_missing
ledger_missing_pid="$FIXTURE_BACKGROUND_PID"
ACTIVE_FIXTURE_PID="$ledger_missing_pid"
wait_for_file_long "$ledger_missing_fixture/ledger-pause.started" ||
  fail 'ledger invalidation fixture did not reach the final external probe'
wait_for_file "$ledger_missing_fixture/workspace.path" ||
  fail 'ledger invalidation fixture did not expose its private workspace'
ledger_missing_workspace="$(<"$ledger_missing_fixture/workspace.path")"
[[ "$ledger_missing_workspace" == "${TMPDIR:-/tmp}/phase5-restore-verify."* &&
  -d "$ledger_missing_workspace" &&
  ! -L "$ledger_missing_workspace" ]] ||
  fail 'ledger invalidation fixture exposed an unsafe workspace'
rm -f "$ledger_missing_workspace/control/cleanup.intent"
touch "$ledger_missing_fixture/ledger-pause.release"
if wait_for_direct_child "$ledger_missing_pid"; then
  fail 'missing cleanup ledger was accepted'
fi
ACTIVE_FIXTURE_PID=''
assert_no_resources "$ledger_missing_fixture"
test ! -e "$ledger_missing_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'missing cleanup ledger published a success report'

for ledger_mode in \
  ledger_empty \
  ledger_truncated \
  ledger_reordered \
  ledger_duplicate \
  ledger_oversized
do
  ledger_fixture="$(make_fixture)"
  start_fixture_background "$ledger_fixture" "$ledger_mode"
  ledger_pid="$FIXTURE_BACKGROUND_PID"
  ACTIVE_FIXTURE_PID="$ledger_pid"
  wait_for_file_long "$ledger_fixture/ledger-pause.started" ||
    fail "$ledger_mode fixture did not reach the final probe"
  wait_for_file "$ledger_fixture/workspace.path" ||
    fail "$ledger_mode fixture did not expose its workspace"
  ledger_workspace="$(<"$ledger_fixture/workspace.path")"
  ledger_path="$ledger_workspace/control/cleanup.intent"
  [[ -f "$ledger_path" && ! -L "$ledger_path" ]] ||
    fail "$ledger_mode fixture exposed an invalid ledger"
  if [[ "$ledger_mode" == ledger_empty ]]; then
    ledger_project="$(
      sed -n \
        's/.*--label com\.docker\.compose\.project=\(happylearn-phase5-restore-[a-f0-9]\{12\}\).*/\1/p' \
        "$ledger_fixture/docker.log" |
        head -n1
    )"
    [[ "$ledger_project" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ ]] ||
      fail 'cleanup ledger baseline did not expose its project'
    ledger_expected="$ledger_fixture/cleanup.expected"
    printf '%s\n' \
      "networks|$ledger_project-network|network" \
      "volumes|$ledger_project-postgres|postgres" \
      "volumes|$ledger_project-aistor|aistor" \
      "volumes|$ledger_project-aistor-license|aistor-license" \
      "volumes|$ledger_project-secrets|secrets" \
      "containers|$ledger_project-volume-probe-postgres|volume-probe-postgres" \
      "containers|$ledger_project-volume-probe-aistor|volume-probe-aistor" \
      "containers|$ledger_project-volume-probe-aistor-license|volume-probe-aistor-license" \
      "containers|$ledger_project-volume-probe-secrets|volume-probe-secrets" \
      "containers|$ledger_project-restic-check|restic-check" \
      "containers|$ledger_project-restic-select|restic-select" \
      "containers|$ledger_project-restic-restore|restic-restore" \
      "containers|$ledger_project-restore-ownership|restore-ownership" \
      "containers|$ledger_project-object-restore|object-restore" \
      "containers|$ledger_project-aistor-license-init|aistor-license-init" \
      "containers|$ledger_project-secret-init|secret-init" \
      "containers|$ledger_project-postgres|postgres" \
      "containers|$ledger_project-postgres-restore|postgres-restore" \
      "containers|$ledger_project-aistor|aistor" \
      "containers|$ledger_project-redis|redis" \
      "containers|$ledger_project-revoke-sessions|revoke-sessions" \
      "containers|$ledger_project-app|app" \
      "containers|$ledger_project-restore-check|restore-check" \
      "containers|$ledger_project-restore-http-probe|restore-http-probe" \
      >"$ledger_expected"
    cmp -s "$ledger_path" "$ledger_expected" ||
      fail 'successful cleanup ledger was not the exact 24-line contract'
    [[ "$(wc -l <"$ledger_path" | tr -d '[:space:]')" == 24 &&
      "$(sort "$ledger_path" | uniq | wc -l | tr -d '[:space:]')" == 24 &&
      "$(wc -c <"$ledger_path" | tr -d '[:space:]')" -le 4096 ]] ||
      fail 'successful cleanup ledger line, uniqueness, or size bound failed'
  fi
  case "$ledger_mode" in
    ledger_empty)
      : >"$ledger_path"
      ;;
    ledger_truncated)
      sed '$d' "$ledger_path" >"$ledger_path.tampered"
      chmod 0600 "$ledger_path.tampered"
      mv "$ledger_path.tampered" "$ledger_path"
      ;;
    ledger_reordered)
      awk '
        NR == 1 { first = $0; next }
        NR == 2 { print; print first; next }
        { print }
      ' "$ledger_path" >"$ledger_path.tampered"
      chmod 0600 "$ledger_path.tampered"
      mv "$ledger_path.tampered" "$ledger_path"
      ;;
    ledger_duplicate)
      sed -n '1p' "$ledger_path" >>"$ledger_path"
      ;;
    ledger_oversized)
      dd if=/dev/zero bs=4097 count=1 >>"$ledger_path" 2>/dev/null
      ;;
    *) fail "unknown ledger tamper mode $ledger_mode" ;;
  esac
  touch "$ledger_fixture/ledger-pause.release"
  if wait_for_direct_child "$ledger_pid"; then
    fail "$ledger_mode cleanup ledger tamper was accepted"
  fi
  ACTIVE_FIXTURE_PID=''
  assert_no_resources "$ledger_fixture"
  test ! -e "$ledger_fixture/reports/restore-$BACKUP_ID.json" ||
    fail "$ledger_mode published a final report"
  test ! -e "$ledger_fixture/reports/.restore-${BACKUP_ID}.new" ||
    fail "$ledger_mode left a temporary report"
done

for mode in wrong_secret tampered_pack check_failure; do
  fixture="$(make_fixture)"
  if run_fixture "$fixture" "$mode" \
    >"$fixture/stdout" 2>"$fixture/stderr"; then
    fail "$mode repository corruption was accepted"
  fi
  assert_failure_before_restore "$fixture"
done

for mode in missing_snapshot duplicate_snapshot missing_object stale_session \
  nonempty_postgres nonempty_aistor http_probe_failure \
  report_unknown_field report_duplicate_field report_missing_field \
  report_wrong_backup report_wrong_manifest report_wrong_evidence \
  report_row_total_mismatch report_leading_zero report_negative \
  report_int64_overflow report_table_sum_overflow
do
  fixture="$(make_fixture)"
  if run_fixture "$fixture" "$mode" \
    >"$fixture/stdout" 2>"$fixture/stderr"; then
    fail "$mode restore failure was accepted"
  fi
  assert_no_resources "$fixture"
  test ! -e "$fixture/reports/restore-$BACKUP_ID.json" ||
    fail "$mode restore failure published a success report"
  if [[ "$mode" == http_probe_failure ]]; then
    grep -Fq \
      '/app/happylearn-backup restore-http-probe' \
      "$fixture/docker.log" ||
      fail 'HTTP probe failure fixture did not reach the real probe'
    if grep -Fq "$TEACHER_CREDENTIAL_SECRET" "$fixture/docker.log" ||
      grep -Fq "$TEACHER_CREDENTIAL_SECRET" "$fixture/stdout" ||
      grep -Fq "$TEACHER_CREDENTIAL_SECRET" "$fixture/stderr" ||
      grep -Fq "$fixture/restore-probe-teacher.json" "$fixture/stdout" ||
      grep -Fq "$fixture/restore-probe-teacher.json" "$fixture/stderr"; then
      fail 'failed HTTP probe leaked its credential content or host path'
    fi
  fi
done

for timeout_self_test_iteration in 1 2 3; do
  PHASE5_CONTRACT_TIMEOUT_SELF_TEST=once \
    bash "$0" ||
    fail "isolated restore timeout contract $timeout_self_test_iteration failed"
done

cleanup_fixture="$(make_fixture)"
if run_fixture "$cleanup_fixture" cleanup_failure; then
  fail 'cleanup failure was hidden'
fi
cleanup_project="$(
  sed -n \
    's/.*--label com\.docker\.compose\.project=\(happylearn-phase5-restore-[a-f0-9]\{12\}\).*/\1/p' \
    "$cleanup_fixture/docker.log" |
    head -n1
)"
for resource in app redis aistor postgres secret-init; do
  cleanup_container_id="$(fake_resource_identity "$cleanup_project-$resource")"
  grep -Fq "docker rm --force $cleanup_container_id" \
    "$cleanup_fixture/docker.log" ||
    fail "cleanup did not remove $resource through its immutable ID"
done
grep -Fq "docker volume rm $cleanup_project-postgres" \
  "$cleanup_fixture/docker.log" ||
  fail 'cleanup did not attempt PostgreSQL volume removal'
grep -Fq "docker volume rm $cleanup_project-aistor" \
  "$cleanup_fixture/docker.log" ||
  fail 'cleanup did not attempt volume removal'
grep -Fq "docker volume rm $cleanup_project-secrets" \
  "$cleanup_fixture/docker.log" ||
  fail 'cleanup did not attempt secret volume removal'
cleanup_network_id="$(fake_resource_identity "$cleanup_project-network")"
grep -Fq "docker network rm $cleanup_network_id" \
  "$cleanup_fixture/docker.log" ||
  fail 'cleanup did not remove the network through its immutable ID'
assert_no_resources "$cleanup_fixture"
test ! -e "$cleanup_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'cleanup failure published a successful restore artifact'

if grep -Eiq 'docker[[:space:]]+compose.*[[:space:]]down([[:space:]]|$)' \
  "$TARGET" ||
  grep -Eiq 'set[[:space:]]+-[^[:space:]]*x' "$TARGET" ||
  grep -Eq 'sleep[[:space:]]+[1-9][0-9]*' "$TARGET"; then
  fail 'restore harness contains a forbidden unsafe primitive'
fi
grep -Fq 'phase5-restore-contract:' "$MAKEFILE" ||
  fail 'Makefile restore contract target is absent'
grep -Fq 'bash scripts/phase5-restore_contract_test.sh' "$MAKEFILE" ||
  fail 'Makefile restore contract command is absent'

printf 'phase5 restore contract: PASS\n'
