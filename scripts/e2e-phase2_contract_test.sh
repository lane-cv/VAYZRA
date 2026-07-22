#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
script="$repo_root/scripts/e2e-phase2.sh"
playwright_config="$repo_root/playwright.config.ts"
fixture_script="$repo_root/scripts/generate-phase2-fixtures.sh"
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
grep -Fq '"$fixture_runner" --network none --read-only --user 1000:1000' "$script"
grep -Fq -- '--tmpfs /tmp:rw,noexec,nosuid,size=256m,uid=1000,gid=1000,mode=0700' "$script"
grep -Fq -- '-w /tmp --entrypoint /bin/bash' "$script"
grep -Fq -- '-v "$fixture_volume:/fixtures"' "$script"
grep -Fq -- '-e E2E_FIXTURE_DIR=/fixtures -e E2E_OUTPUT_DIR=/artifacts/results' "$script"
grep -Fq 'path: test-results/phase2/containers.log' "$workflow"

echo 'phase 2 E2E harness contract: PASS'
