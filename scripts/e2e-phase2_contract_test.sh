#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
script="$repo_root/scripts/e2e-phase2.sh"
playwright_config="$repo_root/playwright.config.ts"
fixture_script="$repo_root/scripts/generate-phase2-fixtures.sh"
artifact_init_script="$repo_root/scripts/init-e2e-artifacts.sh"
workflow="$repo_root/.github/workflows/verify.yml"

test "$(grep -o 'corepack pnpm exec playwright test' "$script" | wc -l | tr -d ' ')" -eq 2
test "$(grep -o 'COREPACK_HOME=/workspace/.corepack' "$script" | wc -l | tr -d ' ')" -eq 2
grep -Fq 'workers: 1' "$playwright_config"
grep -Fq 'timeout: 120_000' "$playwright_config"
grep -Fq 'resume.pdf' "$fixture_script"
! grep -Fq 'resume.bin' "$fixture_script"
grep -Fq -- '-c:v png -f image2' "$fixture_script"
grep -Fq "test -s \"\$destination/question.png\"" "$fixture_script"
grep -Fq "89504e470d0a1a0a" "$fixture_script"
grep -Fq 'sanitize-e2e-artifacts.sh' "$script"
grep -Fq 'mc alias set local http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"' "$script"
grep -Fq 'mc ls local >/dev/null 2>&1' "$script"
! grep -Fq 'http://127.0.0.1:9000/minio/health/live' "$script"
test -x "$artifact_init_script"
grep -Fq 'artifact_init="${prefix}_artifact_init"' "$script"
grep -Fq '"$artifact_init"' "$script"
grep -Fq 'docker_bounded 120 run --rm --name "$artifact_init" --network none --read-only --user 0:0' "$script"
grep -Fq -- '--cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --security-opt no-new-privileges' "$script"
grep -Fq '"$fixture_runner" --network none --read-only --user 1000:1000' "$script"
grep -Fq -- '--tmpfs /tmp:rw,noexec,nosuid,size=256m,uid=1000,gid=1000,mode=0700' "$script"
grep -Fq -- '-w /tmp --entrypoint /bin/bash' "$script"
grep -Fq -- '-v "$fixture_volume:/fixtures"' "$script"
grep -Fq -- '-e E2E_FIXTURE_DIR=/fixtures -e E2E_OUTPUT_DIR=/artifacts/results/phase2' "$script"
grep -Fq 'path: test-results/phase2/containers.log' "$workflow"

source "$repo_root/scripts/e2e-harness-lib.sh"
ownership_image='alpine:3.22.1@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1'
ownership_nonce="$(date +%s)-$RANDOM"
ownership_resources=()
cleanup_ownership_contract() {
  local resource
  set +e
  for resource in "${ownership_resources[@]}"; do docker_bounded 30 rm -f "$resource" >/dev/null 2>&1 || true; done
  for resource in "phase2" "phase3"; do docker_bounded 30 volume rm "happylearn_${resource}_artifact_contract_${ownership_nonce}" >/dev/null 2>&1 || true; done
}
trap cleanup_ownership_contract EXIT INT TERM

run_ownership_case() {
  local phase="$1"
  local volume="happylearn_${phase}_artifact_contract_${ownership_nonce}"
  local seed="${volume}_seed" init="${volume}_init" probe="${volume}_probe" verify="${volume}_verify"
  ownership_resources+=("$seed" "$init" "$probe" "$verify")
  docker_bounded 60 volume create "$volume" >/dev/null
  docker_bounded 60 run --rm --name "$seed" --network none --read-only --user 0:0 --cap-drop ALL --cap-add CHOWN --security-opt no-new-privileges \
    --tmpfs /tmp:rw,noexec,nosuid,size=4m -v "$volume:/artifacts" "$ownership_image" /bin/sh -c \
    'mkdir -p /artifacts/results && : > /artifacts/containers.log && chmod 0700 /artifacts /artifacts/results && chown 1001:1001 /artifacts/results /artifacts/containers.log /artifacts'
  docker_bounded 60 run --rm --name "$init" --network none --read-only --user 0:0 --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --security-opt no-new-privileges \
    --tmpfs /tmp:rw,noexec,nosuid,size=4m -v "$artifact_init_script:/init-e2e-artifacts.sh:ro" -v "$volume:/artifacts" \
    "$ownership_image" /bin/sh /init-e2e-artifacts.sh /artifacts
  docker_bounded 60 run --rm --name "$probe" --network none --read-only --user 1000:1000 --cap-drop ALL --security-opt no-new-privileges \
    --tmpfs /tmp:rw,noexec,nosuid,size=4m,uid=1000,gid=1000,mode=0700 \
    --mount "type=volume,src=$volume,dst=/artifacts/results,volume-subpath=results" \
    "$ownership_image" /bin/sh -c 'test ! -e /artifacts/containers.log && printf "%s\n" uid1000 > /artifacts/results/probe'
  docker_bounded 60 run --rm --name "$verify" --network none --read-only --user 1000:1000 --cap-drop ALL --security-opt no-new-privileges \
    --mount "type=volume,src=$volume,dst=/artifacts/results,volume-subpath=results,readonly" \
    "$ownership_image" /bin/sh -c 'test "$(cat /artifacts/results/probe)" = uid1000'
  docker_bounded 30 volume rm "$volume" >/dev/null
}

run_ownership_case phase2
run_ownership_case phase3
trap - EXIT INT TERM
cleanup_ownership_contract

echo 'phase 2 E2E harness contract: PASS'
