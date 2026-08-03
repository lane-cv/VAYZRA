#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
target=$root/scripts/prod-rollback.sh
[[ -f $target ]] || { echo 'prod rollback contract: FAIL: script missing'; exit 1; }

states=(rollback_diagnostics rollback_compatibility rollback_stopped rollback_started rollback_ready rollback_smoke_passed environment_restored rolled_back normal traffic_open)
last=0
for state in "${states[@]}"; do
  line=$(grep -n -m1 -E "(^|[ (])${state}([ )]|$)" "$target" | cut -d: -f1)
  [[ -n $line && $line -ge $last ]] || { echo "prod rollback contract: FAIL: state $state"; exit 1; }
  last=$line
done

for literal in '--mode' 'local|server' 'previousManifestSha256' 'previous_manifest_missing' 'previous_manifest_hash_invalid' 'schema_incompatible' 'failed_safe' 'failureCategory' 'rollback_manifest_state_failed' 'rollback_manifest_validation_failed' 'rollback_schema_query_failed' 'rollback_compatibility_check_failed' 'rollback_readiness_failed' 'rollback_smoke_failed' 'rollback_traffic_open_failed' 'Caddyfile.maintenance' 'release-input' 'HAPPYLEARN_APP_IMAGE' 'HAPPYLEARN_WORKER_IMAGE' 'HAPPYLEARN_MIGRATE_IMAGE' 'acceptance' 'phase6-release-rollback.md' 'HAPPYLEARN_LOCAL_' 'FAILURE_INJECTION' 'HAPPYLEARN_ROLLBACK_FAILURE_INJECTION' 'previous_image_incompatible' 'previous_image_readiness_failure' 'invalid_failure_injection' 'readlink -f' 'release.lock'; do
  grep -F -- "$literal" "$target" >/dev/null || { echo "prod rollback contract: FAIL: $literal"; exit 1; }
done

for forbidden in 'down migration' 'pg_restore' 'restic restore' 'DELETE FROM' 'mc rm' 'docker image rm' 'docker system prune' 'change DNS' 'set -x'; do
  ! grep -Fi -- "$forbidden" "$target" >/dev/null || { echo "prod rollback contract: FAIL: unsafe $forbidden"; exit 1; }
done

grep -F -- 'prod-rollback.sh' "$root/scripts/prod-release.sh" >/dev/null || { echo 'prod rollback contract: FAIL: release integration'; exit 1; }
grep -F -- 'rollback_allowed' "$root/scripts/prod-release.sh" >/dev/null || { echo 'prod rollback contract: FAIL: rollback boundary'; exit 1; }
grep -F -- '--entrypoint /app/happylearn-release-manifest acceptance validate --file /release-input/candidate-manifest.json' "$target" >/dev/null || { echo 'prod rollback contract: FAIL: validation mount service'; exit 1; }
grep -F -- '--entrypoint /app/happylearn-release-manifest acceptance compatible --file /release-input/candidate-manifest.json' "$target" >/dev/null || { echo 'prod rollback contract: FAIL: compatibility mount service'; exit 1; }
! grep -E -- '--entrypoint /app/happylearn-release-manifest app (validate|compatible)' "$target" >/dev/null || { echo 'prod rollback contract: FAIL: app lacks release-input mount'; exit 1; }
bash -n "$target"
echo 'prod rollback contract: PASS'
