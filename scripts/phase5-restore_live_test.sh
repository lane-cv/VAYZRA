#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
umask 077

readonly DEFAULT_LICENSE_SOURCE='/Users/lane/Downloads/minio.license'
readonly RTO_LIMIT_SECONDS=14400
readonly TEACHER_USERNAME='phase5_restore_teacher'
readonly TEACHER_DISPLAY_NAME='Phase 5 Restore Teacher'
readonly CONTROLLER_SOCKET_GID='0'
readonly CONTROLLER_SOCKET_MODE='660'
readonly CONTROLLER_STOP_TIMEOUT_SECONDS=30
readonly CONTROLLER_WAIT_TERM_GRACE_SECONDS=5

CONTROLLER_WAIT_LIMIT_SECONDS="${HAPPYLEARN_PHASE5_RESTORE_LIVE_CONTROLLER_TIMEOUT_SECONDS:-1200}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
COMPOSE_FILE="$ROOT/deploy/compose.dev.yml"
COMPOSE_LIVE_FILE="$ROOT/deploy/compose.backup-live.yml"
CONTROLLER_DOCKERFILE="$ROOT/deploy/Dockerfile.restore-live-controller"
BACKUP_SCRIPT="$ROOT/scripts/phase5-backup.sh"
RESTORE_SCRIPT="$ROOT/scripts/phase5-restore-verify.sh"
LICENSE_SOURCE="$DEFAULT_LICENSE_SOURCE"

PROJECT=''
FIXTURE_ROOT=''
FIXTURE_SUFFIX=''
LICENSE_COPY=''
TEACHER_PASSWORD_FILE=''
TEACHER_CREDENTIAL_FILE=''
BACKUP_LOG=''
RESTORE_LOG=''
REPORT_FILE=''
RESTORE_STAGE_FILE=''
BACKUP_ID=''
EXPECTED_MANIFEST_SHA256=''
BACKUP_IMAGE=''
APP_IMAGE=''
WORKER_IMAGE=''
CONTROLLER_IMAGE=''
FIXTURE_IMAGE=''
CONTROLLER_NAME=''
CONTROLLER_ID=''
CONTROLLER_CREATED=false
CONTROLLER_WAIT_PID=''
CONTROLLER_EXIT_STATUS=''
CONTROLLER_CID_FILE=''
CONTROLLER_WAIT_STATUS_FILE=''
DOCKER_SOCKET=''
CREATED_IMAGE_RECORD=''
FIXTURE_CONFIG_FILE=''
FIXTURE_ORIGINAL_FILE=''
FIXTURE_PREVIEW_FILE=''
FIXTURE_OBJECT_LOG=''
FIXTURE_ORIGINAL_KEY=''
FIXTURE_PREVIEW_KEY=''
FIXTURE_ORIGINAL_SHA256=''
FIXTURE_PREVIEW_SHA256=''
FIXTURE_ORIGINAL_SIZE=''
FIXTURE_PREVIEW_SIZE=''
SOURCE_MINIO_ACCESS_KEY_FILE=''
SOURCE_MINIO_SECRET_KEY_FILE=''
SOURCE_MINIO_ACCESS_KEY=''
SOURCE_MINIO_SECRET_KEY=''
HOST_UID=''
HOST_GID=''
REPOSITORY_HOST_HANDOFF=false
CLEANED=false

LIVE_REPORT_DURATION_SECONDS=''
LIVE_REPORT_VERIFICATION_ID=''
LIVE_REPORT_MIGRATION_VERSION=''
LIVE_REPORT_DATABASE_ROW_COUNTS=''
LIVE_REPORT_ROW_COUNT_TOTAL=''
LIVE_REPORT_CHECKED_OBJECT_COUNT=''
LIVE_REPORT_ISOLATION_404_PROBE_COUNT=''
LIVE_REPORT_MANIFEST_SHA256=''
LIVE_REPORT_VERIFICATION_SHA256=''
LIVE_REPORT_EVIDENCE_SHA256=''
LIVE_REPORT_SHA256=''

fail() {
  printf 'phase5 restore live: %s\n' "$1" >&2
  exit 1
}

valid_uuid() {
  [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]]
}

valid_sha256() {
  [[ "$1" =~ ^[a-f0-9]{64}$ ]]
}

valid_restore_stage() {
  case "$1" in
    wrapper_preflight | verifier_started | paths_validate_started | \
      restore_lock_started | report_lock_started | identity_init_started | \
      workspace_init_started | orphan_reap_started | \
      network_create_started | volume_create_started | \
      repository_check_started | snapshot_select_started | \
      snapshot_restore_started | object_restore_started | \
      license_init_started | postgres_start_started | \
      database_restore_started | dependencies_start_started | \
      sessions_revoke_started | app_start_started | \
      app_ready_wait_started | restore_check_started | \
      counts_load_started | http_probe_started | report_publish_started)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

valid_nonnegative_int64() {
  local value="$1"
  [[ "$value" =~ ^(0|[1-9][0-9]{0,18})$ ]] || return 1
  if [[ "${#value}" -eq 19 ]]; then
    [[ "$value" < '9223372036854775808' ]]
  fi
}

portable_mode() {
  local path="$1"
  if stat -c '%a' "$path" >/dev/null 2>&1; then
    stat -c '%a' "$path"
  else
    stat -f '%Lp' "$path"
  fi
}

portable_owner() {
  local path="$1"
  if stat -c '%u' "$path" >/dev/null 2>&1; then
    stat -c '%u' "$path"
  else
    stat -f '%u' "$path"
  fi
}

portable_size() {
  local path="$1"
  if stat -c '%s' "$path" >/dev/null 2>&1; then
    stat -c '%s' "$path"
  else
    stat -f '%z' "$path"
  fi
}

portable_sha256_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256
  else
    return 1
  fi
}

portable_sha256_file() {
  local path="$1"
  portable_sha256_stdin <"$path" |
    sed -n '1s/[[:space:]].*$//p'
}

owner_private_regular_file() {
  local path="$1"
  local expected_mode="$2"
  local maximum_size="$3"
  local size
  [[ -f "$path" && ! -L "$path" &&
    "$(portable_mode "$path")" == "$expected_mode" &&
    "$(portable_owner "$path")" == "$(id -u)" ]] ||
    return 1
  size="$(portable_size "$path")" || return 1
  [[ "$size" -ge 1 && "$size" -le "$maximum_size" ]]
}

owner_only_regular_file() {
  owner_private_regular_file "$1" 400 "$2"
}

valid_license_source() {
  local path="$1"
  local mode size
  [[ "$path" == /* && -f "$path" && ! -L "$path" ]] ||
    return 1
  mode="$(portable_mode "$path")" || return 1
  [[ "$mode" == 400 || "$mode" == 600 ]] || return 1
  [[ "$(portable_owner "$path")" == "$(id -u)" ]] || return 1
  size="$(portable_size "$path")" || return 1
  [[ "$size" -ge 1 && "$size" -le 65536 ]]
}

compose() {
  docker compose \
    --project-name "$PROJECT" \
    --file "$COMPOSE_FILE" \
    --file "$COMPOSE_LIVE_FILE" \
    "$@"
}

parse_sanitized_report() {
  local path="$1"
  local expected_backup_id="$2"
  local expected_manifest_sha256="$3"
  local content pattern
  local verification_id backup_id manifest_sha256
  local verification_sha256 evidence_sha256 database_counts
  local duration migration row_total checked missing unexpected active
  local isolation report_sha256 calculated_sha256

  valid_uuid "$expected_backup_id" || return 1
  valid_sha256 "$expected_manifest_sha256" || return 1
  [[ -f "$path" && ! -L "$path" &&
    "$(portable_mode "$path")" == 600 &&
    "$(portable_owner "$path")" == "$(id -u)" &&
    "$(portable_size "$path")" -ge 1 &&
    "$(portable_size "$path")" -le 4096 &&
    "$(wc -l <"$path" | tr -d '[:space:]')" == 1 ]] ||
    return 1

  content="$(<"$path")"
  pattern='^\{"schemaVersion":2,"verificationId":"([0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})","backupId":"([0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})","manifestSHA256":"([a-f0-9]{64})","verificationReportSHA256":"([a-f0-9]{64})","evidenceSHA256":"([a-f0-9]{64})","durationSeconds":(0|[1-9][0-9]*),"migrationVersion":(0|[1-9][0-9]*),"databaseRowCounts":(\{"users":[0-9]+,"sessions":[0-9]+,"subjects":[0-9]+,"grades":[0-9]+,"terms":[0-9]+,"chapters":[0-9]+,"lessons":[0-9]+,"lesson_revisions":[0-9]+,"files":[0-9]+,"file_versions":[0-9]+,"file_previews":[0-9]+,"qa_threads":[0-9]+,"qa_messages":[0-9]+,"ai_threads":[0-9]+,"ai_messages":[0-9]+,"ai_runs":[0-9]+\}),"rowCountTotal":(0|[1-9][0-9]*),"checkedObjectCount":(0|[1-9][0-9]*),"missingObjectCount":(0|[1-9][0-9]*),"unexpectedObjectCount":(0|[1-9][0-9]*),"activeSessionCount":(0|[1-9][0-9]*),"isolation404ProbeCount":(0|[1-9][0-9]*),"reportSHA256":"([a-f0-9]{64})"\}$'
  [[ "$content" =~ $pattern ]] || return 1

  verification_id="${BASH_REMATCH[1]}"
  backup_id="${BASH_REMATCH[2]}"
  manifest_sha256="${BASH_REMATCH[3]}"
  verification_sha256="${BASH_REMATCH[4]}"
  evidence_sha256="${BASH_REMATCH[5]}"
  duration="${BASH_REMATCH[6]}"
  migration="${BASH_REMATCH[7]}"
  database_counts="${BASH_REMATCH[8]}"
  row_total="${BASH_REMATCH[9]}"
  checked="${BASH_REMATCH[10]}"
  missing="${BASH_REMATCH[11]}"
  unexpected="${BASH_REMATCH[12]}"
  active="${BASH_REMATCH[13]}"
  isolation="${BASH_REMATCH[14]}"
  report_sha256="${BASH_REMATCH[15]}"

  valid_uuid "$verification_id" || return 1
  [[ "$backup_id" == "$expected_backup_id" &&
    "$manifest_sha256" == "$expected_manifest_sha256" ]] ||
    return 1
  valid_sha256 "$verification_sha256" || return 1
  valid_sha256 "$evidence_sha256" || return 1
  valid_sha256 "$report_sha256" || return 1
  valid_nonnegative_int64 "$duration" || return 1
  valid_nonnegative_int64 "$migration" || return 1
  valid_nonnegative_int64 "$row_total" || return 1
  valid_nonnegative_int64 "$checked" || return 1
  valid_nonnegative_int64 "$missing" || return 1
  valid_nonnegative_int64 "$unexpected" || return 1
  valid_nonnegative_int64 "$active" || return 1
  valid_nonnegative_int64 "$isolation" || return 1
  [[ "$duration" -lt "$RTO_LIMIT_SECONDS" &&
    "$migration" -ge 20 &&
    "$row_total" -ge 1 &&
    "$checked" -ge 2 &&
    "$missing" == 0 &&
    "$unexpected" == 0 &&
    "$active" == 0 &&
    "$isolation" == 2 ]] ||
    return 1

  calculated_sha256="$(
    printf '%s\n' \
      'schemaVersion=2' \
      "verificationId=$verification_id" \
      "backupId=$backup_id" \
      "manifestSHA256=$manifest_sha256" \
      "verificationReportSHA256=$verification_sha256" \
      "evidenceSHA256=$evidence_sha256" \
      "durationSeconds=$duration" \
      "migrationVersion=$migration" \
      "databaseRowCounts=$database_counts" \
      "rowCountTotal=$row_total" \
      "checkedObjectCount=$checked" \
      "missingObjectCount=$missing" \
      "unexpectedObjectCount=$unexpected" \
      "activeSessionCount=$active" \
      "isolation404ProbeCount=$isolation" |
      portable_sha256_stdin |
      sed -n '1s/[[:space:]].*$//p'
  )"
  [[ "$calculated_sha256" == "$report_sha256" ]] || return 1

  LIVE_REPORT_VERIFICATION_ID="$verification_id"
  LIVE_REPORT_DURATION_SECONDS="$duration"
  LIVE_REPORT_MIGRATION_VERSION="$migration"
  LIVE_REPORT_DATABASE_ROW_COUNTS="$database_counts"
  LIVE_REPORT_ROW_COUNT_TOTAL="$row_total"
  LIVE_REPORT_CHECKED_OBJECT_COUNT="$checked"
  LIVE_REPORT_ISOLATION_404_PROBE_COUNT="$isolation"
  LIVE_REPORT_MANIFEST_SHA256="$manifest_sha256"
  LIVE_REPORT_VERIFICATION_SHA256="$verification_sha256"
  LIVE_REPORT_EVIDENCE_SHA256="$evidence_sha256"
  LIVE_REPORT_SHA256="$report_sha256"
}

assert_secret_absent() {
  local password_file="$1"
  local credential_file="$2"
  shift 2
  local password credential artifact
  owner_private_regular_file "$password_file" 600 4096 || return 1
  owner_only_regular_file "$credential_file" 4096 || return 1
  password="$(<"$password_file")"
  credential="$(<"$credential_file")"
  [[ -n "$password" && -n "$credential" ]] || return 1
  for artifact in "$@"; do
    [[ -f "$artifact" && ! -L "$artifact" ]] || return 1
    if grep -Fq -- "$password" "$artifact" ||
      grep -Fq -- "$credential" "$artifact"; then
      return 1
    fi
  done
}

assert_single_line_secret_absent() {
  local secret_file="$1"
  shift
  local secret artifact size
  [[ -f "$secret_file" && ! -L "$secret_file" ]] || return 1
  size="$(portable_size "$secret_file")" || return 1
  [[ "$size" -ge 1 && "$size" -le 4096 ]] || return 1
  secret="$(<"$secret_file")"
  [[ -n "$secret" &&
    "$secret" != *$'\n'* &&
    "$secret" != *$'\r'* ]] ||
    return 1
  for artifact in "$@"; do
    [[ -f "$artifact" && ! -L "$artifact" ]] || return 1
    if grep -Fq -- "$secret" "$artifact"; then
      return 1
    fi
  done
}

assert_age_identity_absent() {
  local identity_file="$1"
  shift
  local private_key artifact
  [[ -f "$identity_file" && ! -L "$identity_file" ]] || return 1
  private_key="$(
    sed -n '/^AGE-SECRET-KEY-[0-9A-Z]*$/p' "$identity_file"
  )"
  [[ "$private_key" =~ ^AGE-SECRET-KEY-[0-9A-Z]+$ ]] || return 1
  for artifact in "$@"; do
    [[ -f "$artifact" && ! -L "$artifact" ]] || return 1
    if grep -Fq -- "$private_key" "$artifact"; then
      return 1
    fi
  done
}

extract_restore_failure_category() {
  local log_file="$1"
  local category
  [[ -f "$log_file" && ! -L "$log_file" ]] || return 1
  category="$(
    sed -n \
      's/^phase5_restore: \([a-z0-9_][a-z0-9_]*\)$/\1/p' \
      "$log_file" |
      sed -n '$p'
  )"
  [[ "$category" =~ ^[a-z0-9_]{1,64}$ ]] || return 1
  printf '%s\n' "$category"
}

assert_restore_resources_absent() {
  local backup_id="$1"
  local class resources
  valid_uuid "$backup_id" || return 1
  for class in containers volumes networks; do
    case "$class" in
      containers)
        resources="$(
          docker ps -aq \
            --filter \
            "label=io.happylearn.phase5.restore-backup-id=${backup_id}"
        )" || return 1
        ;;
      volumes)
        resources="$(
          docker volume ls --quiet \
            --filter \
            "label=io.happylearn.phase5.restore-backup-id=${backup_id}"
        )" || return 1
        ;;
      networks)
        resources="$(
          docker network ls --quiet \
            --filter \
            "label=io.happylearn.phase5.restore-backup-id=${backup_id}"
        )" || return 1
        ;;
    esac
    [[ -z "$resources" ]] || return 1
  done
}

require_dependencies() {
  command -v docker >/dev/null || fail 'docker is required'
  command -v uuidgen >/dev/null || fail 'uuidgen is required'
  command -v sed >/dev/null || fail 'sed is required'
  portable_sha256_stdin </dev/null >/dev/null ||
    fail 'sha256sum or shasum is required'
  valid_nonnegative_int64 "$CONTROLLER_WAIT_LIMIT_SECONDS" &&
    [[ "$CONTROLLER_WAIT_LIMIT_SECONDS" -ge 1 &&
      "$CONTROLLER_WAIT_LIMIT_SECONDS" -lt "$RTO_LIMIT_SECONDS" ]] ||
    fail 'controller wait limit must be positive and below the restore RTO'
  docker compose version >/dev/null ||
    fail 'Docker Compose v2 is required'
  [[ -f "$COMPOSE_FILE" && ! -L "$COMPOSE_FILE" &&
    -f "$COMPOSE_LIVE_FILE" && ! -L "$COMPOSE_LIVE_FILE" &&
    -f "$CONTROLLER_DOCKERFILE" && ! -L "$CONTROLLER_DOCKERFILE" &&
    -f "$BACKUP_SCRIPT" && ! -L "$BACKUP_SCRIPT" &&
    -f "$RESTORE_SCRIPT" && ! -L "$RESTORE_SCRIPT" ]] ||
    fail 'fixed Phase 5 inputs are unavailable'

  local docker_endpoint
  docker_endpoint="$(
    docker context inspect --format '{{.Endpoints.docker.Host}}'
  )" || fail 'Docker context endpoint is unavailable'
  [[ "$docker_endpoint" == unix:///* &&
    "$docker_endpoint" != *$'\n'* &&
    "$docker_endpoint" != *$'\r'* ]] ||
    fail 'the live gate requires one local Unix Docker context'
  DOCKER_SOCKET="${docker_endpoint#unix://}"
  [[ -S "$DOCKER_SOCKET" && ! -L "$DOCKER_SOCKET" ]] ||
    fail 'Docker context socket is not a direct Unix socket'
  [[ "$(portable_owner "$DOCKER_SOCKET")" == "$(id -u)" ]] ||
    fail 'Docker context socket is not owned by the live-gate caller'

  LICENSE_SOURCE="$(
    printf '%s' \
      "${HAPPYLEARN_PHASE5_RESTORE_LIVE_LICENSE_SOURCE:-$DEFAULT_LICENSE_SOURCE}"
  )"
  valid_license_source "$LICENSE_SOURCE" ||
    fail 'AIStor license source must be owner-only mode 0400 or 0600'
}

new_uuid() {
  local value
  value="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  valid_uuid "$value" ||
    fail 'uuidgen did not return a canonical v4 UUID'
  printf '%s\n' "$value"
}

image_reference_is_expected() {
  local reference="$1"
  [[ -n "$reference" &&
    "$reference" != *'|'* &&
    ("$reference" == "$BACKUP_IMAGE" ||
      "$reference" == "$APP_IMAGE" ||
      "$reference" == "$WORKER_IMAGE" ||
      "$reference" == "$CONTROLLER_IMAGE" ||
      "$reference" == "$FIXTURE_IMAGE") ]]
}

inspect_image_reference_id() {
  local reference="$1"
  local image_id listed_ids
  image_reference_is_expected "$reference" || return 1
  if image_id="$(
    docker image inspect --format '{{.Id}}' "$reference" 2>/dev/null
  )"; then
    [[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] || return 1
    printf '%s\n' "$image_id"
    return 0
  fi
  listed_ids="$(
    docker image ls --quiet --no-trunc \
      --filter "reference=$reference"
  )" || return 1
  [[ -z "$listed_ids" ]] && return 2
  return 1
}

docker_container_name_absent() {
  local name="$1"
  local matches
  [[ "$name" =~ ^[a-z0-9][a-z0-9_.-]{0,127}$ ]] || return 1
  matches="$(
    docker container ls --all --quiet \
      --filter "name=^/${name}$"
  )" || return 1
  [[ -z "$matches" ]]
}

docker_container_id_absent() {
  local container_id="$1"
  local matches
  [[ "$container_id" =~ ^[a-f0-9]{64}$ ]] || return 1
  matches="$(
    docker container ls --all --quiet --no-trunc \
      --filter "id=$container_id"
  )" || return 1
  [[ -z "$matches" ]]
}

pre_register_image_references() {
  local reference baseline_id inspect_status
  [[ -f "$CREATED_IMAGE_RECORD" &&
    ! -L "$CREATED_IMAGE_RECORD" &&
    "$(portable_mode "$CREATED_IMAGE_RECORD")" == 600 &&
    "$(portable_owner "$CREATED_IMAGE_RECORD")" == "$(id -u)" &&
    ! -s "$CREATED_IMAGE_RECORD" ]] ||
    return 1
  for reference in \
    "$BACKUP_IMAGE" "$APP_IMAGE" "$WORKER_IMAGE" \
    "$CONTROLLER_IMAGE" "$FIXTURE_IMAGE"; do
    image_reference_is_expected "$reference" || return 1
    inspect_status=0
    baseline_id="$(inspect_image_reference_id "$reference")" ||
      inspect_status=$?
    case "$inspect_status" in
      0) ;;
      2) baseline_id=absent ;;
      *) return 1 ;;
    esac
    printf '%s|%s|pending\n' "$reference" "$baseline_id" \
      >>"$CREATED_IMAGE_RECORD"
  done
  [[ "$(wc -l <"$CREATED_IMAGE_RECORD" | tr -d '[:space:]')" == 5 ]]
}

create_fixture() {
  local fixture_uuid compact_uuid
  local repository_password teacher_password
  local existing_containers existing_volumes existing_networks
  fixture_uuid="$(new_uuid)"
  compact_uuid="${fixture_uuid//-/}"
  FIXTURE_SUFFIX="${compact_uuid:0:12}"
  [[ "$FIXTURE_SUFFIX" =~ ^[a-f0-9]{12}$ ]] ||
    fail 'fixture suffix is invalid'
  PROJECT="happylearn-phase5-live-${FIXTURE_SUFFIX}"
  FIXTURE_ROOT="$(mktemp -d \
    "${TMPDIR:-/tmp}/phase5-restore-live.XXXXXX")"
  [[ -d "$FIXTURE_ROOT" && ! -L "$FIXTURE_ROOT" &&
    "$(portable_mode "$FIXTURE_ROOT")" == 700 &&
    "$(portable_owner "$FIXTURE_ROOT")" == "$(id -u)" ]] ||
    fail 'fixture root is not owner-only'

  LICENSE_COPY="$FIXTURE_ROOT/secrets/aistor.license"
  TEACHER_PASSWORD_FILE="$FIXTURE_ROOT/secrets/teacher-password"
  TEACHER_CREDENTIAL_FILE="$FIXTURE_ROOT/secrets/restore-probe-teacher.json"
  BACKUP_LOG="$FIXTURE_ROOT/logs/backup.log"
  RESTORE_LOG="$FIXTURE_ROOT/logs/restore.log"
  REPORT_FILE="$FIXTURE_ROOT/report/restore.pending.json"
  RESTORE_STAGE_FILE="$FIXTURE_ROOT/controller-tmp/restore.stage"
  BACKUP_IMAGE="happylearn-backup:phase5-restore-live-${FIXTURE_SUFFIX}"
  APP_IMAGE="${PROJECT}-app"
  WORKER_IMAGE="${PROJECT}-worker"
  CONTROLLER_IMAGE="happylearn-restore-controller:phase5-live-${FIXTURE_SUFFIX}"
  FIXTURE_IMAGE="happylearn-restore-live-fixture:phase5-live-${FIXTURE_SUFFIX}"
  CONTROLLER_NAME="${PROJECT}-restore-controller"
  CONTROLLER_CID_FILE="$FIXTURE_ROOT/control/controller.cid"
  CONTROLLER_WAIT_STATUS_FILE="$FIXTURE_ROOT/control/controller.wait"
  CREATED_IMAGE_RECORD="$FIXTURE_ROOT/created-images"
  FIXTURE_CONFIG_FILE="$FIXTURE_ROOT/secrets/restore-live-object-fixture.json"
  FIXTURE_ORIGINAL_FILE="$FIXTURE_ROOT/offline/original.bin"
  FIXTURE_PREVIEW_FILE="$FIXTURE_ROOT/offline/preview.bin"
  FIXTURE_OBJECT_LOG="$FIXTURE_ROOT/logs/object-fixture.log"
  FIXTURE_ORIGINAL_KEY="phase5-restore-live/${FIXTURE_SUFFIX}/original.bin"
  FIXTURE_PREVIEW_KEY="phase5-restore-live/${FIXTURE_SUFFIX}/preview.bin"
  SOURCE_MINIO_ACCESS_KEY_FILE="$FIXTURE_ROOT/secrets/source_minio_access_key"
  SOURCE_MINIO_SECRET_KEY_FILE="$FIXTURE_ROOT/secrets/source_minio_secret_key"
  HOST_UID="$(id -u)"
  HOST_GID="$(id -g)"

  mkdir -m 0700 \
    "$FIXTURE_ROOT/secrets" \
    "$FIXTURE_ROOT/repository" \
    "$FIXTURE_ROOT/state" \
    "$FIXTURE_ROOT/control" \
    "$FIXTURE_ROOT/report" \
    "$FIXTURE_ROOT/offline" \
    "$FIXTURE_ROOT/logs" \
    "$FIXTURE_ROOT/controller-tmp"
  : >"$CREATED_IMAGE_RECORD"
  chmod 0600 "$CREATED_IMAGE_RECORD"
  pre_register_image_references ||
    fail 'could not pre-register randomized live fixture images'
  existing_containers="$(
    docker ps -aq \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )" || fail 'container collision scan failed'
  existing_volumes="$(
    docker volume ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )" || fail 'volume collision scan failed'
  existing_networks="$(
    docker network ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )" || fail 'network collision scan failed'
  if [[ -n "$existing_containers" ||
    -n "$existing_volumes" ||
    -n "$existing_networks" ]] ||
    grep -Evq '\|absent\|pending$' "$CREATED_IMAGE_RECORD" ||
    ! docker_container_name_absent "$CONTROLLER_NAME"; then
    fail 'randomized live fixture collided with an existing resource'
  fi
  install -m 0400 "$LICENSE_SOURCE" "$LICENSE_COPY"
  repository_password="${compact_uuid}$(
    new_uuid |
      tr -d '-'
  )"
  teacher_password="Phase5 Restore ${compact_uuid}!"
  SOURCE_MINIO_ACCESS_KEY="restore${compact_uuid:0:16}"
  SOURCE_MINIO_SECRET_KEY="${compact_uuid}$(
    new_uuid |
      tr -d '-'
  )"
  printf '%s\n' 'happylearn_dev' \
    >"$FIXTURE_ROOT/secrets/database_password"
  printf '%s\n' '/repository' \
    >"$FIXTURE_ROOT/secrets/local_repository"
  printf '%s\n' "$repository_password" \
    >"$FIXTURE_ROOT/secrets/local_password"
  printf '%s\n' "$teacher_password" >"$TEACHER_PASSWORD_FILE"
  printf '%s\n' "$SOURCE_MINIO_ACCESS_KEY" >"$SOURCE_MINIO_ACCESS_KEY_FILE"
  printf '%s\n' "$SOURCE_MINIO_SECRET_KEY" >"$SOURCE_MINIO_SECRET_KEY_FILE"
  printf '%s' 'phase5 restore original fixture v1' >"$FIXTURE_ORIGINAL_FILE"
  printf '%s' 'phase5 restore preview fixture v1' >"$FIXTURE_PREVIEW_FILE"
  FIXTURE_ORIGINAL_SIZE="$(portable_size "$FIXTURE_ORIGINAL_FILE")"
  FIXTURE_PREVIEW_SIZE="$(portable_size "$FIXTURE_PREVIEW_FILE")"
  FIXTURE_ORIGINAL_SHA256="$(portable_sha256_file "$FIXTURE_ORIGINAL_FILE")"
  FIXTURE_PREVIEW_SHA256="$(portable_sha256_file "$FIXTURE_PREVIEW_FILE")"
  valid_nonnegative_int64 "$FIXTURE_ORIGINAL_SIZE" &&
    valid_nonnegative_int64 "$FIXTURE_PREVIEW_SIZE" &&
    [[ "$FIXTURE_ORIGINAL_SIZE" -ge 1 &&
      "$FIXTURE_PREVIEW_SIZE" -ge 1 ]] &&
    valid_sha256 "$FIXTURE_ORIGINAL_SHA256" &&
    valid_sha256 "$FIXTURE_PREVIEW_SHA256" ||
    fail 'source object fixture evidence is invalid'
  printf '{"schemaVersion":1,"endpoint":"minio:9000","accessKey":"%s","secretKey":"%s","originalKey":"%s","previewKey":"%s"}\n' \
    "$SOURCE_MINIO_ACCESS_KEY" \
    "$SOURCE_MINIO_SECRET_KEY" \
    "$FIXTURE_ORIGINAL_KEY" \
    "$FIXTURE_PREVIEW_KEY" \
    >"$FIXTURE_CONFIG_FILE"
  printf '{"username":"%s","password":"%s"}\n' \
    "$TEACHER_USERNAME" "$teacher_password" >"$TEACHER_CREDENTIAL_FILE"
  chmod 0400 \
    "$FIXTURE_ROOT/secrets/database_password" \
    "$FIXTURE_ROOT/secrets/local_repository" \
    "$FIXTURE_ROOT/secrets/local_password" \
    "$SOURCE_MINIO_ACCESS_KEY_FILE" \
    "$SOURCE_MINIO_SECRET_KEY_FILE" \
    "$FIXTURE_CONFIG_FILE" \
    "$FIXTURE_ORIGINAL_FILE" \
    "$FIXTURE_PREVIEW_FILE"
  chmod 0600 "$TEACHER_PASSWORD_FILE"
  chmod 0400 "$TEACHER_CREDENTIAL_FILE"
  owner_only_regular_file "$LICENSE_COPY" 65536 ||
    fail 'fixture AIStor license is not owner-only'
  owner_private_regular_file "$TEACHER_PASSWORD_FILE" 600 4096 ||
    fail 'teacher password file does not meet the admin CLI contract'
  owner_only_regular_file "$TEACHER_CREDENTIAL_FILE" 4096 ||
    fail 'teacher credential file is not owner-only'
  : >"$BACKUP_LOG"
  : >"$RESTORE_LOG"
  : >"$FIXTURE_OBJECT_LOG"
  chmod 0600 \
    "$BACKUP_LOG" \
    "$RESTORE_LOG" \
    "$FIXTURE_OBJECT_LOG" \
    "$CREATED_IMAGE_RECORD"
}

record_image() {
  local reference="$1"
  local image_id record_tmp
  local record_reference baseline_id expected_id extra
  local updated=0
  image_reference_is_expected "$reference" || return 1
  image_id="$(
    docker image inspect --format '{{.Id}}' "$reference"
  )" || fail 'built image ID is unavailable'
  [[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] ||
    fail 'built image ID is invalid'
  record_tmp="$FIXTURE_ROOT/control/.created-images.new"
  [[ ! -e "$record_tmp" && ! -L "$record_tmp" ]] || return 1
  : >"$record_tmp"
  chmod 0600 "$record_tmp"
  while IFS='|' read -r \
      record_reference baseline_id expected_id extra; do
    [[ -z "$extra" &&
      "$baseline_id" == absent &&
      ("$expected_id" == pending ||
        "$expected_id" =~ ^sha256:[a-f0-9]{64}$) ]] &&
      image_reference_is_expected "$record_reference" || {
      rm -f "$record_tmp"
      return 1
    }
    if [[ "$record_reference" == "$reference" ]]; then
      [[ "$expected_id" == pending && "$updated" == 0 ]] || {
        rm -f "$record_tmp"
        return 1
      }
      expected_id="$image_id"
      updated=1
    fi
    printf '%s|%s|%s\n' \
      "$record_reference" "$baseline_id" "$expected_id" >>"$record_tmp"
  done <"$CREATED_IMAGE_RECORD"
  [[ "$updated" == 1 &&
    "$(wc -l <"$record_tmp" | tr -d '[:space:]')" == 5 ]] || {
    rm -f "$record_tmp"
    return 1
  }
  mv -f "$record_tmp" "$CREATED_IMAGE_RECORD"
}

build_restore_controller() {
  docker build \
    --file "$CONTROLLER_DOCKERFILE" \
    --target restore_live_controller \
    --tag "$CONTROLLER_IMAGE" \
    "$ROOT"
  record_image "$CONTROLLER_IMAGE"
}

build_restore_fixture() {
  docker build \
    --file "$CONTROLLER_DOCKERFILE" \
    --target restore_live_fixture \
    --tag "$FIXTURE_IMAGE" \
    "$ROOT"
  record_image "$FIXTURE_IMAGE"
}

build_images() {
  docker build --file "$ROOT/Dockerfile.backup" \
    --tag "$BACKUP_IMAGE" "$ROOT"
  record_image "$BACKUP_IMAGE"

  export HAPPYLEARN_AISTOR_LICENSE_FILE="$LICENSE_COPY"
  export HAPPYLEARN_BACKUP_IMAGE="$BACKUP_IMAGE"
  compose build app worker
  record_image "$APP_IMAGE"
  record_image "$WORKER_IMAGE"
  build_restore_controller
  build_restore_fixture
}

generate_age_identity() {
  local identity_file="$FIXTURE_ROOT/offline/age.identity"
  local recipient
  docker run --rm \
    --network none \
    --read-only \
    --user 10003:0 \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --tmpfs /tmp:rw,noexec,nosuid,size=4m,uid=10003,gid=0,mode=0700 \
    --entrypoint /usr/local/bin/age-keygen \
    "$BACKUP_IMAGE" >"$identity_file" 2>/dev/null
  chmod 0400 "$identity_file"
  recipient="$(
    docker run --rm \
      --interactive \
      --network none \
      --read-only \
      --user 10003:0 \
      --cap-drop ALL \
      --security-opt no-new-privileges:true \
      --entrypoint /usr/local/bin/age-keygen \
      "$BACKUP_IMAGE" -y <"$identity_file"
  )" || fail 'Age recipient generation failed'
  export HAPPYLEARN_BACKUP_AGE_RECIPIENT="$recipient"
  [[ "$HAPPYLEARN_BACKUP_AGE_RECIPIENT" =~ ^age1[0-9a-z]+$ ]] ||
    fail 'Age recipient generation failed'
}

configure_backup_context() {
  local minio_access_variable='HAPPYLEARN_MINIO_ACCESS_KEY'
  local minio_secret_variable='HAPPYLEARN_MINIO_SECRET_KEY'
  export HAPPYLEARN_BACKUP_LIVE_TEST='1'
  export HAPPYLEARN_BACKUP_LIVE_PROJECT="$PROJECT"
  export HAPPYLEARN_BACKUP_LIVE_ROOT="$FIXTURE_ROOT"
  export HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$FIXTURE_ROOT/secrets"
  export HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$FIXTURE_ROOT/repository"
  export HAPPYLEARN_BACKUP_STATE_DIRECTORY="$FIXTURE_ROOT/state"
  export HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID='phase5-restore-live-key'
  export HAPPYLEARN_BACKUP_LOCK_DIRECTORY="$FIXTURE_ROOT/host.lock"
  export HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS='1800'
  export HAPPYLEARN_BACKUP_DATABASE_QUERY_TIMEOUT_SECONDS='60'
  printf -v "$minio_access_variable" '%s' "$SOURCE_MINIO_ACCESS_KEY"
  printf -v "$minio_secret_variable" '%s' "$SOURCE_MINIO_SECRET_KEY"
  export "$minio_access_variable" "$minio_secret_variable"
}

wait_for_source_stack() {
  local deadline=$((SECONDS + 300))
  local service container_id state
  local all_healthy statuses=''
  while ((SECONDS < deadline)); do
    all_healthy=true
    statuses=''
    for service in postgres redis minio app worker; do
      container_id="$(compose ps --quiet "$service")" ||
        return 1
      if [[ -z "$container_id" || "$container_id" == *$'\n'* ]]; then
        all_healthy=false
        statuses="${statuses}${service}=missing "
        continue
      fi
      state="$(
        docker container inspect --format \
          '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' \
          "$container_id"
      )" || return 1
      statuses="${statuses}${service}=${state} "
      [[ "$state" == healthy ]] || all_healthy=false
    done
    if [[ "$all_healthy" == true ]]; then
      return 0
    fi
    sleep 2
  done
  printf 'phase5 restore live: source status %s\n' "$statuses" >&2
  return 1
}

start_source_stack() {
  if ! compose create postgres redis minio app worker; then
    return 1
  fi
  compose up --detach --no-build postgres redis minio app worker
  wait_for_source_stack ||
    fail 'source stack did not become healthy'
}

db_query() {
  compose exec -T \
    -e PGCONNECTTIMEOUT=5 \
    -e 'PGOPTIONS=-c statement_timeout=10000 -c lock_timeout=10000' \
    postgres psql \
    --username happylearn \
    --dbname happylearn \
    --no-psqlrc \
    --tuples-only \
    --no-align \
    --set ON_ERROR_STOP=1
}

db_scalar() {
  local sql="$1"
  local value
  value="$(printf '%s\n' "$sql" | db_query)"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  [[ -n "$value" && "$value" != *$'\n'* ]] ||
    return 1
  printf '%s\n' "$value"
}

bootstrap_teacher() {
  compose run --rm --no-deps \
    --user "$HOST_UID:$HOST_GID" \
    --volume "$TEACHER_PASSWORD_FILE:/run/secrets/teacher-password:ro" \
    --entrypoint /app/happylearn-admin \
    app \
    create-teacher \
    --username "$TEACHER_USERNAME" \
    --display-name "$TEACHER_DISPLAY_NAME" \
    --password-file /run/secrets/teacher-password \
    >/dev/null
  [[ "$(db_scalar \
    "SELECT count(*) FROM users WHERE username='${TEACHER_USERNAME}' AND role='admin' AND status='active';")" == 1 ]] ||
    fail 'teacher bootstrap evidence is invalid'
}

build_source_object_fixture() {
  local helper_output expected_output
  helper_output="$(
    docker run --rm \
      --name "${PROJECT}-object-fixture" \
      --label "com.docker.compose.project=$PROJECT" \
      --label "io.happylearn.phase5.restore-live=$FIXTURE_SUFFIX" \
      --label 'io.happylearn.phase5.restore-kind=source-object-fixture' \
      --network "${PROJECT}_happylearn" \
      --read-only \
      --user "$HOST_UID:$HOST_GID" \
      --cap-drop ALL \
      --security-opt no-new-privileges:true \
      --memory 64m \
      --memory-swap 64m \
      --cpus 0.1 \
      --pids-limit 64 \
      --mount "type=bind,src=$FIXTURE_CONFIG_FILE,dst=/run/secrets/restore-live-object-fixture.json,readonly" \
      --mount "type=bind,src=$FIXTURE_ORIGINAL_FILE,dst=/run/fixture/original.bin,readonly" \
      --mount "type=bind,src=$FIXTURE_PREVIEW_FILE,dst=/run/fixture/preview.bin,readonly" \
      "$FIXTURE_IMAGE"
  )" || fail 'source object fixture write failed'
  printf '%s\n' "$helper_output" >"$FIXTURE_OBJECT_LOG"
  expected_output="phase5_restore_fixture: PASS originalBytes=${FIXTURE_ORIGINAL_SIZE} previewBytes=${FIXTURE_PREVIEW_SIZE}"
  [[ "$helper_output" == "$expected_output" &&
    "$(wc -l <"$FIXTURE_OBJECT_LOG" | tr -d '[:space:]')" == 1 ]] ||
    fail 'source object fixture evidence is invalid'

  printf '%s\n' "
WITH actor AS (
  SELECT id
  FROM users
  WHERE username='${TEACHER_USERNAME}'
    AND role='admin'
    AND status='active'
),
created_file AS (
  INSERT INTO files(created_by)
  SELECT id FROM actor
  RETURNING id,created_by
),
created_version AS (
  INSERT INTO file_versions(
    file_id,version,purpose,object_key,display_name,declared_mime,
    detected_mime,size_bytes,sha256,processing_state,scan_result,created_by
  )
  SELECT
    id,1,'teaching','${FIXTURE_ORIGINAL_KEY}','restore-live-original.bin',
    'application/octet-stream','application/octet-stream',
    ${FIXTURE_ORIGINAL_SIZE},'${FIXTURE_ORIGINAL_SHA256}',
    'ready','clean',created_by
  FROM created_file
  RETURNING id
)
INSERT INTO file_previews(
  file_version_id,preview_kind,object_key,content_type,
  size_bytes,sha256,processing_state
)
SELECT
  id,'thumbnail','${FIXTURE_PREVIEW_KEY}','application/octet-stream',
  ${FIXTURE_PREVIEW_SIZE},'${FIXTURE_PREVIEW_SHA256}','ready'
FROM created_version;" |
    db_query >/dev/null ||
    fail 'source object fixture rows failed'
}

verify_source_object_fixture() {
  local evidence expected
  evidence="$(
    db_scalar "
SELECT
  (SELECT count(*)
   FROM files f
   JOIN file_versions fv ON fv.file_id=f.id
   WHERE fv.object_key='${FIXTURE_ORIGINAL_KEY}')::text || '|' ||
  (SELECT count(*)
   FROM file_versions
   WHERE object_key='${FIXTURE_ORIGINAL_KEY}')::text || '|' ||
  (SELECT count(*)
   FROM file_previews
   WHERE object_key='${FIXTURE_PREVIEW_KEY}')::text || '|' ||
  (SELECT size_bytes::text || '|' || sha256
   FROM file_versions
   WHERE object_key='${FIXTURE_ORIGINAL_KEY}') || '|' ||
  (SELECT size_bytes::text || '|' || sha256
   FROM file_previews
   WHERE object_key='${FIXTURE_PREVIEW_KEY}');"
  )" || fail 'source object fixture row evidence is unavailable'
  expected="1|1|1|${FIXTURE_ORIGINAL_SIZE}|${FIXTURE_ORIGINAL_SHA256}|${FIXTURE_PREVIEW_SIZE}|${FIXTURE_PREVIEW_SHA256}"
  [[ "$evidence" == "$expected" ]] ||
    fail 'source object fixture row evidence is invalid'
}

run_source_backup() {
  if ! bash "$BACKUP_SCRIPT" --project happylearn-dev --trigger manual \
    >"$BACKUP_LOG" 2>&1; then
    fail 'real source backup failed'
  fi

  BACKUP_ID="$(
    db_scalar "
SELECT CASE
  WHEN count(*)=1
    AND count(*) FILTER (WHERE local_snapshot_id IS NOT NULL)=1
    AND count(*) FILTER (WHERE manifest_sha256 IS NOT NULL)=1
  THEN min(id::text)
  ELSE ''
END
FROM backup_runs
WHERE state='succeeded';"
  )" || fail 'could not select the unique successful recovery point'
  valid_uuid "$BACKUP_ID" ||
    fail 'source backup did not produce one canonical successful UUID'
  EXPECTED_MANIFEST_SHA256="$(
    db_scalar "
SELECT encode(manifest_sha256,'hex')
FROM backup_runs
WHERE id='${BACKUP_ID}'::uuid
  AND state='succeeded'
  AND local_snapshot_id IS NOT NULL;"
  )" || fail 'could not load source manifest evidence'
  valid_sha256 "$EXPECTED_MANIFEST_SHA256" ||
    fail 'source manifest evidence is invalid'
  REPORT_FILE="$FIXTURE_ROOT/report/restore-${BACKUP_ID}.json"
}

assert_repository_handoff_safe() {
  local oneoff_containers
  [[ "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" == "$FIXTURE_ROOT/repository" ]] ||
    return 1
  [[ ! -e "$FIXTURE_ROOT/host.lock" &&
    ! -L "$FIXTURE_ROOT/host.lock" ]] ||
    return 1
  oneoff_containers="$(
    docker ps --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}" \
      --filter 'label=com.docker.compose.oneoff=True'
  )" || return 1
  [[ -z "$oneoff_containers" ]]
}

prepare_restore_repository_access() {
  local repository_owner repository_mode
  assert_repository_handoff_safe ||
    fail 'repository ownership handoff was not quiescent'
  compose run --rm --no-deps \
    --entrypoint /usr/bin/timeout \
    backup-storage-init \
    --foreground --kill-after=10s 300s \
    /bin/sh -ceu '
      uid="$1"
      gid="$2"
      case "$uid:$gid" in
        *[!0-9:]* | :* | *: | *:*:*) exit 1 ;;
      esac
      repository_find_empty() {
        result="$(find -P /repository -xdev "$@")" || return 1
        test -z "$result"
      }
      repository_find_empty -type l -print -quit
      repository_find_empty ! -type d ! -type f -print -quit
      repository_find_empty -type f -links +1 -print -quit
      chown -R -- "$uid:$gid" /repository
      repository_stat="$(stat -c "%u:%g:%a" /repository)" || exit 1
      test "$repository_stat" = "$uid:$gid:700"
      repository_find_empty ! -user "$uid" -print -quit
      repository_find_empty ! -group "$gid" -print -quit
    ' handoff "$HOST_UID" "$HOST_GID" \
    >/dev/null ||
    return 1
  repository_owner="$(
    portable_owner "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY"
  )" || return 1
  repository_mode="$(
    portable_mode "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY"
  )" || return 1
  [[ "$repository_owner" == "$HOST_UID" &&
    "$repository_mode" == 700 ]] ||
    fail 'repository ownership handoff to restore operator failed'
}

restore_backup_repository_access() {
  assert_repository_handoff_safe ||
    fail 'repository ownership return was not quiescent'
  compose run --rm --no-deps \
    --entrypoint /usr/bin/timeout \
    backup-storage-init \
    --foreground --kill-after=10s 300s \
    /bin/sh -ceu '
      repository_find_empty() {
        result="$(find -P /repository -xdev "$@")" || return 1
        test -z "$result"
      }
      repository_find_empty -type l -print -quit
      repository_find_empty ! -type d ! -type f -print -quit
      repository_find_empty -type f -links +1 -print -quit
      chown -R -- 10003:0 /repository
      repository_stat="$(stat -c "%u:%g:%a" /repository)" || exit 1
      test "$repository_stat" = "10003:0:700"
      repository_find_empty ! -user 10003 -print -quit
      repository_find_empty ! -group 0 -print -quit
    ' handback \
    >/dev/null ||
    return 1
  compose run --rm --no-deps backup-storage-init >/dev/null ||
    return 1
  compose run --rm --no-deps \
    --entrypoint /usr/bin/timeout \
    backup \
    --foreground --kill-after=10s 300s \
    restic --no-cache \
    --repository-file /run/secrets/local_repository \
    --password-file /run/secrets/local_password \
    cat config \
    >/dev/null ||
    return 1
}

controller_identity_matches() {
  local details
  [[ "$CONTROLLER_ID" =~ ^[a-f0-9]{64}$ &&
    "$CONTROLLER_NAME" == "${PROJECT}-restore-controller" ]] ||
    return 1
  details="$(
    docker container inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.restore-live"}}' \
      "$CONTROLLER_ID" 2>/dev/null
  )" || return 1
  [[ "$details" == \
    "$CONTROLLER_ID|/$CONTROLLER_NAME|$PROJECT|$FIXTURE_SUFFIX" ]]
}

discover_restore_controller() {
  local details discovered_id discovered_name candidates
  local project_label restore_label extra
  [[ "$CONTROLLER_NAME" == "${PROJECT}-restore-controller" &&
    "$PROJECT" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ &&
    "$FIXTURE_SUFFIX" =~ ^[a-f0-9]{12}$ ]] ||
    return 1
  details="$(
    docker container inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.restore-live"}}' \
      "$CONTROLLER_NAME" 2>/dev/null
  )" || {
    candidates="$(
      docker container ls --all --quiet \
        --filter "name=^/${CONTROLLER_NAME}$"
    )" || return 1
    [[ -z "$candidates" ]] && return 2
    return 1
  }
  IFS='|' read -r discovered_id discovered_name \
    project_label restore_label extra <<<"$details"
  [[ -z "$extra" &&
    "$discovered_id" =~ ^[a-f0-9]{64}$ &&
    "$discovered_name" == "/$CONTROLLER_NAME" &&
    "$project_label" == "$PROJECT" &&
    "$restore_label" == "$FIXTURE_SUFFIX" ]] ||
    return 1
  if [[ -n "$CONTROLLER_ID" &&
    "$CONTROLLER_ID" != "$discovered_id" ]]; then
    return 1
  fi
  CONTROLLER_ID="$discovered_id"
  CONTROLLER_CREATED=true
}

terminate_process_bounded() {
  local pid="$1"
  local term_grace_seconds="$2"
  local deadline
  [[ "$pid" =~ ^[1-9][0-9]*$ &&
    "$pid" != "$$" &&
    "$term_grace_seconds" =~ ^[1-9][0-9]*$ ]] ||
    return 1
  kill -TERM "$pid" 2>/dev/null || true
  deadline=$((SECONDS + term_grace_seconds))
  while kill -0 "$pid" 2>/dev/null; do
    if ((SECONDS >= deadline)); then
      kill -KILL "$pid" 2>/dev/null || true
      break
    fi
    sleep 0.1
  done
  # pid is the direct child created below. After SIGKILL, wait cannot depend
  # on the child cooperating and reaps its status immediately.
  wait "$pid" 2>/dev/null || true
  ! kill -0 "$pid" 2>/dev/null
}

reap_controller_wait_client() {
  [[ -n "$CONTROLLER_WAIT_PID" ]] || return 0
  terminate_process_bounded \
    "$CONTROLLER_WAIT_PID" \
    "$CONTROLLER_WAIT_TERM_GRACE_SECONDS" ||
    return 1
  CONTROLLER_WAIT_PID=''
  if [[ -n "$CONTROLLER_WAIT_STATUS_FILE" ]]; then
    rm -f "$CONTROLLER_WAIT_STATUS_FILE"
  fi
}

stop_restore_controller_bounded() {
  local state
  controller_identity_matches || return 1
  docker container stop \
    --signal TERM \
    --timeout "$CONTROLLER_STOP_TIMEOUT_SECONDS" \
    "$CONTROLLER_ID" >/dev/null 2>&1 ||
    return 1
  reap_controller_wait_client || return 1
  controller_identity_matches || return 1
  state="$(
    docker container inspect --format '{{.State.Running}}' \
      "$CONTROLLER_ID" 2>/dev/null
  )" || return 1
  [[ "$state" == false ]]
}

remove_restore_controller() {
  local discover_status=0 stop_succeeded=false
  discover_restore_controller || discover_status=$?
  if [[ "$discover_status" -ne 0 ]]; then
    if [[ "$discover_status" == 2 ]]; then
      reap_controller_wait_client || return 1
      CONTROLLER_CREATED=false
      CONTROLLER_ID=''
      return 0
    fi
    return 1
  fi
  if stop_restore_controller_bounded; then
    stop_succeeded=true
  fi
  controller_identity_matches || return 1
  if [[ "$stop_succeeded" == true ]]; then
    if ! docker container rm "$CONTROLLER_ID" >/dev/null 2>&1; then
      controller_identity_matches || return 1
      docker container rm --force "$CONTROLLER_ID" >/dev/null 2>&1 ||
        return 1
    fi
  else
    docker container rm --force "$CONTROLLER_ID" >/dev/null 2>&1 ||
      return 1
    reap_controller_wait_client || return 1
  fi
  docker_container_id_absent "$CONTROLLER_ID" || return 1
  docker_container_name_absent "$CONTROLLER_NAME" || return 1
  CONTROLLER_CREATED=false
  CONTROLLER_ID=''
}

wait_restore_controller_bounded() {
  local container_id="$1"
  local timeout_seconds="$2"
  local deadline wait_status
  [[ "$container_id" =~ ^[a-f0-9]{64}$ &&
    "$timeout_seconds" =~ ^[1-9][0-9]*$ &&
    "$timeout_seconds" -lt "$RTO_LIMIT_SECONDS" &&
    "$CONTROLLER_WAIT_STATUS_FILE" == \
      "$FIXTURE_ROOT/control/controller.wait" &&
    ! -e "$CONTROLLER_WAIT_STATUS_FILE" &&
    ! -L "$CONTROLLER_WAIT_STATUS_FILE" ]] ||
    return 1
  (umask 077 && : >"$CONTROLLER_WAIT_STATUS_FILE") || return 1
  docker container wait "$container_id" \
    >"$CONTROLLER_WAIT_STATUS_FILE" 2>/dev/null &
  CONTROLLER_WAIT_PID=$!
  deadline=$((SECONDS + timeout_seconds))
  while kill -0 "$CONTROLLER_WAIT_PID" 2>/dev/null; do
    if ((SECONDS >= deadline)); then
      reap_controller_wait_client >/dev/null 2>&1 || return 1
      return 124
    fi
    sleep 0.1
  done
  if wait "$CONTROLLER_WAIT_PID"; then
    wait_status=0
  else
    wait_status=$?
  fi
  CONTROLLER_WAIT_PID=''
  [[ "$wait_status" -eq 0 &&
    -f "$CONTROLLER_WAIT_STATUS_FILE" &&
    ! -L "$CONTROLLER_WAIT_STATUS_FILE" &&
    "$(portable_mode "$CONTROLLER_WAIT_STATUS_FILE")" == 600 &&
    "$(portable_owner "$CONTROLLER_WAIT_STATUS_FILE")" == "$(id -u)" &&
    "$(wc -l <"$CONTROLLER_WAIT_STATUS_FILE" | tr -d '[:space:]')" == 1 ]] ||
    return 1
  CONTROLLER_EXIT_STATUS="$(<"$CONTROLLER_WAIT_STATUS_FILE")"
  rm -f "$CONTROLLER_WAIT_STATUS_FILE"
  [[ "$CONTROLLER_EXIT_STATUS" =~ ^(0|[1-9][0-9]{0,2})$ &&
    "$CONTROLLER_EXIT_STATUS" -le 255 ]]
}

run_restore_controller() {
  local container_status create_output create_status=0 cidfile_id
  [[ "$CONTROLLER_CREATED" == false &&
    -z "$CONTROLLER_ID" &&
    -S "$DOCKER_SOCKET" &&
    ! -L "$DOCKER_SOCKET" ]] ||
    return 1
  if create_output="$(
    docker container create \
      --cidfile "$CONTROLLER_CID_FILE" \
      --name "$CONTROLLER_NAME" \
      --label "com.docker.compose.project=$PROJECT" \
      --label "io.happylearn.phase5.restore-live=$FIXTURE_SUFFIX" \
      --network none \
      --read-only \
      --user "$HOST_UID:$HOST_GID" \
      --group-add "$CONTROLLER_SOCKET_GID" \
      --cap-drop ALL \
      --security-opt no-new-privileges:true \
      --memory 256m \
      --memory-swap 256m \
      --cpus 0.2 \
      --pids-limit 128 \
      --mount "type=bind,src=$DOCKER_SOCKET,dst=/var/run/docker.sock" \
      --mount "type=bind,src=$ROOT,dst=$ROOT,readonly" \
      --mount "type=bind,src=$FIXTURE_ROOT/repository,dst=$FIXTURE_ROOT/repository" \
      --mount "type=bind,src=$FIXTURE_ROOT/control,dst=$FIXTURE_ROOT/control" \
      --mount "type=bind,src=$FIXTURE_ROOT/report,dst=$FIXTURE_ROOT/report" \
      --mount "type=bind,src=$FIXTURE_ROOT/controller-tmp,dst=$FIXTURE_ROOT/controller-tmp" \
      --mount "type=bind,src=$FIXTURE_ROOT/secrets,dst=$FIXTURE_ROOT/secrets,readonly" \
      --env 'DOCKER_HOST=unix:///var/run/docker.sock' \
      --env "TMPDIR=$FIXTURE_ROOT/controller-tmp" \
      --env "RESTORE_SCRIPT=$RESTORE_SCRIPT" \
      --env "RESTORE_STAGE_FILE=$RESTORE_STAGE_FILE" \
      --env "BACKUP_ID=$BACKUP_ID" \
      --env "HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY=$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" \
      --env "HAPPYLEARN_BACKUP_SECRET_DIRECTORY=$HAPPYLEARN_BACKUP_SECRET_DIRECTORY" \
      --env "HAPPYLEARN_RESTORE_CONTROL_DIRECTORY=$HAPPYLEARN_RESTORE_CONTROL_DIRECTORY" \
      --env "HAPPYLEARN_RESTORE_REPORT_DIRECTORY=$HAPPYLEARN_RESTORE_REPORT_DIRECTORY" \
      --env "HAPPYLEARN_AISTOR_LICENSE_FILE=$HAPPYLEARN_AISTOR_LICENSE_FILE" \
      --env "HAPPYLEARN_BACKUP_IMAGE=$HAPPYLEARN_BACKUP_IMAGE" \
      --env "HAPPYLEARN_RESTORE_APP_IMAGE=$HAPPYLEARN_RESTORE_APP_IMAGE" \
      --env "HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE=$HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE" \
      --env "HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS=$HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS" \
      --env "HAPPYLEARN_RESTORE_READY_TIMEOUT_SECONDS=$HAPPYLEARN_RESTORE_READY_TIMEOUT_SECONDS" \
      --entrypoint /bin/bash \
      "$CONTROLLER_IMAGE" \
      -ceu '
        write_restore_stage() {
          local stage="$1"
          local temporary="$TMPDIR/.restore.stage.new"
          case "$stage" in
            wrapper_preflight | verifier_started | \
              paths_validate_started | restore_lock_started | \
              report_lock_started | identity_init_started | \
              workspace_init_started | orphan_reap_started | \
              network_create_started | volume_create_started | \
              repository_check_started | snapshot_select_started | \
              snapshot_restore_started | object_restore_started | \
              license_init_started | postgres_start_started | \
              database_restore_started | dependencies_start_started | \
              sessions_revoke_started | app_start_started | \
              app_ready_wait_started | restore_check_started | \
              counts_load_started | http_probe_started | \
              report_publish_started) ;;
            *) return 1 ;;
          esac
          (umask 077 && printf "%s\n" "$stage" >"$temporary")
          chmod 0600 "$temporary"
          test "$(stat -c "%u:%g:%a:%s" "$temporary")" = \
            "$(id -u):$(id -g):600:$((${#stage} + 1))"
          mv -f "$temporary" "$RESTORE_STAGE_FILE"
        }
        trace_restore_stage() {
          local function_name="${FUNCNAME[1]:-}"
          local stage=
          case "$function_name" in
            validate_paths) stage=paths_validate_started ;;
            acquire_restore_lock) stage=restore_lock_started ;;
            acquire_report_lock) stage=report_lock_started ;;
            initialize_identity) stage=identity_init_started ;;
            initialize_workspace) stage=workspace_init_started ;;
            reap_orphan_resources) stage=orphan_reap_started ;;
            create_network) stage=network_create_started ;;
            create_volume) stage=volume_create_started ;;
            repository_check) stage=repository_check_started ;;
            select_snapshot) stage=snapshot_select_started ;;
            restore_snapshot) stage=snapshot_restore_started ;;
            restore_object_data) stage=object_restore_started ;;
            initialize_aistor_license) stage=license_init_started ;;
            start_postgres) stage=postgres_start_started ;;
            restore_database) stage=database_restore_started ;;
            start_dependencies) stage=dependencies_start_started ;;
            revoke_restored_sessions) stage=sessions_revoke_started ;;
            start_restored_app) stage=app_start_started ;;
            wait_for_restored_app) stage=app_ready_wait_started ;;
            run_restore_check) stage=restore_check_started ;;
            load_safe_restore_counts) stage=counts_load_started ;;
            run_restore_http_probe) stage=http_probe_started ;;
            write_sanitized_report) stage=report_publish_started ;;
          esac
          if [[ -n "$stage" && "$stage" != "${LAST_RESTORE_STAGE:-}" ]]; then
            write_restore_stage "$stage"
            LAST_RESTORE_STAGE="$stage"
          fi
        }
        write_restore_stage wrapper_preflight
        controller_supplementary_groups_match() {
          local host_gid="$1"
          local socket_gid="$2"
          local group saw_host=false saw_socket=false
          for group in $(id -G); do
            [[ "$group" == "$host_gid" ||
              "$group" == "$socket_gid" ]] ||
              return 1
            [[ "$group" != "$host_gid" ]] || saw_host=true
            [[ "$group" != "$socket_gid" ]] || saw_socket=true
          done
          [[ "$saw_host" == true && "$saw_socket" == true ]]
        }
        test "$(id -u):$(id -g)" = "$1:$2"
        controller_supplementary_groups_match "$2" "$3"
        test -S /var/run/docker.sock
        test "$(stat -c "%g" /var/run/docker.sock)" = "$3"
        test "$(stat -c "%a" /var/run/docker.sock)" = "$4"
        test -r /var/run/docker.sock
        test -w /var/run/docker.sock
        docker version >/dev/null
        write_restore_stage verifier_started
        set -o functrace
        trap trace_restore_stage DEBUG
        source "$RESTORE_SCRIPT" --backup-id "$BACKUP_ID"
      ' controller "$HOST_UID" "$HOST_GID" \
      "$CONTROLLER_SOCKET_GID" "$CONTROLLER_SOCKET_MODE"
  )"; then
    create_status=0
  else
    create_status=$?
  fi
  discover_restore_controller || return 1
  [[ -f "$CONTROLLER_CID_FILE" &&
    ! -L "$CONTROLLER_CID_FILE" &&
    "$(portable_owner "$CONTROLLER_CID_FILE")" == "$(id -u)" &&
    "$(portable_size "$CONTROLLER_CID_FILE")" -ge 64 &&
    "$(portable_size "$CONTROLLER_CID_FILE")" -le 65 ]] ||
    return 1
  cidfile_id="$(<"$CONTROLLER_CID_FILE")"
  cidfile_id="${cidfile_id%$'\n'}"
  [[ "$create_status" == 0 &&
    "$cidfile_id" == "$CONTROLLER_ID" &&
    ("$create_output" == "$CONTROLLER_ID" ||
      -z "$create_output") ]] ||
    return 1
  rm -f "$CONTROLLER_CID_FILE"

  container_status=125
  if docker container start "$CONTROLLER_ID" >/dev/null; then
    if wait_restore_controller_bounded \
      "$CONTROLLER_ID" "$CONTROLLER_WAIT_LIMIT_SECONDS"; then
      container_status="$CONTROLLER_EXIT_STATUS"
    fi
  fi
  docker container logs "$CONTROLLER_ID" >"$RESTORE_LOG" 2>&1 ||
    container_status=125
  remove_restore_controller || return 1
  [[ "$container_status" == 0 ]]
}

extract_restore_failure_stage() {
  local stage
  owner_private_regular_file "$RESTORE_STAGE_FILE" 600 64 ||
    return 1
  [[ "$(wc -l <"$RESTORE_STAGE_FILE" | tr -d '[:space:]')" == 1 ]] ||
    return 1
  stage="$(<"$RESTORE_STAGE_FILE")"
  valid_restore_stage "$stage" || return 1
  printf '%s\n' "$stage"
}

run_empty_restore() {
  local failure_category failure_stage
  export HAPPYLEARN_RESTORE_CONTROL_DIRECTORY="$FIXTURE_ROOT/control"
  export HAPPYLEARN_RESTORE_REPORT_DIRECTORY="$FIXTURE_ROOT/report"
  export HAPPYLEARN_RESTORE_APP_IMAGE="$APP_IMAGE"
  export HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE="$TEACHER_CREDENTIAL_FILE"
  export HAPPYLEARN_RESTORE_EXTERNAL_TIMEOUT_SECONDS='600'
  export HAPPYLEARN_RESTORE_READY_TIMEOUT_SECONDS='300'

  if ! run_restore_controller; then
    [[ ! -e "$REPORT_FILE" ]] ||
      fail 'failed restore published a success report'
    assert_restore_resources_absent "$BACKUP_ID" ||
      fail 'failed restore left disposable resources'
    failure_category='unavailable'
    if assert_secret_absent \
        "$TEACHER_PASSWORD_FILE" \
        "$TEACHER_CREDENTIAL_FILE" \
        "$RESTORE_LOG" &&
      assert_single_line_secret_absent \
        "$FIXTURE_ROOT/secrets/local_password" \
        "$RESTORE_LOG" &&
      assert_single_line_secret_absent \
        "$FIXTURE_ROOT/secrets/database_password" \
        "$RESTORE_LOG" &&
      assert_single_line_secret_absent \
        "$SOURCE_MINIO_ACCESS_KEY_FILE" \
        "$RESTORE_LOG" &&
      assert_single_line_secret_absent \
        "$SOURCE_MINIO_SECRET_KEY_FILE" \
        "$RESTORE_LOG" &&
      assert_single_line_secret_absent \
        "$LICENSE_COPY" \
        "$RESTORE_LOG" &&
      assert_age_identity_absent \
        "$FIXTURE_ROOT/offline/age.identity" \
        "$RESTORE_LOG"; then
      failure_category="$(
        extract_restore_failure_category "$RESTORE_LOG" ||
          printf 'unavailable\n'
      )"
    fi
    failure_stage="$(
      extract_restore_failure_stage ||
        printf 'unavailable\n'
    )"
    fail "empty-environment restore verification failed category=${failure_category} stage=${failure_stage}"
  fi

  [[ -f "$REPORT_FILE" &&
    ! -e "$FIXTURE_ROOT/report/.restore-${BACKUP_ID}.new" &&
    "$(find "$FIXTURE_ROOT/report" -maxdepth 1 -type f \
      -name 'restore-*.json' | wc -l | tr -d '[:space:]')" == 1 ]] ||
    fail 'restore did not publish exactly one sanitized report'
  parse_sanitized_report "$REPORT_FILE" "$BACKUP_ID" "$EXPECTED_MANIFEST_SHA256" ||
    fail 'restore report validation failed'
  assert_secret_absent "$TEACHER_PASSWORD_FILE" "$TEACHER_CREDENTIAL_FILE" "$RESTORE_LOG" "$REPORT_FILE" ||
    fail 'restore log or report exposed teacher credentials'
  assert_secret_absent "$TEACHER_PASSWORD_FILE" "$TEACHER_CREDENTIAL_FILE" "$BACKUP_LOG" ||
    fail 'backup log exposed teacher credentials'
  assert_single_line_secret_absent "$FIXTURE_ROOT/secrets/local_password" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE" ||
    fail 'backup or restore evidence exposed the repository credential'
  assert_single_line_secret_absent "$FIXTURE_ROOT/secrets/database_password" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE" ||
    fail 'backup or restore evidence exposed the database credential'
  assert_single_line_secret_absent "$SOURCE_MINIO_ACCESS_KEY_FILE" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE" ||
    fail 'backup or restore evidence exposed the source object access key'
  assert_single_line_secret_absent "$SOURCE_MINIO_SECRET_KEY_FILE" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE" ||
    fail 'backup or restore evidence exposed the source object secret'
  assert_single_line_secret_absent "$LICENSE_COPY" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE" ||
    fail 'backup or restore evidence exposed the AIStor license'
  assert_age_identity_absent "$FIXTURE_ROOT/offline/age.identity" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE" ||
    fail 'backup or restore evidence exposed the Age identity'
  assert_restore_resources_absent "$BACKUP_ID" ||
    fail 'successful restore left disposable resources'
}

record_restore_verification() {
  local record_log="$FIXTURE_ROOT/logs/restore-record.log"
  local evidence expected
  : >"$record_log"
  chmod 0600 "$record_log"
  if ! compose run --rm --no-deps -T backup restore-record \
    <"$REPORT_FILE" >"$record_log" 2>&1; then
    fail 'successful restore evidence was not recorded in the primary database'
  fi
  if ! compose run --rm --no-deps -T backup restore-record \
    <"$REPORT_FILE" >>"$record_log" 2>&1; then
    fail 'successful restore evidence replay was not idempotent'
  fi
  assert_secret_absent \
    "$TEACHER_PASSWORD_FILE" \
    "$TEACHER_CREDENTIAL_FILE" \
    "$record_log" ||
    fail 'restore evidence recorder exposed teacher credentials'
  assert_single_line_secret_absent \
    "$FIXTURE_ROOT/secrets/database_password" \
    "$record_log" ||
    fail 'restore evidence recorder exposed the database credential'
  evidence="$(
    db_scalar "
SELECT
  count(*)::text || '|' ||
  min(state) || '|' ||
  min(restored_migration_version)::text || '|' ||
  min(checked_object_count)::text || '|' ||
  min(missing_object_count)::text || '|' ||
  min(unexpected_object_count)::text || '|' ||
  bool_and(session_revocation_verified)::text || '|' ||
  min(rto_seconds)::text || '|' ||
  min(encode(report_sha256,'hex')) || '|' ||
  min((
    SELECT sum((value #>> '{}')::bigint)
    FROM jsonb_each(database_row_counts)
  ))::text || '|' ||
  min((
    SELECT count(*)
    FROM jsonb_object_keys(database_row_counts)
  ))::text
FROM restore_verifications
WHERE id='${LIVE_REPORT_VERIFICATION_ID}'::uuid
  AND backup_run_id='${BACKUP_ID}'::uuid
  AND database_row_counts='${LIVE_REPORT_DATABASE_ROW_COUNTS}'::jsonb;"
  )" || fail 'primary restore verification evidence is unavailable'
  expected="1|succeeded|${LIVE_REPORT_MIGRATION_VERSION}|${LIVE_REPORT_CHECKED_OBJECT_COUNT}|0|0|true|${LIVE_REPORT_DURATION_SECONDS}|${LIVE_REPORT_SHA256}|${LIVE_REPORT_ROW_COUNT_TOTAL}|16"
  [[ "$evidence" == "$expected" ]] ||
    fail 'primary restore verification evidence is invalid'
}

expected_owned_restore_name() {
  local project="$1"
  local class="$2"
  local kind="$3"
  case "$class:$kind" in
    volumes:postgres) printf '%s-postgres\n' "$project" ;;
    volumes:aistor) printf '%s-aistor\n' "$project" ;;
    volumes:aistor-license) printf '%s-aistor-license\n' "$project" ;;
    networks:network) printf '%s-network\n' "$project" ;;
    containers:volume-probe-postgres)
      printf '%s-volume-probe-postgres\n' "$project"
      ;;
    containers:volume-probe-aistor)
      printf '%s-volume-probe-aistor\n' "$project"
      ;;
    containers:volume-probe-aistor-license)
      printf '%s-volume-probe-aistor-license\n' "$project"
      ;;
    containers:restic-check) printf '%s-restic-check\n' "$project" ;;
    containers:restic-select) printf '%s-restic-select\n' "$project" ;;
    containers:restic-restore) printf '%s-restic-restore\n' "$project" ;;
    containers:object-restore) printf '%s-object-restore\n' "$project" ;;
    containers:aistor-license-init)
      printf '%s-aistor-license-init\n' "$project"
      ;;
    containers:postgres) printf '%s-postgres\n' "$project" ;;
    containers:postgres-restore)
      printf '%s-postgres-restore\n' "$project"
      ;;
    containers:aistor) printf '%s-aistor\n' "$project" ;;
    containers:redis) printf '%s-redis\n' "$project" ;;
    containers:revoke-sessions)
      printf '%s-revoke-sessions\n' "$project"
      ;;
    containers:app) printf '%s-app\n' "$project" ;;
    containers:restore-check) printf '%s-restore-check\n' "$project" ;;
    containers:restore-http-'probe')
      printf '%s-restore-http-%s\n' "$project" probe
      ;;
    *) return 1 ;;
  esac
}

list_owned_restore_resources() {
  local class="$1"
  case "$class" in
    containers)
      docker container ls --all --format '{{.Names}}' \
        --filter \
        "label=io.happylearn.phase5.restore-backup-id=$BACKUP_ID"
      ;;
    volumes)
      docker volume ls --format '{{.Name}}' \
        --filter \
        "label=io.happylearn.phase5.restore-backup-id=$BACKUP_ID"
      ;;
    networks)
      docker network ls --format '{{.Name}}' \
        --filter \
        "label=io.happylearn.phase5.restore-backup-id=$BACKUP_ID"
      ;;
    *) return 1 ;;
  esac
}

inspect_owned_restore_resource() {
  local class="$1"
  local identifier="$2"
  case "$class" in
    containers)
      docker container inspect --format \
        '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.restore-owner"}}|{{index .Config.Labels "io.happylearn.phase5.restore-kind"}}|{{index .Config.Labels "io.happylearn.phase5.restore-backup-id"}}' \
        "$identifier"
      ;;
    volumes)
      docker volume inspect --format \
        '{{.Name}}|{{.Name}}|{{index .Labels "com.docker.compose.project"}}|{{index .Labels "io.happylearn.phase5.restore-owner"}}|{{index .Labels "io.happylearn.phase5.restore-kind"}}|{{index .Labels "io.happylearn.phase5.restore-backup-id"}}' \
        "$identifier"
      ;;
    networks)
      docker network inspect --format \
        '{{.Id}}|{{.Name}}|{{index .Labels "com.docker.compose.project"}}|{{index .Labels "io.happylearn.phase5.restore-owner"}}|{{index .Labels "io.happylearn.phase5.restore-kind"}}|{{index .Labels "io.happylearn.phase5.restore-backup-id"}}' \
        "$identifier"
      ;;
    *) return 1 ;;
  esac
}

validate_owned_restore_resource() {
  local class="$1"
  local listed_name="$2"
  local observation="$3"
  local identity observed_name project owner kind backup extra expected
  IFS='|' read -r identity observed_name project owner kind backup extra \
    <<<"$observation"
  [[ -z "$extra" &&
    "$owner" =~ ^[a-f0-9]{64}$ &&
    "$project" == "happylearn-phase5-restore-${owner:0:12}" &&
    "$project" =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ &&
    "$backup" == "$BACKUP_ID" ]] ||
    return 1
  expected="$(expected_owned_restore_name "$project" "$class" "$kind")" ||
    return 1
  [[ "$listed_name" == "$expected" ]] || return 1
  case "$class" in
    containers)
      [[ "$identity" =~ ^[a-f0-9]{64}$ &&
        "$observed_name" == "/$listed_name" ]]
      ;;
    volumes)
      [[ "$identity" == "$listed_name" &&
        "$observed_name" == "$listed_name" ]]
      ;;
    networks)
      [[ "$identity" =~ ^[a-f0-9]{64}$ &&
        "$observed_name" == "$listed_name" ]]
      ;;
    *) return 1 ;;
  esac
}

remove_owned_restore_resource() {
  local class="$1"
  local name="$2"
  local observation confirmation identity owner dual_names remaining_names
  observation="$(inspect_owned_restore_resource "$class" "$name")" ||
    return 1
  validate_owned_restore_resource "$class" "$name" "$observation" ||
    return 1
  identity="${observation%%|*}"
  owner="${observation#*|}"
  owner="${owner#*|}"
  owner="${owner#*|}"
  owner="${owner%%|*}"
  case "$class" in
    containers)
      dual_names="$(
        docker container ls --all --format '{{.Names}}' \
          --filter \
          "label=io.happylearn.phase5.restore-backup-id=$BACKUP_ID" \
          --filter "label=io.happylearn.phase5.restore-owner=$owner"
      )" || return 1
      ;;
    volumes)
      dual_names="$(
        docker volume ls --format '{{.Name}}' \
          --filter \
          "label=io.happylearn.phase5.restore-backup-id=$BACKUP_ID" \
          --filter "label=io.happylearn.phase5.restore-owner=$owner"
      )" || return 1
      ;;
    networks)
      dual_names="$(
        docker network ls --format '{{.Name}}' \
          --filter \
          "label=io.happylearn.phase5.restore-backup-id=$BACKUP_ID" \
          --filter "label=io.happylearn.phase5.restore-owner=$owner"
      )" || return 1
      ;;
    *) return 1 ;;
  esac
  [[ "$(printf '%s\n' "$dual_names" | grep -Fxc "$name")" == 1 ]] ||
    return 1
  confirmation="$(inspect_owned_restore_resource "$class" "$identity")" ||
    return 1
  [[ "$confirmation" == "$observation" ]] || return 1
  case "$class" in
    containers)
      docker container rm --force "$identity" >/dev/null || return 1
      ;;
    volumes)
      docker volume rm "$name" >/dev/null || return 1
      ;;
    networks)
      docker network rm "$identity" >/dev/null || return 1
      ;;
  esac
  remaining_names="$(list_owned_restore_resources "$class")" || return 1
  [[ "$(printf '%s\n' "$remaining_names" | grep -Fxc "$name")" == 0 ]]
}

cleanup_owned_restore_resources() {
  local class names name
  valid_uuid "$BACKUP_ID" || return 0
  for class in containers volumes networks; do
    names="$(list_owned_restore_resources "$class")" || return 1
    [[ "${#names}" -le 65536 ]] || return 1
    while IFS= read -r name; do
      [[ -z "$name" ]] && continue
      [[ "$name" =~ \
        ^happylearn-phase5-restore-[a-f0-9]{12}[-a-z0-9]*$ ]] ||
        return 1
      remove_owned_restore_resource "$class" "$name" || return 1
    done <<<"$names"
  done
  assert_restore_resources_absent "$BACKUP_ID"
}

repository_find_empty() {
  local repository="$1"
  local result
  shift
  [[ "$repository" == "$FIXTURE_ROOT/repository" &&
    "$repository" == "${TMPDIR:-/tmp}/phase5-restore-live."*/repository &&
    -d "$repository" && ! -L "$repository" ]] ||
    return 1
  result="$(find "$repository" -xdev "$@")" || return 1
  [[ -z "$result" ]]
}

handoff_repository_to_host_for_cleanup() {
  local repository="$FIXTURE_ROOT/repository"
  local actual_image_id registered_baseline registered_expected extra
  local repository_entry repository_owner
  [[ "$HOST_UID" =~ ^[1-9][0-9]*$ &&
    "$HOST_GID" =~ ^[0-9]+$ &&
    "$repository" == "${TMPDIR:-/tmp}/phase5-restore-live."*/repository &&
    -d "$repository" && ! -L "$repository" ]] ||
    return 1
  repository_entry="$(
    find "$repository" -xdev -mindepth 1 -print -quit
  )" || return 1
  if [[ -z "$repository_entry" ]]; then
    repository_owner="$(portable_owner "$repository")" || return 1
    if [[ "$repository_owner" == "$HOST_UID" ]]; then
      REPOSITORY_HOST_HANDOFF=true
      return 0
    fi
  fi
  IFS='|' read -r registered_baseline registered_expected extra < <(
    awk -F '|' -v reference="$BACKUP_IMAGE" \
      '$1 == reference { print $2 "|" $3 }' \
      "$CREATED_IMAGE_RECORD"
  )
  [[ -z "$extra" &&
    "$registered_baseline" == absent &&
    ("$registered_expected" == pending ||
      "$registered_expected" =~ ^sha256:[a-f0-9]{64}$) ]] ||
    return 1
  actual_image_id="$(
    docker image inspect --format '{{.Id}}' "$BACKUP_IMAGE" 2>/dev/null
  )" || return 1
  [[ "$actual_image_id" =~ ^sha256:[a-f0-9]{64}$ &&
    ("$registered_expected" == pending ||
      "$actual_image_id" == "$registered_expected") ]] ||
    return 1
  docker run --rm \
    --name "${PROJECT}-repository-cleanup" \
    --label "com.docker.compose.project=$PROJECT" \
    --label "io.happylearn.phase5.restore-live=$FIXTURE_SUFFIX" \
    --label 'io.happylearn.phase5.restore-kind=repository-cleanup' \
    --network none \
    --read-only \
    --user 0:0 \
    --cap-drop ALL \
    --cap-add CHOWN \
    --cap-add DAC_OVERRIDE \
    --security-opt no-new-privileges:true \
    --memory 64m \
    --memory-swap 64m \
    --cpus 0.1 \
    --pids-limit 64 \
    --mount "type=bind,src=$repository,dst=/repository" \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_IMAGE" \
    --foreground --kill-after=5s 60s \
    /bin/sh -ceu '
      host_uid="$1"
      host_gid="$2"
      case "$host_uid:$host_gid" in
        *[!0-9:]* | 0:* | :* | *: | *:*:*) exit 1 ;;
      esac
      repository_find_empty() {
        result="$(find -P /repository -xdev "$@")" || return 1
        test -z "$result"
      }
      repository_find_empty -type l -print -quit
      repository_find_empty ! -type d ! -type f -print -quit
      repository_find_empty -type f -links +1 -print -quit
      owners="$(find -P /repository -xdev -printf "%U:%G\n")" || exit 1
      while IFS= read -r owner; do
        case "$owner" in
          "$host_uid:$host_gid" | 10003:0) ;;
          *) exit 1 ;;
        esac
      done <<EOF
$owners
EOF
      chown -R -- "$host_uid:$host_gid" /repository
      repository_find_empty ! -user "$host_uid" -print -quit
      repository_find_empty ! -group "$host_gid" -print -quit
    ' cleanup "$HOST_UID" "$HOST_GID" \
    >/dev/null 2>&1 ||
    return 1
  repository_owner="$(portable_owner "$repository")" || return 1
  [[ "$repository_owner" == "$HOST_UID" ]] || return 1
  repository_find_empty \
    "$repository" ! -user "$HOST_UID" -print -quit ||
    return 1
  repository_find_empty \
    "$repository" ! -group "$HOST_GID" -print -quit ||
    return 1
  repository_find_empty "$repository" -type l -print -quit ||
    return 1
  repository_find_empty \
    "$repository" ! -type d ! -type f -print -quit ||
    return 1
  repository_find_empty "$repository" -type f -links +1 -print -quit ||
    return 1
  REPOSITORY_HOST_HANDOFF=true
}

remove_created_images() {
  [[ -f "$CREATED_IMAGE_RECORD" && ! -L "$CREATED_IMAGE_RECORD" ]] ||
    return 0
  local reference baseline_id expected_id extra actual_id index
  local inspect_status
  local seen='|'
  local -a records=()
  while IFS='|' read -r reference baseline_id expected_id extra; do
    [[ -z "$extra" &&
      "$seen" != *"|${reference}|"* &&
      ("$baseline_id" == absent ||
        "$baseline_id" =~ ^sha256:[a-f0-9]{64}$) &&
      ("$expected_id" == pending ||
        "$expected_id" =~ ^sha256:[a-f0-9]{64}$) ]] &&
      image_reference_is_expected "$reference" ||
      return 1
    seen="${seen}${reference}|"
    records+=("${reference}|${baseline_id}|${expected_id}")
  done <"$CREATED_IMAGE_RECORD"
  for ((index = ${#records[@]} - 1; index >= 0; index--)); do
    IFS='|' read -r reference baseline_id expected_id \
      <<<"${records[$index]}"
    inspect_status=0
    actual_id="$(inspect_image_reference_id "$reference")" ||
      inspect_status=$?
    if [[ "$inspect_status" == 2 ]]; then
      actual_id=''
    elif [[ "$inspect_status" != 0 ]]; then
      return 1
    fi
    if [[ "$baseline_id" != absent ]]; then
      [[ "$actual_id" == "$baseline_id" ]] || return 1
      continue
    fi
    [[ -z "$actual_id" ||
      "$actual_id" =~ ^sha256:[a-f0-9]{64}$ ]] ||
      return 1
    if [[ -z "$actual_id" ]]; then
      continue
    fi
    if [[ "$expected_id" != pending &&
      "$actual_id" != "$expected_id" ]]; then
      return 1
    fi
    docker image rm "$reference" >/dev/null 2>&1 || return 1
    inspect_status=0
    inspect_image_reference_id "$reference" >/dev/null ||
      inspect_status=$?
    if [[ "$inspect_status" != 2 ]]; then
      return 1
    fi
  done
}

project_container_identity_matches() {
  local identifier="$1"
  local details identity name project_label service oneoff
  local restore_label kind extra confirmation
  details="$(
    docker container inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.service"}}|{{index .Config.Labels "com.docker.compose.oneoff"}}|{{index .Config.Labels "io.happylearn.phase5.restore-live"}}|{{index .Config.Labels "io.happylearn.phase5.restore-kind"}}' \
      "$identifier" 2>/dev/null
  )" || return 1
  IFS='|' read -r identity name project_label service oneoff \
    restore_label kind extra <<<"$details"
  [[ -z "$extra" &&
    "$identity" =~ ^[a-f0-9]{64}$ &&
    "$project_label" == "$PROJECT" ]] ||
    return 1
  name="${name#/}"
  case "$service:$oneoff" in
    postgres:False | redis:False | minio:False | app:False | worker:False)
      [[ "$name" == "${PROJECT}-${service}-1" ]] || return 1
      ;;
    app:True | backup:True | backup-storage-init:True)
      [[ "$name" =~ ^${PROJECT}-${service}-run-[a-z0-9]+$ ]] ||
        return 1
      ;;
    :)
      case "$kind:$name" in
        "source-object-fixture:${PROJECT}-object-fixture" | \
          "repository-cleanup:${PROJECT}-repository-cleanup")
          [[ "$restore_label" == "$FIXTURE_SUFFIX" ]] || return 1
          ;;
        *) return 1 ;;
      esac
      ;;
    *) return 1 ;;
  esac
  confirmation="$(
    docker container inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.service"}}|{{index .Config.Labels "com.docker.compose.oneoff"}}|{{index .Config.Labels "io.happylearn.phase5.restore-live"}}|{{index .Config.Labels "io.happylearn.phase5.restore-kind"}}' \
      "$identity" 2>/dev/null
  )" || return 1
  [[ "$confirmation" == "$details" ]] || return 1
  printf '%s\n' "$identity"
}

remove_labeled_project_containers() {
  local container identity listed
  listed="$(
    docker container ls --all --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )" || return 1
  while IFS= read -r container; do
    [[ -n "$container" ]] || continue
    identity="$(project_container_identity_matches "$container")" ||
      return 1
    docker container rm --force "$identity" >/dev/null || return 1
  done <<<"$listed"
}

volume_identity_matches() {
  local volume="$1"
  local details name project_label compose_volume extra
  [[ "$PROJECT" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ ]] ||
    return 1
  details="$(
    docker volume inspect --format \
      '{{.Name}}|{{index .Labels "com.docker.compose.project"}}|{{index .Labels "com.docker.compose.volume"}}' \
      "$volume" 2>/dev/null
  )" || return 1
  IFS='|' read -r name project_label compose_volume extra <<<"$details"
  case "$compose_volume" in
    minio_data | postgres_tls | backup_secrets) ;;
    *) return 1 ;;
  esac
  [[ -z "$extra" &&
    "$project_label" == "$PROJECT" &&
    "$name" == "${PROJECT}_${compose_volume}" &&
    "$volume" == "$name" ]]
}

remove_labeled_project_volumes() {
  local volume listed attachments
  listed="$(
    docker volume ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )" || return 1
  while IFS= read -r volume; do
    [[ -n "$volume" ]] || continue
    volume_identity_matches "$volume" || return 1
    attachments="$(
      docker ps -aq --filter "volume=${volume}"
    )" || return 1
    [[ -z "$attachments" ]] || return 1
    docker volume rm "$volume" >/dev/null || return 1
  done <<<"$listed"
}

project_network_identity_matches() {
  local network="$1"
  local details identity name project_label compose_network extra
  local confirmation
  details="$(
    docker network inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Labels "com.docker.compose.project"}}|{{index .Labels "com.docker.compose.network"}}' \
      "$network" 2>/dev/null
  )" || return 1
  IFS='|' read -r identity name project_label compose_network extra \
    <<<"$details"
  [[ -z "$extra" &&
    "$identity" =~ ^[a-f0-9]{64}$ &&
    "$project_label" == "$PROJECT" &&
    "$compose_network" == happylearn &&
    "$name" == "${PROJECT}_happylearn" &&
    "$network" == "$name" ]] ||
    return 1
  confirmation="$(
    docker network inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Labels "com.docker.compose.project"}}|{{index .Labels "com.docker.compose.network"}}' \
      "$identity" 2>/dev/null
  )" || return 1
  [[ "$confirmation" == "$details" ]] || return 1
  printf '%s\n' "$identity"
}

remove_labeled_project_networks() {
  local network identity listed
  listed="$(
    docker network ls --format '{{.Name}}' \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )" || return 1
  while IFS= read -r network; do
    [[ -n "$network" ]] || continue
    identity="$(project_network_identity_matches "$network")" ||
      return 1
    docker network rm "$identity" >/dev/null || return 1
  done <<<"$listed"
}

project_resources_absent() {
  local containers volumes networks
  [[ "$PROJECT" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ ]] ||
    return 1
  containers="$(
    docker ps -aq \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )" || return 1
  volumes="$(
    docker volume ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )" || return 1
  networks="$(
    docker network ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )" || return 1
  [[ -z "$containers" && -z "$volumes" && -z "$networks" ]]
}

cleanup_live() {
  trap '' HUP INT TERM
  local status="${1:-0}"
  if [[ "$CLEANED" == true ]]; then
    return "$status"
  fi
  CLEANED=true

  remove_restore_controller || status=1
  if [[ "$PROJECT" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ ]]; then
    cleanup_owned_restore_resources || status=1
    if [[ -n "$FIXTURE_ROOT" &&
      -d "$FIXTURE_ROOT/repository" &&
      ! -L "$FIXTURE_ROOT/repository" ]]; then
      handoff_repository_to_host_for_cleanup || status=1
    elif [[ -n "$FIXTURE_ROOT" &&
      "$FIXTURE_ROOT" == "${TMPDIR:-/tmp}/phase5-restore-live."* &&
      -d "$FIXTURE_ROOT" && ! -L "$FIXTURE_ROOT" &&
      ! -e "$FIXTURE_ROOT/repository" &&
      ! -L "$FIXTURE_ROOT/repository" &&
      "$(portable_owner "$FIXTURE_ROOT")" == "$(id -u)" ]]; then
      REPOSITORY_HOST_HANDOFF=true
    else
      status=1
    fi
    compose down --volumes --remove-orphans --timeout 30 \
      >/dev/null 2>&1 || status=1
    remove_labeled_project_containers || status=1
    remove_labeled_project_volumes || status=1
    remove_labeled_project_networks || status=1
    project_resources_absent || status=1
  fi
  remove_created_images || status=1

  if [[ -n "$FIXTURE_ROOT" &&
    "$FIXTURE_ROOT" == "${TMPDIR:-/tmp}/phase5-restore-live."* &&
    -d "$FIXTURE_ROOT" && ! -L "$FIXTURE_ROOT" ]]; then
    if [[ "$REPOSITORY_HOST_HANDOFF" == true &&
      "$(portable_owner "$FIXTURE_ROOT")" == "$(id -u)" ]]; then
      rm -rf "$FIXTURE_ROOT" || status=1
    else
      status=1
    fi
  fi
  return "$status"
}

verify_no_orphans() {
  [[ ! -e "$FIXTURE_ROOT" ]] || return 1
  project_resources_absent || return 1
  local image inspect_status
  for image in \
    "$BACKUP_IMAGE" "$APP_IMAGE" "$WORKER_IMAGE" \
    "$CONTROLLER_IMAGE" "$FIXTURE_IMAGE"; do
    inspect_status=0
    inspect_image_reference_id "$image" >/dev/null ||
      inspect_status=$?
    if [[ "$inspect_status" != 2 ]]; then
      return 1
    fi
  done
}

on_exit() {
  local status=$?
  local cleanup_status=0
  trap - EXIT HUP INT TERM
  if cleanup_live "$status"; then
    cleanup_status=0
  else
    cleanup_status=$?
  fi
  if [[ "$status" -ne 0 ]]; then
    exit "$status"
  fi
  exit "$cleanup_status"
}

main() {
  [[ "$#" -eq 0 ]] || fail 'this live gate accepts no arguments'
  require_dependencies
  trap on_exit EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM
  create_fixture
  build_images
  generate_age_identity
  configure_backup_context
  start_source_stack
  bootstrap_teacher
  build_source_object_fixture
  verify_source_object_fixture
  run_source_backup
  prepare_restore_repository_access
  run_empty_restore
  record_restore_verification
  restore_backup_repository_access

  local result_backup_id="$BACKUP_ID"
  local result_verification_id="$LIVE_REPORT_VERIFICATION_ID"
  local result_duration="$LIVE_REPORT_DURATION_SECONDS"
  local result_manifest="$LIVE_REPORT_MANIFEST_SHA256"
  local result_evidence="$LIVE_REPORT_EVIDENCE_SHA256"
  local result_rows="$LIVE_REPORT_ROW_COUNT_TOTAL"
  local result_isolation="$LIVE_REPORT_ISOLATION_404_PROBE_COUNT"
  cleanup_live 0 || fail 'live fixture cleanup failed'
  trap - EXIT HUP INT TERM
  verify_no_orphans || fail 'live fixture cleanup evidence failed'
  printf 'phase5 restore live: PASS backupId=%s verificationId=%s durationSeconds=%s manifestSHA256=%s evidenceSHA256=%s rowCountTotal=%s isolation404ProbeCount=%s\n' \
    "$result_backup_id" \
    "$result_verification_id" \
    "$result_duration" \
    "$result_manifest" \
    "$result_evidence" \
    "$result_rows" \
    "$result_isolation"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
