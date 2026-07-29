#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
umask 077

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TARGET="$ROOT/scripts/phase5-restore-verify.sh"
MAKEFILE="$ROOT/Makefile"
BACKUP_ID='11111111-1111-4111-8111-111111111111'
SNAPSHOT_ID='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
SECRET_MARKER='phase5-restore-secret-marker'
DATABASE_SECRET_MARKER='fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210'
OBJECT_SECRET_MARKER='123456789abcdeffedcba98765432100123456789abcdeffedcba98765432100'
SIGNING_SECRET_MARKER='abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd'
CONTRACT_TEMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/phase5-restore-contract.XXXXXX")"
ACTIVE_FIXTURE_PID=''

fail() {
  printf 'phase5 restore contract: %s\n' "$1" >&2
  exit 1
}

cleanup_contract() {
  if [[ "$ACTIVE_FIXTURE_PID" =~ ^[1-9][0-9]*$ ]]; then
    kill -TERM "$ACTIVE_FIXTURE_PID" 2>/dev/null || true
    kill -KILL "$ACTIVE_FIXTURE_PID" 2>/dev/null || true
    wait "$ACTIVE_FIXTURE_PID" 2>/dev/null || true
  fi
  chmod -R u+rwX "$CONTRACT_TEMP_ROOT" 2>/dev/null || true
  rm -rf "$CONTRACT_TEMP_ROOT"
}
trap cleanup_contract EXIT HUP INT TERM

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
if [[ "${PHASE5_FAKE_MODE:-}" == workspace_failure &&
  "$target" == "${TMPDIR:-/tmp}/phase5-restore-verify."* ]]; then
  printf '%s\n' "$target" >"$PHASE5_FAKE_WORKSPACE_MARKER"
  exit 76
fi
exec /bin/chmod "$@"
FAKE_CHMOD
  chmod 0700 "$fixture/bin/chmod"
  cat >"$fixture/bin/stat" <<'FAKE_STAT'
#!/usr/bin/env bash
set -euo pipefail
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
  "$3" =~ ^[0-9]+$ ]]
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
  local name="$2"
  [[ "$name" =~ ^happylearn-phase5-restore-[a-f0-9]{12}[-a-z0-9]*$ ]] ||
    exit 64
  printf '%s/%s/%s' "$state" "$kind" "$name"
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
  file="$(resource_file "$kind" "$name")"
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
  local project owner type backup file
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
  printf '%s|%s|%s|%s\n' "$project" "$owner" "$type" "$backup" >"$file"
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
  local argument file project owner type backup
  for argument in "$@"; do
    if [[ "$previous" == '--filter' &&
      "$argument" == "label=io.happylearn.phase5.restore-backup-id="* ]]; then
      wanted_backup="${argument#*=}"
      wanted_backup="${wanted_backup#*=}"
    fi
    previous="$argument"
  done
  [[ "$wanted_backup" == "$PHASE5_FAKE_BACKUP_ID" ]] || exit 64
  for file in "$state/$kind"/*; do
    [[ -f "$file" ]] || continue
    IFS='|' read -r project owner type backup <"$file"
    [[ "$backup" == "$wanted_backup" ]] || continue
    basename "$file"
  done
}

remove_resource() {
  local kind="$1"
  local name="$2"
  local file
  file="$(resource_file "$kind" "$name")"
  [[ -f "$file" ]] || exit 1
  rm -f "$file"
  if [[ "$mode" == cleanup_failure &&
    "$kind:$name" == containers:*-app ]]; then
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
if [[ "$*" == *"$PHASE5_FAKE_SECRET_MARKER"* ||
  "$*" == *'POSTGRES_PASSWORD='* ||
  "$*" == *'MINIO_ROOT_PASSWORD='* ||
  "$*" == *'HAPPYLEARN_DATABASE_URL='* ||
  "$*" == *'HAPPYLEARN_LOGIN_THROTTLE_SECRET='* ||
  "$*" == *'HAPPYLEARN_MINIO_SECRET_KEY='* ]]; then
  exit 64
fi
container_file="$(resource_file containers "$name")"
[[ ! -e "$container_file" ]] || exit 1
printf '%s|%s|%s|%s\n' "$project" "$owner" "$kind" "$backup" >"$container_file"
if [[ "$mode" == ambiguous_container_create && "$kind" == restic-check ]]; then
  exit 70
fi

case "$kind" in
  restic-*|postgres-restore|revoke-sessions|restore-check|student-one|student-two)
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
      *)
        printf '[{"id":"%s","tags":["happylearn-batch:%s"]}]\n' \
          "$PHASE5_FAKE_SNAPSHOT_ID" "$PHASE5_FAKE_BACKUP_ID"
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
    [[ "$*" == *'PHASE5_RESTORE_SESSIONS_REVOKED'* &&
      "$*" == *'UPDATE sessions'* &&
      "$*" == *"restore_verification"* ]] ||
      exit 64
    ;;
  restore-check)
    [[ "$*" == *"/app/happylearn-backup restore-check --backup-id ${PHASE5_FAKE_BACKUP_ID} --report-file /work/restore-check.report"* ]] ||
      exit 64
    report_mount=''
    previous=''
    for argument in "$@"; do
      if [[ "$previous" == '--mount' &&
        "$argument" == type=bind,src=*,dst=/work ]]; then
        report_mount="${argument#type=bind,src=}"
        report_mount="${report_mount%,dst=/work}"
      fi
      previous="$argument"
    done
    [[ -n "$report_mount" ]] || exit 64
    active=0
    missing=0
    unexpected=0
    case "$mode" in
      stale_session) active=1 ;;
      missing_object) missing=1 ;;
    esac
    printf '%s\n' \
      'schema_version=1' \
      'migration_version=42' \
      'row_count_total=123' \
      'checked_object_count=7' \
      "missing_object_count=$missing" \
      "unexpected_object_count=$unexpected" \
      "active_session_count=$active" \
      >"$report_mount/restore-check.report"
    ;;
  student-one|student-two)
    index=1
    [[ "$kind" == student-two ]] && index=2
    [[ "$*" == *"/app/happylearn-backup restore-check --backup-id ${PHASE5_FAKE_BACKUP_ID} --student-isolation-probe ${index} --expected-status 404"* ]] ||
      exit 64
    if [[ "$mode" == student_probe_failure && "$index" == 2 ]]; then
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
    "PHASE5_FAKE_SNAPSHOT_ID=$SNAPSHOT_ID"
    "PHASE5_FAKE_SECRET_MARKER=$SECRET_MARKER"
    "PHASE5_FAKE_DATABASE_SECRET_MARKER=$DATABASE_SECRET_MARKER"
    "PHASE5_FAKE_OBJECT_SECRET_MARKER=$OBJECT_SECRET_MARKER"
    "PHASE5_FAKE_SIGNING_SECRET_MARKER=$SIGNING_SECRET_MARKER"
    "PHASE5_FAKE_HOST_UID=$(id -u)"
    "PHASE5_FAKE_HOST_GID=$(id -g)"
    "PHASE5_FAKE_WORKSPACE_MARKER=$fixture/workspace.path"
    "PHASE5_FAKE_SIGNAL_MARKER=$fixture/signal.started"
    "HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY=$fixture/repository"
    "HAPPYLEARN_BACKUP_SECRET_DIRECTORY=$fixture/secrets"
    "HAPPYLEARN_AISTOR_LICENSE_FILE=$fixture/minio.license"
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
if grep -Eq 'restic[[:space:]]+[^[:cntrl:]]*--no-lock' "$TARGET"; then
  fail 'read-only repository bypassed restic locks without proven exclusion'
fi
grep -Fq 'cleanup.intent' "$TARGET" ||
  fail 'restore harness has no cleanup intent ledger'
grep -Fq 'label=$BACKUP_LABEL=$BACKUP_ID' "$TARGET" ||
  fail 'restore orphan reaper lacks the exact backup label'
grep -Fq 'owned_resources_absent' "$TARGET" ||
  fail 'restore report is not fenced on zero owned resources'

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

success_fixture="$(make_fixture)"
if ! run_fixture "$success_fixture"; then
  sed -n '1,240p' "$success_fixture/docker.log" >&2
  find "$success_fixture/state" -mindepth 1 -maxdepth 2 -print >&2
  fail 'strict success restore fixture failed'
fi
assert_no_resources "$success_fixture"

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
  "--mount type=bind,src=$success_repository,dst=/repository,readonly" \
  "$success_fixture/docker.log" ||
  fail 'source repository was not mounted read-only'
if grep -F -- \
  "--mount type=bind,src=$success_repository,dst=/repository" \
  "$success_fixture/docker.log" |
  grep -Fq 'rw'; then
  fail 'source repository received a writable mount'
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
  grep -Fq "$SIGNING_SECRET_MARKER" "$success_fixture/docker.log"; then
  fail 'restore secret appeared in Docker argv'
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
  '--student-isolation-probe 1 --expected-status 404'
assert_before "$success_fixture" \
  '--student-isolation-probe 1 --expected-status 404' \
  '--student-isolation-probe 2 --expected-status 404'

report="$success_fixture/reports/restore-$BACKUP_ID.json"
test -f "$report" || fail 'sanitized restore report was not written'
grep -Eq '"durationSeconds":[0-9]+' "$report" ||
  fail 'restore report omitted duration'
duration="$(sed -n 's/.*"durationSeconds":\([0-9][0-9]*\).*/\1/p' "$report")"
[[ "$duration" =~ ^[0-9]+$ && "$duration" -lt 14400 ]] ||
  fail 'restore report violated the four-hour RTO'
grep -Eq '"checkedObjectCount":7' "$report" ||
  fail 'restore report omitted checked object count'
grep -Eq '"rowCountTotal":123' "$report" ||
  fail 'restore report omitted safe row counts'
grep -Eq '"reportSHA256":"[a-f0-9]{64}"' "$report" ||
  fail 'restore report omitted its safe hash'
if grep -Fq "$SECRET_MARKER" "$report" ||
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
: >"$cleanup_unknown_fixture/docker.log"
if ! run_fixture "$cleanup_unknown_fixture"; then
  fail 'next run did not reap the exact-label orphan'
fi
assert_no_resources "$cleanup_unknown_fixture"
assert_before "$cleanup_unknown_fixture" \
  'docker container ls --all --quiet --filter label=io.happylearn.phase5.restore-backup-id=' \
  'docker network create'

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

for mode in wrong_secret tampered_pack check_failure; do
  fixture="$(make_fixture)"
  if run_fixture "$fixture" "$mode"; then
    fail "$mode repository corruption was accepted"
  fi
  assert_failure_before_restore "$fixture"
done

for mode in missing_snapshot duplicate_snapshot missing_object stale_session \
  nonempty_postgres nonempty_aistor student_probe_failure
do
  fixture="$(make_fixture)"
  if run_fixture "$fixture" "$mode"; then
    fail "$mode restore failure was accepted"
  fi
  assert_no_resources "$fixture"
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
  grep -Fq "docker rm --force $cleanup_project-$resource" \
    "$cleanup_fixture/docker.log" ||
    fail "cleanup did not attempt exact container removal"
done
grep -Fq "docker volume rm $cleanup_project-postgres" \
  "$cleanup_fixture/docker.log" ||
  fail 'cleanup did not attempt PostgreSQL volume removal'
grep -Fq "docker volume rm $cleanup_project-aistor" \
  "$cleanup_fixture/docker.log" ||
  fail 'cleanup did not attempt volume removal'
grep -Fq "docker network rm $cleanup_project-network" \
  "$cleanup_fixture/docker.log" ||
  fail 'cleanup did not attempt network removal'
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
