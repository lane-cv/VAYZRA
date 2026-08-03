#!/usr/bin/env bash
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
target=$root/scripts/prod-backup.sh
coordinator=$root/scripts/phase5-backup.sh
[[ -f $target ]] || { echo 'prod backup contract: FAIL'; exit 1; }
for literal in '--release-id' '--project happylearn-prod --trigger pre_release' 'root_required' "trigger_kind='pre_release'" "r.state='succeeded'" "a.repository='local'" "string_agg(DISTINCT a.kind, ',' ORDER BY a.kind)" 'database_dump,manifest,object_snapshot' 'r.local_expires_at>clock_timestamp()' 'pre-release.json' 'hl_atomic_json_write'; do
  grep -F -- "$literal" "$target" >/dev/null || { echo "prod backup contract: FAIL: $literal"; exit 1; }
done
for forbidden in 'snapshot_id' 'manifest_sha256' 'docker inspect' 'printenv' 'set -x'; do
  ! grep -F -- "$forbidden" "$target" >/dev/null || { echo "prod backup contract: FAIL: unsafe $forbidden"; exit 1; }
done
bash -n "$target"
# shellcheck disable=SC2016
for literal in '"$PROJECT" != "happylearn-prod"' 'COMPOSE_FILE="$ROOT/deploy/compose.prod.yml"' 'HAPPYLEARN_PRODUCTION_ENV_FILE' "filename='backup-database-password'" "filename='backup-local-repository'" "filename='backup-password'" "PROJECT\" == 'happylearn-prod'" '"10003:600"'; do
  grep -F -- "$literal" "$coordinator" >/dev/null || { echo "prod backup contract: FAIL: coordinator $literal"; exit 1; }
done
echo 'prod backup contract: PASS'
