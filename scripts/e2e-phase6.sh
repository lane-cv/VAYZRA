#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2016
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/.." && pwd -P)
# shellcheck source=e2e-harness-lib.sh
source "$script_dir/e2e-harness-lib.sh"

readonly registry_image='registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373'
readonly runner_base='docker:28.3.3-cli@sha256:0135662b510037ea581d99c2e5929c5e01185139c0b86986a418bd4da0b98a44'
readonly aistor_image='quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120'
readonly playwright_image='mcr.microsoft.com/playwright:v1.57.0-noble@sha256:3bed4b1a12f2338642f3d8cba28e291deef3c66bd4a964bbeb3e57bbff511dbd'
readonly owner_label='io.happylearn.phase6.e2e-owner'
readonly allowed_groups='all install regression mobile recovery release rollback restart security resources failure-matrix'

fail() { printf 'phase6 e2e: FAIL: %s\n' "$1" >&2; exit 1; }
for command in docker openssl curl jq git go sha256sum realpath stat timeout; do command -v "$command" >/dev/null 2>&1 || fail "missing command: $command"; done

license_file=${HAPPYLEARN_AISTOR_LICENSE_FILE:-}
[[ $license_file == /* && -f $license_file && ! -L $license_file && -r $license_file ]] || fail 'HAPPYLEARN_AISTOR_LICENSE_FILE must be an absolute readable file'
license_file=$(realpath -e -- "$license_file")
group=${HAPPYLEARN_E2E_GROUP:-all}
case " $allowed_groups " in *" $group "*) ;; *) fail 'invalid HAPPYLEARN_E2E_GROUP' ;; esac
regression_stage=${HAPPYLEARN_PHASE6_REGRESSION_STAGE:-all}
case $regression_stage in all|phase1|phase2|phase3|phase4|phase5) ;; *) fail 'invalid HAPPYLEARN_PHASE6_REGRESSION_STAGE' ;; esac

nonce=$(openssl rand -hex 6)
[[ $nonce =~ ^[a-f0-9]{12}$ ]] || fail 'nonce generation failed'
project="happylearn_phase6_${nonce}_prod"
prefix="happylearn_phase6_${nonce}_"
registry_container="${prefix}registry"
export HAPPYLEARN_PHASE6_REGISTRY_CONTAINER=$registry_container
runner_image="${prefix}runner"
browser_runner="${prefix}browser"
fixture_runner="${prefix}fixture"
install_runner="${prefix}install"
fake_ai_container="${prefix}fake_ai"
runner_volume="${prefix}runner"
fixture_volume="${prefix}fixtures"
credential_volume="${prefix}credentials"
registry_port=$((20000 + 16#${nonce:0:4} % 20000))
http_port=$((40000 + 16#${nonce:4:4} % 10000))
https_port=$((50000 + 16#${nonce:8:4} % 10000))
[[ $registry_port != "$http_port" && $registry_port != "$https_port" && $http_port != "$https_port" ]] || fail 'port allocation collision'
registry_endpoint="127.0.0.1:${registry_port}"
domain="phase6-${nonce}.test"

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/happylearn-phase6-e2e.XXXXXX")
chmod 0700 "$tmpdir"
release_project=$tmpdir/project
secret_dir=$tmpdir/secrets
backup_dir=$tmpdir/backups
state_dir=$tmpdir/releases
env_file=$tmpdir/production.env
manifest_a=$tmpdir/manifest-a.json
manifest_b=$tmpdir/manifest-b.json
release_manifest_a=$tmpdir/release-manifest-a.json
release_manifest_b=$tmpdir/release-manifest-b.json
bootstrap_manifest=$tmpdir/manifest-bootstrap.json
ca_file=$tmpdir/caddy-root.crt
admin_password_file=$tmpdir/admin-password
student_password_file=$tmpdir/student-password
student_new_password_file=$tmpdir/student-new-password
provider_key_file=$tmpdir/provider-key
control_token_file=$tmpdir/control-token
e2e_compose_file=$tmpdir/compose.e2e.yml
artifact_dir=${E2E_ARTIFACT_DIR:-$repo_root/test-results/phase6/$nonce}
durable_marker="Phase 6 durable $nonce"
expected_version=''
previous_version=''
accepted_write_marker=''
chromium_spki=''
case $artifact_dir in "$repo_root"/test-results/phase6/*) ;; *) fail 'E2E_ARTIFACT_DIR must be below test-results/phase6' ;; esac
mkdir -p "$artifact_dir/results" "$artifact_dir/evidence"
chmod 0700 "$artifact_dir" "$artifact_dir/results" "$artifact_dir/evidence"

env_ready=false
runner_ready=false
cleanup_started=false
acceptance_controls_started=false
restore_project=''
restore_projects=()
release_completed=false
resource_pid=''
owned_images=()
failure_matrix_root=$tmpdir/release-failure-matrix
failure_matrix_adapter=$failure_matrix_root/adapter

prepare_release_failure_matrix() {
  [[ ! -e $failure_matrix_root ]] || fail 'failure matrix root already exists'
  mkdir -p "$failure_matrix_root/cases"
  chmod 0700 "$failure_matrix_root" "$failure_matrix_root/cases"
  cp -- "$script_dir/phase6-release_failure_matrix_adapter.sh" "$failure_matrix_adapter"
  chmod 0700 "$failure_matrix_adapter"
  {
    printf 'REPO_ROOT=%q\n' "$repo_root"
    printf 'HARNESS_TMPDIR=%q\n' "$tmpdir"
    printf 'RELEASE_PROJECT=%q\n' "$release_project"
    printf 'MAIN_ENV=%q\n' "$env_file"
    printf 'MANIFEST_A=%q\n' "$release_manifest_a"
    printf 'MANIFEST_B=%q\n' "$release_manifest_b"
    printf 'STATE_DIR=%q\n' "$state_dir"
    printf 'SECRET_DIR=%q\n' "$secret_dir"
    printf 'RUNNER_IMAGE=%q\n' "$runner_image"
    printf 'COMPOSE_PROJECT=%q\n' "$project"
    printf 'OWNER_LABEL=%q\n' "$owner_label"
    printf 'OWNER_TOKEN=%q\n' "$nonce"
    printf 'DOMAIN=%q\n' "$domain"
  } >"$failure_matrix_root/context.env"
  chmod 0600 "$failure_matrix_root/context.env"
}

run_release_failure_matrix() {
  prepare_release_failure_matrix
  HAPPYLEARN_PHASE6_FAILURE_MATRIX_ADAPTER=$failure_matrix_adapter \
  HAPPYLEARN_PHASE6_FAILURE_MATRIX_ROOT=$failure_matrix_root \
  HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE_DEADLINE_SECONDS=14400 \
    "$script_dir/phase6-release_failure_matrix.sh"
}

root_runner() {
  docker run --rm --interactive --name "${prefix}coordinator" \
    --label "$owner_label=$nonce" \
    --network none --memory 512m --cpus .5 \
    --volume /var/run/docker.sock:/var/run/docker.sock \
    --volume "$repo_root:$repo_root:ro" \
    --volume "$tmpdir:$tmpdir" \
    --workdir "$repo_root" \
    --env "TMPDIR=$tmpdir" \
    --env "HAPPYLEARN_LOCAL_COMPOSE_PROJECT=$project" \
    --entrypoint /bin/bash "$runner_image" "$@"
}

compose_root() {
  # The production environment file is deliberately root-owned. Keep all
  # Compose parsing inside the unprivileged coordinator container while the
  # Docker daemon continues to enforce the actual service identities.
  # shellcheck disable=SC2016
  root_runner -eu -c '
    repository=$1; environment=$2; shift 2
    exec timeout --foreground --kill-after=10s 180s docker compose \
      --project-name "$HAPPYLEARN_LOCAL_COMPOSE_PROJECT" \
      --project-directory "$repository" --env-file "$environment" \
      -f "$repository/deploy/compose.prod.yml" \
      -f "$repository/deploy/compose.prod.local.yml" "$@"
  ' _ "$repo_root" "$env_file" "$@"
}

owned_ids() {
  docker ps --all --quiet --no-trunc --filter "label=$owner_label=$nonce"
}

zero_resource_proof() {
  [[ -z $(docker ps --all --quiet --filter "label=$owner_label=$nonce") ]] || return 1
  [[ -z $(docker ps --all --quiet --filter "label=com.docker.compose.project=$project") ]] || return 1
  [[ -z $(docker network ls --quiet --filter "label=com.docker.compose.project=$project") ]] || return 1
  [[ -z $(docker volume ls --quiet --filter "label=com.docker.compose.project=$project") ]] || return 1
  [[ -z $(docker image ls --quiet --filter "reference=$registry_endpoint/happylearn/*") ]] || return 1
}

capture_diagnostics() {
  local staging=$tmpdir/diagnostics container state exit_code oom release_summary release_state release_result rollback_category
  mkdir -p "$staging"
  chmod 0700 "$staging"
  printf '%s\n' diagnostics_version=1 >"$staging/containers.log"
  while IFS= read -r container; do
    [[ -n $container ]] || continue
    state=$(docker inspect --format '{{.State.Status}}' "$container" 2>/dev/null || printf dead)
    exit_code=$(docker inspect --format '{{.State.ExitCode}}' "$container" 2>/dev/null || printf 1)
    oom=$(docker inspect --format '{{.State.OOMKilled}}' "$container" 2>/dev/null || printf false)
    printf 'container=%s\nstate_status=%s\nexit_code=%s\noom_killed=%s\n' "$container" "$state" "$exit_code" "${oom,,}" >>"$staging/containers.log"
  done < <(docker ps --all --format '{{.Names}}' --filter "label=com.docker.compose.project=$project")
  if [[ $runner_ready == true && -f $state_dir/release-state.json && ! -L $state_dir/release-state.json ]]; then
    release_summary=$(root_runner -eu -c 'jq -r "[.state // \"unknown\", .result // \"unknown\", .rollbackFailureCategory // \"none\"] | @tsv" "$1"' _ "$state_dir/release-state.json" 2>/dev/null || true)
    IFS=$'\t' read -r release_state release_result rollback_category <<<"$release_summary"
    if [[ $release_state =~ ^[a-z][a-z_]{0,63}$ && $release_result =~ ^[a-z][a-z_]{0,31}$ && $rollback_category =~ ^[a-z][a-z_]{0,63}$ ]]; then
      printf 'release_state=%s\nrelease_result=%s\nrollback_failure_category=%s\n' "$release_state" "$release_result" "$rollback_category" >>"$staging/containers.log"
    fi
  fi
  chmod 0600 "$staging/containers.log"
  "$script_dir/publish-e2e-diagnostics.sh" "$staging/containers.log" "$artifact_dir" "$nonce"
}

cleanup() {
  local status=${1:-$?} id
  [[ $cleanup_started == false ]] || return 0
  cleanup_started=true
  trap - EXIT HUP INT TERM
  if [[ $resource_pid =~ ^[1-9][0-9]*$ ]] && kill -0 "$resource_pid" 2>/dev/null; then
    kill "$resource_pid" 2>/dev/null || true
    wait "$resource_pid" 2>/dev/null || true
  fi
  if ((status != 0)); then
    for id in "${project}-app-1" "${project}-worker-1"; do
      docker logs --tail 15 "$id" 2>&1 | sed -E 's/(password|authorization|cookie|token|secret|database_url|redis_url)=[^[:space:]]+/\1=[REDACTED]/Ig' >&2 || true
    done
  fi
  capture_diagnostics || status=$(preserve_first_failure "$status" 1)
  if [[ $env_ready == true && $runner_ready == true && -f $env_file ]]; then compose_root --profile '*' down --volumes --remove-orphans --timeout 30 >/dev/null 2>&1 || status=$(preserve_first_failure "$status" 1); fi
  while IFS= read -r id; do [[ -z $id ]] || docker rm --force "$id" >/dev/null 2>&1 || status=$(preserve_first_failure "$status" 1); done < <(owned_ids)
  docker rm --force "$registry_container" >/dev/null 2>&1 || true
  docker volume rm "$runner_volume" "$fixture_volume" "$credential_volume" >/dev/null 2>&1 || true
  for restore_project in "${restore_projects[@]}"; do
    if [[ $restore_project =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ ]]; then
      docker volume rm "$restore_project-postgres" "$restore_project-aistor" "$restore_project-aistor-license" "$restore_project-secrets" >/dev/null 2>&1 || true
    fi
  done
  for id in "${owned_images[@]}"; do docker image rm "$id" >/dev/null 2>&1 || true; done
  if [[ -d $tmpdir && ! -L $tmpdir ]]; then
    docker run --rm --network none --volume "$tmpdir:/cleanup" "$runner_base" find /cleanup -mindepth 1 -delete >/dev/null 2>&1 || true
    rmdir "$tmpdir" >/dev/null 2>&1 || true
  fi
  zero_resource_proof || status=$(preserve_first_failure "$status" 1)
  if [[ -x $script_dir/sanitize-e2e-artifacts.sh ]]; then "$script_dir/sanitize-e2e-artifacts.sh" "$artifact_dir" >/dev/null || status=$(preserve_first_failure "$status" 1); fi
  exit "$status"
}
trap 'cleanup $?' EXIT
trap 'cleanup 129' HUP
trap 'cleanup 130' INT
trap 'cleanup 143' TERM

build_runner() {
  docker_bounded 300 build --pull=false --file "$repo_root/deploy/Dockerfile.phase6-runner" --tag "$runner_image" "$repo_root"
  owned_images+=("$runner_image")
  docker run --rm --network none --entrypoint /bin/sh "$runner_image" -eu -c 'command -v bash >/dev/null; command -v docker >/dev/null; command -v jq >/dev/null'
  runner_ready=true
}

prepare_release_project() {
  root_runner -eu -c '
    source_root=$1; target_root=$2
    install -d -o 0 -g 0 -m 0700 "$target_root"
    cp -R --no-preserve=ownership "$source_root/deploy" "$source_root/scripts" "$target_root/"
    cp --no-preserve=ownership "$source_root/Dockerfile.backup" "$target_root/Dockerfile.backup"
    chown -R 0:0 "$target_root"
    find "$target_root" -type d -exec chmod go-w {} +
    find "$target_root" -type f -exec chmod go-w {} +
  ' _ "$repo_root" "$release_project"
  [[ $(stat -c '%u:%a' "$release_project") == 0:700 ]] || fail 'release project ownership invalid'
}

start_registry() {
  docker run --detach --name "$registry_container" --label "$owner_label=$nonce" \
    --network bridge --read-only --cap-drop ALL --security-opt no-new-privileges \
    --memory 128m --cpus .15 --tmpfs /tmp:rw,noexec,nosuid,size=16m \
    --publish "127.0.0.1:${registry_port}:5000" "$registry_image" >/dev/null
  local deadline=$((SECONDS + 60))
  until curl --fail --silent "http://${registry_endpoint}/v2/" >/dev/null; do ((SECONDS < deadline)) || fail 'registry readiness timeout'; sleep 1; done
}

pull_digest() {
  local image=$1 digest expected_digest
  docker_bounded 600 pull "$image" >/dev/null
  digest=$(docker image inspect --format '{{index .RepoDigests 0}}' "$image")
  [[ $digest =~ @sha256:[a-f0-9]{64}$ ]] || return 1
  if [[ $image =~ :[^/@]+@(sha256:[a-f0-9]{64})$ ]]; then
    expected_digest=${BASH_REMATCH[1]}
    [[ ${digest##*@} == "$expected_digest" ]] || return 1
    printf '%s\n' "$image"
    return 0
  fi
  printf '%s\n' "$digest"
}

push_digest() {
  local tag=$1 digest
  docker_bounded 600 push "$tag" >/dev/null
  digest=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$tag" | grep -F "${tag%:*}@sha256:" | head -n1)
  [[ $digest =~ ^127\.0\.0\.1:[0-9]+/[^[:space:]@]+@sha256:[a-f0-9]{64}$ ]] || return 1
  printf '%s\n' "$digest"
}

build_image_set() {
  local set=$1 version=$2 commit=$3 app_tag worker_tag
  app_tag="$registry_endpoint/happylearn/app:$set"
  worker_tag="$registry_endpoint/happylearn/worker:$set"
  docker_bounded 1200 build --target server --tag "$app_tag" \
    --build-arg "HAPPYLEARN_BUILD_VERSION=$version" --build-arg "HAPPYLEARN_BUILD_COMMIT=$commit" \
    --build-arg 'HAPPYLEARN_BUILD_TIME=2026-08-02T00:00:00Z' --build-arg HAPPYLEARN_BUILD_MIN_SCHEMA=27 --build-arg HAPPYLEARN_BUILD_MAX_SCHEMA=27 "$repo_root"
  docker_bounded 1200 build --file "$repo_root/Dockerfile.worker" --target worker --tag "$worker_tag" \
    --build-arg "HAPPYLEARN_BUILD_VERSION=$version" --build-arg "HAPPYLEARN_BUILD_COMMIT=$commit" \
    --build-arg 'HAPPYLEARN_BUILD_TIME=2026-08-02T00:00:00Z' --build-arg HAPPYLEARN_BUILD_MIN_SCHEMA=27 --build-arg HAPPYLEARN_BUILD_MAX_SCHEMA=27 "$repo_root"
  owned_images+=("$app_tag" "$worker_tag")
  if [[ $set == a ]]; then
    image_set_a_app=$(push_digest "$app_tag")
    image_set_a_worker=$(push_digest "$worker_tag")
  else
    image_set_b_app=$(push_digest "$app_tag")
    image_set_b_worker=$(push_digest "$worker_tag")
  fi
}

build_shared_images() {
  local migrate_tag="$registry_endpoint/happylearn/migrate:phase6" backup_tag="$registry_endpoint/happylearn/backup:phase6"
  docker_bounded 900 build --target migrate --tag "$migrate_tag" "$repo_root"
  owned_images+=("$migrate_tag")
  docker_bounded 1200 build --file "$repo_root/Dockerfile.backup" --target backup --tag "$backup_tag" \
    --build-arg 'GO_MODULE_PROXY=https://goproxy.cn,direct' "$repo_root"
  owned_images+=("$backup_tag")
  migrate_image=$(push_digest "$migrate_tag")
  backup_image=$(push_digest "$backup_tag")
  caddy_image=$(pull_digest caddy:2.10.2-alpine)
  postgres_image=$(pull_digest postgres:18.4)
  redis_image=$(pull_digest redis:8.8)
  minio_image=$(pull_digest "$aistor_image")
}

random_secret() { openssl rand -hex 32 >"$1"; chmod 0600 "$1"; }
random_base64_key() { openssl rand -base64 32 >"$1"; chmod 0600 "$1"; }
prepare_secrets() {
  mkdir -p "$secret_dir" "$backup_dir" "$state_dir/recovery" "$state_dir/backup-workflows" "$state_dir/release-input" "$state_dir/restore-control" "$state_dir/restore-reports"
  chmod 0700 "$secret_dir" "$backup_dir" "$state_dir" "$state_dir/recovery" "$state_dir/backup-workflows" "$state_dir/release-input" "$state_dir/restore-control" "$state_dir/restore-reports"
  local name
  for name in postgres-password redis-password minio-secret-key app-login-throttle app-ai-master-key worker-login-throttle worker-ai-master-key metrics-bearer host-metrics-hmac backup-password; do random_secret "$secret_dir/$name"; done
  random_base64_key "$secret_dir/app-ai-master-key"
  random_base64_key "$secret_dir/worker-ai-master-key"
  printf 'phase6%s\n' "$nonce" >"$secret_dir/minio-access-key"
  printf '/repository\n' >"$secret_dir/backup-local-repository"
  cp -- "$secret_dir/postgres-password" "$secret_dir/backup-database-password"
  cp -- "$license_file" "$secret_dir/aistor-license"
  random_secret "$admin_password_file"
  random_secret "$student_password_file"
  random_secret "$student_new_password_file"
  random_secret "$provider_key_file"
  random_secret "$control_token_file"
  local database_password redis_password minio_access minio_secret
  database_password=$(<"$secret_dir/postgres-password"); redis_password=$(<"$secret_dir/redis-password")
  minio_access=$(<"$secret_dir/minio-access-key"); minio_secret=$(<"$secret_dir/minio-secret-key")
  printf 'postgres://happylearn:%s@postgres:5432/happylearn?sslmode=disable\n' "$database_password" >"$secret_dir/app-database-url"
  cp -- "$secret_dir/app-database-url" "$secret_dir/worker-database-url"
  printf 'redis://:%s@redis:6379/0\n' "$redis_password" >"$secret_dir/app-redis-url"
  cp -- "$secret_dir/app-redis-url" "$secret_dir/worker-redis-url"
  printf '%s\n' "$minio_access" >"$secret_dir/app-minio-access-key"; cp -- "$secret_dir/app-minio-access-key" "$secret_dir/worker-minio-access-key"
  printf '%s\n' "$minio_secret" >"$secret_dir/app-minio-secret-key"; cp -- "$secret_dir/app-minio-secret-key" "$secret_dir/worker-minio-secret-key"
  chmod 0600 "$secret_dir"/* "$admin_password_file"
  find "$secret_dir" -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort >"$artifact_dir/evidence/secret-inventory.txt"
  docker run --rm --network none --volume "$secret_dir:/secrets" --volume "$backup_dir:/backups" --volume "$state_dir:/releases" --volume "$admin_password_file:/admin-password" --volume "$student_password_file:/student-password" --volume "$student_new_password_file:/student-new-password" --volume "$provider_key_file:/provider-key" --volume "$control_token_file:/control-token" "$runner_base" sh -eu -c '
    chown 0:0 /secrets; chmod 0711 /secrets
    chown 999:999 /secrets/postgres-password /secrets/redis-password
    chown 1000:1000 /secrets/minio-access-key /secrets/minio-secret-key /secrets/aistor-license
    chown 10001:10001 /secrets/app-* /secrets/metrics-bearer /secrets/host-metrics-hmac
    chown 10002:10002 /secrets/worker-*
    chown 10003:10003 /secrets/backup-*
    chown 10001:10001 /admin-password
    chown 1000:1000 /student-password /student-new-password
    chown 10003:10003 /provider-key
    chown 10002:10002 /control-token
    chown 0:0 /releases /releases/recovery /releases/restore-control /releases/restore-reports
    chmod 0700 /releases /releases/recovery /releases/restore-control /releases/restore-reports
    chown 10003:0 /backups /releases/backup-workflows
    chown 10001:10001 /releases/release-input
  '
}

build_acceptance_images() {
  local fake_tag="$registry_endpoint/happylearn/fake-ai:phase6"
  local supervisor_tag="$registry_endpoint/happylearn/worker-supervisor:phase6"
  local supervisor_base=$image_set_a_worker
  [[ $expected_version != 1.0.0-rc.2 ]] || supervisor_base=$image_set_b_worker
  docker_bounded 900 build --file "$repo_root/Dockerfile.fake-ai" --tag "$fake_tag" "$repo_root"
  docker_bounded 1200 build --file "$repo_root/Dockerfile.e2e-worker" \
    --build-arg "WORKER_IMAGE=$supervisor_base" \
    --tag "$supervisor_tag" "$repo_root"
  owned_images+=("$fake_tag" "$supervisor_tag")
  fake_ai_image=$fake_tag
  supervisor_image=$(push_digest "$supervisor_tag")
}

write_e2e_compose_override() {
  cat >"$e2e_compose_file" <<EOF
services:
  worker:
    image: $supervisor_image
    entrypoint: [/bin/sh, -eu, -c]
    command:
      - export E2E_AI_PROCESSING_CONTROL_TOKEN="\$(cat /run/secrets/e2e-control-token)"; exec /app/e2e-processing-supervisor
    secrets:
      - source: e2e_control_token
        target: e2e-control-token
secrets:
  e2e_control_token:
    file: $control_token_file
EOF
  chmod 0600 "$e2e_compose_file"
}

compose_e2e() {
  # shellcheck disable=SC2016
  root_runner -eu -c '
    repository=$1; environment=$2; overlay=$3; shift 3
    exec timeout --foreground --kill-after=10s 180s docker compose \
      --project-name "$HAPPYLEARN_LOCAL_COMPOSE_PROJECT" \
      --project-directory "$repository" --env-file "$environment" \
      -f "$repository/deploy/compose.prod.yml" \
      -f "$repository/deploy/compose.prod.local.yml" -f "$overlay" "$@"
  ' _ "$repo_root" "$env_file" "$e2e_compose_file" "$@"
}

start_acceptance_controls() {
  [[ $acceptance_controls_started == false ]] || return 0
  build_acceptance_images
  write_e2e_compose_override
  compose_root stop --timeout 30 worker
  compose_e2e up --detach --no-deps --force-recreate worker
  local deadline=$((SECONDS + 180)) app_id
  until curl --noproxy '*' --fail --silent --cacert "$ca_file" --resolve "$domain:$https_port:127.0.0.1" "https://$domain:$https_port/api/v1/health/live" >/dev/null; do
    ((SECONDS < deadline)) || fail 'application unavailable before acceptance controls'; sleep 2
  done
  app_id=$(compose_root ps --quiet app)
  [[ $app_id =~ ^[a-f0-9]{64}$ ]] || fail 'app container identity missing'
  docker run --detach --name "$fake_ai_container" --label "$owner_label=$nonce" \
    --network "container:$app_id" --read-only --user 10003:10003 --cap-drop ALL \
    --security-opt no-new-privileges --memory 64m --cpus .05 \
    --tmpfs /tmp:rw,noexec,nosuid,size=4m,uid=10003,gid=10003,mode=0700 \
    --volume "$provider_key_file:/run/provider-key:ro" --entrypoint /bin/sh "$fake_ai_image" -eu -c \
    'export E2E_AI_PROVIDER_KEY="$(cat /run/provider-key)"; exec /app/fake-ai-provider' >/dev/null
  deadline=$((SECONDS + 120))
  until docker exec "$fake_ai_container" curl --fail --silent http://127.0.0.1:8090/health/live >/dev/null 2>&1; do
    ((SECONDS < deadline)) || fail 'fake provider readiness timeout'; sleep 2
  done
  until compose_e2e exec -T worker curl --fail --silent http://127.0.0.1:8092/health/live >/dev/null 2>&1; do
    ((SECONDS < deadline)) || fail 'processing supervisor readiness timeout'; sleep 2
  done
  acceptance_controls_started=true
}

prepare_browser_workspace() {
  docker volume create --label "$owner_label=$nonce" "$runner_volume" >/dev/null
  docker volume create --label "$owner_label=$nonce" "$fixture_volume" >/dev/null
  docker volume create --label "$owner_label=$nonce" "$credential_volume" >/dev/null
  docker run --rm --network none --user 0:0 --cap-drop ALL --cap-add CHOWN \
    --security-opt no-new-privileges --volume "$runner_volume:/workspace" --volume "$fixture_volume:/fixtures" \
    --entrypoint /bin/sh "$playwright_image" -eu -c 'chmod 0700 /workspace /fixtures; chown 1000:1000 /workspace /fixtures'
  docker run --rm --network none --user 0:0 --cap-drop ALL --cap-add CHOWN --cap-add FOWNER --cap-add DAC_READ_SEARCH \
    --security-opt no-new-privileges --volume "$admin_password_file:/inputs/admin:ro" \
    --volume "$student_password_file:/inputs/student:ro" --volume "$student_new_password_file:/inputs/student-new:ro" \
    --volume "$provider_key_file:/inputs/provider:ro" --volume "$control_token_file:/inputs/control:ro" \
    --volume "$credential_volume:/credentials" --entrypoint /bin/sh "$runner_base" -eu -c \
    'cp /inputs/* /credentials/; chown 1000:1000 /credentials /credentials/*; chmod 0700 /credentials; chmod 0400 /credentials/*'
  docker run --rm --name "$fixture_runner" --label "$owner_label=$nonce" --network none \
    --read-only --user 1000:1000 --cap-drop ALL --security-opt no-new-privileges \
    --memory 512m --cpus .5 --tmpfs /tmp:rw,noexec,nosuid,size=256m,uid=1000,gid=1000,mode=0700 \
    --workdir /tmp --entrypoint /bin/bash --volume "$repo_root:/src:ro" --volume "$fixture_volume:/fixtures" \
    "$image_set_a_worker" /src/scripts/generate-phase2-fixtures.sh /fixtures
  docker run --rm --name "$install_runner" --label "$owner_label=$nonce" --network bridge \
    --read-only --user 1000:1000 --cap-drop ALL --security-opt no-new-privileges \
    --memory 1024m --cpus .5 --tmpfs /tmp:rw,noexec,nosuid,size=64m \
    --volume "$repo_root:/source:ro" --volume "$runner_volume:/workspace" --entrypoint /bin/bash \
    --env COREPACK_HOME=/workspace/.corepack --env XDG_DATA_HOME=/workspace/.xdg --env PNPM_HOME=/workspace/.pnpm \
    "$playwright_image" -lc '/source/scripts/copy-e2e-workspace.sh /source /workspace && cd /workspace && for attempt in 1 2 3; do corepack pnpm install --frozen-lockfile --store-dir /workspace/.pnpm-store --fetch-retries 5 --fetch-retry-mintimeout 10000 --fetch-retry-maxtimeout 60000 && exit 0; done; exit 1'
}

run_browser_command() {
  local command=$1 network_scope=${2:-edge} browser_network caddy_ip
  [[ $network_scope == edge || $network_scope == private ]] || fail 'invalid browser network scope'
  browser_network="${project}_${network_scope}"
  caddy_ip=$(docker inspect --format "{{with index .NetworkSettings.Networks \"$browser_network\"}}{{.IPAddress}}{{end}}" "${project}-caddy-1")
  [[ $caddy_ip =~ ^[0-9.]+$ ]] || fail 'Caddy edge address missing'
  docker run --rm --name "$browser_runner" --label "$owner_label=$nonce" --network "$browser_network" \
    --add-host "$domain:$caddy_ip" --read-only --user 1000:1000 --shm-size 384m --memory 1024m --cpus .5 \
    --cap-drop ALL --security-opt no-new-privileges \
    --tmpfs /tmp:rw,noexec,nosuid,nodev,size=128m,uid=1000,gid=1000,mode=0700 \
    --volume "$runner_volume:/workspace" --volume "$fixture_volume:/fixtures:ro" --volume "$artifact_dir/results:/artifacts/results" \
    --volume "$ca_file:/certificates/caddy-root.crt:ro" --volume "$credential_volume:/run/credentials:ro" \
    --workdir /workspace --entrypoint /bin/bash \
    --env COREPACK_HOME=/workspace/.corepack --env XDG_DATA_HOME=/workspace/.xdg --env PNPM_HOME=/workspace/.pnpm \
    --env NODE_EXTRA_CA_CERTS=/certificates/caddy-root.crt --env "E2E_BASE_URL=https://$domain" \
    --env "E2E_CHROMIUM_SPKI_LIST=$chromium_spki" \
    --env "E2E_PHASE6_HTTPS_BASE_URL=https://$domain" --env "E2E_PHASE6_HTTP_BASE_URL=http://$domain" \
    --env "E2E_PHASE6_HOSTNAME=$domain" --env E2E_FIXTURE_DIR=/fixtures \
    --env "E2E_PHASE6_DURABLE_MARKER=$durable_marker" \
    --env "E2E_PHASE6_EXPECTED_VERSION=$expected_version" --env "E2E_PHASE6_PREVIOUS_VERSION=$previous_version" \
    --env "E2E_PHASE6_ACCEPTED_WRITE_MARKER=$accepted_write_marker" \
    --env E2E_AI_PROVIDER_BASE_URL=http://localhost:8090/v1 --env E2E_AI_PROVIDER_COUNTS_URL=http://app:8090/test/counts \
    --env E2E_AI_PROCESSING_CONTROL_URL=http://worker:8092 "$playwright_image" -eu -c \
    'export E2E_ADMIN_PASSWORD="$(cat /run/credentials/admin)" E2E_STUDENT_PASSWORD="$(cat /run/credentials/student)" E2E_STUDENT_NEW_PASSWORD="$(cat /run/credentials/student-new)" E2E_AI_PROVIDER_KEY="$(cat /run/credentials/provider)" E2E_AI_PROCESSING_CONTROL_TOKEN="$(cat /run/credentials/control)"; '"$command"
}

run_acceptance_browser_command() {
  run_browser_command "$1" private
}

run_phase6_edge() {
  run_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase6 corepack pnpm exec playwright test tests/e2e/production.spec.ts --project=phase6 --grep "public edge"'
}

seed_phase5_restore_evidence() {
  compose_root exec -T postgres psql --username happylearn --dbname happylearn \
    --no-psqlrc --set ON_ERROR_STOP=1 >/dev/null <<'SQL'
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,started_at,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  remote_snapshot_id,manifest_sha256,logical_bytes,stored_bytes,
  local_expires_at,remote_expires_at
) VALUES (
  '53000000-0000-4000-8000-000000000001','phase6-phase5-browser-seed',
  'manual','succeeded',clock_timestamp()-interval '30 minutes',
  clock_timestamp()-interval '29 minutes',clock_timestamp()-interval '28 minutes',
  27,'phase6-e2e-seed-key',repeat('1',64),repeat('2',64),
  decode(repeat('a',64),'hex'),4096,2048,
  clock_timestamp()+interval '7 days',clock_timestamp()+interval '30 days'
) ON CONFLICT (id) DO NOTHING;

INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
) VALUES
  ('53000000-0000-4000-8000-000000000001','manifest','local',repeat('1',64),
   decode(repeat('b',64),'hex'),1024,clock_timestamp()-interval '28 minutes',clock_timestamp()+interval '7 days'),
  ('53000000-0000-4000-8000-000000000001','manifest','remote',repeat('2',64),
   decode(repeat('c',64),'hex'),1024,clock_timestamp()-interval '27 minutes',clock_timestamp()+interval '30 days')
ON CONFLICT DO NOTHING;

INSERT INTO restore_verifications(
  id,backup_run_id,state,started_at,finished_at,restored_migration_version,
  database_row_counts,checked_object_count,missing_object_count,
  unexpected_object_count,session_revocation_verified,rto_seconds,report_sha256
) VALUES (
  '55000000-0000-4000-8000-000000000001',
  '53000000-0000-4000-8000-000000000001','succeeded',
  clock_timestamp()-interval '20 minutes',clock_timestamp()-interval '18 minutes',
  27,'{"users":1}'::jsonb,0,0,0,true,120,decode(repeat('d',64),'hex')
) ON CONFLICT (id) DO NOTHING;
SQL
}

run_regression() {
  start_acceptance_controls
  if [[ $regression_stage == all || $regression_stage == phase1 ]]; then
    run_acceptance_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase1 corepack pnpm exec playwright test tests/e2e/auth-students.spec.ts tests/e2e/teaching.spec.ts --project=chromium'
  fi
  if [[ $regression_stage == all || $regression_stage == phase2 ]]; then
    run_acceptance_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase2 corepack pnpm exec playwright test tests/e2e/files.spec.ts tests/e2e/learning.spec.ts --project=chromium'
  fi
  if [[ $regression_stage == all || $regression_stage == phase3 ]]; then
    run_acceptance_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase3 corepack pnpm exec playwright test tests/e2e/questions.spec.ts tests/e2e/notifications.spec.ts --project=chromium'
  fi
  if [[ $regression_stage == all || $regression_stage == phase4 ]]; then
    run_acceptance_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase4 corepack pnpm exec playwright test tests/e2e/ai-questions.spec.ts tests/e2e/ai-admin.spec.ts tests/e2e/ai-privacy.spec.ts --project=chromium'
  fi
  if [[ $regression_stage == all || $regression_stage == phase5 ]]; then
    seed_phase5_restore_evidence
    run_acceptance_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase5 corepack pnpm exec playwright test tests/e2e/operations.spec.ts tests/e2e/backup-restore.spec.ts --project=chromium'
  fi
}

run_manual_backup_acceptance() {
  local manual_id state
  # The Phase 5 browser dashboard uses one explicitly synthetic restore row.
  # Remove only that fixed fixture before exercising the real Restic inventory;
  # otherwise retention correctly rejects a database-only snapshot identifier
  # that can never exist in the disposable repository.
  compose_root exec -T postgres psql --username happylearn --dbname happylearn \
    --no-psqlrc --quiet --set ON_ERROR_STOP=1 >/dev/null <<'SQL'
BEGIN;
DELETE FROM restore_verifications
WHERE backup_run_id='53000000-0000-4000-8000-000000000001'::uuid;
DELETE FROM backup_artifacts
WHERE backup_run_id='53000000-0000-4000-8000-000000000001'::uuid;
DELETE FROM backup_runs
WHERE id='53000000-0000-4000-8000-000000000001'::uuid
  AND idempotency_key='phase6-phase5-browser-seed';
COMMIT;
SQL
  manual_id=$(compose_root exec -T postgres psql --username happylearn --dbname happylearn \
    --no-psqlrc --quiet --tuples-only --no-align --set ON_ERROR_STOP=1 \
    --command "SELECT COALESCE((SELECT id::text FROM backup_runs WHERE trigger_kind='manual' AND state='queued' ORDER BY requested_at,id LIMIT 1),'');" \
    | tr -d '[:space:]')
  [[ $manual_id =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    fail 'browser manual backup request missing'

  root_runner -eu -c '
    project_root=$1; production_env=$2; secret_root=$3; repository=$4
    workflow_state=$5; lock_path=$6; recipient=$7; key_id=$8
    export HAPPYLEARN_PRODUCTION_ENV_FILE="$production_env"
    export HAPPYLEARN_AISTOR_LICENSE_FILE="$secret_root/aistor-license"
    export HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$secret_root"
    export HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$repository"
    export HAPPYLEARN_BACKUP_STATE_DIRECTORY="$workflow_state"
    export HAPPYLEARN_BACKUP_LOCK_DIRECTORY="$lock_path"
    export HAPPYLEARN_BACKUP_AGE_RECIPIENT="$recipient"
    export HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID="$key_id"
    exec timeout --foreground --kill-after=30s 7200s \
      "$project_root/scripts/phase5-backup.sh" --project happylearn-prod --trigger manual
  ' _ "$release_project" "$env_file" "$secret_dir" "$backup_dir" \
    "$state_dir/backup-workflows" "$state_dir/backup.lock" "$age_recipient" "phase6-$nonce" >/dev/null

  state=$(compose_root exec -T postgres psql --username happylearn --dbname happylearn \
    --no-psqlrc --quiet --tuples-only --no-align --set ON_ERROR_STOP=1 \
    --command "SELECT state FROM backup_runs WHERE id='$manual_id'::uuid;" | tr -d '[:space:]')
  [[ $state == succeeded ]] || fail 'browser manual backup did not complete'
  printf '{"status":"pass","group":"manual-backup","backupId":"%s"}\n' "$manual_id" \
    >"$artifact_dir/evidence/manual-backup.json"
}

run_mobile() {
  start_acceptance_controls
  run_acceptance_browser_command '. /workspace/scripts/e2e-harness-lib.sh; status=0; E2E_OUTPUT_DIR=/artifacts/results/phase4-mobile corepack pnpm exec playwright test tests/e2e/ai-questions.spec.ts tests/e2e/ai-admin.spec.ts tests/e2e/ai-privacy.spec.ts --project=mobile --grep @phase4-mobile || status="$(preserve_first_failure "$status" "$?")"; E2E_OUTPUT_DIR=/artifacts/results/phase5-mobile corepack pnpm exec playwright test tests/e2e/operations.spec.ts --project=mobile --grep @phase5-mobile || status="$(preserve_first_failure "$status" "$?")"; E2E_OUTPUT_DIR=/artifacts/results/phase6-mobile corepack pnpm exec playwright test tests/e2e/production.spec.ts --project=phase6-mobile || status="$(preserve_first_failure "$status" "$?")"; exit "$status"'
}

run_maintenance_acceptance() {
  compose_root exec -T caddy caddy reload --config /etc/caddy/Caddyfile.maintenance --adapter caddyfile >/dev/null
  run_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase6-maintenance corepack pnpm exec playwright test tests/e2e/production.spec.ts --project=phase6 --grep "maintenance mode"'
  compose_root exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
}

wait_service_health() {
  local service=$1 deadline=$((SECONDS + 240)) status
  until status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "${project}-${service}-1" 2>/dev/null) && [[ $status == healthy || $status == running ]]; do
    ((SECONDS < deadline)) || fail "$service restart readiness timeout"
    sleep 2
  done
}

run_restart_acceptance() {
  run_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase6-seed corepack pnpm exec playwright test tests/e2e/production.spec.ts --project=phase6 --grep "seeds an accepted"'
  local service
  for service in app worker caddy postgres redis minio; do
    compose_root restart --timeout 30 "$service"
    wait_service_health "$service"
  done
  compose_root stop --timeout 30
  compose_root start
  for service in postgres redis minio app worker caddy; do wait_service_health "$service"; done
  run_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase6-restart corepack pnpm exec playwright test tests/e2e/production.spec.ts --project=phase6 --grep "restart preserves"'
}

activate_manifest_environment() {
  local manifest=$1
  root_runner -eu -c '
    environment=$1; manifest=$2; temporary=$(mktemp --tmpdir="$(dirname "$environment")" .phase6-env.XXXXXX)
    grep -Ev "^HAPPYLEARN_(APP|WORKER|MIGRATE|BACKUP|CADDY|POSTGRES|REDIS|MINIO)_IMAGE=" "$environment" >"$temporary"
    for mapping in APP:app WORKER:worker MIGRATE:migrate BACKUP:backup CADDY:caddy POSTGRES:postgres REDIS:redis MINIO:minio; do
      variable=${mapping%%:*}; key=${mapping#*:}; value=$(jq -er --arg key "$key" ".images[\$key]" "$manifest")
      printf "HAPPYLEARN_%s_IMAGE=%s\n" "$variable" "$value" >>"$temporary"
    done
    chmod 0600 "$temporary"; chown 0:0 "$temporary"; sync "$temporary"; mv -f -- "$temporary" "$environment"; sync "$(dirname "$environment")"
  ' _ "$env_file" "$manifest"
}

refresh_manifest_recovery_evidence() {
  local manifest=$1 evidence now body
  evidence=$(root_runner -eu -c 'jq -er ".backupEvidenceId" "$1"' _ "$manifest")
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  body=$(jq -cn --arg evidence "$evidence" --arg now "$now" '{status:"verified",evidenceId:$evidence,verifiedAt:$now}')
  root_runner -eu -c 'printf "%s\n" "$1" >"$2/recovery/latest.json"; chown 0:0 "$2/recovery/latest.json"; chmod 0600 "$2/recovery/latest.json"; sync "$2/recovery/latest.json"' _ "$body" "$state_dir"
}

invoke_local_release() {
  local manifest=$1 version=$2 injection=${3:-}
  if [[ -n $injection ]]; then
    root_runner -eu -c 'export HAPPYLEARN_RELEASE_FAILURE_INJECTION="$1"; shift; exec "$@"' _ "$injection" \
      "$release_project/scripts/prod-release.sh" --project-dir "$release_project" --env-file "$env_file" --manifest "$manifest" \
      --version "$version" --mode local --confirm-maintenance-window
  else
    root_runner "$release_project/scripts/prod-release.sh" --project-dir "$release_project" --env-file "$env_file" --manifest "$manifest" \
      --version "$version" --mode local --confirm-maintenance-window
  fi
}

run_successful_release() {
  [[ $release_completed == false ]] || return 0
  run_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase6-release-seed corepack pnpm exec playwright test tests/e2e/production.spec.ts --project=phase6 --grep "seeds an accepted"'
  activate_manifest_environment "$release_manifest_b"
  refresh_manifest_recovery_evidence "$release_manifest_b"
  invoke_local_release "$release_manifest_b" 1.0.0-rc.2
  expected_version=1.0.0-rc.2
  run_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase6-release corepack pnpm exec playwright test tests/e2e/release-rollback.spec.ts --project=phase6 --grep "successful release"'
  release_completed=true
}

run_rollback_acceptance() {
  run_successful_release
  accepted_write_marker="Phase 6 accepted after release $nonce"
  durable_marker=$accepted_write_marker
  run_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase6-rollback-seed corepack pnpm exec playwright test tests/e2e/production.spec.ts --project=phase6 --grep "seeds an accepted"'
  activate_manifest_environment "$release_manifest_a"
  refresh_manifest_recovery_evidence "$release_manifest_a"
  if invoke_local_release "$release_manifest_a" 1.0.0-rc.1 app_readiness_failure; then
    fail 'injected release unexpectedly succeeded'
  fi
  previous_version=1.0.0-rc.2
  run_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase6-rollback corepack pnpm exec playwright test tests/e2e/release-rollback.spec.ts --project=phase6 --grep "failed release"'
}

run_recovery_acceptance() {
  run_successful_release
  start_acceptance_controls
  run_acceptance_browser_command 'E2E_OUTPUT_DIR=/artifacts/results/phase6-recovery-object-seed corepack pnpm exec playwright test tests/e2e/files.spec.ts --project=chromium --grep "processing, policy, Range, replacement, and rollback"'
  local first_evidence first_evidence_id second_json second_evidence second_evidence_document third_json third_evidence third_evidence_document fourth_json fourth_evidence fourth_evidence_document release_id teacher_credential
  local wrong_secret_dir wrong_env wrong_target wrong_log tampered_repository tampered_env tampered_target tampered_log missing_env missing_target missing_log
  first_evidence=$(root_runner -eu -c 'cat "$1/recovery/pre-release.json"' _ "$state_dir")
  first_evidence_id=$(jq -er '.evidenceId' <<<"$first_evidence")
  [[ $first_evidence_id =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] || fail 'first recovery point missing'
  root_runner -eu -c 'printf "%s\n" "$1" >"$2/recovery/latest.json"; chown 0:0 "$2/recovery/latest.json"; chmod 0600 "$2/recovery/latest.json"' _ "$first_evidence" "$state_dir"
  release_id=$(cat /proc/sys/kernel/random/uuid)
  second_json=$(root_runner "$release_project/scripts/prod-backup.sh" --project-dir "$release_project" --env-file "$env_file" --release-id "$release_id")
  second_evidence=$(jq -er 'select(.status=="pass") | .evidenceId' <<<"$second_json")
  [[ $second_evidence =~ ^[0-9a-f-]{36}$ ]] || fail 'second recovery point missing'
  second_evidence_document=$(root_runner -eu -c 'cat "$1/recovery/pre-release.json"' _ "$state_dir")

  root_runner -eu -c 'cp -- "$1/recovery/pre-release.json" "$1/recovery/latest.json"; chown 0:0 "$1/recovery/latest.json"; chmod 0600 "$1/recovery/latest.json"; sync "$1/recovery/latest.json"' _ "$state_dir"
  release_id=$(cat /proc/sys/kernel/random/uuid)
  third_json=$(root_runner "$release_project/scripts/prod-backup.sh" --project-dir "$release_project" --env-file "$env_file" --release-id "$release_id")
  third_evidence=$(jq -er 'select(.status=="pass") | .evidenceId' <<<"$third_json")
  [[ $third_evidence =~ ^[0-9a-f-]{36}$ && $third_evidence != "$second_evidence" ]] || fail 'third recovery point missing'
  third_evidence_document=$(root_runner -eu -c 'cat "$1/recovery/pre-release.json"' _ "$state_dir")

  root_runner -eu -c 'cp -- "$1/recovery/pre-release.json" "$1/recovery/latest.json"; chown 0:0 "$1/recovery/latest.json"; chmod 0600 "$1/recovery/latest.json"; sync "$1/recovery/latest.json"' _ "$state_dir"
  release_id=$(cat /proc/sys/kernel/random/uuid)
  fourth_json=$(root_runner "$release_project/scripts/prod-backup.sh" --project-dir "$release_project" --env-file "$env_file" --release-id "$release_id")
  fourth_evidence=$(jq -er 'select(.status=="pass") | .evidenceId' <<<"$fourth_json")
  [[ $fourth_evidence =~ ^[0-9a-f-]{36}$ && $fourth_evidence != "$third_evidence" ]] || fail 'fourth recovery point missing'
  fourth_evidence_document=$(root_runner -eu -c 'cat "$1/recovery/pre-release.json"' _ "$state_dir")

  teacher_credential="$state_dir/restore-control/teacher-credential"
  root_runner -eu -c 'password=$(cat "$1"); jq -cn --arg password "$password" '\''{username:"admin",password:$password}'\'' >"$2"; chown 0:0 "$2"; chmod 0400 "$2"' _ "$admin_password_file" "$teacher_credential"
  compose_root --profile release up --detach --no-deps release-control
  local deadline=$((SECONDS + 120))
  until compose_root --profile release logs --no-color release-control 2>/dev/null | grep -Fq release_mode_ready; do
    ((SECONDS < deadline)) || fail 'restore release-control readiness timeout'; sleep 2
  done
  compose_root exec -T caddy caddy reload --config /etc/caddy/Caddyfile.maintenance --adapter caddyfile >/dev/null

  root_runner -eu -c '
    printf "%s\n" "$1" >"$3/recovery/latest.json"
    printf "%s\n" "$2" >"$3/recovery/pre-release.json"
    chown 0:0 "$3/recovery/latest.json" "$3/recovery/pre-release.json"
    chmod 0600 "$3/recovery/latest.json" "$3/recovery/pre-release.json"
    sync "$3/recovery/latest.json" "$3/recovery/pre-release.json"
  ' _ "$first_evidence" "$second_evidence_document" "$state_dir"

  wrong_secret_dir="$tmpdir/wrong-restore-secrets"
  wrong_env="$tmpdir/wrong-restore.env"
  wrong_log="$tmpdir/wrong-restore.log"
  wrong_target="happylearn-phase5-restore-$(printf '%s' "$nonce-wrong-key" | sha256sum | cut -c1-12)"
  restore_projects+=("$wrong_target")
  root_runner -eu -c '
    source_directory=$1; target_directory=$2; source_environment=$3; target_environment=$4
    cp -a -- "$source_directory" "$target_directory"
    printf "wrong-%s\n" "$(cat /proc/sys/kernel/random/uuid)" >"$target_directory/backup-password"
    chown 10003:10003 "$target_directory/backup-password"
    chmod 0600 "$target_directory/backup-password"
    sed "s|^HAPPYLEARN_SECRET_DIR=.*|HAPPYLEARN_SECRET_DIR=$target_directory|" "$source_environment" >"$target_environment"
    chown 0:0 "$target_environment"; chmod 0600 "$target_environment"
  ' _ "$secret_dir" "$wrong_secret_dir" "$env_file" "$wrong_env"
  if run_bounded 15000 root_runner "$release_project/scripts/prod-restore.sh" --project-dir "$release_project" --env-file "$wrong_env" \
    --mode local --target-project "$wrong_target" --backup-id "$first_evidence_id" --destructive \
    --confirmation "$wrong_target:$first_evidence_id" >"$wrong_log" 2>&1; then
    fail 'restore accepted wrong repository key'
  fi
  grep -Fq 'wrong password or no key found' "$wrong_log" || {
    cat "$wrong_log" >&2
    fail 'wrong-key restore did not reach repository authentication'
  }

  tampered_repository="$tmpdir/tampered-repository"
  tampered_env="$tmpdir/tampered-restore.env"
  tampered_log="$tmpdir/tampered-restore.log"
  tampered_target="happylearn-phase5-restore-$(printf '%s' "$nonce-tampered-pack" | sha256sum | cut -c1-12)"
  restore_projects+=("$tampered_target")
  root_runner -eu -c '
    source_repository=$1; target_repository=$2; source_environment=$3; target_environment=$4
    cp -a -- "$source_repository" "$target_repository"
    pack=$(find -P "$target_repository/data" -type f -print -quit)
    test -n "$pack" && test -f "$pack" && test ! -L "$pack"
    printf x >>"$pack"
    sed "s|^HAPPYLEARN_BACKUP_HOST_PATH=.*|HAPPYLEARN_BACKUP_HOST_PATH=$target_repository|" "$source_environment" >"$target_environment"
    chown 0:0 "$target_environment"; chmod 0600 "$target_environment"
  ' _ "$backup_dir" "$tampered_repository" "$env_file" "$tampered_env"
  if run_bounded 15000 root_runner "$release_project/scripts/prod-restore.sh" --project-dir "$release_project" --env-file "$tampered_env" \
    --mode local --target-project "$tampered_target" --backup-id "$second_evidence" --destructive \
    --confirmation "$tampered_target:$second_evidence" >"$tampered_log" 2>&1; then
    fail 'restore accepted tampered repository pack'
  fi
  grep -Fq 'repository contains errors' "$tampered_log" || {
    cat "$tampered_log" >&2
    fail 'tampered-pack restore did not reach repository integrity checking'
  }

  root_runner -eu -c '
    printf "%s\n" "$1" >"$3/recovery/latest.json"
    printf "%s\n" "$2" >"$3/recovery/pre-release.json"
    chown 0:0 "$3/recovery/latest.json" "$3/recovery/pre-release.json"
    chmod 0600 "$3/recovery/latest.json" "$3/recovery/pre-release.json"
    sync "$3/recovery/latest.json" "$3/recovery/pre-release.json"
  ' _ "$second_evidence_document" "$third_evidence_document" "$state_dir"

  missing_env="$tmpdir/missing-object-restore.env"
  missing_log="$tmpdir/missing-object-restore.log"
  missing_target="happylearn-phase5-restore-$(printf '%s' "$nonce-missing-object" | sha256sum | cut -c1-12)"
  restore_projects+=("$missing_target")
  root_runner -eu -c '
    source_environment=$1; target_environment=$2
    cp -- "$source_environment" "$target_environment"
    printf "HAPPYLEARN_LOCAL_RESTORE_FAILURE_INJECTION=missing_object\n" >>"$target_environment"
    chown 0:0 "$target_environment"; chmod 0600 "$target_environment"
  ' _ "$env_file" "$missing_env"
  if run_bounded 15000 root_runner "$release_project/scripts/prod-restore.sh" --project-dir "$release_project" --env-file "$missing_env" \
    --mode local --target-project "$missing_target" --backup-id "$third_evidence" --destructive \
    --confirmation "$missing_target:$third_evidence" >"$missing_log" 2>&1; then
    fail 'restore accepted a genuinely missing restored object'
  fi
  grep -Fq 'phase5_restore: local_missing_object_injected' "$missing_log" || {
    cat "$missing_log" >&2
    fail 'missing-object restore did not prove a real object deletion'
  }
  grep -Fq '"category":"verification","event":"backup.restore_check_failure"' "$missing_log" || {
    cat "$missing_log" >&2
    fail 'missing-object restore did not reach the real integrity verifier'
  }

  root_runner -eu -c '
    printf "%s\n" "$1" >"$3/recovery/latest.json"
    printf "%s\n" "$2" >"$3/recovery/pre-release.json"
    chown 0:0 "$3/recovery/latest.json" "$3/recovery/pre-release.json"
    chmod 0600 "$3/recovery/latest.json" "$3/recovery/pre-release.json"
    sync "$3/recovery/latest.json" "$3/recovery/pre-release.json"
  ' _ "$third_evidence_document" "$fourth_evidence_document" "$state_dir"

  restore_project="happylearn-phase5-restore-$nonce"
  restore_projects+=("$restore_project")
  run_bounded 15000 root_runner "$release_project/scripts/prod-restore.sh" --project-dir "$release_project" --env-file "$env_file" \
    --mode local --target-project "$restore_project" --backup-id "$fourth_evidence" --destructive \
    --confirmation "$restore_project:$fourth_evidence"
  root_runner -eu -c '
    jq -e --arg project "$1" --arg backup "$2" \
      '\''.status=="verified" and .targetProject==$project and .backupId==$backup and .switchAutomatic==false'\'' \
      "$3" >/dev/null
  ' _ "$restore_project" "$fourth_evidence" "$state_dir/switch-proposal-$fourth_evidence.json"
  printf '{"status":"pass","group":"recovery","backupId":"%s","targetProject":"%s"}\n' "$fourth_evidence" "$restore_project" >"$artifact_dir/evidence/recovery.json"
}

ingest_resource_host_sample() {
  local sampler=$tmpdir/host-sampler sampler_input=$tmpdir/host-sampler-input.json
  local compose_rows=$tmpdir/host-compose.ndjson stats_rows=$tmpdir/host-stats.ndjson
  local payload=$tmpdir/host-payload.json container metadata stats service
  local timestamp sample_nonce signature status root_percent backup_percent
  local host_sample_runner="${prefix}host_sample"

  mkdir -p "$tmpdir/go-cache"
  chmod 0700 "$tmpdir/go-cache"
  GOTOOLCHAIN=local GOCACHE="$tmpdir/go-cache" CGO_ENABLED=0 \
    go build -trimpath -o "$sampler" ./cmd/host-sampler
  chmod 0555 "$sampler"
  : >"$compose_rows"; : >"$stats_rows"
  chmod 0600 "$compose_rows" "$stats_rows"
  for service in caddy app worker postgres redis minio; do
    container="${project}-${service}-1"
    metadata=$(docker inspect --format \
      '{"state":{{json .State.Status}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}},"restarts":{{.RestartCount}}}' \
      "$container") || fail 'host sample inspect failed'
    stats=$(docker stats --no-stream --format '{{json .}}' "$container") || fail 'host sample stats failed'
    jq -cn --arg service "$service" --argjson metadata "$metadata" \
      '{service:$service,state:$metadata.state,health:$metadata.health,restarts:$metadata.restarts}' \
      >>"$compose_rows"
    jq -cn --arg service "$service" --argjson stats "$stats" \
      '{service:$service,cpuPercent:$stats.CPUPerc,memoryUsage:$stats.MemUsage}' \
      >>"$stats_rows"
  done
  root_percent=$(df -Pk / | awk 'NR == 2 {sub(/%$/, "", $5); print $5}')
  backup_percent=$(df -Pk "$backup_dir" | awk 'NR == 2 {sub(/%$/, "", $5); print $5}')
  [[ $root_percent =~ ^[0-9]+$ && $backup_percent =~ ^[0-9]+$ ]] || fail 'host filesystem sample invalid'
  timestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  jq -cn --arg observedAt "$timestamp" --slurpfile compose "$compose_rows" --slurpfile stats "$stats_rows" \
    --arg root "$root_percent%" --arg backup "$backup_percent%" \
    '{schemaVersion:1,observedAt:$observedAt,compose:$compose,stats:$stats,filesystems:[{filesystem:"root",usedPercent:$root},{filesystem:"backup",usedPercent:$backup}]}' \
    >"$sampler_input"
  "$sampler" payload <"$sampler_input" >"$payload"
  chmod 0444 "$payload"
  timestamp=$(date -u +%s)
  sample_nonce=$(openssl rand -hex 16)
  signature=$(docker run --rm --interactive --name "$host_sample_runner" --label "$owner_label=$nonce" \
    --network none --read-only --user 10001:10001 --cap-drop ALL --security-opt no-new-privileges \
    --memory 64m --cpus .05 --volume "$sampler:/host-sampler:ro" \
    --volume "$secret_dir/host-metrics-hmac:/run/host-metrics-hmac:ro" \
    --entrypoint /host-sampler "$image_set_a_app" sign --secret-file /run/host-metrics-hmac \
    --timestamp "$timestamp" --nonce "$sample_nonce" <"$payload") || fail 'host sample signing failed'
  [[ $signature =~ ^sha256=[0-9a-f]{64}$ ]] || fail 'host sample signature invalid'
  status=$(docker run --rm --name "$host_sample_runner" --label "$owner_label=$nonce" \
    --network "${project}_private" --read-only --user 10001:10001 --cap-drop ALL \
    --security-opt no-new-privileges --memory 64m --cpus .05 \
    --volume "$payload:/run/host-payload.json:ro" --entrypoint /usr/bin/curl "$image_set_a_app" \
    --silent --show-error --output /dev/null --write-out '%{http_code}' --request POST \
    --connect-timeout 3 --max-time 10 -H 'Content-Type: application/json' \
    -H "X-HL-Timestamp: $timestamp" -H "X-HL-Nonce: $sample_nonce" -H "X-HL-Signature: $signature" \
    --data-binary @/run/host-payload.json http://app:9090/internal/host-samples) || fail 'host sample ingestion failed'
  [[ $status == 204 ]] || fail 'host sample ingestion rejected'
}

run_resource_acceptance() {
  local resource_result=$artifact_dir/evidence/resource-result.json
  prepare_browser_workspace
  start_acceptance_controls
  seed_phase5_restore_evidence
  ingest_resource_host_sample

  HAPPYLEARN_PHASE6_PROJECT="$project" \
  HAPPYLEARN_PHASE6_RESOURCE_OUTPUT="$artifact_dir/evidence/resources.ndjson" \
  HAPPYLEARN_PHASE6_RESOURCE_HTTPS_BASE_URL="https://$domain:$https_port" \
  HAPPYLEARN_PHASE6_RESOURCE_HOSTNAME="$domain" \
  HAPPYLEARN_PHASE6_RESOURCE_CA_FILE="$ca_file" \
    "$script_dir/e2e-phase6_resources.sh" >"$resource_result" &
  resource_pid=$!

  # The complete browser regression overlaps the live sampler and supplies the
  # representative teaching, file, notification, Q&A, AI SSE, operations,
  # host-sample, and Caddy traffic required by the resource gate.
  run_regression
  kill -0 "$resource_pid" 2>/dev/null || fail 'resource sampler ended before the live backup window'

  # The UI-created manual run is serviced by the real Phase 5 host coordinator,
  # which owns the durable drain and worker-stop proof. The sampler must observe
  # the disposable backup service while that worker remains absent.
  run_manual_backup_acceptance

  wait "$resource_pid" || fail 'resource sampler rejected the live capture'
  resource_pid=''
  jq -e '.status == "pass" and .samples == 180 and .steadyObserved == true and .workerDrainedBackupObserved == true' \
    "$resource_result" >/dev/null || fail 'resource result evidence invalid'
}

generate_age_material() {
  docker run --rm --network none --user 10003:10003 --volume "$backup_dir:/output" --entrypoint /usr/local/bin/age-keygen "$backup_image" -o /output/backup-age-identity >/dev/null
  docker run --rm --network none --user 10003:10003 --volume "$backup_dir:/output" --entrypoint /usr/local/bin/age-keygen "$backup_image" -y /output/backup-age-identity >"$tmpdir/age-recipient"
  age_recipient=$(tr -d '[:space:]' <"$tmpdir/age-recipient")
  [[ $age_recipient =~ ^age1[023456789ac-hj-np-z]{20,100}$ ]] || fail 'age recipient generation failed'
  docker run --rm --network none --volume "$secret_dir:/secrets" --volume "$backup_dir:/output" "$runner_base" sh -eu -c 'cp /output/backup-age-identity /secrets/backup-age-identity; chown 10003:10003 /secrets/backup-age-identity; chmod 0600 /secrets/backup-age-identity'
}

write_environment() {
  cat >"$env_file" <<EOF
COMPOSE_PROJECT_NAME=happylearn-prod
HAPPYLEARN_DOMAIN=$domain
HAPPYLEARN_TIMEZONE=Asia/Shanghai
HAPPYLEARN_APP_IMAGE=$image_set_a_app
HAPPYLEARN_WORKER_IMAGE=$image_set_a_worker
HAPPYLEARN_MIGRATE_IMAGE=$migrate_image
HAPPYLEARN_BACKUP_IMAGE=$backup_image
HAPPYLEARN_CADDY_IMAGE=$caddy_image
HAPPYLEARN_POSTGRES_IMAGE=$postgres_image
HAPPYLEARN_REDIS_IMAGE=$redis_image
HAPPYLEARN_MINIO_IMAGE=$minio_image
HAPPYLEARN_BACKUP_HOST_PATH=$backup_dir
HAPPYLEARN_RELEASE_STATE_PATH=$state_dir
HAPPYLEARN_BACKUP_AGE_RECIPIENT=$age_recipient
HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID=phase6-$nonce
HAPPYLEARN_SECRET_DIR=$secret_dir
HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE=$secret_dir/host-metrics-hmac
HAPPYLEARN_CADDYFILE=$release_project/deploy/Caddyfile.local
HAPPYLEARN_MAINTENANCE_CADDYFILE=$release_project/deploy/Caddyfile.maintenance.local
HAPPYLEARN_MAINTENANCE_FILE=$release_project/deploy/maintenance.html
HAPPYLEARN_LOCAL_HTTP_PORT=$http_port
HAPPYLEARN_LOCAL_HTTPS_PORT=$https_port
EOF
  chmod 0600 "$env_file"
  docker run --rm --network none --volume "$env_file:/input" "$runner_base" chown 0:0 /input
  env_ready=true
}

write_manifest() {
  local destination=$1 version=$2 commit=$3 app_image=$4 worker_image=$5 evidence="phase6-initial-$nonce" compose_hash caddy_hash
  local resolved="$tmpdir/resolved-$version.yml" candidate_env="$tmpdir/env-$version"
  root_runner -eu -c '
    repository=$1; source_env=$2; candidate_env=$3; resolved=$4; app=$5; worker=$6
    grep -Ev "^HAPPYLEARN_(APP|WORKER)_IMAGE=" "$source_env" >"$candidate_env"
    printf "HAPPYLEARN_APP_IMAGE=%s\nHAPPYLEARN_WORKER_IMAGE=%s\n" "$app" "$worker" >>"$candidate_env"
    chmod 0600 "$candidate_env"
    docker compose --project-name "$HAPPYLEARN_LOCAL_COMPOSE_PROJECT" --project-directory "$repository" --env-file "$candidate_env" -f "$repository/deploy/compose.prod.yml" -f "$repository/deploy/compose.prod.local.yml" config >"$resolved"
  ' _ "$repo_root" "$env_file" "$candidate_env" "$resolved" "$app_image" "$worker_image"
  # shellcheck disable=SC2016
  compose_hash=$(root_runner -eu -c 'sha256sum "$1"' _ "$resolved"); compose_hash=${compose_hash%% *}
  caddy_hash=$(sha256sum "$repo_root/deploy/Caddyfile.local" | awk '{print $1}')
  jq -cn --arg version "$version" --arg commit "$commit" --arg builtAt '2026-08-02T00:00:00Z' \
    --arg app "$app_image" --arg worker "$worker_image" --arg migrate "$migrate_image" --arg backup "$backup_image" \
    --arg caddy "$caddy_image" --arg postgres "$postgres_image" --arg redis "$redis_image" --arg minio "$minio_image" \
    --arg compose "$compose_hash" --arg caddyHash "$caddy_hash" --arg evidence "$evidence" \
    '{version:$version,commit:$commit,builtAt:$builtAt,images:{app:$app,worker:$worker,migrate:$migrate,backup:$backup,caddy:$caddy,postgres:$postgres,redis:$redis,minio:$minio},minSchemaVersion:27,maxSchemaVersion:27,composeSha256:$compose,caddySha256:$caddyHash,backupEvidenceId:$evidence,createdBy:"phase6-e2e",createdAt:$builtAt}' >"$destination"
  chmod 0600 "$destination"
}

install_stack() {
  build_runner
  prepare_release_project
  start_registry
  build_image_set a 1.0.0-rc.1 "$(printf 'a%.0s' {1..40})"
  if [[ $group =~ ^(all|release|rollback|recovery|failure-matrix)$ ]]; then
    build_image_set b 1.0.0-rc.2 "$(printf 'b%.0s' {1..40})"
  fi
  build_shared_images
  prepare_secrets
  generate_age_material
  write_environment
  write_manifest "$manifest_a" 1.0.0-rc.1 "$(printf 'a%.0s' {1..40})" "$image_set_a_app" "$image_set_a_worker"
  if [[ $group =~ ^(all|release|rollback|recovery|failure-matrix)$ ]]; then
    write_manifest "$manifest_b" 1.0.0-rc.2 "$(printf 'b%.0s' {1..40})" "$image_set_b_app" "$image_set_b_worker"
  fi
  root_runner -eu -c '
    install -o 0 -g 0 -m 0600 "$1" "$2"
    if [[ -f $3 ]]; then install -o 0 -g 0 -m 0600 "$3" "$4"; fi
  ' _ "$manifest_a" "$release_manifest_a" "$manifest_b" "$release_manifest_b"
  jq '.minSchemaVersion = 0' "$manifest_a" >"$bootstrap_manifest"
  chmod 0600 "$bootstrap_manifest"
  jq -cn --arg evidence "phase6-initial-$nonce" --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '{status:"verified",evidenceId:$evidence,verifiedAt:$now}' >"$tmpdir/recovery.json"
  root_runner -eu -c 'install -o 10001 -g 10001 -m 0600 "$1" "$3/release-input/candidate-manifest.json"; install -o 0 -g 0 -m 0600 "$2" "$3/active-manifest.json"; install -o 0 -g 0 -m 0600 "$4" "$3/recovery/latest.json"' _ "$bootstrap_manifest" "$manifest_a" "$state_dir" "$tmpdir/recovery.json"
  root_runner -eu -c 'docker compose --project-name "$HAPPYLEARN_LOCAL_COMPOSE_PROJECT" --project-directory "$1" --env-file "$2" -f "$1/deploy/compose.prod.yml" -f "$1/deploy/compose.prod.local.yml" --profile release up --detach --wait --wait-timeout 180 postgres redis minio' _ "$repo_root" "$env_file"
  root_runner -eu -c 'docker compose --project-name "$HAPPYLEARN_LOCAL_COMPOSE_PROJECT" --project-directory "$1" --env-file "$2" -f "$1/deploy/compose.prod.yml" -f "$1/deploy/compose.prod.local.yml" --profile release run --rm migrate' _ "$repo_root" "$env_file"
  root_runner -eu -c 'install -o 10001 -g 10001 -m 0600 "$1" "$2/release-input/candidate-manifest.json"' _ "$manifest_a" "$state_dir"
  root_runner -eu -c 'docker compose --project-name "$HAPPYLEARN_LOCAL_COMPOSE_PROJECT" --project-directory "$1" --env-file "$2" -f "$1/deploy/compose.prod.yml" -f "$1/deploy/compose.prod.local.yml" run --rm --no-deps -v "$3:/run/admin-password:ro" --entrypoint /app/happylearn-admin app create-teacher --username admin --display-name "Phase 6 Teacher" --password-file /run/admin-password' _ "$repo_root" "$env_file" "$admin_password_file"
  compose_root up --detach app worker caddy
  local deadline=$((SECONDS + 180))
  until [[ $(compose_root ps --status running --quiet app worker caddy | wc -l) -eq 3 ]]; do ((SECONDS < deadline)) || fail 'application readiness timeout'; sleep 2; done
  local ca_deadline=$((SECONDS + 120))
  until docker cp "${project}-caddy-1:/data/caddy/pki/authorities/local/root.crt" "$ca_file" >/dev/null 2>&1; do
    ((SECONDS < ca_deadline)) || fail 'Caddy local CA readiness timeout'
    sleep 2
  done
  chmod 0600 "$ca_file"
  local edge_deadline=$((SECONDS + 120))
  until curl --noproxy '*' --fail --silent --cacert "$ca_file" --resolve "$domain:$https_port:127.0.0.1" "https://$domain:$https_port/api/v1/health/live" >/dev/null; do
    ((SECONDS < edge_deadline)) || fail 'Caddy HTTPS readiness timeout'
    sleep 2
  done
  chromium_spki=$(openssl s_client -connect "127.0.0.1:$https_port" -servername "$domain" -CAfile "$ca_file" </dev/null 2>/dev/null \
    | openssl x509 -pubkey -noout \
    | openssl pkey -pubin -outform DER \
    | openssl dgst -sha256 -binary \
    | openssl base64 -A)
  [[ $chromium_spki =~ ^[A-Za-z0-9+/]{43}=$ ]] || fail 'Caddy leaf SPKI derivation failed'
  printf '{"status":"pass","group":"install","project":"%s"}\n' "$project" >"$artifact_dir/evidence/install.json"
}

run_selected_group() {
  case $group in
    install) ;;
    regression) prepare_browser_workspace; run_phase6_edge; run_regression ;;
    mobile) prepare_browser_workspace; run_mobile ;;
    restart) prepare_browser_workspace; run_phase6_edge; run_maintenance_acceptance; run_restart_acceptance ;;
    security)
      E2E_PHASE6_HTTPS_BASE_URL="https://$domain:$https_port" E2E_PHASE6_HTTP_BASE_URL="http://$domain:$http_port" \
      E2E_PHASE6_HOSTNAME="$domain" E2E_PHASE6_CA_FILE="$ca_file" HAPPYLEARN_PHASE6_PROJECT="$project" \
      HAPPYLEARN_PHASE6_ENV_FILE="$env_file" "$script_dir/e2e-phase6_security.sh" --live
      ;;
    recovery) prepare_browser_workspace; run_recovery_acceptance ;;
    release) prepare_browser_workspace; run_successful_release ;;
    rollback) prepare_browser_workspace; run_rollback_acceptance ;;
    resources) run_resource_acceptance ;;
    failure-matrix) run_release_failure_matrix ;;
    all) prepare_browser_workspace; run_phase6_edge; run_regression; run_mobile; run_maintenance_acceptance; run_restart_acceptance; run_manual_backup_acceptance; run_rollback_acceptance; run_recovery_acceptance; run_release_failure_matrix ;;
  esac
}

install_stack
run_selected_group
printf 'phase6 e2e: PASS: %s project=%s\n' "$group" "$project"
