#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

readonly USAGE='Usage: scripts/phase5-backup.sh --project happylearn-dev --trigger scheduled|manual|pre_release'
readonly PRODUCTION_USAGE='Production additionally accepts --project happylearn-prod when HAPPYLEARN_PRODUCTION_ENV_FILE is set.'
readonly OPERATIONS_ADVISORY_KEY='845103120'
readonly BACKUP_ADVISORY_KEY='845103121'
readonly SYNC_RUN_LABEL='io.happylearn.phase5.sync-run'
readonly SYNC_OWNER_LABEL='io.happylearn.phase5.sync-owner'
readonly HEARTBEAT_HANDSHAKE_ATTEMPTS=500
readonly HEARTBEAT_HANDSHAKE_POLL_SECONDS='0.01'
readonly EXTERNAL_MONITOR_POLL_SECONDS='0.01'
readonly EXTERNAL_TERMINATION_GRACE_SECONDS='0.1'

PROJECT=''
EFFECTIVE_PROJECT=''
TRIGGER=''
ROOT=''
COMPOSE_FILE=''
LIVE_COMPOSE_FILE=''
E2E_LIVE_COMPOSE_FILE=''
PRODUCTION_ENV_FILE=''
LIVE_ROOT=''
LIVE_ONE_SHOT_RECORD_FILE=''
LIVE_RUN_ID_FILE=''
ACTIVE_EXTERNAL_GROUP_PID=''
ACTIVE_EXTERNAL_GROUP_IDENTITY=''
ACTIVE_EXTERNAL_GROUP_HANDSHAKE=''
UNCERTAIN_EXTERNAL_GROUP_PID=''
UNCERTAIN_EXTERNAL_GROUP_IDENTITY=''
EXTERNAL_CLEANUP_UNSAFE=false
LOCK_DIRECTORY=''
LOCK_HELD=false
LOCK_OWNER_DIRECTORY=''
LOCK_OWNER_TOKEN=''
LOCK_OWNER_PID=''
LOCK_OWNER_IDENTITY=''
LOCK_OWNER_FD_OPEN=false
HOST_LOCK_PLATFORM=''
OBSERVED_LOCK_OWNER_PID=''
OBSERVED_LOCK_OWNER_IDENTITY=''
OBSERVED_LOCK_OWNER_TOKEN=''
LEASE_FIFO=''
LEASE_OUTPUT=''
LEASE_PID=''
LEASE_PROCESS_IDENTITY=''
LEASE_GROUP_HANDSHAKE=''
LEASE_FD_OPEN=false
LEASE_OWNER_ID=''
LEASE_TOKEN=''
LEASE_DURABLE=false
LEASE_HEARTBEAT_PID=''
LEASE_HEARTBEAT_IDENTITY=''
LEASE_HEARTBEAT_HANDSHAKE=''
LEASE_HEARTBEAT_FAILED=''
RUN_ID=''
WORKER_STOP_REQUESTED=false
WORKER_STOPPED=false
AISTOR_STOPPED=false
BACKUP_ACTIVITY_AUDITED=false
TERMINAL_RECORDED=false
REMOTE_ENABLED=false
REMOTE_DEGRADED=false
RECOVERY_UNSAFE=false
ALLOW_LOST_LEASE_ACTIONS=false
SEPARATE_EXTERNAL_GROUP=true
SHARED_EXTERNAL_GROUP_PID=''
CAPTURED_CHILD_IDENTITY=''
CLEANING_UP=false
FAILURE_CATEGORY='internal'
CURRENT_STAGE='startup'
LOCAL_SNAPSHOT_ID=''
REMOTE_SNAPSHOT_ID=''
SYNC_CONTAINER_NAME=''
SYNC_CONTAINER_ID=''
SYNC_CONTAINER_OWNER=''
SYNC_CONTAINER_HOSTNAME=''

SERVICE_STOP_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_SERVICE_STOP_TIMEOUT_SECONDS:-60}"
DRAIN_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_DRAIN_TIMEOUT_SECONDS:-600}"
READY_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_READY_TIMEOUT_SECONDS:-180}"
POLL_INTERVAL_SECONDS="${HAPPYLEARN_BACKUP_POLL_INTERVAL_SECONDS:-2}"
HEARTBEAT_INTERVAL_SECONDS="${HAPPYLEARN_BACKUP_HEARTBEAT_INTERVAL_SECONDS:-60}"
EXTERNAL_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS:-2700}"
DATABASE_QUERY_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_DATABASE_QUERY_TIMEOUT_SECONDS:-30}"
DATABASE_CONNECT_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_DATABASE_CONNECT_TIMEOUT_SECONDS:-5}"
DATABASE_STATEMENT_TIMEOUT_MILLISECONDS="$((DATABASE_QUERY_TIMEOUT_SECONDS * 1000))"
SYNC_CONTAINER_STOP_TIMEOUT_SECONDS="${HAPPYLEARN_BACKUP_SYNC_CONTAINER_STOP_TIMEOUT_SECONDS:-30}"

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
  EFFECTIVE_PROJECT="$PROJECT"
  TRIGGER="$4"
  if [[ "$PROJECT" == "happylearn-dev" ]]; then
    :
  elif [[ "$PROJECT" != "happylearn-prod" ]]; then
    usage_error
  fi
  if [[ "$PROJECT" == happylearn-prod && -n ${HAPPYLEARN_LOCAL_COMPOSE_PROJECT:-} ]]; then
    [[ $HAPPYLEARN_LOCAL_COMPOSE_PROJECT =~ ^happylearn_phase6_[a-z0-9]+_[a-z0-9]+$ ]] || usage_error
    EFFECTIVE_PROJECT=$HAPPYLEARN_LOCAL_COMPOSE_PROJECT
  fi
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
  valid_uint "$DATABASE_QUERY_TIMEOUT_SECONDS" || usage_error
  [[ "$DATABASE_QUERY_TIMEOUT_SECONDS" -le 300 ]] || usage_error
  valid_uint "$DATABASE_CONNECT_TIMEOUT_SECONDS" || usage_error
  [[ "$DATABASE_CONNECT_TIMEOUT_SECONDS" -le "$DATABASE_QUERY_TIMEOUT_SECONDS" ]] ||
    usage_error
  valid_uint "$SYNC_CONTAINER_STOP_TIMEOUT_SECONDS" || usage_error
  [[ "$SYNC_CONTAINER_STOP_TIMEOUT_SECONDS" -le 120 ]] || usage_error
}

resolve_root() {
  local script_directory
  script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
  ROOT="$(cd "$script_directory/.." && pwd -P)"
  if [[ "$PROJECT" == 'happylearn-prod' ]]; then
    COMPOSE_FILE="$ROOT/deploy/compose.prod.yml"
    PRODUCTION_ENV_FILE="${HAPPYLEARN_PRODUCTION_ENV_FILE:-}"
    [[ "$PRODUCTION_ENV_FILE" == /* && -f "$PRODUCTION_ENV_FILE" && ! -L "$PRODUCTION_ENV_FILE" && "$(portable_mode "$PRODUCTION_ENV_FILE")" == 600 ]] || usage_error
  else
    COMPOSE_FILE="$ROOT/deploy/compose.dev.yml"
  fi
  LIVE_COMPOSE_FILE="$ROOT/deploy/compose.backup-live.yml"
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

portable_size() {
  local path="$1"
  if stat -f '%z' "$path" >/dev/null 2>&1; then
    stat -f '%z' "$path"
  else
    stat -c '%s' "$path"
  fi
}

owner_only_directory() {
  local path="$1"
  [[ -d "$path" && ! -L "$path" && "$(portable_mode "$path")" == '700' ]]
}

portable_owner() {
  local path="$1"
  if stat -f '%u' "$path" >/dev/null 2>&1; then
    stat -f '%u' "$path"
  else
    stat -c '%u' "$path"
  fi
}

owner_only_secret() {
  local path="$1"
  local size
  local expected_mode='400'
  [[ "$PROJECT" != 'happylearn-prod' ]] || expected_mode='600'
  [[ -f "$path" && ! -L "$path" && "$(portable_mode "$path")" == "$expected_mode" ]] ||
    return 1
  if stat -f '%z' "$path" >/dev/null 2>&1; then
    size="$(stat -f '%z' "$path")"
  else
    size="$(stat -c '%s' "$path")"
  fi
  [[ "$size" -ge 1 && "$size" -le 4096 ]]
}

backup_secret_path() {
  local logical=$1 filename=$1
  if [[ "$PROJECT" == 'happylearn-prod' ]]; then
    case "$logical" in
      database_password) filename='backup-database-password' ;;
      local_repository) filename='backup-local-repository' ;;
      local_password) filename='backup-password' ;;
    esac
  fi
  printf '%s/%s' "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" "$filename"
}

single_line_secret_value() {
  local path="$1"
  local value
  local size
  owner_only_secret "$path" || return 1
  if od -An -tx1 "$path" |
    grep -Eq '(^|[[:space:]])00([[:space:]]|$)'; then
    return 1
  fi
  value="$(<"$path")"
  if stat -f '%z' "$path" >/dev/null 2>&1; then
    size="$(stat -f '%z' "$path")"
  else
    size="$(stat -c '%s' "$path")"
  fi
  [[ -n "$value" &&
    "$value" != *$'\n'* &&
    "$value" != *$'\r'* &&
    "$value" == "${value#"${value%%[![:space:]]*}"}" &&
    "$value" == "${value%"${value##*[![:space:]]}"}" ]] ||
    return 1
  [[ "$size" -eq "${#value}" || "$size" -eq $((${#value} + 1)) ]] ||
    return 1
  printf '%s' "$value"
}

valid_remote_repository() {
  local repository="$1"
  local location authority bucket lower_bucket percent_suffix port
  [[ "$repository" == s3:https://* &&
    ! "$repository" =~ [[:space:][:cntrl:]] &&
    "$repository" != *'?'* &&
    "$repository" != *'#'* &&
    "$repository" != *'\'* ]] ||
    return 1
  location="${repository#s3:https://}"
  [[ "$location" == */* ]] || return 1
  authority="${location%%/*}"
  bucket="${location#*/}"
  [[ -n "$authority" && "$authority" != *'@'* ]] || return 1
  if [[ "$authority" =~ ^\[[0-9A-Fa-f:.]+\](:([0-9]{1,5}))?$ ]]; then
    port="${BASH_REMATCH[2]:-}"
  elif [[ "$authority" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:([0-9]{1,5}))?$ ]]; then
    port="${BASH_REMATCH[3]:-}"
  else
    return 1
  fi
  if [[ -n "$port" ]] && ! ((10#$port <= 65535)); then
    return 1
  fi
  while [[ "$bucket" == /* ]]; do
    bucket="${bucket#/}"
  done
  while [[ "$bucket" == */ ]]; do
    bucket="${bucket%/}"
  done
  lower_bucket="$(printf '%s' "$bucket" | tr '[:upper:]' '[:lower:]')"
  [[ -n "$bucket" &&
    "$lower_bucket" != *insecure-tls* ]] ||
    return 1
  percent_suffix="$repository"
  while [[ "$percent_suffix" == *'%'* ]]; do
    percent_suffix="${percent_suffix#*%}"
    [[ "$percent_suffix" =~ ^[0-9A-Fa-f]{2} ]] || return 1
    percent_suffix="${percent_suffix:2}"
  done
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
  if [[ "$PROJECT" == 'happylearn-prod' ]]; then
    [[ "$(id -u)" == 0 && -d "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" && ! -L "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" && "$(portable_mode "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY")" == 711 ]] || return 1
  else
    owner_only_directory "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" || return 1
  fi
  owner_only_directory "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" || return 1
  owner_only_directory "$HAPPYLEARN_BACKUP_STATE_DIRECTORY" || return 1
  single_line_secret_value \
    "$(backup_secret_path database_password)" >/dev/null ||
    return 1
  [[ "$(single_line_secret_value \
    "$(backup_secret_path local_repository)")" == '/repository' ]] ||
    return 1
  single_line_secret_value \
    "$(backup_secret_path local_password)" >/dev/null ||
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
      single_line_secret_value \
        "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/$name" >/dev/null ||
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
  repository="$(single_line_secret_value \
    "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/remote_repository")"
  valid_remote_repository "$repository" || return 1
  REMOTE_ENABLED=true
}

configure_live_context() {
  local enabled="${HAPPYLEARN_BACKUP_LIVE_TEST:-}"
  local live_project="${HAPPYLEARN_BACKUP_LIVE_PROJECT:-}"
  local live_root="${HAPPYLEARN_BACKUP_LIVE_ROOT:-}"
  local secret_root
  local repository_root
  local state_root
  local lock_path
  if [[ -z "$enabled" ]]; then
    [[ -z "$live_project" && -z "$live_root" ]] || return 1
    return 0
  fi
  [[ "$enabled" == '1' &&
    "$live_project" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ &&
    "$live_root" == /* &&
    -f "$LIVE_COMPOSE_FILE" ]] ||
    return 1
  owner_only_directory "$live_root" || return 1
  LIVE_ROOT="$(cd "$live_root" && pwd -P)"
  [[ "$LIVE_ROOT" == "$live_root" ]] || return 1
  E2E_LIVE_COMPOSE_FILE="$ROOT/deploy/compose.phase5-e2e-live.yml"
  [[ -f "$E2E_LIVE_COMPOSE_FILE" &&
    ! -L "$E2E_LIVE_COMPOSE_FILE" ]] ||
    return 1
  secret_root="$(cd "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" && pwd -P)"
  repository_root="$(cd "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" && pwd -P)"
  state_root="$(cd "$HAPPYLEARN_BACKUP_STATE_DIRECTORY" && pwd -P)"
  lock_path="$(
    cd "$(dirname "${HAPPYLEARN_BACKUP_LOCK_DIRECTORY:-/}")" &&
      printf '%s/%s' "$(pwd -P)" \
        "$(basename "${HAPPYLEARN_BACKUP_LOCK_DIRECTORY:-/}")"
  )"
  [[ "$secret_root" == "$LIVE_ROOT/secrets" &&
    "$repository_root" == "$LIVE_ROOT/repository" &&
    "$state_root" == "$LIVE_ROOT/state" &&
    "$lock_path" == "$LIVE_ROOT/host.lock" ]] ||
    return 1
  owner_only_directory "$LIVE_ROOT/runtime-secrets" || return 1
  LIVE_ONE_SHOT_RECORD_FILE="$LIVE_ROOT/coordinator-one-shots"
  LIVE_RUN_ID_FILE="$LIVE_ROOT/coordinator-run-id"
  [[ -f "$LIVE_ONE_SHOT_RECORD_FILE" &&
    ! -L "$LIVE_ONE_SHOT_RECORD_FILE" &&
    ! -s "$LIVE_ONE_SHOT_RECORD_FILE" &&
    "$(portable_mode "$LIVE_ONE_SHOT_RECORD_FILE")" == '600' &&
    "$(portable_owner "$LIVE_ONE_SHOT_RECORD_FILE")" == "$(id -u)" ]] ||
    return 1
  [[ -f "$LIVE_RUN_ID_FILE" &&
    ! -L "$LIVE_RUN_ID_FILE" &&
    ! -s "$LIVE_RUN_ID_FILE" &&
    "$(portable_mode "$LIVE_RUN_ID_FILE" 2>/dev/null)" == '600' &&
    "$(portable_owner "$LIVE_RUN_ID_FILE" 2>/dev/null)" == "$(id -u)" ]] ||
    return 1
  HAPPYLEARN_BACKUP_LIVE_ROOT="$LIVE_ROOT"
  export HAPPYLEARN_BACKUP_LIVE_ROOT
  EFFECTIVE_PROJECT="$live_project"
}

publish_live_run_id() {
  [[ -n "$LIVE_ROOT" && -n "$LIVE_RUN_ID_FILE" ]] || return 0
  local temporary=''
  local published=''
  [[ "$RUN_ID" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  [[ -f "$LIVE_RUN_ID_FILE" &&
    ! -L "$LIVE_RUN_ID_FILE" &&
    ! -s "$LIVE_RUN_ID_FILE" &&
    "$(portable_mode "$LIVE_RUN_ID_FILE" 2>/dev/null)" == '600' &&
    "$(portable_owner "$LIVE_RUN_ID_FILE" 2>/dev/null)" == "$(id -u)" ]] ||
    return 1
  temporary="$(
    mktemp "$LIVE_ROOT/.coordinator-run-id.XXXXXX" 2>/dev/null
  )" || return 1
  if ! chmod 0600 "$temporary" 2>/dev/null ||
    ! printf '%s\n' "$RUN_ID" >"$temporary" 2>/dev/null ||
    [[ ! -f "$temporary" || -L "$temporary" ||
      "$(portable_mode "$temporary" 2>/dev/null)" != '600' ||
      "$(portable_owner "$temporary" 2>/dev/null)" != "$(id -u)" ||
      "$(portable_size "$temporary" 2>/dev/null)" != '37' ]]; then
    rm -f "$temporary" 2>/dev/null || true
    return 1
  fi
  if ! mv -f "$temporary" "$LIVE_RUN_ID_FILE" 2>/dev/null; then
    rm -f "$temporary" 2>/dev/null || true
    return 1
  fi
  [[ -f "$LIVE_RUN_ID_FILE" &&
    ! -L "$LIVE_RUN_ID_FILE" &&
    "$(portable_mode "$LIVE_RUN_ID_FILE" 2>/dev/null)" == '600' &&
    "$(portable_owner "$LIVE_RUN_ID_FILE" 2>/dev/null)" == "$(id -u)" &&
    "$(portable_size "$LIVE_RUN_ID_FILE" 2>/dev/null)" == '37' ]] ||
    return 1
  IFS= read -r published <"$LIVE_RUN_ID_FILE" 2>/dev/null || return 1
  [[ "$published" == "$RUN_ID" ]]
}

portable_file_identity() {
  local path="$1"
  if stat -c '%d:%i' "$path" >/dev/null 2>&1; then
    stat -c '%d:%i' "$path"
  else
    stat -f '%d:%i' "$path"
  fi
}

portable_process_identity() {
  local pid="$1"
  local identity
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  command -v ps >/dev/null 2>&1 || return 1
  identity="$(ps -o lstart= -p "$pid" 2>/dev/null)" || return 1
  identity="${identity#"${identity%%[![:space:]]*}"}"
  identity="${identity%"${identity##*[![:space:]]}"}"
  [[ -n "$identity" &&
    "$identity" != *$'\n'* &&
    "${#identity}" -le 128 &&
    "$identity" =~ ^[[:print:]]+$ ]] ||
    return 1
  printf '%s' "$identity"
}

process_identity_matches() {
  local pid="$1"
  local expected="$2"
  local actual
  [[ -n "$expected" ]] || return 1
  actual="$(portable_process_identity "$pid")" || return 1
  [[ "$actual" == "$expected" ]]
}

portable_process_group() {
  local pid="$1"
  local process_group
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  process_group="$(ps -o pgid= -p "$pid" 2>/dev/null)" || return 1
  process_group="${process_group#"${process_group%%[![:space:]]*}"}"
  process_group="${process_group%"${process_group##*[![:space:]]}"}"
  [[ "$process_group" =~ ^[1-9][0-9]*$ ]] || return 1
  printf '%s' "$process_group"
}

owned_group_witness_matches() {
  local pid="$1"
  local expected_identity="$2"
  local process_group
  process_identity_matches "$pid" "$expected_identity" || return 1
  process_group="$(portable_process_group "$pid")" || return 1
  [[ "$process_group" == "$pid" ]]
}

direct_child_job_running() {
  local pid="$1"
  local candidate
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  for candidate in $(jobs -pr); do
    [[ "$candidate" == "$pid" ]] && return 0
  done
  return 1
}

capture_direct_child_identity() {
  local pid="$1"
  local identity
  CAPTURED_CHILD_IDENTITY=''
  direct_child_job_running "$pid" || return 1
  identity="$(portable_process_identity "$pid")" || return 1
  direct_child_job_running "$pid" || return 1
  owned_group_witness_matches "$pid" "$identity" || return 1
  CAPTURED_CHILD_IDENTITY="$identity"
}

valid_group_owner_handshake_directory() {
  local directory="$1"
  [[ -n "$LOCK_OWNER_DIRECTORY" &&
    "$directory" == "$LOCK_OWNER_DIRECTORY/.group-owner."?????? &&
    -d "$directory" && ! -L "$directory" &&
    "$(portable_mode "$directory" 2>/dev/null)" == '700' &&
    "$(portable_owner "$directory" 2>/dev/null)" == "$(id -u)" ]]
}

create_group_owner_handshake() {
  local directory
  directory="$(
    mktemp -d "$LOCK_OWNER_DIRECTORY/.group-owner.XXXXXX" 2>/dev/null
  )" || return 1
  if ! chmod 0700 "$directory" ||
    ! valid_group_owner_handshake_directory "$directory"; then
    rmdir "$directory" 2>/dev/null || true
    return 1
  fi
  printf '%s' "$directory"
}

cleanup_group_owner_handshake() {
  local directory="$1"
  [[ "$directory" == "$LOCK_OWNER_DIRECTORY/.group-owner."?????? ]] ||
    return 1
  rm -f \
    "$directory/ready" "$directory/ready.pending" \
    "$directory/decision" \
    "$directory/decision.start.pending" \
    "$directory/decision.cancel.pending" 2>/dev/null ||
    return 1
  if [[ -d "$directory" && ! -L "$directory" ]]; then
    rmdir "$directory" 2>/dev/null || return 1
  else
    [[ ! -e "$directory" && ! -L "$directory" ]] || return 1
  fi
}

publish_group_owner_control() {
  local directory="$1"
  local name="$2"
  local pending="$directory/$name.pending"
  local target="$directory/$name"
  valid_group_owner_handshake_directory "$directory" || return 1
  [[ "$name" == ready ]] || return 1
  [[ ! -e "$pending" && ! -L "$pending" &&
    ! -e "$target" && ! -L "$target" ]] ||
    return 1
  if ! (umask 077 && printf '%s\n' "$name" >"$pending") ||
    ! chmod 0600 "$pending" ||
    ! mv "$pending" "$target"; then
    rm -f "$pending" 2>/dev/null || true
    return 1
  fi
  [[ -f "$target" && ! -L "$target" &&
    "$(portable_mode "$target" 2>/dev/null)" == '600' &&
    "$(portable_owner "$target" 2>/dev/null)" == "$(id -u)" ]]
}

group_owner_control_matches() {
  local directory="$1"
  local name="$2"
  local path="$directory/$name"
  local value
  valid_group_owner_handshake_directory "$directory" || return 1
  [[ -f "$path" && ! -L "$path" &&
    "$(portable_mode "$path" 2>/dev/null)" == '600' &&
    "$(portable_owner "$path" 2>/dev/null)" == "$(id -u)" ]] ||
    return 1
  value="$(sed -n '1p' "$path" 2>/dev/null)" || return 1
  [[ "$value" == "$name" ]]
}

publish_group_owner_decision() {
  local directory="$1"
  local decision="$2"
  local pending="$directory/decision.${decision}.pending"
  local target="$directory/decision"
  local published=false
  local status=0
  valid_group_owner_handshake_directory "$directory" || {
    safe_log 'group_owner_decision_directory_invalid'
    return 1
  }
  case "$decision" in
    start|cancel) ;;
    *) return 1 ;;
  esac
  if group_owner_decision_matches "$directory" "$decision"; then
    return 0
  fi
  [[ ! -e "$pending" && ! -L "$pending" ]] || {
    safe_log 'group_owner_decision_pending_exists'
    return 1
  }
  if ! (umask 077 && printf '%s\n' "$decision" >"$pending") ||
    ! chmod 0600 "$pending"; then
    safe_log 'group_owner_decision_pending_failed'
    rm -f "$pending" 2>/dev/null || true
    return 1
  fi
  if ! ln "$pending" "$target" 2>/dev/null; then
    safe_log 'group_owner_decision_conflict'
    status=1
  else
    published=true
  fi
  if ! rm -f "$pending" 2>/dev/null; then
    if [[ -e "$directory" || -L "$directory" ]]; then
      safe_log 'group_owner_decision_pending_cleanup_failed'
      status=1
    fi
  fi
  if [[ "$published" == true &&
    ! -e "$directory" && ! -L "$directory" ]]; then
    return 0
  fi
  if [[ "$published" == true && "$status" -eq 0 ]]; then
    return 0
  fi
  if [[ "$status" -ne 0 ]]; then
    group_owner_decision_matches "$directory" "$decision"
    return
  fi
  group_owner_decision_matches "$directory" "$decision" || {
    safe_log 'group_owner_decision_validation_failed'
    return 1
  }
}

group_owner_decision_matches() {
  local directory="$1"
  local expected="$2"
  local path="$directory/decision"
  local value
  valid_group_owner_handshake_directory "$directory" || return 1
  case "$expected" in
    start|cancel) ;;
    *) return 1 ;;
  esac
  [[ -f "$path" && ! -L "$path" &&
    "$(portable_mode "$path" 2>/dev/null)" == '600' &&
    "$(portable_owner "$path" 2>/dev/null)" == "$(id -u)" ]] ||
    return 1
  value="$(sed -n '1p' "$path" 2>/dev/null)" || return 1
  [[ "$value" == "$expected" ]]
}

hold_owned_group() {
  trap '' HUP INT TERM
  while :; do
    sleep "$EXTERNAL_MONITOR_POLL_SECONDS" || true
  done
}

run_external_group_owner() {
  local handshake_directory="$1"
  shift
  local child_pid
  local child_status=0
  local handshake_attempts=0
  set +m
  trap 'cleanup_group_owner_handshake "$handshake_directory" 2>/dev/null || true; exit 1' \
    HUP INT TERM
  publish_group_owner_control "$handshake_directory" ready || return 1
  while ((handshake_attempts < HEARTBEAT_HANDSHAKE_ATTEMPTS)); do
    if group_owner_decision_matches "$handshake_directory" cancel; then
      trap - HUP INT TERM
      cleanup_group_owner_handshake "$handshake_directory" || true
      return 1
    fi
    if group_owner_decision_matches "$handshake_directory" start; then
      trap 'hold_owned_group' HUP INT TERM
      cleanup_group_owner_handshake "$handshake_directory" || return 1
      break
    fi
    sleep "$HEARTBEAT_HANDSHAKE_POLL_SECONDS"
    handshake_attempts=$((handshake_attempts + 1))
  done
  if ((handshake_attempts >= HEARTBEAT_HANDSHAKE_ATTEMPTS)); then
    trap - HUP INT TERM
    cleanup_group_owner_handshake "$handshake_directory" 2>/dev/null || true
    return 1
  fi
  "$@" &
  child_pid="$!"
  if wait "$child_pid"; then
    :
  else
    child_status=$?
  fi
  trap - HUP INT TERM
  return "$child_status"
}

prepare_group_owner() {
  local pid="$1"
  local handshake_directory="$2"
  local handshake_attempts=0
  CAPTURED_CHILD_IDENTITY=''
  while ((handshake_attempts < HEARTBEAT_HANDSHAKE_ATTEMPTS)); do
    if group_owner_control_matches "$handshake_directory" ready; then
      break
    fi
    direct_child_job_running "$pid" || return 1
    sleep "$HEARTBEAT_HANDSHAKE_POLL_SECONDS"
    handshake_attempts=$((handshake_attempts + 1))
  done
  ((handshake_attempts < HEARTBEAT_HANDSHAKE_ATTEMPTS)) || return 1
  capture_direct_child_identity "$pid" || return 1
}

commit_group_owner() {
  local pid="$1"
  local expected_identity="$2"
  local handshake_directory="$3"
  local registered_pid="$4"
  local registered_identity="$5"
  local registered_handshake="$6"
  local handshake_attempts=0
  [[ "$pid" =~ ^[1-9][0-9]*$ &&
    "$registered_pid" == "$pid" &&
    -n "$expected_identity" &&
    "$registered_identity" == "$expected_identity" &&
    "$registered_handshake" == "$handshake_directory" ]] || {
    safe_log 'group_owner_registration_invalid'
    return 1
  }
  owned_group_witness_matches "$pid" "$expected_identity" || {
    safe_log 'group_owner_identity_changed_before_commit'
    return 1
  }
  publish_group_owner_decision "$handshake_directory" start || {
    safe_log 'group_owner_start_decision_failed'
    return 1
  }
  handshake_attempts=0
  while valid_group_owner_handshake_directory "$handshake_directory"; do
    if ! direct_child_job_running "$pid"; then
      [[ ! -e "$handshake_directory" && ! -L "$handshake_directory" ]] &&
        return 0
      safe_log 'group_owner_exited_before_ack'
      return 1
    fi
    sleep "$HEARTBEAT_HANDSHAKE_POLL_SECONDS"
    handshake_attempts=$((handshake_attempts + 1))
    if ((handshake_attempts >= HEARTBEAT_HANDSHAKE_ATTEMPTS)); then
      safe_log 'group_owner_ack_timeout'
      return 1
    fi
  done
  if [[ ! -e "$handshake_directory" && ! -L "$handshake_directory" ]]; then
    return 0
  fi
  safe_log 'group_owner_ack_invalid'
  return 1
}

cancel_or_terminate_group_owner() {
  local pid="$1"
  local expected_identity="$2"
  local handshake_directory="$3"
  local grace_seconds="${4:-$EXTERNAL_TERMINATION_GRACE_SECONDS}"
  local handshake_attempts=0
  if valid_group_owner_handshake_directory "$handshake_directory"; then
    if publish_group_owner_decision "$handshake_directory" cancel; then
      while direct_child_job_running "$pid" &&
        valid_group_owner_handshake_directory "$handshake_directory" &&
        ((handshake_attempts < HEARTBEAT_HANDSHAKE_ATTEMPTS)); do
        sleep "$HEARTBEAT_HANDSHAKE_POLL_SECONDS"
        handshake_attempts=$((handshake_attempts + 1))
      done
      if ! direct_child_job_running "$pid"; then
        wait "$pid" 2>/dev/null || true
        if ! cleanup_group_owner_handshake \
          "$handshake_directory" 2>/dev/null &&
          [[ -e "$handshake_directory" || -L "$handshake_directory" ]]; then
          return 1
        fi
        return 0
      fi
    fi
  fi
  [[ -n "$expected_identity" ]] || return 1
  if terminate_external_group "$pid" "$expected_identity" "$grace_seconds"; then
    if ! cleanup_group_owner_handshake \
      "$handshake_directory" 2>/dev/null &&
      [[ -e "$handshake_directory" || -L "$handshake_directory" ]]; then
      return 1
    fi
    return 0
  fi
  return 1
}

heartbeat_owner_matches() {
  local heartbeat_pid="$1"
  local owner_pid="$2"
  local expected_identity="$3"
  local actual_parent
  [[ "$heartbeat_pid" =~ ^[1-9][0-9]*$ &&
    "$owner_pid" =~ ^[1-9][0-9]*$ ]] ||
    return 1
  process_identity_matches "$owner_pid" "$expected_identity" || return 1
  actual_parent="$(ps -o ppid= -p "$heartbeat_pid" 2>/dev/null)" || return 1
  actual_parent="${actual_parent#"${actual_parent%%[![:space:]]*}"}"
  actual_parent="${actual_parent%"${actual_parent##*[![:space:]]}"}"
  [[ "$actual_parent" == "$owner_pid" ]]
}

write_heartbeat_handshake_file() {
  local target="$1"
  local pending="$2"
  local value="$3"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || return 1
  case "$target:$pending" in
    "$LOCK_OWNER_DIRECTORY/heartbeat.pid:$LOCK_OWNER_DIRECTORY/heartbeat.pid.pending" | \
      "$LOCK_OWNER_DIRECTORY/heartbeat.ready:$LOCK_OWNER_DIRECTORY/heartbeat.ready.pending") ;;
    *) return 1 ;;
  esac
  [[ ! -e "$target" && ! -L "$target" &&
    ! -e "$pending" && ! -L "$pending" ]] ||
    return 1
  if ! (umask 077 && printf '%s\n' "$value" >"$pending") ||
    ! chmod 0600 "$pending" ||
    ! mv "$pending" "$target"; then
    rm -f "$pending"
    return 1
  fi
  [[ -f "$target" && ! -L "$target" &&
    "$(portable_mode "$target")" == '600' ]]
}

read_heartbeat_handshake_file() {
  local path="$1"
  local value
  [[ -f "$path" && ! -L "$path" &&
    "$(portable_mode "$path")" == '600' &&
    "$(portable_owner "$path")" == "$(id -u)" ]] ||
    return 1
  value="$(<"$path")"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || return 1
  printf '%s' "$value"
}

wait_for_heartbeat_handshake() {
  local path="$1"
  local expected_pid="$2"
  local attempts=0
  local observed
  while [[ "$attempts" -lt "$HEARTBEAT_HANDSHAKE_ATTEMPTS" ]]; do
    kill -0 "$expected_pid" 2>/dev/null || return 1
    observed="$(read_heartbeat_handshake_file "$path" 2>/dev/null || true)"
    if [[ "$observed" == "$expected_pid" ]] &&
      kill -0 "$expected_pid" 2>/dev/null; then
      return 0
    fi
    sleep "$HEARTBEAT_HANDSHAKE_POLL_SECONDS"
    attempts=$((attempts + 1))
  done
  return 1
}

system_holds_liveness_file() {
  local path="$1"
  local identity="$2"
  local descriptor_path flock_status output
  local current_pid="$$"
  [[ -f "$path" && ! -L "$path" &&
    "$(portable_file_identity "$path")" == "$identity" ]] ||
    return 2
  case "$HOST_LOCK_PLATFORM" in
    Linux)
      command -v flock >/dev/null 2>&1 || return 2
      if ! exec 7<"$path"; then
        return 2
      fi
      if [[ -e /proc/self/fd/7 ]]; then
        descriptor_path=/proc/self/fd/7
      elif [[ -e /dev/fd/7 ]]; then
        descriptor_path=/dev/fd/7
      else
        exec 7<&-
        return 2
      fi
      if flock --exclusive --nonblock --conflict-exit-code 75 \
        "$descriptor_path" true; then
        flock_status=1
      else
        case "$?" in
          75) flock_status=0 ;;
          *) flock_status=2 ;;
        esac
      fi
      if [[ ! -f "$path" || -L "$path" ||
        "$(portable_file_identity "$path")" != "$identity" ]]; then
        flock_status=2
      fi
      exec 7<&-
      return "$flock_status"
      ;;
    Darwin)
      command -v lsof >/dev/null 2>&1 || return 2
      if output="$(lsof -Fn -- "$path" 2>/dev/null)"; then
        grep -Fxq "n$path" <<<"$output" && return 0
        return 2
      fi
      if lsof -a -p "$current_pid" -Fp -d cwd 2>/dev/null |
        grep -Eq "^p${current_pid}$"; then
        return 1
      fi
      return 2
      ;;
    *) return 2 ;;
  esac
}

load_host_lock_owner() {
  local directory="$1"
  local owner_file="$directory/owner"
  local liveness_file="$directory/liveness"
  local version_line pid_line identity_line token_line extra_line
  owner_only_directory "$directory" || return 1
  [[ "$(portable_owner "$directory")" == "$(id -u)" ]] || return 1
  [[ -f "$owner_file" && ! -L "$owner_file" &&
    "$(portable_mode "$owner_file")" == '400' &&
    "$(portable_owner "$owner_file")" == "$(id -u)" ]] ||
    return 1
  {
    IFS= read -r version_line &&
      IFS= read -r pid_line &&
      IFS= read -r identity_line &&
      IFS= read -r token_line &&
      ! IFS= read -r extra_line
  } <"$owner_file" || return 1
  [[ "$version_line" == 'version=1' &&
    "$pid_line" =~ ^pid=([1-9][0-9]*)$ &&
    "$identity_line" =~ ^identity=([0-9]+:[0-9]+)$ &&
    "$token_line" =~ ^token=([0-9a-f]{64})$ ]] ||
    return 1
  [[ -f "$liveness_file" && ! -L "$liveness_file" &&
    "$(portable_mode "$liveness_file")" == '400' &&
    "$(portable_owner "$liveness_file")" == "$(id -u)" &&
    "$(portable_file_identity "$liveness_file")" == "${identity_line#identity=}" ]] ||
    return 1
  OBSERVED_LOCK_OWNER_PID="${pid_line#pid=}"
  OBSERVED_LOCK_OWNER_IDENTITY="${identity_line#identity=}"
  OBSERVED_LOCK_OWNER_TOKEN="${token_line#token=}"
}

host_lock_owner_matches() {
  local directory="$1"
  local token="$2"
  local pid="$3"
  local identity="$4"
  load_host_lock_owner "$directory" &&
    [[ "$OBSERVED_LOCK_OWNER_TOKEN" == "$token" &&
      "$OBSERVED_LOCK_OWNER_PID" == "$pid" &&
      "$OBSERVED_LOCK_OWNER_IDENTITY" == "$identity" ]]
}

host_lock_is_stale() {
  local directory="$1"
  local liveness_status
  load_host_lock_owner "$directory" || return 1
  if system_holds_liveness_file \
    "$directory/liveness" "$OBSERVED_LOCK_OWNER_IDENTITY"; then
    return 1
  else
    liveness_status=$?
  fi
  case "$liveness_status" in
    1) return 0 ;;
    *) return 1 ;;
  esac
}

remove_host_lock_owner_directory() {
  local directory="$1"
  local token="$2"
  load_host_lock_owner "$directory" || return 1
  [[ "$OBSERVED_LOCK_OWNER_TOKEN" == "$token" ]] || return 1
  local path
  for path in \
    "$directory/operational.stdin" \
    "$directory/operational.stdout" \
    "$directory/heartbeat.pid" \
    "$directory/heartbeat.pid.pending" \
    "$directory/heartbeat.ready" \
    "$directory/heartbeat.ready.pending" \
    "$directory/heartbeat.failed" \
    "$directory/liveness"
  do
    if [[ -p "$path" || (-f "$path" && ! -L "$path") ]]; then
      rm -f "$path"
    elif [[ -e "$path" || -L "$path" ]]; then
      return 1
    fi
  done
  rm -f "$directory/owner"
  rmdir "$directory"
}

discard_unpublished_host_lock_owner() {
  local directory="$1"
  local token="$2"
  [[ -n "$directory" && -n "$token" ]] || return 0
  remove_host_lock_owner_directory "$directory" "$token"
}

existing_lock_owner_directory() {
  local target
  [[ -L "$LOCK_DIRECTORY" ]] || return 1
  target="$(readlink "$LOCK_DIRECTORY")" || return 1
  [[ "$target" == "${LOCK_DIRECTORY}.owner."?????? ]] || return 1
  printf '%s' "$target"
}

publish_host_lock() {
  local accidental_link
  if ln -sh "$LOCK_OWNER_DIRECTORY" "$LOCK_DIRECTORY" 2>/dev/null; then
    :
  elif ln -sT "$LOCK_OWNER_DIRECTORY" "$LOCK_DIRECTORY" 2>/dev/null; then
    :
  else
    return 1
  fi
  if [[ -L "$LOCK_DIRECTORY" &&
    "$(readlink "$LOCK_DIRECTORY")" == "$LOCK_OWNER_DIRECTORY" ]]; then
    return 0
  fi
  accidental_link="$LOCK_DIRECTORY/$(basename "$LOCK_OWNER_DIRECTORY")"
  if [[ -L "$accidental_link" &&
    "$(readlink "$accidental_link")" == "$LOCK_OWNER_DIRECTORY" ]]; then
    rm -f "$accidental_link"
  fi
  return 1
}

reclaim_stale_host_lock() {
  local owner_directory="$1"
  local reclaim_directory="${LOCK_DIRECTORY}.reclaim"
  local current_owner stale_token
  host_lock_is_stale "$owner_directory" || return 1
  stale_token="$OBSERVED_LOCK_OWNER_TOKEN"
  mkdir "$reclaim_directory" 2>/dev/null || return 1
  if ! chmod 0700 "$reclaim_directory"; then
    rmdir "$reclaim_directory" 2>/dev/null || true
    return 1
  fi
  current_owner="$(existing_lock_owner_directory 2>/dev/null || true)"
  if [[ "$current_owner" != "$owner_directory" ]] ||
    ! host_lock_is_stale "$owner_directory"; then
    rmdir "$reclaim_directory" || true
    return 1
  fi
  stale_token="$OBSERVED_LOCK_OWNER_TOKEN"
  if ! rm -f "$LOCK_DIRECTORY"; then
    rmdir "$reclaim_directory" 2>/dev/null || true
    return 1
  fi
  rmdir "$reclaim_directory" || return 1
  if ! remove_host_lock_owner_directory "$owner_directory" "$stale_token"; then
    safe_log 'stale_host_lock_owner_cleanup_failed'
  fi
}

discard_host_lock_staging() {
  local directory="$1"
  local path
  [[ "$directory" == "${LOCK_DIRECTORY}.owner."?????? &&
    -d "$directory" && ! -L "$directory" ]] ||
    return 1
  for path in "$directory/owner" "$directory/liveness"; do
    if [[ -f "$path" && ! -L "$path" ]]; then
      rm -f "$path"
    elif [[ -e "$path" || -L "$path" ]]; then
      return 1
    fi
  done
  rmdir "$directory"
}

acquire_host_lock() {
  local existing_owner
  local lock_parent
  local lock_basename
  local liveness_file
  local owner_file
  HOST_LOCK_PLATFORM="$(uname -s)" || return 1
  case "$HOST_LOCK_PLATFORM" in
    Linux) command -v flock >/dev/null 2>&1 || return 1 ;;
    Darwin) command -v lsof >/dev/null 2>&1 || return 1 ;;
    *) return 1 ;;
  esac
  LOCK_DIRECTORY="${HAPPYLEARN_BACKUP_LOCK_DIRECTORY:-${TMPDIR:-/tmp}/happylearn-phase5-backup-${PROJECT}.lock}"
  [[ "$LOCK_DIRECTORY" == /* ]] || return 1
  lock_parent="$(dirname "$LOCK_DIRECTORY")"
  lock_basename="$(basename "$LOCK_DIRECTORY")"
  [[ -d "$lock_parent" ]] || return 1
  LOCK_DIRECTORY="$(
    cd "$lock_parent" &&
      printf '%s/%s' "$(pwd -P)" "$lock_basename"
  )" || return 1
  LOCK_OWNER_PID="$$"
  LOCK_OWNER_TOKEN="$(
    od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]'
  )"
  [[ "$LOCK_OWNER_TOKEN" =~ ^[0-9a-f]{64}$ ]] || return 1
  LOCK_OWNER_DIRECTORY="$(
    mktemp -d "${LOCK_DIRECTORY}.owner.XXXXXX"
  )" || return 1
  chmod 0700 "$LOCK_OWNER_DIRECTORY"
  liveness_file="$LOCK_OWNER_DIRECTORY/liveness"
  if ! printf '%s\n' "$LOCK_OWNER_TOKEN" >"$liveness_file" ||
    ! chmod 0400 "$liveness_file" ||
    ! LOCK_OWNER_IDENTITY="$(portable_file_identity "$liveness_file")" ||
    [[ ! "$LOCK_OWNER_IDENTITY" =~ ^[0-9]+:[0-9]+$ ]]; then
    discard_host_lock_staging "$LOCK_OWNER_DIRECTORY" || true
    LOCK_OWNER_DIRECTORY=''
    LOCK_OWNER_TOKEN=''
    return 1
  fi
  if ! exec 8<"$liveness_file"; then
    discard_host_lock_staging "$LOCK_OWNER_DIRECTORY" || true
    LOCK_OWNER_DIRECTORY=''
    LOCK_OWNER_TOKEN=''
    return 1
  fi
  LOCK_OWNER_FD_OPEN=true
  if [[ "$HOST_LOCK_PLATFORM" == Linux ]] &&
    ! flock --exclusive --nonblock --conflict-exit-code 75 8; then
    exec 8<&-
    LOCK_OWNER_FD_OPEN=false
    discard_host_lock_staging "$LOCK_OWNER_DIRECTORY" || true
    LOCK_OWNER_DIRECTORY=''
    LOCK_OWNER_TOKEN=''
    return 1
  fi
  owner_file="$LOCK_OWNER_DIRECTORY/owner"
  if ! printf 'version=1\npid=%s\nidentity=%s\ntoken=%s\n' \
    "$LOCK_OWNER_PID" "$LOCK_OWNER_IDENTITY" "$LOCK_OWNER_TOKEN" >"$owner_file" ||
    ! chmod 0400 "$owner_file"; then
    exec 8<&-
    LOCK_OWNER_FD_OPEN=false
    discard_host_lock_staging "$LOCK_OWNER_DIRECTORY" || true
    LOCK_OWNER_DIRECTORY=''
    LOCK_OWNER_TOKEN=''
    return 1
  fi
  if ! publish_host_lock; then
    existing_owner="$(existing_lock_owner_directory 2>/dev/null || true)"
    if [[ -z "$existing_owner" ]] ||
      ! reclaim_stale_host_lock "$existing_owner" ||
      ! publish_host_lock; then
      discard_unpublished_host_lock_owner \
        "$LOCK_OWNER_DIRECTORY" "$LOCK_OWNER_TOKEN" || true
      if [[ "$LOCK_OWNER_FD_OPEN" == true ]]; then
        exec 8<&-
        LOCK_OWNER_FD_OPEN=false
      fi
      LOCK_OWNER_DIRECTORY=''
      LOCK_OWNER_TOKEN=''
      return 1
    fi
  fi
  LOCK_HELD=true
  LEASE_FIFO="$LOCK_OWNER_DIRECTORY/operational.stdin"
  LEASE_OUTPUT="$LOCK_OWNER_DIRECTORY/operational.stdout"
}

compose() {
  local -a arguments=(
    --project-name "$EFFECTIVE_PROJECT"
    --file "$COMPOSE_FILE"
  )
  if [[ -n "$PRODUCTION_ENV_FILE" ]]; then
    arguments+=(--profile '*')
    arguments+=(--env-file "$PRODUCTION_ENV_FILE")
    if [[ -n ${HAPPYLEARN_LOCAL_COMPOSE_PROJECT:-} ]]; then
      [[ -f $ROOT/deploy/compose.prod.local.yml && ! -L $ROOT/deploy/compose.prod.local.yml ]] || return 1
      arguments+=(--file "$ROOT/deploy/compose.prod.local.yml")
    fi
  fi
  if [[ -n "$LIVE_ROOT" ]]; then
    arguments+=(--file "$LIVE_COMPOSE_FILE")
    arguments+=(--file "$E2E_LIVE_COMPOSE_FILE")
  fi
  docker compose "${arguments[@]}" "$@"
}

record_live_coordinator_one_shots() {
  [[ -n "$LIVE_ROOT" && -n "$LIVE_ONE_SHOT_RECORD_FILE" ]] || return 0
  local ids container_id
  ids="$(
    docker ps --all --quiet --no-trunc \
      --filter "label=com.docker.compose.project=${EFFECTIVE_PROJECT}" \
      --filter 'label=com.docker.compose.oneoff=True'
  )" || return 1
  while IFS= read -r container_id; do
    [[ -n "$container_id" ]] || continue
    [[ "$container_id" =~ ^[0-9a-f]{64}$ ]] || return 1
    if ! grep -Fxq "$container_id" "$LIVE_ONE_SHOT_RECORD_FILE"; then
      printf '%s\n' "$container_id" >>"$LIVE_ONE_SHOT_RECORD_FILE" ||
        return 1
    fi
  done <<<"$ids"
}

audit_running_production_backup_activity() {
  local service ids container_id metadata
  local inspected_id inspected_name project inspected_service
  local oneoff running extra
  for service in backup backup-storage-init backup-secrets-init; do
    ids="$(
      run_guarded_external 30 docker ps --quiet --no-trunc \
        --filter "label=com.docker.compose.project=${EFFECTIVE_PROJECT}" \
        --filter 'label=com.docker.compose.oneoff=True' \
        --filter "label=com.docker.compose.service=${service}"
    )" || return 1
    while IFS= read -r container_id; do
      [[ -n "$container_id" ]] || continue
      [[ "$container_id" =~ ^[0-9a-f]{64}$ ]] || return 1
      metadata="$(
        run_guarded_external 30 docker container inspect --format \
          '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.service"}}|{{index .Config.Labels "com.docker.compose.oneoff"}}|{{.State.Running}}' \
          "$container_id"
      )" || return 1
      IFS='|' read -r inspected_id inspected_name project \
        inspected_service oneoff running extra <<<"$metadata"
      [[ "$inspected_id" == "$container_id" &&
        "$inspected_name" == /* &&
        "$project" == "$EFFECTIVE_PROJECT" &&
        "$inspected_service" == "$service" &&
        "$oneoff" == True &&
        "$running" =~ ^(true|false)$ &&
        -z "$extra" ]] ||
        return 1
      [[ "$running" == false ]] || return 1
    done <<<"$ids"
  done
}

audit_recorded_live_coordinator_one_shots() {
  BACKUP_ACTIVITY_AUDITED=false
  if [[ -z "$LIVE_ROOT" ]]; then
    audit_running_production_backup_activity || return 1
    BACKUP_ACTIVITY_AUDITED=true
    return 0
  fi
  if [[ -z "$LIVE_ONE_SHOT_RECORD_FILE" ]]; then
    BACKUP_ACTIVITY_AUDITED=true
    return 0
  fi
  local expected_owner
  local ids container_id ownership project oneoff owner running extra
  expected_owner="${EFFECTIVE_PROJECT#happylearn-phase5-live-}"
  [[ "$EFFECTIVE_PROJECT" == "happylearn-phase5-live-${expected_owner}" &&
    "$expected_owner" =~ ^[a-f0-9]{12}$ ]] ||
    return 1
  record_live_coordinator_one_shots || return 1
  ids="$(sort -u "$LIVE_ONE_SHOT_RECORD_FILE")" || return 1
  while IFS= read -r container_id; do
    [[ -n "$container_id" ]] || continue
    [[ "$container_id" =~ ^[0-9a-f]{64}$ ]] || return 1
    ownership="$(
      docker container inspect --format \
        '{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.oneoff"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}|{{.State.Running}}' \
        "$container_id"
    )" || return 1
    IFS='|' read -r project oneoff owner running extra <<<"$ownership"
    [[ "$project" == "$EFFECTIVE_PROJECT" &&
      "$oneoff" == True &&
      "$owner" == "$expected_owner" &&
      "$running" == false &&
      -z "$extra" ]] ||
      return 1
  done <<<"$ids"
  BACKUP_ACTIVITY_AUDITED=true
}

cleanup_recorded_live_coordinator_one_shots() {
  BACKUP_ACTIVITY_AUDITED=false
  if [[ -z "$LIVE_ROOT" ]]; then
    audit_running_production_backup_activity || return 1
    BACKUP_ACTIVITY_AUDITED=true
    return 0
  fi
  if [[ -z "$LIVE_ONE_SHOT_RECORD_FILE" ]]; then
    BACKUP_ACTIVITY_AUDITED=true
    return 0
  fi
  local expected_owner
  local ids container_id ownership remaining
  audit_recorded_live_coordinator_one_shots || return 1
  BACKUP_ACTIVITY_AUDITED=false
  expected_owner="${EFFECTIVE_PROJECT#happylearn-phase5-live-}"
  [[ "$EFFECTIVE_PROJECT" == "happylearn-phase5-live-${expected_owner}" &&
    "$expected_owner" =~ ^[a-f0-9]{12}$ ]] ||
    return 1
  record_live_coordinator_one_shots || return 1
  ids="$(sort -u "$LIVE_ONE_SHOT_RECORD_FILE")" || return 1
  while IFS= read -r container_id; do
    [[ -n "$container_id" ]] || continue
    [[ "$container_id" =~ ^[0-9a-f]{64}$ ]] || return 1
    ownership="$(
      docker container inspect --format \
        '{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.oneoff"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
        "$container_id"
    )" || return 1
    [[ "$ownership" == "$EFFECTIVE_PROJECT|True|$expected_owner" ]] ||
      return 1
  done <<<"$ids"
  while IFS= read -r container_id; do
    [[ -n "$container_id" ]] || continue
    docker rm --force "$container_id" >/dev/null || return 1
  done <<<"$ids"
  remaining="$(
    docker ps --all --quiet --no-trunc \
      --filter "label=com.docker.compose.project=${EFFECTIVE_PROJECT}" \
      --filter 'label=com.docker.compose.oneoff=True'
  )" || return 1
  [[ -z "$remaining" ]] || return 1
  BACKUP_ACTIVITY_AUDITED=true
}

compose_run() {
  BACKUP_ACTIVITY_AUDITED=false
  if [[ -z "$LIVE_ROOT" ]]; then
    compose run --rm "$@"
    return
  fi
  local status=0
  compose run "$@" || status=$?
  record_live_coordinator_one_shots || return 1
  return "$status"
}

initialize_backup_mounts() {
  if [[ "$PROJECT" == 'happylearn-prod' ]]; then
    return 0
  fi
  run_guarded_external 300 \
    compose_run --no-deps backup-storage-init
  run_guarded_external 300 \
    compose_run --no-deps backup-secrets-init
}

verify_backup_mount_ownership() {
  if [[ "$PROJECT" == 'happylearn-prod' ]]; then
    run_guarded_external 120 \
      compose_run --no-deps --entrypoint /bin/sh backup -eu -c '
        test "$(stat -c "%u:%g:%a" /repository)" = "10003:0:700" || exit 1
        test "$(stat -c "%u:%g:%a" /state)" = "10003:0:700" || exit 1
        for name in database_password local_repository local_password; do
          test "$(stat -c "%u:%a" "/run/secrets/${name}")" = "10003:600" || exit 1
        done
      '
    return
  fi
  run_guarded_external 120 \
    compose_run --no-deps --entrypoint /bin/sh backup -eu -c '
      test "$(stat -c "%u:%g:%a" /repository)" = "10003:0:700" ||
        { printf "%s\n" repository_ownership >&2; exit 1; }
      test "$(stat -c "%u:%g:%a" /state)" = "10003:0:700" ||
        { printf "%s\n" state_ownership >&2; exit 1; }
      test "$(stat -c "%u:%g:%a" /run/secrets)" = "10003:0:700" ||
        { printf "%s\n" secret_directory_ownership >&2; exit 1; }
      for name in database_password local_repository local_password; do
        test "$(stat -c "%u:%g:%a" "/run/secrets/${name}")" = "10003:0:400" ||
          { printf "%s\n" required_secret_ownership >&2; exit 1; }
      done
      for name in remote_repository remote_password remote_access_key_id \
        remote_secret_access_key; do
        if test -e "/run/secrets/${name}"; then
          test "$(stat -c "%u:%g:%a" "/run/secrets/${name}")" = "10003:0:400" ||
            { printf "%s\n" optional_secret_ownership >&2; exit 1; }
        fi
      done
    '
}

database_query() {
  local query
  query="$(cat)"
  [[ -n "$query" ]] || return 1
  run_guarded_external "$DATABASE_QUERY_TIMEOUT_SECONDS" \
    database_query_text "$query"
}

database_query_text() {
  local query="$1"
  [[ -n "$query" ]] || return 1
  compose exec -T \
    -e "PGCONNECTTIMEOUT=${DATABASE_CONNECT_TIMEOUT_SECONDS}" \
    -e "PGOPTIONS=-c statement_timeout=${DATABASE_STATEMENT_TIMEOUT_MILLISECONDS} -c lock_timeout=${DATABASE_STATEMENT_TIMEOUT_MILLISECONDS}" \
    postgres psql \
    --username happylearn \
    --dbname happylearn \
    --no-psqlrc \
    --quiet \
    --tuples-only \
    --no-align \
    --set ON_ERROR_STOP=1 <<<"$query"
}

database_session() {
  compose exec -T \
    -e "PGCONNECTTIMEOUT=${DATABASE_CONNECT_TIMEOUT_SECONDS}" \
    -e "PGOPTIONS=-c statement_timeout=0" \
    postgres psql \
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
  local state selected_trigger selected_id extra line
  local selected_result=''
  local selected_count=0
  while IFS= read -r line; do
    case "$line" in
      ''|BEGIN|COMMIT) ;;
      queued\|*|draining\|*|snapshotting\|*|encrypting\|*|verifying\|*|syncing\|*|succeeded\|*|degraded\|*)
        if [[ "$line" =~ ^(queued|draining|snapshotting|encrypting|verifying|syncing|succeeded|degraded)\|(scheduled|manual|pre_release)\|[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]]; then
          selected_result="$line"
          selected_count=$((selected_count + 1))
        else
          safe_log 'queue_result_invalid_record'
          return 1
        fi
        ;;
      *)
        safe_log 'queue_result_unexpected_line'
        return 1
        ;;
    esac
  done <<<"$selected"
  if [[ "$selected_count" -ne 1 ]]; then
    safe_log "queue_result_count_${selected_count}"
    return 1
  fi
  IFS='|' read -r state selected_trigger selected_id extra <<<"$selected_result"
  [[ -z "${extra:-}" ]] || return 1
  if [[ "$selected_trigger" != "$TRIGGER" ]]; then
    safe_log 'queue_result_trigger_mismatch'
    return 1
  fi
  if [[ ! "$selected_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]]; then
    safe_log "queue_result_id_shape_${#selected_id}"
    return 1
  fi
  case "$state" in
    queued|draining|snapshotting|encrypting|verifying|syncing)
      RUN_ID="$selected_id"
      ;;
    succeeded|degraded)
      TERMINAL_RECORDED=true
      return 2
      ;;
    *)
      safe_log "queue_result_state_shape_${#state}"
      return 1
      ;;
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
    sleep "$HEARTBEAT_HANDSHAKE_POLL_SECONDS"
  done
  return 1
}

operational_lock_session_payload() {
  database_session <"$LEASE_FIFO" >"$LEASE_OUTPUT" 2>/dev/null
}

start_operational_lock_session() {
  local group_handshake
  local lease_pid
  mkfifo "$LEASE_FIFO"
  : >"$LEASE_OUTPUT"
  chmod 0600 "$LEASE_OUTPUT"
  group_handshake="$(create_group_owner_handshake)" || return 1
  set -m
  (
    run_external_group_owner \
      "$group_handshake" operational_lock_session_payload
  ) &
  lease_pid="$!"
  set +m
  if ! prepare_group_owner "$lease_pid" "$group_handshake"; then
    if ! cancel_or_terminate_group_owner \
      "$lease_pid" '' "$group_handshake"; then
      remember_uncertain_external_group "$lease_pid" '' || true
    fi
    return 1
  fi
  LEASE_GROUP_HANDSHAKE="$group_handshake"
  LEASE_PROCESS_IDENTITY="$CAPTURED_CHILD_IDENTITY"
  LEASE_PID="$lease_pid"
  if ! commit_group_owner \
    "$lease_pid" \
    "$LEASE_PROCESS_IDENTITY" \
    "$group_handshake" \
    "$LEASE_PID" \
    "$LEASE_PROCESS_IDENTITY" \
    "$LEASE_GROUP_HANDSHAKE"; then
    if cancel_or_terminate_group_owner \
      "$LEASE_PID" \
      "$LEASE_PROCESS_IDENTITY" \
      "$LEASE_GROUP_HANDSHAKE"; then
      LEASE_PID=''
      LEASE_PROCESS_IDENTITY=''
      LEASE_GROUP_HANDSHAKE=''
    else
      remember_uncertain_external_group \
        "$LEASE_PID" "$LEASE_PROCESS_IDENTITY" || true
    fi
    return 1
  fi
  LEASE_GROUP_HANDSHAKE=''
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
  local status=0
  if [[ "$LEASE_FD_OPEN" == true ]]; then
    exec 9>&-
    LEASE_FD_OPEN=false
  fi
  if [[ -n "$LEASE_PID" ]]; then
    if [[ -n "$LEASE_GROUP_HANDSHAKE" ]]; then
      cancel_or_terminate_group_owner \
        "$LEASE_PID" \
        "$LEASE_PROCESS_IDENTITY" \
        "$LEASE_GROUP_HANDSHAKE" ||
        status=1
    else
      terminate_external_group \
        "$LEASE_PID" "$LEASE_PROCESS_IDENTITY" ||
        status=1
    fi
    if [[ "$status" -eq 0 ]]; then
      LEASE_PID=''
      LEASE_PROCESS_IDENTITY=''
      LEASE_GROUP_HANDSHAKE=''
    else
      remember_uncertain_external_group \
        "$LEASE_PID" "$LEASE_PROCESS_IDENTITY" || true
    fi
  fi
  return "$status"
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

lease_heartbeat_worker() {
  local original_owner_pid="$1"
  local original_owner_process_identity="$2"
  local heartbeat_pid_file="$3"
  local heartbeat_ready_file="$4"
  local heartbeat_ready_pending="$5"
  local heartbeat_process_pid=''
  local handshake_attempts=0
  set +m
  exec 8<&-
  exec 9>&-
  while [[ "$handshake_attempts" -lt "$HEARTBEAT_HANDSHAKE_ATTEMPTS" ]]; do
    process_identity_matches \
      "$original_owner_pid" "$original_owner_process_identity" ||
      return 0
    heartbeat_process_pid="$(
      read_heartbeat_handshake_file "$heartbeat_pid_file" 2>/dev/null ||
        true
    )"
    [[ "$heartbeat_process_pid" =~ ^[1-9][0-9]*$ ]] && break
    sleep "$HEARTBEAT_HANDSHAKE_POLL_SECONDS"
    handshake_attempts=$((handshake_attempts + 1))
  done
  [[ "$heartbeat_process_pid" =~ ^[1-9][0-9]*$ ]] || return 0
  heartbeat_owner_matches \
    "$heartbeat_process_pid" \
    "$original_owner_pid" \
    "$original_owner_process_identity" ||
    return 0
  ALLOW_LOST_LEASE_ACTIONS=true
  SEPARATE_EXTERNAL_GROUP=false
  SHARED_EXTERNAL_GROUP_PID="$heartbeat_process_pid"
  write_heartbeat_handshake_file \
    "$heartbeat_ready_file" \
    "$heartbeat_ready_pending" \
    "$heartbeat_process_pid" ||
    return 0
  while heartbeat_owner_matches \
    "$heartbeat_process_pid" \
    "$original_owner_pid" \
    "$original_owner_process_identity"; do
    sleep "$HEARTBEAT_INTERVAL_SECONDS"
    heartbeat_owner_matches \
      "$heartbeat_process_pid" \
      "$original_owner_pid" \
      "$original_owner_process_identity" ||
      return 0
    if ! renew_operational_lease; then
      printf '%s\n' 'lease_lost' >"$LEASE_HEARTBEAT_FAILED"
      return 1
    fi
    heartbeat_owner_matches \
      "$heartbeat_process_pid" \
      "$original_owner_pid" \
      "$original_owner_process_identity" ||
      return 0
  done
}

start_lease_heartbeat() {
  local original_owner_pid="$LOCK_OWNER_PID"
  local original_owner_process_identity
  local group_handshake
  local heartbeat_pid
  local heartbeat_pid_file="$LOCK_OWNER_DIRECTORY/heartbeat.pid"
  local heartbeat_pid_pending="$LOCK_OWNER_DIRECTORY/heartbeat.pid.pending"
  local heartbeat_ready_file="$LOCK_OWNER_DIRECTORY/heartbeat.ready"
  local heartbeat_ready_pending="$LOCK_OWNER_DIRECTORY/heartbeat.ready.pending"
  original_owner_process_identity="$(
    portable_process_identity "$original_owner_pid"
  )" || return 1
  LEASE_HEARTBEAT_FAILED="$LOCK_OWNER_DIRECTORY/heartbeat.failed"
  [[ ! -e "$LEASE_HEARTBEAT_FAILED" &&
    ! -e "$heartbeat_pid_file" && ! -L "$heartbeat_pid_file" &&
    ! -e "$heartbeat_pid_pending" && ! -L "$heartbeat_pid_pending" &&
    ! -e "$heartbeat_ready_file" && ! -L "$heartbeat_ready_file" &&
    ! -e "$heartbeat_ready_pending" && ! -L "$heartbeat_ready_pending" ]] ||
    return 1
  group_handshake="$(create_group_owner_handshake)" || return 1
  set -m
  (
    run_external_group_owner \
      "$group_handshake" \
      lease_heartbeat_worker \
      "$original_owner_pid" \
      "$original_owner_process_identity" \
      "$heartbeat_pid_file" \
      "$heartbeat_ready_file" \
      "$heartbeat_ready_pending"
  ) &
  heartbeat_pid="$!"
  set +m
  if ! prepare_group_owner "$heartbeat_pid" "$group_handshake"; then
    if ! cancel_or_terminate_group_owner \
      "$heartbeat_pid" '' "$group_handshake"; then
      remember_uncertain_external_group "$heartbeat_pid" '' || true
    fi
    return 1
  fi
  LEASE_HEARTBEAT_HANDSHAKE="$group_handshake"
  LEASE_HEARTBEAT_IDENTITY="$CAPTURED_CHILD_IDENTITY"
  LEASE_HEARTBEAT_PID="$heartbeat_pid"
  if ! commit_group_owner \
    "$heartbeat_pid" \
    "$LEASE_HEARTBEAT_IDENTITY" \
    "$group_handshake" \
    "$LEASE_HEARTBEAT_PID" \
    "$LEASE_HEARTBEAT_IDENTITY" \
    "$LEASE_HEARTBEAT_HANDSHAKE"; then
    if cancel_or_terminate_group_owner \
      "$LEASE_HEARTBEAT_PID" \
      "$LEASE_HEARTBEAT_IDENTITY" \
      "$LEASE_HEARTBEAT_HANDSHAKE"; then
      LEASE_HEARTBEAT_PID=''
      LEASE_HEARTBEAT_IDENTITY=''
      LEASE_HEARTBEAT_HANDSHAKE=''
    else
      remember_uncertain_external_group \
        "$LEASE_HEARTBEAT_PID" "$LEASE_HEARTBEAT_IDENTITY" || true
    fi
    return 1
  fi
  LEASE_HEARTBEAT_HANDSHAKE=''
  if ! write_heartbeat_handshake_file \
    "$heartbeat_pid_file" \
    "$heartbeat_pid_pending" \
    "$LEASE_HEARTBEAT_PID" ||
    ! wait_for_heartbeat_handshake \
      "$heartbeat_ready_file" "$LEASE_HEARTBEAT_PID"; then
    stop_lease_heartbeat || true
    rm -f \
      "$heartbeat_pid_file" "$heartbeat_pid_pending" \
      "$heartbeat_ready_file" "$heartbeat_ready_pending"
    return 1
  fi
}

assert_lease_heartbeat() {
  if [[ -z "$LEASE_HEARTBEAT_PID" ||
    -e "$LEASE_HEARTBEAT_FAILED" ]] ||
    ! kill -0 "$LEASE_HEARTBEAT_PID" 2>/dev/null ||
    ! renew_operational_lease; then
    FAILURE_CATEGORY='lease_lost'
    RECOVERY_UNSAFE=true
    return 1
  fi
}

terminate_external_group() {
  local pid="$1"
  local expected_identity="$2"
  local grace_seconds="${3:-$EXTERNAL_TERMINATION_GRACE_SECONDS}"
  [[ "$pid" =~ ^[1-9][0-9]*$ && -n "$expected_identity" ]] ||
    return 1
  if ! owned_group_witness_matches "$pid" "$expected_identity"; then
    if kill -0 "$pid" 2>/dev/null ||
      kill -0 "-$pid" 2>/dev/null; then
      return 1
    fi
    wait "$pid" 2>/dev/null || true
    return 0
  fi
  kill -TERM "-$pid" 2>/dev/null || return 1
  sleep "$grace_seconds"
  if ! owned_group_witness_matches "$pid" "$expected_identity"; then
    if kill -0 "$pid" 2>/dev/null ||
      kill -0 "-$pid" 2>/dev/null; then
      return 1
    fi
    wait "$pid" 2>/dev/null || true
    return 0
  fi
  kill -KILL "-$pid" 2>/dev/null || return 1
  wait "$pid" 2>/dev/null || true
  return 0
}

remember_uncertain_external_group() {
  local pid="$1"
  local expected_identity="$2"
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  RECOVERY_UNSAFE=true
  if [[ -z "$UNCERTAIN_EXTERNAL_GROUP_PID" ]]; then
    UNCERTAIN_EXTERNAL_GROUP_PID="$pid"
    UNCERTAIN_EXTERNAL_GROUP_IDENTITY="$expected_identity"
    return 0
  fi
  [[ "$UNCERTAIN_EXTERNAL_GROUP_PID" == "$pid" &&
    "$UNCERTAIN_EXTERNAL_GROUP_IDENTITY" == "$expected_identity" ]]
}

terminate_active_external_group() {
  local pid="$ACTIVE_EXTERNAL_GROUP_PID"
  local expected_identity="$ACTIVE_EXTERNAL_GROUP_IDENTITY"
  local handshake_directory="$ACTIVE_EXTERNAL_GROUP_HANDSHAKE"
  local status=0
  [[ -n "$pid" ]] || return 0
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  if [[ "$SEPARATE_EXTERNAL_GROUP" == false ]]; then
    [[ "$SHARED_EXTERNAL_GROUP_PID" =~ ^[1-9][0-9]*$ &&
      -n "$LEASE_HEARTBEAT_FAILED" ]] ||
      return 1
    printf '%s\n' 'query_timeout' >"$LEASE_HEARTBEAT_FAILED" 2>/dev/null ||
      true
    kill -KILL "-$SHARED_EXTERNAL_GROUP_PID" 2>/dev/null
    ACTIVE_EXTERNAL_GROUP_PID=''
    ACTIVE_EXTERNAL_GROUP_IDENTITY=''
    ACTIVE_EXTERNAL_GROUP_HANDSHAKE=''
    return 1
  fi
  if [[ -n "$handshake_directory" ]]; then
    cancel_or_terminate_group_owner \
      "$pid" \
      "$expected_identity" \
      "$handshake_directory" \
      "$EXTERNAL_TERMINATION_GRACE_SECONDS" ||
      status=1
  else
    terminate_external_group \
      "$pid" "$expected_identity" "$EXTERNAL_TERMINATION_GRACE_SECONDS" ||
      status=1
  fi
  if [[ "$status" -ne 0 ]]; then
    remember_uncertain_external_group "$pid" "$expected_identity" || true
  fi
  ACTIVE_EXTERNAL_GROUP_PID=''
  ACTIVE_EXTERNAL_GROUP_IDENTITY=''
  ACTIVE_EXTERNAL_GROUP_HANDSHAKE=''
  return "$status"
}

cleanup_timed_out_external() {
  if ! terminate_active_external_group; then
    EXTERNAL_CLEANUP_UNSAFE=true
    RECOVERY_UNSAFE=true
    return 1
  fi
  if ! cleanup_recorded_live_coordinator_one_shots; then
    EXTERNAL_CLEANUP_UNSAFE=true
    RECOVERY_UNSAFE=true
    return 1
  fi
}

wait_for_lease_session_exit() {
  local pid="$1"
  local deadline=$((SECONDS + DATABASE_QUERY_TIMEOUT_SECONDS))
  while kill -0 "$pid" 2>/dev/null; do
    if ((SECONDS >= deadline)); then
      return 1
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
  wait "$pid"
}

run_guarded_external() {
  local timeout_seconds="$1"
  shift
  valid_uint "$timeout_seconds" || return 1
  if [[ -n "$UNCERTAIN_EXTERNAL_GROUP_PID" &&
    "$CLEANING_UP" != true ]]; then
    RECOVERY_UNSAFE=true
    safe_log 'external_group_unsafe'
    return 1
  fi
  local command_status=0
  local deadline
  local pid
  local group_handshake=''
  if [[ "$SEPARATE_EXTERNAL_GROUP" == true ]]; then
    group_handshake="$(create_group_owner_handshake)" || return 1
    set -m
    (run_external_group_owner "$group_handshake" "$@") &
    pid="$!"
    set +m
    if ! prepare_group_owner "$pid" "$group_handshake"; then
      safe_log 'external_group_prepare_failed'
      if ! cancel_or_terminate_group_owner \
        "$pid" '' "$group_handshake"; then
        remember_uncertain_external_group "$pid" '' || true
      fi
      return 1
    fi
    ACTIVE_EXTERNAL_GROUP_HANDSHAKE="$group_handshake"
    ACTIVE_EXTERNAL_GROUP_IDENTITY="$CAPTURED_CHILD_IDENTITY"
    ACTIVE_EXTERNAL_GROUP_PID="$pid"
    if ! commit_group_owner \
      "$pid" \
      "$ACTIVE_EXTERNAL_GROUP_IDENTITY" \
      "$group_handshake" \
      "$ACTIVE_EXTERNAL_GROUP_PID" \
      "$ACTIVE_EXTERNAL_GROUP_IDENTITY" \
      "$ACTIVE_EXTERNAL_GROUP_HANDSHAKE"; then
      safe_log 'external_group_commit_failed'
      terminate_active_external_group || true
      return 1
    fi
    ACTIVE_EXTERNAL_GROUP_HANDSHAKE=''
  else
    ( "$@" ) &
    pid="$!"
    ACTIVE_EXTERNAL_GROUP_IDENTITY=''
    ACTIVE_EXTERNAL_GROUP_HANDSHAKE=''
    ACTIVE_EXTERNAL_GROUP_PID="$pid"
  fi
  deadline=$((SECONDS + timeout_seconds))
  while kill -0 "$pid" 2>/dev/null; do
    if [[ "$ALLOW_LOST_LEASE_ACTIONS" == false &&
      "$LEASE_DURABLE" == true ]] &&
      { [[ -e "$LEASE_HEARTBEAT_FAILED" ]] ||
        [[ -z "$LEASE_HEARTBEAT_PID" ]] ||
        ! kill -0 "$LEASE_HEARTBEAT_PID" 2>/dev/null; }; then
      FAILURE_CATEGORY='lease_lost'
      RECOVERY_UNSAFE=true
      if ! cleanup_timed_out_external; then
        RECOVERY_UNSAFE=true
      fi
      return 1
    fi
    if ((SECONDS >= deadline)); then
      FAILURE_CATEGORY='timeout'
      if ! cleanup_timed_out_external; then
        RECOVERY_UNSAFE=true
      fi
      return 1
    fi
    sleep "$EXTERNAL_MONITOR_POLL_SECONDS"
  done
  if wait "$pid"; then
    command_status=0
  else
    command_status=$?
  fi
  if [[ "$SEPARATE_EXTERNAL_GROUP" == true ]] &&
    kill -0 "-$pid" 2>/dev/null; then
    safe_log 'external_group_descendant_survived'
    remember_uncertain_external_group \
      "$pid" "$ACTIVE_EXTERNAL_GROUP_IDENTITY" || true
    return 1
  fi
  ACTIVE_EXTERNAL_GROUP_PID=''
  ACTIVE_EXTERNAL_GROUP_IDENTITY=''
  ACTIVE_EXTERNAL_GROUP_HANDSHAKE=''
  return "$command_status"
}

stop_lease_heartbeat() {
  local status=0
  if [[ -n "$LEASE_HEARTBEAT_PID" ]]; then
    if [[ -n "$LEASE_HEARTBEAT_HANDSHAKE" ]]; then
      cancel_or_terminate_group_owner \
        "$LEASE_HEARTBEAT_PID" \
        "$LEASE_HEARTBEAT_IDENTITY" \
        "$LEASE_HEARTBEAT_HANDSHAKE" ||
        status=1
    else
      terminate_external_group \
        "$LEASE_HEARTBEAT_PID" "$LEASE_HEARTBEAT_IDENTITY" ||
        status=1
    fi
    if [[ "$status" -eq 0 ]]; then
      LEASE_HEARTBEAT_PID=''
      LEASE_HEARTBEAT_IDENTITY=''
      LEASE_HEARTBEAT_HANDSHAKE=''
    else
      remember_uncertain_external_group \
        "$LEASE_HEARTBEAT_PID" "$LEASE_HEARTBEAT_IDENTITY" || true
    fi
  fi
  return "$status"
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
  run_guarded_external "$((EXTERNAL_TIMEOUT_SECONDS + 15))" \
    bounded_backup_compose "$@"
}

bounded_backup_compose() {
  compose_run --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /app/happylearn-backup "$@"
}

valid_run_uuid() {
  [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]]
}

sync_hostname_for_run() {
  local run_id="$1"
  valid_run_uuid "$run_id" || return 1
  printf 'phase5-sync-%s' "${run_id//-/}"
}

prepare_sync_container_identity() {
  valid_run_uuid "$RUN_ID" || return 1
  SYNC_CONTAINER_NAME="${EFFECTIVE_PROJECT}-phase5-sync-${RUN_ID}"
  SYNC_CONTAINER_HOSTNAME="$(sync_hostname_for_run "$RUN_ID")" || return 1
  SYNC_CONTAINER_OWNER="$(
    od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]'
  )"
  [[ "$SYNC_CONTAINER_NAME" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]+$ &&
    "$SYNC_CONTAINER_HOSTNAME" =~ ^[a-z0-9][a-z0-9-]{0,62}$ &&
    "$SYNC_CONTAINER_OWNER" =~ ^[0-9a-f]{64}$ ]]
}

bounded_backup_sync_compose() {
  local removal_argument='--rm'
  [[ -z "$LIVE_ROOT" ]] || removal_argument=''
  export HAPPYLEARN_BACKUP_CONTAINER_HOSTNAME="$SYNC_CONTAINER_HOSTNAME"
  compose run \
    --name "$SYNC_CONTAINER_NAME" \
    --label "${SYNC_RUN_LABEL}=${RUN_ID}" \
    --label "${SYNC_OWNER_LABEL}=${SYNC_CONTAINER_OWNER}" \
    ${removal_argument:+"$removal_argument"} \
    --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /app/happylearn-backup sync --run-id "$RUN_ID"
}

sync_container_record() {
  run_guarded_external 30 docker ps --all --no-trunc \
    --filter "name=^/${SYNC_CONTAINER_NAME}$" \
    --format "{{.ID}}|{{.Names}}|{{.Label \"${SYNC_RUN_LABEL}\"}}|{{.Label \"${SYNC_OWNER_LABEL}\"}}|{{.State}}"
}

validate_sync_container_record() {
  local record="$1"
  local container_id name run_id owner state extra
  IFS='|' read -r container_id name run_id owner state extra <<<"$record"
  [[ -z "${extra:-}" &&
    "$container_id" =~ ^[0-9a-f]{64}$ &&
    "$name" == "$SYNC_CONTAINER_NAME" &&
    "$run_id" == "$RUN_ID" &&
    "$owner" == "$SYNC_CONTAINER_OWNER" &&
    ( -z "$SYNC_CONTAINER_ID" || "$container_id" == "$SYNC_CONTAINER_ID" ) ]] ||
    return 1
  SYNC_CONTAINER_ID="$container_id"
}

cleanup_sync_container() {
  local saved_failure="$FAILURE_CATEGORY"
  local record
  if ! record="$(sync_container_record)"; then
    FAILURE_CATEGORY="$saved_failure"
    RECOVERY_UNSAFE=true
    return 1
  fi
  if [[ -z "$record" ]]; then
    FAILURE_CATEGORY="$saved_failure"
    return 0
  fi
  if [[ "$record" == *$'\n'* ]] ||
    ! validate_sync_container_record "$record"; then
    FAILURE_CATEGORY="$saved_failure"
    RECOVERY_UNSAFE=true
    return 1
  fi
  if ! run_guarded_external "$((SYNC_CONTAINER_STOP_TIMEOUT_SECONDS + 15))" \
    docker stop --time "$SYNC_CONTAINER_STOP_TIMEOUT_SECONDS" \
    "$SYNC_CONTAINER_ID" >/dev/null; then
    if ! record="$(sync_container_record)" || [[ -n "$record" ]]; then
      FAILURE_CATEGORY="$saved_failure"
      RECOVERY_UNSAFE=true
      return 1
    fi
    FAILURE_CATEGORY="$saved_failure"
    return 0
  fi
  if ! record="$(sync_container_record)"; then
    FAILURE_CATEGORY="$saved_failure"
    RECOVERY_UNSAFE=true
    return 1
  fi
  if [[ -z "$record" ]]; then
    FAILURE_CATEGORY="$saved_failure"
    return 0
  fi
  validate_sync_container_record "$record" || {
    FAILURE_CATEGORY="$saved_failure"
    RECOVERY_UNSAFE=true
    return 1
  }
  if ! run_guarded_external 30 docker container wait \
    "$SYNC_CONTAINER_ID" >/dev/null; then
    if ! record="$(sync_container_record)" || [[ -n "$record" ]]; then
      FAILURE_CATEGORY="$saved_failure"
      RECOVERY_UNSAFE=true
      return 1
    fi
    FAILURE_CATEGORY="$saved_failure"
    return 0
  fi
  if ! record="$(sync_container_record)"; then
    FAILURE_CATEGORY="$saved_failure"
    RECOVERY_UNSAFE=true
    return 1
  fi
  if [[ -n "$record" ]]; then
    validate_sync_container_record "$record" || {
      FAILURE_CATEGORY="$saved_failure"
      RECOVERY_UNSAFE=true
      return 1
    }
    if ! run_guarded_external 30 docker rm \
      "$SYNC_CONTAINER_ID" >/dev/null; then
      if ! record="$(sync_container_record)" || [[ -n "$record" ]]; then
        FAILURE_CATEGORY="$saved_failure"
        RECOVERY_UNSAFE=true
        return 1
      fi
    fi
  fi
  FAILURE_CATEGORY="$saved_failure"
}

backup_sync_command() {
  local command_status cleanup_status
  BACKUP_ACTIVITY_AUDITED=false
  prepare_sync_container_identity || return 1
  SYNC_CONTAINER_ID=''
  if run_guarded_external "$((EXTERNAL_TIMEOUT_SECONDS + 15))" \
    bounded_backup_sync_compose; then
    command_status=0
  else
    command_status=$?
  fi
  if [[ -n "$LIVE_ROOT" ]]; then
    if record_live_coordinator_one_shots; then
      cleanup_status=0
    else
      cleanup_status=$?
    fi
  elif cleanup_sync_container; then
    cleanup_status=0
  else
    cleanup_status=$?
  fi
  [[ "$cleanup_status" -eq 0 ]] || return "$cleanup_status"
  if [[ "$command_status" -ne 0 ]] &&
    ! unlock_local_sync_run "$RUN_ID"; then
    safe_log 'local_sync_lock_cleanup_deferred'
  fi
  return "$command_status"
}

worker_is_stopped() {
  local running
  running="$(compose ps --status running --quiet worker)" || return 1
  [[ -z "$running" ]]
}

stop_and_verify_worker() {
  FAILURE_CATEGORY='object_store_stop'
  WORKER_STOP_REQUESTED=true
  if ! run_guarded_external "$((SERVICE_STOP_TIMEOUT_SECONDS + 30))" \
    compose stop --timeout "$SERVICE_STOP_TIMEOUT_SECONDS" worker; then
    RECOVERY_UNSAFE=true
    return 1
  fi
  if ! run_guarded_external 30 worker_is_stopped; then
    RECOVERY_UNSAFE=true
    return 1
  fi
  WORKER_STOPPED=true
}

stop_snapshot_services() {
  [[ "$WORKER_STOPPED" == true ]] || return 1
  FAILURE_CATEGORY='object_store_stop'
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

restart_aistor_service() {
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
}

restart_stopped_services() {
  restart_aistor_service || return 1
  restart_worker_service
}

restart_worker_service() {
  [[ "$WORKER_STOP_REQUESTED" == true ]] || return 0
  [[ "$WORKER_STOPPED" == true &&
    "$BACKUP_ACTIVITY_AUDITED" == true &&
    "$RECOVERY_UNSAFE" == false &&
    "$EXTERNAL_CLEANUP_UNSAFE" == false &&
    -z "$UNCERTAIN_EXTERNAL_GROUP_PID" ]] || {
    RECOVERY_UNSAFE=true
    return 1
  }
  if ! run_guarded_external 60 \
    compose up --detach --no-deps worker; then
    RECOVERY_UNSAFE=true
    return 1
  fi
  WORKER_STOPPED=false
  WORKER_STOP_REQUESTED=false
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
SELECT min(artifacts.snapshot_id)
FROM backup_artifacts AS artifacts
JOIN backup_runs AS runs ON runs.id=artifacts.backup_run_id
WHERE artifacts.backup_run_id='${RUN_ID}'::uuid
  AND runs.state='syncing'
  AND artifacts.repository='remote'
  AND artifacts.kind IN ('database_dump','object_snapshot','manifest')
  AND artifacts.snapshot_id ~ '^[0-9a-f]{64}$'
GROUP BY artifacts.backup_run_id
HAVING count(*)=3
   AND count(DISTINCT artifacts.kind)=3
   AND count(DISTINCT artifacts.snapshot_id)=1;
SQL
}

local_restic() {
  run_guarded_external "$((EXTERNAL_TIMEOUT_SECONDS + 15))" \
    compose_run --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" restic \
    --no-cache \
    --repository-file /run/secrets/local_repository \
    --password-file /run/secrets/local_password \
    "$@"
}

remote_restic() {
  run_guarded_external "$((EXTERNAL_TIMEOUT_SECONDS + 15))" \
    compose_run --no-deps --entrypoint /bin/sh backup \
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

bounded_local_unlock() {
  local hostname="$1"
  export HAPPYLEARN_BACKUP_CONTAINER_HOSTNAME="$hostname"
  compose_run --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${SYNC_CONTAINER_STOP_TIMEOUT_SECONDS}s" \
    restic --no-cache \
    --repository-file /run/secrets/local_repository \
    --password-file /run/secrets/local_password \
    unlock
}

bounded_remote_unlock() {
  local hostname="$1"
  export HAPPYLEARN_BACKUP_CONTAINER_HOSTNAME="$hostname"
  compose_run --no-deps --entrypoint /bin/sh backup \
    -eu -c '
      deadline="$1"
      AWS_ACCESS_KEY_ID="$(sed -n "1p" /run/secrets/remote_access_key_id)"
      AWS_SECRET_ACCESS_KEY="$(sed -n "1p" /run/secrets/remote_secret_access_key)"
      RESTIC_REPOSITORY="$(sed -n "1p" /run/secrets/remote_repository)"
      RESTIC_PASSWORD="$(sed -n "1p" /run/secrets/remote_password)"
      export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY RESTIC_REPOSITORY RESTIC_PASSWORD
      exec /usr/bin/timeout --foreground --kill-after=10s "$deadline" restic \
        --no-cache unlock
    ' phase5-remote-unlock "${SYNC_CONTAINER_STOP_TIMEOUT_SECONDS}s"
}

unlock_local_sync_run() {
  local hostname
  hostname="$(sync_hostname_for_run "$1")" || return 1
  # Recovery is intentionally limited to default restic unlock semantics.
  run_guarded_external "$((SYNC_CONTAINER_STOP_TIMEOUT_SECONDS + 15))" \
    bounded_local_unlock "$hostname"
}

unlock_remote_sync_run() {
  local hostname
  hostname="$(sync_hostname_for_run "$1")" || return 1
  run_guarded_external "$((SYNC_CONTAINER_STOP_TIMEOUT_SECONDS + 15))" \
    bounded_remote_unlock "$hostname"
}

pending_degraded_sync_runs() {
  local records line count=0
  records="$(
    database_query <<'SQL'
-- PHASE5_QUERY_DEGRADED_SYNC_RUNS
SELECT id::text
FROM backup_runs
WHERE state='degraded'
  AND error_category='remote_unavailable'
  AND finished_at IS NOT NULL
  AND finished_at>COALESCE(
    (SELECT max(finished_at)
     FROM backup_runs
     WHERE state='succeeded' AND remote_snapshot_id IS NOT NULL),
    '-infinity'::timestamptz
  )
ORDER BY finished_at DESC,id DESC
LIMIT 32;
SQL
  )" || return 1
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    valid_run_uuid "$line" || {
      safe_log 'degraded_sync_run_invalid_record'
      return 1
    }
    count=$((count + 1))
    [[ "$count" -le 32 ]] || {
      safe_log 'degraded_sync_run_count_exceeded'
      return 1
    }
    printf '%s\n' "$line"
  done <<<"$records"
}

cleanup_pending_local_sync_locks() {
  local records run_id
  records="$(pending_degraded_sync_runs)" || return 1
  while IFS= read -r run_id; do
    [[ -n "$run_id" ]] || continue
    unlock_local_sync_run "$run_id" || return 1
  done <<<"$records"
}

cleanup_pending_remote_sync_locks() {
  local records run_id
  records="$(pending_degraded_sync_runs)" || return 1
  while IFS= read -r run_id; do
    [[ -n "$run_id" ]] || continue
    unlock_remote_sync_run "$run_id" || return 1
  done <<<"$records"
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

run_retention() {
  local repository="$1"
  case "$repository" in
    local|remote) ;;
    *) return 1 ;;
  esac
  run_guarded_external "$((EXTERNAL_TIMEOUT_SECONDS + 15))" \
    compose_run --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /app/happylearn-backup-retention \
    --repository "$repository" --run-id "$RUN_ID"
}

complete_remote_degraded() {
  if [[ "$FAILURE_CATEGORY" == 'lease_lost' ]]; then
    return 1
  fi
  FAILURE_CATEGORY='remote_unavailable'
  backup_command fail --run-id "$RUN_ID" --category "$FAILURE_CATEGORY"
  TERMINAL_RECORDED=true
  audit_recorded_live_coordinator_one_shots || {
    RECOVERY_UNSAFE=true
    return 1
  }
  restart_stopped_services || return 1
  release_operational_lease
}

record_failure() {
  [[ -n "$RUN_ID" && "$TERMINAL_RECORDED" == false ]] || return 0
  if [[ "$WORKER_STOPPED" != true ||
    "$FAILURE_CATEGORY" == lease_lost ]]; then
    if record_unprepared_failure; then
      TERMINAL_RECORDED=true
      return 0
    fi
    return 1
  fi
  if run_guarded_external "$((EXTERNAL_TIMEOUT_SECONDS + 15))" \
    bounded_backup_compose \
    fail --run-id "$RUN_ID" --category "$FAILURE_CATEGORY"; then
    TERMINAL_RECORDED=true
    return 0
  fi
  if record_unprepared_failure; then
    TERMINAL_RECORDED=true
    return 0
  fi
  return 1
}

record_unprepared_failure() {
  [[ "$FAILURE_CATEGORY" =~ ^(drain_timeout|database_dump|object_store_stop|snapshot|object_store_restart|integrity|remote_sync|remote_unavailable|retention|lease_lost|timeout|internal)$ ]] ||
    return 1
  local recorded
  recorded="$(
    database_query <<SQL
-- PHASE5_QUERY_UNPREPARED_FAIL
UPDATE backup_runs
SET state='failed',
    finished_at=clock_timestamp(),
    error_category='${FAILURE_CATEGORY}',
    error_trace_id='',
    owner_id=NULL,
    lease_expires_at=NULL
WHERE id='${RUN_ID}'::uuid
  AND state='queued'
  AND owner_id IS NULL
  AND lease_expires_at IS NULL
RETURNING 'recorded';
SQL
  )"
  [[ "$recorded" == 'recorded' ]]
}

stop_operational_lock_session() {
  if [[ "$LEASE_FD_OPEN" != true ]]; then
    [[ -n "$LEASE_PID" ]] || return 0
    abort_operational_lock_session
    return
  fi
  printf '%s\n' \
    "SELECT pg_advisory_unlock(${OPERATIONS_ADVISORY_KEY}); -- PHASE5_RELEASE_LOCK" \
    "SELECT 'PHASE5_LEASE_RELEASED';" \
    "\\q" >&9 || true
  exec 9>&-
  LEASE_FD_OPEN=false
  if [[ -n "$LEASE_PID" ]]; then
    if ! wait_for_marker \
      'PHASE5_LEASE_RELEASED' \
      "$DATABASE_QUERY_TIMEOUT_SECONDS"; then
      safe_log 'lease_release_marker_timeout'
      abort_operational_lock_session
      return 1
    fi
    local released_pid="$LEASE_PID"
    if ! wait_for_lease_session_exit "$released_pid"; then
      safe_log 'lease_session_exit_timeout'
      abort_operational_lock_session
      return 1
    fi
    LEASE_PID=''
    LEASE_PROCESS_IDENTITY=''
    LEASE_GROUP_HANDSHAKE=''
  fi
  LEASE_PID=''
  LEASE_PROCESS_IDENTITY=''
  LEASE_GROUP_HANDSHAKE=''
  return 0
}

release_operational_lease() {
  local released
  if [[ "$LEASE_DURABLE" == true ]]; then
    if [[ "$RECOVERY_UNSAFE" == true ]]; then
      if ! transition_operational_mode release; then
        stop_lease_heartbeat || RECOVERY_UNSAFE=true
        return 1
      fi
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
      if [[ "$released" != 'released' ]]; then
        safe_log 'durable_lease_release_failed'
        RECOVERY_UNSAFE=true
        stop_lease_heartbeat || RECOVERY_UNSAFE=true
        return 1
      fi
    fi
    LEASE_DURABLE=false
  fi
  if ! stop_lease_heartbeat; then
    RECOVERY_UNSAFE=true
    stop_operational_lock_session || true
    return 1
  fi
  if ! stop_operational_lock_session; then
    RECOVERY_UNSAFE=true
    return 1
  fi
}

remove_host_lock() {
  local published_owner
  [[ "$LOCK_HELD" == true ]] || return 0
  [[ "$LOCK_DIRECTORY" == /* && -L "$LOCK_DIRECTORY" ]] || return 1
  published_owner="$(existing_lock_owner_directory)" || return 1
  [[ "$published_owner" == "$LOCK_OWNER_DIRECTORY" ]] || return 1
  host_lock_owner_matches \
    "$LOCK_OWNER_DIRECTORY" \
    "$LOCK_OWNER_TOKEN" \
    "$LOCK_OWNER_PID" \
    "$LOCK_OWNER_IDENTITY" ||
    return 1
  [[ -z "$LEASE_FIFO" || "$LEASE_FIFO" == "$LOCK_OWNER_DIRECTORY/operational.stdin" ]] ||
    return 1
  [[ -z "$LEASE_OUTPUT" || "$LEASE_OUTPUT" == "$LOCK_OWNER_DIRECTORY/operational.stdout" ]] ||
    return 1
  [[ -z "$LEASE_FIFO" || ! -e "$LEASE_FIFO" || -p "$LEASE_FIFO" ]] || return 1
  if [[ -n "$LEASE_FIFO" && -p "$LEASE_FIFO" ]]; then
    rm -f "$LEASE_FIFO"
  fi
  if [[ -n "$LEASE_OUTPUT" && -f "$LEASE_OUTPUT" && ! -L "$LEASE_OUTPUT" ]]; then
    rm -f "$LEASE_OUTPUT"
  fi
  if [[ -n "$LEASE_HEARTBEAT_FAILED" &&
    "$LEASE_HEARTBEAT_FAILED" == "$LOCK_OWNER_DIRECTORY/heartbeat.failed" &&
    -f "$LEASE_HEARTBEAT_FAILED" && ! -L "$LEASE_HEARTBEAT_FAILED" ]]; then
    rm -f "$LEASE_HEARTBEAT_FAILED"
  fi
  rm -f "$LOCK_DIRECTORY"
  if [[ "$LOCK_OWNER_FD_OPEN" == true ]]; then
    exec 8<&-
    LOCK_OWNER_FD_OPEN=false
  fi
  remove_host_lock_owner_directory \
    "$LOCK_OWNER_DIRECTORY" "$LOCK_OWNER_TOKEN"
  LOCK_HELD=false
  LOCK_OWNER_DIRECTORY=''
  LOCK_OWNER_TOKEN=''
}

cleanup() {
  local status=$?
  local failed_stage="$FAILURE_CATEGORY"
  local signal_cleanup=false
  local signal_status=''
  local external_group_safe=true
  if [[ "$CLEANING_UP" == true ]]; then
    exit "$status"
  fi
  CLEANING_UP=true
  trap - EXIT HUP INT TERM
  ALLOW_LOST_LEASE_ACTIONS=true
  case "$status" in
    129|130|143)
      signal_cleanup=true
      signal_status="$status"
      ;;
  esac
  if [[ "$EXTERNAL_CLEANUP_UNSAFE" == true ||
    -n "$UNCERTAIN_EXTERNAL_GROUP_PID" ]]; then
    safe_log 'cleanup_external_state_unsafe'
    RECOVERY_UNSAFE=true
    external_group_safe=false
    status=1
  fi
  if ! terminate_active_external_group; then
    safe_log 'cleanup_external_group_failed'
    RECOVERY_UNSAFE=true
    external_group_safe=false
    status=1
  fi
  if [[ "$external_group_safe" == true ]]; then
    if ! restart_aistor_service; then
      safe_log 'cleanup_aistor_restart_failed'
      status=1
    fi
    FAILURE_CATEGORY="$failed_stage"
    if [[ "$status" -ne 0 ]] && ! record_failure; then
      safe_log 'cleanup_record_failure_failed'
      RECOVERY_UNSAFE=true
      status=1
    fi
    if [[ "$signal_cleanup" == true ]]; then
      if ! cleanup_recorded_live_coordinator_one_shots; then
        safe_log 'cleanup_live_one_shots_failed'
        RECOVERY_UNSAFE=true
        status=1
      fi
    elif ! audit_recorded_live_coordinator_one_shots; then
      safe_log 'cleanup_live_one_shot_audit_failed'
      RECOVERY_UNSAFE=true
      status=1
    fi
    if [[ "$RECOVERY_UNSAFE" == false ]] &&
      ! restart_worker_service; then
      safe_log 'cleanup_worker_restart_failed'
      status=1
    fi
  elif [[ "$status" -ne 0 && -n "$RUN_ID" &&
    "$TERMINAL_RECORDED" == false ]]; then
    FAILURE_CATEGORY="$failed_stage"
    if record_unprepared_failure; then
      TERMINAL_RECORDED=true
    else
      safe_log 'cleanup_unprepared_failure_record_failed'
      status=1
    fi
  fi
  if ! release_operational_lease; then
    safe_log 'cleanup_release_failed'
    RECOVERY_UNSAFE=true
    status=1
    stop_operational_lock_session
  fi
  if ! remove_host_lock; then
    safe_log 'cleanup_host_lock_failed'
    status=1
  fi
  if [[ "$signal_cleanup" == true ]]; then
    status="$signal_status"
  fi
  if [[ "$status" -ne 0 ]]; then
    safe_log "failed_${CURRENT_STAGE}_${failed_stage}"
  fi
  exit "$status"
}

main() {
  validate_arguments "$@"
  CURRENT_STAGE='paths'
  resolve_root
  validate_paths_and_secrets
  configure_live_context
  trap cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  acquire_host_lock
  CURRENT_STAGE='queue'
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
  CURRENT_STAGE='run_id_publish'
  publish_live_run_id
  CURRENT_STAGE='lease_values'
  prepare_lease_values
  CURRENT_STAGE='lock_session'
  start_operational_lock_session
  CURRENT_STAGE='durable_lease'
  acquire_durable_lease
  CURRENT_STAGE='heartbeat'
  start_lease_heartbeat
  assert_lease_heartbeat

  FAILURE_CATEGORY='drain_timeout'
  CURRENT_STAGE='drain'
  wait_for_durable_drain
  transition_operational_mode backup
  assert_lease_heartbeat
  CURRENT_STAGE='worker_stop'
  stop_and_verify_worker
  assert_lease_heartbeat

  FAILURE_CATEGORY='internal'
  CURRENT_STAGE='mount_init'
  initialize_backup_mounts
  CURRENT_STAGE='mount_verify'
  verify_backup_mount_ownership
  assert_lease_heartbeat

  FAILURE_CATEGORY='integrity'
  CURRENT_STAGE='prior_sync_lock_cleanup'
  cleanup_pending_local_sync_locks
  assert_lease_heartbeat

  FAILURE_CATEGORY='internal'
  CURRENT_STAGE='prepare'
  backup_command prepare --run-id "$RUN_ID"
  assert_lease_heartbeat
  FAILURE_CATEGORY='integrity'
  CURRENT_STAGE='local_repository'
  ensure_repository local
  assert_lease_heartbeat

  CURRENT_STAGE='aistor_stop'
  stop_snapshot_services

  FAILURE_CATEGORY='snapshot'
  CURRENT_STAGE='snapshot'
  backup_command snapshot --run-id "$RUN_ID"
  CURRENT_STAGE='aistor_restart'
  restart_aistor_service
  assert_lease_heartbeat

  FAILURE_CATEGORY='integrity'
  CURRENT_STAGE='verify'
  backup_command verify --run-id "$RUN_ID"
  assert_lease_heartbeat
  LOCAL_SNAPSHOT_ID="$(local_snapshot_id)"
  [[ "$LOCAL_SNAPSHOT_ID" =~ ^[0-9a-f]{64}$ ]] || return 1
  restic_check local
  assert_lease_heartbeat

  if [[ "$REMOTE_ENABLED" == true ]]; then
    local remote_repository_ready=false
    local remote_cleanup_ready=true
    FAILURE_CATEGORY='remote_sync'
    if ! ensure_repository remote; then
      safe_log 'remote_repository_unavailable'
      REMOTE_DEGRADED=true
    else
      remote_repository_ready=true
      if ! cleanup_pending_remote_sync_locks; then
        safe_log 'remote_sync_lock_cleanup_failed'
        REMOTE_DEGRADED=true
        remote_cleanup_ready=false
      fi
    fi
    assert_lease_heartbeat
    FAILURE_CATEGORY='remote_sync'
    if [[ "$remote_cleanup_ready" == false ]]; then
      safe_log 'remote_sync_skipped_after_lock_cleanup_failure'
      REMOTE_SNAPSHOT_ID=''
    elif ! backup_sync_command; then
      [[ "$RECOVERY_UNSAFE" == false ]] || return 1
      if [[ "$remote_repository_ready" == true ]] &&
        ! unlock_remote_sync_run "$RUN_ID"; then
        safe_log 'remote_sync_lock_cleanup_deferred'
      fi
      safe_log 'remote_sync_command_failed'
      REMOTE_DEGRADED=true
      REMOTE_SNAPSHOT_ID=''
    elif REMOTE_SNAPSHOT_ID="$(remote_snapshot_id)" &&
      [[ "$REMOTE_SNAPSHOT_ID" =~ ^[0-9a-f]{64}$ ]]; then
      REMOTE_DEGRADED=false
    else
      safe_log 'remote_sync_evidence_missing'
      REMOTE_DEGRADED=true
      REMOTE_SNAPSHOT_ID=''
    fi
    assert_lease_heartbeat
  fi

  FAILURE_CATEGORY='retention'
  CURRENT_STAGE='local_retention'
  if ! run_retention local; then
    return 1
  fi
  assert_lease_heartbeat

  if [[ "$REMOTE_ENABLED" == true && "$REMOTE_DEGRADED" == true ]]; then
    complete_remote_degraded
    return
  fi

  if [[ "$REMOTE_ENABLED" == true && "$REMOTE_DEGRADED" == false ]]; then
    if ! restic_check remote; then
      safe_log 'remote_repository_check_failed'
      complete_remote_degraded
      return
    fi
    assert_lease_heartbeat
    if ! run_retention remote; then
      safe_log 'remote_retention_failed'
      complete_remote_degraded
      return
    fi
    assert_lease_heartbeat
  fi

  FAILURE_CATEGORY='internal'
  CURRENT_STAGE='finish'
  backup_command finish --run-id "$RUN_ID"
  TERMINAL_RECORDED=true
  CURRENT_STAGE='one_shot_audit'
  if ! audit_recorded_live_coordinator_one_shots; then
    RECOVERY_UNSAFE=true
    return 1
  fi
  CURRENT_STAGE='worker_restart'
  restart_worker_service
  assert_lease_heartbeat
  if ! release_operational_lease; then
    safe_log 'main_release_failed'
    RECOVERY_UNSAFE=true
    return 1
  fi
}

main "$@"
