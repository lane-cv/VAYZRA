#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TARGET="$ROOT/scripts/phase5-backup.sh"
COMPOSE="$ROOT/deploy/compose.dev.yml"
MAKEFILE="$ROOT/Makefile"
PACKAGE_JSON="$ROOT/package.json"
LIVE_FIXTURE="$ROOT/scripts/phase5-backup_live_test.sh"
LIVE_COMPOSE="$ROOT/deploy/compose.backup-live.yml"
E2E_LIVE_COMPOSE="$ROOT/deploy/compose.phase5-e2e-live.yml"
CONTRACT_TEMP_BASE="$(cd "${TMPDIR:-/tmp}" && pwd -P)"
CONTRACT_TEMP_ROOT="$(
  mktemp -d "$CONTRACT_TEMP_BASE/phase5-backup-contract-root.XXXXXX"
)"
ACTIVE_FIXTURE_PID=''
ACTIVE_FIXTURE_RELEASE_FILES=('')
ACTIVE_FIXTURE_FINISH_FILES=('')

cleanup_contract() {
  local path
  for path in "${ACTIVE_FIXTURE_RELEASE_FILES[@]}"; do
    [[ -n "$path" ]] && touch "$path"
  done
  if [[ -n "$ACTIVE_FIXTURE_PID" ]] &&
    command -v terminate_direct_fixture_child >/dev/null 2>&1; then
    terminate_direct_fixture_child "$ACTIVE_FIXTURE_PID" || true
  fi
  if command -v wait_for_file >/dev/null 2>&1; then
    for path in "${ACTIVE_FIXTURE_FINISH_FILES[@]}"; do
      [[ -z "$path" ]] || wait_for_file "$path" || true
    done
  fi
  if [[ -d "$CONTRACT_TEMP_ROOT" &&
    "$CONTRACT_TEMP_ROOT" == "$CONTRACT_TEMP_BASE/phase5-backup-contract-root."* ]]; then
    rm -rf "$CONTRACT_TEMP_ROOT"
  fi
}
trap cleanup_contract EXIT

register_active_fixture() {
  local pid="$1"
  shift
  [[ "$pid" =~ ^[1-9][0-9]*$ && -z "$ACTIVE_FIXTURE_PID" ]] ||
    return 1
  ACTIVE_FIXTURE_PID="$pid"
  ACTIVE_FIXTURE_RELEASE_FILES=("$@")
  ACTIVE_FIXTURE_FINISH_FILES=('')
}

register_active_fixture_finish() {
  ACTIVE_FIXTURE_FINISH_FILES+=("$1")
}

clear_active_fixture() {
  ACTIVE_FIXTURE_PID=''
  ACTIVE_FIXTURE_RELEASE_FILES=('')
  ACTIVE_FIXTURE_FINISH_FILES=('')
}

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
test -f "$E2E_LIVE_COMPOSE" ||
  fail 'fixed Phase 5 E2E live Compose override is absent'

require_literal "$TARGET" 'set -euo pipefail'
require_literal "$TARGET" 'Usage: scripts/phase5-backup.sh --project happylearn-dev --trigger scheduled|manual|pre_release'
require_literal "$TARGET" '[[ "$PROJECT" == "happylearn-dev" ]]'
require_pattern "$TARGET" 'scheduled\|manual\|pre_release'
require_literal "$TARGET" 'COMPOSE_FILE="$ROOT/deploy/compose.dev.yml"'
require_literal "$TARGET" \
  'E2E_LIVE_COMPOSE_FILE="$ROOT/deploy/compose.phase5-e2e-live.yml"'
require_literal "$TARGET" '--project-name "$EFFECTIVE_PROJECT"'
require_literal "$TARGET" 'arguments+=(--file "$LIVE_COMPOSE_FILE")'
require_literal "$TARGET" 'arguments+=(--file "$E2E_LIVE_COMPOSE_FILE")'
require_literal "$TARGET" 'HAPPYLEARN_BACKUP_LIVE_TEST'
require_literal "$TARGET" '^happylearn-phase5-live-[a-f0-9]{12}$'
require_literal "$TARGET" 'configure_live_context'
require_literal "$TARGET" 'owner_only_directory "$LIVE_ROOT/runtime-secrets"'
require_literal "$TARGET" '[[ "$LIVE_ROOT" == "$live_root" ]]'
require_literal "$TARGET" 'HAPPYLEARN_BACKUP_LIVE_ROOT="$LIVE_ROOT"'
require_literal "$TARGET" 'export HAPPYLEARN_BACKUP_LIVE_ROOT'
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
require_literal "$TARGET" 'system_holds_liveness_file'
require_literal "$TARGET" 'heartbeat.ready'
require_literal "$TARGET" 'heartbeat.ready.pending'
forbid_pattern "$TARGET" 'mkfifo[^[:cntrl:]]*heartbeat\.pid'
require_literal "$TARGET" 'command -v flock'
require_literal "$TARGET" '--conflict-exit-code 75'
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

for service in phase5-secrets-init postgres minio app worker; do
  require_pattern "$E2E_LIVE_COMPOSE" \
    "^[[:space:]]{2}${service}:$"
done
require_literal "$E2E_LIVE_COMPOSE" 'network_mode: "none"'
require_literal "$E2E_LIVE_COMPOSE" 'user: "0:0"'
require_literal "$E2E_LIVE_COMPOSE" 'read_only: true'
require_literal "$E2E_LIVE_COMPOSE" 'cap_drop:'
require_literal "$E2E_LIVE_COMPOSE" 'cap_add:'
require_literal "$E2E_LIVE_COMPOSE" '- CHOWN'
require_literal "$E2E_LIVE_COMPOSE" '- DAC_READ_SEARCH'
require_literal "$E2E_LIVE_COMPOSE" \
  'source: ${HAPPYLEARN_BACKUP_LIVE_ROOT:?}/runtime-secrets'
require_literal "$E2E_LIVE_COMPOSE" 'target: /secret-source'
require_literal "$E2E_LIVE_COMPOSE" 'create_host_path: false'
require_literal "$E2E_LIVE_COMPOSE" 'source: phase5_runtime_secrets'
require_literal "$E2E_LIVE_COMPOSE" 'target: /secret-target'
require_literal "$E2E_LIVE_COMPOSE" 'read_only: false'
forbid_pattern "$E2E_LIVE_COMPOSE" \
  'phase5_runtime_secrets:/secret-target'
require_literal "$E2E_LIVE_COMPOSE" 'chmod 0400'
require_literal "$E2E_LIVE_COMPOSE" 'chmod 0500'
require_literal "$E2E_LIVE_COMPOSE" 'PHASE5_E2E_SECRET_INIT'
require_literal "$E2E_LIVE_COMPOSE" 'environment: !override'
require_literal "$E2E_LIVE_COMPOSE" \
  'POSTGRES_PASSWORD_FILE: /run/phase5-secrets/password'
for consumer in postgres minio app worker; do
  require_literal "$E2E_LIVE_COMPOSE" \
    "subpath: ${consumer}"
done
require_literal "$E2E_LIVE_COMPOSE" \
  '. /run/phase5-secrets/runtime.env'
require_literal "$E2E_LIVE_COMPOSE" \
  'exec minio server /data'
require_literal "$E2E_LIVE_COMPOSE" 'exec /app/happylearn'
require_literal "$E2E_LIVE_COMPOSE" 'exec /app/happylearn-worker'
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
require_literal "$LIVE_FIXTURE" 'FIXTURE_TEMP_BASE='
require_literal "$LIVE_FIXTURE" 'HAPPYLEARN_BACKUP_LIVE_TEST'
require_literal "$LIVE_FIXTURE" '--file "$COMPOSE_LIVE_FILE"'
require_literal "$LIVE_FIXTURE" '--file "$COMPOSE_E2E_LIVE_FILE"'
forbid_pattern "$LIVE_FIXTURE" '--env-file'
require_literal "$LIVE_FIXTURE" \
  'volume-subpath=remote-s3,readonly'
require_literal "$LIVE_FIXTURE" \
  '. /run/phase5-secrets/runtime.env'
require_literal "$LIVE_FIXTURE" 'audit_container_metadata'
require_literal "$LIVE_FIXTURE" \
  '--exit-code-from phase5-secrets-init'
require_literal "$LIVE_FIXTURE" \
  '{{json .Config.Env}}|{{json .Config.Entrypoint}}|{{json .Config.Cmd}}'
require_literal "$LIVE_FIXTURE" \
  '{{json .HostConfig.Binds}}|{{json .HostConfig.Privileged}}|{{json .HostConfig.NetworkMode}}'
require_literal "$LIVE_FIXTURE" \
  '{{range .HostConfig.Mounts}}{{printf "%s|%s|%s|%t|" .Type .Source .Target .ReadOnly}}{{if .VolumeOptions}}{{printf "%s" .VolumeOptions.Subpath}}{{end}}{{printf "\n"}}{{end}}'
require_literal "$LIVE_FIXTURE" \
  '{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}'
require_literal "$LIVE_FIXTURE" 'runtime secret HostConfig mount'
forbid_pattern "$LIVE_FIXTURE" \
  '\{\{println \.Type "\|" \.Name "\|" \.Source'
require_literal "$LIVE_FIXTURE" 'phase5-e2e-secret-marker'
require_literal "$LIVE_FIXTURE" '/var/run/docker.sock'
require_literal "$LIVE_FIXTURE" 'EXTERNAL_SENTINEL_VOLUME'
require_literal "$LIVE_FIXTURE" 'create_external_sentinel'
require_literal "$LIVE_FIXTURE" 'verify_external_sentinel'
require_literal "$LIVE_FIXTURE" 'remove_external_sentinel'
require_literal "$LIVE_FIXTURE" '"$FIXTURE_ROOT/ca-context"'
require_literal "$LIVE_FIXTURE" \
  'type=bind,src=$FIXTURE_ROOT/server-certs,dst=/certs,readonly'
require_literal "$LIVE_FIXTURE" 'monitor_backup_runtime'
require_literal "$LIVE_FIXTURE" 'backup-storage-init|backup-secrets-init|backup'
require_literal "$LIVE_FIXTURE" 'local saw_heavy=false'
require_literal "$LIVE_FIXTURE" 'local exclusive_overlap=false'
require_literal "$LIVE_FIXTURE" '.Config.Cmd'
require_literal "$LIVE_FIXTURE" 'snapshot|verify|sync'
require_literal "$LIVE_FIXTURE" 'happylearn-backup-retention'
require_literal "$LIVE_FIXTURE" 'restic'
require_literal "$LIVE_FIXTURE" 'check'
require_literal "$LIVE_FIXTURE" 'runtime monitor missed a heavy backup stage'
require_literal "$LIVE_FIXTURE" 'worker and heavy backup stage overlapped'
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
grep -Eq '^[[:space:]]+init:[[:space:]]+true$' <<<"$backup_block" ||
  fail "backup service must enable an init reaper"
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
    --profile backup \
    --file "$COMPOSE" --file "$LIVE_COMPOSE" config
)" || fail "fixed live Compose override does not render"
if grep -Eq '^[[:space:]]+ports:' <<<"$live_rendered"; then
  fail "live Compose override retained a published host port"
fi
live_backup_block="$(
  awk '
    /^  backup:$/ { inside=1 }
    inside && /^  [a-zA-Z0-9_-]+:$/ && $1 != "backup:" { exit }
    inside { print }
  ' <<<"$live_rendered"
)"
grep -Eq '^[[:space:]]+init:[[:space:]]+true$' <<<"$live_backup_block" ||
  fail "rendered backup service must enable an init reaper"
if grep -Fq 'phase5-secrets-init' <<<"$live_rendered" ||
  grep -Fq 'phase5_runtime_secrets' <<<"$live_rendered"; then
  fail 'non-live Compose rendering unexpectedly loaded the E2E override'
fi

render_live_root="$CONTRACT_TEMP_ROOT/render-live"
mkdir -m 0700 "$render_live_root" "$render_live_root/runtime-secrets"
render_phase5_e2e_config() {
  local output="$1"
  shift
  HAPPYLEARN_AISTOR_LICENSE_FILE="$CONTRACT_TEMP_ROOT/compose.license" \
    HAPPYLEARN_BACKUP_LIVE_ROOT="$render_live_root" \
    docker compose \
      --project-name happylearn-phase5-live-012345abcdef \
      --profile backup \
      --file "$COMPOSE" \
      --file "$LIVE_COMPOSE" \
      --file "$E2E_LIVE_COMPOSE" \
      "$@" \
      config --format json >"$output"
}

validate_phase5_e2e_config() {
  node - "$1" "$render_live_root" <<'NODE'
const fs = require('node:fs')
const config = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
const expectedLiveRoot = process.argv[3]
const fail = (message) => { throw new Error(message) }
const expectedUsers = {
  postgres: '999:999',
  minio: '1000:0',
  app: '10001:10001',
  worker: '10002:10002',
}
const forbiddenKeys = new Set([
  'POSTGRES_PASSWORD',
  'MINIO_ROOT_USER',
  'MINIO_ROOT_PASSWORD',
  'HAPPYLEARN_DATABASE_URL',
  'HAPPYLEARN_LOGIN_THROTTLE_SECRET',
  'HAPPYLEARN_AI_MASTER_KEY',
  'HAPPYLEARN_MINIO_ACCESS_KEY',
  'HAPPYLEARN_MINIO_SECRET_KEY',
  'HAPPYLEARN_METRICS_BEARER_SECRET',
  'HAPPYLEARN_HOST_METRICS_HMAC_SECRET',
  'HAPPYLEARN_WEBHOOK_URL',
  'HAPPYLEARN_WEBHOOK_AUTHORIZATION',
  'RESTIC_PASSWORD',
  'AWS_ACCESS_KEY_ID',
  'AWS_SECRET_ACCESS_KEY',
])
const allowedFileKeys = new Map([
  ['POSTGRES_PASSWORD_FILE', '/run/phase5-secrets/password'],
  ['HAPPYLEARN_METRICS_BEARER_SECRET_FILE', '/run/secrets/metrics-bearer'],
  ['HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE', '/run/secrets/host-metrics-hmac'],
])
const services = config.services || {}
const init = services['phase5-secrets-init']
if (!init) fail('secret init service absent')
if (init.user !== '0:0' || init.network_mode !== 'none' ||
    init.read_only !== true || init.restart !== 'no') {
  fail('secret init isolation mismatch')
}
if (JSON.stringify(init.entrypoint) !== JSON.stringify([
  '/usr/bin/timeout',
  '--foreground',
  '--kill-after=10s',
  '120s',
  '/bin/sh',
  '-ceu',
])) fail('secret init entrypoint mismatch')
if (JSON.stringify(init.cap_drop) !== JSON.stringify(['ALL']) ||
    JSON.stringify(init.cap_add) !== JSON.stringify(['CHOWN', 'DAC_READ_SEARCH'])) {
  fail('secret init capabilities mismatch')
}
if (init.profiles) fail('secret init unexpectedly profiled')
const initSource = init.volumes.find((mount) => mount.target === '/secret-source')
const initTarget = init.volumes.find((mount) => mount.target === '/secret-target')
if (!initSource || initSource.type !== 'bind' ||
    initSource.source !== `${expectedLiveRoot}/runtime-secrets` ||
    initSource.read_only !== true ||
    initSource.bind?.create_host_path !== false) fail('secret init source mismatch')
if (!initTarget || initTarget.type !== 'volume' ||
    initTarget.source !== 'phase5_runtime_secrets' ||
    initTarget.read_only === true) fail('secret init target mismatch')
const initCommand = (init.command || []).join('\n')
const fileChmod = initCommand.indexOf('chmod 0400')
const fileChown = initCommand.indexOf('chown "$${owner}"')
const directoryChmod = initCommand.indexOf('chmod 0500')
const directoryChown = initCommand.indexOf('chown 1000:0')
if (!initCommand.includes('PHASE5_E2E_SECRET_INIT') ||
    fileChmod < 0 || fileChown < 0 || directoryChmod < 0 ||
    directoryChown < 0 || fileChmod > fileChown ||
    directoryChmod > directoryChown) {
  fail('secret init permission order mismatch')
}
const expectedExecutables = {
  minio: 'exec minio server /data',
  app: 'exec /app/happylearn',
  worker: 'exec /app/happylearn-worker',
}
for (const [name, expectedUser] of Object.entries(expectedUsers)) {
  const service = services[name]
  if (!service || service.user !== expectedUser) fail(`${name} user mismatch`)
  if (service.profiles) fail(`${name} unexpectedly profiled`)
  for (const [key, value] of Object.entries(service.environment || {})) {
    if (forbiddenKeys.has(key)) fail(`${name} leaked ${key}`)
    if (key.endsWith('_FILE') && allowedFileKeys.get(key) !== value) {
      fail(`${name} has an unapproved file environment`)
    }
  }
  const mount = (service.volumes || []).find(
    (candidate) => candidate.target === '/run/phase5-secrets',
  )
  if (!mount || mount.type !== 'volume' ||
      mount.source !== 'phase5_runtime_secrets' ||
      mount.read_only !== true ||
      mount.volume?.subpath !== name) fail(`${name} secret mount mismatch`)
  if (service.depends_on?.['phase5-secrets-init']?.condition !==
      'service_completed_successfully') fail(`${name} init dependency mismatch`)
  if (name === 'postgres') {
    if (service.environment?.POSTGRES_PASSWORD_FILE !==
        '/run/phase5-secrets/password' ||
        service.entrypoint != null) fail('postgres password-file contract mismatch')
  } else {
    if (JSON.stringify(service.entrypoint) !==
        JSON.stringify(['/bin/sh', '-ceu'])) fail(`${name} entrypoint mismatch`)
    const command = (service.command || []).join('\n')
    if (!command.includes('. /run/phase5-secrets/runtime.env') ||
        !command.includes(expectedExecutables[name])) {
      fail(`${name} source/exec mismatch`)
    }
  }
}
if (!config.volumes?.phase5_runtime_secrets ||
    config.volumes.phase5_runtime_secrets.labels?.[
      'io.happylearn.phase5.e2e-secrets'
    ] !== 'true') fail('labeled secret volume absent')
NODE
}

live_e2e_json="$CONTRACT_TEMP_ROOT/live-e2e.json"
render_phase5_e2e_config "$live_e2e_json" ||
  fail 'fixed Phase 5 E2E live Compose override does not render'
validate_phase5_e2e_config "$live_e2e_json" ||
  fail 'rendered Phase 5 E2E live Compose contract failed'

for mutation in \
  postgres:POSTGRES_PASSWORD \
  minio:MINIO_ROOT_PASSWORD \
  app:HAPPYLEARN_DATABASE_URL \
  app:HAPPYLEARN_LOGIN_THROTTLE_SECRET \
  app:HAPPYLEARN_AI_MASTER_KEY \
  app:HAPPYLEARN_MINIO_SECRET_KEY \
  app:HAPPYLEARN_WEBHOOK_URL \
  app:HAPPYLEARN_DATABASE_URL_FILE \
  worker:AWS_SECRET_ACCESS_KEY; do
  mutation_service="${mutation%%:*}"
  mutation_key="${mutation##*:}"
  mutation_override="$CONTRACT_TEMP_ROOT/mutation-${mutation_service}-${mutation_key}.yml"
  printf '%s\n' \
    'services:' \
    "  ${mutation_service}:" \
    '    environment:' \
    "      ${mutation_key}: phase5-e2e-secret-marker" \
    >"$mutation_override"
  mutation_json="$mutation_override.json"
  render_phase5_e2e_config "$mutation_json" --file "$mutation_override" ||
    fail "secret Config.Env mutation did not render: $mutation"
  if validate_phase5_e2e_config "$mutation_json" >/dev/null 2>&1; then
    fail "secret Config.Env mutation survived: $mutation"
  fi
done

missing_marker_override="$CONTRACT_TEMP_ROOT/mutation-init-missing-marker.yml"
printf '%s\n' \
  'services:' \
  '  phase5-secrets-init:' \
  '    command: !override' \
  '      - |' \
  '        chmod 0400 /tmp/file' \
  '        chown "$${owner}" /tmp/file' \
  '        chmod 0500 /tmp/directory' \
  '        chown 1000:0 /tmp/directory' \
  >"$missing_marker_override"
missing_marker_json="$missing_marker_override.json"
render_phase5_e2e_config "$missing_marker_json" \
  --file "$missing_marker_override" ||
  fail 'missing secret-init marker mutation did not render'
if validate_phase5_e2e_config "$missing_marker_json" >/dev/null 2>&1; then
  fail 'missing secret-init completion marker survived validation'
fi

reordered_permissions_override="$CONTRACT_TEMP_ROOT/mutation-init-permission-order.yml"
printf '%s\n' \
  'services:' \
  '  phase5-secrets-init:' \
  '    command: !override' \
  '      - |' \
  '        chown "$${owner}" /tmp/file' \
  '        chmod 0400 /tmp/file' \
  '        chown 1000:0 /tmp/directory' \
  '        chmod 0500 /tmp/directory' \
  '        printf "%s\n" PHASE5_E2E_SECRET_INIT' \
  >"$reordered_permissions_override"
reordered_permissions_json="$reordered_permissions_override.json"
render_phase5_e2e_config "$reordered_permissions_json" \
  --file "$reordered_permissions_override" ||
  fail 'reordered secret-init permissions mutation did not render'
if validate_phase5_e2e_config "$reordered_permissions_json" >/dev/null 2>&1; then
  fail 'chown-before-chmod secret-init mutation survived validation'
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

wait_for_process_gone() {
  local pid="$1"
  local attempts=0
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  while kill -0 "$pid" 2>/dev/null && [[ "$attempts" -lt 1000 ]]; do
    sleep 0.01
    attempts=$((attempts + 1))
  done
  ! kill -0 "$pid" 2>/dev/null
}

terminate_direct_fixture_child() {
  local pid="$1"
  local attempts=0
  local direct_child_running=false
  local running_pid
  local still_running
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  for running_pid in $(jobs -pr); do
    if [[ "$running_pid" == "$pid" ]]; then
      direct_child_running=true
      break
    fi
  done
  if [[ "$direct_child_running" == false ]]; then
    wait "$pid" 2>/dev/null || true
    return 0
  fi
  kill -KILL "$pid" 2>/dev/null || true
  while [[ "$attempts" -lt 1000 ]]; do
    still_running=false
    for running_pid in $(jobs -pr); do
      if [[ "$running_pid" == "$pid" ]]; then
        still_running=true
        break
      fi
    done
    [[ "$still_running" == false ]] && break
    sleep 0.01
    attempts=$((attempts + 1))
  done
  [[ "$still_running" == false ]] || return 1
  wait "$pid" 2>/dev/null || true
}

log_count() {
  local path="$1"
  local pattern="$2"
  local count
  count="$(grep -Fc "$pattern" "$path" 2>/dev/null || true)"
  [[ "$count" =~ ^[0-9]+$ ]] || return 1
  printf '%s' "$count"
}

wait_for_log_count_greater() {
  local path="$1"
  local pattern="$2"
  local baseline="$3"
  local attempts=0
  local count
  [[ "$baseline" =~ ^[0-9]+$ ]] || return 1
  while [[ "$attempts" -lt 500 ]]; do
    count="$(log_count "$path" "$pattern")" || return 1
    if [[ "$count" -gt "$baseline" ]]; then
      return 0
    fi
    sleep 0.01
    attempts=$((attempts + 1))
  done
  return 1
}

make_fixture() {
  local fixture
  fixture="$(mktemp -d "$CONTRACT_TEMP_ROOT/fixture.XXXXXX")"
  mkdir -m 0700 "$fixture/bin" "$fixture/secrets" \
    "$fixture/repository" "$fixture/state" "$fixture/secret-volume" \
    "$fixture/runtime-secrets"
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
    trap 'printf "%s\n" finished >"${PHASE5_FAKE_DELAY_RELEASE_FILE}.finished"' EXIT
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
      "$line" == *"$PHASE5_FAKE_BLOCK_SQL_MATCH"* &&
      (-z "${PHASE5_FAKE_BLOCK_SQL_ARM_FILE:-}" ||
        -e "$PHASE5_FAKE_BLOCK_SQL_ARM_FILE") ]]; then
      trap '' HUP TERM
      if [[ -n "${PHASE5_FAKE_BLOCK_SQL_RELEASE_FILE:-}" ]]; then
        printf '%s\n' started >"${PHASE5_FAKE_BLOCK_SQL_RELEASE_FILE}.started"
        attempts=0
        while [[ ! -e "$PHASE5_FAKE_BLOCK_SQL_RELEASE_FILE" &&
          "$attempts" -lt 500 ]]; do
          sleep 0.01
          attempts=$((attempts + 1))
        done
        [[ -e "$PHASE5_FAKE_BLOCK_SQL_RELEASE_FILE" ]] || exit 80
      else
        sleep "${PHASE5_FAKE_BLOCK_SQL_SECONDS:-3}"
      fi
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
        printf '%s\n' "${PHASE5_FAKE_LEASE_ACQUIRED_RESPONSE-acquired}"
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
        if [[ -n "${PHASE5_FAKE_RENEW_OWNER_PID_FILE:-}" &&
          -f "$PHASE5_FAKE_RENEW_OWNER_PID_FILE" ]]; then
          owner_pid="$(<"$PHASE5_FAKE_RENEW_OWNER_PID_FILE")"
          [[ "$owner_pid" =~ ^[1-9][0-9]*$ ]] || exit 79
          if ! kill -0 "$owner_pid" 2>/dev/null; then
            [[ -n "${PHASE5_FAKE_ORPHAN_RENEW_MARKER:-}" ]] || exit 79
            printf '%s\n' orphan_renew \
              >"$PHASE5_FAKE_ORPHAN_RENEW_MARKER"
            exit 79
          fi
        fi
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
  cat >"$fixture/bin/ps" <<'FAKE_PS'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$#" -eq 4 && "$1" == '-o' && "$2" == 'lstart=' &&
  "$3" == '-p' && "$4" =~ ^[1-9][0-9]*$ ]]; then
  if [[ -n "${PHASE5_FAKE_OWNER_IDENTITY_FAIL_AFTER_FIRST_FILE:-}" ]]; then
    if [[ -f "$PHASE5_FAKE_OWNER_IDENTITY_FAIL_AFTER_FIRST_FILE" ]]; then
      printf '%s\n' failed \
        >"${PHASE5_FAKE_OWNER_IDENTITY_FAIL_AFTER_FIRST_FILE}.failed"
      exit 83
    fi
    printf '%s\n' first \
      >"$PHASE5_FAKE_OWNER_IDENTITY_FAIL_AFTER_FIRST_FILE"
  fi
  if [[ -n "${PHASE5_FAKE_REUSED_PROCESS_IDENTITY_PID_FILE:-}" &&
    -f "$PHASE5_FAKE_REUSED_PROCESS_IDENTITY_PID_FILE" &&
    "$(<"$PHASE5_FAKE_REUSED_PROCESS_IDENTITY_PID_FILE")" == "$4" ]]; then
    printf 'phase5-contract-process-%s\n' "$4"
    exit 0
  fi
  kill -0 "$4" 2>/dev/null || exit 1
  printf 'phase5-contract-process-%s\n' "$4"
  exit 0
fi
if [[ "$#" -eq 4 && "$1" == '-o' && "$2" == 'ppid=' &&
  "$3" == '-p' && "$4" =~ ^[1-9][0-9]*$ ]]; then
  fixture="${PHASE5_FAKE_PROCESS_STATE_ROOT:-}"
  [[ -n "$fixture" && -d "$fixture" && ! -L "$fixture" ]] || exit 81
  fixture="$(cd "$fixture" && pwd -P)" || exit 81
  kill -0 "$4" 2>/dev/null || exit 81
  owner_directory="$(readlink "$fixture/host.lock" 2>/dev/null)" || exit 81
  [[ "$owner_directory" == "$fixture/host.lock.owner."?????? &&
    -d "$owner_directory" && ! -L "$owner_directory" ]] ||
    exit 81
  owner_pid="$(sed -n 's/^pid=//p' "$owner_directory/owner")"
  [[ "$owner_pid" =~ ^[1-9][0-9]*$ ]] || exit 81
  heartbeat_pid_file="$fixture/fake-heartbeat.pid"
  if [[ -f "$heartbeat_pid_file" && ! -L "$heartbeat_pid_file" ]]; then
    recorded_heartbeat_pid="$(<"$heartbeat_pid_file")"
    [[ "$recorded_heartbeat_pid" =~ ^[1-9][0-9]*$ ]] || exit 81
    if [[ "$recorded_heartbeat_pid" != "$4" ]]; then
      kill -0 "$recorded_heartbeat_pid" 2>/dev/null && exit 81
      printf '%s\n' "$4" >"$heartbeat_pid_file"
    fi
  elif [[ ! -e "$heartbeat_pid_file" && ! -L "$heartbeat_pid_file" ]]; then
    printf '%s\n' "$4" >"$heartbeat_pid_file"
    chmod 0600 "$heartbeat_pid_file"
  else
    exit 81
  fi
  if kill -0 "$owner_pid" 2>/dev/null; then
    printf '%s\n' "$owner_pid"
  else
    printf '%s\n' '1'
  fi
  exit 0
fi
exec /bin/ps "$@"
FAKE_PS
  chmod 0700 "$fixture/bin/ps"
  cat >"$fixture/bin/mv" <<'FAKE_MV'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$#" -eq 2 &&
  -n "${PHASE5_FAKE_HEARTBEAT_PUBLISH_FAIL_KIND:-}" &&
  -n "${PHASE5_FAKE_HEARTBEAT_PUBLISH_PID_MARKER:-}" ]]; then
  case "$PHASE5_FAKE_HEARTBEAT_PUBLISH_FAIL_KIND:$2" in
    "child_before_pid:"*/heartbeat.pid)
      identity_file="${PHASE5_FAKE_OWNER_IDENTITY_FAIL_AFTER_FIRST_FILE:-}"
      [[ -n "$identity_file" ]] || exit 82
      attempts=0
      while [[ ! -f "${identity_file}.failed" && "$attempts" -lt 500 ]]; do
        sleep 0.01
        attempts=$((attempts + 1))
      done
      [[ -f "${identity_file}.failed" ]] || exit 82
      value="$(<"$1")"
      [[ "$value" =~ ^[1-9][0-9]*$ ]] || exit 82
      printf '%s\n' "$value" >"$PHASE5_FAKE_HEARTBEAT_PUBLISH_PID_MARKER"
      exec /bin/mv "$@"
      ;;
    "ready:"*/heartbeat.ready)
      value="$(<"$1")"
      [[ "$value" =~ ^[1-9][0-9]*$ ]] || exit 82
      printf '%s\n' "$value" >"$PHASE5_FAKE_HEARTBEAT_PUBLISH_PID_MARKER"
      exit 82
      ;;
  esac
fi
exec /bin/mv "$@"
FAKE_MV
  chmod 0700 "$fixture/bin/mv"
  printf '%s\n' "$fixture"
}

install_linux_lock_tools() {
  local fixture="$1"
  cat >"$fixture/bin/uname" <<'FAKE_UNAME'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == '-s' || "$#" -eq 0 ]]; then
  printf '%s\n' Linux
  exit 0
fi
exec /usr/bin/uname "$@"
FAKE_UNAME
  cat >"$fixture/bin/flock" <<'FAKE_FLOCK'
#!/usr/bin/perl
use strict;
use warnings;
use Fcntl qw(LOCK_EX LOCK_NB);

my $conflict = 1;
my @operands;
while (@ARGV) {
  my $argument = shift @ARGV;
  if ($argument eq '--conflict-exit-code' || $argument eq '-E') {
    @ARGV or exit 64;
    $conflict = shift @ARGV;
    next;
  }
  next if $argument eq '--exclusive' || $argument eq '-x';
  next if $argument eq '--nonblock' || $argument eq '--nb' ||
    $argument eq '-n';
  push @operands, $argument;
}
@operands or exit 64;
my $handle;
if (@operands == 1 && $operands[0] =~ /^[0-9]+$/) {
  open($handle, '<&=', $operands[0]) or exit 70;
} else {
  open($handle, '<', $operands[0]) or exit 70;
}
flock($handle, LOCK_EX | LOCK_NB) or exit $conflict;
exit 0;
FAKE_FLOCK
  chmod 0700 "$fixture/bin/uname" "$fixture/bin/flock"
}

install_gnu_stat_shim() {
  local fixture="$1"
  cat >"$fixture/bin/stat" <<'FAKE_GNU_STAT'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == '-f' && "${2:-}" == '%d:%i' && "$#" -eq 3 ]]; then
  directory="$(dirname "$3")"
  entries="$(
    find "$directory" -mindepth 1 -maxdepth 1 -print | wc -l |
      tr -d '[:space:]'
  )"
  printf '%s:%s\n' "$((1000000 - entries))" 77
  exit 0
fi
if [[ "${1:-}" == '-f' ]]; then
  exit 1
fi
if [[ "${1:-}" == '-c' && "$#" -eq 3 ]]; then
  case "$(/usr/bin/uname -s)" in
    Darwin)
      case "$2" in
        '%d:%i') exec /usr/bin/stat -f '%d:%i' "$3" ;;
        '%a') exec /usr/bin/stat -f '%Lp' "$3" ;;
        '%u') exec /usr/bin/stat -f '%u' "$3" ;;
        '%s') exec /usr/bin/stat -f '%z' "$3" ;;
        *) exit 64 ;;
      esac
      ;;
    Linux) exec /usr/bin/stat "$@" ;;
    *) exit 64 ;;
  esac
fi
exec /usr/bin/stat "$@"
FAKE_GNU_STAT
  chmod 0700 "$fixture/bin/stat"
}

run_fixture() {
  local fixture="$1"
  local fail_match="${2:-}"
  local remote_result="${3:-success}"
  local run_response="${4:-queued|scheduled|11111111-1111-4111-8111-111111111111}"
  local -a fixture_environment=(
    "PATH=$fixture/bin:$PATH"
    "PHASE5_FAKE_DOCKER_LOG=${PHASE5_FAKE_DOCKER_LOG:-$fixture/docker.log}"
    "PHASE5_FAKE_CONTAINER_STATE=$fixture/sync-container.state"
    "PHASE5_FAKE_SYNC_TIMEOUT=${PHASE5_FAKE_SYNC_TIMEOUT:-}"
    "PHASE5_FAKE_DEGRADED_RUNS=${PHASE5_FAKE_DEGRADED_RUNS:-}"
    "PHASE5_FAKE_SECRET_VOLUME=$fixture/secret-volume"
    "PHASE5_FAKE_SECRET_COPY_FAIL_NAME=${PHASE5_FAKE_SECRET_COPY_FAIL_NAME:-}"
    "PHASE5_FAKE_FAIL_MATCH=$fail_match"
    "PHASE5_FAKE_REMOTE_RESULT=$remote_result"
    "PHASE5_FAKE_FAIL_SQL_MATCH=${PHASE5_FAKE_FAIL_SQL_MATCH:-}"
    "PHASE5_FAKE_BLOCK_SQL_MATCH=${PHASE5_FAKE_BLOCK_SQL_MATCH:-}"
    "PHASE5_FAKE_BLOCK_SQL_SECONDS=${PHASE5_FAKE_BLOCK_SQL_SECONDS:-3}"
    "PHASE5_FAKE_BLOCK_SQL_RELEASE_FILE=${PHASE5_FAKE_BLOCK_SQL_RELEASE_FILE:-}"
    "PHASE5_FAKE_BLOCK_SQL_ARM_FILE=${PHASE5_FAKE_BLOCK_SQL_ARM_FILE:-}"
    "PHASE5_FAKE_LEASE_ACQUIRED_RESPONSE=${PHASE5_FAKE_LEASE_ACQUIRED_RESPONSE-acquired}"
    "PHASE5_FAKE_DELAY_MATCH=${PHASE5_FAKE_DELAY_MATCH:-}"
    "PHASE5_FAKE_DELAY_SECONDS=${PHASE5_FAKE_DELAY_SECONDS:-3}"
    "PHASE5_FAKE_DELAY_RELEASE_FILE=${PHASE5_FAKE_DELAY_RELEASE_FILE:-}"
    "PHASE5_FAKE_BLOCK_LOCK_SECONDS=${PHASE5_FAKE_BLOCK_LOCK_SECONDS:-}"
    "PHASE5_FAKE_FAIL_BACKUP_FAIL=${PHASE5_FAKE_FAIL_BACKUP_FAIL:-}"
    "PHASE5_FAKE_RENEW_OWNER_PID_FILE=${PHASE5_FAKE_RENEW_OWNER_PID_FILE:-}"
    "PHASE5_FAKE_ORPHAN_RENEW_MARKER=${PHASE5_FAKE_ORPHAN_RENEW_MARKER:-}"
    "PHASE5_FAKE_REUSED_PROCESS_IDENTITY_PID_FILE=${PHASE5_FAKE_REUSED_PROCESS_IDENTITY_PID_FILE:-}"
    "PHASE5_FAKE_PROCESS_STATE_ROOT=$fixture"
    "PHASE5_FAKE_HEARTBEAT_PUBLISH_FAIL_KIND=${PHASE5_FAKE_HEARTBEAT_PUBLISH_FAIL_KIND:-}"
    "PHASE5_FAKE_HEARTBEAT_PUBLISH_PID_MARKER=${PHASE5_FAKE_HEARTBEAT_PUBLISH_PID_MARKER:-}"
    "PHASE5_FAKE_OWNER_IDENTITY_FAIL_AFTER_FIRST_FILE=${PHASE5_FAKE_OWNER_IDENTITY_FAIL_AFTER_FIRST_FILE:-}"
    "PHASE5_FAKE_RUN_RESPONSE=$run_response"
    "HAPPYLEARN_AISTOR_LICENSE_FILE=$fixture/minio.license"
    "HAPPYLEARN_BACKUP_SECRET_DIRECTORY=$fixture/secrets"
    "HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY=$fixture/repository"
    "HAPPYLEARN_BACKUP_STATE_DIRECTORY=$fixture/state"
    'HAPPYLEARN_BACKUP_AGE_RECIPIENT=age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp5m40h'
    'HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID=phase5-contract-key'
    "HAPPYLEARN_BACKUP_POLL_INTERVAL_SECONDS=${HAPPYLEARN_BACKUP_POLL_INTERVAL_SECONDS:-0.01}"
    'HAPPYLEARN_BACKUP_DRAIN_TIMEOUT_SECONDS=2'
    'HAPPYLEARN_BACKUP_READY_TIMEOUT_SECONDS=2'
    "HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS=${HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS:-1}"
    "HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS=${HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS:-2700}"
    "HAPPYLEARN_BACKUP_DATABASE_QUERY_TIMEOUT_SECONDS=${HAPPYLEARN_BACKUP_DATABASE_QUERY_TIMEOUT_SECONDS:-1}"
    'HAPPYLEARN_BACKUP_DATABASE_CONNECT_TIMEOUT_SECONDS=1'
    "HAPPYLEARN_BACKUP_LOCK_DIRECTORY=$fixture/host.lock"
  )
  local -a fixture_command=(
    "$TARGET" --project happylearn-dev --trigger scheduled
  )
  if [[ "${PHASE5_FAKE_EXEC_FIXTURE:-}" == true ]]; then
    exec env "${fixture_environment[@]}" "${fixture_command[@]}"
  fi
  env "${fixture_environment[@]}" "${fixture_command[@]}"
}

FIXTURE_BACKGROUND_PID=''

start_fixture_background() {
  PHASE5_FAKE_EXEC_FIXTURE=true \
    run_fixture "$@" >/dev/null 2>&1 &
  FIXTURE_BACKGROUND_PID="$!"
}

if [[ "${PHASE5_CONTRACT_REAPED_PID_PROBE:-}" == true ]]; then
  reaped_probe_marker="${PHASE5_CONTRACT_REAPED_PID_MARKER:?}"
  (
    exit 0
  ) &
  reaped_probe_pid="$!"
  wait "$reaped_probe_pid"
  register_active_fixture \
    "$reaped_probe_pid" "${reaped_probe_marker}.release"
  kill() {
    printf '%s\n' "$*" >"$reaped_probe_marker"
    return 0
  }
  exit 0
fi

if [[ "${PHASE5_CONTRACT_EARLY_FAIL_PROBE:-}" == true ]]; then
  early_fail_release="${PHASE5_CONTRACT_EARLY_FAIL_RELEASE:?}"
  early_fail_pid_marker="${PHASE5_CONTRACT_EARLY_FAIL_PID_MARKER:?}"
  early_fail_fixture="$(make_fixture)"
  PHASE5_FAKE_DELAY_MATCH='backup-storage-init' \
    PHASE5_FAKE_DELAY_RELEASE_FILE="$early_fail_release" \
    start_fixture_background "$early_fail_fixture"
  early_fail_pid="$FIXTURE_BACKGROUND_PID"
  register_active_fixture "$early_fail_pid" "$early_fail_release"
  register_active_fixture_finish "${early_fail_release}.finished"
  wait_for_file "${early_fail_release}.started" ||
    fail "early-fail cleanup probe did not start its background fixture"
  printf '%s\n' "$early_fail_pid" >"$early_fail_pid_marker"
  fail "intentional early-fail cleanup probe"
fi

background_fixture_call_count="$(
  grep -Ec '^[[:space:]]+run_fixture .*&[[:space:]]*$' "${BASH_SOURCE[0]}"
)"
[[ "$background_fixture_call_count" -eq 1 ]] ||
  fail "background fixtures bypass start_fixture_background"

reaped_pid_kill_marker="$CONTRACT_TEMP_ROOT/reaped-pid.kill"
if ! PHASE5_CONTRACT_REAPED_PID_PROBE=true \
  PHASE5_CONTRACT_REAPED_PID_MARKER="$reaped_pid_kill_marker" \
  bash "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
  fail "reaped PID cleanup probe failed"
fi
[[ -f "${reaped_pid_kill_marker}.release" ]] ||
  fail "reaped PID cleanup probe did not reach its EXIT trap"
[[ ! -e "$reaped_pid_kill_marker" ]] ||
  fail "reaped PID cleanup attempted to signal a reused PID"

early_fail_release="$CONTRACT_TEMP_ROOT/early-fail.release"
early_fail_pid_marker="$CONTRACT_TEMP_ROOT/early-fail.pid"
if PHASE5_CONTRACT_EARLY_FAIL_PROBE=true \
  PHASE5_CONTRACT_EARLY_FAIL_RELEASE="$early_fail_release" \
  PHASE5_CONTRACT_EARLY_FAIL_PID_MARKER="$early_fail_pid_marker" \
  bash "${BASH_SOURCE[0]}" >/dev/null 2>&1; then
  fail "early-fail cleanup probe unexpectedly succeeded"
fi
[[ -f "$early_fail_release" &&
  -f "${early_fail_release}.started" &&
  -f "${early_fail_release}.finished" ]] ||
  fail "early-fail EXIT trap did not release and finish its background fixture"
early_fail_pid="$(<"$early_fail_pid_marker")"
[[ "$early_fail_pid" =~ ^[1-9][0-9]*$ ]] ||
  fail "early-fail cleanup probe recorded an invalid PID"
! kill -0 "$early_fail_pid" 2>/dev/null ||
  fail "early-fail EXIT trap left its background fixture running"

for heartbeat_publish_failure in child_before_pid ready; do
  heartbeat_publish_fixture="$(make_fixture)"
  heartbeat_publish_marker="$heartbeat_publish_fixture/heartbeat-publish.pid"
  heartbeat_identity_state=''
  if [[ "$heartbeat_publish_failure" == child_before_pid ]]; then
    heartbeat_identity_state="$heartbeat_publish_fixture/heartbeat-owner-identity"
  fi
  heartbeat_publish_started="$SECONDS"
  if PHASE5_FAKE_HEARTBEAT_PUBLISH_FAIL_KIND="$heartbeat_publish_failure" \
    PHASE5_FAKE_HEARTBEAT_PUBLISH_PID_MARKER="$heartbeat_publish_marker" \
    PHASE5_FAKE_OWNER_IDENTITY_FAIL_AFTER_FIRST_FILE="$heartbeat_identity_state" \
    run_fixture "$heartbeat_publish_fixture"; then
    fail "heartbeat $heartbeat_publish_failure publication failure was accepted"
  fi
  if ((SECONDS - heartbeat_publish_started >= 5)); then
    fail "heartbeat $heartbeat_publish_failure publication failure exceeded five seconds"
  fi
  heartbeat_publish_pid="$(<"$heartbeat_publish_marker")"
  [[ "$heartbeat_publish_pid" =~ ^[1-9][0-9]*$ ]] ||
    fail "heartbeat $heartbeat_publish_failure failure captured an invalid PID"
  ! kill -0 "$heartbeat_publish_pid" 2>/dev/null ||
    fail "heartbeat $heartbeat_publish_failure failure left an orphan process"
  [[ ! -e "$heartbeat_publish_fixture/host.lock" &&
    ! -L "$heartbeat_publish_fixture/host.lock" ]] ||
    fail "heartbeat $heartbeat_publish_failure failure left its host lock"
done

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
grep -Fq -- "--project-name happylearn-dev --file $COMPOSE" "$success_log" ||
  fail 'production execution omitted the fixed base Compose file'
if grep -Fq -- "--file $LIVE_COMPOSE" "$success_log" ||
  grep -Fq -- "--file $E2E_LIVE_COMPOSE" "$success_log"; then
  fail 'production execution loaded a live-only Compose override'
fi
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
  "--project-name happylearn-phase5-live-012345abcdef --file $COMPOSE --file $LIVE_COMPOSE --file $E2E_LIVE_COMPOSE" \
  "$live_context_fixture/docker.log" ||
  fail "live execution did not use the unique project and both fixed overrides"

invalid_live_context_fixture="$(make_fixture)"
if HAPPYLEARN_BACKUP_LIVE_TEST='1' \
  HAPPYLEARN_BACKUP_LIVE_PROJECT='happylearn-dev' \
  HAPPYLEARN_BACKUP_LIVE_ROOT="$invalid_live_context_fixture" \
  run_fixture "$invalid_live_context_fixture"; then
  fail "unsafe live execution context was accepted"
fi
test ! -s "$invalid_live_context_fixture/docker.log" ||
  fail "unsafe live execution context mutated Compose state"

symlink_live_context_fixture="$(make_fixture)"
symlink_live_parent="$CONTRACT_TEMP_ROOT/live-parent-link"
ln -s "$(dirname "$symlink_live_context_fixture")" "$symlink_live_parent"
symlink_live_root="$symlink_live_parent/$(basename "$symlink_live_context_fixture")"
if HAPPYLEARN_BACKUP_LIVE_TEST='1' \
  HAPPYLEARN_BACKUP_LIVE_PROJECT='happylearn-phase5-live-012345abcdef' \
  HAPPYLEARN_BACKUP_LIVE_ROOT="$symlink_live_root" \
  run_fixture "$symlink_live_context_fixture"; then
  fail 'live root with a symlink ancestor was accepted'
fi
test ! -s "$symlink_live_context_fixture/docker.log" ||
  fail 'live root with a symlink ancestor reached Compose'

noncanonical_live_context_fixture="$(make_fixture)"
noncanonical_live_root="$(
  dirname "$noncanonical_live_context_fixture"
)/../$(basename "$(dirname "$noncanonical_live_context_fixture")")/$(basename "$noncanonical_live_context_fixture")"
if HAPPYLEARN_BACKUP_LIVE_TEST='1' \
  HAPPYLEARN_BACKUP_LIVE_PROJECT='happylearn-phase5-live-012345abcdef' \
  HAPPYLEARN_BACKUP_LIVE_ROOT="$noncanonical_live_root" \
  run_fixture "$noncanonical_live_context_fixture"; then
  fail 'non-canonical live root was accepted'
fi
test ! -s "$noncanonical_live_context_fixture/docker.log" ||
  fail 'non-canonical live root reached Compose'

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
if run_fixture "$degraded_retention_fixture" \
  '/app/happylearn-backup-retention --repository local' 'outage'; then
  fail "remote degradation disguised local retention failure as degraded"
fi
grep -Fq '/app/happylearn-backup fail --run-id 11111111-1111-4111-8111-111111111111 --category retention' \
  "$degraded_retention_fixture/docker.log" ||
  fail "remote degradation did not preserve local retention failure"
if grep -Fq '/app/happylearn-backup fail --run-id 11111111-1111-4111-8111-111111111111 --category remote_unavailable' \
  "$degraded_retention_fixture/docker.log"; then
  fail "local retention failure recorded a false remote_unavailable terminal"
fi

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
  start_fixture_background "$active_lock_fixture"
active_lock_runner="$FIXTURE_BACKGROUND_PID"
register_active_fixture "$active_lock_runner" "$active_lock_release"
register_active_fixture_finish "${active_lock_release}.finished"
if ! wait_for_file "${active_lock_release}.started"; then
  fail "active lock fixture did not reach the blocked one-shot"
fi
[[ -L "$active_lock_fixture/host.lock" ]] ||
  fail "active lock was not atomically published as a symlink"
active_lock_owner="$(readlink "$active_lock_fixture/host.lock")"
active_log_lines_before="$(
  wc -l <"$active_lock_fixture/docker.log" | tr -d '[:space:]'
)"
if run_fixture "$active_lock_fixture"; then
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
wait_for_file "${active_lock_release}.finished" ||
  fail "active lock descendant did not finish after release"
clear_active_fixture
[[ ! -e "$active_lock_fixture/host.lock" &&
  ! -L "$active_lock_fixture/host.lock" ]] ||
  fail "active owner cleanup left its host lock"

stale_lock_fixture="$(make_fixture)"
stale_lock_release="$stale_lock_fixture/stale-lock.release"
PHASE5_FAKE_DELAY_MATCH='backup-storage-init' \
  PHASE5_FAKE_DELAY_RELEASE_FILE="$stale_lock_release" \
  start_fixture_background "$stale_lock_fixture"
stale_lock_runner="$FIXTURE_BACKGROUND_PID"
register_active_fixture "$stale_lock_runner" "$stale_lock_release"
register_active_fixture_finish "${stale_lock_release}.finished"
if ! wait_for_file "${stale_lock_release}.started"; then
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
[[ "$stale_lock_pid" == "$stale_lock_runner" ]] ||
  fail "stale lock owner was not the direct fixture child"
terminate_direct_fixture_child "$stale_lock_runner" ||
  fail "SIGKILL did not terminate the lock owner"
[[ -L "$stale_lock_fixture/host.lock" &&
  -d "$stale_lock_owner" ]] ||
  fail "SIGKILL did not leave the published stale lock fixture"
stale_log_lines_before="$(
  wc -l <"$stale_lock_fixture/docker.log" | tr -d '[:space:]'
)"
if run_fixture "$stale_lock_fixture"; then
  fail "inherited liveness descriptor was treated as stale"
fi
stale_log_lines_after="$(
  wc -l <"$stale_lock_fixture/docker.log" | tr -d '[:space:]'
)"
test "$stale_log_lines_before" -eq "$stale_log_lines_after" ||
  fail "inherited liveness lock rejection accessed Compose"
[[ -L "$stale_lock_fixture/host.lock" &&
  "$(readlink "$stale_lock_fixture/host.lock")" == "$stale_lock_owner" ]] ||
  fail "inherited liveness owner lock was replaced"
touch "$stale_lock_release"
wait_for_file "${stale_lock_release}.finished" ||
  fail "inherited liveness holder did not finish after release"
clear_active_fixture
if ! run_fixture "$stale_lock_fixture"; then
  fail "verified SIGKILL-stale host lock was not reclaimed"
fi
[[ ! -e "$stale_lock_fixture/host.lock" &&
  ! -L "$stale_lock_fixture/host.lock" ]] ||
  fail "recovered run left its host lock"
[[ ! -e "$stale_lock_owner" ]] ||
  fail "reclaimed stale owner directory remained"

post_heartbeat_fixture="$(make_fixture)"
post_heartbeat_release="$post_heartbeat_fixture/post-heartbeat.release"
post_heartbeat_owner_pid_file="$post_heartbeat_fixture/post-heartbeat.owner.pid"
post_heartbeat_orphan_renew="$post_heartbeat_fixture/orphan-renew.detected"
post_heartbeat_second_log="$post_heartbeat_fixture/second-run.log"
: >"$post_heartbeat_second_log"
PHASE5_FAKE_DELAY_MATCH=' /app/happylearn-backup prepare ' \
  PHASE5_FAKE_DELAY_RELEASE_FILE="$post_heartbeat_release" \
  PHASE5_FAKE_RENEW_OWNER_PID_FILE="$post_heartbeat_owner_pid_file" \
  PHASE5_FAKE_ORPHAN_RENEW_MARKER="$post_heartbeat_orphan_renew" \
  PHASE5_FAKE_REUSED_PROCESS_IDENTITY_PID_FILE="$post_heartbeat_owner_pid_file" \
  HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS='0.05' \
  start_fixture_background "$post_heartbeat_fixture"
post_heartbeat_runner="$FIXTURE_BACKGROUND_PID"
register_active_fixture "$post_heartbeat_runner" "$post_heartbeat_release"
register_active_fixture_finish "${post_heartbeat_release}.finished"
if ! wait_for_file "${post_heartbeat_release}.started"; then
  fail "post-heartbeat fixture did not reach the blocked external descendant"
fi
post_heartbeat_owner="$(readlink "$post_heartbeat_fixture/host.lock")"
post_heartbeat_owner_pid="$(
  sed -n 's/^pid=//p' "$post_heartbeat_owner/owner"
)"
[[ "$post_heartbeat_owner_pid" =~ ^[1-9][0-9]*$ ]] ||
  fail "post-heartbeat owner PID was invalid"
[[ "$post_heartbeat_owner_pid" == "$post_heartbeat_runner" ]] ||
  fail "post-heartbeat owner was not the direct fixture child"
printf '%s\n' "$post_heartbeat_owner_pid" \
  >"$post_heartbeat_owner_pid_file"
post_heartbeat_renew_baseline="$(
  log_count "$post_heartbeat_fixture/docker.log" \
    'SQL PHASE5_QUERY_LEASE_RENEW'
)"
if ! wait_for_log_count_greater \
  "$post_heartbeat_fixture/docker.log" \
  'SQL PHASE5_QUERY_LEASE_RENEW' \
  "$post_heartbeat_renew_baseline"; then
  fail "heartbeat did not renew while the external descendant was blocked"
fi
if ! terminate_direct_fixture_child "$post_heartbeat_runner"; then
  fail "post-heartbeat direct child did not terminate"
fi
post_heartbeat_second_rejected=true
if PHASE5_FAKE_DOCKER_LOG="$post_heartbeat_second_log" \
  run_fixture "$post_heartbeat_fixture"; then
  post_heartbeat_second_rejected=false
fi
post_heartbeat_owner_preserved=true
if [[ ! -L "$post_heartbeat_fixture/host.lock" ||
  "$(readlink "$post_heartbeat_fixture/host.lock")" != \
    "$post_heartbeat_owner" ]]; then
  post_heartbeat_owner_preserved=false
fi
touch "$post_heartbeat_release"
wait_for_file "${post_heartbeat_release}.finished" ||
  fail "post-heartbeat external descendant did not finish after release"
clear_active_fixture
post_heartbeat_reclaimed=false
attempts=0
while [[ "$attempts" -lt 100 ]]; do
  if run_fixture "$post_heartbeat_fixture"; then
    post_heartbeat_reclaimed=true
    break
  fi
  sleep 0.01
  attempts=$((attempts + 1))
done
[[ "$post_heartbeat_second_rejected" == true ]] ||
  fail "active post-heartbeat descendant lock was stolen"
test ! -s "$post_heartbeat_second_log" ||
  fail "post-heartbeat lock rejection accessed Compose"
[[ "$post_heartbeat_owner_preserved" == true ]] ||
  fail "post-heartbeat lock rejection replaced the published owner"
test ! -e "$post_heartbeat_orphan_renew" ||
  fail "orphan heartbeat renewed after its original owner died"
[[ "$post_heartbeat_reclaimed" == true ]] ||
  fail "post-heartbeat host lock was not reclaimable after descendants exited"
[[ ! -e "$post_heartbeat_fixture/host.lock" &&
  ! -L "$post_heartbeat_fixture/host.lock" ]] ||
  fail "post-heartbeat recovery left its host lock"

inflight_renew_fixture="$(make_fixture)"
inflight_external_release="$inflight_renew_fixture/inflight-external.release"
inflight_renew_release="$inflight_renew_fixture/inflight-renew.release"
inflight_renew_arm="$inflight_renew_fixture/inflight-renew.arm"
inflight_owner_pid_file="$inflight_renew_fixture/inflight-owner.pid"
inflight_second_log="$inflight_renew_fixture/inflight-second.log"
inflight_ttl_log="$inflight_renew_fixture/inflight-ttl.log"
: >"$inflight_second_log"
: >"$inflight_ttl_log"
PHASE5_FAKE_DELAY_MATCH=' /app/happylearn-backup prepare ' \
  PHASE5_FAKE_DELAY_RELEASE_FILE="$inflight_external_release" \
  PHASE5_FAKE_BLOCK_SQL_MATCH='PHASE5_QUERY_LEASE_RENEW' \
  PHASE5_FAKE_BLOCK_SQL_RELEASE_FILE="$inflight_renew_release" \
  PHASE5_FAKE_BLOCK_SQL_ARM_FILE="$inflight_renew_arm" \
  PHASE5_FAKE_REUSED_PROCESS_IDENTITY_PID_FILE="$inflight_owner_pid_file" \
  HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS='0.05' \
  HAPPYLEARN_BACKUP_DATABASE_QUERY_TIMEOUT_SECONDS='10' \
  start_fixture_background "$inflight_renew_fixture"
inflight_runner="$FIXTURE_BACKGROUND_PID"
register_active_fixture \
  "$inflight_runner" "$inflight_external_release" "$inflight_renew_release"
register_active_fixture_finish "${inflight_external_release}.finished"
if ! wait_for_file "${inflight_external_release}.started"; then
  fail "in-flight renewal fixture did not reach its external descendant"
fi
inflight_owner="$(readlink "$inflight_renew_fixture/host.lock")"
inflight_owner_pid="$(
  sed -n 's/^pid=//p' "$inflight_owner/owner"
)"
[[ "$inflight_owner_pid" =~ ^[1-9][0-9]*$ ]] ||
  fail "in-flight renewal owner PID was invalid"
[[ "$inflight_owner_pid" == "$inflight_runner" ]] ||
  fail "in-flight renewal owner was not the direct fixture child"
inflight_heartbeat_pid="$(
  read_heartbeat_pid="$inflight_owner/heartbeat.pid"
  [[ -f "$read_heartbeat_pid" && ! -L "$read_heartbeat_pid" ]] || exit 1
  printf '%s' "$(<"$read_heartbeat_pid")"
)"
[[ "$inflight_heartbeat_pid" =~ ^[1-9][0-9]*$ ]] ||
  fail "in-flight heartbeat PID was invalid"
printf '%s\n' "$inflight_owner_pid" >"$inflight_owner_pid_file"
touch "$inflight_renew_arm"
if ! wait_for_file "${inflight_renew_release}.started"; then
  fail "heartbeat renewal did not enter its bounded in-flight window"
fi
inflight_renew_baseline="$(
  log_count "$inflight_renew_fixture/docker.log" \
    'SQL PHASE5_QUERY_LEASE_RENEW'
)"
if ! terminate_direct_fixture_child "$inflight_runner"; then
  fail "in-flight renewal direct child did not terminate"
fi
touch "$inflight_renew_release"
if ! wait_for_log_count_greater \
  "$inflight_renew_fixture/docker.log" \
  'SQL PHASE5_QUERY_LEASE_RENEW' \
  "$inflight_renew_baseline"; then
  fail "the already in-flight renewal did not complete"
fi
if ! wait_for_process_gone "$inflight_heartbeat_pid"; then
  fail "in-flight heartbeat did not exit after owner death"
fi
inflight_renew_after_heartbeat_exit="$(
  log_count "$inflight_renew_fixture/docker.log" \
    'SQL PHASE5_QUERY_LEASE_RENEW'
)"
[[ "$inflight_renew_after_heartbeat_exit" -eq \
  $((inflight_renew_baseline + 1)) ]] ||
  fail "owner death allowed more than one already in-flight renewal"
inflight_second_rejected=true
if PHASE5_FAKE_DOCKER_LOG="$inflight_second_log" \
  run_fixture "$inflight_renew_fixture"; then
  inflight_second_rejected=false
fi
[[ "$inflight_second_rejected" == true &&
  ! -s "$inflight_second_log" &&
  -L "$inflight_renew_fixture/host.lock" &&
  "$(readlink "$inflight_renew_fixture/host.lock")" == "$inflight_owner" ]] ||
  fail "in-flight renewal descendant host lock was stolen"
touch "$inflight_external_release"
wait_for_file "${inflight_external_release}.finished" ||
  fail "in-flight renewal external descendant did not finish"
clear_active_fixture
if PHASE5_FAKE_LEASE_ACQUIRED_RESPONSE='' \
  PHASE5_FAKE_DOCKER_LOG="$inflight_ttl_log" \
  run_fixture "$inflight_renew_fixture"; then
  fail "host-lock recovery bypassed the durable database lease TTL"
fi
grep -Fq 'SQL PHASE5_QUERY_LEASE_ACQUIRED' "$inflight_ttl_log" ||
  fail "database lease TTL was not consulted after host-lock recovery"
if grep -Fq ' /app/happylearn-backup prepare ' "$inflight_ttl_log"; then
  fail "database lease TTL rejection reached backup mutation"
fi
grep -Fq 'SQL PHASE5_QUERY_LEASE_RELEASE' \
  "$inflight_renew_fixture/docker.log" &&
  fail "owner death unsafely compensated by releasing the durable lease"
[[ ! -e "$inflight_renew_fixture/host.lock" &&
  ! -L "$inflight_renew_fixture/host.lock" ]] ||
  fail "database lease TTL rejection left its recovered host lock"

gnu_identity_fixture="$(make_fixture)"
install_linux_lock_tools "$gnu_identity_fixture"
install_gnu_stat_shim "$gnu_identity_fixture"
printf '%s\n' first >"$gnu_identity_fixture/identity-first"
printf '%s\n' second >"$gnu_identity_fixture/identity-second"
sed -e '$d' "$TARGET" >"$gnu_identity_fixture/identity-source.sh"
gnu_identity_result="$(
  PATH="$gnu_identity_fixture/bin:$PATH" \
  PHASE5_IDENTITY_TARGET="$gnu_identity_fixture/identity-source.sh" \
  PHASE5_IDENTITY_FIRST="$gnu_identity_fixture/identity-first" \
  PHASE5_IDENTITY_SECOND="$gnu_identity_fixture/identity-second" \
    bash -c '
      source "$PHASE5_IDENTITY_TARGET"
      first_before="$(portable_file_identity "$PHASE5_IDENTITY_FIRST")"
      second="$(portable_file_identity "$PHASE5_IDENTITY_SECOND")"
      printf "%s\n" third >"${PHASE5_IDENTITY_FIRST}.third"
      first_after="$(portable_file_identity "$PHASE5_IDENTITY_FIRST")"
      printf "%s|%s|%s\n" "$first_before" "$second" "$first_after"
    '
)" || fail "GNU stat identity probe failed to execute"
IFS='|' read -r gnu_first_before gnu_second gnu_first_after \
  <<<"$gnu_identity_result"
gnu_identity_failed=false
[[ "$gnu_first_before" != "$gnu_second" ]] ||
  gnu_identity_failed=true
[[ "$gnu_first_before" == "$gnu_first_after" ]] ||
  gnu_identity_failed=true
gnu_cleanup_failed=false
if ! run_fixture "$gnu_identity_fixture"; then
  gnu_cleanup_failed=true
fi
[[ ! -e "$gnu_identity_fixture/host.lock" &&
  ! -L "$gnu_identity_fixture/host.lock" ]] ||
  gnu_cleanup_failed=true

linux_flock_failure_fixture="$(make_fixture)"
install_linux_lock_tools "$linux_flock_failure_fixture"
cat >"$linux_flock_failure_fixture/bin/flock" <<'FAKE_FAILED_FLOCK'
#!/usr/bin/env bash
exit 69
FAKE_FAILED_FLOCK
chmod 0700 "$linux_flock_failure_fixture/bin/flock"
if run_fixture "$linux_flock_failure_fixture"; then
  fail "Linux flock preflight failure was accepted"
fi
test ! -s "$linux_flock_failure_fixture/docker.log" ||
  fail "Linux flock failure reached Compose"
[[ ! -e "$linux_flock_failure_fixture/host.lock" &&
  ! -L "$linux_flock_failure_fixture/host.lock" ]] ||
  fail "Linux flock failure left a published host lock"

linux_holder_fixture="$(make_fixture)"
install_linux_lock_tools "$linux_holder_fixture"
cat >"$linux_holder_fixture/bin/lsof" <<'FAKE_INVISIBLE_LSOF'
#!/usr/bin/env bash
set -euo pipefail
pid=''
while [[ "$#" -gt 0 ]]; do
  if [[ "$1" == '-p' && "$#" -gt 1 ]]; then
    pid="$2"
    shift 2
    continue
  fi
  shift
done
if [[ -n "$pid" ]]; then
  printf 'p%s\n' "$pid"
  exit 0
fi
exit 1
FAKE_INVISIBLE_LSOF
chmod 0700 "$linux_holder_fixture/bin/lsof"
linux_holder_release="$linux_holder_fixture/linux-holder.release"
PHASE5_FAKE_DELAY_MATCH='backup-storage-init' \
  PHASE5_FAKE_DELAY_RELEASE_FILE="$linux_holder_release" \
  start_fixture_background "$linux_holder_fixture"
linux_holder_runner="$FIXTURE_BACKGROUND_PID"
register_active_fixture "$linux_holder_runner" "$linux_holder_release"
register_active_fixture_finish "${linux_holder_release}.finished"
if ! wait_for_file "${linux_holder_release}.started"; then
  fail "Linux holder fixture did not reach the blocked descendant"
fi
linux_holder_owner="$(readlink "$linux_holder_fixture/host.lock")"
linux_holder_pid="$(
  sed -n 's/^pid=//p' "$linux_holder_owner/owner"
)"
[[ "$linux_holder_pid" == "$linux_holder_runner" ]] ||
  fail "Linux holder owner was not the direct fixture child"
if ! terminate_direct_fixture_child "$linux_holder_runner"; then
  fail "Linux holder owner survived SIGKILL"
fi
linux_holder_log_before="$(
  wc -l <"$linux_holder_fixture/docker.log" | tr -d '[:space:]'
)"
linux_holder_rejected=true
if run_fixture "$linux_holder_fixture"; then
  linux_holder_rejected=false
fi
linux_holder_log_after="$(
  wc -l <"$linux_holder_fixture/docker.log" | tr -d '[:space:]'
)"
touch "$linux_holder_release"
wait_for_file "${linux_holder_release}.finished" ||
  fail "Linux inherited holder did not finish after release"
clear_active_fixture
linux_holder_failed=false
if [[ "$linux_holder_rejected" != true ||
  "$linux_holder_log_before" -ne "$linux_holder_log_after" ||
  ! -L "$linux_holder_fixture/host.lock" ||
  "$(readlink "$linux_holder_fixture/host.lock")" != "$linux_holder_owner" ]]; then
  linux_holder_failed=true
fi
attempts=0
while ! run_fixture "$linux_holder_fixture" &&
  [[ "$attempts" -lt 100 ]]; do
  sleep 0.01
  attempts=$((attempts + 1))
done
if [[ "$attempts" -ge 100 ]]; then
  fail "Linux lock was not reclaimable after its inherited holder exited"
fi
if [[ "$gnu_identity_failed" == true ||
  "$gnu_cleanup_failed" == true ||
  "$linux_holder_failed" == true ]]; then
  fail "GNU identity=${gnu_identity_failed} cleanup=${gnu_cleanup_failed}; Linux inherited holder=${linux_holder_failed}"
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
  blocked_sql_block_seconds='4'
  if [[ "$blocked_sql" == 'PHASE5_QUERY_LEASE_RENEW' ]]; then
    blocked_sql_heartbeat_interval='0.01'
  elif [[ "$blocked_sql" == 'PHASE5_QUERY_LEASE_RELEASE' ]]; then
    # The release failure is retried during cleanup. Keep the synthetic block
    # comfortably beyond both bounded attempts so integer SECONDS rounding
    # cannot make the host-deadline assertion flaky.
    blocked_sql_block_seconds='8'
  fi
  if PHASE5_FAKE_BLOCK_SQL_MATCH="$blocked_sql" \
    PHASE5_FAKE_BLOCK_SQL_SECONDS="$blocked_sql_block_seconds" \
    HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS="$blocked_sql_heartbeat_interval" \
    run_fixture "$blocked_sql_fixture"; then
    fail "hung database query was accepted: $blocked_sql"
  fi
  if ((SECONDS - blocked_sql_started >= blocked_sql_block_seconds)); then
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
