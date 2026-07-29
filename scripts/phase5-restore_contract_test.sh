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
CONTRACT_TEMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/phase5-restore-contract.XXXXXX")"

fail() {
  printf 'phase5 restore contract: %s\n' "$1" >&2
  exit 1
}

cleanup_contract() {
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
  grep -Fq ' restic check --read-data' "$fixture/docker.log" ||
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

make_fixture() {
  local fixture
  fixture="$(mktemp -d "$CONTRACT_TEMP_ROOT/fixture.XXXXXX")"
  mkdir -p \
    "$fixture/bin" \
    "$fixture/repository" \
    "$fixture/secrets" \
    "$fixture/reports" \
    "$fixture/state/containers" \
    "$fixture/state/volumes" \
    "$fixture/state/networks"
  chmod 0700 \
    "$fixture/repository" \
    "$fixture/secrets" \
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
  [[ -f "$file" ]] || exit 1
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
  local project owner type file
  project="$(label_value com.docker.compose.project "$@")" || exit 64
  owner="$(label_value io.happylearn.phase5.restore-owner "$@")" || exit 64
  type="$(label_value io.happylearn.phase5.restore-kind "$@")" || exit 64
  [[ "$project" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ &&
    "$name" == "$project-"* &&
    "$owner" =~ ^[a-f0-9]{64}$ &&
    "$type" =~ ^(postgres|aistor|network)$ ]] ||
    exit 64
  file="$(resource_file "$kind" "$name")"
  [[ ! -e "$file" ]] || exit 1
  printf '%s|%s|%s\n' "$project" "$owner" "$type" >"$file"
  printf '%s\n' "$name"
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
[[ "$project" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ &&
  "$name" == "$project-"* &&
  "$owner" =~ ^[a-f0-9]{64}$ ]] ||
  exit 64
container_file="$(resource_file containers "$name")"
[[ ! -e "$container_file" ]] || exit 1
printf '%s|%s|%s\n' "$project" "$owner" "$kind" >"$container_file"

if [[ "$*" == *'--detach'* ]]; then
  case "$kind" in
    postgres|aistor|redis)
      printf '%064d\n' 1
      exit 0
      ;;
    app)
      grep -Fq 'PHASE5_RESTORE_SESSIONS_REVOKED' "$log" || exit 71
      [[ "$*" == *'HAPPYLEARN_LOGIN_THROTTLE_SECRET='* ]] || exit 71
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
  restic-check)
    [[ "$*" == *' restic check --read-data'* ]] || exit 64
    case "$mode" in
      wrong_secret|tampered_pack|check_failure) exit 73 ;;
      timeout)
        trap '' TERM
        while :; do sleep 0.1; done
        ;;
    esac
    ;;
  restic-select)
    [[ "$*" == *" restic snapshots --json --tag happylearn-batch:${PHASE5_FAKE_BACKUP_ID}"* ]] ||
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
    grep -Fq ' restic check --read-data' "$log" || exit 74
    [[ "$*" == *" restic restore ${PHASE5_FAKE_SNAPSHOT_ID} --target /restore"* ]] ||
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
  PATH="$fixture/bin:$PATH" \
  PHASE5_FAKE_DOCKER_LOG="$fixture/docker.log" \
  PHASE5_FAKE_DOCKER_STATE="$fixture/state" \
  PHASE5_FAKE_RANDOM_COUNTER="$fixture/random.counter" \
  PHASE5_FAKE_MODE="$mode" \
  PHASE5_FAKE_BACKUP_ID="$BACKUP_ID" \
  PHASE5_FAKE_SNAPSHOT_ID="$SNAPSHOT_ID" \
  PHASE5_FAKE_WORKSPACE_MARKER="$fixture/workspace.path" \
  HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$fixture/repository" \
  HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$fixture/secrets" \
  HAPPYLEARN_AISTOR_LICENSE_FILE="$fixture/minio.license" \
  HAPPYLEARN_RESTORE_REPORT_DIRECTORY="$fixture/reports" \
  HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS="${HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS:-3}" \
  HAPPYLEARN_RESTORE_READY_TIMEOUT_SECONDS="${HAPPYLEARN_RESTORE_READY_TIMEOUT_SECONDS:-3}" \
  HAPPYLEARN_RESTORE_POLL_INTERVAL_SECONDS='0.01' \
    "$TARGET" --backup-id "$BACKUP_ID"
}

test -x "$TARGET" || fail 'restore harness is absent'
bash -n "$TARGET"

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

success_fixture="$(make_fixture)"
if ! run_fixture "$success_fixture"; then
  sed -n '1,240p' "$success_fixture/docker.log" >&2
  find "$success_fixture/state" -mindepth 1 -maxdepth 2 -print >&2
  fail 'strict success restore fixture failed'
fi
assert_no_resources "$success_fixture"

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
grep -Fq "restic snapshots --json --tag happylearn-batch:$BACKUP_ID" \
  "$success_fixture/docker.log" ||
  fail 'exact backup tag was not selected'
grep -Fq "restic restore $SNAPSHOT_ID --target /restore" \
  "$success_fixture/docker.log" ||
  fail 'selected snapshot was not restored exactly'
signing_epoch="$(
  sed -n 's/.*HAPPYLEARN_LOGIN_THROTTLE_SECRET=\([a-f0-9]\{64\}\).*/\1/p' \
    "$success_fixture/docker.log" |
    head -n1
)"
minio_secret="$(
  sed -n 's/.*MINIO_ROOT_PASSWORD=\([a-f0-9]\{64\}\).*/\1/p' \
    "$success_fixture/docker.log" |
    head -n1
)"
[[ "$signing_epoch" =~ ^[a-f0-9]{64}$ &&
  "$minio_secret" =~ ^[a-f0-9]{64}$ &&
  "$signing_epoch" != "$minio_secret" ]] ||
  fail 'isolated signing epoch was not rotated independently'

assert_before "$success_fixture" \
  ' restic check --read-data' \
  ' restic restore '
assert_before "$success_fixture" \
  ' restic restore ' \
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
run_fixture "$second_success_fixture"
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
