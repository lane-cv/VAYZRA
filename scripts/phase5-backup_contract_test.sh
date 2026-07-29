#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TARGET="$ROOT/scripts/phase5-backup.sh"
COMPOSE="$ROOT/deploy/compose.dev.yml"
MAKEFILE="$ROOT/Makefile"
PACKAGE_JSON="$ROOT/package.json"
LIVE_FIXTURE="$ROOT/scripts/phase5-backup_live_test.sh"
LIVE_COMPOSE="$ROOT/deploy/compose.backup-live.yml"
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
require_literal "$TARGET" '--project-name "$EFFECTIVE_PROJECT"'
require_literal "$TARGET" 'arguments+=(--file "$LIVE_COMPOSE_FILE")'
require_literal "$TARGET" 'HAPPYLEARN_BACKUP_LIVE_TEST'
require_literal "$TARGET" '^happylearn-phase5-live-[a-f0-9]{12}$'
require_literal "$TARGET" 'configure_live_context'
require_literal "$TARGET" 'SYNC_CONTAINER_NAME'
require_literal "$TARGET" 'sync_hostname_for_run'
require_literal "$TARGET" 'io.happylearn.phase5.sync-run'
require_literal "$TARGET" 'io.happylearn.phase5.sync-owner'
require_literal "$TARGET" 'cleanup_sync_container'
require_literal "$TARGET" 'pending_degraded_sync_runs'
require_literal "$TARGET" "error_category='remote_unavailable'"
require_literal "$TARGET" "WHERE state='succeeded' AND remote_snapshot_id IS NOT NULL"
require_literal "$TARGET" 'ORDER BY finished_at DESC,id DESC'
require_literal "$TARGET" 'LIMIT 32'
require_literal "$TARGET" 'restic unlock'
require_literal "$TARGET" 'LOCK_OWNER_TOKEN'
require_literal "$TARGET" 'process_holds_liveness_file'
require_literal "$TARGET" 'host_lock_owner_matches'
require_literal "$TARGET" 'publish_host_lock'

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
local_prune_line="$(grep -nF 'run_retention local' "$TARGET" | head -n1 | cut -d: -f1)"
require_literal "$TARGET" 'ensure_repository local'
test -n "$local_check_line" || fail "missing local repository check"
test -n "$local_prune_line" || fail "missing local retention"
test "$local_check_line" -lt "$local_prune_line" ||
  fail "local check must precede local forget/prune"
require_literal "$TARGET" 'remote_configuration_complete'
require_literal "$TARGET" 'run_retention local'
require_literal "$TARGET" 'run_retention remote'
require_literal "$TARGET" 'REMOTE_DEGRADED=true'
require_literal "$TARGET" 'FROM backup_artifacts AS artifacts'
require_literal "$TARGET" 'count(DISTINCT artifacts.kind)=3'
require_literal "$TARGET" 'count(DISTINCT artifacts.snapshot_id)=1'
require_literal "$TARGET" "safe_log 'remote_repository_unavailable'"
require_literal "$TARGET" "safe_log 'remote_sync_command_failed'"
require_literal "$TARGET" "safe_log 'remote_sync_evidence_missing'"
require_literal "$TARGET" "safe_log 'remote_repository_check_failed'"
require_literal "$TARGET" "safe_log 'remote_retention_failed'"
require_literal "$TARGET" "FAILURE_CATEGORY='internal'"
require_literal "$TARGET" "digest(decode('\${LEASE_TOKEN}','hex'),'sha256')"
require_literal "$TARGET" 'start_lease_heartbeat'
require_literal "$TARGET" 'assert_lease_heartbeat'
require_literal "$TARGET" 'SEPARATE_EXTERNAL_GROUP=false'
require_literal "$TARGET" 'terminate_external_group "$LEASE_HEARTBEAT_PID"'
require_literal "$TARGET" '/app/happylearn-backup-retention'
require_literal "$TARGET" '--repository "$repository" --run-id "$RUN_ID"'
require_literal "$TARGET" 'DATABASE_QUERY_TIMEOUT_SECONDS='
require_literal "$TARGET" 'PGCONNECTTIMEOUT='
require_literal "$TARGET" "statement_timeout="
require_literal "$TARGET" 'PHASE5_LEASE_RELEASED'
forbid_pattern "$TARGET" '--group-by[[:space:]]+paths'
forbid_pattern "$TARGET" 'unlock[^[:cntrl:]]*--remove-all'
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
require_literal "$COMPOSE" 'mem_limit: 768m'
require_literal "$COMPOSE" 'cpus: 0.4'
require_literal "$COMPOSE" 'hostname: ${HAPPYLEARN_BACKUP_CONTAINER_HOSTNAME:-happylearn-backup}'
test -x "$ROOT/scripts/phase5-backup_live_test.sh" ||
  fail "real Task 4 live fixture is absent or not executable"
test -f "$LIVE_COMPOSE" ||
  fail "fixed live Compose override is absent"
bash -n "$LIVE_FIXTURE"
require_literal "$LIVE_COMPOSE" 'ports: !reset []'
require_literal "$LIVE_FIXTURE" 'PROJECT="happylearn-phase5-live-${FIXTURE_SUFFIX}"'
require_literal "$LIVE_FIXTURE" 'HAPPYLEARN_BACKUP_LIVE_TEST'
require_literal "$LIVE_FIXTURE" '--file "$COMPOSE_LIVE_FILE"'
require_literal "$LIVE_FIXTURE" '--env-file "$REMOTE_ENV_FILE"'
require_literal "$LIVE_FIXTURE" '"$FIXTURE_ROOT/ca-context"'
require_literal "$LIVE_FIXTURE" '"$FIXTURE_ROOT/server-certs:/certs:ro"'
require_literal "$LIVE_FIXTURE" 'monitor_backup_runtime'
require_literal "$LIVE_FIXTURE" 'backup-storage-init|backup-secrets-init|backup'
forbid_pattern "$LIVE_FIXTURE" 'worker and backup container overlapped'
require_literal "$LIVE_FIXTURE" '--foreground --kill-after=5s 30s /bin/sh'
require_literal "$LIVE_FIXTURE" '/usr/bin/timeout --foreground --kill-after=5s 300s'
require_literal "$LIVE_FIXTURE" 'base backup image unexpectedly trusted the fixture CA'
require_literal "$LIVE_FIXTURE" 'derived backup image did not trust the fixture CA'
require_literal "$LIVE_FIXTURE" 'docker pause "$REMOTE_NAME"'
require_literal "$LIVE_FIXTURE" 'docker unpause "$REMOTE_NAME"'
require_literal "$LIVE_FIXTURE" 'com.docker.compose.oneoff=True'
require_literal "$LIVE_FIXTURE" 'record_compose_resources'
require_literal "$LIVE_FIXTURE" 'compose create postgres redis minio app worker'
require_literal "$LIVE_FIXTURE" 'fixture root remained after exact cleanup'
require_literal "$LIVE_FIXTURE" 'MAX_CPU_PERCENT='
require_literal "$LIVE_FIXTURE" 'MAX_MEMORY_MIB='
require_literal "$LIVE_FIXTURE" "HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS='30'"
require_literal "$LIVE_FIXTURE" 'for day in $(seq 1 29)'
require_literal "$LIVE_FIXTURE" 'for month in $(seq 2 12)'
require_literal "$LIVE_FIXTURE" 'seed_path="/work/seed-$run_id"'
require_literal "$LIVE_FIXTURE" "'YYYY-MM-DD HH24:MI:SS'"
require_literal "$LIVE_FIXTURE" 'remote-pre-release-protected'
require_literal "$LIVE_FIXTURE" 'remote-pre-release-expired'
require_literal "$LIVE_FIXTURE" 'old uncommitted local snapshot was not removed'
require_literal "$LIVE_FIXTURE" 'old uncommitted remote snapshot was not removed'
require_literal "$LIVE_FIXTURE" 'external unowned local snapshot was deleted'
require_literal "$LIVE_FIXTURE" 'external unowned remote snapshot was deleted'
require_literal "$LIVE_FIXTURE" 'recent uncommitted local snapshot was removed'
require_literal "$LIVE_FIXTURE" 'recent failed remote point was removed or occupied a success slot'
forbid_pattern "$LIVE_FIXTURE" '--env[[:space:]]+MINIO_ROOT_(USER|PASSWORD)='
forbid_pattern "$LIVE_FIXTURE" 'offline:/certs'
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
grep -Fq 'mem_limit: 768m' <<<"$backup_block" ||
  fail "backup service memory limit is missing"
grep -Fq 'cpus: 0.4' <<<"$backup_block" ||
  fail "backup service CPU limit is missing"

for init_service in backup-storage-init backup-secrets-init; do
  init_block="$(
    awk -v service="$init_service" '
      $0 == "  " service ":" { inside=1 }
      inside && /^  [a-zA-Z0-9_-]+:$/ &&
        $1 != service ":" { exit }
      inside { print }
    ' "$COMPOSE"
  )"
  grep -Fq 'mem_limit: 64m' <<<"$init_block" ||
    fail "$init_service memory limit is missing"
  grep -Fq 'cpus: 0.05' <<<"$init_block" ||
    fail "$init_service CPU limit is missing"
done

service_cpu_hundredths() {
  local service="$1"
  awk -v service="$service" '
    $0 == "  " service ":" { inside=1; next }
    inside && /^  [a-zA-Z0-9_-]+:$/ { exit }
    inside && /^[[:space:]]+cpus:[[:space:]]/ {
      value=$2
      printf "%d\n", (value * 100) + 0.5
      exit
    }
  ' "$COMPOSE"
}

persistent_cpu_hundredths=0
for service in postgres redis minio app worker; do
  service_cpu="$(service_cpu_hundredths "$service")"
  [[ "$service_cpu" =~ ^[0-9]+$ ]] ||
    fail "$service CPU limit is missing or invalid"
  persistent_cpu_hundredths=$((persistent_cpu_hundredths + service_cpu))
done
max_backup_one_shot_cpu_hundredths=0
for service in backup-storage-init backup-secrets-init backup; do
  service_cpu="$(service_cpu_hundredths "$service")"
  [[ "$service_cpu" =~ ^[0-9]+$ ]] ||
    fail "$service CPU limit is missing or invalid"
  if test "$service_cpu" -gt "$max_backup_one_shot_cpu_hundredths"; then
    max_backup_one_shot_cpu_hundredths="$service_cpu"
  fi
done
test "$((persistent_cpu_hundredths + max_backup_one_shot_cpu_hundredths))" \
  -le 200 ||
  fail "worker-overlap backup one-shot CPU peak exceeds 2 CPUs"

maintenance_cpu_hundredths=$((35 + 15 + 30 + 20 + 40))
maintenance_memory_mib=$((512 + 128 + 512 + 256 + 768))
test "$maintenance_cpu_hundredths" -le 200 ||
  fail "worker-stopped maintenance CPU peak exceeds 2 CPUs"
test "$maintenance_memory_mib" -le 4096 ||
  fail "worker-stopped maintenance memory peak exceeds 4 GiB"

printf 'compose-contract-license\n' >"$CONTRACT_TEMP_ROOT/compose.license"
HAPPYLEARN_AISTOR_LICENSE_FILE="$CONTRACT_TEMP_ROOT/compose.license" \
  docker compose --project-name happylearn-dev \
  --file "$COMPOSE" config --quiet ||
  fail "base Compose config requires inactive backup-profile paths"
live_rendered="$(
  HAPPYLEARN_AISTOR_LICENSE_FILE="$CONTRACT_TEMP_ROOT/compose.license" \
    docker compose --project-name happylearn-phase5-live-012345abcdef \
    --file "$COMPOSE" --file "$LIVE_COMPOSE" config
)" || fail "fixed live Compose override does not render"
if grep -Eq '^[[:space:]]+ports:' <<<"$live_rendered"; then
  fail "live Compose override retained a published host port"
fi

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

wait_for_file() {
  local path="$1"
  local attempts=0
  while [[ ! -f "$path" && "$attempts" -lt 500 ]]; do
    sleep 0.01
    attempts=$((attempts + 1))
  done
  [[ -f "$path" ]]
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
if [[ "${1:-}" == 'ps' ]]; then
  if [[ -s "${PHASE5_FAKE_CONTAINER_STATE:-}" ]]; then
    IFS='|' read -r container_id container_name run_id owner state \
      <"$PHASE5_FAKE_CONTAINER_STATE"
    printf '%s|%s|%s|%s|%s\n' \
      "$container_id" "$container_name" "$run_id" "$owner" "$state"
  fi
  exit 0
fi
if [[ "${1:-}" == 'stop' ]]; then
  IFS='|' read -r container_id container_name run_id owner state \
    <"$PHASE5_FAKE_CONTAINER_STATE"
  requested_name="${!#}"
  [[ "$requested_name" == "$container_id" ]] || exit 75
  printf '%s|%s|%s|%s|exited\n' \
    "$container_id" "$container_name" "$run_id" "$owner" \
    >"$PHASE5_FAKE_CONTAINER_STATE"
  printf '%s\n' "$container_id"
  exit 0
fi
if [[ "${1:-}" == 'container' && "${2:-}" == 'wait' ]]; then
  IFS='|' read -r container_id container_name run_id owner state \
    <"$PHASE5_FAKE_CONTAINER_STATE"
  [[ "${3:-}" == "$container_id" && "$state" == 'exited' ]] || exit 76
  printf '%s\n' 124
  exit 0
fi
if [[ "${1:-}" == 'rm' ]]; then
  IFS='|' read -r container_id container_name run_id owner state \
    <"$PHASE5_FAKE_CONTAINER_STATE"
  [[ "${2:-}" == "$container_id" && "$state" == 'exited' ]] || exit 77
  rm -f "$PHASE5_FAKE_CONTAINER_STATE"
  printf '%s\n' "$container_id"
  exit 0
fi
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
if [[ "$*" == *"/app/happylearn-backup sync --run-id "* &&
  "${PHASE5_FAKE_SYNC_TIMEOUT:-}" == true ]]; then
  container_name=''
  run_id=''
  owner=''
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --name)
        container_name="${2:-}"
        shift 2
        ;;
      --label)
        case "${2:-}" in
          io.happylearn.phase5.sync-run=*)
            run_id="${2#*=}"
            ;;
          io.happylearn.phase5.sync-owner=*)
            owner="${2#*=}"
            ;;
        esac
        shift 2
        ;;
      *)
        shift
        ;;
    esac
  done
  [[ -n "$container_name" &&
    "$run_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ &&
    "$owner" =~ ^[0-9a-f]{64}$ ]] || exit 78
  printf '%s|%s|%s|%s|running\n' \
    'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd' \
    "$container_name" "$run_id" "$owner" \
    >"$PHASE5_FAKE_CONTAINER_STATE"
  exit 124
fi
if [[ -n "${PHASE5_FAKE_DELAY_MATCH:-}" &&
      "$*" == *"$PHASE5_FAKE_DELAY_MATCH"* ]]; then
  if [[ -n "${PHASE5_FAKE_DELAY_RELEASE_FILE:-}" ]]; then
    printf '%s\n' started >"${PHASE5_FAKE_DELAY_RELEASE_FILE}.started"
    while [[ ! -e "$PHASE5_FAKE_DELAY_RELEASE_FILE" ]]; do
      sleep 0.01
    done
  else
    sleep "${PHASE5_FAKE_DELAY_SECONDS:-3}"
  fi
fi
if [[ -n "${PHASE5_FAKE_FAIL_MATCH:-}" &&
      "$*" == *"$PHASE5_FAKE_FAIL_MATCH"* ]]; then
  exit 71
fi
if [[ "$*" == *postgres*psql* ]]; then
  while IFS= read -r line; do
    if [[ -n "${PHASE5_FAKE_BLOCK_SQL_MATCH:-}" &&
      "$line" == *"$PHASE5_FAKE_BLOCK_SQL_MATCH"* ]]; then
      trap '' HUP TERM
      sleep "${PHASE5_FAKE_BLOCK_SQL_SECONDS:-3}"
      printf 'SQL PHASE5_BLOCK_COMPLETED %s\n' \
        "$PHASE5_FAKE_BLOCK_SQL_MATCH" >>"$PHASE5_FAKE_DOCKER_LOG"
    fi
    if [[ -n "${PHASE5_FAKE_FAIL_SQL_MATCH:-}" &&
      "$line" == *"$PHASE5_FAKE_FAIL_SQL_MATCH"* ]]; then
      exit 72
    fi
    case "$line" in
      *PHASE5_QUERY_RUN*)
        printf '%s\n' 'SQL PHASE5_QUERY_RUN' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' 'BEGIN'
        printf '%s\n' "${PHASE5_FAKE_RUN_RESPONSE:-queued|scheduled|11111111-1111-4111-8111-111111111111}"
        printf '%s\n' 'COMMIT'
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
      *PHASE5_QUERY_UNPREPARED_FAIL*)
        printf '%s\n' 'SQL PHASE5_QUERY_UNPREPARED_FAIL' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%s\n' 'recorded'
        ;;
      *PHASE5_QUERY_LOCAL_SNAPSHOT*)
        printf '%s\n' 'SQL PHASE5_QUERY_LOCAL_SNAPSHOT' >>"$PHASE5_FAKE_DOCKER_LOG"
        printf '%064d\n' 1
        ;;
      *PHASE5_QUERY_DEGRADED_SYNC_RUNS*)
        printf '%s\n' 'SQL PHASE5_QUERY_DEGRADED_SYNC_RUNS' >>"$PHASE5_FAKE_DOCKER_LOG"
        if [[ -n "${PHASE5_FAKE_DEGRADED_RUNS:-}" ]]; then
          printf '%s\n' "$PHASE5_FAKE_DEGRADED_RUNS"
        fi
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
if [[ "${PHASE5_FAKE_FAIL_BACKUP_FAIL:-}" == true &&
      "$*" == *"/app/happylearn-backup fail "* ]]; then
  exit 74
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
  PHASE5_FAKE_CONTAINER_STATE="$fixture/sync-container.state" \
  PHASE5_FAKE_SYNC_TIMEOUT="${PHASE5_FAKE_SYNC_TIMEOUT:-}" \
  PHASE5_FAKE_DEGRADED_RUNS="${PHASE5_FAKE_DEGRADED_RUNS:-}" \
  PHASE5_FAKE_SECRET_VOLUME="$fixture/secret-volume" \
  PHASE5_FAKE_SECRET_COPY_FAIL_NAME="${PHASE5_FAKE_SECRET_COPY_FAIL_NAME:-}" \
  PHASE5_FAKE_FAIL_MATCH="$fail_match" \
  PHASE5_FAKE_REMOTE_RESULT="$remote_result" \
  PHASE5_FAKE_FAIL_SQL_MATCH="${PHASE5_FAKE_FAIL_SQL_MATCH:-}" \
  PHASE5_FAKE_BLOCK_SQL_MATCH="${PHASE5_FAKE_BLOCK_SQL_MATCH:-}" \
  PHASE5_FAKE_BLOCK_SQL_SECONDS="${PHASE5_FAKE_BLOCK_SQL_SECONDS:-3}" \
  PHASE5_FAKE_DELAY_MATCH="${PHASE5_FAKE_DELAY_MATCH:-}" \
  PHASE5_FAKE_DELAY_SECONDS="${PHASE5_FAKE_DELAY_SECONDS:-3}" \
  PHASE5_FAKE_DELAY_RELEASE_FILE="${PHASE5_FAKE_DELAY_RELEASE_FILE:-}" \
  PHASE5_FAKE_BLOCK_LOCK_SECONDS="${PHASE5_FAKE_BLOCK_LOCK_SECONDS:-}" \
  PHASE5_FAKE_FAIL_BACKUP_FAIL="${PHASE5_FAKE_FAIL_BACKUP_FAIL:-}" \
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
  HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS:-2700}" \
  HAPPYLEARN_BACKUP_DATABASE_QUERY_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_DATABASE_QUERY_TIMEOUT_SECONDS:-1}" \
  HAPPYLEARN_BACKUP_DATABASE_CONNECT_TIMEOUT_SECONDS='1' \
  HAPPYLEARN_BACKUP_LOCK_DIRECTORY="$fixture/host.lock" \
    "$TARGET" --project happylearn-dev --trigger scheduled
}

success_fixture="$(make_fixture)"
if ! run_fixture "$success_fixture"; then
  sed -n '1,240p' "$success_fixture/docker.log" >&2
  if [[ -d "$success_fixture/host.lock" ]]; then
    find "$success_fixture/host.lock" -maxdepth 1 -print >&2
    sed -n '1,80p' "$success_fixture/host.lock/operational.stdout" >&2 ||
      true
  fi
  fail "baseline fixture failed"
fi
success_log="$success_fixture/docker.log"
assert_before "$success_log" \
  'run --rm --no-deps --entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s /app/happylearn-backup prepare --run-id 11111111-1111-4111-8111-111111111111' \
  'stop --timeout 60 worker'
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
  'run --rm --no-deps --entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s /app/happylearn-backup-retention --repository local --run-id 11111111-1111-4111-8111-111111111111'
assert_before "$success_log" 'entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s restic --no-cache --repository-file /run/secrets/local_repository --password-file /run/secrets/local_password check --read-data' \
  'run --rm --no-deps --entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s /app/happylearn-backup-retention --repository local --run-id 11111111-1111-4111-8111-111111111111'
assert_before "$success_log" \
  'run --rm --no-deps --entrypoint /usr/bin/timeout backup --foreground --kill-after=10s 2700s /app/happylearn-backup-retention --repository local --run-id 11111111-1111-4111-8111-111111111111' \
  'start worker'
assert_before "$success_log" 'PHASE5_HOLD_LOCK' 'PHASE5_RELEASE_LOCK'
grep -Fq 'SQL PHASE5_QUERY_LEASE_RENEW' "$success_log" ||
  fail "operational lease was not renewed across external stages"
if grep -Fq 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
  "$success_log"; then
  fail "plaintext operational lease token was logged"
fi
grep -Fq '/app/happylearn-backup sync ' "$success_log" &&
  fail "remote sync ran without the complete optional tuple"

live_context_fixture="$(make_fixture)"
if ! HAPPYLEARN_BACKUP_LIVE_TEST='1' \
  HAPPYLEARN_BACKUP_LIVE_PROJECT='happylearn-phase5-live-012345abcdef' \
  HAPPYLEARN_BACKUP_LIVE_ROOT="$live_context_fixture" \
  run_fixture "$live_context_fixture"; then
  fail "strict live execution context was rejected"
fi
grep -Fq -- \
  "--project-name happylearn-phase5-live-012345abcdef --file $COMPOSE --file $LIVE_COMPOSE" \
  "$live_context_fixture/docker.log" ||
  fail "live execution did not use the unique project and fixed override"

invalid_live_context_fixture="$(make_fixture)"
if HAPPYLEARN_BACKUP_LIVE_TEST='1' \
  HAPPYLEARN_BACKUP_LIVE_PROJECT='happylearn-dev' \
  HAPPYLEARN_BACKUP_LIVE_ROOT="$invalid_live_context_fixture" \
  run_fixture "$invalid_live_context_fixture"; then
  fail "unsafe live execution context was accepted"
fi
test ! -s "$invalid_live_context_fixture/docker.log" ||
  fail "unsafe live execution context mutated Compose state"

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
  '/app/happylearn-backup-retention --repository local|retention'
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
grep -Fq '/app/happylearn-backup fail --run-id 11111111-1111-4111-8111-111111111111 --category remote_unavailable' \
  "$remote_log" ||
  fail "remote outage did not preserve the local point as degraded"

remote_sync_failure_fixture="$(make_fixture)"
printf 's3:https://remote.test/happylearn\n' \
  >"$remote_sync_failure_fixture/secrets/remote_repository"
printf 'remote-repository-password\n' \
  >"$remote_sync_failure_fixture/secrets/remote_password"
printf 'remote-access-key\n' \
  >"$remote_sync_failure_fixture/secrets/remote_access_key_id"
printf 'remote-secret-key\n' \
  >"$remote_sync_failure_fixture/secrets/remote_secret_access_key"
chmod 0400 "$remote_sync_failure_fixture/secrets/remote_"*
if ! run_fixture "$remote_sync_failure_fixture" \
  ' /app/happylearn-backup sync '; then
  fail "remote sync failure discarded the verified local point"
fi
remote_sync_failure_log="$remote_sync_failure_fixture/docker.log"
assert_before "$remote_sync_failure_log" \
  '/app/happylearn-backup sync --run-id 11111111-1111-4111-8111-111111111111' \
  '/app/happylearn-backup-retention --repository local --run-id 11111111-1111-4111-8111-111111111111'
grep -Fq \
  'local_password unlock' "$remote_sync_failure_log" ||
  fail "remote sync failure did not run default local lock cleanup"
if grep -Eq 'unlock[^[:cntrl:]]*--remove-all' "$remote_sync_failure_log"; then
  fail "remote sync failure used destructive Restic lock cleanup"
fi
if grep -Fq '/app/happylearn-backup-retention --repository remote' \
  "$remote_sync_failure_log"; then
  fail "failed remote sync ran remote retention"
fi
grep -Fq '/app/happylearn-backup fail --run-id 11111111-1111-4111-8111-111111111111 --category remote_unavailable' \
  "$remote_sync_failure_log" ||
  fail "failed remote sync did not complete the local point as degraded"

sync_timeout_fixture="$(make_fixture)"
printf 's3:https://remote.test/happylearn\n' \
  >"$sync_timeout_fixture/secrets/remote_repository"
printf 'remote-repository-password\n' \
  >"$sync_timeout_fixture/secrets/remote_password"
printf 'remote-access-key\n' \
  >"$sync_timeout_fixture/secrets/remote_access_key_id"
printf 'remote-secret-key\n' \
  >"$sync_timeout_fixture/secrets/remote_secret_access_key"
chmod 0400 "$sync_timeout_fixture/secrets/remote_"*
printf '%s\n' external-active-lock >"$sync_timeout_fixture/external.lock"
if ! PHASE5_FAKE_SYNC_TIMEOUT=true run_fixture "$sync_timeout_fixture"; then
  fail "timed-out managed sync did not preserve the verified local point"
fi
sync_timeout_log="$sync_timeout_fixture/docker.log"
managed_name='happylearn-dev-phase5-sync-11111111-1111-4111-8111-111111111111'
managed_id='dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
grep -Eq \
  "run --name ${managed_name} --label io\\.happylearn\\.phase5\\.sync-run=11111111-1111-4111-8111-111111111111 --label io\\.happylearn\\.phase5\\.sync-owner=[0-9a-f]{64} --rm .* /app/happylearn-backup sync --run-id 11111111-1111-4111-8111-111111111111" \
  "$sync_timeout_log" ||
  fail "sync did not use the exact name and complete ownership labels"
assert_before "$sync_timeout_log" \
  "ps --all --no-trunc --filter name=^/${managed_name}$" \
  "stop --time 30 ${managed_id}"
assert_before "$sync_timeout_log" \
  "stop --time 30 ${managed_id}" \
  "container wait ${managed_id}"
assert_before "$sync_timeout_log" \
  "container wait ${managed_id}" \
  "rm ${managed_id}"
test ! -e "$sync_timeout_fixture/sync-container.state" ||
  fail "timed-out managed sync container survived exact cleanup"
test -f "$sync_timeout_fixture/external.lock" ||
  fail "default unlock removed an external active lock marker"
if ! PHASE5_FAKE_DEGRADED_RUNS='11111111-1111-4111-8111-111111111111' \
  run_fixture "$sync_timeout_fixture"; then
  fail "next run did not recover after exact stale sync cleanup"
fi
grep -Fq 'SQL PHASE5_QUERY_DEGRADED_SYNC_RUNS' "$sync_timeout_log" ||
  fail "recovery did not query bounded degraded sync owners"
grep -Fq \
  '/app/happylearn-backup finish --run-id 11111111-1111-4111-8111-111111111111' \
  "$sync_timeout_log" ||
  fail "recovery run did not finish successfully"
test -f "$sync_timeout_fixture/external.lock" ||
  fail "recovery removed an external active lock marker"

degraded_retention_fixture="$(make_fixture)"
printf 's3:https://remote.test/happylearn\n' \
  >"$degraded_retention_fixture/secrets/remote_repository"
printf 'remote-repository-password\n' \
  >"$degraded_retention_fixture/secrets/remote_password"
printf 'remote-access-key\n' \
  >"$degraded_retention_fixture/secrets/remote_access_key_id"
printf 'remote-secret-key\n' \
  >"$degraded_retention_fixture/secrets/remote_secret_access_key"
chmod 0400 "$degraded_retention_fixture/secrets/remote_"*
if ! run_fixture "$degraded_retention_fixture" \
  '/app/happylearn-backup-retention --repository local' 'outage'; then
  fail "remote degradation plus conservative local retention failure became failed"
fi
grep -Fq '/app/happylearn-backup fail --run-id 11111111-1111-4111-8111-111111111111 --category remote_unavailable' \
  "$degraded_retention_fixture/docker.log" ||
  fail "remote degradation plus local retention failure was not degraded"

remote_probe_recovery_fixture="$(make_fixture)"
printf 's3:https://remote.test/happylearn\n' \
  >"$remote_probe_recovery_fixture/secrets/remote_repository"
printf 'remote-repository-password\n' \
  >"$remote_probe_recovery_fixture/secrets/remote_password"
printf 'remote-access-key\n' \
  >"$remote_probe_recovery_fixture/secrets/remote_access_key_id"
printf 'remote-secret-key\n' \
  >"$remote_probe_recovery_fixture/secrets/remote_secret_access_key"
chmod 0400 "$remote_probe_recovery_fixture/secrets/remote_"*
run_fixture "$remote_probe_recovery_fixture" \
  'phase5-remote-restic 2700s cat config'
remote_probe_recovery_log="$remote_probe_recovery_fixture/docker.log"
grep -Fq '/app/happylearn-backup-retention --repository remote --run-id 11111111-1111-4111-8111-111111111111' \
  "$remote_probe_recovery_log" ||
  fail "successful sync did not clear the earlier remote probe failure"
grep -Fq '/app/happylearn-backup finish --run-id 11111111-1111-4111-8111-111111111111' \
  "$remote_probe_recovery_log" ||
  fail "recovered remote probe did not finish successfully"
if grep -Fq '/app/happylearn-backup fail --run-id 11111111-1111-4111-8111-111111111111 --category remote_unavailable' \
  "$remote_probe_recovery_log"; then
  fail "recovered remote probe remained degraded"
fi

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

for invalid_repository in \
  's3:http://remote.test/happylearn' \
  's3:https://user@remote.test/happylearn' \
  's3:https://remote.test/' \
  's3:https://remote.test/happylearn?prefix=x' \
  's3:https://remote.test/happylearn#fragment' \
  's3:https://remote.test/happy\learn' \
  's3:https://[bad/bucket' \
  's3:https://remote.test/INSECURE-TLS' \
  's3:https://remote.test/bad%zz' \
  's3:https://remote.test/path with space'
do
  invalid_remote_fixture="$(make_fixture)"
  printf '%s\n' "$invalid_repository" \
    >"$invalid_remote_fixture/secrets/remote_repository"
  printf 'remote-repository-password\n' \
    >"$invalid_remote_fixture/secrets/remote_password"
  printf 'remote-access-key\n' \
    >"$invalid_remote_fixture/secrets/remote_access_key_id"
  printf 'remote-secret-key\n' \
    >"$invalid_remote_fixture/secrets/remote_secret_access_key"
  chmod 0400 "$invalid_remote_fixture/secrets/remote_"*
  if run_fixture "$invalid_remote_fixture"; then
    fail "malicious remote repository was accepted: $invalid_repository"
  fi
  test ! -s "$invalid_remote_fixture/docker.log" ||
    fail "malicious remote repository mutated Compose state"
done

for invalid_secret_name in \
  database_password local_repository local_password \
  remote_repository remote_password remote_access_key_id \
  remote_secret_access_key
do
  invalid_secret_fixture="$(make_fixture)"
  printf 's3:https://remote.test/happylearn\n' \
    >"$invalid_secret_fixture/secrets/remote_repository"
  printf 'remote-repository-password\n' \
    >"$invalid_secret_fixture/secrets/remote_password"
  printf 'remote-access-key\n' \
    >"$invalid_secret_fixture/secrets/remote_access_key_id"
  printf 'remote-secret-key\n' \
    >"$invalid_secret_fixture/secrets/remote_secret_access_key"
  if [[ -e "$invalid_secret_fixture/secrets/$invalid_secret_name" ]]; then
    chmod 0600 "$invalid_secret_fixture/secrets/$invalid_secret_name"
  fi
  printf 'first-line\nsecond-line\n' \
    >"$invalid_secret_fixture/secrets/$invalid_secret_name"
  chmod 0400 "$invalid_secret_fixture/secrets/"*
  if run_fixture "$invalid_secret_fixture"; then
    fail "multiline secret was accepted: $invalid_secret_name"
  fi
  test ! -s "$invalid_secret_fixture/docker.log" ||
    fail "multiline secret mutated Compose state: $invalid_secret_name"
done

trailing_nul_fixture="$(make_fixture)"
chmod 0600 "$trailing_nul_fixture/secrets/local_password"
printf 'local-repository-password\0' \
  >"$trailing_nul_fixture/secrets/local_password"
chmod 0400 "$trailing_nul_fixture/secrets/local_password"
if run_fixture "$trailing_nul_fixture"; then
  fail "secret with a trailing NUL was accepted"
fi
test ! -s "$trailing_nul_fixture/docker.log" ||
  fail "secret with a trailing NUL mutated Compose state"

single_nul_fixture="$(make_fixture)"
chmod 0600 "$single_nul_fixture/secrets/local_password"
printf '\0' >"$single_nul_fixture/secrets/local_password"
chmod 0400 "$single_nul_fixture/secrets/local_password"
if run_fixture "$single_nul_fixture"; then
  fail "single-NUL secret was accepted"
fi
test ! -s "$single_nul_fixture/docker.log" ||
  fail "single-NUL secret mutated Compose state"

terminal_fixture="$(make_fixture)"
if ! run_fixture "$terminal_fixture" '' 'success' \
  'succeeded|scheduled|11111111-1111-4111-8111-111111111111'; then
  fail "idempotent completed schedule did not exit successfully"
fi
if grep -Eq '(happylearn-backup (prepare|snapshot|verify|sync|finish|fail)| stop | start | up )' \
  "$terminal_fixture/docker.log"; then
  fail "idempotent completed schedule mutated service state"
fi

active_lock_fixture="$(make_fixture)"
active_lock_release="$active_lock_fixture/active-lock.release"
cat >"$active_lock_fixture/bin/lsof" <<'FAKE_LSOF'
#!/usr/bin/env bash
exit 127
FAKE_LSOF
chmod 0700 "$active_lock_fixture/bin/lsof"
PHASE5_FAKE_DELAY_MATCH='backup-storage-init' \
  PHASE5_FAKE_DELAY_RELEASE_FILE="$active_lock_release" \
  run_fixture "$active_lock_fixture" >/dev/null 2>&1 &
active_lock_runner="$!"
if ! wait_for_file "${active_lock_release}.started"; then
  kill -KILL "$active_lock_runner" 2>/dev/null || true
  fail "active lock fixture did not reach the blocked one-shot"
fi
[[ -L "$active_lock_fixture/host.lock" ]] ||
  fail "active lock was not atomically published as a symlink"
active_lock_owner="$(readlink "$active_lock_fixture/host.lock")"
active_log_lines_before="$(
  wc -l <"$active_lock_fixture/docker.log" | tr -d '[:space:]'
)"
if run_fixture "$active_lock_fixture"; then
  touch "$active_lock_release"
  wait "$active_lock_runner" || true
  fail "active host lock was stolen"
fi
active_log_lines_after="$(
  wc -l <"$active_lock_fixture/docker.log" | tr -d '[:space:]'
)"
test "$active_log_lines_before" -eq "$active_log_lines_after" ||
  fail "active lock rejection accessed Compose"
[[ -L "$active_lock_fixture/host.lock" &&
  "$(readlink "$active_lock_fixture/host.lock")" == "$active_lock_owner" ]] ||
  fail "active owner lock was replaced"
touch "$active_lock_release"
wait "$active_lock_runner" ||
  fail "active lock owner did not finish after release"
[[ ! -e "$active_lock_fixture/host.lock" &&
  ! -L "$active_lock_fixture/host.lock" ]] ||
  fail "active owner cleanup left its host lock"

stale_lock_fixture="$(make_fixture)"
stale_lock_release="$stale_lock_fixture/stale-lock.release"
PHASE5_FAKE_DELAY_MATCH='backup-storage-init' \
  PHASE5_FAKE_DELAY_RELEASE_FILE="$stale_lock_release" \
  run_fixture "$stale_lock_fixture" >/dev/null 2>&1 &
stale_lock_runner="$!"
if ! wait_for_file "${stale_lock_release}.started"; then
  kill -KILL "$stale_lock_runner" 2>/dev/null || true
  fail "stale lock fixture did not reach the blocked one-shot"
fi
[[ -L "$stale_lock_fixture/host.lock" ]] ||
  fail "stale lock fixture did not publish its lock"
stale_lock_owner="$(readlink "$stale_lock_fixture/host.lock")"
stale_lock_pid="$(
  sed -n 's/^pid=//p' "$stale_lock_owner/owner"
)"
[[ "$stale_lock_pid" =~ ^[1-9][0-9]*$ ]] ||
  fail "stale lock owner PID was invalid"
kill -KILL "$stale_lock_pid"
attempts=0
while kill -0 "$stale_lock_pid" 2>/dev/null && [[ "$attempts" -lt 500 ]]; do
  sleep 0.01
  attempts=$((attempts + 1))
done
if kill -0 "$stale_lock_pid" 2>/dev/null; then
  fail "SIGKILL did not terminate the lock owner"
fi
[[ -L "$stale_lock_fixture/host.lock" &&
  -d "$stale_lock_owner" ]] ||
  fail "SIGKILL did not leave the published stale lock fixture"
touch "$stale_lock_release"
wait "$stale_lock_runner" 2>/dev/null || true
if ! run_fixture "$stale_lock_fixture"; then
  fail "verified SIGKILL-stale host lock was not reclaimed"
fi
[[ ! -e "$stale_lock_fixture/host.lock" &&
  ! -L "$stale_lock_fixture/host.lock" ]] ||
  fail "recovered run left its host lock"
[[ ! -e "$stale_lock_owner" ]] ||
  fail "reclaimed stale owner directory remained"

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

healthy_heartbeat_fixture="$(make_fixture)"
if ! PHASE5_FAKE_DELAY_MATCH=' /app/happylearn-backup prepare ' \
  PHASE5_FAKE_DELAY_SECONDS='2' \
  HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS='0.05' \
  run_fixture "$healthy_heartbeat_fixture"; then
  fail "healthy heartbeat killed its own bounded renewal"
fi
healthy_renewals="$(
  grep -Fc 'SQL PHASE5_QUERY_LEASE_RENEW' \
    "$healthy_heartbeat_fixture/docker.log"
)"
test "$healthy_renewals" -ge 2 ||
  fail "healthy heartbeat did not renew across a long action"

unprepared_failure_fixture="$(make_fixture)"
if PHASE5_FAKE_FAIL_SQL_MATCH='PHASE5_QUERY_ACTIVE_COUNTS' \
  PHASE5_FAKE_FAIL_BACKUP_FAIL=true \
  run_fixture "$unprepared_failure_fixture"; then
  fail "pre-prepare drain failure unexpectedly succeeded"
fi
grep -Fq 'SQL PHASE5_QUERY_UNPREPARED_FAIL' \
  "$unprepared_failure_fixture/docker.log" ||
  fail "pre-prepare failure had no durable queued-run fallback"

for blocked_sql in \
  PHASE5_QUERY_RUN \
  PHASE5_QUERY_ACTIVE_COUNTS \
  PHASE5_QUERY_LEASE_RENEW \
  PHASE5_QUERY_LEASE_RELEASE
do
  blocked_sql_fixture="$(make_fixture)"
  blocked_sql_started="$SECONDS"
  blocked_sql_heartbeat_interval='1'
  if [[ "$blocked_sql" == 'PHASE5_QUERY_LEASE_RENEW' ]]; then
    blocked_sql_heartbeat_interval='0.01'
  fi
  if PHASE5_FAKE_BLOCK_SQL_MATCH="$blocked_sql" \
    PHASE5_FAKE_BLOCK_SQL_SECONDS='4' \
    HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS="$blocked_sql_heartbeat_interval" \
    run_fixture "$blocked_sql_fixture"; then
    fail "hung database query was accepted: $blocked_sql"
  fi
  if ((SECONDS - blocked_sql_started >= 4)); then
    fail "hung database query exceeded its host deadline: $blocked_sql"
  fi
  test ! -e "$blocked_sql_fixture/host.lock" ||
    fail "hung database query left the host lock: $blocked_sql"
  if [[ "$blocked_sql" == 'PHASE5_QUERY_LEASE_RENEW' ]]; then
    sleep 4.1
    if grep -Fq 'SQL PHASE5_BLOCK_COMPLETED PHASE5_QUERY_LEASE_RENEW' \
      "$blocked_sql_fixture/docker.log"; then
      fail "stopped heartbeat left an in-flight renewal descendant"
    fi
  fi
done

heartbeat_teardown_fixture="$(make_fixture)"
if PHASE5_FAKE_BLOCK_SQL_MATCH='PHASE5_QUERY_LEASE_RELEASE' \
  PHASE5_FAKE_BLOCK_SQL_SECONDS='4' \
  HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS='0.05' \
  run_fixture "$heartbeat_teardown_fixture"; then
  fail "blocked durable release unexpectedly succeeded"
fi
renewals_before_teardown_wait="$(
  grep -Fc 'SQL PHASE5_QUERY_LEASE_RENEW' \
    "$heartbeat_teardown_fixture/docker.log" || true
)"
sleep 0.2
renewals_after_teardown_wait="$(
  grep -Fc 'SQL PHASE5_QUERY_LEASE_RENEW' \
    "$heartbeat_teardown_fixture/docker.log" || true
)"
test "$renewals_before_teardown_wait" -eq "$renewals_after_teardown_wait" ||
  fail "release failure left an orphaned lease heartbeat"

blocked_release_marker_fixture="$(make_fixture)"
blocked_release_marker_started="$SECONDS"
if PHASE5_FAKE_BLOCK_SQL_MATCH='PHASE5_RELEASE_LOCK' \
  PHASE5_FAKE_BLOCK_SQL_SECONDS='4' \
  run_fixture "$blocked_release_marker_fixture"; then
  fail "hung advisory release marker was accepted"
fi
if ((SECONDS - blocked_release_marker_started >= 4)); then
  fail "hung advisory release marker exceeded its host deadline"
fi
test ! -e "$blocked_release_marker_fixture/host.lock" ||
  fail "hung advisory release marker left the host lock"

blocked_retention_fixture="$(make_fixture)"
blocked_retention_started="$SECONDS"
if PHASE5_FAKE_DELAY_MATCH='/app/happylearn-backup-retention --repository local' \
  PHASE5_FAKE_DELAY_SECONDS='20' \
  HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS='1' \
  run_fixture "$blocked_retention_fixture"; then
  fail "hung retention database/tag plan was accepted"
fi
if ((SECONDS - blocked_retention_started >= 20)); then
  fail "hung retention database/tag plan exceeded its host deadline"
fi
test ! -e "$blocked_retention_fixture/host.lock" ||
  fail "hung retention database/tag plan left the host lock"

printf 'phase5 backup contract: PASS\n'
