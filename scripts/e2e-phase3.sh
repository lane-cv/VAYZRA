#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
contract_mode="${HAPPYLEARN_E2E_CONTRACT_MODE:-}"
case "$contract_mode" in
  '') ;;
  print) printf 'group=%s\n' "${HAPPYLEARN_E2E_GROUP:-all}"; exit 0 ;;
  interrupt) ;;
  *) echo "unknown contract mode" >&2; exit 2 ;;
esac

license_file="${HAPPYLEARN_AISTOR_LICENSE_FILE:-}"
if [[ "$contract_mode" != interrupt && ( -z "$license_file" || ! -r "$license_file" ) ]]; then
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
install_runner="${prefix}_install_runner"
e2e_runner="${prefix}_e2e_runner"
data_volume="${prefix}_data"
fixture_volume="${prefix}_fixtures"
runner_volume="${prefix}_runner"
app_image="happylearn:phase3-${nonce}"
worker_image="happylearn-worker:phase3-${nonce}"
playwright_image="mcr.microsoft.com/playwright:v1.57.0-noble"
minio_image="quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120"
database="happylearn_phase3_${nonce//-/_}"
admin_password="Phase3 Admin ${nonce}!"
student_password="Phase3 Student ${nonce}!"
student_new_password="Phase3 Changed ${nonce}!"
object_secret="phase3-object-${nonce}-secret"
artifact_dir="${E2E_ARTIFACT_DIR:-$PWD/test-results/phase3}"
e2e_group="${HAPPYLEARN_E2E_GROUP:-all}"
case "$e2e_group" in
  all|phase3) ;;
  *) echo "HAPPYLEARN_E2E_GROUP must be all or phase3" >&2; exit 2 ;;
esac
tmpdir="$(mktemp -d)"
temporary_containers=("$data_init" "$runner_init" "$admin_init" "$fixture_runner" "$install_runner" "$e2e_runner")
service_containers=("$app" "$worker" "$minio" "$redis" "$postgres")

run_bounded() {
  local seconds="$1"; shift
  "$@" & local command_pid=$!
  ( sleep "$seconds"; kill -TERM "$command_pid" 2>/dev/null || exit 0; sleep 10; kill -KILL "$command_pid" 2>/dev/null || true ) & local timer_pid=$!
  local status=0
  wait "$command_pid" || status=$?
  kill "$timer_pid" 2>/dev/null || true
  wait "$timer_pid" 2>/dev/null || true
  return "$status"
}

diagnostics() {
  install -d -m 0700 "$artifact_dir"
  : > "$artifact_dir/containers.log"
  for container in "$postgres" "$redis" "$minio" "$worker" "$app"; do
    if docker ps -a --format '{{.Names}}' | grep -Fxq "$container"; then
      docker inspect --format '{{json .State}}' "$container" >> "$artifact_dir/containers.log" 2>&1 || true
      docker logs --tail 200 "$container" 2>&1 | sed -E \
        -e 's#postgres://[^[:space:]]+#postgres://REDACTED#g' \
        -e 's#redis://[^[:space:]]+#redis://REDACTED#g' \
        -e 's#(password|secret|token|authorization|cookie)=([^[:space:]]+)#\1=REDACTED#gI' \
        >> "$artifact_dir/containers.log" || true
    fi
  done
  bash "$script_dir/sanitize-e2e-artifacts.sh" "$artifact_dir"
}

cleanup() {
  local exit_status=$?
  trap - EXIT INT TERM
  if (( exit_status != 0 )) && [[ "$contract_mode" != interrupt ]]; then diagnostics; fi
  docker rm -f "${temporary_containers[@]}" >/dev/null 2>&1 || true
  docker rm -f "${service_containers[@]}" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  docker volume rm "$runner_volume" "$fixture_volume" "$data_volume" >/dev/null 2>&1 || true
  docker image rm "$app_image" "$worker_image" >/dev/null 2>&1 || true
  rm -rf "$tmpdir"
  exit "$exit_status"
}
trap cleanup EXIT INT TERM

if [[ "$contract_mode" == interrupt ]]; then
  docker network create --internal "$network" >/dev/null
  docker volume create "$data_volume" >/dev/null
  docker volume create "$fixture_volume" >/dev/null
  docker volume create "$runner_volume" >/dev/null
  run_bounded 120 docker run --name "$data_init" contract-hold
  exit 0
fi

wait_for() {
  local label="$1" container="$2"
  shift 2
  local attempt
  for attempt in $(seq 1 90); do
    if "$@"; then return 0; fi
    if [[ "$(docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null || true)" != "true" ]]; then
      echo "$label exited before becoming ready" >&2
      return 1
    fi
    sleep 1
  done
  echo "timed out waiting for $label" >&2
  return 1
}

run_bounded 900 docker build -t "$app_image" .
run_bounded 1200 docker build -f Dockerfile.worker -t "$worker_image" .
docker network create --internal "$network" >/dev/null
docker volume create "$data_volume" >/dev/null
docker volume create "$fixture_volume" >/dev/null
docker volume create "$runner_volume" >/dev/null

run_bounded 120 docker run --rm --name "$data_init" --network none --user 0:0 --entrypoint /bin/sh -v "$data_volume:/data" "$minio_image" -c 'chown 1000:0 /data && chmod 0750 /data'
run_bounded 120 docker run --rm --name "$runner_init" --network none --user 0:0 --entrypoint /bin/sh -v "$runner_volume:/workspace" "$playwright_image" -c 'chown 1000:1000 /workspace && chmod 0700 /workspace'
run_bounded 60 docker run -d --name "$postgres" --network "$network" --memory 384m --cpus .25 \
  -e POSTGRES_USER=happylearn -e POSTGRES_PASSWORD=happylearn_e2e -e POSTGRES_DB="$database" postgres:18.4 >/dev/null
run_bounded 60 docker run -d --name "$redis" --network "$network" --memory 96m --cpus .1 redis:8.8 >/dev/null
run_bounded 60 docker run -d --name "$minio" --network "$network" --network-alias minio --user 1000:0 --memory 384m --cpus .2 \
  -e MINIO_ROOT_USER=happylearn_e2e -e "MINIO_ROOT_PASSWORD=$object_secret" \
  -v "$data_volume:/data" -v "$license_file:/minio.license:ro" "$minio_image" \
  minio server /data --console-address :9001 --license /minio.license >/dev/null
wait_for PostgreSQL "$postgres" docker exec "$postgres" pg_isready -U happylearn -d "$database"
wait_for Redis "$redis" docker exec "$redis" redis-cli ping
wait_for AIStor "$minio" docker exec "$minio" curl --fail --silent http://127.0.0.1:9000/minio/health/live

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

run_bounded 60 docker run -d --name "$app" --network "$network" --network-alias app --read-only --user 10001:10001 \
  --cap-drop ALL --security-opt no-new-privileges --memory 256m --cpus .5 --tmpfs /tmp:rw,noexec,nosuid,size=32m \
  "${common_env[@]}" "$app_image" >/dev/null
wait_for application "$app" docker exec "$app" curl --fail --silent http://127.0.0.1:8080/api/v1/health/ready

password_file="$tmpdir/admin-password"
umask 077
printf '%s' "$admin_password" > "$password_file"
chmod 0600 "$password_file"
run_bounded 120 docker run --rm --name "$admin_init" --network "$network" --read-only --user 0:0 --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m "${common_env[@]}" -v "$password_file:/run/admin-password:ro" \
  --entrypoint /app/happylearn-admin "$app_image" create-teacher --username admin --display-name 'Phase 3 Teacher' --password-file /run/admin-password

run_bounded 60 docker run -d --name "$worker" --network "$network" --read-only --user 10002:10002 --cap-drop ALL \
  --security-opt no-new-privileges --memory 1280m --cpus .7 \
  --tmpfs /work:rw,noexec,nosuid,size=1024m,uid=10002,gid=10002,mode=0700 --tmpfs /tmp:rw,noexec,nosuid,size=32m,uid=10002,gid=10002,mode=0700 \
  "${common_env[@]}" -e HAPPYLEARN_WORK_DIR=/work "$worker_image" >/dev/null
wait_for worker "$worker" docker exec "$worker" curl --fail --silent http://127.0.0.1:8081/ready

run_bounded 300 docker run --rm --name "$fixture_runner" --network none --user 0:0 --memory 768m --cpus .5 --entrypoint /bin/bash -v "$PWD:/src:ro" -v "$fixture_volume:/fixtures" "$worker_image" \
  /src/scripts/generate-phase2-fixtures.sh /fixtures
run_bounded 600 docker run --rm --name "$install_runner" --read-only --user 1000:1000 --memory 1280m --cpus .6 --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -v "$PWD:/source:ro" -v "$runner_volume:/workspace" --entrypoint /bin/bash \
  -e COREPACK_HOME=/workspace/.corepack -e XDG_DATA_HOME=/workspace/.xdg -e PNPM_HOME=/workspace/.pnpm \
  "$playwright_image" -lc '/source/scripts/copy-e2e-workspace.sh /source /workspace && cd /workspace && corepack pnpm install --frozen-lockfile --store-dir /workspace/.pnpm-store'

install -d -m 0700 "$artifact_dir"
if [[ "$e2e_group" == phase3 ]]; then
  e2e_command='E2E_OUTPUT_DIR=/artifacts/results/phase3 corepack pnpm exec playwright test tests/e2e/questions.spec.ts tests/e2e/notifications.spec.ts'
else
  e2e_command='test_status=0; E2E_OUTPUT_DIR=/artifacts/results/phase1 corepack pnpm exec playwright test tests/e2e/auth-students.spec.ts tests/e2e/teaching.spec.ts || test_status=$?; E2E_OUTPUT_DIR=/artifacts/results/phase2 corepack pnpm exec playwright test tests/e2e/files.spec.ts tests/e2e/learning.spec.ts || test_status=$?; E2E_OUTPUT_DIR=/artifacts/results/phase3 corepack pnpm exec playwright test tests/e2e/questions.spec.ts tests/e2e/notifications.spec.ts || test_status=$?; exit "$test_status"'
fi
run_bounded 1200 docker run --rm --name "$e2e_runner" --network "$network" --read-only --user 1000:1000 --shm-size 512m --memory 2048m --cpus 1 \
  --cap-drop ALL --security-opt no-new-privileges --tmpfs /tmp:rw,noexec,nosuid,size=256m \
  -v "$runner_volume:/workspace" -v "$fixture_volume:/fixtures:ro" -v "$artifact_dir:/artifacts" -w /workspace \
  -e COREPACK_HOME=/workspace/.corepack -e XDG_DATA_HOME=/workspace/.xdg -e PNPM_HOME=/workspace/.pnpm -e E2E_BASE_URL=http://app:8080 \
  -e "E2E_ADMIN_PASSWORD=$admin_password" -e "E2E_STUDENT_PASSWORD=$student_password" -e "E2E_STUDENT_NEW_PASSWORD=$student_new_password" \
  -e E2E_FIXTURE_DIR=/fixtures "$playwright_image" /bin/bash -lc \
  "$e2e_command"
