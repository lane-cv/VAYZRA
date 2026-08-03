#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
target=$root/scripts/phase5-restore-verify.sh
fail() { printf 'phase5 restore preserve contract: FAIL: %s\n' "$1" >&2; exit 1; }

for literal in 'HAPPYLEARN_RESTORE_PRESERVE_VERIFIED_VOLUMES:-false' 'HAPPYLEARN_RESTORE_TARGET_PROJECT' 'HAPPYLEARN_RESTORE_HANDOFF_FILE' 'preserve_verified_volumes' 'preserved_volumes_recorded' 'PRESERVED_VOLUMES_RECORD="$CONTROL_DIRECTORY/preserved-volumes"' 'HTTP_PROBE_SUCCEEDED' 'current_resource_matches volumes' 'docker container ls --all' 'cleanup_network' 'PRESERVED_VOLUMES=true' 'cleanup_owned_resources_batch "$status"' 'verified_detached' 'sessionsRevoked' 'authorizationVerified' 'csrfVerified' 'objectIntegrityVerified' 'switchAutomatic'; do
  grep -F -- "$literal" "$target" >/dev/null || fail "$literal"
done

preserve_body=$(sed -n '/^preserve_verified_volumes()/,/^}/p' "$target")
grep -Fq 'HTTP_PROBE_SUCCEEDED' <<<"$preserve_body" || fail 'preservation is not fenced by the final probe'
grep -Fq 'attachments' <<<"$preserve_body" || fail 'volume attachment check missing'
! grep -Fq 'docker volume rm' <<<"$preserve_body" || fail 'preservation deletes a volume'

bash -n "$target"
echo 'phase5 restore preserve contract: PASS'
