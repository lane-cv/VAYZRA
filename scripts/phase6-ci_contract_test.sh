#!/usr/bin/env bash
# shellcheck disable=SC2016
set -Eeuo pipefail
IFS=$'\n\t'

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
workflow=${HAPPYLEARN_PHASE6_CI_WORKFLOW:-$root/.github/workflows/phase6.yml}
fail() { printf 'phase6 CI contract: FAIL: %s\n' "$1" >&2; exit 1; }
[[ -f $workflow && ! -L $workflow ]] || fail 'workflow missing or unsafe'

for job in phase6-contracts phase6-license-gate phase6-production phase6-resources; do
  [[ $(grep -c "^  $job:$" "$workflow") == 1 ]] || fail "job $job must occur exactly once"
done
for literal in \
  'make phase6-contracts' \
  'make phase6-caddy-runtime-contract' \
  'inputs.run_production' 'inputs.run_resources' 'test -n "$AISTOR_LICENSE"' \
  'HAPPYLEARN_E2E_GROUP=all' '19800s' 'HAPPYLEARN_E2E_GROUP=security' '7200s' \
  'HAPPYLEARN_E2E_GROUP=resources' 'exactly 30 minutes' '3300s' \
  'timeout --foreground --kill-after=30s' 'test-results/phase6/*/containers.log' \
  'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02' \
  'aquasecurity/setup-trivy@3fb12ec12f41e471780db15c232d5dd185dcb514' \
  'persist-credentials: false' 'retention-days: 7'; do
  grep -Fq -- "$literal" "$workflow" || fail "required invariant missing: $literal"
done

grep -Eq '^  phase6-production:$' "$workflow" || fail 'production job missing'
grep -A5 '^  phase6-production:$' "$workflow" | grep -Fq "if: github.event_name == 'workflow_dispatch' && inputs.run_production" || fail 'production job is not explicitly protected'
grep -A6 '^  phase6-resources:$' "$workflow" | grep -Fq "github.event_name == 'schedule'" || fail 'resource schedule guard missing'
grep -A16 '^  phase6-resources:$' "$workflow" | grep -Fq 'go-version: 1.26.5' || fail 'resource host-sample builder is not pinned to the required Go version'

if grep -Eq '(^|[[:space:]])continue-on-error:' "$workflow"; then fail 'required work may not continue on error'; fi
while IFS= read -r action; do
  [[ ! $action =~ @(main|master|latest)$ ]] || fail 'action is floating'
  [[ $action =~ @[0-9a-f]{40}$ ]] || fail "action is not pinned to a full commit SHA: $action"
done < <(sed -nE 's/^[[:space:]]*(-[[:space:]]*)?uses:[[:space:]]*([^[:space:]#]+).*/\2/p' "$workflow")
if grep -Eq 'image: [^@[:space:]]+:(latest|edge)([[:space:]]|$)' "$workflow"; then fail 'image is floating'; fi
while IFS=: read -r line _; do
  next=$(sed -n "$((line + 1)),$((line + 8))p" "$workflow")
  grep -Fq 'path: test-results/phase6/*/containers.log' <<<"$next" || fail 'artifact upload path is not canonical and sanitized'
done < <(grep -n 'uses: actions/upload-artifact@' "$workflow")

grep -Fq 'trap '\''cleanup' "$root/scripts/e2e-phase6.sh" || fail 'harness cleanup trap missing'
grep -Fq 'timeout --foreground' "$root/scripts/e2e-phase6.sh" || fail 'harness timeout missing'
grep -Eq 'all\).*run_release_failure_matrix' "$root/scripts/e2e-phase6.sh" || fail 'complete group omits the real failure matrix'
printf '%s\n' 'phase6 CI contract: PASS'
