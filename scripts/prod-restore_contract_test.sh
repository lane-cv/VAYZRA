#!/usr/bin/env bash
set -Eeuo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
target=$root/scripts/prod-restore.sh
phase5=$root/scripts/phase5-restore-verify.sh
[[ -f $target ]] || { echo 'prod restore contract: FAIL: script missing'; exit 1; }

for literal in '--project-dir' '--env-file' '--mode' 'local|server' '--target-project' '--backup-id' '--destructive' '--confirmation' 'happylearn-prod' 'active_public_traffic' 'maintenance_required' 'second_recovery_point' 'repository_check' 'replacement_volume_not_empty' 'phase5-restore-verify.sh' 'HAPPYLEARN_RESTORE_PRESERVE_VERIFIED_VOLUMES=true' 'HAPPYLEARN_RESTORE_HANDOFF_FILE' 'revoke_restored_sessions' 'authorization' 'csrf' 'object' 'switch-proposal' 'switch_not_automatic' 'HAPPYLEARN_LOCAL_RESTORE_FAILURE_INJECTION' 'missing_object' 'inject_real_missing_object' 'SELECT repository' '"$client" rm --force' '"$client" stat' 'local_missing_object_injected' "10003:0:700" 'backup_path_invalid' 'maintenance_probe_failed' '--resolve "$1:443:127.0.0.1"'; do
  grep -F -- "$literal" "$target" "$phase5" >/dev/null || { echo "prod restore contract: FAIL: $literal"; exit 1; }
done

inject_line=$(grep -n '^[[:space:]]*inject_real_missing_object$' "$phase5" | cut -d: -f1)
check_line=$(grep -n '^[[:space:]]*run_restore_check$' "$phase5" | cut -d: -f1)
[[ $inject_line =~ ^[0-9]+$ && $check_line =~ ^[0-9]+$ && $inject_line -lt $check_line ]] || {
  echo 'prod restore contract: FAIL: real missing-object injection is not immediately before verification'
  exit 1
}

for forbidden in 'docker volume rm' 'compose down' 'happylearn-prod_postgres_data' 'happylearn-prod_minio_data' 'automatic switch' 'set -x'; do
  ! grep -Fi -- "$forbidden" "$target" >/dev/null || { echo "prod restore contract: FAIL: unsafe $forbidden"; exit 1; }
done
grep -Fq 'compgen -v' "$target" || { echo 'prod restore contract: FAIL: inherited server test-variable guard'; exit 1; }

grep -F -- 'preserve_verified_volumes' "$phase5" >/dev/null || { echo 'prod restore contract: FAIL: phase5 preserve mode'; exit 1; }
grep -F -- '[[ ${#object_key} -le 1024 &&' "$phase5" >/dev/null || { echo 'prod restore contract: FAIL: portable object-key length guard'; exit 1; }
grep -F -- '"$object_key" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$' "$phase5" >/dev/null || { echo 'prod restore contract: FAIL: portable object-key character guard'; exit 1; }
! grep -E -- 'object_key.*\{[0-9]+,[0-9]{3,}\}' "$phase5" >/dev/null || { echo 'prod restore contract: FAIL: non-portable musl regex interval'; exit 1; }
bash -n "$target" "$phase5"
echo 'prod restore contract: PASS'
