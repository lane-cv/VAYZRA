#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

readonly USAGE='Usage: scripts/phase5-backup.sh --project happylearn-dev --trigger scheduled|manual|pre_release'
readonly OPERATIONS_ADVISORY_KEY='845103120'
readonly BACKUP_ADVISORY_KEY='845103121'
readonly SYNC_RUN_LABEL='io.happylearn.phase5.sync-run'
readonly SYNC_OWNER_LABEL='io.happylearn.phase5.sync-owner'

PROJECT=''
EFFECTIVE_PROJECT=''
TRIGGER=''
ROOT=''
COMPOSE_FILE=''
LIVE_COMPOSE_FILE=''
LIVE_ROOT=''
LOCK_DIRECTORY=''
LOCK_HELD=false
LOCK_OWNER_DIRECTORY=''
LOCK_OWNER_TOKEN=''
LOCK_OWNER_PID=''
LOCK_OWNER_IDENTITY=''
LOCK_OWNER_FD_OPEN=false
OBSERVED_LOCK_OWNER_PID=''
OBSERVED_LOCK_OWNER_IDENTITY=''
OBSERVED_LOCK_OWNER_TOKEN=''
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
SEPARATE_EXTERNAL_GROUP=true
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
  COMPOSE_FILE="$ROOT/deploy/compose.dev.yml"
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
  [[ -f "$path" && ! -L "$path" && "$(portable_mode "$path")" == '400' ]] ||
    return 1
  if stat -f '%z' "$path" >/dev/null 2>&1; then
    size="$(stat -f '%z' "$path")"
  else
    size="$(stat -c '%s' "$path")"
  fi
  [[ "$size" -ge 1 && "$size" -le 4096 ]]
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
  owner_only_directory "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" || return 1
  owner_only_directory "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" || return 1
  owner_only_directory "$HAPPYLEARN_BACKUP_STATE_DIRECTORY" || return 1
  single_line_secret_value \
    "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/database_password" >/dev/null ||
    return 1
  [[ "$(single_line_secret_value \
    "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_repository")" == '/repository' ]] ||
    return 1
  single_line_secret_value \
    "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_password" >/dev/null ||
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
  EFFECTIVE_PROJECT="$live_project"
}

portable_file_identity() {
  local path="$1"
  if stat -f '%d:%i' "$path" >/dev/null 2>&1; then
    stat -f '%d:%i' "$path"
  else
    stat -c '%d:%i' "$path"
  fi
}

system_holds_liveness_file() {
  local path="$1"
  local identity="$2"
  local process_directory descriptor process_owner output
  local current_pid="$$"
  [[ -f "$path" && ! -L "$path" &&
    "$(portable_file_identity "$path")" == "$identity" ]] ||
    return 2
  if [[ -d /proc ]]; then
    [[ -r /proc && -x /proc ]] || return 2
    for process_directory in /proc/[1-9]*; do
      [[ -d "$process_directory" ]] || continue
      if ! process_owner="$(portable_owner "$process_directory" 2>/dev/null)"; then
        [[ ! -e "$process_directory" ]] && continue
        return 2
      fi
      [[ "$process_owner" == "$(id -u)" ]] || continue
      if [[ ! -d "$process_directory/fd" ]]; then
        [[ ! -e "$process_directory" ]] && continue
        return 2
      fi
      [[ -r "$process_directory/fd" && -x "$process_directory/fd" ]] ||
        return 2
      for descriptor in "$process_directory/fd/"*; do
        [[ -e "$descriptor" ]] || continue
        if [[ "$descriptor" -ef "$path" ]]; then
          return 0
        fi
      done
    done
    return 1
  fi
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
  if [[ -n "$LIVE_ROOT" ]]; then
    arguments+=(--file "$LIVE_COMPOSE_FILE")
  fi
  docker compose "${arguments[@]}" "$@"
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
    sleep "$POLL_INTERVAL_SECONDS"
  done
  return 1
}

start_operational_lock_session() {
  mkfifo "$LEASE_FIFO"
  : >"$LEASE_OUTPUT"
  chmod 0600 "$LEASE_OUTPUT"
  set -m
  (
    database_session <"$LEASE_FIFO" >"$LEASE_OUTPUT" 2>/dev/null
  ) &
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
  LEASE_HEARTBEAT_FAILED="$LOCK_OWNER_DIRECTORY/heartbeat.failed"
  [[ ! -e "$LEASE_HEARTBEAT_FAILED" ]] || return 1
  set -m
  (
    set +m
    exec 9>&-
    ALLOW_LOST_LEASE_ACTIONS=true
    SEPARATE_EXTERNAL_GROUP=false
    while sleep "$HEARTBEAT_INTERVAL_SECONDS"; do
      if ! renew_operational_lease; then
        printf '%s\n' 'lease_lost' >"$LEASE_HEARTBEAT_FAILED"
        exit 1
      fi
    done
  ) &
  LEASE_HEARTBEAT_PID="$!"
  set +m
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
  return 0
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
  local deadline=$((SECONDS + timeout_seconds))
  local pid
  if [[ "$SEPARATE_EXTERNAL_GROUP" == true ]]; then
    set -m
    ( "$@" ) &
    pid="$!"
    set +m
  else
    ( "$@" ) &
    pid="$!"
  fi
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
    terminate_external_group "$LEASE_HEARTBEAT_PID"
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
  run_guarded_external "$((EXTERNAL_TIMEOUT_SECONDS + 15))" \
    bounded_backup_compose "$@"
}

bounded_backup_compose() {
  compose run --rm --no-deps --entrypoint /usr/bin/timeout backup \
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
  export HAPPYLEARN_BACKUP_CONTAINER_HOSTNAME="$SYNC_CONTAINER_HOSTNAME"
  compose run \
    --name "$SYNC_CONTAINER_NAME" \
    --label "${SYNC_RUN_LABEL}=${RUN_ID}" \
    --label "${SYNC_OWNER_LABEL}=${SYNC_CONTAINER_OWNER}" \
    --rm --no-deps --entrypoint /usr/bin/timeout backup \
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
  prepare_sync_container_identity || return 1
  SYNC_CONTAINER_ID=''
  if run_guarded_external "$((EXTERNAL_TIMEOUT_SECONDS + 15))" \
    bounded_backup_sync_compose; then
    command_status=0
  else
    command_status=$?
  fi
  if cleanup_sync_container; then
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
    compose run --rm --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" restic \
    --no-cache \
    --repository-file /run/secrets/local_repository \
    --password-file /run/secrets/local_password \
    "$@"
}

remote_restic() {
  run_guarded_external "$((EXTERNAL_TIMEOUT_SECONDS + 15))" \
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

bounded_local_unlock() {
  local hostname="$1"
  export HAPPYLEARN_BACKUP_CONTAINER_HOSTNAME="$hostname"
  compose run --rm --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${SYNC_CONTAINER_STOP_TIMEOUT_SECONDS}s" \
    restic --no-cache \
    --repository-file /run/secrets/local_repository \
    --password-file /run/secrets/local_password \
    unlock
}

bounded_remote_unlock() {
  local hostname="$1"
  export HAPPYLEARN_BACKUP_CONTAINER_HOSTNAME="$hostname"
  compose run --rm --no-deps --entrypoint /bin/sh backup \
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
    compose run --rm --no-deps --entrypoint /usr/bin/timeout backup \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /app/happylearn-backup-retention \
    --repository "$repository" --run-id "$RUN_ID"
}

complete_remote_degraded() {
  if [[ "$FAILURE_CATEGORY" == 'lease_lost' ]]; then
    return 1
  fi
  restart_stopped_services || return 1
  FAILURE_CATEGORY='remote_unavailable'
  backup_command fail --run-id "$RUN_ID" --category "$FAILURE_CATEGORY"
  TERMINAL_RECORDED=true
  release_operational_lease
}

record_failure() {
  [[ -n "$RUN_ID" && "$TERMINAL_RECORDED" == false ]] || return 0
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
  [[ "$LEASE_FD_OPEN" == true ]] || return 0
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
  fi
  LEASE_PID=''
  return 0
}

release_operational_lease() {
  local released
  if [[ "$LEASE_DURABLE" == true ]]; then
    if [[ "$RECOVERY_UNSAFE" == true ]]; then
      if ! transition_operational_mode release; then
        stop_lease_heartbeat
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
        stop_lease_heartbeat
        return 1
      fi
    fi
    LEASE_DURABLE=false
  fi
  stop_lease_heartbeat
  stop_operational_lock_session
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
  if [[ "$CLEANING_UP" == true ]]; then
    exit "$status"
  fi
  CLEANING_UP=true
  trap - EXIT HUP INT TERM
  ALLOW_LOST_LEASE_ACTIONS=true
  if restart_stopped_services; then
    FAILURE_CATEGORY="$failed_stage"
  else
    safe_log 'cleanup_restart_failed'
    status=1
  fi
  if [[ "$status" -ne 0 ]] && ! record_failure; then
    safe_log 'cleanup_record_failure_failed'
    status=1
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
  trap cleanup EXIT HUP INT TERM
  acquire_host_lock
  CURRENT_STAGE='mount_init'
  initialize_backup_mounts
  CURRENT_STAGE='mount_verify'
  verify_backup_mount_ownership

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
  CURRENT_STAGE='lease_values'
  prepare_lease_values
  CURRENT_STAGE='lock_session'
  start_operational_lock_session
  CURRENT_STAGE='durable_lease'
  acquire_durable_lease
  CURRENT_STAGE='heartbeat'
  start_lease_heartbeat

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
  FAILURE_CATEGORY='drain_timeout'
  CURRENT_STAGE='drain'
  wait_for_durable_drain
  transition_operational_mode backup
  assert_lease_heartbeat
  CURRENT_STAGE='stop_services'
  stop_snapshot_services
  assert_lease_heartbeat

  FAILURE_CATEGORY='snapshot'
  CURRENT_STAGE='snapshot'
  backup_command snapshot --run-id "$RUN_ID"
  assert_lease_heartbeat
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

  FAILURE_CATEGORY='object_store_restart'
  CURRENT_STAGE='service_restart'
  restart_stopped_services
  assert_lease_heartbeat
  FAILURE_CATEGORY='internal'
  CURRENT_STAGE='finish'
  backup_command finish --run-id "$RUN_ID"
  TERMINAL_RECORDED=true
  if ! release_operational_lease; then
    safe_log 'main_release_failed'
    return 1
  fi
}

main "$@"
