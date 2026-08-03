#!/usr/bin/env bash
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
target=$root/scripts/prod-release.sh
[[ -f $target ]] || { echo 'prod release contract: FAIL'; exit 1; }
states=(preflight backup_started backup_verified release_mode maintenance drained images_pulled schema_compatible migrated services_started ready smoke_passed activated normal traffic_open succeeded)
last=0
for state in "${states[@]}"; do
  line=$(grep -n -m1 -E "(^|[ (])${state}([ )]|$)" "$target" | cut -d: -f1)
  [[ -n $line && $line -ge $last ]] || { echo "prod release contract: FAIL: state $state"; exit 1; }
  last=$line
done
for literal in '--confirm-maintenance-window' 'root_required' 'flock -n' 'manifestSha256' 'previousManifestSha256' 'backupEvidenceId' 'traceId' 'failed_safe' 'Caddyfile.maintenance' 'prod-backup.sh' 'prod-rollback.sh' 'rollback_allowed' 'release-control' 'release_control_ready' 'pull_release_images' 'for pull_attempt in 1 2 3' 'state_at_least activated' 'reproved_mode' 'current-schema' 'hl_atomic_json_write' 'HAPPYLEARN_RELEASE_FAILURE_INJECTION' 'server_test_variable_rejected' 'invalid_failure_injection' 'inject_after_state'; do
  grep -F -- "$literal" "$target" "$root/scripts/prod-common.sh" >/dev/null || { echo "prod release contract: FAIL: $literal"; exit 1; }
done
for forbidden in 'down migration' 'pg_restore' 'docker system prune' 'rm -rf' 'set -x'; do
  ! grep -F -- "$forbidden" "$target" >/dev/null || { echo "prod release contract: FAIL: $forbidden"; exit 1; }
done
grep -Fq 'state_at_least traffic_open' "$target" || { echo 'prod release contract: FAIL: traffic-open interruption boundary'; exit 1; }
grep -Fq 'persist_state traffic_open failed' "$target" || { echo 'prod release contract: FAIL: traffic-open resume checkpoint'; exit 1; }
grep -Fq '^(activated|normal|traffic_open)$' "$target" || { echo 'prod release contract: FAIL: promoted-manifest resume boundary'; exit 1; }
grep -Fq 'previous_hash=$(jq -r' "$target" || { echo 'prod release contract: FAIL: resumable previous manifest hash'; exit 1; }
grep -Fq 'release-history' "$target" || { echo 'prod release contract: FAIL: terminal release history rollover'; exit 1; }
grep -A2 -F 'if [[ $mode == server ]]; then' "$target" | grep -Fq 'dirty_checkout' || { echo 'prod release contract: FAIL: server dirty-checkout guard'; exit 1; }
bash -n "$target"
echo 'prod release contract: PASS'
