#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
umask 077

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TARGET="$ROOT/scripts/phase5-restore-verify.sh"
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

contract_sha256_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256
  else
    return 1
  fi
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
  trap - HUP INT TERM
  cleanup_contract
  exit "$status"
}

trap cleanup_contract EXIT
trap 'handle_contract_signal 129' HUP
trap 'handle_contract_signal 130' INT
trap 'handle_contract_signal 143' TERM

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

wait_for_direct_child() {
  local pid="$1"
  local attempts=0
  local running
  while [[ "$attempts" -lt 1000 ]]; do
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
  1) printf ' 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n' ;;
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
      volumes) printf 'Error: No such volume: %s\n' "$name" >&2 ;;
      networks) printf 'Error: No such network: %s\n' "$name" >&2 ;;
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
      volumes) printf 'Error: No such volume: %s\n' "$name" >&2 ;;
      networks) printf 'Error: No such network: %s\n' "$name" >&2 ;;
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
    "$type" =~ ^(postgres|aistor|aistor-license|network)$ ]] ||
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
      *pg_isready*|*minio/health/ready*|*api/v1/health/ready*)
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
esac

case "$kind" in
  postgres|aistor|app|restore-check)
    env_file="$(arg_after --env-file "$@")" || exit 64
    [[ -f "$env_file" && ! -L "$env_file" ]] || exit 64
    ;;
esac

if [[ "$*" == *'--detach'* ]]; then
  case "$kind" in
    postgres)
      [[ "$*" == *'--tmpfs /var/run/postgresql:rw,noexec,nosuid,size=8m'* ]] ||
        exit 71
      grep -Fxq 'POSTGRES_USER=happylearn' "$env_file" || exit 71
      grep -Eq '^POSTGRES_PASSWORD=[a-f0-9]{64}$' "$env_file" || exit 71
      printf '%064d\n' 1
      exit 0
      ;;
    aistor)
      [[ "$*" == *'dst=/minio-license,readonly'* &&
        "$*" != *'type=bind,src='*'minio.license'* ]] ||
        exit 71
      grep -Eq '^MINIO_ROOT_PASSWORD=[a-f0-9]{64}$' "$env_file" || exit 71
      printf '%064d\n' 1
      exit 0
      ;;
    redis)
      printf '%064d\n' 1
      exit 0
      ;;
    app)
      grep -Fq 'PHASE5_RESTORE_SESSIONS_REVOKED' "$log" || exit 71
      grep -Eq '^HAPPYLEARN_LOGIN_THROTTLE_SECRET=[a-f0-9]{64}$' \
        "$env_file" ||
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
  restic-check)
    [[ "$*" == *' restic --no-cache check --read-data'* &&
      "$*" != *'--no-lock'* ]] ||
      exit 64
    case "$mode" in
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
    mkdir -p "$restore_mount/source/aistor"
    printf 'database dump fixture\n' >"$restore_mount/database.dump"
    printf 'object fixture\n' >"$restore_mount/source/aistor/object"
    printf '%s' "$PHASE5_FAKE_MANIFEST_TEXT" >"$restore_mount/manifest.json"
    if [[ "$mode" == manifest_hash_mismatch ]]; then
      printf 'x' >>"$restore_mount/manifest.json"
    fi
    ;;
  object-restore)
    [[ "$*" == *'PHASE5_RESTORE_OBJECT_DATA'* ]] || exit 64
    ;;
  aistor-license-init)
    [[ "$*" == *'PHASE5_RESTORE_AISTOR_LICENSE'* &&
      "$*" == *'dst=/license-source/minio.license,readonly'* &&
      "$*" == *'dst=/license-target'* ]] ||
      exit 64
    ;;
  postgres-restore)
    [[ "$*" == *' pg_restore '* ]] || exit 64
    ;;
  revoke-sessions)
    sql="$(arg_after --command "$@")" || exit 64
    [[ "$sql" == "BEGIN; UPDATE sessions "*"restore_verification"*"UPDATE operational_modes "*"mode='normal'"*"owner_id=NULL"*"lease_token_hash=NULL"*"lease_expires_at=NULL"*"entered_at=NULL"*"version=version+1"*"WHERE singleton_id=true; COMMIT; SELECT 'PHASE5_RESTORE_SESSIONS_REVOKED';" ]] ||
      exit 64
    ;;
  restore-check)
    [[ "$*" == *"/app/happylearn-backup restore-check --backup-id ${PHASE5_FAKE_BACKUP_ID} --report-file /work/restore-check.report"* ]] ||
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
    grep -Fxq 'HAPPYLEARN_MINIO_USE_TLS=false' "$env_file" || exit 64
    active=0
    missing=0
    unexpected=0
    report_backup_id="$PHASE5_FAKE_BACKUP_ID"
    report_manifest_sha256="$PHASE5_FAKE_MANIFEST_SHA256"
    report_row_count_total=136
    report_evidence_sha256="$PHASE5_FAKE_EVIDENCE_SHA256"
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
        'table_users_count=1' \
        'table_sessions_count=2' \
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
        'checked_object_count=7' \
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
    if [[ "$mode" == ledger_missing ]]; then
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
    run_fixture "$fixture" "$mode" >/dev/null 2>&1 &
  FIXTURE_BACKGROUND_PID="$!"
}

test -x "$TARGET" || fail 'restore harness is absent'
bash -n "$TARGET"
if grep -Eq \
  '^[[:space:]]*trap[[:space:]]+cleanup_contract[[:space:]]+EXIT[[:space:]]+HUP' \
  "$0"; then
  fail 'contract signal traps reuse the ordinary EXIT cleanup handler'
fi
grep -Fq 'handle_contract_signal 129' "$0" ||
  fail 'contract HUP trap does not preserve 128+signal status'
grep -Fq 'handle_contract_signal 130' "$0" ||
  fail 'contract INT trap does not preserve 128+signal status'
grep -Fq 'handle_contract_signal 143' "$0" ||
  fail 'contract TERM trap does not preserve 128+signal status'
grep -Fq 'active_fixture_is_direct_job' "$0" ||
  fail 'contract cleanup does not verify the active direct job'
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
if ! run_fixture "$success_fixture" \
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
wait_for_file "$report_race_fixture/report-pause.started" ||
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
if run_fixture "$manifest_mismatch_fixture" manifest_hash_mismatch; then
  fail 'restored manifest bytes were not bound to the snapshot tag'
fi
assert_no_resources "$manifest_mismatch_fixture"
test ! -e "$manifest_mismatch_fixture/reports/restore-$BACKUP_ID.json" ||
  fail 'manifest hash mismatch published a success report'

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
if grep -Fq "$SECRET_MARKER" "$success_fixture/docker.log" ||
  grep -Fq "$DATABASE_SECRET_MARKER" "$success_fixture/docker.log" ||
  grep -Fq "$OBJECT_SECRET_MARKER" "$success_fixture/docker.log" ||
  grep -Fq "$SIGNING_SECRET_MARKER" "$success_fixture/docker.log" ||
  grep -Fq "$TEACHER_CREDENTIAL_SECRET" "$success_fixture/docker.log"; then
  fail 'restore secret appeared in Docker argv'
fi
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
if ! wait_for_file "$sigterm_fixture/signal.started"; then
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
if ! wait_for_file "$sigterm_delayed_fixture/signal.started"; then
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
wait_for_file "$ledger_missing_fixture/ledger-pause.started" ||
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
  report_row_total_mismatch
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

timeout_fixture="$(make_fixture)"
timeout_started="$SECONDS"
if HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS=1 \
  run_fixture "$timeout_fixture" timeout; then
  fail 'timed out restore command was accepted'
fi
timeout_duration=$((SECONDS - timeout_started))
[[ "$timeout_duration" -lt 10 ]] ||
  fail 'restore timeout was not bounded'
assert_no_resources "$timeout_fixture"

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
for resource in app redis aistor postgres; do
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
