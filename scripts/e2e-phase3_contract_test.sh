#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
script="$repo_root/scripts/e2e-phase3.sh"
workflow="$repo_root/.github/workflows/verify.yml"
config="$repo_root/playwright.config.ts"
copy_script="$repo_root/scripts/copy-e2e-workspace.sh"

test -f "$script"
grep -Fq 'prefix="happylearn_phase3_' "$script"
grep -Fq 'docker network create --internal "$network"' "$script"
grep -Fq -- '--read-only --user 10001:10001' "$script"
grep -Fq -- '--read-only --user 10002:10002' "$script"
grep -Fq -- '--cap-drop ALL --security-opt no-new-privileges' "$script"
grep -Fq 'workers: 1' "$config"
grep -Fq 'timeout: 120_000' "$config"
grep -Fq 'questions.spec.ts tests/e2e/notifications.spec.ts' "$script"
grep -Fq 'auth-students.spec.ts tests/e2e/teaching.spec.ts' "$script"
grep -Fq 'files.spec.ts tests/e2e/learning.spec.ts' "$script"
grep -Fq 'E2E_ARTIFACT_DIR' "$script"
grep -Fq 'HAPPYLEARN_E2E_GROUP' "$script"
grep -Fq 'all|phase3' "$script"
grep -Fq 'sanitize-e2e-artifacts.sh' "$script"
grep -Fq -- '--read-only --user 1000:1000' "$script"
grep -Fq 'phase3-e2e:' "$workflow"
grep -Fq 'pnpm lint' "$workflow"
grep -Fq 'pnpm e2e-contracts' "$workflow"
grep -Fq 'path: test-results/phase3/containers.log' "$workflow"
grep -Fq -- "--exclude='./test-results'" "$copy_script"

grep -Fq 'license_file="${HAPPYLEARN_AISTOR_LICENSE_FILE:-}"' "$script"
! grep -Eq '(HAPPYLEARN_AISTOR_LICENSE|MINIO\.license)[[:space:]]*=' "$script"
! grep -Eq 'docker (rm|volume rm|network rm).*(-f )?\$prefix|docker system prune|docker volume prune|docker network prune' "$script"
! grep -Eq '(/var/run/docker.sock|--network host|--privileged)' "$script"

echo 'phase 3 E2E harness contract: PASS'
