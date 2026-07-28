#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TARGET="$ROOT/scripts/phase5-backup.sh"
COMPOSE="$ROOT/deploy/compose.dev.yml"
MAKEFILE="$ROOT/Makefile"
PACKAGE_JSON="$ROOT/package.json"
CONTRACT_TEMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/phase5-backup-contract-root.XXXXXX")"

cleanup_contract() {
  if [[ -d "$CONTRACT_TEMP_ROOT" &&
    "$CONTRACT_TEMP_ROOT" == "${TMPDIR:-/tmp}/phase5-backup-contract-root."* ]]; then
    rm -rf "$CONTRACT_TEMP_ROOT"
  fi
}
trap cleanup_contract EXIT

fail() {
  printf 'phase5 backup contract: %s\n' "$1" >&2
  exit 1
}

require_literal() {
  local file="$1"
  local value="$2"
  grep -Fq -- "$value" "$file" ||
    fail "$(basename "$file") missing literal: $value"
}

require_pattern() {
  local file="$1"
  local value="$2"
  grep -Eq -- "$value" "$file" ||
    fail "$(basename "$file") missing pattern: $value"
}

forbid_pattern() {
  local file="$1"
  local value="$2"
  if grep -Eiq -- "$value" "$file"; then
    fail "$(basename "$file") contains forbidden pattern: $value"
  fi
}

portable_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

portable_owner() {
  if stat -f '%u' "$1" >/dev/null 2>&1; then
    stat -f '%u' "$1"
  else
    stat -c '%u' "$1"
  fi
}

test -f "$TARGET" || fail "scripts/phase5-backup.sh is absent"
bash -n "$TARGET"

require_literal "$TARGET" 'set -euo pipefail'
require_literal "$TARGET" 'Usage: scripts/phase5-backup.sh --project happylearn-dev --trigger scheduled|manual|pre_release'
require_literal "$TARGET" '[[ "$PROJECT" == "happylearn-dev" ]]'
require_pattern "$TARGET" 'scheduled\|manual\|pre_release'
require_literal "$TARGET" 'COMPOSE_FILE="$ROOT/deploy/compose.dev.yml"'
require_literal "$TARGET" 'docker compose --project-name "$PROJECT" --file "$COMPOSE_FILE"'

lock_line="$(grep -nE '^[[:space:]]*acquire_host_lock$' "$TARGET" | tail -n1 | cut -d: -f1)"
trap_line="$(grep -nF 'trap cleanup EXIT HUP INT TERM' "$TARGET" | tail -n1 | cut -d: -f1)"
mutation_line="$(grep -nE '^[[:space:]]*(if[[:space:]]+(![[:space:]]+)?)?queue_or_select_run' "$TARGET" | tail -n1 | cut -d: -f1)"
test -n "$lock_line" || fail "missing host lock acquisition"
test -n "$trap_line" || fail "missing cleanup trap installation"
test -n "$mutation_line" || fail "missing queued-run mutation"
test "$trap_line" -lt "$lock_line" ||
  fail "cleanup trap must be installed before host lock acquisition"
test "$lock_line" -lt "$mutation_line" ||
  fail "lock must be acquired before the first Compose mutation"

require_literal "$TARGET" 'prepare --run-id "$RUN_ID"'
require_literal "$TARGET" 'wait_for_durable_drain'
require_literal "$TARGET" 'transition_operational_mode backup'
require_literal "$TARGET" 'stop --timeout "$SERVICE_STOP_TIMEOUT_SECONDS" worker'
require_literal "$TARGET" 'stop --timeout "$SERVICE_STOP_TIMEOUT_SECONDS" minio'
require_literal "$TARGET" 'snapshot --run-id "$RUN_ID"'
require_literal "$TARGET" 'up --detach --no-deps minio'
require_literal "$TARGET" 'wait_for_authenticated_aistor'
require_literal "$TARGET" 'start worker'
require_literal "$TARGET" 'verify --run-id "$RUN_ID"'
require_literal "$TARGET" 'sync --run-id "$RUN_ID"'
require_literal "$TARGET" 'finish --run-id "$RUN_ID"'
require_literal "$TARGET" 'fail --run-id "$RUN_ID" --category "$FAILURE_CATEGORY"'
require_literal "$TARGET" 'trap cleanup EXIT HUP INT TERM'
require_literal "$TARGET" 'restart_stopped_services'
require_literal "$TARGET" 'release_operational_lease'

local_check_line="$(grep -nF 'restic_check local' "$TARGET" | head -n1 | cut -d: -f1)"
local_prune_line="$(grep -nF 'apply_retention local' "$TARGET" | head -n1 | cut -d: -f1)"
require_literal "$TARGET" 'ensure_repository local'
test -n "$local_check_line" || fail "missing local repository check"
test -n "$local_prune_line" || fail "missing local retention"
test "$local_check_line" -lt "$local_prune_line" ||
  fail "local check must precede local forget/prune"
require_literal "$TARGET" 'remote_configuration_complete'
require_literal "$TARGET" 'apply_retention local --keep-daily 7'
require_literal "$TARGET" 'apply_retention remote --keep-daily 30 --keep-monthly 12'
require_literal "$TARGET" 'protect_pre_release 30'
require_literal "$TARGET" 'protect_last_good'
require_literal "$TARGET" 'REMOTE_DEGRADED=true'
require_literal "$TARGET" "FAILURE_CATEGORY='internal'"
require_literal "$TARGET" "digest(decode('\${LEASE_TOKEN}','hex'),'sha256')"
require_literal "$TARGET" 'start_lease_heartbeat'
require_literal "$TARGET" 'assert_lease_heartbeat'
require_literal "$TARGET" '--keep-tag'
require_literal "$TARGET" 'local -a arguments=(--group-by paths "$@")'
require_literal "$TARGET" "finished_at>=clock_timestamp()-interval '\${days} days'"
require_literal "$TARGET" 'ORDER BY finished_at DESC,id DESC'
require_literal "$TARGET" 'LIMIT 513'
require_literal "$TARGET" 'run_guarded_external'
require_literal "$TARGET" 'abort_operational_lock_session'
require_literal "$TARGET" 'compose run --rm --no-deps --entrypoint /usr/bin/timeout backup'
require_literal "$TARGET" '/app/happylearn-backup "$@"'
require_literal "$TARGET" 'exec /usr/bin/timeout --foreground --kill-after=10s "$deadline" restic'
require_literal "$TARGET" 'initialize_backup_mounts'
require_literal "$TARGET" 'verify_backup_mount_ownership'
forbid_pattern "$TARGET" "FAILURE_CATEGORY='orchestrator'"
forbid_pattern "$TARGET" 'happylearn-pre-release'

forbid_pattern "$TARGET" 'set[[:space:]]+-[^[:space:]]*x'
forbid_pattern "$TARGET" '(^|[;&|[:space:]])(env|printenv)([;&|[:space:]]|$)'
forbid_pattern "$TARGET" 'docker([[:space:]]+[^[:space:]]+)*[[:space:]]+inspect'
forbid_pattern "$TARGET" 'docker[[:space:]]+compose.*[[:space:]]down([[:space:]]|$)'
forbid_pattern "$TARGET" 'docker[[:space:]].*(volume|network)[[:space:]]+(rm|prune)'
forbid_pattern "$TARGET" 'rm[[:space:]]+-rf'
forbid_pattern "$TARGET" '(/var/run/docker\.sock|docker\.sock)'
forbid_pattern "$TARGET" '--(password|secret|access[_-]?key)(=|[[:space:]])'
forbid_pattern "$TARGET" '(password|secret|access[_-]?key)[^[:cntrl:]]*--(password|secret|access[_-]?key)(=|[[:space:]])'
forbid_pattern "$TARGET" 'sleep[[:space:]]+[0-9]'

require_pattern "$COMPOSE" '^[[:space:]]+backup:$'
require_literal "$COMPOSE" 'profiles: ["backup"]'
require_literal "$COMPOSE" 'user: "10003:0"'
require_literal "$COMPOSE" 'read_only: true'
require_literal "$COMPOSE" '/work:rw,noexec,nosuid,size=1024m,uid=10003,gid=0,mode=0700'
require_literal "$COMPOSE" 'cap_drop:'
require_literal "$COMPOSE" '- ALL'
require_literal "$COMPOSE" 'minio_data:/source/aistor:ro'
require_literal "$COMPOSE" '${HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY:-/var/lib/happylearn/backup/repository}:/repository:rw'
require_literal "$COMPOSE" '${HAPPYLEARN_BACKUP_STATE_DIRECTORY:-/var/lib/happylearn/backup/state}:/state:rw'
require_literal "$COMPOSE" '${HAPPYLEARN_BACKUP_SECRET_DIRECTORY:-/var/lib/happylearn/backup/secrets}:/source:ro'
require_literal "$COMPOSE" 'backup_secrets:/secrets:rw'
require_literal "$COMPOSE" 'backup_secrets:/run/secrets:ro'
require_literal "$COMPOSE" 'mv -f "/secrets/.$${name}.new" "/secrets/$${name}"'
forbid_pattern "$COMPOSE" 'rm -f "/secrets/\.\$\$\{name\}\.new" "/secrets/\$\$\{name\}"'
require_literal "$COMPOSE" 'backup-storage-init:'
require_literal "$COMPOSE" 'backup-secrets-init:'
require_literal "$COMPOSE" 'restart: "no"'
require_literal "$COMPOSE" 'remote_access_key_id'
require_literal "$COMPOSE" 'remote_secret_access_key'
require_literal "$COMPOSE" 'postgres-tls-init:'
require_literal "$COMPOSE" 'ssl=on'
require_literal "$COMPOSE" 'HAPPYLEARN_DATABASE_SSLMODE: ${HAPPYLEARN_BACKUP_DATABASE_SSLMODE:-require}'
require_literal "$COMPOSE" '/usr/bin/timeout'
require_literal "$COMPOSE" '--kill-after=10s'
forbid_pattern "$COMPOSE" 'HAPPYLEARN_BACKUP_(REPOSITORY|STATE|SECRET)_DIRECTORY:\?'
forbid_pattern "$COMPOSE" '(/var/run/docker\.sock|docker\.sock)'

backup_block="$(
  awk '
    /^  backup:$/ { inside=1 }
    inside && /^  [a-zA-Z0-9_-]+:$/ && $1 != "backup:" { exit }
    inside { print }
  ' "$COMPOSE"
)"
grep -Eq '^[[:space:]]+ports:' <<<"$backup_block" &&
  fail "backup service must not publish ports"
grep -Eq 'restart:[[:space:]]+(always|unless-stopped)' <<<"$backup_block" &&
  fail "backup service must be one-shot"

printf 'compose-contract-license\n' >"$CONTRACT_TEMP_ROOT/compose.license"
HAPPYLEARN_AISTOR_LICENSE_FILE="$CONTRACT_TEMP_ROOT/compose.license" \
  docker compose --project-name happylearn-dev \
  --file "$COMPOSE" config --quiet ||
  fail "base Compose config requires inactive backup-profile paths"

require_literal "$MAKEFILE" 'phase5-backup-contract:'
require_literal "$MAKEFILE" 'bash scripts/phase5-backup_contract_test.sh'
require_literal "$MAKEFILE" 'phase5-backup:'
require_literal "$MAKEFILE" 'bash scripts/phase5-backup.sh --project happylearn-dev --trigger $(BACKUP_TRIGGER)'
require_literal "$PACKAGE_JSON" '"backup:contract": "bash scripts/phase5-backup_contract_test.sh"'
require_literal "$PACKAGE_JSON" '"backup:run": "make phase5-backup"'

assert_before() {
  local log="$1"
  local first="$2"
  local second="$3"
  local first_line
  local second_line
  first_line="$(grep -nF -- "$first" "$log" | head -n1 | cut -d: -f1)"
  second_line="$(grep -nF -- "$second" "$log" | head -n1 | cut -d: -f1)"
  test -n "$first_line" || fail "dynamic trace missing: $first"
  test -n "$second_line" || fail "dynamic trace missing: $second"
  test "$first_line" -lt "$second_line" ||
    fail "dynamic trace out of order: $first must precede $second"
}

make_fixture() {
  local fixture
  fixture="$(mktemp -d "$CONTRACT_TEMP_ROOT/fixture.XXXXXX")"
  mkdir -m 0700 "$fixture/bin" "$fixture/secrets" \
    "$fixture/repository" "$fixture/state" "$fixture/secret-volume"
  printf 'license-fixture\n' >"$fixture/minio.license"
  printf 'database-password\n' >"$fixture/secrets/database_password"
  printf '/repository\n' >"$fixture/secrets/local_repository"
  printf 'local-repository-password\n' >"$fixture/secrets/local_password"
  chmod 0400 "$fixture/secrets/"*
  : >"$fixture/docker.log"
  cat >"$fixture/bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$PHASE5_FAKE_DOCKER_LOG"
if [[ "$*" == *"run --rm --no-deps backup-secrets-init"* ]]; then
  for name in database_password local_repository local_password; do
    test -f "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/$name"
  done
  for name in database_password local_repository local_password \
    remote_repository remote_password remote_access_key_id \
    remote_secret_access_key; do
    rm -f "$PHASE5_FAKE_SECRET_VOLUME/.$name.new"
    if [[ -f "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/$name" ]]; then
      cp "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/$name" \
        "$PHASE5_FAKE_SECRET_VOLUME/.$name.new"
      chmod 0400 "$PHASE5_FAKE_SECRET_VOLUME/.$name.new"
      if [[ "${PHASE5_FAKE_SECRET_COPY_FAIL_NAME:-}" == "$name" ]]; then
        exit 73
      fi
      mv -f "$PHASE5_FAKE_SECRET_VOLUME/.$name.new" \
        "$PHASE5_FAKE_SECRET_VOLUME/$name"
    else
      rm -f "$PHASE5_FAKE_SECRET_VOLUME/$name"
    fi
  done
fi
if [[ -n "${PHASE5_FAKE_DELAY_MATCH:-}" &&
      "$*" == *"$PHASE5_FAKE_DELAY_MATCH"* ]]; then
  sleep "${PHASE5_FAKE_DELAY_SECONDS:-3}"
fi
if [[ -n "${PHASE5_FAKE_FAIL_MATCH:-}" &&
      "$*" == *"$PHASE5_FAKE_FAIL_MATCH"* ]]; then
  exit 71
fi
if [[ "$*" == *" exec -T postgres psql "* ]]; then
  while IFS= read -r line; do
    if [[ -n "${PHASE5_FAKE_FAIL_SQL_MATCH:-}" &&
      "$line" == *"$PHASE5_FAKE_FAIL_SQL_MATCH"* ]]; then
      exit 72
    fi
    case "$line" in
      *PHASE5_QUERY_RUN*)
        printf '%s\n' 'SQL PHASE5_QUERY_RUN' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' "${PHASE5_FAKE_RUN_RESPONSE:-queued|scheduled|11111111-1111-4111-8111-111111111111}"
        ;;
      *PHASE5_QUERY_LEASE_VALUES*)
        printf '%s\n' 'SQL PHASE5_QUERY_LEASE_VALUES' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' '22222222-2222-4222-8222-222222222222|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
        ;;
      *PHASE5_QUERY_LEASE_ACQUIRED*)
        printf '%s\n' 'SQL PHASE5_QUERY_LEASE_ACQUIRED' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' 'acquired'
        ;;
      *PHASE5_HOLD_LOCK*)
        printf '%s\n' 'SQL PHASE5_HOLD_LOCK' >>"$PHASE5_FAKE_DOCKER_LOG"
        if [[ -n "${PHASE5_FAKE_BLOCK_LOCK_SECONDS:-}" ]]; then
          sleep "$PHASE5_FAKE_BLOCK_LOCK_SECONDS"
        fi
        printf '%s\n' 'PHASE5_LEASE_LOCKED'
        ;;
      *PHASE5_QUERY_ACTIVE_COUNTS*)
        printf '%s\n' 'SQL PHASE5_QUERY_ACTIVE_COUNTS' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' '0'
        ;;
      *PHASE5_QUERY_LEASE_TRANSITION*)
        printf '%s\n' 'SQL PHASE5_QUERY_LEASE_TRANSITION' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' 'changed'
        ;;
      *PHASE5_QUERY_LEASE_RENEW*)
        printf '%s\n' 'SQL PHASE5_QUERY_LEASE_RENEW' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' 'renewed'
        ;;
      *PHASE5_QUERY_LEASE_RELEASE*)
        printf '%s\n' 'SQL PHASE5_QUERY_LEASE_RELEASE' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' 'released'
        ;;
      *PHASE5_QUERY_LOCAL_SNAPSHOT*)
        printf '%s\n' 'SQL PHASE5_QUERY_LOCAL_SNAPSHOT' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%064d\n' 1
        ;;
      *PHASE5_QUERY_REMOTE_RESULT*)
        printf '%s\n' 'SQL PHASE5_QUERY_REMOTE_RESULT' >>"$PHASE5_FAKE_DOCKER_LOG"
        if [[ "${PHASE5_FAKE_REMOTE_RESULT:-success}" == "success" ]]; then
          printf '%064d\n' 2
        fi
        ;;
      *PHASE5_QUERY_PROTECTED_TAGS*)
        printf '%s\n' 'SQL PHASE5_QUERY_PROTECTED_TAGS' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' 'happylearn-batch:11111111-1111-4111-8111-111111111111'
        printf '%s\n' 'happylearn-batch:44444444-4444-4444-8444-444444444444'
        ;;
      *PHASE5_RELEASE_LOCK*)
        printf '%s\n' 'SQL PHASE5_RELEASE_LOCK' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' 'PHASE5_LEASE_RELEASED'
        ;;
    esac
  done
fi
FAKE_DOCKER
  chmod 0700 "$fixture/bin/docker"
  printf '%s\n' "$fixture"
}

run_fixture() {
  local fixture="$1"
  local fail_match="${2:-}"
  local remote_result="${3:-success}"
  local run_response="${4:-queued|scheduled|11111111-1111-4111-8111-111111111111}"
  PATH="$fixture/bin:$PATH" \
  PHASE5_FAKE_DOCKER_LOG="$fixture/docker.log" \
  PHASE5_FAKE_SECRET_VOLUME="$fixture/secret-volume" \
  PHASE5_FAKE_SECRET_COPY_FAIL_NAME="${PHASE5_FAKE_SECRET_COPY_FAIL_NAME:-}" \
  PHASE5_FAKE_FAIL_MATCH="$fail_match" \
  PHASE5_FAKE_REMOTE_RESULT="$remote_result" \
  PHASE5_FAKE_FAIL_SQL_MATCH="${PHASE5_FAKE_FAIL_SQL_MATCH:-}" \
  PHASE5_FAKE_DELAY_MATCH="${PHASE5_FAKE_DELAY_MATCH:-}" \
  PHASE5_FAKE_DELAY_SECONDS="${PHASE5_FAKE_DELAY_SECONDS:-3}" \
  PHASE5_FAKE_BLOCK_LOCK_SECONDS="${PHASE5_FAKE_BLOCK_LOCK_SECONDS:-}" \
  PHASE5_FAKE_RUN_RESPONSE="$run_response" \
  HAPPYLEARN_AISTOR_LICENSE_FILE="$fixture/minio.license" \
  HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$fixture/secrets" \
  HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$fixture/repository" \
  HAPPYLEARN_BACKUP_STATE_DIRECTORY="$fixture/state" \
  HAPPYLEARN_BACKUP_AGE_RECIPIENT='age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp5m40h' \
  HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID='phase5-contract-key' \
  HAPPYLEARN_BACKUP_POLL_INTERVAL_SECONDS="${HAPPYLEARN_BACKUP_POLL_INTERVAL_SECONDS:-0.01}" \
  HAPPYLEARN_BACKUP_DRAIN_TIMEOUT_SECONDS='2' \
  HAPPYLEARN_BACKUP_READY_TIMEOUT_SECONDS='2' \
  HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS="${HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS:-1}" \
  HAPPYLEARN_BACKUP_LOCK_DIRECTORY="$fixture/host.lock" \
    "$TARGET" --project happylearn-dev --trigger scheduled
}

success_fixture="$(make_fixture)"
run_fixture "$success_fixture"
success_log="$success_fixture/docker.log"
assert_before "$success_log" 'run --rm --no-deps --entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s /app/happylearn-backup prepare --run-id 11111111-1111-4111-8111-111111111111' \
  'stop --timeout'
assert_before "$success_log" 'entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s restic --no-cache --repository-file /run/secrets/local_repository --password-file /run/secrets/local_password cat config' \
  'run --rm --no-deps --entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s /app/happylearn-backup snapshot --run-id 11111111-1111-4111-8111-111111111111'
assert_before "$success_log" 'stop --timeout' \
  'run --rm --no-deps --entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s /app/happylearn-backup snapshot --run-id 11111111-1111-4111-8111-111111111111'
assert_before "$success_log" 'run --rm --no-deps --entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s /app/happylearn-backup snapshot --run-id 11111111-1111-4111-8111-111111111111' \
  'up --detach --no-deps minio'
assert_before "$success_log" 'up --detach --no-deps minio' \
  'exec -T app curl --fail --silent --show-error http://127.0.0.1:8080/api/v1/health/ready'
assert_before "$success_log" 'exec -T app curl --fail --silent --show-error http://127.0.0.1:8080/api/v1/health/ready' \
  'start worker'
assert_before "$success_log" 'run --rm --no-deps --entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s /app/happylearn-backup verify --run-id 11111111-1111-4111-8111-111111111111' \
  'entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s restic --no-cache --repository-file /run/secrets/local_repository --password-file /run/secrets/local_password forget --group-by paths --keep-daily 7 --keep-tag happylearn-batch:11111111-1111-4111-8111-111111111111 --keep-tag happylearn-batch:44444444-4444-4444-8444-444444444444 --prune'
assert_before "$success_log" 'entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s restic --no-cache --repository-file /run/secrets/local_repository --password-file /run/secrets/local_password check --read-data' \
  'entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s restic --no-cache --repository-file /run/secrets/local_repository --password-file /run/secrets/local_password forget --group-by paths --keep-daily 7 --keep-tag happylearn-batch:11111111-1111-4111-8111-111111111111 --keep-tag happylearn-batch:44444444-4444-4444-8444-444444444444 --prune'
assert_before "$success_log" 'PHASE5_HOLD_LOCK' 'PHASE5_RELEASE_LOCK'
grep -Fq 'SQL PHASE5_QUERY_LEASE_RENEW' "$success_log" ||
  fail "operational lease was not renewed across external stages"
if grep -Fq 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
  "$success_log"; then
  fail "plaintext operational lease token was logged"
fi
grep -Fq '/app/happylearn-backup sync ' "$success_log" &&
  fail "remote sync ran without the complete optional tuple"

zero_interval_fixture="$(make_fixture)"
if HAPPYLEARN_BACKUP_POLL_INTERVAL_SECONDS='0.0' \
  run_fixture "$zero_interval_fixture"; then
  fail "zero poll interval was accepted"
fi
test ! -s "$zero_interval_fixture/docker.log" ||
  fail "zero poll interval mutated Compose state"

zero_heartbeat_fixture="$(make_fixture)"
if HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS='0.000' \
  run_fixture "$zero_heartbeat_fixture"; then
  fail "zero heartbeat interval was accepted"
fi
test ! -s "$zero_heartbeat_fixture/docker.log" ||
  fail "zero heartbeat interval mutated Compose state"

for injected in \
  ' /app/happylearn-backup prepare |internal' \
  ' stop --timeout |object_store_stop' \
  ' /app/happylearn-backup snapshot |snapshot' \
  ' up --detach --no-deps minio|object_store_restart' \
  'api/v1/health/ready|object_store_restart' \
  ' /app/happylearn-backup verify |integrity' \
  'local_password cat config|integrity' \
  'local_password check --read-data|integrity' \
  'local_password forget --group-by paths --keep-daily 7|retention'
do
  stage="${injected%%|*}"
  expected_category="${injected##*|}"
  failure_fixture="$(make_fixture)"
  if run_fixture "$failure_fixture" "$stage"; then
    fail "failure injection unexpectedly succeeded: $stage"
  fi
  failure_log="$failure_fixture/docker.log"
  grep -Fq 'PHASE5_RELEASE_LOCK' "$failure_log" ||
    fail "failure trap did not release the operational lease: $stage"
  if grep -Fq ' stop --timeout ' "$failure_log"; then
    grep -Eq '(up --detach --no-deps minio|start worker)' "$failure_log" ||
      fail "failure trap did not restart a stopped service: $stage"
  fi
  grep -Fq "/app/happylearn-backup fail --run-id 11111111-1111-4111-8111-111111111111 --category $expected_category" \
    "$failure_log" ||
    fail "failure trap recorded the wrong safe stage: $stage"
  if [[ "$stage" == 'local_password cat config' ]] &&
    grep -Fq 'local_password init' "$failure_log"; then
    fail "repository probe failure incorrectly attempted initialization"
  fi
done

remote_fixture="$(make_fixture)"
printf 's3:https://remote.test/happylearn\n' >"$remote_fixture/secrets/remote_repository"
printf 'remote-repository-password\n' >"$remote_fixture/secrets/remote_password"
printf 'remote-access-key\n' >"$remote_fixture/secrets/remote_access_key_id"
printf 'remote-secret-key\n' >"$remote_fixture/secrets/remote_secret_access_key"
chmod 0400 "$remote_fixture/secrets/remote_"*
run_fixture "$remote_fixture" '' 'outage'
remote_log="$remote_fixture/docker.log"
grep -Fq '/app/happylearn-backup sync --run-id 11111111-1111-4111-8111-111111111111' \
  "$remote_log" || fail "complete remote tuple did not run sync"
grep -Fq '/app/happylearn-backup finish --run-id 11111111-1111-4111-8111-111111111111' \
  "$remote_log" || fail "remote outage did not preserve local point and finish degraded"

repeat_fixture="$(make_fixture)"
printf 's3:https://remote.test/happylearn\n' >"$repeat_fixture/secrets/remote_repository"
printf 'remote-repository-password\n' >"$repeat_fixture/secrets/remote_password"
printf 'remote-access-key\n' >"$repeat_fixture/secrets/remote_access_key_id"
printf 'remote-secret-key\n' >"$repeat_fixture/secrets/remote_secret_access_key"
chmod 0400 "$repeat_fixture/secrets/remote_"*
source_owner_before="$(portable_owner "$repeat_fixture/secrets/database_password")"
run_fixture "$repeat_fixture"
for name in remote_repository remote_password remote_access_key_id \
  remote_secret_access_key; do
  test -f "$repeat_fixture/secret-volume/$name" ||
    fail "secret init omitted optional file: $name"
  rm -f "$repeat_fixture/secrets/$name"
done
run_fixture "$repeat_fixture"
for name in remote_repository remote_password remote_access_key_id \
  remote_secret_access_key; do
  test ! -e "$repeat_fixture/secret-volume/$name" ||
    fail "secret init retained removed optional file: $name"
done
test "$(portable_owner "$repeat_fixture/secrets/database_password")" = \
  "$source_owner_before" ||
  fail "secret init changed host source ownership"
test "$(portable_mode "$repeat_fixture/secrets/database_password")" = '400' ||
  fail "secret init changed host source mode"

atomic_secret_fixture="$(make_fixture)"
printf '%s\n' 'previous-local-password' \
  >"$atomic_secret_fixture/secret-volume/local_password"
chmod 0400 "$atomic_secret_fixture/secret-volume/local_password"
if PHASE5_FAKE_SECRET_COPY_FAIL_NAME='local_password' \
  run_fixture "$atomic_secret_fixture"; then
  fail "secret copy failure was ignored"
fi
test "$(<"$atomic_secret_fixture/secret-volume/local_password")" = \
  'previous-local-password' ||
  fail "secret copy failure destroyed the previous target"
if grep -Fq 'SQL PHASE5_QUERY_RUN' "$atomic_secret_fixture/docker.log"; then
  fail "secret copy failure queued a backup run"
fi

post_sync_remote_fixture="$(make_fixture)"
printf 's3:https://remote.test/happylearn\n' >"$post_sync_remote_fixture/secrets/remote_repository"
printf 'remote-repository-password\n' >"$post_sync_remote_fixture/secrets/remote_password"
printf 'remote-access-key\n' >"$post_sync_remote_fixture/secrets/remote_access_key_id"
printf 'remote-secret-key\n' >"$post_sync_remote_fixture/secrets/remote_secret_access_key"
chmod 0400 "$post_sync_remote_fixture/secrets/remote_"*
if ! run_fixture "$post_sync_remote_fixture" \
  'phase5-remote-restic 2700s check --read-data'; then
  fail "post-sync remote check failure discarded the verified local point"
fi
post_sync_remote_log="$post_sync_remote_fixture/docker.log"
grep -Fq '/app/happylearn-backup fail --run-id 11111111-1111-4111-8111-111111111111 --category remote_unavailable' \
  "$post_sync_remote_log" ||
  fail "post-sync remote check failure was not completed as degraded"
if grep -Fq '/app/happylearn-backup finish --run-id 11111111-1111-4111-8111-111111111111' \
  "$post_sync_remote_log"; then
  fail "post-sync remote check failure used the normal finish path"
fi

incomplete_fixture="$(make_fixture)"
printf 's3:https://remote.test/happylearn\n' >"$incomplete_fixture/secrets/remote_repository"
chmod 0400 "$incomplete_fixture/secrets/remote_repository"
if run_fixture "$incomplete_fixture"; then
  fail "incomplete remote tuple was accepted"
fi
test ! -s "$incomplete_fixture/docker.log" ||
  fail "incomplete remote validation mutated Compose state"

terminal_fixture="$(make_fixture)"
if ! run_fixture "$terminal_fixture" '' 'success' \
  'succeeded|scheduled|11111111-1111-4111-8111-111111111111'; then
  fail "idempotent completed schedule did not exit successfully"
fi
if grep -Eq '(happylearn-backup (prepare|snapshot|verify|sync|finish|fail)| stop | start | up )' \
  "$terminal_fixture/docker.log"; then
  fail "idempotent completed schedule mutated service state"
fi

locked_fixture="$(make_fixture)"
mkdir -m 0700 "$locked_fixture/host.lock"
if run_fixture "$locked_fixture"; then
  fail "concurrent host lock was accepted"
fi
test ! -s "$locked_fixture/docker.log" ||
  fail "concurrent lock rejection occurred after Compose access"

other_trigger_fixture="$(make_fixture)"
if run_fixture "$other_trigger_fixture" '' 'success' \
  'queued|manual|33333333-3333-4333-8333-333333333333'; then
  fail "active run for another trigger was claimed"
fi
if grep -Eq '(happylearn-backup (prepare|snapshot|verify|sync|finish|fail)| stop | start | up )' \
  "$other_trigger_fixture/docker.log"; then
  fail "another trigger active run was mutated"
fi

mount_failure_fixture="$(make_fixture)"
if run_fixture "$mount_failure_fixture" 'backup-storage-init'; then
  fail "backup mount initialization failure was ignored"
fi
if grep -Fq 'SQL PHASE5_QUERY_RUN' "$mount_failure_fixture/docker.log"; then
  fail "backup run was queued before mount initialization succeeded"
fi

blocked_advisory_fixture="$(make_fixture)"
blocked_advisory_started="$SECONDS"
if PHASE5_FAKE_BLOCK_LOCK_SECONDS='5' \
  run_fixture "$blocked_advisory_fixture"; then
  fail "blocked advisory lock was accepted"
fi
if ((SECONDS - blocked_advisory_started >= 5)); then
  fail "blocked advisory lock session was not terminated at its deadline"
fi
test ! -e "$blocked_advisory_fixture/host.lock" ||
  fail "blocked advisory lock left the host lock behind"

heartbeat_fixture="$(make_fixture)"
heartbeat_started="$SECONDS"
if PHASE5_FAKE_FAIL_SQL_MATCH='PHASE5_QUERY_LEASE_RENEW' \
  PHASE5_FAKE_DELAY_MATCH=' /app/happylearn-backup prepare ' \
  PHASE5_FAKE_DELAY_SECONDS='4' \
  HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS='0.05' \
  run_fixture "$heartbeat_fixture"; then
  fail "lost operational lease heartbeat was ignored"
fi
if ((SECONDS - heartbeat_started >= 4)); then
  fail "lost heartbeat did not terminate the running external action"
fi
grep -Fq '/app/happylearn-backup fail --run-id 11111111-1111-4111-8111-111111111111 --category lease_lost' \
  "$heartbeat_fixture/docker.log" ||
  fail "lost operational lease heartbeat was not recorded safely"

printf 'phase5 backup contract: PASS\n'
