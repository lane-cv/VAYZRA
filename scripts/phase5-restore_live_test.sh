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
CONTROLLER_NAME=''
CONTROLLER_ID=''
CONTROLLER_CREATED=false
DOCKER_SOCKET=''
CREATED_IMAGE_RECORD=''
HOST_UID=''
HOST_GID=''
CLEANED=false

LIVE_REPORT_DURATION_SECONDS=''
LIVE_REPORT_MIGRATION_VERSION=''
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
  if stat --version 2>/dev/null | grep -Fq 'GNU coreutils'; then
    stat -c '%a' "$path"
  else
    stat -f '%Lp' "$path"
  fi
}

portable_owner() {
  local path="$1"
  if stat --version 2>/dev/null | grep -Fq 'GNU coreutils'; then
    stat -c '%u' "$path"
  else
    stat -f '%u' "$path"
  fi
}

portable_size() {
  local path="$1"
  if stat --version 2>/dev/null | grep -Fq 'GNU coreutils'; then
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
  local backup_id manifest_sha256 verification_sha256 evidence_sha256
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
  pattern='^\{"schemaVersion":1,"backupId":"([0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})","manifestSHA256":"([a-f0-9]{64})","verificationReportSHA256":"([a-f0-9]{64})","evidenceSHA256":"([a-f0-9]{64})","durationSeconds":(0|[1-9][0-9]*),"migrationVersion":(0|[1-9][0-9]*),"rowCountTotal":(0|[1-9][0-9]*),"checkedObjectCount":(0|[1-9][0-9]*),"missingObjectCount":(0|[1-9][0-9]*),"unexpectedObjectCount":(0|[1-9][0-9]*),"activeSessionCount":(0|[1-9][0-9]*),"isolation404ProbeCount":(0|[1-9][0-9]*),"reportSHA256":"([a-f0-9]{64})"\}$'
  [[ "$content" =~ $pattern ]] || return 1

  backup_id="${BASH_REMATCH[1]}"
  manifest_sha256="${BASH_REMATCH[2]}"
  verification_sha256="${BASH_REMATCH[3]}"
  evidence_sha256="${BASH_REMATCH[4]}"
  duration="${BASH_REMATCH[5]}"
  migration="${BASH_REMATCH[6]}"
  row_total="${BASH_REMATCH[7]}"
  checked="${BASH_REMATCH[8]}"
  missing="${BASH_REMATCH[9]}"
  unexpected="${BASH_REMATCH[10]}"
  active="${BASH_REMATCH[11]}"
  isolation="${BASH_REMATCH[12]}"
  report_sha256="${BASH_REMATCH[13]}"

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
    "$missing" == 0 &&
    "$unexpected" == 0 &&
    "$active" == 0 &&
    "$isolation" == 2 ]] ||
    return 1

  calculated_sha256="$(
    printf '%s\n' \
      'schemaVersion=1' \
      "backupId=$backup_id" \
      "manifestSHA256=$manifest_sha256" \
      "verificationReportSHA256=$verification_sha256" \
      "evidenceSHA256=$evidence_sha256" \
      "durationSeconds=$duration" \
      "migrationVersion=$migration" \
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

  LIVE_REPORT_DURATION_SECONDS="$duration"
  LIVE_REPORT_MIGRATION_VERSION="$migration"
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
  local class
  valid_uuid "$backup_id" || return 1
  for class in containers volumes networks; do
    case "$class" in
      containers)
        [[ -z "$(docker ps -aq \
          --filter "label=io.happylearn.phase5.restore-backup-id=${backup_id}")" ]] ||
          return 1
        ;;
      volumes)
        [[ -z "$(docker volume ls --quiet \
          --filter "label=io.happylearn.phase5.restore-backup-id=${backup_id}")" ]] ||
          return 1
        ;;
      networks)
        [[ -z "$(docker network ls --quiet \
          --filter "label=io.happylearn.phase5.restore-backup-id=${backup_id}")" ]] ||
          return 1
        ;;
    esac
  done
}

require_dependencies() {
  local license_size
  command -v docker >/dev/null || fail 'docker is required'
  command -v uuidgen >/dev/null || fail 'uuidgen is required'
  command -v sed >/dev/null || fail 'sed is required'
  portable_sha256_stdin </dev/null >/dev/null ||
    fail 'sha256sum or shasum is required'
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
  [[ "$LICENSE_SOURCE" == /* &&
    -f "$LICENSE_SOURCE" &&
    ! -L "$LICENSE_SOURCE" &&
    "$(portable_owner "$LICENSE_SOURCE")" == "$(id -u)" ]] ||
    fail 'AIStor license source must be an owned regular file'
  license_size="$(portable_size "$LICENSE_SOURCE")" ||
    fail 'AIStor license source size is unavailable'
  [[ "$license_size" -ge 1 && "$license_size" -le 65536 ]] ||
    fail 'AIStor license source has an invalid size'
}

new_uuid() {
  local value
  value="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  valid_uuid "$value" ||
    fail 'uuidgen did not return a canonical v4 UUID'
  printf '%s\n' "$value"
}

create_fixture() {
  local fixture_uuid compact_uuid
  local repository_password teacher_password
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
  CONTROLLER_NAME="${PROJECT}-restore-controller"
  CREATED_IMAGE_RECORD="$FIXTURE_ROOT/created-images"
  HOST_UID="$(id -u)"
  HOST_GID="$(id -g)"

  if [[ -n "$(docker ps -aq \
      --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    [[ -n "$(docker volume ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    [[ -n "$(docker network ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    docker image inspect "$BACKUP_IMAGE" >/dev/null 2>&1 ||
    docker image inspect "$APP_IMAGE" >/dev/null 2>&1 ||
    docker image inspect "$WORKER_IMAGE" >/dev/null 2>&1 ||
    docker image inspect "$CONTROLLER_IMAGE" >/dev/null 2>&1 ||
    docker container inspect "$CONTROLLER_NAME" >/dev/null 2>&1; then
    fail 'randomized live fixture collided with an existing resource'
  fi

  mkdir -m 0700 \
    "$FIXTURE_ROOT/secrets" \
    "$FIXTURE_ROOT/repository" \
    "$FIXTURE_ROOT/state" \
    "$FIXTURE_ROOT/control" \
    "$FIXTURE_ROOT/report" \
    "$FIXTURE_ROOT/offline" \
    "$FIXTURE_ROOT/logs" \
    "$FIXTURE_ROOT/controller-tmp"
  install -m 0400 "$LICENSE_SOURCE" "$LICENSE_COPY"
  repository_password="${compact_uuid}$(
    new_uuid |
      tr -d '-'
  )"
  teacher_password="Phase5 Restore ${compact_uuid}!"
  printf '%s\n' 'happylearn_dev' \
    >"$FIXTURE_ROOT/secrets/database_password"
  printf '%s\n' '/repository' \
    >"$FIXTURE_ROOT/secrets/local_repository"
  printf '%s\n' "$repository_password" \
    >"$FIXTURE_ROOT/secrets/local_password"
  printf '%s\n' "$teacher_password" >"$TEACHER_PASSWORD_FILE"
  printf '{"username":"%s","password":"%s"}\n' \
    "$TEACHER_USERNAME" "$teacher_password" >"$TEACHER_CREDENTIAL_FILE"
  chmod 0400 \
    "$FIXTURE_ROOT/secrets/database_password" \
    "$FIXTURE_ROOT/secrets/local_repository" \
    "$FIXTURE_ROOT/secrets/local_password"
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
  : >"$CREATED_IMAGE_RECORD"
  chmod 0600 "$BACKUP_LOG" "$RESTORE_LOG" "$CREATED_IMAGE_RECORD"
}

record_image() {
  local reference="$1"
  local image_id
  image_id="$(docker image inspect --format '{{.Id}}' "$reference")"
  [[ "$image_id" =~ ^sha256:[a-f0-9]{64}$ ]] ||
    fail 'built image ID is invalid'
  printf '%s|%s\n' "$reference" "$image_id" >>"$CREATED_IMAGE_RECORD"
}

build_restore_controller() {
  docker build \
    --file "$CONTROLLER_DOCKERFILE" \
    --tag "$CONTROLLER_IMAGE" \
    "$ROOT"
  record_image "$CONTROLLER_IMAGE"
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
}

generate_age_identity() {
  local identity_file="$FIXTURE_ROOT/offline/age.identity"
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
  export HAPPYLEARN_BACKUP_AGE_RECIPIENT="$(
    docker run --rm \
      --interactive \
      --network none \
      --read-only \
      --user 10003:0 \
      --cap-drop ALL \
      --security-opt no-new-privileges:true \
      --entrypoint /usr/local/bin/age-keygen \
      "$BACKUP_IMAGE" -y <"$identity_file"
  )"
  [[ "$HAPPYLEARN_BACKUP_AGE_RECIPIENT" =~ ^age1[0-9a-z]+$ ]] ||
    fail 'Age recipient generation failed'
}

configure_backup_context() {
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
}

wait_for_source_stack() {
  local deadline=$((SECONDS + 300))
  local service container_id state
  local all_healthy statuses=''
  while ((SECONDS < deadline)); do
    all_healthy=true
    statuses=''
    for service in postgres redis minio app worker; do
      container_id="$(compose ps --quiet "$service")"
      if [[ -z "$container_id" || "$container_id" == *$'\n'* ]]; then
        all_healthy=false
        statuses="${statuses}${service}=missing "
        continue
      fi
      state="$(
        docker container inspect --format \
          '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' \
          "$container_id"
      )"
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
  [[ "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" == "$FIXTURE_ROOT/repository" ]] ||
    return 1
  [[ ! -e "$FIXTURE_ROOT/host.lock" &&
    ! -L "$FIXTURE_ROOT/host.lock" ]] ||
    return 1
  [[ -z "$(docker ps --quiet \
    --filter "label=com.docker.compose.project=${PROJECT}" \
    --filter 'label=com.docker.compose.oneoff=True')" ]] ||
    return 1
}

prepare_restore_repository_access() {
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
      test -z "$(find -P /repository -xdev -type l -print -quit)"
      test -z "$(find -P /repository -xdev ! -type d ! -type f -print -quit)"
      test -z "$(find -P /repository -xdev -type f -links +1 -print -quit)"
      chown -R -- "$uid:$gid" /repository
      test "$(stat -c "%u:%g:%a" /repository)" = "$uid:$gid:700"
      test -z "$(find -P /repository -xdev ! -user "$uid" -print -quit)"
      test -z "$(find -P /repository -xdev ! -group "$gid" -print -quit)"
    ' handoff "$HOST_UID" "$HOST_GID" \
    >/dev/null
  [[ "$(portable_owner "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY")" == \
    "$HOST_UID" &&
    "$(portable_mode "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY")" == 700 ]] ||
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
      test -z "$(find -P /repository -xdev -type l -print -quit)"
      test -z "$(find -P /repository -xdev ! -type d ! -type f -print -quit)"
      test -z "$(find -P /repository -xdev -type f -links +1 -print -quit)"
      chown -R -- 10003:0 /repository
      test "$(stat -c "%u:%g:%a" /repository)" = "10003:0:700"
      test -z "$(find -P /repository -xdev ! -user 10003 -print -quit)"
      test -z "$(find -P /repository -xdev ! -group 0 -print -quit)"
    ' handback \
    >/dev/null
  compose run --rm --no-deps backup-storage-init >/dev/null
  compose run --rm --no-deps \
    --entrypoint /usr/bin/timeout \
    backup \
    --foreground --kill-after=10s 300s \
    restic --no-cache \
    --repository-file /run/secrets/local_repository \
    --password-file /run/secrets/local_password \
    cat config \
    >/dev/null
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

remove_restore_controller() {
  if [[ "$CONTROLLER_CREATED" == false ]]; then
    return 0
  fi
  controller_identity_matches || return 1
  docker container rm --force "$CONTROLLER_ID" >/dev/null 2>&1 ||
    return 1
  if docker container inspect "$CONTROLLER_ID" >/dev/null 2>&1 ||
    docker container inspect "$CONTROLLER_NAME" >/dev/null 2>&1; then
    return 1
  fi
  CONTROLLER_CREATED=false
  CONTROLLER_ID=''
}

run_restore_controller() {
  local container_status
  [[ "$CONTROLLER_CREATED" == false &&
    -z "$CONTROLLER_ID" &&
    -S "$DOCKER_SOCKET" &&
    ! -L "$DOCKER_SOCKET" ]] ||
    return 1
  CONTROLLER_ID="$(
    docker container create \
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
  )" || return 1
  [[ "$CONTROLLER_ID" =~ ^[a-f0-9]{64}$ ]] || return 1
  CONTROLLER_CREATED=true
  controller_identity_matches || return 1

  container_status=125
  if docker container start "$CONTROLLER_ID" >/dev/null; then
    container_status="$(
      docker container wait "$CONTROLLER_ID"
    )" || container_status=125
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
  assert_single_line_secret_absent "$LICENSE_COPY" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE" ||
    fail 'backup or restore evidence exposed the AIStor license'
  assert_age_identity_absent "$FIXTURE_ROOT/offline/age.identity" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE" ||
    fail 'backup or restore evidence exposed the Age identity'
  assert_restore_resources_absent "$BACKUP_ID" ||
    fail 'successful restore left disposable resources'
}

remove_created_images() {
  [[ -f "$CREATED_IMAGE_RECORD" && ! -L "$CREATED_IMAGE_RECORD" ]] ||
    return 0
  local reference expected_id actual_id index
  local -a records=()
  while IFS='|' read -r reference expected_id; do
    [[ -n "$reference" && -n "$expected_id" ]] || continue
    records+=("${reference}|${expected_id}")
  done <"$CREATED_IMAGE_RECORD"
  for ((index = ${#records[@]} - 1; index >= 0; index--)); do
    IFS='|' read -r reference expected_id <<<"${records[$index]}"
    actual_id="$(docker image inspect --format '{{.Id}}' \
      "$reference" 2>/dev/null || true)"
    if [[ "$actual_id" == "$expected_id" ]]; then
      docker image rm "$reference" >/dev/null 2>&1 || return 1
    elif [[ -n "$actual_id" ]]; then
      return 1
    fi
  done
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
  local volume
  local -a volumes=()
  while IFS= read -r volume; do
    [[ -n "$volume" ]] && volumes+=("$volume")
  done < <(
    docker volume ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}"
  )
  for volume in "${volumes[@]}"; do
    volume_identity_matches "$volume" || return 1
    [[ -z "$(docker ps -aq --filter "volume=${volume}")" ]] ||
      return 1
    docker volume rm "$volume" >/dev/null || return 1
  done
}

cleanup_live() {
  local status="${1:-0}"
  if [[ "$CLEANED" == true ]]; then
    return "$status"
  fi
  CLEANED=true

  remove_restore_controller || status=1
  if [[ "$PROJECT" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ ]]; then
    compose down --volumes --remove-orphans --timeout 30 \
      >/dev/null 2>&1 || status=1
    remove_labeled_project_volumes || status=1
    [[ -z "$(docker ps -aq \
      --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
      status=1
    [[ -z "$(docker volume ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
      status=1
    [[ -z "$(docker network ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
      status=1
  fi
  remove_created_images || status=1

  if [[ -n "$FIXTURE_ROOT" &&
    "$FIXTURE_ROOT" == "${TMPDIR:-/tmp}/phase5-restore-live."* &&
    -d "$FIXTURE_ROOT" && ! -L "$FIXTURE_ROOT" ]]; then
    chmod -R u+rwX "$FIXTURE_ROOT" 2>/dev/null || status=1
    rm -rf "$FIXTURE_ROOT" || status=1
  fi
  return "$status"
}

verify_no_orphans() {
  [[ ! -e "$FIXTURE_ROOT" ]] || return 1
  [[ -z "$(docker ps -aq \
    --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    return 1
  [[ -z "$(docker volume ls --quiet \
    --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    return 1
  [[ -z "$(docker network ls --quiet \
    --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    return 1
  local image
  for image in \
    "$BACKUP_IMAGE" "$APP_IMAGE" "$WORKER_IMAGE" "$CONTROLLER_IMAGE"; do
    if docker image inspect "$image" >/dev/null 2>&1; then
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
  run_source_backup
  prepare_restore_repository_access
  run_empty_restore
  restore_backup_repository_access

  local result_backup_id="$BACKUP_ID"
  local result_duration="$LIVE_REPORT_DURATION_SECONDS"
  local result_manifest="$LIVE_REPORT_MANIFEST_SHA256"
  local result_evidence="$LIVE_REPORT_EVIDENCE_SHA256"
  local result_rows="$LIVE_REPORT_ROW_COUNT_TOTAL"
  local result_isolation="$LIVE_REPORT_ISOLATION_404_PROBE_COUNT"
  cleanup_live 0 || fail 'live fixture cleanup failed'
  trap - EXIT HUP INT TERM
  verify_no_orphans || fail 'live fixture cleanup evidence failed'
  printf 'phase5 restore live: PASS backupId=%s durationSeconds=%s manifestSHA256=%s evidenceSHA256=%s rowCountTotal=%s isolation404ProbeCount=%s\n' \
    "$result_backup_id" \
    "$result_duration" \
    "$result_manifest" \
    "$result_evidence" \
    "$result_rows" \
    "$result_isolation"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
