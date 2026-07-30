#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
source "$script_dir/e2e-harness-lib.sh"
repo_root="$(cd "$script_dir/.." && pwd -P)"

canonicalize_directory_target() {
  local input="${1:?path required}" probe part suffix='' base
  [[ "$input" == /* ]] || return 1
  probe="${input%/}"
  [[ -n "$probe" ]] || return 1
  while [[ ! -e "$probe" ]]; do
    part="$(basename "$probe")"
    [[ "$part" != . && "$part" != .. && -n "$part" ]] || return 1
    suffix="/$part$suffix"
    base="$(dirname "$probe")"
    [[ "$base" != "$probe" ]] || return 1
    probe="$base"
  done
  [[ -d "$probe" ]] || return 1
  base="$(cd "$probe" && pwd -P)" || return 1
  printf '%s%s\n' "$base" "$suffix"
}

license_file="${HAPPYLEARN_AISTOR_LICENSE_FILE:-}"
if [[ -z "$license_file" || "$license_file" != /* || ! -r "$license_file" ]]; then
  echo "HAPPYLEARN_AISTOR_LICENSE_FILE must be an absolute readable AIStor Free license file" >&2
  exit 2
fi
license_file="$(cd "$(dirname "$license_file")" && pwd)/$(basename "$license_file")"

e2e_group="${HAPPYLEARN_E2E_GROUP:-all}"
case "$e2e_group" in
  all|phase4) ;;
  *) echo "HAPPYLEARN_E2E_GROUP must be all or phase4" >&2; exit 2 ;;
esac

nonce="$(date +%s)-$RANDOM"
prefix="happylearn_phase4_${nonce}"
network="${prefix}_net"
postgres="${prefix}_postgres"
redis="${prefix}_redis"
minio="${prefix}_minio"
app="${prefix}_app"
worker="${prefix}_worker"
processing_supervisor="$worker"
fake_ai="${prefix}_fake_ai"
data_init="${prefix}_data_init"
runner_init="${prefix}_runner_init"
admin_init="${prefix}_admin_init"
fixture_runner="${prefix}_fixture_runner"
artifact_init="${prefix}_artifact_init"
install_runner="${prefix}_install_runner"
e2e_runner="${prefix}_e2e_runner"
data_volume="${prefix}_data"
fixture_volume="${prefix}_fixtures"
runner_volume="${prefix}_runner"
app_image="happylearn:phase4-${nonce}"
worker_image="happylearn-worker:phase4-${nonce}"
fake_ai_image="happylearn-fake-ai:phase4-${nonce}"
supervisor_image="happylearn-e2e-worker:phase4-${nonce}"
playwright_image="mcr.microsoft.com/playwright:v1.57.0-noble"
artifact_init_image="alpine:3.22.1@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1"
minio_image="quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120"
database="happylearn_phase4_${nonce//-/_}"
admin_password="Phase4 Admin ${nonce}!"
student_password="Phase4 Student ${nonce}!"
student_new_password="Phase4 Changed ${nonce}!"
object_secret="phase4-object-${nonce}-secret"
allowed_artifact_root="$repo_root/test-results"
artifact_input="${E2E_ARTIFACT_DIR:-$allowed_artifact_root/phase4}"
allowed_artifact_root_canonical="$(canonicalize_directory_target "$allowed_artifact_root")" || {
  echo "repository test-results root cannot be resolved safely" >&2
  exit 2
}
artifact_dir="$(canonicalize_directory_target "$artifact_input")" || {
  echo "E2E_ARTIFACT_DIR must be an absolute safe directory below repository test-results" >&2
  exit 2
}
if [[ "$allowed_artifact_root_canonical" != "$allowed_artifact_root" ||
      "$artifact_dir" == "$allowed_artifact_root_canonical" ||
      "$artifact_dir" != "$allowed_artifact_root_canonical"/* ]]; then
  echo "E2E_ARTIFACT_DIR must be an absolute safe directory below repository test-results" >&2
  exit 2
fi
artifact_init_script="$script_dir/init-e2e-artifacts.sh"
tmpdir="$(mktemp -d)"
admin_user="$(id -u):$(id -g)"
early_cleanup() {
  local exit_status=$?
  trap - EXIT INT TERM
  rm -rf "$tmpdir"
  exit "$exit_status"
}
trap early_cleanup EXIT INT TERM
master_key_file="$tmpdir/master-key"
provider_key_file="$tmpdir/provider-key"
control_key_file="$tmpdir/control-key"
secret_volume="${prefix}_secrets"
secret_init="${prefix}_secret_init"
temporary_containers=("$data_init" "$runner_init" "$admin_init" "$fixture_runner" "$artifact_init" "$install_runner" "$e2e_runner")
temporary_containers+=("$secret_init")
service_containers=("$fake_ai" "$app" "$worker" "$minio" "$redis" "$postgres")

umask 077
openssl rand -base64 32 > "$master_key_file"
openssl rand -base64 32 > "$provider_key_file"
openssl rand -base64 32 > "$control_key_file"
chmod 0600 "$master_key_file" "$provider_key_file" "$control_key_file"

artifact_target_is_safe() {
  local current
  [[ -d "$artifact_dir" && ! -L "$artifact_dir" ]] || return 1
  current="$(canonicalize_directory_target "$artifact_dir")" || return 1
  [[ "$current" == "$artifact_dir" &&
     "$current" != "$allowed_artifact_root_canonical" &&
     "$current" == "$allowed_artifact_root_canonical"/* ]]
}

diagnostics() {
  local staging_dir="$tmpdir/diagnostics"
  local staging_log="$staging_dir/containers.log"
  local final_log="$artifact_dir/containers.log"
  local publish_tmp="$artifact_dir/.containers.log.${nonce}.tmp"
  artifact_target_is_safe || return 0
  rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
  rm -rf "$staging_dir" || return 0
  install -d -m 0700 "$staging_dir" || return 0
  install -m 0600 /dev/null "$staging_log" || return 0
  printf 'diagnostics_version=1\n' > "$staging_log" || return 0
  for container in "$postgres" "$redis" "$minio" "$worker" "$app" "$fake_ai" "$processing_supervisor"; do
    if docker_bounded 15 ps -a --format '{{.Names}}' | grep -Fxq "$container"; then
      printf 'container=%s\n' "$container" >> "$staging_log" || true
      docker_bounded 15 inspect --format 'state_status={{.State.Status}}' "$container" >> "$staging_log" 2>&1 || true
      docker_bounded 15 inspect --format 'exit_code={{.State.ExitCode}}' "$container" >> "$staging_log" 2>&1 || true
      docker_bounded 15 inspect --format 'oom_killed={{.State.OOMKilled}}' "$container" >> "$staging_log" 2>&1 || true
      docker_bounded 20 logs --tail 200 "$container" >> "$staging_log" 2>&1 || true
    fi
  done
  if ! bash "$script_dir/sanitize-e2e-artifacts.sh" "$staging_dir"; then
    rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
    return 0
  fi
  if ! "$script_dir/publish-e2e-diagnostics.sh" "$staging_log" "$artifact_dir" "$nonce"; then
    rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
  fi
}

cleanup() {
  local exit_status=$?
  local sanitizer_status=0
  trap - EXIT INT TERM
  set +e
  cancel_bounded_command || true
  if (( exit_status != 0 )); then diagnostics || true; fi
  if artifact_target_is_safe; then
    bash "$script_dir/sanitize-e2e-artifacts.sh" "$artifact_dir" || sanitizer_status=$?
  else
    sanitizer_status=2
  fi
  if (( sanitizer_status != 0 )); then
    if artifact_target_is_safe; then find "$artifact_dir" -mindepth 1 -delete 2>/dev/null || true; fi
    if (( exit_status == 0 )); then exit_status=$sanitizer_status; fi
  fi
  docker_bounded 30 rm -f "${temporary_containers[@]}" >/dev/null 2>&1 || true
  docker_bounded 30 rm -f "${service_containers[@]}" >/dev/null 2>&1 || true
  docker_bounded 30 network rm "$network" >/dev/null 2>&1 || true
  docker_bounded 30 volume rm "$runner_volume" "$fixture_volume" "$secret_volume" "$data_volume" >/dev/null 2>&1 || true
  docker_bounded 60 image rm "$supervisor_image" "$fake_ai_image" "$worker_image" "$app_image" >/dev/null 2>&1 || true
  rm -rf "$tmpdir" || true
  exit "$exit_status"
}
trap cleanup EXIT INT TERM

wait_for() {
  local label="$1" container="$2"
  shift 2
  local attempt
  for attempt in $(seq 1 90); do
    if docker_bounded 15 "$@"; then return 0; fi
    if [[ "$(docker_bounded 15 inspect --format '{{.State.Running}}' "$container" 2>/dev/null || true)" != "true" ]]; then
      echo "$label exited before becoming ready" >&2
      return 1
    fi
    sleep 1
  done
  echo "timed out waiting for $label" >&2
  return 1
}

install -d -m 0700 "$artifact_dir"
if ! artifact_target_is_safe; then
  echo "E2E_ARTIFACT_DIR changed during validation" >&2
  exit 2
fi
docker_bounded 120 run --rm --name "$artifact_init" --network none --read-only --user 0:0 \
  --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --security-opt no-new-privileges --memory 16m --cpus .05 --tmpfs /tmp:rw,noexec,nosuid,size=4m \
  -v "$artifact_init_script:/init-e2e-artifacts.sh:ro" -v "$artifact_dir:/artifacts" \
  "$artifact_init_image" /bin/sh /init-e2e-artifacts.sh /artifacts

docker_bounded 900 build -t "$app_image" .
docker_bounded 1200 build -f Dockerfile.worker -t "$worker_image" .
docker_bounded 900 build -f Dockerfile.fake-ai -t "$fake_ai_image" .
docker_bounded 900 build -f Dockerfile.e2e-worker --build-arg "WORKER_IMAGE=$worker_image" -t "$supervisor_image" .
docker_bounded 60 network create --internal "$network" >/dev/null
docker_bounded 60 volume create "$data_volume" >/dev/null
docker_bounded 60 volume create "$fixture_volume" >/dev/null
docker_bounded 60 volume create "$runner_volume" >/dev/null
docker_bounded 60 volume create "$secret_volume" >/dev/null

docker_bounded 120 run --rm --name "$secret_init" --network none --read-only --user 0:0 --cap-drop ALL \
  --cap-add CHOWN --cap-add DAC_OVERRIDE --security-opt no-new-privileges --memory 16m --cpus .05 \
  --tmpfs /tmp:rw,noexec,nosuid,size=4m -v "$tmpdir:/source:ro" -v "$secret_volume:/secrets" \
  "$artifact_init_image" /bin/sh -c \
  'mkdir /secrets/app-master /secrets/fake-provider /secrets/runner-provider /secrets/supervisor-control /secrets/runner-control; cp /source/master-key /secrets/app-master/value; cp /source/provider-key /secrets/fake-provider/value; cp /source/provider-key /secrets/runner-provider/value; cp /source/control-key /secrets/supervisor-control/value; cp /source/control-key /secrets/runner-control/value; chmod 0500 /secrets/*; chmod 0400 /secrets/*/value; chown -R 10001:10001 /secrets/app-master; chown -R 10003:10003 /secrets/fake-provider; chown -R 1000:1000 /secrets/runner-provider /secrets/runner-control; chown -R 10002:10002 /secrets/supervisor-control'

docker_bounded 120 run --rm --name "$data_init" --network none --read-only --user 0:0 --cap-drop ALL \
  --cap-add CHOWN --security-opt no-new-privileges --memory 32m --cpus .05 --entrypoint /bin/sh -v "$data_volume:/data" "$minio_image" \
  -c 'chmod 0750 /data && chown 1000:0 /data'
docker_bounded 120 run --rm --name "$runner_init" --network none --read-only --user 0:0 --cap-drop ALL \
  --cap-add CHOWN --security-opt no-new-privileges --memory 32m --cpus .05 --entrypoint /bin/sh \
  -v "$runner_volume:/workspace" -v "$fixture_volume:/fixtures" "$playwright_image" \
  -c 'chmod 0700 /workspace /fixtures && chown 1000:1000 /workspace /fixtures'

docker_bounded 60 run -d --name "$postgres" --network "$network" --read-only --user 999:999 --cap-drop ALL \
  --security-opt no-new-privileges --memory 384m --cpus .1 \
  --tmpfs /var/run/postgresql:rw,noexec,nosuid,size=16m,uid=999,gid=999 \
  --tmpfs /var/lib/postgresql:rw,noexec,nosuid,size=320m,uid=999,gid=999 \
  -e POSTGRES_USER=happylearn -e POSTGRES_PASSWORD=happylearn_e2e -e POSTGRES_DB="$database" postgres:18.4 >/dev/null
docker_bounded 60 run -d --name "$redis" --network "$network" --read-only --user 999:999 --cap-drop ALL \
  --security-opt no-new-privileges --memory 96m --cpus .05 \
  --tmpfs /data:rw,noexec,nosuid,size=64m,uid=999,gid=999 redis:8.8 >/dev/null
docker_bounded 60 run -d --name "$minio" --network "$network" --network-alias minio --read-only --user 1000:0 \
  --cap-drop ALL --security-opt no-new-privileges --memory 384m --cpus .1 \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m,uid=1000,gid=0,mode=0700 -e HOME=/tmp \
  -e MINIO_ROOT_USER=happylearn_e2e -e "MINIO_ROOT_PASSWORD=$object_secret" \
  -v "$data_volume:/data" -v "$license_file:/minio.license:ro" "$minio_image" \
  minio server /data --console-address :9001 --license /minio.license >/dev/null
wait_for PostgreSQL "$postgres" exec "$postgres" pg_isready -U happylearn -d "$database"
wait_for Redis "$redis" exec "$redis" redis-cli ping
wait_for AIStor "$minio" exec "$minio" curl --fail --silent http://127.0.0.1:9000/minio/health/live

common_env=(
  -e HAPPYLEARN_ENV=development
  -e "HAPPYLEARN_DATABASE_URL=postgres://happylearn:happylearn_e2e@${postgres}:5432/${database}?sslmode=disable"
  -e "HAPPYLEARN_REDIS_URL=redis://${redis}:6379/0"
  -e "HAPPYLEARN_LOGIN_THROTTLE_SECRET=phase4-throttle-${nonce}-at-least-32-bytes"
  -e HAPPYLEARN_PUBLIC_ORIGIN=http://app:8080
  -e HAPPYLEARN_MINIO_ENDPOINT=minio:9000
  -e HAPPYLEARN_MINIO_ACCESS_KEY=happylearn_e2e
  -e "HAPPYLEARN_MINIO_SECRET_KEY=$object_secret"
  -e HAPPYLEARN_MINIO_ORIGINALS_BUCKET=phase4-originals
  -e HAPPYLEARN_MINIO_PREVIEWS_BUCKET=phase4-previews
)
app_env=(
  "${common_env[@]}"
  -e HAPPYLEARN_AI_MASTER_KEY_VERSION=1
  -e HAPPYLEARN_AI_BUSINESS_TIMEZONE=Asia/Shanghai
  -e HAPPYLEARN_AI_GLOBAL_CONCURRENCY=2
  -e HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY=1
  -e HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=true
)

docker_bounded 60 run -d --name "$app" --network "$network" --network-alias app --read-only --user 10001:10001 \
  --cap-drop ALL --security-opt no-new-privileges --memory 256m --cpus .2 --tmpfs /tmp:rw,noexec,nosuid,size=32m \
  "${app_env[@]}" \
  --mount "type=volume,src=$secret_volume,dst=/run/e2e-master-key,volume-subpath=app-master,readonly" \
  --entrypoint /bin/sh "$app_image" -c \
  'export HAPPYLEARN_AI_MASTER_KEY="$(cat /run/e2e-master-key/value)"; exec /app/happylearn' >/dev/null
wait_for application "$app" exec "$app" curl --fail --silent http://127.0.0.1:8080/api/v1/health/ready
docker_bounded 60 run -d --name "$fake_ai" --network "container:$app" --read-only --user 10003:10003 \
  --cap-drop ALL --security-opt no-new-privileges --memory 64m --cpus .05 --tmpfs /tmp:rw,noexec,nosuid,size=4m \
  --mount "type=volume,src=$secret_volume,dst=/run/e2e-provider-key,volume-subpath=fake-provider,readonly" \
  --entrypoint /bin/sh "$fake_ai_image" -c \
  'export E2E_AI_PROVIDER_KEY="$(cat /run/e2e-provider-key/value)"; exec /app/fake-ai-provider' >/dev/null
wait_for fake-provider "$fake_ai" exec "$fake_ai" curl --fail --silent http://127.0.0.1:8090/health/live

password_file="$tmpdir/admin-password"
printf '%s' "$admin_password" > "$password_file"
chmod 0600 "$password_file"
docker_bounded 120 run --rm --name "$admin_init" --network "$network" --read-only --user "$admin_user" --cap-drop ALL \
  --security-opt no-new-privileges --memory 128m --cpus .1 --tmpfs /tmp:rw,noexec,nosuid,size=16m "${common_env[@]}" \
  -v "$password_file:/run/admin-password:ro" --entrypoint /app/happylearn-admin "$app_image" \
  create-teacher --username admin --display-name 'Phase 4 Teacher' --password-file /run/admin-password

docker_bounded 60 run -d --name "$worker" --network "$network" --network-alias processing-supervisor --read-only --user 10002:10002 \
  --cap-drop ALL --security-opt no-new-privileges --memory 1792m --cpus 1 \
  --tmpfs /work:rw,noexec,nosuid,size=1408m,uid=10002,gid=10002,mode=0700 \
  --tmpfs /tmp:rw,noexec,nosuid,size=32m,uid=10002,gid=10002,mode=0700 \
  "${common_env[@]}" -e HAPPYLEARN_WORK_DIR=/work \
  --mount "type=volume,src=$secret_volume,dst=/run/e2e-control-token,volume-subpath=supervisor-control,readonly" \
  --entrypoint /bin/sh "$supervisor_image" -c \
  'export E2E_AI_PROCESSING_CONTROL_TOKEN="$(cat /run/e2e-control-token/value)"; exec /app/e2e-processing-supervisor' >/dev/null
wait_for worker "$worker" exec "$worker" curl --fail --silent http://127.0.0.1:8081/ready
wait_for processing-supervisor "$processing_supervisor" exec "$processing_supervisor" curl --fail --silent http://127.0.0.1:8092/health/live

docker_bounded 300 run --rm --name "$fixture_runner" --network none --read-only --user 1000:1000 \
  --cap-drop ALL --security-opt no-new-privileges --memory 512m --cpus .5 \
  --tmpfs /tmp:rw,noexec,nosuid,size=256m,uid=1000,gid=1000,mode=0700 -w /tmp --entrypoint /bin/bash \
  -v "$repo_root:/src:ro" -v "$fixture_volume:/fixtures" "$worker_image" \
  /src/scripts/generate-phase2-fixtures.sh /fixtures
docker_bounded 600 run --rm --name "$install_runner" --network bridge --read-only --user 1000:1000 \
  --cap-drop ALL --security-opt no-new-privileges --memory 1024m --cpus .5 --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -v "$repo_root:/source:ro" -v "$runner_volume:/workspace" --entrypoint /bin/bash \
  -e COREPACK_HOME=/workspace/.corepack -e XDG_DATA_HOME=/workspace/.xdg -e PNPM_HOME=/workspace/.pnpm \
  "$playwright_image" -lc '/source/scripts/copy-e2e-workspace.sh /source /workspace && cd /workspace && corepack pnpm install --frozen-lockfile --store-dir /workspace/.pnpm-store'

phase4_specs='tests/e2e/ai-questions.spec.ts tests/e2e/ai-admin.spec.ts tests/e2e/ai-privacy.spec.ts'
if [[ "$e2e_group" == phase4 ]]; then
  e2e_command=". /workspace/scripts/e2e-harness-lib.sh; test_status=0; E2E_OUTPUT_DIR=/artifacts/results/phase4 corepack pnpm exec playwright test $phase4_specs --project=chromium || test_status=\"\$(preserve_first_failure \"\$test_status\" \"\$?\")\"; E2E_OUTPUT_DIR=/artifacts/results/phase4-mobile corepack pnpm exec playwright test $phase4_specs --project=mobile --grep @phase4-mobile || test_status=\"\$(preserve_first_failure \"\$test_status\" \"\$?\")\"; exit \"\$test_status\""
else
  e2e_command=". /workspace/scripts/e2e-harness-lib.sh; test_status=0; E2E_OUTPUT_DIR=/artifacts/results/phase1 corepack pnpm exec playwright test tests/e2e/auth-students.spec.ts tests/e2e/teaching.spec.ts --project=chromium || test_status=\"\$(preserve_first_failure \"\$test_status\" \"\$?\")\"; E2E_OUTPUT_DIR=/artifacts/results/phase2 corepack pnpm exec playwright test tests/e2e/files.spec.ts tests/e2e/learning.spec.ts --project=chromium || test_status=\"\$(preserve_first_failure \"\$test_status\" \"\$?\")\"; E2E_OUTPUT_DIR=/artifacts/results/phase3 corepack pnpm exec playwright test tests/e2e/questions.spec.ts tests/e2e/notifications.spec.ts --project=chromium || test_status=\"\$(preserve_first_failure \"\$test_status\" \"\$?\")\"; E2E_OUTPUT_DIR=/artifacts/results/phase4 corepack pnpm exec playwright test $phase4_specs --project=chromium || test_status=\"\$(preserve_first_failure \"\$test_status\" \"\$?\")\"; E2E_OUTPUT_DIR=/artifacts/results/phase4-mobile corepack pnpm exec playwright test $phase4_specs --project=mobile --grep @phase4-mobile || test_status=\"\$(preserve_first_failure \"\$test_status\" \"\$?\")\"; exit \"\$test_status\""
fi

docker_bounded 1800 run --rm --name "$e2e_runner" --network "$network" --read-only --user 1000:1000 \
  --shm-size 384m --memory 1024m --cpus .5 --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,noexec,nosuid,size=128m,uid=1000,gid=1000,mode=0700 \
  -v "$runner_volume:/workspace" -v "$fixture_volume:/fixtures:ro" -v "$artifact_dir/results:/artifacts/results" -w /workspace \
  -e COREPACK_HOME=/workspace/.corepack -e XDG_DATA_HOME=/workspace/.xdg -e PNPM_HOME=/workspace/.pnpm \
  -e E2E_BASE_URL=http://app:8080 -e "E2E_ADMIN_PASSWORD=$admin_password" -e "E2E_STUDENT_PASSWORD=$student_password" \
  -e "E2E_STUDENT_NEW_PASSWORD=$student_new_password" -e E2E_FIXTURE_DIR=/fixtures \
  -e E2E_AI_PROVIDER_BASE_URL=http://localhost:8090/v1 \
  -e E2E_AI_PROVIDER_COUNTS_URL=http://app:8090/test/counts \
  -e E2E_AI_PROCESSING_CONTROL_URL=http://processing-supervisor:8092 \
  --mount "type=volume,src=$secret_volume,dst=/run/e2e-provider-key,volume-subpath=runner-provider,readonly" \
  --mount "type=volume,src=$secret_volume,dst=/run/e2e-control-token,volume-subpath=runner-control,readonly" \
  "$playwright_image" /bin/bash -lc \
  "export E2E_AI_PROVIDER_KEY=\"\$(cat /run/e2e-provider-key/value)\" E2E_AI_PROCESSING_CONTROL_TOKEN=\"\$(cat /run/e2e-control-token/value)\"; $e2e_command"
