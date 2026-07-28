#!/usr/bin/env bash
set -euo pipefail

readonly USAGE='Usage: scripts/phase5-backup.sh --project happylearn-dev --trigger scheduled|manual|pre_release'
readonly OPERATIONS_ADVISORY_KEY='845103120'
readonly BACKUP_ADVISORY_KEY='845103121'

PROJECT=''
TRIGGER=''
ROOT=''
COMPOSE_FILE=''
LOCK_DIRECTORY=''
LOCK_HELD=false
LEASE_FIFO=''
LEASE_OUTPUT=''
LEASE_PID=''
LEASE_FD_OPEN=false
LEASE_OWNER_ID=''
LEASE_TOKEN=''
LEASE_DURABLE=false
LEASE_HEARTBEAT_PID=''
LEASE_HEARTBEAT_FAILED=''
RUN_ID=''
WORKER_STOPPED=false
AISTOR_STOPPED=false
TERMINAL_RECORDED=false
REMOTE_ENABLED=false
REMOTE_DEGRADED=false
RECOVERY_UNSAFE=false
ALLOW_LOST_LEASE_ACTIONS=false
CLEANING_UP=false
FAILURE_CATEGORY='internal'
LOCAL_SNAPSHOT_ID=''
REMOTE_SNAPSHOT_ID=''
declare -a LOCAL_PROTECTED_TAGS=()
declare -a REMOTE_PROTECTED_TAGS=()

SERVICE_STOP_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_SERVICE_STOP_TIMEOUT_SECONDS:-60}"
DRAIN_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_DRAIN_TIMEOUT_SECONDS:-600}"
READY_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_READY_TIMEOUT_SECONDS:-180}"
POLL_INTERVAL_SECONDS="${HAPPYLEARN_BACKUP_POLL_INTERVAL_SECONDS:-2}"
HEARTBEAT_INTERVAL_SECONDS="${HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS:-60}"
EXTERNAL_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS:-2700}"

usage_error() {
  printf '%s\n' "$USAGE" >&2
  exit 2
}

safe_log() {
  printf 'phase5_backup: %s\n' "$1" >&2
}

valid_uint() {
  [[ "$1" =~ ^[1-9][0-9]*$ ]]
}

validate_arguments() {
  [[ "$#" -eq 4 ]] || usage_error
  [[ "$1" == '--project' && "$3" == '--trigger' ]] || usage_error
  PROJECT="$2"
  TRIGGER="$4"
  [[ "$PROJECT" == "happylearn-dev" ]] || usage_error
  case "$TRIGGER" in
    scheduled|manual|pre_release) ;;
    *) usage_error ;;
  esac
  valid_uint "$SERVICE_STOP_TIMEOUT_SECONDS" || usage_error
  valid_uint "$DRAIN_TIMEOUT_SECONDS" || usage_error
  valid_uint "$READY_TIMEOUT_SECONDS" || usage_error
  [[ "$POLL_INTERVAL_SECONDS" =~ ^(0\.[0-9]*[1-9][0-9]*|[1-9][0-9]*(\.[0-9]+)?)$ ]] ||
    usage_error
  [[ "$HEARTBEAT_INTERVAL_SECONDS" =~ ^(0\.[0-9]*[1-9][0-9]*|[1-9][0-9]*(\.[0-9]+)?)$ ]] ||
    usage_error
  valid_uint "$EXTERNAL_TIMEOUT_SECONDS" || usage_error
  [[ "$EXTERNAL_TIMEOUT_SECONDS" -lt 7200 ]] || usage_error
}

resolve_root() {
  local script_directory
  script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
  ROOT="$(cd "$script_directory/.." && pwd -P)"
  COMPOSE_FILE="$ROOT/deploy/compose.dev.yml"
  [[ -f "$COMPOSE_FILE" && -f "$ROOT/Dockerfile.backup" ]] ||
    usage_error
}

portable_mode() {
  local path="$1"
  if stat -f '%Lp' "$path" >/dev/null 2>&1; then
    stat -f '%Lp' "$path"
  else
    stat -c '%a' "$path"
  fi
}

owner_only_directory() {
  local path="$1"
  [[ -d "$path" && ! -L "$path" && "$(portable_mode "$path")" == '700' ]]
}

owner_only_secret() {
  local path="$1"
  local size
  [[ -f "$path" && ! -L "$path" && "$(portable_mode "$path")" == '400' ]] ||
    return 1
  if stat -f '%z' "$path" >/dev/null 2>&1; then
    size="$(stat -f '%z' "$path")"
  else
    size="$(stat -c '%s' "$path")"
  fi
  [[ "$size" -ge 1 && "$size" -le 4096 ]]
}

validate_paths_and_secrets() {
  : "${HAPPYLEARN_AISTOR_LICENSE_FILE:?missing AIStor license file}"
  : "${HAPPYLEARN_BACKUP_SECRET_DIRECTORY:?missing backup secret directory}"
  : "${HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY:?missing local backup repository directory}"
  : "${HAPPYLEARN_BACKUP_STATE_DIRECTORY:?missing backup workflow state directory}"
  : "${HAPPYLEARN_BACKUP_AGE_RECIPIENT:?missing Age recipient}"
  : "${HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID:?missing backup encryption key ID}"

  [[ -f "$HAPPYLEARN_AISTOR_LICENSE_FILE" &&
    ! -L "$HAPPYLEARN_AISTOR_LICENSE_FILE" &&
    -s "$HAPPYLEARN_AISTOR_LICENSE_FILE" ]] ||
    return 1
  owner_only_directory "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" || return 1
  owner_only_directory "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" || return 1
  owner_only_directory "$HAPPYLEARN_BACKUP_STATE_DIRECTORY" || return 1
  owner_only_secret "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/database_password" ||
    return 1
  owner_only_secret "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_repository" ||
    return 1
  owner_only_secret "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_password" ||
    return 1
  [[ "$(<"$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_repository")" == '/repository' ]] ||
    return 1
  [[ "$HAPPYLEARN_BACKUP_AGE_RECIPIENT" =~ ^age1[023456789ac-hj-np-z]{20,100}$ ]] ||
    return 1
  [[ "$HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID" =~ ^[A-Za-z0-9._:-]{1,128}$ ]] ||
    return 1
  remote_configuration_complete
}

remote_configuration_complete() {
  local names=(
    remote_repository
    remote_password
    remote_access_key_id
    remote_secret_access_key
  )
  local present=0
  local name
  for name in "${names[@]}"; do
    if [[ -e "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/$name" ]]; then
      owner_only_secret "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/$name" ||
        return 1
      present=$((present + 1))
    fi
  done
  if [[ "$present" -eq 0 ]]; then
    REMOTE_ENABLED=false
    return 0
  fi
  [[ "$present" -eq 4 ]] || return 1
  local repository
  repository="$(<"$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/remote_repository")"
  [[ "$repository" =~ ^s3:https://[^/?#]+/[^?#]+$ ]] || return 1
  REMOTE_ENABLED=true
}

acquire_host_lock() {
  LOCK_DIRECTORY="${HAPPYLEARN_BACKUP_LOCK_DIRECTORY:-${TMPDIR:-/tmp}/happylearn-phase5-backup-${PROJECT}.lock}"
  [[ "$LOCK_DIRECTORY" == /* && ! -e "$LOCK_DIRECTORY" ]] || return 1
  mkdir "$LOCK_DIRECTORY"
  LOCK_HELD=true
  chmod 0700 "$LOCK_DIRECTORY"
  LEASE_FIFO="$LOCK_DIRECTORY/operational.stdin"
  LEASE_OUTPUT="$LOCK_DIRECTORY/operational.stdout"
}

compose() {
  docker compose --project-name "$PROJECT" --file "$COMPOSE_FILE" "$@"
}

initialize_backup_mounts() {
  run_guarded_external 300 \
    compose run --rm --no-deps backup-storage-init
  run_guarded_external 300 \
    compose run --rm --no-deps backup-secrets-init
}

verify_backup_mount_ownership() {
  run_guarded_external 120 \
    compose run --rm --no-deps --entrypoint /bin/sh backup -eu -c '
      test "$(stat -c "%u:%g:%a" /repository)" = "10003:0:700"
      test "$(stat -c "%u:%g:%a" /state)" = "10003:0:700"
      test "$(stat -c "%u:%g:%a" /run/secrets)" = "10003:0:700"
      for name in database_password local_repository local_password; do
        test "$(stat -c "%u:%g:%a" "/run/secrets/${name}")" = "10003:0:400"
      done
      for name in remote_repository remote_password remote_access_key_id \
        remote_secret_access_key; do
        if test -e "/run/secrets/${name}"; then
          test "$(stat -c "%u:%g:%a" "/run/secrets/${name}")" = "10003:0:400"
        fi
      done
    '
}

database_query() {
  compose exec -T postgres psql \
    --username happylearn \
    --dbname happylearn \
    --no-psqlrc \
    --tuples-only \
    --no-align \
    --set ON_ERROR_STOP=1
}

queue_or_select_run() {
  local key_expression
  case "$TRIGGER" in
    scheduled)
      key_expression="to_char(clock_timestamp() AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD')"
      ;;
    manual|pre_release)
      key_expression="'host-' || gen_random_uuid()::text"
      ;;
  esac
  local selected
  selected="$(
    database_query <<SQL
-- PHASE5_QUERY_RUN
BEGIN;
SELECT pg_advisory_xact_lock(${BACKUP_ADVISORY_KEY});
WITH existing AS MATERIALIZED (
  SELECT state,trigger_kind,id
  FROM backup_runs
  WHERE trigger_kind='${TRIGGER}'
    AND idempotency_key=${key_expression}
  ORDER BY requested_at DESC,id DESC
  LIMIT 1
), active AS MATERIALIZED (
  SELECT state,trigger_kind,id
  FROM backup_runs
  WHERE state NOT IN ('succeeded','degraded','failed')
  ORDER BY requested_at,id
  LIMIT 1
), created AS (
  INSERT INTO backup_runs(
    id,idempotency_key,trigger_kind,state,requested_at
  )
  SELECT gen_random_uuid(),${key_expression},'${TRIGGER}','queued',clock_timestamp()
  WHERE NOT EXISTS (SELECT 1 FROM existing)
    AND NOT EXISTS (SELECT 1 FROM active)
  RETURNING state,trigger_kind,id
), selected AS (
  SELECT * FROM existing
  UNION ALL SELECT * FROM active WHERE NOT EXISTS (SELECT 1 FROM existing)
  UNION ALL SELECT * FROM created
)
SELECT state || '|' || trigger_kind || '|' || id::text
FROM selected
LIMIT 1;
COMMIT;
SQL
  )"
  local state selected_trigger selected_id extra
  IFS='|' read -r state selected_trigger selected_id extra <<<"$selected"
  [[ -z "${extra:-}" &&
    "$selected_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ &&
    "$selected_trigger" == "$TRIGGER" ]] || return 1
  case "$state" in
    queued|draining|snapshotting|encrypting|verifying|syncing)
      RUN_ID="$selected_id"
      ;;
    succeeded|degraded)
      TERMINAL_RECORDED=true
      return 2
      ;;
    *) return 1 ;;
  esac
}

wait_for_marker() {
  local marker="$1"
  local deadline_seconds="$2"
  local deadline=$((SECONDS + deadline_seconds))
  while ((SECONDS < deadline)); do
    if grep -Fq "$marker" "$LEASE_OUTPUT" 2>/dev/null; then
      return 0
    fi
    if [[ -n "$LEASE_PID" ]] && ! kill -0 "$LEASE_PID" 2>/dev/null; then
      return 1
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
  return 1
}

start_operational_lock_session() {
  mkfifo "$LEASE_FIFO"
  : >"$LEASE_OUTPUT"
  chmod 0600 "$LEASE_OUTPUT"
  set -m
  database_query <"$LEASE_FIFO" >"$LEASE_OUTPUT" 2>/dev/null &
  LEASE_PID="$!"
  set +m
  exec 9>"$LEASE_FIFO"
  LEASE_FD_OPEN=true
  printf '%s\n' \
    "SELECT pg_advisory_lock(${OPERATIONS_ADVISORY_KEY}); -- PHASE5_HOLD_LOCK" \
    "SELECT 'PHASE5_LEASE_LOCKED';" >&9
  if ! wait_for_marker 'PHASE5_LEASE_LOCKED' "$DRAIN_TIMEOUT_SECONDS"; then
    abort_operational_lock_session
    return 1
  fi
}

abort_operational_lock_session() {
  if [[ "$LEASE_FD_OPEN" == true ]]; then
    exec 9>&-
    LEASE_FD_OPEN=false
  fi
  if [[ -n "$LEASE_PID" ]]; then
    terminate_external_group "$LEASE_PID"
    LEASE_PID=''
  fi
}

prepare_lease_values() {
  local values
  values="$(
    database_query <<'SQL'
-- PHASE5_QUERY_LEASE_VALUES
SELECT gen_random_uuid()::text || '|' ||
       encode(gen_random_bytes(32),'hex');
SQL
  )"
  local extra
  IFS='|' read -r LEASE_OWNER_ID LEASE_TOKEN extra <<<"$values"
  [[ -z "${extra:-}" &&
    "$LEASE_OWNER_ID" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ &&
    "$LEASE_TOKEN" =~ ^[0-9a-f]{64}$ ]]
}

acquire_durable_lease() {
  local acquired
  acquired="$(
    database_query <<SQL
-- PHASE5_QUERY_LEASE_ACQUIRED
WITH previous AS MATERIALIZED (
  SELECT mode,owner_id,lease_expires_at
  FROM operational_modes
  WHERE singleton_id=true
  FOR UPDATE
), acquired AS (
  UPDATE operational_modes AS modes
  SET mode='draining',
      owner_id='${LEASE_OWNER_ID}'::uuid,
      lease_token_hash=digest(decode('${LEASE_TOKEN}','hex'),'sha256'),
      lease_expires_at=clock_timestamp()+interval '2 hours',
      entered_at=clock_timestamp(),
      updated_at=clock_timestamp(),
      version=version+1
  FROM previous
  WHERE modes.singleton_id=true
    AND (
      previous.mode='normal'
      OR previous.lease_expires_at <= clock_timestamp()
    )
  RETURNING modes.singleton_id
), takeover_audit AS (
  INSERT INTO audit_logs(
    actor_user_id,action,target_type,target_id,metadata,request_id,ip
  )
  SELECT NULL,'operations.lease_taken_over','operational_mode','global',
         '{}'::jsonb,'phase5-backup-host',NULL
  FROM previous,acquired
  WHERE previous.mode<>'normal'
  RETURNING id
)
SELECT 'acquired' FROM acquired;
SQL
  )"
  [[ "$acquired" == 'acquired' ]] || return 1
  LEASE_DURABLE=true
}

transition_operational_mode() {
  local mode="$1"
  case "$mode" in draining|backup|release) ;; *) return 1 ;; esac
  local changed
  changed="$(
    database_query <<SQL
-- PHASE5_QUERY_LEASE_TRANSITION
UPDATE operational_modes
SET mode='${mode}',
    lease_expires_at=clock_timestamp()+interval '2 hours',
    updated_at=clock_timestamp(),
    version=version+1
WHERE singleton_id=true
  AND owner_id='${LEASE_OWNER_ID}'::uuid
  AND lease_token_hash=digest(decode('${LEASE_TOKEN}','hex'),'sha256')
  AND lease_expires_at>clock_timestamp()
RETURNING 'changed';
SQL
  )"
  [[ "$changed" == 'changed' ]]
}

renew_operational_lease() {
  local renewed
  renewed="$(
    database_query <<SQL
-- PHASE5_QUERY_LEASE_RENEW
UPDATE operational_modes
SET lease_expires_at=clock_timestamp()+interval '2 hours',
    updated_at=clock_timestamp(),
    version=version+1
WHERE singleton_id=true
  AND owner_id='${LEASE_OWNER_ID}'::uuid
  AND lease_token_hash=digest(decode('${LEASE_TOKEN}','hex'),'sha256')
  AND lease_expires_at>clock_timestamp()
RETURNING 'renewed';
SQL
  )"
  [[ "$renewed" == 'renewed' ]]
}

start_lease_heartbeat() {
  LEASE_HEARTBEAT_FAILED="$LOCK_DIRECTORY/heartbeat.failed"
  [[ ! -e "$LEASE_HEARTBEAT_FAILED" ]] || return 1
  (
    while sleep "$HEARTBEAT_INTERVAL_SECONDS"; do
      if ! renew_operational_lease; then
        printf '%s\n' 'lease_lost' >"$LEASE_HEARTBEAT_FAILED"
        exit 1
      fi
    done
  ) &
  LEASE_HEARTBEAT_PID="$!"
}

assert_lease_heartbeat() {
  if [[ -z "$LEASE_HEARTBEAT_PID" ||
    -e "$LEASE_HEARTBEAT_FAILED" ]] ||
    ! kill -0 "$LEASE_HEARTBEAT_PID" 2>/dev/null ||
    ! renew_operational_lease; then
    FAILURE_CATEGORY='lease_lost'
    return 1
  fi
}

terminate_external_group() {
  local pid="$1"
  kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
  sleep "$POLL_INTERVAL_SECONDS"
  kill -KILL "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

run_guarded_external() {
  local timeout_seconds="$1"
  shift
  valid_uint "$timeout_seconds" || return 1
  local deadline=$((SECONDS + timeout_seconds))
  local pid
  set -m
  ( "$@" ) &
  pid="$!"
  set +m
  while kill -0 "$pid" 2>/dev/null; do
    if [[ "$ALLOW_LOST_LEASE_ACTIONS" == false &&
      "$LEASE_DURABLE" == true ]] &&
      { [[ -e "$LEASE_HEARTBEAT_FAILED" ]] ||
        [[ -z "$LEASE_HEARTBEAT_PID" ]] ||
        ! kill -0 "$LEASE_HEARTBEAT_PID" 2>/dev/null; }; then
      FAILURE_CATEGORY='lease_lost'
      terminate_external_group "$pid"
      return 1
    fi
    if ((SECONDS >= deadline)); then
      FAILURE_CATEGORY='timeout'
      terminate_external_group "$pid"
      return 1
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
  if wait "$pid"; then
    return 0
  else
    return $?
  fi
}

stop_lease_heartbeat() {
  if [[ -n "$LEASE_HEARTBEAT_PID" ]]; then
    kill "$LEASE_HEARTBEAT_PID" 2>/dev/null || true
    wait "$LEASE_HEARTBEAT_PID" 2>/dev/null || true
    LEASE_HEARTBEAT_PID=''
  fi
}

durable_active_count() {
  database_query <<'SQL'
-- PHASE5_QUERY_ACTIVE_COUNTS
SELECT
  (SELECT count(*) FROM ai_runs WHERE status='streaming') +
  (SELECT count(*) FROM file_processing_jobs WHERE state='running') +
  (SELECT count(*) FROM outbox_events
   WHERE lease_owner IS NOT NULL AND lease_until>clock_timestamp()) +
  (SELECT count(*) FROM file_processing_artifacts
   WHERE cleanup_lease_owner IS NOT NULL
     AND cleanup_lease_until>clock_timestamp());
SQL
}

wait_for_durable_drain() {
  local deadline=$((SECONDS + DRAIN_TIMEOUT_SECONDS))
  local active
  while ((SECONDS < deadline)); do
    active="$(durable_active_count)" || return 1
    [[ "$active" =~ ^[0-9]+$ ]] || return 1
    if [[ "$active" == '0' ]]; then
      return 0
    fi
    renew_operational_lease || return 1
    sleep "$POLL_INTERVAL_SECONDS"
  done
  FAILURE_CATEGORY='drain_timeout'
  return 1
}

backup_command() {
  run_guarded_external "$EXTERNAL_TIMEOUT_SECONDS" \
    bounded_backup_compose "$@"
}

bounded_backup_compose() {
  compose run --rm --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /app/happylearn-backup "$@"
}

stop_snapshot_services() {
  FAILURE_CATEGORY='object_store_stop'
  WORKER_STOPPED=true
  run_guarded_external "$((SERVICE_STOP_TIMEOUT_SECONDS + 30))" \
    compose stop --timeout "$SERVICE_STOP_TIMEOUT_SECONDS" worker
  AISTOR_STOPPED=true
  run_guarded_external "$((SERVICE_STOP_TIMEOUT_SECONDS + 30))" \
    compose stop --timeout "$SERVICE_STOP_TIMEOUT_SECONDS" minio
}

wait_for_authenticated_aistor() {
  local deadline=$((SECONDS + READY_TIMEOUT_SECONDS))
  while ((SECONDS < deadline)); do
    if run_guarded_external 30 \
      compose exec -T app curl --fail --silent --show-error \
      http://127.0.0.1:8080/api/v1/health/ready >/dev/null 2>&1; then
      return 0
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
  return 1
}

restart_stopped_services() {
  if [[ "$AISTOR_STOPPED" == true ]]; then
    FAILURE_CATEGORY='object_store_restart'
    if ! run_guarded_external "$READY_TIMEOUT_SECONDS" \
      compose up --detach --no-deps minio; then
      RECOVERY_UNSAFE=true
      return 1
    fi
    if ! wait_for_authenticated_aistor; then
      RECOVERY_UNSAFE=true
      return 1
    fi
    AISTOR_STOPPED=false
  fi
  if [[ "$WORKER_STOPPED" == true ]]; then
    if ! run_guarded_external 60 compose start worker; then
      RECOVERY_UNSAFE=true
      return 1
    fi
    WORKER_STOPPED=false
  fi
}

local_snapshot_id() {
  database_query <<SQL
-- PHASE5_QUERY_LOCAL_SNAPSHOT
SELECT local_snapshot_id
FROM backup_runs
WHERE id='${RUN_ID}'::uuid
  AND state IN ('verifying','syncing')
  AND local_snapshot_id ~ '^[0-9a-f]{64}$';
SQL
}

remote_snapshot_id() {
  database_query <<SQL
-- PHASE5_QUERY_REMOTE_RESULT
SELECT remote_snapshot_id
FROM backup_runs
WHERE id='${RUN_ID}'::uuid
  AND state='syncing'
  AND remote_snapshot_id ~ '^[0-9a-f]{64}$';
SQL
}

local_restic() {
  run_guarded_external "$EXTERNAL_TIMEOUT_SECONDS" \
    compose run --rm --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" restic \
    --no-cache \
    --repository-file /run/secrets/local_repository \
    --password-file /run/secrets/local_password \
    "$@"
}

remote_restic() {
  run_guarded_external "$EXTERNAL_TIMEOUT_SECONDS" \
    compose run --rm --no-deps --entrypoint /bin/sh backup \
    -eu -c '
      deadline="$1"
      shift
      AWS_ACCESS_KEY_ID="$(sed -n "1p" /run/secrets/remote_access_key_id)"
      AWS_SECRET_ACCESS_KEY="$(sed -n "1p" /run/secrets/remote_secret_access_key)"
      RESTIC_REPOSITORY="$(sed -n "1p" /run/secrets/remote_repository)"
      RESTIC_PASSWORD="$(sed -n "1p" /run/secrets/remote_password)"
      export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY RESTIC_REPOSITORY RESTIC_PASSWORD
      exec /usr/bin/timeout --foreground --kill-after=10s "$deadline" restic \
        --no-cache "$@"
    ' phase5-remote-restic "${EXTERNAL_TIMEOUT_SECONDS}s" "$@"
}

restic_check() {
  local repository="$1"
  case "$repository" in
    local) local_restic check --read-data ;;
    remote) remote_restic check --read-data ;;
    *) return 1 ;;
  esac
}

ensure_repository() {
  local repository="$1"
  local probe_status
  case "$repository" in
    local)
      if local_restic cat config >/dev/null 2>&1; then
        return 0
      else
        probe_status=$?
      fi
      [[ "$probe_status" -eq 10 ]] || return "$probe_status"
      local_restic init >/dev/null 2>&1 &&
        local_restic cat config >/dev/null 2>&1
      ;;
    remote)
      if remote_restic cat config >/dev/null 2>&1; then
        return 0
      else
        probe_status=$?
      fi
      [[ "$probe_status" -eq 10 ]] || return "$probe_status"
      remote_restic init >/dev/null 2>&1 &&
        remote_restic cat config >/dev/null 2>&1
      ;;
    *) return 1 ;;
  esac
}

protected_batch_tags() {
  local repository="$1"
  local days="$2"
  local snapshot_column
  local terminal_states
  case "$repository" in
    local)
      snapshot_column='local_snapshot_id'
      terminal_states="'succeeded','degraded'"
      ;;
    remote)
      snapshot_column='remote_snapshot_id'
      terminal_states="'succeeded'"
      ;;
    *) return 1 ;;
  esac
  [[ "$days" == '30' ]] || return 1
  database_query <<SQL
-- PHASE5_QUERY_PROTECTED_TAGS
WITH candidates AS (
  SELECT id,0 AS priority
  FROM backup_runs
  WHERE id='${RUN_ID}'::uuid
    AND ${snapshot_column} ~ '^[0-9a-f]{64}$'
  UNION ALL
  SELECT id,1
  FROM backup_runs
  WHERE trigger_kind='pre_release'
    AND state IN (${terminal_states})
    AND ${snapshot_column} ~ '^[0-9a-f]{64}$'
    AND finished_at>=clock_timestamp()-interval '${days} days'
    AND finished_at<=clock_timestamp()
  UNION ALL
  SELECT id,2
  FROM (
    SELECT id
    FROM backup_runs
    WHERE state IN (${terminal_states})
      AND ${snapshot_column} ~ '^[0-9a-f]{64}$'
    ORDER BY finished_at DESC,id DESC
    LIMIT 1
  ) AS latest_good
)
SELECT 'happylearn-batch:' || id::text
FROM candidates
GROUP BY id
ORDER BY min(priority),id
LIMIT 513;
SQL
}

load_protected_tags() {
  local repository="$1"
  local output
  output="$(protected_batch_tags "$repository" 30)" || return 1
  [[ -n "$output" ]] || return 1
  local tag
  local -a parsed=()
  while IFS= read -r tag; do
    [[ "$tag" =~ ^happylearn-batch:[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
      return 1
    parsed+=("$tag")
    [[ "${#parsed[@]}" -le 512 ]] || return 1
  done <<<"$output"
  if [[ "$repository" == 'local' ]]; then
    LOCAL_PROTECTED_TAGS=("${parsed[@]}")
  else
    REMOTE_PROTECTED_TAGS=("${parsed[@]}")
  fi
}

protect_pre_release() {
  local days="$1"
  [[ "$days" == '30' ]] || return 1
  load_protected_tags local
  if [[ "$REMOTE_ENABLED" == true && "$REMOTE_DEGRADED" == false ]]; then
    load_protected_tags remote
  fi
}

protect_last_good() {
  local current="happylearn-batch:${RUN_ID}"
  local tag
  local found=false
  for tag in "${LOCAL_PROTECTED_TAGS[@]}"; do
    if [[ "$tag" == "$current" ]]; then
      found=true
    fi
  done
  [[ "$found" == true ]] || return 1
  if [[ "$REMOTE_ENABLED" == true && "$REMOTE_DEGRADED" == false ]]; then
    found=false
    for tag in "${REMOTE_PROTECTED_TAGS[@]}"; do
      if [[ "$tag" == "$current" ]]; then
        found=true
      fi
    done
    [[ "$found" == true ]] || return 1
  fi
}

apply_retention() {
  local repository="$1"
  shift
  local -a arguments=(--group-by paths "$@")
  local tag
  case "$repository" in
    local)
      for tag in "${LOCAL_PROTECTED_TAGS[@]}"; do
        arguments+=(--keep-tag "$tag")
      done
      local_restic forget "${arguments[@]}" --prune
      ;;
    remote)
      for tag in "${REMOTE_PROTECTED_TAGS[@]}"; do
        arguments+=(--keep-tag "$tag")
      done
      remote_restic forget "${arguments[@]}" --prune
      ;;
    *) return 1 ;;
  esac
}

complete_remote_degraded() {
  if [[ "$FAILURE_CATEGORY" == 'lease_lost' ]]; then
    return 1
  fi
  FAILURE_CATEGORY='remote_unavailable'
  backup_command fail --run-id "$RUN_ID" --category "$FAILURE_CATEGORY"
  TERMINAL_RECORDED=true
  release_operational_lease
}

record_failure() {
  [[ -n "$RUN_ID" && "$TERMINAL_RECORDED" == false ]] || return 0
  if run_guarded_external "$EXTERNAL_TIMEOUT_SECONDS" bounded_backup_compose \
    fail --run-id "$RUN_ID" --category "$FAILURE_CATEGORY"; then
    TERMINAL_RECORDED=true
    return 0
  fi
  return 1
}

stop_operational_lock_session() {
  [[ "$LEASE_FD_OPEN" == true ]] || return 0
  printf '%s\n' \
    "SELECT pg_advisory_unlock(${OPERATIONS_ADVISORY_KEY}); -- PHASE5_RELEASE_LOCK" \
    "\\q" >&9 || true
  exec 9>&-
  LEASE_FD_OPEN=false
  if [[ -n "$LEASE_PID" ]]; then
    wait "$LEASE_PID" 2>/dev/null || true
  fi
  LEASE_PID=''
}

release_operational_lease() {
  local released
  stop_lease_heartbeat
  if [[ "$LEASE_DURABLE" == true ]]; then
    if [[ "$RECOVERY_UNSAFE" == true ]]; then
      transition_operational_mode release || return 1
    else
      released="$(
        database_query <<SQL
-- PHASE5_QUERY_LEASE_RELEASE
UPDATE operational_modes
SET mode='normal',
    owner_id=NULL,
    lease_token_hash=NULL,
    lease_expires_at=NULL,
    entered_at=NULL,
    updated_at=clock_timestamp(),
    version=version+1
WHERE singleton_id=true
  AND owner_id='${LEASE_OWNER_ID}'::uuid
  AND lease_token_hash=digest(decode('${LEASE_TOKEN}','hex'),'sha256')
RETURNING 'released';
SQL
      )"
      [[ "$released" == 'released' ]] || return 1
    fi
    LEASE_DURABLE=false
  fi
  stop_operational_lock_session
}

remove_host_lock() {
  [[ "$LOCK_HELD" == true ]] || return 0
  [[ "$LOCK_DIRECTORY" == /* && -d "$LOCK_DIRECTORY" ]] || return 1
  [[ -z "$LEASE_FIFO" || "$LEASE_FIFO" == "$LOCK_DIRECTORY/operational.stdin" ]] ||
    return 1
  [[ -z "$LEASE_OUTPUT" || "$LEASE_OUTPUT" == "$LOCK_DIRECTORY/operational.stdout" ]] ||
    return 1
  [[ -z "$LEASE_FIFO" || ! -e "$LEASE_FIFO" || -p "$LEASE_FIFO" ]] || return 1
  if [[ -n "$LEASE_FIFO" && -p "$LEASE_FIFO" ]]; then
    rm -f "$LEASE_FIFO"
  fi
  if [[ -n "$LEASE_OUTPUT" && -f "$LEASE_OUTPUT" && ! -L "$LEASE_OUTPUT" ]]; then
    rm -f "$LEASE_OUTPUT"
  fi
  if [[ -n "$LEASE_HEARTBEAT_FAILED" &&
    "$LEASE_HEARTBEAT_FAILED" == "$LOCK_DIRECTORY/heartbeat.failed" &&
    -f "$LEASE_HEARTBEAT_FAILED" && ! -L "$LEASE_HEARTBEAT_FAILED" ]]; then
    rm -f "$LEASE_HEARTBEAT_FAILED"
  fi
  rmdir "$LOCK_DIRECTORY"
  LOCK_HELD=false
}

cleanup() {
  local status=$?
  local failed_stage="$FAILURE_CATEGORY"
  if [[ "$CLEANING_UP" == true ]]; then
    exit "$status"
  fi
  CLEANING_UP=true
  trap - EXIT HUP INT TERM
  ALLOW_LOST_LEASE_ACTIONS=true
  if restart_stopped_services; then
    FAILURE_CATEGORY="$failed_stage"
  else
    status=1
  fi
  if [[ "$status" -ne 0 ]] && ! record_failure; then
    status=1
  fi
  if ! release_operational_lease; then
    RECOVERY_UNSAFE=true
    status=1
    stop_operational_lock_session
  fi
  if ! remove_host_lock; then
    status=1
  fi
  if [[ "$status" -ne 0 ]]; then
    safe_log 'failed'
  fi
  exit "$status"
}

main() {
  validate_arguments "$@"
  resolve_root
  validate_paths_and_secrets
  trap cleanup EXIT HUP INT TERM
  acquire_host_lock
  initialize_backup_mounts
  verify_backup_mount_ownership

  local result
  if queue_or_select_run; then
    :
  else
    result=$?
    if [[ "$result" -eq 2 ]]; then
      return 0
    fi
    FAILURE_CATEGORY='internal'
    return 1
  fi
  prepare_lease_values
  start_operational_lock_session
  acquire_durable_lease
  start_lease_heartbeat

  FAILURE_CATEGORY='internal'
  backup_command prepare --run-id "$RUN_ID"
  assert_lease_heartbeat
  FAILURE_CATEGORY='integrity'
  ensure_repository local
  assert_lease_heartbeat
  wait_for_durable_drain
  transition_operational_mode backup
  assert_lease_heartbeat
  stop_snapshot_services
  assert_lease_heartbeat

  FAILURE_CATEGORY='snapshot'
  backup_command snapshot --run-id "$RUN_ID"
  assert_lease_heartbeat
  restart_stopped_services
  assert_lease_heartbeat

  FAILURE_CATEGORY='integrity'
  backup_command verify --run-id "$RUN_ID"
  assert_lease_heartbeat
  LOCAL_SNAPSHOT_ID="$(local_snapshot_id)"
  [[ "$LOCAL_SNAPSHOT_ID" =~ ^[0-9a-f]{64}$ ]] || return 1
  restic_check local
  assert_lease_heartbeat

  if [[ "$REMOTE_ENABLED" == true ]]; then
    FAILURE_CATEGORY='remote_sync'
    if ! ensure_repository remote; then
      REMOTE_DEGRADED=true
    fi
    assert_lease_heartbeat
    if ! backup_command sync --run-id "$RUN_ID"; then
      REMOTE_DEGRADED=true
    fi
    assert_lease_heartbeat
    REMOTE_SNAPSHOT_ID="$(remote_snapshot_id)"
    if [[ ! "$REMOTE_SNAPSHOT_ID" =~ ^[0-9a-f]{64}$ ]]; then
      REMOTE_DEGRADED=true
      REMOTE_SNAPSHOT_ID=''
    fi
  fi

  FAILURE_CATEGORY='retention'
  protect_pre_release 30
  protect_last_good
  apply_retention local --keep-daily 7
  assert_lease_heartbeat

  if [[ "$REMOTE_ENABLED" == true && "$REMOTE_DEGRADED" == false ]]; then
    if ! restic_check remote; then
      complete_remote_degraded
      return
    fi
    assert_lease_heartbeat
    if ! apply_retention remote --keep-daily 30 --keep-monthly 12; then
      complete_remote_degraded
      return
    fi
    assert_lease_heartbeat
  fi

  FAILURE_CATEGORY='internal'
  backup_command finish --run-id "$RUN_ID"
  TERMINAL_RECORDED=true
  release_operational_lease
}

main "$@"
