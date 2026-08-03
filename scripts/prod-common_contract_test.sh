#!/usr/bin/env bash
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
common=$root/scripts/prod-common.sh
[[ -f $common ]] || { echo 'prod common contract: FAIL'; exit 1; }
for literal in 'set -Eeuo pipefail' 'umask 077' "HL_PROD_PROJECT='happylearn-prod'" 'flock -n' 'mktemp --tmpdir=' 'mv -f --' 'timeout --foreground' 'docker compose --project-name' 'jq -e .' 'stat -c' 'HAPPYLEARN_LOCAL_COMPOSE_PROJECT' 'happylearn_phase6_'; do
  grep -F -- "$literal" "$common" >/dev/null || { echo "prod common contract: FAIL: $literal"; exit 1; }
done
bash -n "$common"
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
chmod 0700 "$fixture"
bash -c 'source "$1"; hl_atomic_json_write "$2/state.json" '\''{"state":"preflight"}'\''; [[ $(stat -c %a "$2/state.json") == 600 ]]' _ "$common" "$fixture"
ln -s "$fixture/state.json" "$fixture/link.json"
if bash -c 'source "$1"; hl_secure_file "$2"' _ "$common" "$fixture/link.json" >/dev/null 2>&1; then echo 'prod common contract: FAIL: symlink accepted'; exit 1; fi
echo 'prod common contract: PASS'
