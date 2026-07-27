#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
source "$script_dir/e2e-harness-lib.sh"

license_file="${HAPPYLEARN_AISTOR_LICENSE_FILE:-}"
if [[ -z "$license_file" || ! -r "$license_file" ]]; then
  echo "HAPPYLEARN_AISTOR_LICENSE_FILE must name a readable AIStor Free license file" >&2
  exit 2
fi
license_file="$(cd "$(dirname "$license_file")" && pwd)/$(basename "$license_file")"

nonce="$(date +%s)-$RANDOM"
prefix="happylearn_phase3_${nonce}"
network="${prefix}_net"
postgres="${prefix}_postgres"
redis="${prefix}_redis"
minio="${prefix}_minio"
worker="${prefix}_worker"
app="${prefix}_app"
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
app_image="happylearn:phase3-${nonce}"
worker_image="happylearn-worker:phase3-${nonce}"
playwright_image="mcr.microsoft.com/playwright:v1.57.0-noble"
artifact_init_image="alpine:3.22.1@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1"
minio_image="quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120"
database="happylearn_phase3_${nonce//-/_}"
admin_password="Phase3 Admin ${nonce}!"
student_password="Phase3 Student ${nonce}!"
student_new_password="Phase3 Changed ${nonce}!"
object_secret="phase3-object-${nonce}-secret"
artifact_dir="${E2E_ARTIFACT_DIR:-$PWD/test-results/phase3}"
artifact_init_script="$script_dir/init-e2e-artifacts.sh"
e2e_group="${HAPPYLEARN_E2E_GROUP:-all}"
case "$e2e_group" in
  all|phase3) ;;
  *) echo "HAPPYLEARN_E2E_GROUP must be all or phase3" >&2; exit 2 ;;
esac
tmpdir="$(mktemp -d)"
admin_user="$(id -u):$(id -g)"
temporary_containers=("$data_init" "$runner_init" "$admin_init" "$fixture_runner" "$artifact_init" "$install_runner" "$e2e_runner")
service_containers=("$app" "$worker" "$minio" "$redis" "$postgres")

diagnostics() {
  local staging_dir="$tmpdir/diagnostics"
  local staging_log="$staging_dir/containers.log"
  local final_log="$artifact_dir/containers.log"
  local publish_tmp="$artifact_dir/.containers.log.${nonce}.tmp"
  rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
  rm -rf "$staging_dir" || return 0
  install -d -m 0700 "$staging_dir" || return 0
  install -m 0600 /dev/null "$staging_log" || return 0
  printf 'diagnostics_version=1\n' > "$staging_log" || return 0
  for container in "$postgres" "$redis" "$minio" "$worker" "$app"; do
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
  trap - EXIT INT TERM
  set +e
  cancel_bounded_command || true
  if (( exit_status != 0 )); then diagnostics || true; fi
  docker_bounded 30 rm -f "${temporary_containers[@]}" >/dev/null 2>&1 || true
  docker_bounded 30 rm -f "${service_containers[@]}" >/dev/null 2>&1 || true
  docker_bounded 30 network rm "$network" >/dev/null 2>&1 || true
  docker_bounded 30 volume rm "$runner_volume" "$fixture_volume" "$data_volume" >/dev/null 2>&1 || true
  docker_bounded 60 image rm "$app_image" "$worker_image" >/dev/null 2>&1 || true
  rm -rf "$tmpdir" || true
  exit "$exit_status"
}
trap cleanup EXIT INT TERM

install -d -m 0700 "$artifact_dir" || true
docker_bounded 120 run --rm --name "$artifact_init" --network none --read-only --user 0:0 \
  --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --security-opt no-new-privileges --tmpfs /tmp:rw,noexec,nosuid,size=4m \
  -v "$artifact_init_script:/init-e2e-artifacts.sh:ro" -v "$artifact_dir:/artifacts" \
  "$artifact_init_image" /bin/sh /init-e2e-artifacts.sh /artifacts

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

docker_bounded 900 build -t "$app_image" .
docker_bounded 1200 build -f Dockerfile.worker -t "$worker_image" .
docker_bounded 60 network create --internal "$network" >/dev/null
docker_bounded 60 volume create "$data_volume" >/dev/null
docker_bounded 60 volume create "$fixture_volume" >/dev/null
docker_bounded 60 volume create "$runner_volume" >/dev/null

docker_bounded 120 run --rm --name "$data_init" --network none --user 0:0 --entrypoint /bin/sh -v "$data_volume:/data" "$minio_image" -c 'chown 1000:0 /data && chmod 0750 /data'
docker_bounded 120 run --rm --name "$runner_init" --network none --user 0:0 --entrypoint /bin/sh -v "$runner_volume:/workspace" "$playwright_image" -c 'chown 1000:1000 /workspace && chmod 0700 /workspace'
docker_bounded 60 run -d --name "$postgres" --network "$network" --memory 384m --cpus .25 \
  -e POSTGRES_USER=happylearn -e POSTGRES_PASSWORD=happylearn_e2e -e POSTGRES_DB="$database" postgres:18.4 >/dev/null
docker_bounded 60 run -d --name "$redis" --network "$network" --memory 96m --cpus .1 redis:8.8 >/dev/null
docker_bounded 60 run -d --name "$minio" --network "$network" --network-alias minio --user 1000:0 --memory 384m --cpus .2 \
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
  -e "HAPPYLEARN_LOGIN_THROTTLE_SECRET=phase3-throttle-${nonce}-at-least-32-bytes"
  -e HAPPYLEARN_PUBLIC_ORIGIN=http://app:8080
  -e HAPPYLEARN_MINIO_ENDPOINT=minio:9000
  -e HAPPYLEARN_MINIO_ACCESS_KEY=happylearn_e2e
  -e "HAPPYLEARN_MINIO_SECRET_KEY=$object_secret"
  -e HAPPYLEARN_MINIO_ORIGINALS_BUCKET=phase3-originals
  -e HAPPYLEARN_MINIO_PREVIEWS_BUCKET=phase3-previews
)

docker_bounded 60 run -d --name "$app" --network "$network" --network-alias app --read-only --user 10001:10001 \
  --cap-drop ALL --security-opt no-new-privileges --memory 256m --cpus .5 --tmpfs /tmp:rw,noexec,nosuid,size=32m \
  "${common_env[@]}" "$app_image" >/dev/null
wait_for application "$app" exec "$app" curl --fail --silent http://127.0.0.1:8080/api/v1/health/ready

password_file="$tmpdir/admin-password"
umask 077
printf '%s' "$admin_password" > "$password_file"
chmod 0600 "$password_file"
docker_bounded 120 run --rm --name "$admin_init" --network "$network" --read-only --user "$admin_user" --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m "${common_env[@]}" -v "$password_file:/run/admin-password:ro" \
  --entrypoint /app/happylearn-admin "$app_image" create-teacher --username admin --display-name 'Phase 3 Teacher' --password-file /run/admin-password

docker_bounded 60 run -d --name "$worker" --network "$network" --read-only --user 10002:10002 --cap-drop ALL \
  --security-opt no-new-privileges --memory 1280m --cpus .7 \
  --tmpfs /work:rw,noexec,nosuid,size=1024m,uid=10002,gid=10002,mode=0700 --tmpfs /tmp:rw,noexec,nosuid,size=32m,uid=10002,gid=10002,mode=0700 \
  "${common_env[@]}" -e HAPPYLEARN_WORK_DIR=/work "$worker_image" >/dev/null
wait_for worker "$worker" exec "$worker" curl --fail --silent http://127.0.0.1:8081/ready

docker_bounded 300 run --rm --name "$fixture_runner" --network none --user 0:0 --memory 768m --cpus .5 --entrypoint /bin/bash -v "$PWD:/src:ro" -v "$fixture_volume:/fixtures" "$worker_image" \
  /src/scripts/generate-phase2-fixtures.sh /fixtures
docker_bounded 600 run --rm --name "$install_runner" --read-only --user 1000:1000 --memory 1280m --cpus .6 --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -v "$PWD:/source:ro" -v "$runner_volume:/workspace" --entrypoint /bin/bash \
  -e COREPACK_HOME=/workspace/.corepack -e XDG_DATA_HOME=/workspace/.xdg -e PNPM_HOME=/workspace/.pnpm \
  "$playwright_image" -lc '/source/scripts/copy-e2e-workspace.sh /source /workspace && cd /workspace && corepack pnpm install --frozen-lockfile --store-dir /workspace/.pnpm-store'

if [[ "$e2e_group" == phase3 ]]; then
  e2e_command='E2E_OUTPUT_DIR=/artifacts/results/phase3 corepack pnpm exec playwright test tests/e2e/questions.spec.ts tests/e2e/notifications.spec.ts'
else
  e2e_command='test_status=0; E2E_OUTPUT_DIR=/artifacts/results/phase1 corepack pnpm exec playwright test tests/e2e/auth-students.spec.ts tests/e2e/teaching.spec.ts || test_status=$?; E2E_OUTPUT_DIR=/artifacts/results/phase2 corepack pnpm exec playwright test tests/e2e/files.spec.ts tests/e2e/learning.spec.ts || test_status=$?; E2E_OUTPUT_DIR=/artifacts/results/phase3 corepack pnpm exec playwright test tests/e2e/questions.spec.ts tests/e2e/notifications.spec.ts || test_status=$?; exit "$test_status"'
fi
docker_bounded 1200 run --rm --name "$e2e_runner" --network "$network" --read-only --user 1000:1000 --shm-size 512m --memory 2048m --cpus 1 \
  --cap-drop ALL --security-opt no-new-privileges --tmpfs /tmp:rw,noexec,nosuid,size=256m \
  -v "$runner_volume:/workspace" -v "$fixture_volume:/fixtures:ro" -v "$artifact_dir/results:/artifacts/results" -w /workspace \
  -e COREPACK_HOME=/workspace/.corepack -e XDG_DATA_HOME=/workspace/.xdg -e PNPM_HOME=/workspace/.pnpm -e E2E_BASE_URL=http://app:8080 \
  -e "E2E_ADMIN_PASSWORD=$admin_password" -e "E2E_STUDENT_PASSWORD=$student_password" -e "E2E_STUDENT_NEW_PASSWORD=$student_new_password" \
  -e E2E_FIXTURE_DIR=/fixtures "$playwright_image" /bin/bash -lc \
  "$e2e_command"
