#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
umask 077

readonly USAGE='Usage: scripts/phase5-restore-verify.sh --backup-id <canonical-uuid>'
readonly PROJECT_PREFIX='happylearn-phase5-restore-'
readonly OWNER_LABEL='io.happylearn.phase5.restore-owner'
readonly KIND_LABEL='io.happylearn.phase5.restore-kind'
readonly BACKUP_LABEL='io.happylearn.phase5.restore-backup-id'
readonly RTO_LIMIT_SECONDS=14400
readonly AISTOR_IMAGE='quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120'
readonly CONTAINER_CPUS='0.25'
readonly CONTAINER_MEMORY='512m'
readonly CONTAINER_MEMORY_SWAP='512m'
readonly CONTAINER_PIDS_LIMIT='256'
readonly SUPERVISOR_WAIT_INTERVAL_SECONDS='0.01'
readonly SUPERVISOR_TERM_GRACE_ATTEMPTS=10
readonly SUPERVISOR_PARENT_GRACE_ATTEMPTS=200

BACKUP_ID=''
PROJECT=''
OWNER_TOKEN=''
WORK_DIRECTORY=''
CONTROL_DIRECTORY=''
RESTORE_DIRECTORY=''
CHECK_OUTPUT_DIRECTORY=''
RESTORED_MANIFEST=''
EXPECTED_MANIFEST_SHA256=''
REPORT_FILE=''
REPORT_TEMPORARY=''
RESTORE_LOCK_FILE=''
RESTORE_LOCK_FD_OPEN=false
REPORT_LOCK_FILE=''
REPORT_LOCK_FD_OPEN=false
NETWORK_NAME=''
POSTGRES_VOLUME=''
AISTOR_VOLUME=''
AISTOR_LICENSE_VOLUME=''
POSTGRES_CONTAINER=''
AISTOR_CONTAINER=''
REDIS_CONTAINER=''
APP_CONTAINER=''
DATABASE_PASSWORD=''
MINIO_ACCESS_KEY=''
MINIO_SECRET_KEY=''
SIGNING_EPOCH=''
CLEANING_UP=false
START_SECONDS="$SECONDS"
CONTAINER_RECORD=''
CLEANUP_INTENT_LEDGER=''
NETWORK_CREATED=false
POSTGRES_VOLUME_CREATED=false
AISTOR_VOLUME_CREATED=false
AISTOR_LICENSE_VOLUME_CREATED=false
HOST_UID=''
HOST_GID=''
ACTIVE_EXTERNAL_PID=''
ACTIVE_EXTERNAL_PGID=''
ACTIVE_EXTERNAL_IDENTITY=''
PENDING_SIGNAL_STATUS=''
WORKSPACE_INITIALIZED=false
HTTP_PROBE_SUCCEEDED=false
BOUNDED_BATCH_ACTIVE=false
SUPERVISOR_STAT_STYLE=''

EXTERNAL_TIMEOUT_SECONDS="${HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS:-300}"
READY_TIMEOUT_SECONDS="${HAPPYLEARN_RESTORE_READY_TIMEOUT_SECONDS:-300}"
POLL_INTERVAL_SECONDS="${HAPPYLEARN_RESTORE_POLL_INTERVAL_SECONDS:-0.25}"
BACKUP_IMAGE="${HAPPYLEARN_BACKUP_IMAGE:-happylearn-backup:phase5}"
APP_IMAGE="${HAPPYLEARN_RESTORE_APP_IMAGE:-happylearn-app:phase5}"
POSTGRES_IMAGE="${HAPPYLEARN_RESTORE_POSTGRES_IMAGE:-postgres:18.4}"
REDIS_IMAGE="${HAPPYLEARN_RESTORE_REDIS_IMAGE:-redis:8.8}"

usage_error() {
  printf '%s\n' "$USAGE" >&2
  exit 2
}

safe_log() {
  printf 'phase5_restore: %s\n' "$1" >&2
}

valid_uint() {
  [[ "$1" =~ ^[1-9][0-9]*$ ]]
}

canonical_backup_uuid() {
  [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]]
}

valid_project() {
  [[ "$1" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ ]]
}

valid_owner_token() {
  [[ "$1" =~ ^[a-f0-9]{64}$ ]]
}

valid_image_reference() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._/@:-]{0,255}$ ]]
}

safe_mount_source() {
  [[ "$1" == /* &&
    "$1" != *','* &&
    "$1" != *$'\n'* &&
    "$1" != *$'\r'* ]]
}

validate_arguments() {
  [[ "$#" -eq 2 && "$1" == '--backup-id' ]] || usage_error
  canonical_backup_uuid "$2" || usage_error
  BACKUP_ID="$2"
  valid_uint "$EXTERNAL_TIMEOUT_SECONDS" || usage_error
  valid_uint "$READY_TIMEOUT_SECONDS" || usage_error
  [[ "$EXTERNAL_TIMEOUT_SECONDS" -lt "$RTO_LIMIT_SECONDS" &&
    "$READY_TIMEOUT_SECONDS" -lt "$RTO_LIMIT_SECONDS" ]] ||
    usage_error
  [[ "$POLL_INTERVAL_SECONDS" =~ ^(0\.[0-9]*[1-9][0-9]*|[1-9][0-9]*(\.[0-9]+)?)$ ]] ||
    usage_error
  valid_image_reference "$BACKUP_IMAGE" || usage_error
  valid_image_reference "$APP_IMAGE" || usage_error
  valid_image_reference "$POSTGRES_IMAGE" || usage_error
  valid_image_reference "$REDIS_IMAGE" || usage_error
}

portable_mode() {
  local path="$1"
  if stat --version 2>/dev/null | grep -Fq 'GNU coreutils'; then
    stat -c '%a' "$path"
  elif stat -f '%Lp' "$path" >/dev/null 2>&1; then
    stat -f '%Lp' "$path"
  else
    return 1
  fi
}

portable_owner() {
  local path="$1"
  if stat --version 2>/dev/null | grep -Fq 'GNU coreutils'; then
    stat -c '%u' "$path"
  elif stat -f '%u' "$path" >/dev/null 2>&1; then
    stat -f '%u' "$path"
  else
    return 1
  fi
}

portable_group() {
  local path="$1"
  if stat --version 2>/dev/null | grep -Fq 'GNU coreutils'; then
    stat -c '%g' "$path"
  elif stat -f '%g' "$path" >/dev/null 2>&1; then
    stat -f '%g' "$path"
  else
    return 1
  fi
}

portable_size() {
  local path="$1"
  if stat --version 2>/dev/null | grep -Fq 'GNU coreutils'; then
    stat -c '%s' "$path"
  elif stat -f '%z' "$path" >/dev/null 2>&1; then
    stat -f '%z' "$path"
  else
    return 1
  fi
}

owner_only_directory() {
  local path="$1"
  [[ -d "$path" && ! -L "$path" &&
    "$(portable_mode "$path")" == '700' &&
    "$(portable_owner "$path")" == "$(id -u)" ]]
}

owner_only_secret() {
  local path="$1"
  local size
  [[ -f "$path" && ! -L "$path" &&
    "$(portable_mode "$path")" == '400' &&
    "$(portable_owner "$path")" == "$(id -u)" ]] ||
    return 1
  size="$(portable_size "$path")" || return 1
  [[ "$size" -ge 1 && "$size" -le 4096 ]]
}

canonical_directory() {
  local path="$1"
  [[ "$path" == /* && -d "$path" && ! -L "$path" ]] || return 1
  (
    cd "$path"
    pwd -P
  )
}

validate_paths() {
  local repository_directory="${HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY:-}"
  local secret_directory="${HAPPYLEARN_BACKUP_SECRET_DIRECTORY:-}"
  local control_directory="${HAPPYLEARN_RESTORE_CONTROL_DIRECTORY:-}"
  local report_directory="${HAPPYLEARN_RESTORE_REPORT_DIRECTORY:-}"
  local license_file="${HAPPYLEARN_AISTOR_LICENSE_FILE:-}"
  local teacher_credential_file="${HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE:-}"
  [[ -n "$repository_directory" &&
    -n "$secret_directory" &&
    -n "$control_directory" &&
    -n "$report_directory" &&
    -n "$license_file" &&
    -n "$teacher_credential_file" ]] ||
    return 1
  HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$(
    canonical_directory "$repository_directory"
  )" || return 1
  HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$(
    canonical_directory "$secret_directory"
  )" || return 1
  HAPPYLEARN_RESTORE_CONTROL_DIRECTORY="$(
    canonical_directory "$control_directory"
  )" || return 1
  HAPPYLEARN_RESTORE_REPORT_DIRECTORY="$(
    canonical_directory "$report_directory"
  )" || return 1
  [[ "$license_file" == /* && -f "$license_file" && ! -L "$license_file" ]] ||
    return 1
  HAPPYLEARN_AISTOR_LICENSE_FILE="$license_file"
  [[ "$teacher_credential_file" == /* &&
    -f "$teacher_credential_file" &&
    ! -L "$teacher_credential_file" ]] ||
    return 1
  HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE="$teacher_credential_file"
  safe_mount_source "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" || return 1
  safe_mount_source "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" || return 1
  safe_mount_source "$HAPPYLEARN_RESTORE_CONTROL_DIRECTORY" || return 1
  safe_mount_source "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY" || return 1
  safe_mount_source "$HAPPYLEARN_AISTOR_LICENSE_FILE" || return 1
  safe_mount_source "$HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE" || return 1
  owner_only_directory "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" || return 1
  owner_only_directory "$HAPPYLEARN_RESTORE_CONTROL_DIRECTORY" || return 1
  owner_only_directory "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY" || return 1
  [[ "$HAPPYLEARN_RESTORE_CONTROL_DIRECTORY" != \
    "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY" ]] ||
    return 1
  owner_only_secret "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_password" ||
    return 1
  owner_only_secret "$HAPPYLEARN_AISTOR_LICENSE_FILE" || return 1
  owner_only_secret "$HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE" ||
    return 1
  REPORT_FILE="$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/restore-${BACKUP_ID}.json"
  REPORT_TEMPORARY="$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/.restore-${BACKUP_ID}.new"
  RESTORE_LOCK_FILE="$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY/.phase5-restore-${BACKUP_ID}.lock"
  REPORT_LOCK_FILE="$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/.restore-${BACKUP_ID}.lock"
}

acquire_restore_lock() {
  [[ "$RESTORE_LOCK_FILE" == \
    "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY/.phase5-restore-${BACKUP_ID}.lock" &&
    ! -L "$RESTORE_LOCK_FILE" ]] ||
    return 1
  if [[ ! -e "$RESTORE_LOCK_FILE" ]]; then
    (umask 077 && : >"$RESTORE_LOCK_FILE") || return 1
    chmod 0600 "$RESTORE_LOCK_FILE" || return 1
  fi
  [[ -f "$RESTORE_LOCK_FILE" && ! -L "$RESTORE_LOCK_FILE" &&
    "$(portable_mode "$RESTORE_LOCK_FILE")" == 600 &&
    "$(portable_owner "$RESTORE_LOCK_FILE")" == "$(id -u)" ]] ||
    return 1
  command -v flock >/dev/null 2>&1 || return 1
  exec 10<>"$RESTORE_LOCK_FILE" || return 1
  if ! flock --exclusive --nonblock 10; then
    exec 10>&-
    return 1
  fi
  RESTORE_LOCK_FD_OPEN=true
}

acquire_report_lock() {
  [[ "$REPORT_LOCK_FILE" == \
    "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/.restore-${BACKUP_ID}.lock" &&
    ! -L "$REPORT_LOCK_FILE" ]] ||
    return 1
  if [[ ! -e "$REPORT_LOCK_FILE" ]]; then
    (umask 077 && : >"$REPORT_LOCK_FILE") || return 1
    chmod 0600 "$REPORT_LOCK_FILE" || return 1
  fi
  [[ -f "$REPORT_LOCK_FILE" && ! -L "$REPORT_LOCK_FILE" &&
    "$(portable_mode "$REPORT_LOCK_FILE")" == 600 &&
    "$(portable_owner "$REPORT_LOCK_FILE")" == "$(id -u)" ]] ||
    return 1
  exec 11<>"$REPORT_LOCK_FILE" || return 1
  if ! flock --exclusive --nonblock 11; then
    exec 11>&-
    return 1
  fi
  REPORT_LOCK_FD_OPEN=true
  [[ ! -e "$REPORT_FILE" && ! -L "$REPORT_FILE" &&
    ! -e "$REPORT_TEMPORARY" && ! -L "$REPORT_TEMPORARY" ]] ||
    return 1
}

random_token() {
  local token
  token="$(od -An -N32 -tx1 /dev/urandom | tr -d '[:space:]')" ||
    return 1
  valid_owner_token "$token" || return 1
  printf '%s' "$token"
}

initialize_identity() {
  HOST_UID="$(id -u)" || return 1
  HOST_GID="$(id -g)" || return 1
  [[ "$HOST_UID" =~ ^[0-9]+$ && "$HOST_GID" =~ ^[0-9]+$ ]] || return 1
  OWNER_TOKEN="$(random_token)" || return 1
  PROJECT="${PROJECT_PREFIX}${OWNER_TOKEN:0:12}"
  valid_project "$PROJECT" || return 1
  DATABASE_PASSWORD="$(random_token)" || return 1
  MINIO_SECRET_KEY="$(random_token)" || return 1
  SIGNING_EPOCH="$(random_token)" || return 1
  MINIO_ACCESS_KEY="restore${MINIO_SECRET_KEY:0:16}"
  NETWORK_NAME="$PROJECT-network"
  POSTGRES_VOLUME="$PROJECT-postgres"
  AISTOR_VOLUME="$PROJECT-aistor"
  AISTOR_LICENSE_VOLUME="$PROJECT-aistor-license"
  POSTGRES_CONTAINER="$PROJECT-postgres"
  AISTOR_CONTAINER="$PROJECT-aistor"
  REDIS_CONTAINER="$PROJECT-redis"
  APP_CONTAINER="$PROJECT-app"
}

initialize_workspace() {
  WORK_DIRECTORY="$(
    mktemp -d "${TMPDIR:-/tmp}/phase5-restore-verify.XXXXXX"
  )" || return 1
  chmod 0700 "$WORK_DIRECTORY" || return 1
  CONTROL_DIRECTORY="$WORK_DIRECTORY/control"
  mkdir "$CONTROL_DIRECTORY" || return 1
  chmod 0700 "$CONTROL_DIRECTORY" || return 1
  RESTORE_DIRECTORY="$WORK_DIRECTORY/restored"
  mkdir "$RESTORE_DIRECTORY" || return 1
  chmod 0700 "$RESTORE_DIRECTORY" || return 1
  CHECK_OUTPUT_DIRECTORY="$WORK_DIRECTORY/check-output"
  mkdir "$CHECK_OUTPUT_DIRECTORY" || return 1
  chmod 0700 "$CHECK_OUTPUT_DIRECTORY" || return 1
  CONTAINER_RECORD="$CONTROL_DIRECTORY/containers"
  : >"$CONTAINER_RECORD" || return 1
  chmod 0600 "$CONTAINER_RECORD" || return 1
  CLEANUP_INTENT_LEDGER="$CONTROL_DIRECTORY/cleanup.intent"
  : >"$CLEANUP_INTENT_LEDGER" || return 1
  chmod 0600 "$CLEANUP_INTENT_LEDGER" || return 1
  printf 'postgres:5432:happylearn:happylearn:%s\n' \
    "$DATABASE_PASSWORD" >"$CONTROL_DIRECTORY/pgpass" ||
    return 1
  chmod 0400 "$CONTROL_DIRECTORY/pgpass" || return 1
  printf '%s\n' \
    'POSTGRES_USER=happylearn' \
    'POSTGRES_DB=happylearn' \
    "POSTGRES_PASSWORD=$DATABASE_PASSWORD" \
    >"$CONTROL_DIRECTORY/postgres.env" ||
    return 1
  printf '%s\n' \
    "MINIO_ROOT_USER=$MINIO_ACCESS_KEY" \
    "MINIO_ROOT_PASSWORD=$MINIO_SECRET_KEY" \
    >"$CONTROL_DIRECTORY/aistor.env" ||
    return 1
  printf '%s\n' \
    'HAPPYLEARN_ENV=development' \
    'HAPPYLEARN_LISTEN=:8080' \
    "HAPPYLEARN_DATABASE_URL=postgres://happylearn:$DATABASE_PASSWORD@postgres:5432/happylearn?sslmode=disable" \
    'HAPPYLEARN_REDIS_URL=redis://redis:6379/0' \
    "HAPPYLEARN_LOGIN_THROTTLE_SECRET=$SIGNING_EPOCH" \
    'HAPPYLEARN_PUBLIC_ORIGIN=http://app:8080' \
    'HAPPYLEARN_MINIO_ENDPOINT=minio:9000' \
    "HAPPYLEARN_MINIO_ACCESS_KEY=$MINIO_ACCESS_KEY" \
    "HAPPYLEARN_MINIO_SECRET_KEY=$MINIO_SECRET_KEY" \
    'HAPPYLEARN_MINIO_ORIGINALS_BUCKET=happylearn-originals' \
    'HAPPYLEARN_MINIO_PREVIEWS_BUCKET=happylearn-previews' \
    >"$CONTROL_DIRECTORY/app.env" ||
    return 1
  printf '%s\n' \
    'HAPPYLEARN_DATABASE_HOST=postgres' \
    'HAPPYLEARN_DATABASE_PORT=5432' \
    'HAPPYLEARN_DATABASE_USER=happylearn' \
    'HAPPYLEARN_DATABASE_NAME=happylearn' \
    'HAPPYLEARN_DATABASE_SSLMODE=disable' \
    'PGPASSFILE=/run/secrets/pgpass' \
    'HAPPYLEARN_MINIO_ENDPOINT=minio:9000' \
    'HAPPYLEARN_MINIO_USE_TLS=false' \
    "HAPPYLEARN_MINIO_ACCESS_KEY=$MINIO_ACCESS_KEY" \
    "HAPPYLEARN_MINIO_SECRET_KEY=$MINIO_SECRET_KEY" \
    >"$CONTROL_DIRECTORY/restore-check.env" ||
    return 1
  chmod 0400 \
    "$CONTROL_DIRECTORY/postgres.env" \
    "$CONTROL_DIRECTORY/aistor.env" \
    "$CONTROL_DIRECTORY/app.env" \
    "$CONTROL_DIRECTORY/restore-check.env" ||
    return 1
  owner_only_secret "$CONTROL_DIRECTORY/pgpass" &&
    owner_only_secret "$CONTROL_DIRECTORY/postgres.env" &&
    owner_only_secret "$CONTROL_DIRECTORY/aistor.env" &&
    owner_only_secret "$CONTROL_DIRECTORY/app.env" &&
  owner_only_secret "$CONTROL_DIRECTORY/restore-check.env" &&
    owner_only_directory "$CHECK_OUTPUT_DIRECTORY" ||
    return 1
  initialize_supervisor_stat_style || return 1
  WORKSPACE_INITIALIZED=true
}

direct_running_job() {
  local wanted_pid="$1"
  local job_pid
  [[ "$wanted_pid" =~ ^[1-9][0-9]*$ ]] || return 1
  while IFS= read -r job_pid; do
    [[ "$job_pid" == "$wanted_pid" ]] && return 0
  done < <(jobs -pr)
  return 1
}

portable_supervisor_metadata() {
  local path="$1"
  case "$SUPERVISOR_STAT_STYLE" in
    gnu) stat -c '%a|%u|%s|%h|%i' "$path" ;;
    bsd) stat -f '%Lp|%u|%z|%l|%i' "$path" ;;
    *) return 1 ;;
  esac
}

initialize_supervisor_stat_style() {
  if stat --version 2>/dev/null | grep -Fq 'GNU coreutils'; then
    SUPERVISOR_STAT_STYLE=gnu
  elif stat -f '%Lp|%u|%z|%l|%i' \
      "$CONTROL_DIRECTORY" >/dev/null 2>&1; then
    SUPERVISOR_STAT_STYLE=bsd
  else
    return 1
  fi
}

owner_only_supervisor_directory() {
  local path="$1"
  local metadata mode owner size links inode extra
  metadata="$(portable_supervisor_metadata "$path")" || return 1
  IFS='|' read -r mode owner size links inode extra <<<"$metadata"
  [[ "$path" == "$CONTROL_DIRECTORY/bounded-supervisor."* &&
    -d "$path" &&
    ! -L "$path" &&
    "$mode" == 700 &&
    "$owner" == "$HOST_UID" &&
    "$size" =~ ^[0-9]+$ &&
    "$links" =~ ^[1-9][0-9]*$ &&
    "$inode" =~ ^[1-9][0-9]*$ &&
    -z "$extra" ]]
}

owner_only_supervisor_fifo() {
  local path="$1"
  local metadata mode owner size links inode extra
  metadata="$(portable_supervisor_metadata "$path")" || return 1
  IFS='|' read -r mode owner size links inode extra <<<"$metadata"
  [[ -p "$path" &&
    ! -L "$path" &&
    "$mode" == 600 &&
    "$owner" == "$HOST_UID" &&
    "$size" =~ ^[0-9]+$ &&
    "$links" == 1 &&
    "$inode" =~ ^[1-9][0-9]*$ &&
    -z "$extra" ]]
}

read_supervisor_handshake() {
  local path="$1"
  local pattern="$2"
  local metadata metadata_after mode owner size links inode extra value
  metadata="$(portable_supervisor_metadata "$path")" || return 1
  IFS='|' read -r mode owner size links inode extra <<<"$metadata"
  [[ -f "$path" &&
    ! -L "$path" &&
    "$mode" == 600 &&
    "$owner" == "$HOST_UID" &&
    "$size" =~ ^[0-9]+$ &&
    "$links" == 1 &&
    "$inode" =~ ^[1-9][0-9]*$ &&
    -z "$extra" ]] ||
    return 1
  [[ "$size" -ge 2 && "$size" -le 8 ]] || return 1
  value="$(<"$path")"
  metadata_after="$(portable_supervisor_metadata "$path")" || return 1
  [[ "$size" -eq $((${#value} + 1)) &&
    "$value" =~ $pattern &&
    "$metadata_after" == "$metadata" ]] ||
    return 1
  printf '%s' "$value"
}

publish_supervisor_handshake() {
  local path="$1"
  local value="$2"
  local pending="${path}.pending"
  local metadata final_metadata mode owner size links inode extra
  local final_mode final_owner final_size final_links final_inode final_extra
  [[ "$path" == "$CONTROL_DIRECTORY/bounded-supervisor."*/* &&
    "$value" =~ ^(ready|ack|[0-9]{1,7})$ &&
    ! -e "$path" &&
    ! -L "$path" &&
    ! -e "$pending" &&
    ! -L "$pending" ]] ||
    return 1
  (
    set -o noclobber
    printf '%s\n' "$value" >"$pending"
  ) || return 1
  metadata="$(portable_supervisor_metadata "$pending")" || return 1
  IFS='|' read -r mode owner size links inode extra <<<"$metadata"
  [[ -f "$pending" &&
    ! -L "$pending" &&
    "$mode" == 600 &&
    "$owner" == "$HOST_UID" &&
    "$size" -eq $((${#value} + 1)) &&
    "$links" == 1 &&
    "$inode" =~ ^[1-9][0-9]*$ &&
    -z "$extra" &&
    "$(<"$pending")" == "$value" ]] ||
    return 1
  ln "$pending" "$path" || {
    rm -f "$pending" || true
    return 1
  }
  metadata="$(portable_supervisor_metadata "$pending")" || {
    rm -f "$pending" "$path" || true
    return 1
  }
  final_metadata="$(portable_supervisor_metadata "$path")" || {
    rm -f "$pending" "$path" || true
    return 1
  }
  IFS='|' read -r mode owner size links inode extra <<<"$metadata"
  IFS='|' read -r final_mode final_owner final_size \
    final_links final_inode final_extra <<<"$final_metadata"
  [[ -f "$pending" &&
    ! -L "$pending" &&
    -f "$path" &&
    ! -L "$path" &&
    "$pending" -ef "$path" &&
    "$mode" == 600 &&
    "$owner" == "$HOST_UID" &&
    "$size" -eq $((${#value} + 1)) &&
    "$links" == 2 &&
    "$inode" =~ ^[1-9][0-9]*$ &&
    -z "$extra" &&
    "$final_mode" == "$mode" &&
    "$final_owner" == "$owner" &&
    "$final_size" == "$size" &&
    "$final_links" == "$links" &&
    "$final_inode" == "$inode" &&
    -z "$final_extra" &&
    "$(<"$pending")" == "$value" &&
    "$(<"$path")" == "$value" ]] || {
    rm -f "$pending" "$path" || true
    return 1
  }
  rm -f "$pending" || {
    rm -f "$path" || true
    return 1
  }
  [[ ! -e "$pending" &&
    ! -L "$pending" ]] || {
    rm -f "$path" || true
    return 1
  }
  final_metadata="$(portable_supervisor_metadata "$path")" || {
    rm -f "$path" || true
    return 1
  }
  IFS='|' read -r final_mode final_owner final_size \
    final_links final_inode final_extra <<<"$final_metadata"
  [[ "$final_mode" == 600 &&
    "$final_owner" == "$HOST_UID" &&
    "$final_size" -eq $((${#value} + 1)) &&
    "$final_links" == 1 &&
    "$final_inode" == "$inode" &&
    -z "$final_extra" &&
    "$(read_supervisor_handshake "$path" "^${value}$")" == "$value" ]] || {
    rm -f "$path" || true
    return 1
  }
}

supervisor_descriptors_closed() {
  ! { : <&7; } 2>/dev/null &&
    ! { : <&8; } 2>/dev/null &&
    ! { : <&9; } 2>/dev/null
}

supervisor_handshake_available() {
  local path="$1"
  local pending="${path}.pending"
  [[ (-e "$path" || -L "$path") &&
    ! -e "$pending" &&
    ! -L "$pending" ]]
}

discard_supervisor_handshake() {
  local directory="$1"
  owner_only_supervisor_directory "$directory" || return 1
  rm -rf "$directory"
}

supervisor_identity_value_matches() {
  local pid="$1"
  local pgid="$2"
  local identity_path="${3:-$ACTIVE_EXTERNAL_IDENTITY}"
  local observed
  [[ "$pid" =~ ^[1-9][0-9]*$ &&
    "$pgid" == "$pid" &&
    -n "$identity_path" ]] ||
    return 1
  observed="$(
    read_supervisor_handshake "$identity_path" '^[1-9][0-9]{0,6}$'
  )" || return 1
  [[ "$observed" == "$pid" ]]
}

supervisor_identity_matches() {
  local pid="$1"
  local pgid="$2"
  local identity_path="${3:-$ACTIVE_EXTERNAL_IDENTITY}"
  direct_running_job "$pid" &&
    supervisor_identity_value_matches "$pid" "$pgid" "$identity_path"
}

terminate_external_pid() {
  local pid="$1"
  local attempts=0
  direct_running_job "$pid" || {
    wait "$pid" 2>/dev/null || true
    return 0
  }
  kill -TERM "$pid" 2>/dev/null || true
  while direct_running_job "$pid" &&
    [[ "$attempts" -lt "$SUPERVISOR_PARENT_GRACE_ATTEMPTS" ]]; do
    sleep "$SUPERVISOR_WAIT_INTERVAL_SECONDS"
    attempts=$((attempts + 1))
  done
  if direct_running_job "$pid"; then
    kill -KILL "$pid" 2>/dev/null || true
  fi
  wait "$pid" 2>/dev/null || true
}

terminate_supervisor_command_group() {
  local pid="$1"
  local attempts=0
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  direct_running_job "$pid" || {
    wait "$pid" 2>/dev/null || true
    return 0
  }
  kill -TERM -- "-$pid" 2>/dev/null ||
    kill -TERM "$pid" 2>/dev/null ||
    true
  while direct_running_job "$pid" &&
    [[ "$attempts" -lt "$SUPERVISOR_TERM_GRACE_ATTEMPTS" ]]; do
    sleep "$SUPERVISOR_WAIT_INTERVAL_SECONDS"
    attempts=$((attempts + 1))
  done
  if direct_running_job "$pid"; then
    kill -KILL -- "-$pid" 2>/dev/null ||
      kill -KILL "$pid" 2>/dev/null ||
      true
  fi
  wait "$pid" 2>/dev/null || true
  ! direct_running_job "$pid"
}

terminate_external_group() {
  local pid="$1"
  local pgid="$2"
  local identity_path="${3:-$ACTIVE_EXTERNAL_IDENTITY}"
  local identity_valid=true
  [[ "$pid" =~ ^[1-9][0-9]*$ &&
    "$pgid" =~ ^[1-9][0-9]*$ ]] ||
    return 1
  direct_running_job "$pid" || {
    wait "$pid" 2>/dev/null || true
    return 0
  }
  supervisor_identity_matches "$pid" "$pgid" "$identity_path" ||
    identity_valid=false
  terminate_external_pid "$pid"
  [[ "$identity_valid" == true ]]
}

terminate_active_external() {
  local pid="$ACTIVE_EXTERNAL_PID"
  local pgid="$ACTIVE_EXTERNAL_PGID"
  if [[ "$pid" =~ ^[1-9][0-9]*$ &&
    "$pgid" =~ ^[1-9][0-9]*$ ]]; then
    terminate_external_group \
      "$pid" "$pgid" "$ACTIVE_EXTERNAL_IDENTITY" || true
  fi
  ACTIVE_EXTERNAL_PID=''
  ACTIVE_EXTERNAL_PGID=''
  ACTIVE_EXTERNAL_IDENTITY=''
}

handle_restore_signal() {
  local status="$1"
  [[ "$status" =~ ^(129|130|143)$ ]] || status=1
  trap '' HUP INT TERM
  exec 9>&- || true
  terminate_active_external
  exit "$status"
}

install_restore_signal_traps() {
  if [[ "$CLEANING_UP" == true ]]; then
    trap '' HUP INT TERM
  else
    trap 'handle_restore_signal 129' HUP
    trap 'handle_restore_signal 130' INT
    trap 'handle_restore_signal 143' TERM
  fi
}

honor_pending_restore_signal() {
  if [[ -n "$PENDING_SIGNAL_STATUS" ]]; then
    handle_restore_signal "$PENDING_SIGNAL_STATUS"
  fi
}

run_bounded() {
  local timeout_seconds="$1"
  shift
  valid_uint "$timeout_seconds" || return 1
  local startup_deadline=$((SECONDS + 5))
  local deadline=0
  local supervisor_directory supervisor_guard supervisor_ready
  local supervisor_identity supervisor_status supervisor_ack
  local pid result=0 supervisor_result=0 attempts=0
  supervisor_descriptors_closed || return 1
  supervisor_directory="$(
    mktemp -d "$CONTROL_DIRECTORY/bounded-supervisor.XXXXXX"
  )" || return 1
  owner_only_supervisor_directory "$supervisor_directory" || {
    rm -rf "$supervisor_directory"
    return 1
  }
  supervisor_guard="$supervisor_directory/descendant.guard"
  supervisor_ready="$supervisor_directory/ready"
  supervisor_identity="$supervisor_directory/identity"
  supervisor_status="$supervisor_directory/status"
  supervisor_ack="$supervisor_directory/ack"
  mkfifo "$supervisor_guard" || {
    discard_supervisor_handshake "$supervisor_directory" || true
    return 1
  }
  owner_only_supervisor_fifo "$supervisor_guard" || {
    discard_supervisor_handshake "$supervisor_directory" || true
    return 1
  }
  exec 9<>"$supervisor_guard" || {
    discard_supervisor_handshake "$supervisor_directory" || true
    return 1
  }
  PENDING_SIGNAL_STATUS=''
  if [[ "$CLEANING_UP" == true ]]; then
    trap '' HUP INT TERM
  else
    trap 'PENDING_SIGNAL_STATUS=129' HUP
    trap 'PENDING_SIGNAL_STATUS=130' INT
    trap 'PENDING_SIGNAL_STATUS=143' TERM
  fi
  set -m
  (
    set +e
    local supervisor_signal_seen=false
    local supervisor_termination_requested=false
    local command_pid command_status wait_status guard_value
    local wait_attempts=0
    trap \
      'supervisor_signal_seen=true; supervisor_termination_requested=true' \
      HUP INT TERM
    exec 9>&-
    exec 7<"$supervisor_guard" || exit 125
    set -m
    (
      set +m
      local command_signal_seen=false
      local actual_pid actual_status=0
      local actual_wait_status=0
      exec 8>"$supervisor_guard" || exit 125
      trap 'command_signal_seen=true' HUP INT TERM
      publish_supervisor_handshake "$supervisor_ready" ready || exit 125
      (
        trap - HUP INT TERM
        "$@"
      ) &
      actual_pid="$!"
      while :; do
        command_signal_seen=false
        wait "$actual_pid"
        actual_status=$?
        [[ "$command_signal_seen" == false ]] && break
      done
      exec 8>&-
      while :; do
        command_signal_seen=false
        guard_value=''
        IFS= read -r -u 7 guard_value
        actual_wait_status=$?
        if [[ "$command_signal_seen" == true ]]; then
          continue
        fi
        [[ "$actual_wait_status" -eq 1 && -z "$guard_value" ]] ||
          exit 125
        break
      done
      exec 7<&-
      [[ "$actual_status" -ge 0 && "$actual_status" -le 255 ]] ||
        exit 125
      exit "$actual_status"
    ) &
    command_pid="$!"
    set +m
    while :; do
      supervisor_signal_seen=false
      wait "$command_pid"
      command_status=$?
      if [[ "$supervisor_termination_requested" == true ]]; then
        trap '' HUP INT TERM
        terminate_supervisor_command_group "$command_pid" || true
        exec 7<&-
        exit 125
      fi
      [[ "$supervisor_signal_seen" == false ]] && break
    done
    while :; do
      supervisor_signal_seen=false
      guard_value=''
      IFS= read -r -u 7 guard_value
      wait_status=$?
      if [[ "$supervisor_signal_seen" == true ]]; then
        if [[ "$supervisor_termination_requested" == true ]]; then
          trap '' HUP INT TERM
          terminate_supervisor_command_group "$command_pid" || true
          exec 7<&-
          exit 125
        fi
        continue
      fi
      [[ "$wait_status" -eq 1 && -z "$guard_value" ]] || exit 125
      break
    done
    exec 7<&-
    [[ "$command_status" -ge 0 && "$command_status" -le 255 ]] || exit 125
    publish_supervisor_handshake \
      "$supervisor_status" "$command_status" ||
      exit 125
    while [[ "$wait_attempts" -lt 1000 ]]; do
      if [[ "$supervisor_termination_requested" == true ]]; then
        trap '' HUP INT TERM
        exit 125
      fi
      if supervisor_handshake_available "$supervisor_ack"; then
        [[ "$(read_supervisor_handshake "$supervisor_ack" '^ack$')" == ack ]] ||
          exit 125
        exit 0
      fi
      sleep 0.01
      wait_attempts=$((wait_attempts + 1))
    done
    exit 125
  ) &
  pid="$!"
  ACTIVE_EXTERNAL_PID="$pid"
  ACTIVE_EXTERNAL_PGID="$pid"
  ACTIVE_EXTERNAL_IDENTITY="$supervisor_identity"
  set +m
  publish_supervisor_handshake "$supervisor_identity" "$pid" || {
    exec 9>&-
    terminate_external_group "$pid" "$pid" "$supervisor_identity" || true
    ACTIVE_EXTERNAL_PID=''
    ACTIVE_EXTERNAL_PGID=''
    ACTIVE_EXTERNAL_IDENTITY=''
    discard_supervisor_handshake "$supervisor_directory" || true
    return 1
  }
  supervisor_identity_value_matches \
      "$pid" "$pid" "$supervisor_identity" || {
    exec 9>&-
    terminate_external_group "$pid" "$pid" "$supervisor_identity" || true
    ACTIVE_EXTERNAL_PID=''
    ACTIVE_EXTERNAL_PGID=''
    ACTIVE_EXTERNAL_IDENTITY=''
    discard_supervisor_handshake "$supervisor_directory" || true
    return 1
  }
  install_restore_signal_traps
  honor_pending_restore_signal
  while direct_running_job "$pid"; do
    honor_pending_restore_signal
    if supervisor_handshake_available "$supervisor_ready"; then
      [[ "$(read_supervisor_handshake "$supervisor_ready" '^ready$')" == ready ]] ||
        {
          exec 9>&-
          terminate_external_group \
            "$pid" "$pid" "$supervisor_identity" || true
          ACTIVE_EXTERNAL_PID=''
          ACTIVE_EXTERNAL_PGID=''
          ACTIVE_EXTERNAL_IDENTITY=''
          discard_supervisor_handshake "$supervisor_directory" || true
          return 1
        }
      exec 9>&-
      supervisor_ready=''
      deadline=$((SECONDS + timeout_seconds + 1))
    fi
    if supervisor_handshake_available "$supervisor_status"; then
      supervisor_identity_value_matches \
          "$pid" "$pid" "$supervisor_identity" || {
        [[ -z "$supervisor_ready" ]] || exec 9>&-
        terminate_external_group \
          "$pid" "$pid" "$supervisor_identity" || true
        ACTIVE_EXTERNAL_PID=''
        ACTIVE_EXTERNAL_PGID=''
        ACTIVE_EXTERNAL_IDENTITY=''
        discard_supervisor_handshake "$supervisor_directory" || true
        return 1
      }
      result="$(
        read_supervisor_handshake "$supervisor_status" \
          '^(0|[1-9][0-9]{0,2})$'
      )" || result=256
      [[ "$result" -ge 0 && "$result" -le 255 ]] || {
        [[ -z "$supervisor_ready" ]] || exec 9>&-
        terminate_external_group \
          "$pid" "$pid" "$supervisor_identity" || true
        ACTIVE_EXTERNAL_PID=''
        ACTIVE_EXTERNAL_PGID=''
        ACTIVE_EXTERNAL_IDENTITY=''
        discard_supervisor_handshake "$supervisor_directory" || true
        return 1
      }
      publish_supervisor_handshake "$supervisor_ack" ack || {
        terminate_external_group \
          "$pid" "$pid" "$supervisor_identity" || true
        ACTIVE_EXTERNAL_PID=''
        ACTIVE_EXTERNAL_PGID=''
        ACTIVE_EXTERNAL_IDENTITY=''
        discard_supervisor_handshake "$supervisor_directory" || true
        return 1
      }
      break
    fi
    if [[ -n "$supervisor_ready" ]] &&
      ((SECONDS >= startup_deadline)); then
      exec 9>&-
      terminate_external_group \
        "$pid" "$pid" "$supervisor_identity" || true
      ACTIVE_EXTERNAL_PID=''
      ACTIVE_EXTERNAL_PGID=''
      ACTIVE_EXTERNAL_IDENTITY=''
      discard_supervisor_handshake "$supervisor_directory" || true
      return 125
    fi
    if [[ -z "$supervisor_ready" ]] &&
      ((SECONDS >= deadline)); then
      [[ -z "$supervisor_ready" ]] || exec 9>&-
      terminate_external_group \
        "$pid" "$pid" "$supervisor_identity" || true
      ACTIVE_EXTERNAL_PID=''
      ACTIVE_EXTERNAL_PGID=''
      ACTIVE_EXTERNAL_IDENTITY=''
      discard_supervisor_handshake "$supervisor_directory" || true
      return 124
    fi
    sleep 0.01
    honor_pending_restore_signal
  done
  [[ -z "$supervisor_ready" ]] || exec 9>&-
  attempts=0
  while direct_running_job "$pid" && [[ "$attempts" -lt 1000 ]]; do
    honor_pending_restore_signal
    sleep 0.01
    attempts=$((attempts + 1))
  done
  honor_pending_restore_signal
  if direct_running_job "$pid"; then
    terminate_external_group \
      "$pid" "$pid" "$supervisor_identity" || true
    supervisor_result=125
  elif wait "$pid"; then
    supervisor_result=0
  else
    supervisor_result=$?
  fi
  supervisor_identity_value_matches \
      "$pid" "$pid" "$supervisor_identity" ||
    supervisor_result=125
  ACTIVE_EXTERNAL_PID=''
  ACTIVE_EXTERNAL_PGID=''
  ACTIVE_EXTERNAL_IDENTITY=''
  discard_supervisor_handshake "$supervisor_directory" || return 1
  [[ "$supervisor_result" -eq 0 && "$result" -ge 0 && "$result" -le 255 ]] ||
    return 1
  return "$result"
}

run_cleanup_aware() {
  local timeout_seconds="$1"
  shift
  if [[ "$BOUNDED_BATCH_ACTIVE" == true ]]; then
    "$@"
  else
    run_bounded "$timeout_seconds" "$@"
  fi
}

poll_until() {
  local timeout_seconds="$1"
  shift
  valid_uint "$timeout_seconds" || return 1
  local deadline=$((SECONDS + timeout_seconds))
  while ((SECONDS < deadline)); do
    if run_bounded "$EXTERNAL_TIMEOUT_SECONDS" "$@"; then
      return 0
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
  return 1
}

resource_labels() {
  local kind="$1"
  printf '%s|%s|%s|%s' "$PROJECT" "$OWNER_TOKEN" "$kind" "$BACKUP_ID"
}

record_cleanup_intent() {
  local class="$1"
  local name="$2"
  local kind="$3"
  [[ "$class" =~ ^(containers|volumes|networks)$ &&
    "$name" =~ ^happylearn-phase5-restore-[a-f0-9]{12}[-a-z0-9]*$ &&
    "$kind" =~ ^[a-z0-9-]+$ &&
    -f "$CLEANUP_INTENT_LEDGER" &&
    ! -L "$CLEANUP_INTENT_LEDGER" ]] ||
    return 1
  grep -Fxq "$class|$name|$kind" "$CLEANUP_INTENT_LEDGER" ||
    printf '%s|%s|%s\n' "$class" "$name" "$kind" \
      >>"$CLEANUP_INTENT_LEDGER"
}

cleanup_intended() {
  local class="$1"
  local name="$2"
  local kind="$3"
  [[ -f "$CLEANUP_INTENT_LEDGER" &&
    ! -L "$CLEANUP_INTENT_LEDGER" ]] ||
    return 1
  grep -Fxq "$class|$name|$kind" "$CLEANUP_INTENT_LEDGER"
}

inspect_not_found_message() {
  local class="$1"
  local name="$2"
  local path="$3"
  local singular
  case "$class" in
    containers) singular=container ;;
    volumes) singular=volume ;;
    networks) singular=network ;;
    *) return 1 ;;
  esac
  if grep -Fxq "Error: No such $singular: $name" "$path" ||
    grep -Fxq "Error response from daemon: No such $singular: $name" "$path"; then
    return 0
  fi
  case "$class" in
    networks)
      grep -Fxq "Error response from daemon: network $name not found" "$path"
      ;;
    volumes)
      grep -Fxq "Error response from daemon: get $name: no such volume" "$path"
      ;;
    *)
      return 1
      ;;
  esac
}

inspect_resource_labels() {
  local class="$1"
  local name="$2"
  local format="$3"
  local output="$CONTROL_DIRECTORY/inspect.output"
  local error="$CONTROL_DIRECTORY/inspect.error"
  : >"$output" || return 4
  : >"$error" || return 4
  chmod 0600 "$output" "$error" || return 4
  local -a command
  case "$class" in
    containers) command=(docker container inspect --format "$format" "$name") ;;
    volumes) command=(docker volume inspect --format "$format" "$name") ;;
    networks) command=(docker network inspect --format "$format" "$name") ;;
    *) return 4 ;;
  esac
  if run_cleanup_aware "$EXTERNAL_TIMEOUT_SECONDS" \
    "${command[@]}" >"$output" 2>"$error"; then
    [[ -f "$output" && ! -L "$output" ]] || return 4
    cat "$output"
    return 0
  fi
  if inspect_not_found_message "$class" "$name" "$error"; then
    return 3
  fi
  return 4
}

inspect_container_labels() {
  local name="$1"
  inspect_resource_labels containers "$name" \
    '{{.Id}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.restore-owner"}}|{{index .Config.Labels "io.happylearn.phase5.restore-kind"}}|{{index .Config.Labels "io.happylearn.phase5.restore-backup-id"}}'
}

inspect_volume_labels() {
  local name="$1"
  inspect_resource_labels volumes "$name" \
    '{{.Name}}|{{index .Labels "com.docker.compose.project"}}|{{index .Labels "io.happylearn.phase5.restore-owner"}}|{{index .Labels "io.happylearn.phase5.restore-kind"}}|{{index .Labels "io.happylearn.phase5.restore-backup-id"}}'
}

inspect_network_labels() {
  local name="$1"
  inspect_resource_labels networks "$name" \
    '{{.Id}}|{{index .Labels "com.docker.compose.project"}}|{{index .Labels "io.happylearn.phase5.restore-owner"}}|{{index .Labels "io.happylearn.phase5.restore-kind"}}|{{index .Labels "io.happylearn.phase5.restore-backup-id"}}'
}

inspect_class_labels() {
  local class="$1"
  local identifier="$2"
  case "$class" in
    containers) inspect_container_labels "$identifier" ;;
    volumes) inspect_volume_labels "$identifier" ;;
    networks) inspect_network_labels "$identifier" ;;
    *) return 4 ;;
  esac
}

list_owned_resource_names() {
  local class="$1"
  local value line
  local -a command
  case "$class" in
    containers) command=(docker container ls --all --format '{{.Names}}') ;;
    volumes) command=(docker volume ls --format '{{.Name}}') ;;
    networks) command=(docker network ls --format '{{.Name}}') ;;
    *) return 1 ;;
  esac
  value="$(
    run_cleanup_aware "$EXTERNAL_TIMEOUT_SECONDS" \
      "${command[@]}" \
      --filter "label=$BACKUP_LABEL=$BACKUP_ID"
  )" || return 1
  [[ "${#value}" -le 65536 ]] || return 1
  while IFS= read -r line; do
    [[ -z "$line" ||
      "$line" =~ ^happylearn-phase5-restore-[a-f0-9]{12}[-a-z0-9]*$ ]] ||
      return 1
  done <<<"$value"
  printf '%s' "$value"
}

expected_resource_name() {
  local project="$1"
  local class="$2"
  local kind="$3"
  case "$class:$kind" in
    volumes:postgres) printf '%s-postgres' "$project" ;;
    volumes:aistor) printf '%s-aistor' "$project" ;;
    volumes:aistor-license) printf '%s-aistor-license' "$project" ;;
    networks:network) printf '%s-network' "$project" ;;
    containers:volume-probe-postgres)
      printf '%s-volume-probe-postgres' "$project"
      ;;
    containers:volume-probe-aistor)
      printf '%s-volume-probe-aistor' "$project"
      ;;
    containers:volume-probe-aistor-license)
      printf '%s-volume-probe-aistor-license' "$project"
      ;;
    containers:restic-check) printf '%s-restic-check' "$project" ;;
    containers:restic-select) printf '%s-restic-select' "$project" ;;
    containers:restic-restore) printf '%s-restic-restore' "$project" ;;
    containers:restore-ownership) printf '%s-restore-ownership' "$project" ;;
    containers:object-restore) printf '%s-object-restore' "$project" ;;
    containers:aistor-license-init)
      printf '%s-aistor-license-init' "$project"
      ;;
    containers:postgres) printf '%s-postgres' "$project" ;;
    containers:postgres-restore) printf '%s-postgres-restore' "$project" ;;
    containers:aistor) printf '%s-aistor' "$project" ;;
    containers:redis) printf '%s-redis' "$project" ;;
    containers:revoke-sessions) printf '%s-revoke-sessions' "$project" ;;
    containers:app) printf '%s-app' "$project" ;;
    containers:restore-check) printf '%s-restore-check' "$project" ;;
    containers:restore-http-probe)
      printf '%s-restore-http-probe' "$project"
      ;;
    *) return 1 ;;
  esac
}

valid_observed_resource() {
  local class="$1"
  local name="$2"
  local observation="$3"
  local identity project owner kind backup extra expected_name
  IFS='|' read -r identity project owner kind backup extra <<<"$observation"
  [[ -z "$extra" ]] || return 1
  valid_project "$project" || return 1
  valid_owner_token "$owner" || return 1
  [[ "$project" == "${PROJECT_PREFIX}${owner:0:12}" &&
    "$backup" == "$BACKUP_ID" ]] ||
    return 1
  case "$class:$kind" in
    volumes:postgres | volumes:aistor | volumes:aistor-license | \
      networks:network | \
      containers:volume-probe-postgres | \
      containers:volume-probe-aistor | \
      containers:volume-probe-aistor-license | \
      containers:restic-check | containers:restic-select | \
      containers:restic-restore | containers:restore-ownership | \
      containers:object-restore | \
      containers:aistor-license-init | containers:postgres | \
      containers:postgres-restore | containers:aistor | containers:redis | \
      containers:revoke-sessions | containers:app | \
      containers:restore-check | containers:restore-http-probe) ;;
    *) return 1 ;;
  esac
  expected_name="$(expected_resource_name "$project" "$class" "$kind")" ||
    return 1
  [[ "$name" == "$expected_name" ]] || return 1
  case "$class" in
    containers | networks) [[ "$identity" =~ ^[a-f0-9]{64}$ ]] ;;
    volumes) [[ "$identity" == "$name" ]] ;;
    *) return 1 ;;
  esac
}

current_resource_matches() {
  local class="$1"
  local name="$2"
  local kind="$3"
  local observation
  observation="$(inspect_class_labels "$class" "$name")" || return 1
  valid_observed_resource "$class" "$name" "$observation" || return 1
  [[ "${observation#*|}" == "$(resource_labels "$kind")" ]]
}

remove_verified_resource() {
  local class="$1"
  local name="$2"
  local expected_labels="${3:-}"
  local observation confirmation identity labels status remove_target
  if observation="$(inspect_class_labels "$class" "$name")"; then
    :
  else
    status=$?
    [[ "$status" -eq 3 ]] && return 0
    return 1
  fi
  valid_observed_resource "$class" "$name" "$observation" || return 1
  identity="${observation%%|*}"
  labels="${observation#*|}"
  [[ -z "$expected_labels" || "$labels" == "$expected_labels" ]] || return 1
  confirmation="$(inspect_class_labels "$class" "$identity")" || return 1
  [[ "$confirmation" == "$observation" ]] || return 1
  case "$class" in
    containers)
      remove_target="$identity"
      run_cleanup_aware "$EXTERNAL_TIMEOUT_SECONDS" \
        docker rm --force "$remove_target" >/dev/null ||
        return 1
      ;;
    volumes)
      remove_target="$name"
      run_cleanup_aware "$EXTERNAL_TIMEOUT_SECONDS" \
        docker volume rm "$remove_target" >/dev/null ||
        return 1
      ;;
    networks)
      remove_target="$identity"
      run_cleanup_aware "$EXTERNAL_TIMEOUT_SECONDS" \
        docker network rm "$remove_target" >/dev/null ||
        return 1
      ;;
    *) return 1 ;;
  esac
  if inspect_class_labels "$class" "$identity" >/dev/null; then
    return 1
  else
    status=$?
  fi
  [[ "$status" -eq 3 ]]
}

remove_owned_orphan() {
  local class="$1"
  local name="$2"
  remove_verified_resource "$class" "$name"
}

reap_orphan_resources() {
  local class names name
  [[ "$RESTORE_LOCK_FD_OPEN" == true ]] || return 1
  for class in containers volumes networks; do
    names="$(list_owned_resource_names "$class")" || return 1
    while IFS= read -r name; do
      [[ -z "$name" ]] || remove_owned_orphan "$class" "$name" || return 1
    done <<<"$names"
  done
}

owned_resources_absent() {
  local class names
  [[ "$RESTORE_LOCK_FD_OPEN" == true ]] || return 1
  for class in containers volumes networks; do
    names="$(list_owned_resource_names "$class")" || return 1
    [[ -z "$names" ]] || return 1
  done
}

create_volume() {
  local name="$1"
  local kind="$2"
  local labels status
  if labels="$(inspect_volume_labels "$name")"; then
    return 1
  else
    status=$?
  fi
  [[ "$status" -eq 3 ]] || return 1
  record_cleanup_intent volumes "$name" "$kind" || return 1
  run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
    docker volume create \
    --label "com.docker.compose.project=$PROJECT" \
    --label "$OWNER_LABEL=$OWNER_TOKEN" \
    --label "$KIND_LABEL=$kind" \
    --label "$BACKUP_LABEL=$BACKUP_ID" \
    "$name" >/dev/null
  case "$kind" in
    postgres) POSTGRES_VOLUME_CREATED=true ;;
    aistor) AISTOR_VOLUME_CREATED=true ;;
    aistor-license) AISTOR_LICENSE_VOLUME_CREATED=true ;;
    *) return 1 ;;
  esac
  current_resource_matches volumes "$name" "$kind"
}

create_network() {
  local labels status
  if labels="$(inspect_network_labels "$NETWORK_NAME")"; then
    return 1
  else
    status=$?
  fi
  [[ "$status" -eq 3 ]] || return 1
  record_cleanup_intent networks "$NETWORK_NAME" network || return 1
  run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
    docker network create --internal \
    --label "com.docker.compose.project=$PROJECT" \
    --label "$OWNER_LABEL=$OWNER_TOKEN" \
    --label "$KIND_LABEL=network" \
    --label "$BACKUP_LABEL=$BACKUP_ID" \
    "$NETWORK_NAME" >/dev/null
  NETWORK_CREATED=true
  current_resource_matches networks "$NETWORK_NAME" network
}

run_named_container() {
  local name="$1"
  local kind="$2"
  shift 2
  [[ -n "$CONTAINER_RECORD" && -f "$CONTAINER_RECORD" &&
    ! -L "$CONTAINER_RECORD" ]] ||
    return 1
  record_cleanup_intent containers "$name" "$kind" || return 1
  printf '%s|%s\n' "$name" "$kind" >>"$CONTAINER_RECORD"
  run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
    docker run \
    --name "$name" \
    --cpus "$CONTAINER_CPUS" \
    --memory "$CONTAINER_MEMORY" \
    --memory-swap "$CONTAINER_MEMORY_SWAP" \
    --pids-limit "$CONTAINER_PIDS_LIMIT" \
    --label "com.docker.compose.project=$PROJECT" \
    --label "$OWNER_LABEL=$OWNER_TOKEN" \
    --label "$KIND_LABEL=$kind" \
    --label "$BACKUP_LABEL=$BACKUP_ID" \
    "$@"
}

assert_new_empty_volume() {
  local volume="$1"
  local kind="$2"
  run_named_container \
    "$PROJECT-volume-probe-$kind" \
    "volume-probe-$kind" \
    --network none \
    --read-only \
    --mount "type=volume,src=$volume,dst=/target,readonly" \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /bin/sh -ceu \
    'test -z "$(find /target -mindepth 1 -maxdepth 1 -print -quit)"' \
    >/dev/null
}

restic_container() {
  local name="$1"
  local kind="$2"
  shift 2
  run_named_container \
    "$name" \
    "$kind" \
    --network none \
    --read-only \
    --user "$HOST_UID:$HOST_GID" \
    --mount "type=bind,src=$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY,dst=/repository" \
    --mount "type=bind,src=$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_password,dst=/run/secrets/local_password,readonly" \
    --env RESTIC_REPOSITORY=/repository \
    --env RESTIC_PASSWORD_FILE=/run/secrets/local_password \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    restic --no-cache "$@"
}

restic_restore_container() {
  local name="$1"
  local kind="$2"
  shift 2
  run_named_container \
    "$name" \
    "$kind" \
    --network none \
    --read-only \
    --user "$HOST_UID:$HOST_GID" \
    --mount "type=bind,src=$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY,dst=/repository" \
    --mount "type=bind,src=$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_password,dst=/run/secrets/local_password,readonly" \
    --mount "type=bind,src=$RESTORE_DIRECTORY,dst=/restore" \
    --env RESTIC_REPOSITORY=/repository \
    --env RESTIC_PASSWORD_FILE=/run/secrets/local_password \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    restic --no-cache "$@"
}

normalize_restore_ownership() {
  run_named_container \
    "$PROJECT-restore-ownership" restore-ownership \
    --network none \
    --read-only \
    --user 0:0 \
    --cap-drop ALL \
    --cap-add CHOWN \
    --cap-add DAC_READ_SEARCH \
    --security-opt no-new-privileges:true \
    --mount "type=bind,src=$RESTORE_DIRECTORY,dst=/restore" \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /bin/sh -ceu '
      target_uid="$1"
      target_gid="$2"
      case "$target_uid:$target_gid" in
        *[!0-9:]* | :* | *: | *:*:*) exit 1 ;;
      esac
      chown --recursive --no-dereference \
        "$target_uid:$target_gid" /restore
      unexpected="$(
        find /restore -xdev \
          \( ! -user "$target_uid" -o ! -group "$target_gid" \) \
          -print -quit
      )"
      [ -z "$unexpected" ]
    ' restore-ownership "$HOST_UID" "$HOST_GID" \
    >/dev/null
}

repository_check() {
  restic_container \
    "$PROJECT-restic-check" restic-check \
    check --read-data >/dev/null
}

select_snapshot() {
  local inventory="$WORK_DIRECTORY/snapshots.json"
  local inventory_value ids id_count tag_array tags tag_count
  local batch_tag_count manifest_tags manifest_tag_count manifest_sha256
  restic_container \
    "$PROJECT-restic-select" restic-select \
    snapshots --json --tag "happylearn-batch:$BACKUP_ID" \
    >"$inventory"
  [[ -f "$inventory" && ! -L "$inventory" ]] || return 1
  inventory_value="$(<"$inventory")"
  [[ -n "$inventory_value" &&
    "${#inventory_value}" -le 65536 &&
    "$inventory_value" == \[*\] ]] ||
    return 1
  ids="$(
    grep -Eo '"id"[[:space:]]*:[[:space:]]*"[a-f0-9]{64}"' \
      "$inventory" |
      sed -E 's/.*"([a-f0-9]{64})"/\1/'
  )" || return 1
  id_count="$(printf '%s\n' "$ids" | grep -Ec '^[a-f0-9]{64}$')" ||
    return 1
  [[ "$id_count" == '1' && "$ids" != *$'\n'* ]] || return 1
  tag_array="$(
    grep -Eo '"tags"[[:space:]]*:[[:space:]]*\[[^][]*\]' "$inventory"
  )" || return 1
  [[ -n "$tag_array" && "$tag_array" != *$'\n'* ]] || return 1
  tags="$(
    printf '%s\n' "$tag_array" |
      grep -Eo '"[^"]*"' |
      sed -n '2,$s/^"//;2,$s/"$//;2,$p'
  )" || return 1
  tag_count="$(printf '%s\n' "$tags" | grep -Ec '^.+$')" || return 1
  [[ "$tag_count" == 2 ]] || return 1
  batch_tag_count="$(
    printf '%s\n' "$tags" |
      grep -Fxc "happylearn-batch:$BACKUP_ID"
  )" || return 1
  [[ "$batch_tag_count" == 1 ]] || return 1
  manifest_tags="$(
    printf '%s\n' "$tags" |
      grep -E '^happylearn-manifest-sha256:[a-f0-9]{64}$'
  )" || return 1
  manifest_tag_count="$(
    printf '%s\n' "$manifest_tags" |
      grep -Ec '^happylearn-manifest-sha256:[a-f0-9]{64}$'
  )" || return 1
  [[ "$manifest_tag_count" == 1 &&
    "$manifest_tags" != *$'\n'* ]] ||
    return 1
  manifest_sha256="${manifest_tags#happylearn-manifest-sha256:}"
  printf '%s|%s' "$ids" "$manifest_sha256"
}

restore_snapshot() {
  local snapshot_id="$1"
  local manifest_path mode size actual_sha256
  [[ "$snapshot_id" =~ ^[a-f0-9]{64}$ ]] || return 1
  if ! restic_restore_container \
    "$PROJECT-restic-restore" restic-restore \
    restore "$snapshot_id" --target /restore >/dev/null; then
    safe_log 'snapshot_restore_command_failed'
    return 1
  fi
  if ! normalize_restore_ownership; then
    safe_log 'snapshot_restore_ownership_failed'
    return 1
  fi
  [[ -f "$RESTORE_DIRECTORY/database.dump" &&
    ! -L "$RESTORE_DIRECTORY/database.dump" &&
    -d "$RESTORE_DIRECTORY/source/aistor" &&
    ! -L "$RESTORE_DIRECTORY/source/aistor" ]] ||
    {
      safe_log 'snapshot_restore_layout_invalid'
      return 1
    }
  manifest_path="$RESTORE_DIRECTORY/manifest.json"
  [[ -f "$manifest_path" &&
    ! -L "$manifest_path" &&
    "$(portable_owner "$manifest_path")" == "$HOST_UID" ]] ||
    {
      safe_log 'snapshot_restore_manifest_identity_invalid'
      return 1
    }
  mode="$(portable_mode "$manifest_path")" || {
    safe_log 'snapshot_restore_manifest_mode_invalid'
    return 1
  }
  [[ "$mode" =~ ^[0-7]?([0-7])[0-7][0-7]$ ]] &&
    ((8#${BASH_REMATCH[1]} & 4)) ||
    {
      safe_log 'snapshot_restore_manifest_mode_invalid'
      return 1
    }
  size="$(portable_size "$manifest_path")" || {
    safe_log 'snapshot_restore_manifest_size_invalid'
    return 1
  }
  [[ "$size" -ge 1 && "$size" -le 65536 ]] || {
    safe_log 'snapshot_restore_manifest_size_invalid'
    return 1
  }
  actual_sha256="$(portable_sha256 "$manifest_path")" || {
    safe_log 'snapshot_restore_manifest_hash_invalid'
    return 1
  }
  [[ "$actual_sha256" == "$EXPECTED_MANIFEST_SHA256" ]] || {
    safe_log 'snapshot_restore_manifest_hash_invalid'
    return 1
  }
  RESTORED_MANIFEST="$manifest_path"
}

restore_object_data() {
  run_named_container \
    "$PROJECT-object-restore" object-restore \
    --network none \
    --read-only \
    --user 0:0 \
    --mount "type=bind,src=$RESTORE_DIRECTORY/source/aistor,dst=/source,readonly" \
    --mount "type=volume,src=$AISTOR_VOLUME,dst=/target" \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /bin/sh -ceu \
    'test -z "$(find /target -mindepth 1 -maxdepth 1 -print -quit)"; cp -a /source/. /target/; chown -R 1000:0 /target; printf "%s\n" PHASE5_RESTORE_OBJECT_DATA' \
    >/dev/null
}

initialize_aistor_license() {
  run_named_container \
    "$PROJECT-aistor-license-init" aistor-license-init \
    --network none \
    --read-only \
    --user 0:0 \
    --cap-drop ALL \
    --cap-add CHOWN \
    --cap-add DAC_OVERRIDE \
    --security-opt no-new-privileges \
    --mount "type=bind,src=$HAPPYLEARN_AISTOR_LICENSE_FILE,dst=/license-source/minio.license,readonly" \
    --mount "type=volume,src=$AISTOR_LICENSE_VOLUME,dst=/license-target" \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /bin/sh -ceu \
    'test -z "$(find /license-target -mindepth 1 -maxdepth 1 -print -quit)"; cp /license-source/minio.license /license-target/minio.license; chown 1000:0 /license-target/minio.license; chmod 0400 /license-target/minio.license; test "$(stat -c %u:%g:%a /license-target/minio.license)" = 1000:0:400; printf "%s\n" PHASE5_RESTORE_AISTOR_LICENSE' \
    >/dev/null
}

start_postgres() {
  run_named_container \
    "$POSTGRES_CONTAINER" postgres \
    --detach \
    --network "$NETWORK_NAME" \
    --network-alias postgres \
    --read-only \
    --tmpfs /tmp:rw,noexec,nosuid,size=32m \
    --tmpfs /var/run/postgresql:rw,noexec,nosuid,size=8m \
    --mount "type=volume,src=$POSTGRES_VOLUME,dst=/var/lib/postgresql" \
    --env-file "$CONTROL_DIRECTORY/postgres.env" \
    "$POSTGRES_IMAGE" >/dev/null
  poll_until "$READY_TIMEOUT_SECONDS" \
    docker exec "$POSTGRES_CONTAINER" \
    pg_isready -U happylearn -d happylearn >/dev/null
}

restore_database() {
  run_named_container \
    "$PROJECT-postgres-restore" postgres-restore \
    --network "$NETWORK_NAME" \
    --read-only \
    --user "$HOST_UID:$HOST_GID" \
    --mount "type=bind,src=$RESTORE_DIRECTORY/database.dump,dst=/restore/database.dump,readonly" \
    --mount "type=bind,src=$CONTROL_DIRECTORY/pgpass,dst=/run/secrets/pgpass,readonly" \
    --env PGHOST=postgres \
    --env PGPORT=5432 \
    --env PGUSER=happylearn \
    --env PGDATABASE=happylearn \
    --env PGPASSFILE=/run/secrets/pgpass \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    pg_restore --no-owner --no-privileges --clean --if-exists \
    --dbname happylearn /restore/database.dump >/dev/null
}

start_dependencies() {
  run_named_container \
    "$AISTOR_CONTAINER" aistor \
    --detach \
    --network "$NETWORK_NAME" \
    --network-alias minio \
    --user 1000:0 \
    --read-only \
    --tmpfs /tmp:rw,noexec,nosuid,size=16m \
    --mount "type=volume,src=$AISTOR_VOLUME,dst=/data" \
    --mount "type=volume,src=$AISTOR_LICENSE_VOLUME,dst=/minio-license,readonly" \
    --env-file "$CONTROL_DIRECTORY/aistor.env" \
    "$AISTOR_IMAGE" \
    minio server /data --license /minio-license/minio.license >/dev/null
  run_named_container \
    "$REDIS_CONTAINER" redis \
    --detach \
    --network "$NETWORK_NAME" \
    --network-alias redis \
    --read-only \
    --tmpfs /data:rw,noexec,nosuid,size=64m \
    "$REDIS_IMAGE" >/dev/null
  poll_until "$READY_TIMEOUT_SECONDS" \
    docker exec "$AISTOR_CONTAINER" \
    curl --fail --silent http://127.0.0.1:9000/minio/health/ready \
    >/dev/null
}

revoke_restored_sessions() {
  run_named_container \
    "$PROJECT-revoke-sessions" revoke-sessions \
    --network "$NETWORK_NAME" \
    --read-only \
    --user "$HOST_UID:$HOST_GID" \
    --mount "type=bind,src=$CONTROL_DIRECTORY/pgpass,dst=/run/secrets/pgpass,readonly" \
    --env PGHOST=postgres \
    --env PGPORT=5432 \
    --env PGUSER=happylearn \
    --env PGDATABASE=happylearn \
    --env PGPASSFILE=/run/secrets/pgpass \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    psql --no-psqlrc --set ON_ERROR_STOP=1 --command \
    "BEGIN; UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()), revoke_reason=COALESCE(revoke_reason,'restore_verification'); UPDATE operational_modes SET mode='normal', owner_id=NULL, lease_token_hash=NULL, lease_expires_at=NULL, entered_at=NULL, updated_at=now(), version=version+1 WHERE singleton_id=true; COMMIT; SELECT 'PHASE5_RESTORE_SESSIONS_REVOKED';" \
    >/dev/null
}

start_restored_app() {
  run_named_container \
    "$APP_CONTAINER" app \
    --detach \
    --network "$NETWORK_NAME" \
    --network-alias app \
    --user 10001:10001 \
    --read-only \
    --tmpfs /tmp:rw,noexec,nosuid,size=16m \
    --env-file "$CONTROL_DIRECTORY/app.env" \
    "$APP_IMAGE" >/dev/null
}

wait_for_restored_app() {
  poll_until "$READY_TIMEOUT_SECONDS" \
    docker exec "$APP_CONTAINER" \
    curl --fail --silent http://127.0.0.1:8080/api/v1/health/ready \
    >/dev/null
}

run_restore_check() {
  local actual_manifest_sha256
  [[ "$RESTORED_MANIFEST" == "$RESTORE_DIRECTORY/manifest.json" &&
    -f "$RESTORED_MANIFEST" &&
    ! -L "$RESTORED_MANIFEST" ]] ||
    return 1
  actual_manifest_sha256="$(portable_sha256 "$RESTORED_MANIFEST")" ||
    return 1
  [[ "$actual_manifest_sha256" == "$EXPECTED_MANIFEST_SHA256" ]] ||
    return 1
  run_named_container \
    "$PROJECT-restore-check" restore-check \
    --network "$NETWORK_NAME" \
    --read-only \
    --user "$HOST_UID:$HOST_GID" \
    --mount "type=bind,src=$CHECK_OUTPUT_DIRECTORY,dst=/work" \
    --mount "type=bind,src=$CONTROL_DIRECTORY/pgpass,dst=/run/secrets/pgpass,readonly" \
    --mount "type=bind,src=$RESTORED_MANIFEST,dst=/run/restore/manifest.json,readonly" \
    --env-file "$CONTROL_DIRECTORY/restore-check.env" \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /app/happylearn-backup restore-check \
    --backup-id "$BACKUP_ID" \
    --report-file /work/restore-check.report \
    >/dev/null
}

run_restore_http_probe() {
  run_named_container \
    "$PROJECT-restore-http-probe" restore-http-probe \
    --network "$NETWORK_NAME" \
    --read-only \
    --user "$HOST_UID:$HOST_GID" \
    --mount "type=bind,src=$HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE,dst=/run/secrets/restore-probe-teacher.json,readonly" \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /app/happylearn-backup restore-http-probe \
    >/dev/null
  HTTP_PROBE_SUCCEEDED=true
}

valid_int64_decimal() {
  local value="$1"
  [[ "$value" =~ ^(0|[1-9][0-9]{0,18})$ ]] || return 1
  [[ "${#value}" -lt 19 ||
    "$value" < 9223372036854775808 ]]
}

hex_to_bytes() {
  local value="$1"
  [[ -n "$value" &&
    "$value" =~ ^[a-f0-9]+$ &&
    $((${#value} % 2)) -eq 0 ]] ||
    return 1
  while [[ -n "$value" ]]; do
    printf '%b' "\\x${value:0:2}" || return 1
    value="${value:2}"
  done
}

verify_bound_evidence_sha256() {
  local verification_sha256="$1"
  local evidence_sha256="$2"
  local evidence_input="$WORK_DIRECTORY/evidence.input"
  local backup_hex="${BACKUP_ID//-/}"
  local actual_evidence_sha256
  [[ "$backup_hex" =~ ^[a-f0-9]{32}$ &&
    "$EXPECTED_MANIFEST_SHA256" =~ ^[a-f0-9]{64}$ &&
    "$verification_sha256" =~ ^[a-f0-9]{64}$ &&
    "$evidence_sha256" =~ ^[a-f0-9]{64}$ ]] ||
    return 1
  : >"$evidence_input" || return 1
  chmod 0600 "$evidence_input" || return 1
  hex_to_bytes "$backup_hex" >>"$evidence_input" || return 1
  hex_to_bytes "$EXPECTED_MANIFEST_SHA256" >>"$evidence_input" || return 1
  hex_to_bytes "$verification_sha256" >>"$evidence_input" || return 1
  actual_evidence_sha256="$(portable_sha256 "$evidence_input")" || return 1
  [[ "$actual_evidence_sha256" == "$evidence_sha256" ]]
}

load_safe_restore_counts() {
  local path="$CHECK_OUTPUT_DIRECTORY/restore-check.report"
  local line key value numeric_value
  local index=0
  local row_sum=0
  local mode size
  local -a expected_keys=(
    schema_version
    backup_id
    manifest_sha256
    migration_version
    table_users_count
    table_sessions_count
    table_subjects_count
    table_grades_count
    table_terms_count
    table_chapters_count
    table_lessons_count
    table_lesson_revisions_count
    table_files_count
    table_file_versions_count
    table_file_previews_count
    table_qa_threads_count
    table_qa_messages_count
    table_ai_threads_count
    table_ai_messages_count
    table_ai_runs_count
    row_count_total
    checked_object_count
    missing_object_count
    unexpected_object_count
    active_session_count
    verification_report_sha256
    evidence_sha256
  )
  SCHEMA_VERSION=''
  MIGRATION_VERSION=''
  ROW_COUNT_TOTAL=''
  CHECKED_OBJECT_COUNT=''
  MISSING_OBJECT_COUNT=''
  UNEXPECTED_OBJECT_COUNT=''
  ACTIVE_SESSION_COUNT=''
  VERIFICATION_REPORT_SHA256=''
  EVIDENCE_SHA256=''
  [[ -f "$path" &&
    ! -L "$path" &&
    "$(portable_owner "$path")" == "$HOST_UID" ]] ||
    return 1
  mode="$(portable_mode "$path")" || return 1
  size="$(portable_size "$path")" || return 1
  [[ "$mode" == 600 && "$size" -ge 1 && "$size" -le 16384 ]] ||
    return 1
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ "$index" -lt "${#expected_keys[@]}" &&
      "$line" =~ ^[a-z0-9_]+=[^=]+$ ]] ||
      return 1
    key="${line%%=*}"
    value="${line#*=}"
    [[ "$key" == "${expected_keys[$index]}" ]] || return 1
    case "$key" in
      schema_version)
        [[ "$value" == 1 ]] || return 1
        SCHEMA_VERSION="$value"
        ;;
      backup_id)
        [[ "$value" == "$BACKUP_ID" ]] || return 1
        ;;
      manifest_sha256)
        [[ "$value" == "$EXPECTED_MANIFEST_SHA256" ]] || return 1
        ;;
      migration_version)
        valid_int64_decimal "$value" && [[ "$value" != 0 ]] || return 1
        MIGRATION_VERSION="$value"
        ;;
      table_*_count)
        valid_int64_decimal "$value" || return 1
        numeric_value=$((10#$value))
        ((row_sum <= 9223372036854775807 - numeric_value)) ||
          return 1
        row_sum=$((row_sum + numeric_value))
        ;;
      row_count_total)
        valid_int64_decimal "$value" || return 1
        ROW_COUNT_TOTAL="$value"
        ;;
      checked_object_count)
        valid_int64_decimal "$value" || return 1
        CHECKED_OBJECT_COUNT="$value"
        ;;
      missing_object_count)
        valid_int64_decimal "$value" || return 1
        MISSING_OBJECT_COUNT="$value"
        ;;
      unexpected_object_count)
        valid_int64_decimal "$value" || return 1
        UNEXPECTED_OBJECT_COUNT="$value"
        ;;
      active_session_count)
        valid_int64_decimal "$value" || return 1
        ACTIVE_SESSION_COUNT="$value"
        ;;
      verification_report_sha256)
        [[ "$value" =~ ^[a-f0-9]{64}$ ]] || return 1
        VERIFICATION_REPORT_SHA256="$value"
        ;;
      evidence_sha256)
        [[ "$value" =~ ^[a-f0-9]{64}$ ]] || return 1
        EVIDENCE_SHA256="$value"
        ;;
      *) return 1 ;;
    esac
    index=$((index + 1))
  done <"$path"
  [[ "$index" -eq "${#expected_keys[@]}" &&
    "$ROW_COUNT_TOTAL" == "$row_sum" &&
    "$MISSING_OBJECT_COUNT" == 0 &&
    "$UNEXPECTED_OBJECT_COUNT" == 0 &&
    "$ACTIVE_SESSION_COUNT" == 0 ]] ||
    return 1
  verify_bound_evidence_sha256 \
    "$VERIFICATION_REPORT_SHA256" "$EVIDENCE_SHA256"
}

portable_sha256() {
  local path="$1"
  local output="$WORK_DIRECTORY/report.sha256"
  if command -v sha256sum >/dev/null 2>&1; then
    run_bounded "$EXTERNAL_TIMEOUT_SECONDS" sha256sum "$path" >"$output"
  elif command -v shasum >/dev/null 2>&1; then
    run_bounded "$EXTERNAL_TIMEOUT_SECONDS" shasum -a 256 "$path" >"$output"
  else
    return 1
  fi
  local value
  value="$(sed -n '1s/[[:space:]].*$//p' "$output")"
  [[ "$value" =~ ^[a-f0-9]{64}$ ]] || return 1
  printf '%s' "$value"
}

write_sanitized_report() {
  local duration=$((SECONDS - START_SECONDS))
  local canonical="$WORK_DIRECTORY/report.canonical"
  local report_sha256
  [[ "$HTTP_PROBE_SUCCEEDED" == true &&
    "$duration" -ge 0 &&
    "$duration" -lt "$RTO_LIMIT_SECONDS" ]] ||
    return 1
  printf '%s\n' \
    "schemaVersion=1" \
    "backupId=$BACKUP_ID" \
    "manifestSHA256=$EXPECTED_MANIFEST_SHA256" \
    "verificationReportSHA256=$VERIFICATION_REPORT_SHA256" \
    "evidenceSHA256=$EVIDENCE_SHA256" \
    "durationSeconds=$duration" \
    "migrationVersion=$MIGRATION_VERSION" \
    "rowCountTotal=$ROW_COUNT_TOTAL" \
    "checkedObjectCount=$CHECKED_OBJECT_COUNT" \
    "missingObjectCount=$MISSING_OBJECT_COUNT" \
    "unexpectedObjectCount=$UNEXPECTED_OBJECT_COUNT" \
    "activeSessionCount=$ACTIVE_SESSION_COUNT" \
    'isolation404ProbeCount=2' \
    >"$canonical"
  chmod 0600 "$canonical"
  report_sha256="$(portable_sha256 "$canonical")" || return 1
  printf '%s\n' \
    "{\"schemaVersion\":1,\"backupId\":\"$BACKUP_ID\",\"manifestSHA256\":\"$EXPECTED_MANIFEST_SHA256\",\"verificationReportSHA256\":\"$VERIFICATION_REPORT_SHA256\",\"evidenceSHA256\":\"$EVIDENCE_SHA256\",\"durationSeconds\":$duration,\"migrationVersion\":$MIGRATION_VERSION,\"rowCountTotal\":$ROW_COUNT_TOTAL,\"checkedObjectCount\":$CHECKED_OBJECT_COUNT,\"missingObjectCount\":$MISSING_OBJECT_COUNT,\"unexpectedObjectCount\":$UNEXPECTED_OBJECT_COUNT,\"activeSessionCount\":$ACTIVE_SESSION_COUNT,\"isolation404ProbeCount\":2,\"reportSHA256\":\"$report_sha256\"}" \
    >"$REPORT_TEMPORARY"
  chmod 0600 "$REPORT_TEMPORARY"
}

cleanup_container() {
  local name="$1"
  local kind="$2"
  remove_verified_resource \
    containers "$name" "$(resource_labels "$kind")"
}

cleanup_volume() {
  local name="$1"
  local kind="$2"
  remove_verified_resource \
    volumes "$name" "$(resource_labels "$kind")"
}

cleanup_network() {
  remove_verified_resource \
    networks "$NETWORK_NAME" "$(resource_labels network)"
}

cleanup_owned_resources_batch() {
  local name kind status=0
  BOUNDED_BATCH_ACTIVE=true
  while IFS='|' read -r name kind; do
    [[ -n "$name" && -n "$kind" ]] || continue
    cleanup_container "$name" "$kind" || status=1
  done <<EOF
$PROJECT-restore-http-probe|restore-http-probe
$PROJECT-restore-check|restore-check
$PROJECT-app|app
$PROJECT-revoke-sessions|revoke-sessions
$PROJECT-redis|redis
$PROJECT-aistor|aistor
$PROJECT-postgres-restore|postgres-restore
$PROJECT-postgres|postgres
$PROJECT-object-restore|object-restore
$PROJECT-aistor-license-init|aistor-license-init
$PROJECT-restore-ownership|restore-ownership
$PROJECT-restic-restore|restic-restore
$PROJECT-restic-select|restic-select
$PROJECT-restic-check|restic-check
$PROJECT-volume-probe-aistor|volume-probe-aistor
$PROJECT-volume-probe-aistor-license|volume-probe-aistor-license
$PROJECT-volume-probe-postgres|volume-probe-postgres
EOF
  cleanup_volume "$AISTOR_LICENSE_VOLUME" aistor-license || status=1
  cleanup_volume "$AISTOR_VOLUME" aistor || status=1
  cleanup_volume "$POSTGRES_VOLUME" postgres || status=1
  cleanup_network || status=1
  owned_resources_absent || status=1
  return "$status"
}

cleanup_ledger_valid() {
  local original_status="$1"
  local expected prefix line size result=1
  [[ "$original_status" =~ ^([0-9]|[1-9][0-9]{1,2})$ &&
    "$original_status" -le 255 ]] ||
    return 1
  [[ -f "$CLEANUP_INTENT_LEDGER" &&
    ! -L "$CLEANUP_INTENT_LEDGER" &&
    "$(portable_mode "$CLEANUP_INTENT_LEDGER")" == 600 &&
    "$(portable_owner "$CLEANUP_INTENT_LEDGER")" == "$(id -u)" ]] ||
    return 1
  size="$(portable_size "$CLEANUP_INTENT_LEDGER")" || return 1
  [[ "$size" -ge 0 && "$size" -le 4096 ]] || return 1
  expected="$(mktemp "$CONTROL_DIRECTORY/cleanup.expected.XXXXXX")" ||
    return 1
  prefix="$(mktemp "$CONTROL_DIRECTORY/cleanup.prefix.XXXXXX")" || {
    rm -f "$expected"
    return 1
  }
  chmod 0600 "$expected" "$prefix" || {
    rm -f "$expected" "$prefix"
    return 1
  }
  printf '%s\n' \
    "networks|$NETWORK_NAME|network" \
    "volumes|$POSTGRES_VOLUME|postgres" \
    "volumes|$AISTOR_VOLUME|aistor" \
    "volumes|$AISTOR_LICENSE_VOLUME|aistor-license" \
    "containers|$PROJECT-volume-probe-postgres|volume-probe-postgres" \
    "containers|$PROJECT-volume-probe-aistor|volume-probe-aistor" \
    "containers|$PROJECT-volume-probe-aistor-license|volume-probe-aistor-license" \
    "containers|$PROJECT-restic-check|restic-check" \
    "containers|$PROJECT-restic-select|restic-select" \
    "containers|$PROJECT-restic-restore|restic-restore" \
    "containers|$PROJECT-restore-ownership|restore-ownership" \
    "containers|$PROJECT-object-restore|object-restore" \
    "containers|$PROJECT-aistor-license-init|aistor-license-init" \
    "containers|$PROJECT-postgres|postgres" \
    "containers|$PROJECT-postgres-restore|postgres-restore" \
    "containers|$PROJECT-aistor|aistor" \
    "containers|$PROJECT-redis|redis" \
    "containers|$PROJECT-revoke-sessions|revoke-sessions" \
    "containers|$PROJECT-app|app" \
    "containers|$PROJECT-restore-check|restore-check" \
    "containers|$PROJECT-restore-http-probe|restore-http-probe" \
    >"$expected"
  if [[ "$original_status" -eq 0 ]]; then
    cmp -s "$CLEANUP_INTENT_LEDGER" "$expected" && result=0
  else
    : >"$prefix"
    if cmp -s "$CLEANUP_INTENT_LEDGER" "$prefix"; then
      result=0
    else
      while IFS= read -r line; do
        printf '%s\n' "$line" >>"$prefix"
        if cmp -s "$CLEANUP_INTENT_LEDGER" "$prefix"; then
          result=0
          break
        fi
      done <"$expected"
    fi
  fi
  rm -f "$expected" "$prefix" || return 1
  return "$result"
}

cleanup_restore() {
  local status="${1:-0}"
  local original_status="$status"
  local cleanup_timeout=$((EXTERNAL_TIMEOUT_SECONDS + 5))
  if [[ "$CLEANING_UP" == true ]]; then
    return "$status"
  fi
  CLEANING_UP=true
  if valid_project "$PROJECT" &&
    valid_owner_token "$OWNER_TOKEN" &&
    [[ "$WORKSPACE_INITIALIZED" == true ]]; then
    cleanup_ledger_valid "$original_status" || status=1
    run_bounded "$cleanup_timeout" cleanup_owned_resources_batch ||
      status=1
  fi
  if [[ -n "$WORK_DIRECTORY" &&
    "$WORK_DIRECTORY" == "${TMPDIR:-/tmp}/phase5-restore-verify."* &&
    -d "$WORK_DIRECTORY" && ! -L "$WORK_DIRECTORY" ]]; then
    chmod -R u+rwX "$WORK_DIRECTORY" 2>/dev/null || status=1
    rm -rf "$WORK_DIRECTORY" || status=1
  fi
  if [[ "$RESTORE_LOCK_FD_OPEN" == true ]]; then
    exec 10>&-
    RESTORE_LOCK_FD_OPEN=false
  fi
  return "$status"
}

discard_report_temporary() {
  if [[ -n "$REPORT_TEMPORARY" &&
    "$REPORT_TEMPORARY" == "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/.restore-${BACKUP_ID}.new" &&
    -f "$REPORT_TEMPORARY" && ! -L "$REPORT_TEMPORARY" ]]; then
    rm -f "$REPORT_TEMPORARY"
  fi
}

release_report_lock() {
  if [[ "$REPORT_LOCK_FD_OPEN" == true ]]; then
    exec 11>&-
    REPORT_LOCK_FD_OPEN=false
  fi
}

on_exit() {
  local status=$?
  local cleanup_status=0
  trap - EXIT
  trap '' HUP INT TERM
  terminate_active_external
  if cleanup_restore "$status"; then
    cleanup_status=0
  else
    cleanup_status=$?
  fi
  if [[ "$status" -ne 0 || "$cleanup_status" -ne 0 ]]; then
    discard_report_temporary || true
  fi
  if [[ "$status" -ne 0 ]]; then
    release_report_lock || true
    exit "$status"
  fi
  if [[ "$cleanup_status" -ne 0 ]]; then
    release_report_lock || true
    exit "$cleanup_status"
  fi
  if [[ -z "$REPORT_TEMPORARY" ||
    "$REPORT_TEMPORARY" != "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/.restore-${BACKUP_ID}.new" ||
    ! -f "$REPORT_TEMPORARY" || -L "$REPORT_TEMPORARY" ||
    -e "$REPORT_FILE" || -L "$REPORT_FILE" ]] ||
    ! ln "$REPORT_TEMPORARY" "$REPORT_FILE"; then
    discard_report_temporary || true
    release_report_lock || true
    exit 1
  fi
  if ! rm -f "$REPORT_TEMPORARY"; then
    rm -f "$REPORT_FILE" || true
    release_report_lock || true
    exit 1
  fi
  release_report_lock || exit 1
  exit 0
}

main() {
  validate_arguments "$@"
  validate_paths || {
    safe_log 'invalid_restore_paths'
    return 1
  }
  acquire_restore_lock || {
    safe_log 'restore_lock_unavailable'
    return 1
  }
  acquire_report_lock || {
    safe_log 'report_lock_unavailable'
    return 1
  }
  initialize_identity || {
    safe_log 'restore_identity_failed'
    return 1
  }
  trap on_exit EXIT
  install_restore_signal_traps
  initialize_workspace || {
    safe_log 'restore_workspace_failed'
    return 1
  }
  reap_orphan_resources || {
    safe_log 'restore_orphan_reap_failed'
    return 1
  }

  create_network
  create_volume "$POSTGRES_VOLUME" postgres
  create_volume "$AISTOR_VOLUME" aistor
  create_volume "$AISTOR_LICENSE_VOLUME" aistor-license
  assert_new_empty_volume "$POSTGRES_VOLUME" postgres
  assert_new_empty_volume "$AISTOR_VOLUME" aistor
  assert_new_empty_volume "$AISTOR_LICENSE_VOLUME" aistor-license

  repository_check
  local selection snapshot_id selection_extra
  selection="$(select_snapshot)"
  IFS='|' read -r snapshot_id EXPECTED_MANIFEST_SHA256 selection_extra \
    <<<"$selection"
  [[ "$snapshot_id" =~ ^[a-f0-9]{64}$ &&
    "$EXPECTED_MANIFEST_SHA256" =~ ^[a-f0-9]{64}$ &&
    -z "$selection_extra" ]] ||
    return 1
  restore_snapshot "$snapshot_id"
  restore_object_data
  initialize_aistor_license
  start_postgres
  restore_database
  start_dependencies
  revoke_restored_sessions
  start_restored_app
  wait_for_restored_app
  run_restore_check
  load_safe_restore_counts
  run_restore_http_probe
  write_sanitized_report
}

main "$@"
