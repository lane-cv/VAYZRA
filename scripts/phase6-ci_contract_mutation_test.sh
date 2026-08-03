#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
source_workflow=$root/.github/workflows/phase6.yml
contract=$root/scripts/phase6-ci_contract_test.sh
tmp=$(mktemp -d); trap 'find "$tmp" -mindepth 1 -delete; rmdir "$tmp"' EXIT
fail() { printf 'phase6 CI mutation: FAIL: %s\n' "$1" >&2; exit 1; }

reject() {
  local name=$1 expression=$2 expected=$3
  local target=$tmp/$name.yml output=$tmp/$name.out
  awk "$expression" "$source_workflow" >"$target"
  if HAPPYLEARN_PHASE6_CI_WORKFLOW="$target" bash "$contract" >"$output" 2>&1; then fail "accepted mutation $name"; fi
  grep -Fq "$expected" "$output" || fail "mutation $name failed for wrong reason"
}

reject continue_on_error '1; /^  phase6-contracts:$/ {print "    continue-on-error: true"}' 'continue on error'
reject production_silent_skip '{gsub(/inputs.run_production/, "false"); print}' 'required invariant missing: inputs.run_production'
reject unsanitized_upload '{gsub(/test-results\/phase6\/\*\/containers.log/, "test-results/phase6"); print}' 'required invariant missing: test-results/phase6/*/containers.log'
reject missing_timeout '{gsub(/timeout --foreground --kill-after=30s /, ""); print}' 'required invariant missing: timeout'
reject floating_action '{gsub(/actions\/checkout@v6.0.2/, "actions/checkout@main"); print}' 'action is floating'
reject short_resource '{gsub(/exactly 30 minutes/, "briefly"); print}' 'required invariant missing: exactly 30 minutes'
reject incomplete_production '{gsub(/HAPPYLEARN_E2E_GROUP=all/, "HAPPYLEARN_E2E_GROUP=release"); print}' 'required invariant missing: HAPPYLEARN_E2E_GROUP=all'

printf '%s\n' 'phase6 CI mutation: PASS'
