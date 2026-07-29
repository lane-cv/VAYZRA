#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
umask 077

readonly REMOTE_IMAGE='quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120'
readonly ORCHESTRATOR_PROJECT='happylearn-dev'
readonly MAX_CPU_PERCENT='200'
readonly MAX_MEMORY_MIB='4096'

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
COMPOSE_FILE="$ROOT/deploy/compose.dev.yml"
COMPOSE_LIVE_FILE="$ROOT/deploy/compose.backup-live.yml"
PROJECT=''
COMPOSE_APP_IMAGE=''
COMPOSE_WORKER_IMAGE=''
FIXTURE_ROOT=''
FIXTURE_SUFFIX=''
REMOTE_NAME=''
REMOTE_VOLUME=''
REMOTE_ENV_FILE=''
BACKUP_BASE_IMAGE=''
BACKUP_CA_IMAGE=''
AGE_IDENTITY_FILE=''
CREATED_IMAGE_RECORD=''
COMPOSE_CONTAINER_RECORD=''
COMPOSE_VOLUME_RECORD=''
COMPOSE_NETWORK_RECORD=''
CA_PROBE_NAME=''
REMOTE_CONTAINER_CREATED=false
REMOTE_VOLUME_CREATED=false
CLEANED=false

fail() {
  printf 'phase5 backup live: %s\n' "$1" >&2
  exit 1
}

compose() {
  docker compose \
    --project-name "$PROJECT" \
    --file "$COMPOSE_FILE" \
    --file "$COMPOSE_LIVE_FILE" \
    "$@"
}

valid_snapshot_id() {
  [[ "$1" =~ ^[0-9a-f]{64}$ ]]
}

valid_uuid() {
  [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]]
}

new_uuid() {
  local value
  value="$(uuidgen | tr '[:upper:]' '[:lower:]')"
  valid_uuid "$value" || fail 'uuidgen did not return a canonical v4 UUID'
  printf '%s\n' "$value"
}

remove_compose_resources() {
  local container_id
  local volume_name
  local network_id
  local project_label
  [[ -n "$COMPOSE_CONTAINER_RECORD" &&
    -n "$COMPOSE_VOLUME_RECORD" &&
    -n "$COMPOSE_NETWORK_RECORD" ]] || return 0
  while IFS= read -r container_id; do
    [[ -n "$container_id" ]] || continue
    project_label="$(docker container inspect --format \
      '{{index .Config.Labels "com.docker.compose.project"}}' \
      "$container_id" 2>/dev/null || true)"
    [[ -z "$project_label" ]] && continue
    if [[ "$project_label" != "$PROJECT" ]]; then
      printf 'phase5 backup live: container ownership changed\n' >&2
      return 1
    fi
    docker rm --force "$container_id" >/dev/null 2>&1 || return 1
  done <"$COMPOSE_CONTAINER_RECORD"
  while IFS= read -r volume_name; do
    [[ -n "$volume_name" ]] || continue
    project_label="$(docker volume inspect --format \
      '{{index .Labels "com.docker.compose.project"}}' \
      "$volume_name" 2>/dev/null || true)"
    [[ -z "$project_label" ]] && continue
    if [[ "$project_label" != "$PROJECT" ]]; then
      printf 'phase5 backup live: Compose volume ownership changed\n' >&2
      return 1
    fi
    docker volume rm "$volume_name" >/dev/null 2>&1 || return 1
  done <"$COMPOSE_VOLUME_RECORD"
  while IFS= read -r network_id; do
    [[ -n "$network_id" ]] || continue
    project_label="$(docker network inspect --format \
      '{{index .Labels "com.docker.compose.project"}}' \
      "$network_id" 2>/dev/null || true)"
    [[ -z "$project_label" ]] && continue
    if [[ "$project_label" != "$PROJECT" ]]; then
      printf 'phase5 backup live: Compose network ownership changed\n' >&2
      return 1
    fi
    docker network rm "$network_id" >/dev/null 2>&1 || return 1
  done <"$COMPOSE_NETWORK_RECORD"
}

record_compose_resources() {
  docker ps -aq \
    --filter "label=com.docker.compose.project=${PROJECT}" \
    >>"$COMPOSE_CONTAINER_RECORD"
  docker volume ls --quiet \
    --filter "label=com.docker.compose.project=${PROJECT}" \
    >>"$COMPOSE_VOLUME_RECORD"
  docker network ls --quiet \
    --filter "label=com.docker.compose.project=${PROJECT}" \
    >>"$COMPOSE_NETWORK_RECORD"
}

remove_created_images() {
  [[ -n "$CREATED_IMAGE_RECORD" && -f "$CREATED_IMAGE_RECORD" ]] || return 0
  local reference
  local expected_id
  local actual_id
  local index
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
      printf 'phase5 backup live: image ownership changed\n' >&2
      return 1
    fi
  done
}

cleanup_live() {
  local status="${1:-0}"
  if [[ "$CLEANED" == true ]]; then
    return "$status"
  fi
  CLEANED=true
  if [[ "$REMOTE_CONTAINER_CREATED" == true && -n "$REMOTE_NAME" ]]; then
    local remote_label
    remote_label="$(docker container inspect --format \
      '{{index .Config.Labels "io.happylearn.phase5-live"}}' \
      "$REMOTE_NAME" 2>/dev/null || true)"
    if [[ "$remote_label" == "$FIXTURE_SUFFIX" ]]; then
      docker rm --force "$REMOTE_NAME" >/dev/null 2>&1 || status=1
    elif [[ -n "$remote_label" ]]; then
      printf 'phase5 backup live: remote container ownership changed\n' >&2
      status=1
    fi
  fi
  if [[ "$REMOTE_VOLUME_CREATED" == true && -n "$REMOTE_VOLUME" ]]; then
    local volume_label
    volume_label="$(docker volume inspect --format \
      '{{index .Labels "io.happylearn.phase5-live"}}' \
      "$REMOTE_VOLUME" 2>/dev/null || true)"
    if [[ "$volume_label" == "$FIXTURE_SUFFIX" ]]; then
      docker volume rm "$REMOTE_VOLUME" >/dev/null 2>&1 || status=1
    elif [[ -n "$volume_label" ]]; then
      printf 'phase5 backup live: remote volume ownership changed\n' >&2
      status=1
    fi
  fi
  if [[ -n "$CA_PROBE_NAME" ]]; then
    local probe_label
    probe_label="$(docker container inspect --format \
      '{{index .Config.Labels "io.happylearn.phase5-live"}}' \
      "$CA_PROBE_NAME" 2>/dev/null || true)"
    if [[ "$probe_label" == "$FIXTURE_SUFFIX" ]]; then
      docker rm --force "$CA_PROBE_NAME" >/dev/null 2>&1 || status=1
    elif [[ -n "$probe_label" ]]; then
      printf 'phase5 backup live: CA probe ownership changed\n' >&2
      status=1
    fi
  fi
  if [[ "$PROJECT" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ &&
    -n "$COMPOSE_CONTAINER_RECORD" &&
    -f "$COMPOSE_CONTAINER_RECORD" ]]; then
    record_compose_resources || status=1
  fi
  remove_compose_resources || status=1
  remove_created_images || status=1
  if [[ -n "$FIXTURE_ROOT" &&
    "$FIXTURE_ROOT" == "${TMPDIR:-/tmp}/phase5-backup-live."* &&
    -d "$FIXTURE_ROOT" ]]; then
    chmod -R u+rwX "$FIXTURE_ROOT" 2>/dev/null || status=1
    rm -rf "$FIXTURE_ROOT" || status=1
  fi
  return "$status"
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
trap on_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

require_dependencies() {
  command -v docker >/dev/null || fail 'docker is required'
  command -v openssl >/dev/null || fail 'openssl is required'
  command -v uuidgen >/dev/null || fail 'uuidgen is required'
  docker compose version >/dev/null || fail 'Docker Compose v2 is required'
  [[ -f "$COMPOSE_LIVE_FILE" ]] ||
    fail 'fixed live Compose override is missing'
  [[ -n "${HAPPYLEARN_AISTOR_LICENSE_FILE:-}" ]] ||
    fail 'set HAPPYLEARN_AISTOR_LICENSE_FILE'
  [[ -f "$HAPPYLEARN_AISTOR_LICENSE_FILE" &&
    ! -L "$HAPPYLEARN_AISTOR_LICENSE_FILE" &&
    -s "$HAPPYLEARN_AISTOR_LICENSE_FILE" ]] ||
    fail 'AIStor license path must name a nonempty regular file'
}

create_fixture() {
  local remote_access_key
  local remote_secret_key
  local local_repository_password
  local remote_repository_password
  FIXTURE_ROOT="$(mktemp -d \
    "${TMPDIR:-/tmp}/phase5-backup-live.XXXXXX")"
  FIXTURE_SUFFIX="$(
    uuidgen |
      tr '[:upper:]' '[:lower:]' |
      tr -d '-' |
      cut -c1-12
  )"
  [[ "$FIXTURE_SUFFIX" =~ ^[a-f0-9]{12}$ ]] ||
    fail 'could not create fixture suffix'
  PROJECT="happylearn-phase5-live-${FIXTURE_SUFFIX}"
  COMPOSE_APP_IMAGE="${PROJECT}-app"
  COMPOSE_WORKER_IMAGE="${PROJECT}-worker"
  REMOTE_NAME="phase5-remote-${FIXTURE_SUFFIX}"
  REMOTE_VOLUME="phase5-remote-data-${FIXTURE_SUFFIX}"
  REMOTE_ENV_FILE="$FIXTURE_ROOT/remote.env"
  CA_PROBE_NAME="phase5-ca-negative-${FIXTURE_SUFFIX}"
  BACKUP_BASE_IMAGE="happylearn-backup:phase5-live-base-${FIXTURE_SUFFIX}"
  BACKUP_CA_IMAGE="happylearn-backup:phase5-live-ca-${FIXTURE_SUFFIX}"
  AGE_IDENTITY_FILE="$FIXTURE_ROOT/offline/age.identity"
  CREATED_IMAGE_RECORD="$FIXTURE_ROOT/created-images"
  COMPOSE_CONTAINER_RECORD="$FIXTURE_ROOT/compose-containers"
  COMPOSE_VOLUME_RECORD="$FIXTURE_ROOT/compose-volumes"
  COMPOSE_NETWORK_RECORD="$FIXTURE_ROOT/compose-networks"
  remote_access_key="p5${FIXTURE_SUFFIX}"
  remote_secret_key="$(
    uuidgen |
      tr '[:upper:]' '[:lower:]' |
      tr -d '-'
  )$(
    uuidgen |
      tr '[:upper:]' '[:lower:]' |
      tr -d '-'
  )"
  local_repository_password="$(
    uuidgen |
      tr '[:upper:]' '[:lower:]' |
      tr -d '-'
  )"
  remote_repository_password="$(
    uuidgen |
      tr '[:upper:]' '[:lower:]' |
      tr -d '-'
  )"
  if [[ -n "$(docker ps -aq \
      --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    [[ -n "$(docker volume ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    [[ -n "$(docker network ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    docker container inspect "$REMOTE_NAME" >/dev/null 2>&1 ||
    docker container inspect "$CA_PROBE_NAME" >/dev/null 2>&1 ||
    docker volume inspect "$REMOTE_VOLUME" >/dev/null 2>&1 ||
    docker image inspect "$BACKUP_BASE_IMAGE" >/dev/null 2>&1 ||
    docker image inspect "$BACKUP_CA_IMAGE" >/dev/null 2>&1 ||
    docker image inspect "$COMPOSE_APP_IMAGE" >/dev/null 2>&1 ||
    docker image inspect "$COMPOSE_WORKER_IMAGE" >/dev/null 2>&1; then
    fail 'randomized fixture resource name unexpectedly collided'
  fi

  mkdir -m 0700 \
    "$FIXTURE_ROOT/secrets" \
    "$FIXTURE_ROOT/repository" \
    "$FIXTURE_ROOT/state" \
    "$FIXTURE_ROOT/offline" \
    "$FIXTURE_ROOT/ca-context" \
    "$FIXTURE_ROOT/server-certs" \
    "$FIXTURE_ROOT/server-certs/CAs" \
    "$FIXTURE_ROOT/results"
  printf '%s\n' 'happylearn_dev' \
    >"$FIXTURE_ROOT/secrets/database_password"
  printf '%s\n' '/repository' \
    >"$FIXTURE_ROOT/secrets/local_repository"
  printf '%s\n' "$local_repository_password" \
    >"$FIXTURE_ROOT/secrets/local_password"
  printf 's3:https://%s:9000/happylearn-backups\n' "$REMOTE_NAME" \
    >"$FIXTURE_ROOT/secrets/remote_repository"
  printf '%s\n' "$remote_repository_password" \
    >"$FIXTURE_ROOT/secrets/remote_password"
  printf '%s\n' "$remote_access_key" \
    >"$FIXTURE_ROOT/secrets/remote_access_key_id"
  printf '%s\n' "$remote_secret_key" \
    >"$FIXTURE_ROOT/secrets/remote_secret_access_key"
  chmod 0400 "$FIXTURE_ROOT/secrets/"*
  printf 'MINIO_ROOT_USER=%s\n' "$remote_access_key" >"$REMOTE_ENV_FILE"
  printf 'MINIO_ROOT_PASSWORD=%s\n' "$remote_secret_key" >>"$REMOTE_ENV_FILE"
  printf '%s\n' 'SSL_CERT_FILE=/certs/CAs/ca.crt' >>"$REMOTE_ENV_FILE"
  chmod 0600 "$REMOTE_ENV_FILE"
  : >"$CREATED_IMAGE_RECORD"
  : >"$COMPOSE_CONTAINER_RECORD"
  : >"$COMPOSE_VOLUME_RECORD"
  : >"$COMPOSE_NETWORK_RECORD"
}

create_fixture_ca() {
  openssl genrsa -out "$FIXTURE_ROOT/offline/ca.key" 2048 \
    >/dev/null 2>&1
  openssl req -x509 -new -sha256 -days 2 \
    -key "$FIXTURE_ROOT/offline/ca.key" \
    -subj "/CN=HappyLearn Phase5 Live CA ${FIXTURE_SUFFIX}" \
    -out "$FIXTURE_ROOT/offline/ca.crt" >/dev/null 2>&1
  openssl genrsa -out "$FIXTURE_ROOT/server-certs/private.key" 2048 \
    >/dev/null 2>&1
  openssl req -new -sha256 \
    -key "$FIXTURE_ROOT/server-certs/private.key" \
    -subj "/CN=${REMOTE_NAME}" \
    -out "$FIXTURE_ROOT/offline/server.csr" >/dev/null 2>&1
  printf 'subjectAltName=DNS:%s\nextendedKeyUsage=serverAuth\n' \
    "$REMOTE_NAME" >"$FIXTURE_ROOT/offline/server.ext"
  openssl x509 -req -sha256 -days 2 \
    -in "$FIXTURE_ROOT/offline/server.csr" \
    -CA "$FIXTURE_ROOT/offline/ca.crt" \
    -CAkey "$FIXTURE_ROOT/offline/ca.key" \
    -CAcreateserial \
    -extfile "$FIXTURE_ROOT/offline/server.ext" \
    -out "$FIXTURE_ROOT/server-certs/public.crt" >/dev/null 2>&1
  cp "$FIXTURE_ROOT/offline/ca.crt" \
    "$FIXTURE_ROOT/server-certs/CAs/ca.crt"
  cp "$FIXTURE_ROOT/offline/ca.crt" \
    "$FIXTURE_ROOT/ca-context/ca.crt"
  chmod 0400 "$FIXTURE_ROOT/offline/ca.key" \
    "$FIXTURE_ROOT/server-certs/private.key"
  chmod 0444 "$FIXTURE_ROOT/offline/ca.crt" \
    "$FIXTURE_ROOT/server-certs/public.crt" \
    "$FIXTURE_ROOT/server-certs/CAs/ca.crt" \
    "$FIXTURE_ROOT/ca-context/ca.crt"
}

record_image() {
  local reference="$1"
  local image_id
  image_id="$(docker image inspect --format '{{.Id}}' "$reference")"
  [[ -n "$image_id" ]] || fail "could not record image: ${reference}"
  printf '%s|%s\n' "$reference" "$image_id" >>"$CREATED_IMAGE_RECORD"
}

build_backup_images() {
  docker build --file "$ROOT/Dockerfile.backup" \
    --tag "$BACKUP_BASE_IMAGE" "$ROOT"
  record_image "$BACKUP_BASE_IMAGE"
  docker build \
    --build-arg "HAPPYLEARN_BACKUP_BASE_IMAGE=${BACKUP_BASE_IMAGE}" \
    --file "$ROOT/deploy/Dockerfile.backup-live-ca" \
    --tag "$BACKUP_CA_IMAGE" "$FIXTURE_ROOT/ca-context"
  record_image "$BACKUP_CA_IMAGE"

  docker run --rm --entrypoint /usr/local/bin/age-keygen \
    "$BACKUP_BASE_IMAGE" >"$AGE_IDENTITY_FILE" 2>/dev/null
  chmod 0400 "$AGE_IDENTITY_FILE"
  HAPPYLEARN_BACKUP_AGE_RECIPIENT="$(
    docker run --rm --interactive \
      --entrypoint /usr/local/bin/age-keygen \
      "$BACKUP_BASE_IMAGE" -y <"$AGE_IDENTITY_FILE"
  )"
  [[ "$HAPPYLEARN_BACKUP_AGE_RECIPIENT" =~ ^age1[0-9a-z]+$ ]] ||
    fail 'generated Age recipient was invalid'
  export HAPPYLEARN_BACKUP_AGE_RECIPIENT
}

start_base_stack() {
  export HAPPYLEARN_BACKUP_LIVE_TEST='1'
  export HAPPYLEARN_BACKUP_LIVE_PROJECT="$PROJECT"
  export HAPPYLEARN_BACKUP_LIVE_ROOT="$FIXTURE_ROOT"
  export HAPPYLEARN_BACKUP_IMAGE="$BACKUP_CA_IMAGE"
  export HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$FIXTURE_ROOT/secrets"
  export HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$FIXTURE_ROOT/repository"
  export HAPPYLEARN_BACKUP_STATE_DIRECTORY="$FIXTURE_ROOT/state"
  export HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID='phase5-live-key'
  export HAPPYLEARN_BACKUP_LOCK_DIRECTORY="$FIXTURE_ROOT/host.lock"
  export HAPPYLEARN_BACKUP_EXTERNAL_TIMEOUT_SECONDS='30'
  export HAPPYLEARN_BACKUP_DATABASE_QUERY_TIMEOUT_SECONDS='30'
  compose build app
  record_image "$COMPOSE_APP_IMAGE"
  compose build worker
  record_image "$COMPOSE_WORKER_IMAGE"
  if ! compose create postgres redis minio app worker; then
    record_compose_resources
    return 1
  fi
  record_compose_resources
  compose up --detach --no-build postgres redis minio app worker
  record_compose_resources
  wait_base_stack
}

wait_base_stack() {
  local deadline=$((SECONDS + 300))
  local service
  local container_id
  local state
  local all_healthy
  local statuses=''
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
  printf 'phase5 backup live: base status %s\n' "$statuses" >&2
  return 1
}

wait_remote_ready() {
  local deadline=$((SECONDS + 120))
  while ((SECONDS < deadline)); do
    if docker exec "$REMOTE_NAME" curl --fail --silent \
      --cacert /certs/CAs/ca.crt \
      --resolve "${REMOTE_NAME}:9000:127.0.0.1" \
      "https://${REMOTE_NAME}:9000/minio/health/ready" \
      >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

start_remote_fixture() {
  local network_id
  local entrypoint
  local command_prefix
  network_id="$(
    docker network ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}" \
      --filter 'label=com.docker.compose.network=happylearn'
  )"
  [[ -n "$network_id" && "$network_id" != *$'\n'* ]] ||
    fail 'could not identify the isolated Compose network'
  if docker container inspect "$REMOTE_NAME" >/dev/null 2>&1 ||
    docker volume inspect "$REMOTE_VOLUME" >/dev/null 2>&1; then
    fail 'unique remote fixture name unexpectedly collided'
  fi
  docker volume create \
    --label "io.happylearn.phase5-live=${FIXTURE_SUFFIX}" \
    "$REMOTE_VOLUME" >/dev/null
  REMOTE_VOLUME_CREATED=true
  entrypoint="$(docker image inspect --format \
    '{{json .Config.Entrypoint}}' "$REMOTE_IMAGE")"
  case "$entrypoint" in
    '["minio"]'|'["/usr/bin/minio"]'|'["/usr/local/bin/minio"]')
      command_prefix='server'
      ;;
    *)
      command_prefix='minio server'
      ;;
  esac
  docker run --detach \
    --name "$REMOTE_NAME" \
    --label "io.happylearn.phase5-live=${FIXTURE_SUFFIX}" \
    --network "$network_id" \
    --network-alias "$REMOTE_NAME" \
    --user 0:0 \
    --read-only \
    --tmpfs /tmp:rw,noexec,nosuid,size=16m \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --memory 512m \
    --cpus 0.3 \
    --env-file "$REMOTE_ENV_FILE" \
    --volume "$REMOTE_VOLUME:/data" \
    --volume "$FIXTURE_ROOT/server-certs:/certs:ro" \
    --volume "$HAPPYLEARN_AISTOR_LICENSE_FILE:/minio.license:ro" \
    "$REMOTE_IMAGE" \
    $command_prefix /data \
      --address :9000 \
      --console-address :9001 \
      --certs-dir /certs \
      --license /minio.license >/dev/null
  REMOTE_CONTAINER_CREATED=true
  wait_remote_ready ||
    fail 'remote HTTPS S3 fixture did not become ready'
}

create_remote_bucket() {
  docker exec "$REMOTE_NAME" /bin/sh -eu -c '
    export MC_CONFIG_DIR=/tmp/mc
    client=
    for candidate in /usr/bin/mc /usr/local/bin/mc /usr/bin/mcli /usr/local/bin/mcli; do
      if test -x "$candidate"; then
        client="$candidate"
        break
      fi
    done
    test -n "$client"
    export MC_HOST_phase5="https://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@'"$REMOTE_NAME"':9000"
    "$client" mb --ignore-existing phase5/happylearn-backups >/dev/null
  ' || fail 'could not create the isolated remote S3 bucket'
}

assert_ca_isolation() {
  local network_id
  local secrets_volume
  network_id="$(
    docker network ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}" \
      --filter 'label=com.docker.compose.network=happylearn'
  )"
  secrets_volume="$(
    docker volume ls --quiet \
      --filter "label=com.docker.compose.project=${PROJECT}" \
      --filter 'label=com.docker.compose.volume=backup_secrets'
  )"
  [[ -n "$network_id" && "$network_id" != *$'\n'* &&
    -n "$secrets_volume" && "$secrets_volume" != *$'\n'* ]] ||
    fail 'could not resolve CA-isolation probe resources'
  if docker run --rm \
    --name "$CA_PROBE_NAME" \
    --label "io.happylearn.phase5-live=${FIXTURE_SUFFIX}" \
    --network "$network_id" \
    --read-only \
    --user 10003:0 \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --tmpfs /tmp:rw,noexec,nosuid,size=8m,uid=10003,gid=0,mode=0700 \
    --volume "$secrets_volume:/run/secrets:ro" \
    --entrypoint /usr/bin/timeout \
    "$BACKUP_BASE_IMAGE" --foreground --kill-after=5s 30s /bin/sh -eu -c '
      export RESTIC_REPOSITORY_FILE=/run/secrets/remote_repository
      export RESTIC_PASSWORD_FILE=/run/secrets/remote_password
      export AWS_ACCESS_KEY_ID="$(sed -n "1p" /run/secrets/remote_access_key_id)"
      export AWS_SECRET_ACCESS_KEY="$(sed -n "1p" /run/secrets/remote_secret_access_key)"
      exec restic --no-cache cat config
    ' >/dev/null 2>&1; then
    fail 'base backup image unexpectedly trusted the fixture CA'
  fi
  restic_fixture remote cat config >/dev/null ||
    fail 'derived backup image did not trust the fixture CA'
}

db_query() {
  compose exec -T \
    -e PGCONNECTTIMEOUT=5 \
    -e "PGOPTIONS=-c statement_timeout=10000 -c lock_timeout=10000" \
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
  [[ -n "$value" && "$value" != *$'\n'* ]] ||
    fail 'database scalar query returned an invalid shape'
  printf '%s\n' "$value"
}

timestamp_days_ago() {
  local days="$1"
  local hour="$2"
  [[ "$days" =~ ^[0-9]+$ && "$hour" =~ ^[0-9]+$ &&
    "$hour" -le 23 ]] || fail 'invalid fixture day offset'
  db_scalar "
SELECT to_char(
  (
    (date_trunc('day',clock_timestamp() AT TIME ZONE 'Asia/Shanghai')
      - make_interval(days => ${days})
      + make_interval(hours => ${hour}))
    AT TIME ZONE 'Asia/Shanghai'
  ) AT TIME ZONE 'UTC',
  'YYYY-MM-DD HH24:MI:SS'
);"
}

timestamp_months_ago() {
  local months="$1"
  [[ "$months" =~ ^[0-9]+$ ]] || fail 'invalid fixture month offset'
  db_scalar "
SELECT to_char(
  (
    (date_trunc('month',clock_timestamp() AT TIME ZONE 'Asia/Shanghai')
      - make_interval(months => ${months})
      + interval '12 hours')
    AT TIME ZONE 'Asia/Shanghai'
  ) AT TIME ZONE 'UTC',
  'YYYY-MM-DD HH24:MI:SS'
);"
}

timestamp_hours_ago() {
  local hours="$1"
  [[ "$hours" =~ ^[0-9]+$ ]] || fail 'invalid fixture hour offset'
  db_scalar "
SELECT to_char(
  (clock_timestamp()-make_interval(hours => ${hours})) AT TIME ZONE 'UTC',
  'YYYY-MM-DD HH24:MI:SS'
);"
}

restic_fixture() {
  local repository="$1"
  shift
  case "$repository" in local|remote) ;; *) fail 'invalid fixture repository' ;; esac
  compose run --rm --no-deps --entrypoint /bin/sh backup -eu -c '
    repository="$1"
    shift
    case "$repository" in
      local)
        export RESTIC_REPOSITORY_FILE=/run/secrets/local_repository
        export RESTIC_PASSWORD_FILE=/run/secrets/local_password
        ;;
      remote)
        export RESTIC_REPOSITORY_FILE=/run/secrets/remote_repository
        export RESTIC_PASSWORD_FILE=/run/secrets/remote_password
        export AWS_ACCESS_KEY_ID="$(sed -n "1p" /run/secrets/remote_access_key_id)"
        export AWS_SECRET_ACCESS_KEY="$(sed -n "1p" /run/secrets/remote_secret_access_key)"
        ;;
      *) exit 2 ;;
    esac
    exec /usr/bin/timeout --foreground --kill-after=5s 300s \
      restic --no-cache "$@"
  ' fixture "$repository" "$@"
}

seed_repository_snapshot() {
  local repository="$1"
  local requested_at="$2"
  local run_id="$3"
  local host="$4"
  local ownership="${5:-owned}"
  local output
  local snapshot_ids
  case "$ownership" in owned|unowned) ;; *) fail 'invalid seed ownership' ;; esac
  output="$(
    compose run --rm --no-deps --entrypoint /bin/sh backup -eu -c '
      repository="$1"
      requested_at="$2"
      run_id="$3"
      host="$4"
      ownership="$5"
      seed_path="/work/seed-$run_id"
      mkdir -m 0700 "$seed_path"
      printf "%s\n" "$run_id" >"$seed_path/payload"
      case "$repository" in
        local)
          export RESTIC_REPOSITORY_FILE=/run/secrets/local_repository
          export RESTIC_PASSWORD_FILE=/run/secrets/local_password
          ;;
        remote)
          export RESTIC_REPOSITORY_FILE=/run/secrets/remote_repository
          export RESTIC_PASSWORD_FILE=/run/secrets/remote_password
          export AWS_ACCESS_KEY_ID="$(sed -n "1p" /run/secrets/remote_access_key_id)"
          export AWS_SECRET_ACCESS_KEY="$(sed -n "1p" /run/secrets/remote_secret_access_key)"
          ;;
        *) exit 2 ;;
      esac
      if test "$ownership" = owned; then
        restic --no-cache backup --json \
          --time "$requested_at" \
          --host "$host" \
          --tag "happylearn-batch:$run_id" \
          "$seed_path"
      else
        restic --no-cache backup --json \
          --time "$requested_at" \
          --host "$host" \
          "$seed_path"
      fi
    ' fixture "$repository" "$requested_at" "$run_id" "$host" "$ownership"
  )"
  snapshot_ids="$(
    sed -n \
      's/.*"snapshot_id":"\([0-9a-f][0-9a-f]*\)".*/\1/p' \
      <<<"$output"
  )"
  valid_snapshot_id "$snapshot_ids" ||
    fail 'seed backup did not produce one full Restic snapshot ID'
  printf '%s\n' "$snapshot_ids"
}

insert_seed_run() {
  local run_id="$1"
  local trigger="$2"
  local requested_at="$3"
  local local_snapshot_id="$4"
  local remote_snapshot_id="${5:-}"
  local state="${6:-succeeded}"
  valid_uuid "$run_id" || fail 'invalid seed run UUID'
  valid_snapshot_id "$local_snapshot_id" ||
    fail 'invalid local seed snapshot ID'
  case "$trigger" in manual|pre_release) ;; *) fail 'invalid seed trigger' ;; esac
  case "$state" in succeeded|failed) ;; *) fail 'invalid seed state' ;; esac
  local remote_value='NULL'
  local remote_artifacts=''
  if [[ -n "$remote_snapshot_id" ]]; then
    valid_snapshot_id "$remote_snapshot_id" ||
      fail 'invalid remote seed snapshot ID'
    remote_value="'${remote_snapshot_id}'"
    remote_artifacts="
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,
  verified_at,expires_at
)
SELECT '${run_id}',kind,'remote','${remote_snapshot_id}',
       digest('${remote_snapshot_id}' || kind,'sha256'),0,
       '${requested_at}'::timestamptz,
       '${requested_at}'::timestamptz+interval '365 days'
FROM unnest(ARRAY['database_dump','object_snapshot','manifest']) AS kind;"
  fi
  db_query >/dev/null <<SQL
BEGIN;
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,started_at,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  remote_snapshot_id,manifest_sha256,logical_bytes,stored_bytes,
  local_expires_at,remote_expires_at
) VALUES(
  '${run_id}','phase5-live-${run_id}','${trigger}','${state}',
  '${requested_at}'::timestamptz,'${requested_at}'::timestamptz,
  '${requested_at}'::timestamptz,1,'phase5-live-seed',
  '${local_snapshot_id}',${remote_value},
  digest('${local_snapshot_id}','sha256'),0,0,
  '${requested_at}'::timestamptz+interval '7 days',
  CASE WHEN ${remote_value} IS NOT NULL
    THEN '${requested_at}'::timestamptz+interval '365 days'
    ELSE NULL END
);
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,
  verified_at,expires_at
)
SELECT '${run_id}',kind,'local','${local_snapshot_id}',
       digest('${local_snapshot_id}' || kind,'sha256'),0,
       '${requested_at}'::timestamptz,
       '${requested_at}'::timestamptz+interval '365 days'
FROM unnest(ARRAY['database_dump','object_snapshot','manifest']) AS kind;
${remote_artifacts}
COMMIT;
SQL
}

seed_failed_run() {
  local requested_at="$1"
  local label="$2"
  local run_id
  local local_snapshot_id
  local remote_snapshot_id
  run_id="$(new_uuid)"
  local_snapshot_id="$(seed_repository_snapshot \
    local "$requested_at" "$run_id" \
    "phase5-failed-local-${label}-${FIXTURE_SUFFIX}")"
  remote_snapshot_id="$(seed_repository_snapshot \
    remote "$requested_at" "$run_id" \
    "phase5-failed-remote-${label}-${FIXTURE_SUFFIX}")"
  insert_seed_run "$run_id" manual "$requested_at" \
    "$local_snapshot_id" "$remote_snapshot_id" failed
  printf '%s|%s\n' "$local_snapshot_id" "$remote_snapshot_id"
}

seed_run() {
  local repository="$1"
  local trigger="$2"
  local requested_at="$3"
  local label="$4"
  local run_id
  local local_snapshot_id
  local remote_snapshot_id=''
  run_id="$(new_uuid)"
  local_snapshot_id="$(seed_repository_snapshot \
    local "$requested_at" "$run_id" \
    "phase5-local-${label}-${FIXTURE_SUFFIX}")"
  if [[ "$repository" == remote ]]; then
    remote_snapshot_id="$(seed_repository_snapshot \
      remote "$requested_at" "$run_id" \
      "phase5-remote-${label}-${FIXTURE_SUFFIX}")"
  elif [[ "$repository" != local ]]; then
    fail 'invalid seed repository'
  fi
  insert_seed_run "$run_id" "$trigger" "$requested_at" \
    "$local_snapshot_id" "$remote_snapshot_id"
  if [[ "$repository" == remote ]]; then
    printf '%s|%s\n' "$local_snapshot_id" "$remote_snapshot_id"
  else
    printf '%s\n' "$local_snapshot_id"
  fi
}

create_orphan_snapshot() {
  local repository="$1"
  local requested_at="$2"
  local run_id
  run_id="$(new_uuid)"
  seed_repository_snapshot \
    "$repository" "$requested_at" "$run_id" \
    "phase5-orphan-${repository}-${FIXTURE_SUFFIX}"
}

create_external_unowned_snapshot() {
  local repository="$1"
  local requested_at="$2"
  local run_id
  run_id="$(new_uuid)"
  seed_repository_snapshot \
    "$repository" "$requested_at" "$run_id" \
    "phase5-external-unowned-${repository}-${FIXTURE_SUFFIX}" unowned
}

latest_state() {
  db_scalar \
    "SELECT state FROM backup_runs ORDER BY requested_at DESC,id DESC LIMIT 1;"
}

latest_snapshot() {
  local repository="$1"
  case "$repository" in
    local) db_scalar "SELECT local_snapshot_id FROM backup_runs WHERE state IN ('succeeded','degraded') ORDER BY requested_at DESC,id DESC LIMIT 1;" ;;
    remote) db_scalar "SELECT remote_snapshot_id FROM backup_runs WHERE state='succeeded' AND remote_snapshot_id IS NOT NULL ORDER BY requested_at DESC,id DESC LIMIT 1;" ;;
    *) fail 'invalid latest snapshot repository' ;;
  esac
}

repository_has_snapshot() {
  local repository="$1"
  local snapshot_id="$2"
  local snapshots
  valid_snapshot_id "$snapshot_id" || return 1
  snapshots="$(
    restic_fixture "$repository" snapshots --json "$snapshot_id" 2>/dev/null
  )" || return 1
  grep -Eq \
    "\"id\"[[:space:]]*:[[:space:]]*\"${snapshot_id}\"" \
    <<<"$snapshots"
}

parse_stats_sample() {
  awk -F'|' '
    function memory_mib(value, number) {
      number=value
      sub(/[A-Za-z]+$/, "", number)
      if (value ~ /GiB$/) return number * 1024
      if (value ~ /MiB$/) return number
      if (value ~ /KiB$/) return number / 1024
      if (value ~ /GB$/) return number * 1000 / 1.048576
      if (value ~ /MB$/) return number / 1.048576
      if (value ~ /kB$/) return number / 1024 / 1.024
      if (value ~ /B$/) return number / 1024 / 1024
      return 0
    }
    {
      cpu=$1
      sub(/%$/, "", cpu)
      split($2, usage, /[[:space:]]+/)
      cpu_sum += cpu
      memory_sum += memory_mib(usage[1])
    }
    END { printf "%.3f|%.3f\n", cpu_sum, memory_sum }
  '
}

monitor_backup_runtime() {
  local backup_pid="$1"
  local result_file="$2"
  local saw_backup=false
  local overlap=false
  local peak_cpu='0'
  local peak_memory='0'
  while kill -0 "$backup_pid" 2>/dev/null; do
    local listing
    local ids=''
    local worker_running=false
    local backup_running=false
    listing="$(
      docker ps \
        --filter "label=com.docker.compose.project=${PROJECT}" \
        --format '{{.ID}}|{{.Label "com.docker.compose.service"}}' ||
        true
    )"
    local container_id
    local service
    while IFS='|' read -r container_id service; do
      [[ -n "$container_id" ]] || continue
      ids="${ids} ${container_id}"
      printf '%s\n' "$container_id" >>"$COMPOSE_CONTAINER_RECORD"
      [[ "$service" == worker ]] && worker_running=true
      case "$service" in
        backup-storage-init|backup-secrets-init|backup)
          backup_running=true
          saw_backup=true
          ;;
        *) ;;
      esac
    done <<<"$listing"
    if [[ "$worker_running" == true && "$backup_running" == true ]]; then
      overlap=true
    fi
    if [[ -n "$ids" ]]; then
      local raw_stats
      local sample
      raw_stats="$(docker stats --no-stream \
        --format '{{.CPUPerc}}|{{.MemUsage}}' $ids 2>/dev/null || true)"
      if [[ -n "$raw_stats" ]]; then
        sample="$(printf '%s\n' "$raw_stats" | parse_stats_sample)"
        local cpu="${sample%%|*}"
        local memory="${sample##*|}"
        peak_cpu="$(awk -v old="$peak_cpu" -v new="$cpu" \
          'BEGIN { print (new > old ? new : old) }')"
        peak_memory="$(awk -v old="$peak_memory" -v new="$memory" \
          'BEGIN { print (new > old ? new : old) }')"
      fi
    fi
    sleep 0.2
  done
  printf '%s|%s|%s|%s\n' \
    "$saw_backup" "$overlap" "$peak_cpu" "$peak_memory" >"$result_file"
}

diagnose_snapshot_failure() {
  local result
  result="$(
    compose run --rm --no-deps --entrypoint /bin/sh backup -eu -c '
      work=0
      objects=0
      dump=0
      age=0
      repository=0
      if test "$(stat -c "%u:%g:%a" /work)" = "10003:0:700"; then
        work=1
      fi
      if test "$(find /source/aistor -xdev ! -type d ! -type f | wc -l)" = 0 &&
        test "$(find /source/aistor -xdev -type f ! -readable | wc -l)" = 0; then
        objects=1
      fi
      PGPASSWORD="$(sed -n "1p" /run/secrets/database_password)"
      export PGPASSWORD PGHOST="${HAPPYLEARN_DATABASE_HOST}" \
        PGPORT="${HAPPYLEARN_DATABASE_PORT}" \
        PGUSER="${HAPPYLEARN_DATABASE_USER}" \
        PGDATABASE="${HAPPYLEARN_DATABASE_NAME}" \
        PGSSLMODE="${HAPPYLEARN_DATABASE_SSLMODE}"
      if /usr/bin/pg_dump --format=custom --no-owner --no-privileges \
        >/work/diagnostic.dump 2>/dev/null &&
        test -s /work/diagnostic.dump; then
        dump=1
      fi
      printf "%s\n" diagnostic >/work/diagnostic.txt
      if /usr/local/bin/age --encrypt \
        --recipient "${HAPPYLEARN_BACKUP_AGE_RECIPIENT}" \
        /work/diagnostic.txt >/work/diagnostic.age 2>/dev/null &&
        test -s /work/diagnostic.age; then
        age=1
      fi
      export RESTIC_REPOSITORY_FILE=/run/secrets/local_repository
      export RESTIC_PASSWORD_FILE=/run/secrets/local_password
      if /usr/local/bin/restic --no-cache cat config >/dev/null 2>&1; then
        repository=1
      fi
      printf "%s|%s|%s|%s|%s\n" \
        "$work" "$objects" "$dump" "$age" "$repository"
    ' 2>/dev/null
  )" || result='diagnostic_failed'
  if [[ "$result" =~ ^[01]\|[01]\|[01]\|[01]\|[01]$ ]]; then
    printf 'phase5 backup live: snapshot diagnostic %s\n' "$result" >&2
  else
    printf 'phase5 backup live: snapshot diagnostic unavailable\n' >&2
  fi
}

run_backup_monitored() {
  local label="$1"
  local result_file="$FIXTURE_ROOT/results/${label}.runtime"
  local backup_pid
  local monitor_pid
  "$ROOT/scripts/phase5-backup.sh" \
    --project "$ORCHESTRATOR_PROJECT" \
    --trigger manual &
  backup_pid="$!"
  monitor_backup_runtime "$backup_pid" "$result_file" &
  monitor_pid="$!"
  local backup_status=0
  if wait "$backup_pid"; then
    backup_status=0
  else
    backup_status=$?
  fi
  wait "$monitor_pid" ||
    fail "runtime monitor failed: ${label}"
  if [[ "$backup_status" -ne 0 ]]; then
    local evidence
    evidence="$(
      db_query <<'SQL' 2>/dev/null || true
SELECT state || '|' || trigger_kind || '|' || error_category
FROM backup_runs
ORDER BY requested_at DESC,id DESC
LIMIT 1;
SQL
    )"
    if [[ "$evidence" =~ ^(queued|draining|snapshotting|encrypting|verifying|syncing|succeeded|degraded|failed)\|(scheduled|manual|pre_release)\|[a-z_]*$ ]]; then
      printf 'phase5 backup live: failure evidence %s\n' "$evidence" >&2
    else
      printf 'phase5 backup live: failure evidence unavailable\n' >&2
    fi
    if [[ "$evidence" == 'failed|manual|snapshot' ]]; then
      diagnose_snapshot_failure
    fi
    fail "real backup failed: ${label}"
  fi
  local saw_backup
  local overlap
  local peak_cpu
  local peak_memory
  IFS='|' read -r saw_backup overlap peak_cpu peak_memory <"$result_file"
  [[ "$saw_backup" == true ]] ||
    fail "runtime monitor missed the backup container: ${label}"
  [[ "$overlap" == true || "$overlap" == false ]] ||
    fail "runtime overlap sample was invalid: ${label}"
  awk -v actual="$peak_memory" \
    'BEGIN { exit !(actual > 0) }' ||
    fail "runtime resource sample was empty: ${label}"
  awk -v actual="$peak_cpu" -v limit="$MAX_CPU_PERCENT" \
    'BEGIN { exit !(actual <= limit) }' ||
    fail "runtime CPU exceeded 2 CPUs: ${label}"
  awk -v actual="$peak_memory" -v limit="$MAX_MEMORY_MIB" \
    'BEGIN { exit !(actual <= limit) }' ||
    fail "runtime memory exceeded 4 GiB: ${label}"
  if [[ -n "$(docker ps -aq \
    --filter "label=com.docker.compose.project=${PROJECT}" \
    --filter 'label=com.docker.compose.oneoff=True')" ]]; then
    fail "Compose one-shot container remained: ${label}"
  fi
}

verify_no_orphans() {
  [[ ! -e "$FIXTURE_ROOT" ]] ||
    fail 'fixture root remained after exact cleanup'
  [[ -z "$(docker ps -aq \
    --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    fail 'Compose containers remained after exact cleanup'
  [[ -z "$(docker volume ls --quiet \
    --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    fail 'Compose volumes remained after exact cleanup'
  [[ -z "$(docker network ls --quiet \
    --filter "label=com.docker.compose.project=${PROJECT}")" ]] ||
    fail 'Compose networks remained after exact cleanup'
  if docker container inspect "$REMOTE_NAME" >/dev/null 2>&1; then
    fail 'remote fixture container remained after exact cleanup'
  fi
  if docker volume inspect "$REMOTE_VOLUME" >/dev/null 2>&1; then
    fail 'remote fixture volume remained after exact cleanup'
  fi
  local image
  for image in \
    "$BACKUP_CA_IMAGE" "$BACKUP_BASE_IMAGE" \
    "$COMPOSE_APP_IMAGE" "$COMPOSE_WORKER_IMAGE"; do
    if docker image inspect "$image" >/dev/null 2>&1; then
      fail "fixture image remained after exact cleanup: ${image}"
    fi
  done
}

seed_retention_boundaries() {
  local day
  local requested_at
  local pair
  local local_snapshot_id
  local snapshot_id
  for day in $(seq 1 29); do
    requested_at="$(timestamp_days_ago "$day" 20)"
    pair="$(seed_run remote manual "$requested_at" "remote-day-${day}")"
    local_snapshot_id="${pair%%|*}"
    snapshot_id="${pair##*|}"
    valid_snapshot_id "$local_snapshot_id" ||
      fail 'remote daily seed omitted its local recovery point'
    if [[ "$day" -eq 29 ]]; then
      REMOTE_RETAINED_DAILY="$snapshot_id"
    elif [[ "$day" -eq 6 ]]; then
      LOCAL_RETAINED_DAILY="$local_snapshot_id"
    elif [[ "$day" -eq 7 ]]; then
      LOCAL_EVICTED_DAILY="$local_snapshot_id"
    fi
  done
  pair="$(
    seed_run remote manual "$(timestamp_days_ago 5 10)" \
      'remote-daily-duplicate'
  )"
  REMOTE_EVICTED_DUPLICATE="${pair##*|}"

  local month
  for month in $(seq 2 12); do
    requested_at="$(timestamp_months_ago "$month")"
    pair="$(seed_run remote manual "$requested_at" \
      "remote-month-${month}")"
    snapshot_id="${pair##*|}"
    if [[ "$month" -eq 11 ]]; then
      REMOTE_RETAINED_MONTHLY="$snapshot_id"
    elif [[ "$month" -eq 12 ]]; then
      REMOTE_EVICTED_MONTHLY="$snapshot_id"
    fi
  done

  pair="$(
    seed_run remote pre_release "$(timestamp_days_ago 20 10)" \
      'remote-pre-release-protected'
  )"
  REMOTE_PROTECTED_PRE_RELEASE="${pair##*|}"
  seed_run remote manual "$(timestamp_days_ago 31 20)" \
    'remote-expired-pre-release-day' >/dev/null
  pair="$(
    seed_run remote pre_release "$(timestamp_days_ago 31 10)" \
      'remote-pre-release-expired'
  )"
  REMOTE_EXPIRED_PRE_RELEASE="${pair##*|}"
  LOCAL_ORPHAN="$(
    create_orphan_snapshot local "$(timestamp_days_ago 2 20)"
  )"
  REMOTE_ORPHAN="$(
    create_orphan_snapshot remote "$(timestamp_days_ago 2 20)"
  )"
  LOCAL_EXTERNAL_UNOWNED="$(
    create_external_unowned_snapshot local "$(timestamp_days_ago 2 19)"
  )"
  REMOTE_EXTERNAL_UNOWNED="$(
    create_external_unowned_snapshot remote "$(timestamp_days_ago 2 19)"
  )"
  LOCAL_RECENT_ORPHAN="$(
    create_orphan_snapshot local "$(timestamp_hours_ago 1)"
  )"
  REMOTE_RECENT_ORPHAN="$(
    create_orphan_snapshot remote "$(timestamp_hours_ago 1)"
  )"
  pair="$(seed_failed_run "$(timestamp_hours_ago 2)" recent)"
  LOCAL_RECENT_FAILED="${pair%%|*}"
  REMOTE_RECENT_FAILED="${pair##*|}"
}

assert_retention_results() {
  local current_local
  local current_remote
  current_local="$(latest_snapshot local)"
  current_remote="$(latest_snapshot remote)"
  repository_has_snapshot local "$current_local" ||
    fail 'current local recovery point was deleted'
  repository_has_snapshot remote "$current_remote" ||
    fail 'current remote recovery point was deleted'
  repository_has_snapshot local "$LAST_GOOD_LOCAL" ||
    fail 'last-good local recovery point was deleted'
  repository_has_snapshot remote "$LAST_GOOD_REMOTE" ||
    fail 'last-good remote recovery point was deleted'
  repository_has_snapshot local "$LOCAL_RETAINED_DAILY" ||
    fail 'local seven-day boundary was not retained'
  if repository_has_snapshot local "$LOCAL_EVICTED_DAILY"; then
    fail 'local eighth daily recovery point was not evicted'
  fi
  repository_has_snapshot remote "$REMOTE_RETAINED_DAILY" ||
    fail 'remote thirtieth daily recovery point was not retained'
  if repository_has_snapshot remote "$REMOTE_EVICTED_DUPLICATE"; then
    fail 'remote duplicate daily recovery point was not evicted'
  fi
  repository_has_snapshot remote "$REMOTE_RETAINED_MONTHLY" ||
    fail 'remote twelfth monthly recovery point was not retained'
  if repository_has_snapshot remote "$REMOTE_EVICTED_MONTHLY"; then
    fail 'remote thirteenth monthly recovery point was not evicted'
  fi
  repository_has_snapshot remote "$REMOTE_PROTECTED_PRE_RELEASE" ||
    fail 'protected pre-release recovery point was deleted'
  if repository_has_snapshot remote "$REMOTE_EXPIRED_PRE_RELEASE"; then
    fail 'expired pre-release recovery point was not evicted'
  fi
  if repository_has_snapshot local "$LOCAL_ORPHAN"; then
    fail 'old uncommitted local snapshot was not removed'
  fi
  if repository_has_snapshot remote "$REMOTE_ORPHAN"; then
    fail 'old uncommitted remote snapshot was not removed'
  fi
  repository_has_snapshot local "$LOCAL_EXTERNAL_UNOWNED" ||
    fail 'external unowned local snapshot was deleted'
  repository_has_snapshot remote "$REMOTE_EXTERNAL_UNOWNED" ||
    fail 'external unowned remote snapshot was deleted'
  repository_has_snapshot local "$LOCAL_RECENT_ORPHAN" ||
    fail 'recent uncommitted local snapshot was removed'
  repository_has_snapshot remote "$REMOTE_RECENT_ORPHAN" ||
    fail 'recent uncommitted remote snapshot was removed'
  repository_has_snapshot local "$LOCAL_RECENT_FAILED" ||
    fail 'recent failed local point was removed or occupied a success slot'
  repository_has_snapshot remote "$REMOTE_RECENT_FAILED" ||
    fail 'recent failed remote point was removed or occupied a success slot'
}

main() {
  local actual_state
  require_dependencies
  create_fixture
  create_fixture_ca
  build_backup_images
  start_base_stack
  start_remote_fixture
  create_remote_bucket

  run_backup_monitored success
  actual_state="$(latest_state)"
  [[ "$actual_state" == succeeded ]] ||
    fail "real local/remote success was not recorded: ${actual_state}"
  assert_ca_isolation

  docker pause "$REMOTE_NAME" >/dev/null
  run_backup_monitored remote-outage
  actual_state="$(latest_state)"
  [[ "$actual_state" == degraded ]] ||
    fail "remote outage did not preserve a degraded local recovery point: ${actual_state}"

  docker unpause "$REMOTE_NAME" >/dev/null
  wait_remote_ready || fail 'remote fixture did not recover'
  run_backup_monitored remote-recovery
  actual_state="$(latest_state)"
  [[ "$actual_state" == succeeded ]] ||
    fail "remote recovery retry did not succeed: ${actual_state}"
  LAST_GOOD_LOCAL="$(latest_snapshot local)"
  LAST_GOOD_REMOTE="$(latest_snapshot remote)"

  seed_retention_boundaries
  run_backup_monitored retention
  actual_state="$(latest_state)"
  [[ "$actual_state" == succeeded ]] ||
    fail "retention-triggering recovery point did not succeed: ${actual_state}"
  assert_retention_results

  cleanup_live 0
  trap - EXIT HUP INT TERM
  verify_no_orphans
  printf 'phase5 backup live: PASS\n'
}

main "$@"
