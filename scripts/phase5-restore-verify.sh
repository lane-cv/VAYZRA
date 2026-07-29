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

BACKUP_ID=''
PROJECT=''
OWNER_TOKEN=''
WORK_DIRECTORY=''
CONTROL_DIRECTORY=''
RESTORE_DIRECTORY=''
REPORT_FILE=''
REPORT_TEMPORARY=''
RESTORE_LOCK_FILE=''
RESTORE_LOCK_FD_OPEN=false
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
  [[ -n "$repository_directory" &&
    -n "$secret_directory" &&
    -n "$control_directory" &&
    -n "$report_directory" &&
    -n "$license_file" ]] ||
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
  safe_mount_source "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" || return 1
  safe_mount_source "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" || return 1
  safe_mount_source "$HAPPYLEARN_RESTORE_CONTROL_DIRECTORY" || return 1
  safe_mount_source "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY" || return 1
  safe_mount_source "$HAPPYLEARN_AISTOR_LICENSE_FILE" || return 1
  owner_only_directory "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" || return 1
  owner_only_directory "$HAPPYLEARN_RESTORE_CONTROL_DIRECTORY" || return 1
  owner_only_directory "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY" || return 1
  [[ "$HAPPYLEARN_RESTORE_CONTROL_DIRECTORY" != \
    "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY" ]] ||
    return 1
  owner_only_secret "$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_password" ||
    return 1
  owner_only_secret "$HAPPYLEARN_AISTOR_LICENSE_FILE" || return 1
  REPORT_FILE="$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/restore-${BACKUP_ID}.json"
  REPORT_TEMPORARY="$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/.restore-${BACKUP_ID}.new"
  RESTORE_LOCK_FILE="$HAPPYLEARN_RESTORE_CONTROL_DIRECTORY/restore-${BACKUP_ID}.lock"
  [[ ! -e "$REPORT_FILE" && ! -L "$REPORT_FILE" &&
    ! -e "$REPORT_TEMPORARY" && ! -L "$REPORT_TEMPORARY" ]] ||
    return 1
}

acquire_restore_lock() {
  [[ "$RESTORE_LOCK_FILE" == \
    "$HAPPYLEARN_RESTORE_CONTROL_DIRECTORY/restore-${BACKUP_ID}.lock" &&
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
    owner_only_secret "$CONTROL_DIRECTORY/restore-check.env"
}

terminate_external_group() {
  local pid="$1"
  kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
  sleep "$POLL_INTERVAL_SECONDS"
  kill -KILL "-$pid" 2>/dev/null || kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

run_bounded() {
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
    if ((SECONDS >= deadline)); then
      terminate_external_group "$pid"
      return 124
    fi
    sleep "$POLL_INTERVAL_SECONDS"
  done
  if wait "$pid"; then
    return 0
  else
    return $?
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
  grep -Fxq "Error: No such $singular: $name" "$path" ||
    grep -Fxq "Error response from daemon: No such $singular: $name" "$path"
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
  if run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
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
    '{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.restore-owner"}}|{{index .Config.Labels "io.happylearn.phase5.restore-kind"}}|{{index .Config.Labels "io.happylearn.phase5.restore-backup-id"}}'
}

inspect_volume_labels() {
  local name="$1"
  inspect_resource_labels volumes "$name" \
    '{{index .Labels "com.docker.compose.project"}}|{{index .Labels "io.happylearn.phase5.restore-owner"}}|{{index .Labels "io.happylearn.phase5.restore-kind"}}|{{index .Labels "io.happylearn.phase5.restore-backup-id"}}'
}

inspect_network_labels() {
  local name="$1"
  inspect_resource_labels networks "$name" \
    '{{index .Labels "com.docker.compose.project"}}|{{index .Labels "io.happylearn.phase5.restore-owner"}}|{{index .Labels "io.happylearn.phase5.restore-kind"}}|{{index .Labels "io.happylearn.phase5.restore-backup-id"}}'
}

list_owned_resource_names() {
  local class="$1"
  local value line
  local -a command
  case "$class" in
    containers) command=(docker container ls --all --quiet) ;;
    volumes) command=(docker volume ls --quiet) ;;
    networks) command=(docker network ls --quiet) ;;
    *) return 1 ;;
  esac
  value="$(
    run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
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

valid_observed_resource() {
  local class="$1"
  local name="$2"
  local labels="$3"
  local project owner kind backup extra
  IFS='|' read -r project owner kind backup extra <<<"$labels"
  [[ -z "$extra" &&
    "$project" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ &&
    "$owner" =~ ^[a-f0-9]{64}$ &&
    "$backup" == "$BACKUP_ID" &&
    "$name" == "$project-"* ]] ||
    return 1
  case "$class:$kind" in
    volumes:postgres | volumes:aistor | volumes:aistor-license | \
      networks:network | \
      containers:volume-probe-postgres | \
      containers:volume-probe-aistor | \
      containers:volume-probe-aistor-license | \
      containers:restic-check | containers:restic-select | \
      containers:restic-restore | containers:object-restore | \
      containers:aistor-license-init | containers:postgres | \
      containers:postgres-restore | containers:aistor | containers:redis | \
      containers:revoke-sessions | containers:app | \
      containers:restore-check | containers:student-one | \
      containers:student-two) ;;
    *) return 1 ;;
  esac
}

remove_owned_orphan() {
  local class="$1"
  local name="$2"
  local labels status
  case "$class" in
    containers)
      if labels="$(inspect_container_labels "$name")"; then
        :
      else
        status=$?
        [[ "$status" -eq 3 ]] && return 0
        return 1
      fi
      valid_observed_resource "$class" "$name" "$labels" || return 1
      run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
        docker rm --force "$name" >/dev/null
      ;;
    volumes)
      if labels="$(inspect_volume_labels "$name")"; then
        :
      else
        status=$?
        [[ "$status" -eq 3 ]] && return 0
        return 1
      fi
      valid_observed_resource "$class" "$name" "$labels" || return 1
      run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
        docker volume rm "$name" >/dev/null
      ;;
    networks)
      if labels="$(inspect_network_labels "$name")"; then
        :
      else
        status=$?
        [[ "$status" -eq 3 ]] && return 0
        return 1
      fi
      valid_observed_resource "$class" "$name" "$labels" || return 1
      run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
        docker network rm "$name" >/dev/null
      ;;
    *) return 1 ;;
  esac
}

reap_orphan_resources() {
  local class names name
  for class in containers volumes networks; do
    names="$(list_owned_resource_names "$class")" || return 1
    while IFS= read -r name; do
      [[ -z "$name" ]] || remove_owned_orphan "$class" "$name" || return 1
    done <<<"$names"
  done
}

owned_resources_absent() {
  local class names
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
  [[ "$(inspect_volume_labels "$name")" == "$(resource_labels "$kind")" ]] ||
    return 1
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
  [[ "$(inspect_network_labels "$NETWORK_NAME")" == \
    "$(resource_labels network)" ]] ||
    return 1
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
    --mount "type=bind,src=$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY,dst=/repository,readonly" \
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
    --mount "type=bind,src=$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY,dst=/repository,readonly" \
    --mount "type=bind,src=$HAPPYLEARN_BACKUP_SECRET_DIRECTORY/local_password,dst=/run/secrets/local_password,readonly" \
    --mount "type=bind,src=$RESTORE_DIRECTORY,dst=/restore" \
    --env RESTIC_REPOSITORY=/repository \
    --env RESTIC_PASSWORD_FILE=/run/secrets/local_password \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    restic --no-cache "$@"
}

repository_check() {
  restic_container \
    "$PROJECT-restic-check" restic-check \
    check --read-data >/dev/null
}

select_snapshot() {
  local inventory="$WORK_DIRECTORY/snapshots.json"
  local inventory_value ids id_count tags tag_count
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
  tags="$(
    grep -Eo 'happylearn-batch:[^"[:space:]]+' "$inventory"
  )" || return 1
  tag_count="$(printf '%s\n' "$tags" |
    grep -Fxc "happylearn-batch:$BACKUP_ID")" || return 1
  [[ "$tag_count" == '1' && "$tags" != *$'\n'* ]] || return 1
  printf '%s' "$ids"
}

restore_snapshot() {
  local snapshot_id="$1"
  [[ "$snapshot_id" =~ ^[a-f0-9]{64}$ ]] || return 1
  restic_restore_container \
    "$PROJECT-restic-restore" restic-restore \
    restore "$snapshot_id" --target /restore >/dev/null
  [[ -f "$RESTORE_DIRECTORY/database.dump" &&
    ! -L "$RESTORE_DIRECTORY/database.dump" &&
    -d "$RESTORE_DIRECTORY/source/aistor" &&
    ! -L "$RESTORE_DIRECTORY/source/aistor" ]]
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
    "UPDATE sessions SET revoked_at=COALESCE(revoked_at,now()), revoke_reason=COALESCE(revoke_reason,'restore_verification'); SELECT 'PHASE5_RESTORE_SESSIONS_REVOKED';" \
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
  run_named_container \
    "$PROJECT-restore-check" restore-check \
    --network "$NETWORK_NAME" \
    --read-only \
    --user "$HOST_UID:$HOST_GID" \
    --mount "type=bind,src=$CONTROL_DIRECTORY,dst=/work" \
    --mount "type=bind,src=$CONTROL_DIRECTORY/pgpass,dst=/run/secrets/pgpass,readonly" \
    --env-file "$CONTROL_DIRECTORY/restore-check.env" \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /app/happylearn-backup restore-check \
    --backup-id "$BACKUP_ID" \
    --report-file /work/restore-check.report \
    >/dev/null
}

run_student_isolation_probe() {
  local index="$1"
  [[ "$index" == 1 || "$index" == 2 ]] || return 1
  run_named_container \
    "$PROJECT-student-$index" "student-$(
      [[ "$index" == 1 ]] && printf one || printf two
    )" \
    --network "$NETWORK_NAME" \
    --read-only \
    --user "$HOST_UID:$HOST_GID" \
    --mount "type=bind,src=$CONTROL_DIRECTORY/pgpass,dst=/run/secrets/pgpass,readonly" \
    --env HAPPYLEARN_DATABASE_HOST=postgres \
    --env HAPPYLEARN_DATABASE_PORT=5432 \
    --env HAPPYLEARN_DATABASE_USER=happylearn \
    --env HAPPYLEARN_DATABASE_NAME=happylearn \
    --env HAPPYLEARN_DATABASE_SSLMODE=disable \
    --env PGPASSFILE=/run/secrets/pgpass \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=10s "${EXTERNAL_TIMEOUT_SECONDS}s" \
    /app/happylearn-backup restore-check \
    --backup-id "$BACKUP_ID" \
    --student-isolation-probe "$index" \
    --expected-status 404 \
    >/dev/null
}

load_safe_restore_counts() {
  local path="$CONTROL_DIRECTORY/restore-check.report"
  local line key value
  local seen_schema=false
  local seen_migration=false
  local seen_rows=false
  local seen_checked=false
  local seen_missing=false
  local seen_unexpected=false
  local seen_sessions=false
  SCHEMA_VERSION=''
  MIGRATION_VERSION=''
  ROW_COUNT_TOTAL=''
  CHECKED_OBJECT_COUNT=''
  MISSING_OBJECT_COUNT=''
  UNEXPECTED_OBJECT_COUNT=''
  ACTIVE_SESSION_COUNT=''
  [[ -f "$path" && ! -L "$path" ]] || return 1
  while IFS= read -r line; do
    [[ "$line" =~ ^[a-z_]+=[0-9]+$ ]] || return 1
    key="${line%%=*}"
    value="${line#*=}"
    case "$key" in
      schema_version)
        [[ "$seen_schema" == false ]] || return 1
        seen_schema=true
        SCHEMA_VERSION="$value"
        ;;
      migration_version)
        [[ "$seen_migration" == false ]] || return 1
        seen_migration=true
        MIGRATION_VERSION="$value"
        ;;
      row_count_total)
        [[ "$seen_rows" == false ]] || return 1
        seen_rows=true
        ROW_COUNT_TOTAL="$value"
        ;;
      checked_object_count)
        [[ "$seen_checked" == false ]] || return 1
        seen_checked=true
        CHECKED_OBJECT_COUNT="$value"
        ;;
      missing_object_count)
        [[ "$seen_missing" == false ]] || return 1
        seen_missing=true
        MISSING_OBJECT_COUNT="$value"
        ;;
      unexpected_object_count)
        [[ "$seen_unexpected" == false ]] || return 1
        seen_unexpected=true
        UNEXPECTED_OBJECT_COUNT="$value"
        ;;
      active_session_count)
        [[ "$seen_sessions" == false ]] || return 1
        seen_sessions=true
        ACTIVE_SESSION_COUNT="$value"
        ;;
      *) return 1 ;;
    esac
  done <"$path"
  [[ "$seen_schema" == true &&
    "$seen_migration" == true &&
    "$seen_rows" == true &&
    "$seen_checked" == true &&
    "$seen_missing" == true &&
    "$seen_unexpected" == true &&
    "$seen_sessions" == true &&
    "$SCHEMA_VERSION" == 1 &&
    "$MIGRATION_VERSION" =~ ^[1-9][0-9]*$ &&
    "$ROW_COUNT_TOTAL" =~ ^[0-9]+$ &&
    "$CHECKED_OBJECT_COUNT" =~ ^[0-9]+$ &&
    "$MISSING_OBJECT_COUNT" == 0 &&
    "$UNEXPECTED_OBJECT_COUNT" == 0 &&
    "$ACTIVE_SESSION_COUNT" == 0 ]]
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
  [[ "$duration" -ge 0 && "$duration" -lt "$RTO_LIMIT_SECONDS" ]] ||
    return 1
  printf '%s\n' \
    "schemaVersion=1" \
    "backupId=$BACKUP_ID" \
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
    "{\"schemaVersion\":1,\"backupId\":\"$BACKUP_ID\",\"durationSeconds\":$duration,\"migrationVersion\":$MIGRATION_VERSION,\"rowCountTotal\":$ROW_COUNT_TOTAL,\"checkedObjectCount\":$CHECKED_OBJECT_COUNT,\"missingObjectCount\":$MISSING_OBJECT_COUNT,\"unexpectedObjectCount\":$UNEXPECTED_OBJECT_COUNT,\"activeSessionCount\":$ACTIVE_SESSION_COUNT,\"isolation404ProbeCount\":2,\"reportSHA256\":\"$report_sha256\"}" \
    >"$REPORT_TEMPORARY"
  chmod 0600 "$REPORT_TEMPORARY"
}

cleanup_container() {
  local name="$1"
  local kind="$2"
  local labels status
  cleanup_intended containers "$name" "$kind" || return 0
  if labels="$(inspect_container_labels "$name")"; then
    :
  else
    status=$?
    [[ "$status" -eq 3 ]] && return 0
    return 1
  fi
  [[ "$labels" == "$(resource_labels "$kind")" ]] || return 1
  run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
    docker rm --force "$name" >/dev/null
}

cleanup_volume() {
  local name="$1"
  local kind="$2"
  local labels status
  cleanup_intended volumes "$name" "$kind" || return 0
  if labels="$(inspect_volume_labels "$name")"; then
    :
  else
    status=$?
    [[ "$status" -eq 3 ]] && return 0
    return 1
  fi
  [[ "$labels" == "$(resource_labels "$kind")" ]] || return 1
  run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
    docker volume rm "$name" >/dev/null
}

cleanup_network() {
  local labels status
  cleanup_intended networks "$NETWORK_NAME" network || return 0
  if labels="$(inspect_network_labels "$NETWORK_NAME")"; then
    :
  else
    status=$?
    [[ "$status" -eq 3 ]] && return 0
    return 1
  fi
  [[ "$labels" == "$(resource_labels network)" ]] || return 1
  run_bounded "$EXTERNAL_TIMEOUT_SECONDS" \
    docker network rm "$NETWORK_NAME" >/dev/null
}

cleanup_restore() {
  local status="${1:-0}"
  local name kind
  if [[ "$CLEANING_UP" == true ]]; then
    return "$status"
  fi
  CLEANING_UP=true
  if valid_project "$PROJECT" &&
    valid_owner_token "$OWNER_TOKEN" &&
    [[ -f "$CLEANUP_INTENT_LEDGER" &&
      ! -L "$CLEANUP_INTENT_LEDGER" ]]; then
    while IFS='|' read -r name kind; do
      [[ -n "$name" && -n "$kind" ]] || continue
      cleanup_container "$name" "$kind" || status=1
    done <<EOF
$PROJECT-student-2|student-two
$PROJECT-student-1|student-one
$PROJECT-restore-check|restore-check
$PROJECT-app|app
$PROJECT-revoke-sessions|revoke-sessions
$PROJECT-redis|redis
$PROJECT-aistor|aistor
$PROJECT-postgres-restore|postgres-restore
$PROJECT-postgres|postgres
$PROJECT-object-restore|object-restore
$PROJECT-aistor-license-init|aistor-license-init
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

on_exit() {
  local status=$?
  local cleanup_status=0
  trap - EXIT HUP INT TERM
  if cleanup_restore "$status"; then
    cleanup_status=0
  else
    cleanup_status=$?
  fi
  if [[ "$status" -ne 0 || "$cleanup_status" -ne 0 ]]; then
    discard_report_temporary || true
  fi
  if [[ "$status" -ne 0 ]]; then
    exit "$status"
  fi
  if [[ "$cleanup_status" -ne 0 ]]; then
    exit "$cleanup_status"
  fi
  if [[ -z "$REPORT_TEMPORARY" ||
    "$REPORT_TEMPORARY" != "$HAPPYLEARN_RESTORE_REPORT_DIRECTORY/.restore-${BACKUP_ID}.new" ||
    ! -f "$REPORT_TEMPORARY" || -L "$REPORT_TEMPORARY" ||
    -e "$REPORT_FILE" || -L "$REPORT_FILE" ]] ||
    ! mv "$REPORT_TEMPORARY" "$REPORT_FILE"; then
    discard_report_temporary || true
    exit 1
  fi
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
  initialize_identity || {
    safe_log 'restore_identity_failed'
    return 1
  }
  trap on_exit EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
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
  local snapshot_id
  snapshot_id="$(select_snapshot)"
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
  run_student_isolation_probe 1
  run_student_isolation_probe 2
  write_sanitized_report
}

main "$@"
