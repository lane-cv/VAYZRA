#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
target=$root/scripts/e2e-phase6.sh
fail() { printf 'phase6 harness contract: FAIL: %s\n' "$1" >&2; exit 1; }
[[ -x $target && ! -L $target ]] || fail 'scripts/e2e-phase6.sh is missing or unsafe'

required=(
  happylearn_phase6_ compose.prod.yml compose.prod.local.yml Caddyfile.local
  postgres redis minio caddy app worker migrate backup restore acceptance browser
  registry:2 127.0.0.1 HAPPYLEARN_PHASE6_REGISTRY_CONTAINER
  image_set_a image_set_b RepoDigests sha256:
  HAPPYLEARN_APP_IMAGE HAPPYLEARN_WORKER_IMAGE HAPPYLEARN_MIGRATE_IMAGE
  HAPPYLEARN_BACKUP_IMAGE HAPPYLEARN_CADDY_IMAGE HAPPYLEARN_POSTGRES_IMAGE
  HAPPYLEARN_REDIS_IMAGE HAPPYLEARN_MINIO_IMAGE
  HAPPYLEARN_AISTOR_LICENSE_FILE production.env manifest
  chmod\ 0700 chmod\ 0600 trap timeout cleanup zero_resource_proof
  --interactive TMPDIR=
  sanitize-e2e-artifacts.sh publish-e2e-diagnostics.sh
  release_state= release_result= rollback_failure_category=
  --mode local HAPPYLEARN_RELEASE_FAILURE_INJECTION
  all install regression mobile recovery release rollback restart security resources failure-matrix
  HAPPYLEARN_PHASE6_REGRESSION_STAGE
  phase6-release_failure_matrix.sh phase6-release_failure_matrix_adapter.sh E2E_PHASE6_HTTP_BASE_URL E2E_PHASE6_HOSTNAME
  seed_phase5_restore_evidence restore_verifications phase6-phase5-browser-seed
  wrong-restore-secrets wrong\ repository\ key tampered-repository tampered\ repository\ pack restore_projects
  missing-object-restore.env HAPPYLEARN_LOCAL_RESTORE_FAILURE_INJECTION=missing_object
  genuinely\ missing\ restored\ object local_missing_object_injected
  expected_digest BASH_REMATCH printf\ \'%s\\n\'\ \"\$image\"
  backup.restore_check_failure fourth_evidence root_runner\ -eu\ -c
  run_resource_acceptance resource_pid resource-result.json
  run_manual_backup_acceptance browser\ manual\ backup\ did\ not\ complete
  phase6-phase5-browser-seed DELETE\ FROM\ restore_verifications
  resource\ sampler\ ended\ before\ the\ live\ backup\ window workerDrainedBackupObserved
)
for literal in "${required[@]}"; do
  grep -Fq -- "${literal//\\ / }" "$target" || fail "required lifecycle invariant missing: $literal"
done

for forbidden in '--privileged' 'network_mode: host' 'docker system prune' 'docker volume prune' 'docker network prune' 'set -x' ':latest'; do
  ! grep -Fq -- "$forbidden" "$target" || fail "unsafe harness operation present: $forbidden"
done
grep -Fq "project=\"happylearn_phase6_\${nonce}_prod\"" "$target" || fail 'run-unique project prefix missing'
grep -Eq '127\.0\.0\.1:[^:]+:[^:]+' "$target" || fail 'loopback-only port mapping missing'
grep -Eq 'docker (image )?inspect.*RepoDigests' "$target" || fail 'local registry digest resolution missing'
grep -Eq 'find[^\n]*secret|secret[^\n]*find' "$target" || fail 'secret inventory proof missing'
grep -Fq 'run_acceptance_browser_command' "$target" || fail 'internal acceptance browser network scope missing'
grep -Fq "network_scope=\${2:-edge}" "$target" || fail 'public browser does not default to edge-only scope'
# shellcheck disable=SC2016
grep -Fq 'run_browser_command "$1" private' "$target" || fail 'acceptance controls are not isolated to private-network runs'
grep -Fq "E2E_AI_PROCESSING_CONTROL_TOKEN=\"\\\$(cat /run/secrets/e2e-control-token)\"" "$target" || fail 'processing control token is not injected without literal quotes'
! grep -Fq 'E2E_AI_PROCESSING_CONTROL_TOKEN=\"' "$target" || fail 'processing control token contains literal quotes'
grep -Fq 'supervisor_base=$image_set_b_worker' "$target" || fail 'post-release recovery can run the matching worker build'

for script in prod-preflight.sh prod-release.sh prod-rollback.sh prod-restore.sh; do
  file=$root/scripts/$script
  grep -Fq 'server_test_variable_rejected' "$file" || fail "$script does not reject server test controls"
  grep -Fq 'HAPPYLEARN_LOCAL_' "$file" || fail "$script does not reject local-only variables"
  grep -Fq 'FAILURE_INJECTION' "$file" || fail "$script does not reject failure injection"
done

bash -n "$target"
printf '%s\n' 'phase6 harness contract: PASS'
