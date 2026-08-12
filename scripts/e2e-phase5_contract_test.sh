#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd -P)"
target="$repo_root/scripts/e2e-phase5.sh"
harness_lib="$repo_root/scripts/e2e-harness-lib.sh"
e2e_overlay="$repo_root/deploy/compose.phase5-e2e-live.yml"
makefile="$repo_root/Makefile"
package_json="$repo_root/package.json"
verify_workflow="$repo_root/.github/workflows/verify.yml"

fail() {
  printf 'phase 5 E2E harness contract: %s\n' "$1" >&2
  exit 1
}

require_literal() {
  grep -Fq -- "$2" "$1" ||
    fail "missing contract literal: $2"
}

require_pattern() {
  grep -Eq -- "$2" "$1" ||
    fail "missing contract pattern: $2"
}

[[ -f "$target" ]] || fail 'scripts/e2e-phase5.sh is absent'
[[ -f "$harness_lib" && -f "$e2e_overlay" ]] ||
  fail 'shared Phase 5 harness files are absent'
[[ -f "$makefile" && -f "$package_json" && -f "$verify_workflow" ]] ||
  fail 'Phase 5 command entrypoint files are absent'
bash -n "$target"

e2e_contracts_block="$(
  sed -n '/^e2e-contracts:/,/^[^[:space:]].*:$/p' "$makefile"
)"
for contract_script in \
  phase5-backup_contract_test.sh \
  phase5-restore_contract_test.sh \
  phase5-restore_live_contract_test.sh \
  e2e-phase5_failure_matrix_contract_test.sh \
  host-metrics_contract_test.sh \
  host-metrics_uid_contract_test.sh \
  phase5-operations-docs_contract_test.sh; do
  [[ "$(grep -Fxc \
    $'\t'bash' scripts/'"$contract_script" \
    <<<"$e2e_contracts_block")" == 1 ]] ||
    fail "standard verification must invoke $contract_script exactly once"
  [[ "$(grep -Fc "$contract_script" "$verify_workflow")" == 0 ]] ||
    fail "verify workflow duplicated $contract_script outside the aggregate"
done
[[ "$(grep -Fc 'run: pnpm e2e-contracts' "$verify_workflow")" == 1 ]] ||
  fail 'verify workflow must invoke the contract aggregate exactly once'
node -e '
  const packageJSON = require(process.argv[1]);
  if (packageJSON.scripts?.["e2e-contracts"] !== "make e2e-contracts") {
    process.exit(1);
  }
' "$package_json" ||
  fail 'package.json must expose the single contract aggregate'

intent_line="$(grep -n '^persist_resource_intents$' "$target" | cut -d: -f1)"
full_trap_line="$(
  grep -n '^if \[\[ -z "\$probe_mode"' "$target" | cut -d: -f1
)"
first_resource_line="$(grep -n '^create_fixture_ca$' "$target" | cut -d: -f1)"
[[ "$intent_line" =~ ^[0-9]+$ &&
  "$full_trap_line" =~ ^[0-9]+$ &&
  "$first_resource_line" =~ ^[0-9]+$ &&
  "$intent_line" -lt "$full_trap_line" &&
  "$full_trap_line" -lt "$first_resource_line" ]] ||
  fail 'resource intents/traps were not persistent before resource creation'

require_literal "$target" 'source "$script_dir/e2e-harness-lib.sh"'
require_literal "$target" '"$license_file" != /*'
require_literal "$target" '! -r "$license_file"'
require_literal "$target" 'prefix="happylearn_phase5_'
require_pattern "$target" \
  'docker_bounded 60 network create --internal([[:space:]\\]|$)'
require_literal "$target" 'case "$e2e_group" in'
require_literal "$target" 'all|phase5|phase5-mobile|recovery|resources)'
require_literal "$target" \
  'all|phase5|phase5-mobile) seed_phase5_browser_data ;;'
if grep -Fxq 'seed_phase5_browser_data' "$target"; then
  fail 'browser-only database seed ran unconditionally'
fi
require_literal "$target" 'remove_phase5_browser_backup_seed()'
require_literal "$target" \
  "DELETE FROM restore_verifications WHERE backup_run_id='53000000-0000-4000-8000-000000000001'"
require_literal "$target" \
  "DELETE FROM backup_artifacts WHERE backup_run_id='53000000-0000-4000-8000-000000000001'"
require_literal "$target" \
  "DELETE FROM backup_runs WHERE id='53000000-0000-4000-8000-000000000001'"
all_group_block="$(sed -n '/^  all)$/,/^    ;;$/p' "$target")"
all_seed_cleanup_line="$(
  grep -n 'remove_phase5_browser_backup_seed' <<<"$all_group_block" |
    cut -d: -f1
)"
all_resource_line="$(
  grep -n 'run_resource_sample 60' <<<"$all_group_block" |
    cut -d: -f1
)"
[[ "$all_seed_cleanup_line" =~ ^[0-9]+$ &&
  "$all_resource_line" =~ ^[0-9]+$ &&
  "$all_seed_cleanup_line" -lt "$all_resource_line" ]] ||
  fail 'all group did not remove the browser backup seed before retention'
require_literal "$target" \
  'live_project="happylearn-phase5-live-${fixture_suffix}"'
require_literal "$target" \
  'restore_license_file="$tmpdir/restore-minio.license"'
require_literal "$target" \
  'restore_controller_image="happylearn-restore-controller:phase5-${nonce}"'
require_literal "$target" \
  'restore_controller="${prefix}_restore_controller"'
require_literal "$target" \
  'restore_repository_handoff="${prefix}_restore_repository_handoff"'
require_literal "$target" 'restore_active_backup_id='
require_literal "$target" 'restore_controller_uid='
require_literal "$target" 'restore_controller_gid='
require_literal "$target" \
  'restore_controller_requires_repository_prepare=false'
require_literal "$target" 'restore_run_active=false'
require_literal "$target" \
  'install -m 0400 "$license_file" "$restore_license_file"'
require_literal "$target" \
  '"$(portable_file_mode "$restore_license_file")" == 400'
require_literal "$target" \
  '--env "HAPPYLEARN_AISTOR_LICENSE_FILE=$restore_license_file"'
require_literal "$target" \
  '--target restore_live_controller'
require_literal "$target" \
  'resolve_restore_controller_identity'
require_literal "$target" \
  'prepare_restore_repository_access'
require_literal "$target" \
  'prepare_restore_repository_access "$backup_id"'
require_literal "$target" \
  'case "$(uname -s)" in'
require_literal "$target" 'Darwin)'
require_literal "$target" 'Linux)'
require_literal "$target" 'restore_controller_uid=0'
require_literal "$target" 'restore_controller_gid=0'
require_literal "$target" \
  'chown -R -- "$uid:$gid" /repository'
require_literal "$target" \
  'docker_bounded 3600 run --rm --name "$restore_controller"'
require_literal "$target" \
  '--mount "type=bind,src=$restore_docker_socket,dst=/var/run/docker.sock"'
require_literal "$target" \
  'restore_docker_socket_group='
require_literal "$target" 'portable_file_group()'
require_literal "$target" \
  'restore_docker_socket_group="$(portable_file_group "$restore_docker_socket")"'
require_literal "$target" \
  '--group-add "$restore_docker_socket_group"'
require_literal "$target" \
  '--user "$restore_controller_uid:$restore_controller_gid"'
require_literal "$target" \
  'uid=$restore_controller_uid,gid=$restore_controller_gid,mode=0700'
if grep -Fq -- '--group-add 0' "$target"; then
  fail 'restore controller assumed the Docker socket belongs to group 0'
fi
restore_proof_block="$(sed -n '/^run_restore_proof()/,/^}/p' "$target")"
require_literal "$target" \
  'if [[ "$restore_controller_requires_repository_prepare" == true ]]; then'
if grep -Fq -- \
  'run_bounded 3600 bash "$script_dir/phase5-restore-verify.sh"' \
  <<<"$restore_proof_block"; then
  fail 'restore proof depended on host flock instead of the pinned controller'
fi
active_line="$(
  grep -n 'restore_run_active=true' <<<"$restore_proof_block" |
    cut -d: -f1
)"
prepare_line="$(
  grep -n 'prepare_restore_repository_access' <<<"$restore_proof_block" |
    cut -d: -f1
)"
controller_line="$(
  grep -n 'docker_bounded 3600 run --rm --name "$restore_controller"' \
    <<<"$restore_proof_block" |
    cut -d: -f1
)"
quiesce_line="$(
  grep -n 'quiesce_restore_access_containers' <<<"$restore_proof_block" |
    cut -d: -f1
)"
verify_line="$(
  grep -n 'verify_restore_resources_absent "$backup_id"' \
    <<<"$restore_proof_block" |
    cut -d: -f1 |
    tail -n 1
)"
[[ "$active_line" =~ ^[0-9]+$ &&
  "$prepare_line" =~ ^[0-9]+$ &&
  "$controller_line" =~ ^[0-9]+$ &&
  "$quiesce_line" =~ ^[0-9]+$ &&
  "$verify_line" =~ ^[0-9]+$ &&
  "$active_line" -lt "$prepare_line" &&
  "$prepare_line" -lt "$controller_line" &&
  "$controller_line" -lt "$quiesce_line" &&
  "$quiesce_line" -lt "$verify_line" ]] ||
  fail 'restore controller active, prepare, run, quiesce, verify order changed'
if grep -Fq -- '--cap-add' <<<"$restore_proof_block"; then
  fail 'restore controller retained Linux capabilities'
fi
controller_command_block="$(
  sed -n \
    '/docker_bounded 3600 run --rm --name "\$restore_controller"/,/controller_status=\$\?/p' \
    <<<"$restore_proof_block"
)"
for controller_literal in \
  '--label "io.happylearn.phase5.restore-access-backup-id=${backup_id}"' \
  '--label "io.happylearn.phase5.restore-access-kind=controller"' \
  '--network none --read-only' \
  '--cap-drop ALL --security-opt no-new-privileges' \
  '--mount "type=bind,src=$repo_root,dst=$repo_root,readonly"' \
  '--mount "type=bind,src=$backup_host_root/secrets,dst=$backup_host_root/secrets,readonly"' \
  '--mount "type=bind,src=$restore_license_file,dst=$restore_license_file,readonly"' \
  '--mount "type=bind,src=$teacher_credential_file,dst=$teacher_credential_file,readonly"'; do
  grep -Fq -- "$controller_literal" <<<"$controller_command_block" ||
    fail "restore controller lost scoped security option: $controller_literal"
done
if grep -Fq -- '--cap-add' <<<"$controller_command_block" ||
  grep -Fq -- '--privileged' <<<"$controller_command_block"; then
  fail 'restore controller gained an unapproved privilege option'
fi
if grep -Fq -- \
  '--label "io.happylearn.phase5.restore-backup-id=${backup_id}"' \
  <<<"$controller_command_block"; then
  fail 'restore controller reused the nested restore-resource label'
fi

for removed_restore_literal in \
  'restore_private_' \
  'restore_access_handback' \
  'restore_cleanup_handback' \
  'restore_controller_requires_handoff' \
  'restore_access_handoff_active' \
  'handoff_restore_controller_access'; do
  if grep -Fq -- "$removed_restore_literal" "$target"; then
    fail "restore proof retained removed access layer: $removed_restore_literal"
  fi
done
require_literal "$target" 'quiesce_restore_access_containers()'
require_literal "$target" '--filter "id=${id}"'
require_literal "$target" \
  'label=io.happylearn.phase5.restore-access-backup-id=${backup_id}'
require_literal "$target" 'recover_active_restore_run()'
require_literal "$target" \
  '[[ "$restore_run_active" == true ]] || return 0'
require_literal "$target" \
  '"$(portable_file_owner "$report_file")" == "$restore_host_uid"'
require_literal "$target" \
  '"$(portable_file_group "$report_file")" == "$restore_host_gid"'

cleanup_block="$(sed -n '/^cleanup()/,/^}/p' "$target")"
cleanup_remove_line="$(
  grep -n 'remove_active_temporary_containers' <<<"$cleanup_block" |
    cut -d: -f1
)"
cleanup_recovery_line="$(
  grep -n 'recover_active_restore_run' <<<"$cleanup_block" |
    cut -d: -f1
)"
cleanup_diagnostics_line="$(
  grep -n 'then diagnostics' <<<"$cleanup_block" | cut -d: -f1
)"
[[ "$cleanup_remove_line" =~ ^[0-9]+$ &&
  "$cleanup_recovery_line" =~ ^[0-9]+$ &&
  "$cleanup_diagnostics_line" =~ ^[0-9]+$ &&
  "$cleanup_remove_line" -lt "$cleanup_recovery_line" &&
  "$cleanup_recovery_line" -lt "$cleanup_diagnostics_line" ]] ||
  fail 'cleanup did not stop the controller and restore host access first'

restore_identity_source="$(
  sed -n '/^resolve_restore_controller_identity()/,/^}/p' "$target"
)"
for identity_case in Darwin Linux; do
  (
    id() {
      case "$1" in
        -u) printf '501\n' ;;
        -g) printf '20\n' ;;
        *) return 2 ;;
      esac
    }
    uname() { printf '%s\n' "$identity_case"; }
    eval "$restore_identity_source"
    restore_host_uid=''
    restore_host_gid=''
    restore_controller_uid=''
    restore_controller_gid=''
    resolve_restore_controller_identity
    [[ "$restore_host_uid:$restore_host_gid" == 501:20 ]]
    if [[ "$identity_case" == Darwin ]]; then
      [[ "$restore_controller_uid:$restore_controller_gid" == 0:0 ]]
      [[ "$restore_controller_requires_repository_prepare" == false ]]
    else
      [[ "$restore_controller_uid:$restore_controller_gid" == 501:20 ]]
      [[ "$restore_controller_requires_repository_prepare" == true ]]
    fi
  ) || fail "restore controller identity failed for $identity_case"
done
if (
  id() {
    case "$1" in
      -u) printf '0\n' ;;
      -g) printf '0\n' ;;
      *) return 2 ;;
    esac
  }
  uname() { printf 'Linux\n'; }
  eval "$restore_identity_source"
  resolve_restore_controller_identity
); then
  fail 'restore controller identity accepted a root Linux host'
fi
if (
  id() {
    case "$1" in
      -u) printf '501\n' ;;
      -g) printf '20\n' ;;
      *) return 2 ;;
    esac
  }
  uname() { printf 'UnsupportedOS\n'; }
  eval "$restore_identity_source"
  resolve_restore_controller_identity
); then
  fail 'restore controller identity accepted an unsupported host platform'
fi
prepare_restore_source="$(
  sed -n '/^prepare_restore_repository_access()/,/^}/p' "$target"
)"
for prepare_literal in \
  'local backup_id="${1:?backup ID required}"' \
  '--label "io.happylearn.phase5.restore-access-backup-id=${backup_id}"' \
  '--label "io.happylearn.phase5.restore-access-kind=repository-prepare"'; do
  grep -Fq -- "$prepare_literal" <<<"$prepare_restore_source" ||
    fail "Linux repository prepare lost scoped identity: $prepare_literal"
done
if grep -Fq -- \
  '--label "io.happylearn.phase5.restore-backup-id=${backup_id}"' \
  <<<"$prepare_restore_source"; then
  fail 'Linux repository prepare reused the nested restore-resource label'
fi
if ! (
  eval "$prepare_restore_source"
  restore_repository_handoff=restore-pre
  live_project=happylearn-phase5-live-a1b2c3d4e5f6
  fixture_suffix=a1b2c3d4e5f6
  backup_host_root=/backup
  backup_image=backup-image
  id() {
    case "$1" in
      -u) printf '501\n' ;;
      -g) printf '20\n' ;;
      *) return 99 ;;
    esac
  }
  docker_bounded() { return 37; }
  portable_file_owner() { printf '501\n'; }
  portable_file_mode() { printf '700\n'; }
  if prepare_restore_repository_access \
    11111111-1111-4111-8111-111111111111; then
    exit 1
  else
    [[ "$?" == 37 ]]
  fi
); then
  fail 'Linux repository handoff masked the Docker controller status'
fi
if grep -Fq -- 'chmod 0400 "$license_file"' "$target"; then
  fail 'restore preparation mutated the caller-owned license file'
fi
require_literal "$target" \
  'compose_file="$repo_root/deploy/compose.dev.yml"'
require_literal "$target" \
  'compose_live_file="$repo_root/deploy/compose.backup-live.yml"'
require_literal "$target" \
  'compose_e2e_live_file="$repo_root/deploy/compose.phase5-e2e-live.yml"'
require_literal "$target" \
  'aistor_license_volume="${live_project}_aistor_license_runtime"'
require_literal "$target" 'compose_live()'
require_literal "$target" '--project-name "$live_project"'
require_literal "$target" '--file "$compose_file"'
require_literal "$target" '--file "$compose_live_file"'
require_literal "$target" '--file "$compose_e2e_live_file"'
require_literal "$target" 'HAPPYLEARN_BACKUP_LIVE_TEST=1'
require_literal "$target" 'HAPPYLEARN_BACKUP_LIVE_PROJECT="$live_project"'
require_literal "$target" 'HAPPYLEARN_BACKUP_LIVE_ROOT="$backup_host_root"'
require_literal "$target" \
  'run_bounded 3600 bash "$script_dir/phase5-backup.sh"'

for resource in \
  postgres redis primary_aistor remote_s3 app worker backup host_sample \
  browser_runner; do
  require_literal "$target" "\"\$$resource\""
done

for secret in \
  ai-master database-password object-access object-secret metrics-bearer \
  host-metrics-hmac restic-local-repository restic-local-password \
  restic-remote-repository restic-remote-password \
  restic-remote-access-key restic-remote-secret-key age-identity \
  webhook-url webhook-authorization login-throttle; do
  require_literal "$target" "$secret"
done
require_literal "$target" 'openssl rand -base64 32'
require_literal "$target" \
  'new_ai_master >"$secret_source_dir/ai-master"'
require_literal "$target" 'openssl rand -hex 32'
require_literal "$target" \
  'new_database_password >"$secret_source_dir/database-password"'
if grep -A1 -F 'for secret_name in \' "$target" |
  grep -Fq 'ai-master database-password'; then
  fail 'AI master key used the generic 36-byte secret generator'
fi
if grep -A1 -F 'for secret_name in \' "$target" |
  grep -Fq 'database-password object-access'; then
  fail 'database password used a URL-unsafe generic secret generator'
fi
require_literal "$target" \
  "printf '%s' 'http://localhost:8080/happylearn-phase5'"
if grep -Fq '.invalid' "$target"; then
  fail 'Phase 5 webhook fixture used a non-resolving .invalid host'
fi
require_literal "$target" 'umask 077'
require_literal "$target" 'chmod 0600 "$secret_source_dir/"*'
require_literal "$target" 'chmod 0500 "$directory"'
require_literal "$target" '--cap-add DAC_READ_SEARCH'
secret_init_block="$(sed -n \
  '/^initialize_secret_volume()/,/^}/p' "$target")"
if grep -Fq -- '--cap-add FOWNER' <<<"$secret_init_block" ||
  grep -Fq -- '--cap-add DAC_OVERRIDE' <<<"$secret_init_block"; then
  fail 'secret init retained an unnecessary mutation capability'
fi
require_literal "$target" 'verify_secret_consumer_reads'
require_literal "$target" \
  'install_consumer admin 10001:10001 0600 admin-password'
require_literal "$target" \
  'install_consumer postgres 999:999 0400 database-password'
require_literal "$target" \
  'chmod "$mode" "$target"'
require_literal "$target" \
  'chown "$owner" "$target"'
if grep -Fq -- \
  'chmod 0600 /owned-secrets/admin/admin-password' \
  "$target"; then
  fail 'admin secret mode was mutated after ownership transfer'
fi
require_literal "$target" \
  'secret_probe_admin|10001:10001|admin|admin-password|10001:10001|600'
require_literal "$target" \
  '--mount "type=volume,src=$secret_volume,dst=/run/secrets-admin,volume-subpath=admin,readonly"'
require_literal "$target" \
  '--password-file /run/secrets-admin/admin-password'
if grep -Fq -- \
  '--mount "type=volume,src=$secret_volume,dst=/run/secrets-browser,volume-subpath=browser,readonly"' \
  "$target"; then
  fail 'teacher bootstrap mounted browser-owned credentials'
fi
for uid in 999:999 1000:0 10001:10001 10002:10002 10003:0; do
  require_literal "$target" "'$uid'"
done
require_literal "$target" \
  'test "$(stat -c "%u:%g:%a" "$target")" = "$owner:${mode#0}"'
require_literal "$target" \
  'test "$(stat -c "%u:%g:%a" "$directory")" = "$owner:500"'
require_literal "$target" 'owned_container_ledger="$tmpdir/owned-containers.tsv"'
require_literal "$target" \
  'container_intent_ledger="$tmpdir/container-intents.tsv"'
require_literal "$target" 'network_intent_ledger="$tmpdir/network-intents.tsv"'
require_literal "$target" 'volume_intent_ledger="$tmpdir/volume-intents.tsv"'
require_literal "$target" 'image_intent_ledger="$tmpdir/image-intents.tsv"'
require_literal "$target" \
  'host_sample_dockerfile="$tmpdir/Dockerfile.host-sampler"'
require_literal "$target" 'seed_sql_file="$tmpdir/phase5-browser-seed.sql"'
require_literal "$target" 'chmod 0600 "$host_sample_dockerfile"'
require_literal "$target" \
  '"$(portable_file_mode "$host_sample_dockerfile")" != 600'
require_literal "$target" \
  'docker_bounded 900 build -f "$host_sample_dockerfile"'
if grep -Fq -- 'docker_bounded 900 build -f -' "$target"; then
  fail 'bounded host sampler build cannot depend on background stdin'
fi
require_literal "$target" \
  '--mount "type=bind,src=$secret_source_dir/age-identity,dst=/input/age-identity,readonly"'
require_literal "$target" '--user "$(id -u):$(id -g)"'
require_literal "$target" '"$backup_image" -y /input/age-identity'
if grep -Fq -- 'docker_bounded 60 run --rm --interactive' "$target"; then
  fail 'bounded age recipient derivation cannot depend on background stdin'
fi
require_literal "$target" 'chmod 0600 "$seed_sql_file"'
require_literal "$target" '--command "$(<"$seed_sql_file")"'
if grep -Fq -- 'docker_bounded 120 exec --interactive "$postgres"' "$target"; then
  fail 'bounded database seeding cannot depend on background stdin'
fi
require_literal "$target" 'persist_resource_intents'
require_literal "$target" 'remove_intended_containers'
require_literal "$target" 'remove_intended_networks'
require_literal "$target" 'remove_intended_volumes'
require_literal "$target" 'remove_intended_images'
require_literal "$target" \
  '"$intended_reference" =~ ^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9_.-]+)?$'
require_literal "$target" 'record_owned_container'
require_literal "$target" 'remove_owned_container_if_match'
require_literal "$target" 'remove_owned_network_if_match'
require_literal "$target" 'remove_owned_volume_if_match'
require_literal "$target" 'remove_owned_image_if_match'
require_literal "$target" 'validate_resource_container_identity'
require_literal "$target" 'validate_resource_ephemeral_identities'
require_literal "$target" \
  '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}'
require_literal "$target" '--cleanup-container-ownership-probe'
require_literal "$target" '--resource-identity-contract-probe'

require_literal "$target" \
  'run_compose_one_shot phase5-secrets-init'
require_literal "$target" \
  'run_compose_one_shot postgres-tls-init'
require_literal "$target" \
  'run_compose_one_shot minio-data-init'
require_literal "$target" \
  '"$aistor_license_volume") compose_volume=aistor_license_runtime ;;'
require_literal "$target" \
  '"${live_project}-aistor-license-init-1"'
require_literal "$e2e_overlay" '--license /license/minio.license'

owned_volumes_block="$(sed -n '/^owned_volumes=(/,/^)/p' "$target")"
[[ "$(grep -Fxc '  "$aistor_license_volume" \' \
  <<<"$owned_volumes_block")" == 1 ]] ||
  fail 'AIStor license volume must be tracked exactly once for intent and cleanup'

one_shot_block="$(sed -n '/^run_compose_one_shot()/,/^}/p' "$target")"
[[ "$(grep -Fxc \
  '    phase5-secrets-init|postgres-tls-init|minio-data-init|aistor-license-init) ;;' \
  <<<"$one_shot_block")" == 1 ]] ||
  fail 'AIStor license initializer must be allowed by the one-shot helper'

start_dependencies_block="$(sed -n '/^start_dependencies()/,/^}/p' "$target")"
aistor_init_line="$(
  grep -nFx '  run_compose_one_shot aistor-license-init' \
    <<<"$start_dependencies_block" | cut -d: -f1
)"
long_lived_start_line="$(
  grep -nFx \
    '  if ! compose_live up --detach --no-build --no-deps postgres redis minio; then' \
    <<<"$start_dependencies_block" | cut -d: -f1
)"
[[ "$aistor_init_line" =~ ^[0-9]+$ &&
  "$long_lived_start_line" =~ ^[0-9]+$ &&
  "$aistor_init_line" -lt "$long_lived_start_line" ]] ||
  fail 'AIStor license initialization must precede long-lived dependency startup'
for literal in \
  'wait_for primary-AIStor "$primary_aistor" exec "$primary_aistor" /bin/sh -ceu' \
  '. /run/phase5-secrets/runtime.env' \
  'mc alias set primary http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1' \
  'mc ls primary >/dev/null 2>&1'; do
  grep -Fq -- "$literal" <<<"$start_dependencies_block" ||
    fail 'primary AIStor must accept authenticated bucket operations before app startup'
done
! grep -Fq 'http://127.0.0.1:9000/minio/health/live' \
  <<<"$start_dependencies_block" ||
  fail 'Phase 5 dependencies used anonymous AIStor liveness'
if grep -Fq -- \
  'compose_live up --detach --no-build postgres redis minio' "$target"; then
  fail 'long-lived dependencies can restart completed one-shot initializers'
fi
require_literal "$target" \
  'compose_live up --detach --no-build --no-deps app'
require_literal "$target" \
  'compose_live up --detach --no-build --no-deps worker'
require_literal "$target" 'verify_compose_service_claims'
require_literal "$target" 'compose_live ps --quiet "$service"'
require_literal "$target" 'compose_live stop --timeout 30 worker'
require_literal "$target" \
  'compose_live up --detach --no-build --no-deps worker'
if grep -Fq -- 'compose_live start worker' "$target"; then
  fail 'worker restart can re-run completed one-shot dependencies'
fi
require_literal "$target" '--name "$backup" --network "$network"'
require_literal "$target" '--read-only --user 10003:0 --cap-drop ALL'
require_literal "$target" '--name "$browser_runner" --network "$network"'
require_literal "$target" '--read-only --user 1000:1000 --shm-size 384m'
require_literal "$target" \
  '--read-only --user 1000:1000 --shm-size 384m --memory 896m --cpus .4'
require_literal "$target" '--cap-drop ALL --security-opt no-new-privileges'
if grep -Eq -- \
  '(^|[[:space:]])(--privileged|--network[=[:space:]]+host|--network host)([[:space:]]|$)|(^|[[:space:]])(-p|--publish)([=[:space:]]|$)' \
  "$target"; then
  fail 'unsafe Docker privilege, host access, socket, or published port'
fi
if grep -Eq -- '(^|[[:space:]])(set -x|env|printenv)([[:space:]]|$)' "$target"; then
  fail 'secret-printing shell command is forbidden'
fi
if grep -Eq -- \
  '-e[[:space:]]+"?(HAPPYLEARN_(AI_MASTER_KEY|DATABASE_URL|LOGIN_THROTTLE_SECRET|MINIO_ACCESS_KEY|MINIO_SECRET_KEY|METRICS_BEARER_SECRET|HOST_METRICS_HMAC_SECRET|WEBHOOK_URL|WEBHOOK_AUTHORIZATION)|RESTIC_PASSWORD|AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY)=' \
  "$target"; then
  fail 'a secret entered Docker configured environment'
fi

require_literal "$target" 'run_phase5_desktop'
require_literal "$target" 'tests/e2e/operations.spec.ts tests/e2e/backup-restore.spec.ts'
require_literal "$repo_root/tests/e2e/operations.spec.ts" \
  '/api/v1/admin/operations/backups/53000000-0000-4000-8000-000000000001'
require_literal "$target" '--project=chromium'
require_literal "$target" 'if [[ "$e2e_group" == all ]]; then'
require_literal "$target" '--no-cache-filter runtime'
if grep -Fq -- 'worker_build_cache_args' "$target"; then
  fail 'worker cache refresh depended on a Bash 3.2-unsafe empty array'
fi
require_literal "$target" 'run_phase5_mobile'
require_literal "$target" '--project=mobile --grep @phase5-mobile'
require_literal "$target" 'run_all_desktop'
for phase in phase1 phase2 phase3 phase4 phase5 phase4-mobile phase5-mobile; do
  require_literal "$target" "/artifacts/results/$phase"
done
require_literal "$target" 'run_backup_workflow'
require_literal "$target" 'finalize_backup_proof'
require_literal "$target" 'run_backup_proof'
require_literal "$target" 'phase5-backup.sh'
require_literal "$target" 'run_restore_proof'
require_literal "$target" 'phase5-restore-verify.sh'
require_literal "$target" 'seed_phase5_browser_data'
require_literal "$target" 'INSERT INTO operational_alerts'
require_literal "$target" "'phase5_e2e_open_backup_alert'"
if grep -Fq -- 'phase5-e2e-open-backup-alert' "$target"; then
  fail 'Phase 5 alert seed used a client-rejected public identifier'
fi
require_literal "$target" "'open'"
require_literal "$target" 'INSERT INTO backup_runs'
require_literal "$target" "'succeeded'"
require_literal "$target" 'INSERT INTO backup_artifacts'
require_literal "$target" "'local'"
require_literal "$target" "'remote'"
require_literal "$target" 'INSERT INTO restore_verifications'
require_literal "$target" 'session_revocation_verified'
require_literal "$target" 'write_recovery_backup_id'
require_literal "$target" 'remote_snapshot_id'
require_literal "$target" \
  'coordinator_one_shot_file="$backup_host_root/coordinator-one-shots"'
require_literal "$target" \
  'coordinator_run_id_file="$backup_host_root/coordinator-run-id"'
require_literal "$target" 'remove_coordinator_one_shots audit'
require_literal "$target" 'remove_coordinator_one_shots cleanup'
require_literal "$target" 'coordinator one-shot failed: category=%s'
require_literal "$target" 'backup finalization failed: category=%s'
for category in \
  ledger id listing missing collision metadata identity audit removal \
  residual_listing residual ledger_reset; do
  require_literal "$target" "coordinator_one_shot_failure $category"
done
require_literal "$target" 'backup_finalization_failure one_shot_audit'
require_literal "$target" 'backup_finalization_failure recovery_evidence'
for category in invalidate handoff query invalid write publish; do
  require_literal "$target" "recovery_evidence_failure $category"
done
require_literal "$target" "WHERE id='\${expected_backup_id}'::uuid"
require_literal "$target" \
  'mv -f "$staging" "$artifact_dir/backup-id"'
require_literal "$target" 'recovery_backup_id="$backup_id"'
require_literal "$target" \
  '[[ "$backup_id" == "$recovery_backup_id" ]]'
if grep -Fq "idempotency_key LIKE 'host-%'" "$target"; then
  fail 'recovery evidence still guessed the coordinator run by host prefix'
fi
require_literal "$target" \
  'HAPPYLEARN_PHASE5_E2E_OWNER="$fixture_suffix"'
require_literal "$target" \
  '{{index .Config.Labels "com.docker.compose.oneoff"}}'
require_literal "$target" \
  '--env "HAPPYLEARN_RESTORE_CONTROL_DIRECTORY=$restore_control_dir"'
require_literal "$target" \
  '--env "HAPPYLEARN_RESTORE_REPORT_DIRECTORY=$restore_report_dir"'
if grep -Eq 'phase5-(backup|restore)_live_test\.sh' "$target"; then
  fail 'an unsafe live fixture was used as a black-box acceptance proof'
fi
require_literal "$target" 'run_resource_sample 60'
require_literal "$target" 'run_resource_sample 1800'
require_literal "$target" 'run_resource_browser_load'
require_literal "$target" \
  'local command="$1" timeout="${2:-1800}"'
require_literal "$target" \
  '[[ "$timeout" =~ ^[1-9][0-9]*$ && "$timeout" -le 3600 ]]'
require_literal "$target" '"$((duration + 300))"'
require_literal "$target" \
  'resource sample failed: browser=%s backup=%s monitor=%s finalize=%s'
require_literal "$target" \
  'run_resource_child "$resource_backup_status_file" run_backup_workflow &'
require_literal "$target" 'monitor_resource_workloads'
require_literal "$target" 'run_resource_child'
require_literal "$target" 'read_resource_child_status'
require_literal "$target" 'cancel_resource_workloads'
require_literal "$target" 'merge_resource_statuses'
require_literal "$target" 'validate_resource_evidence'
require_literal "$target" \
  '--filter "label=com.docker.compose.project=${live_project}"'
require_literal "$target" \
  '--filter "label=io.happylearn.phase5.e2e-owner=${fixture_suffix}"'
require_literal "$target" \
  '{{.State.OOMKilled}}|{{.RestartCount}}|{{.HostConfig.NanoCpus}}|{{.HostConfig.Memory}}'
require_literal "$target" 'resource_is_backup_activity'
require_literal "$target" 'resource_worker_backup_overlap_now'
require_literal "$target" 'worker_backup_overlap'
require_literal "$target" 'browser_included'
require_literal "$target" 'configured_limits_complete'
require_literal "$target" 'peak_configured_cpu'
require_literal "$target" 'peak_configured_memory_mib'
require_literal "$target" 'peak_live_cpu_percent'
require_literal "$target" 'peak_live_memory_mib'
require_literal "$target" 'peak_browser_cpu_percent'
require_literal "$target" 'peak_browser_memory_mib'
require_literal "$target" 'preserved_resource_evidence'
require_literal "$target" 'validate_resource_evidence "$resource_report"'
require_literal "$target" \
  'install -m 0600 "$preserved_resource_evidence" "$resource_report"'
require_literal "$target" 'saw_browser'
require_literal "$target" 'saw_backup'
require_literal "$target" 'saw_heavy'
require_literal "$target" 'saw_worker'
require_literal "$target" 'resource_monitor_retry()'
require_literal "$target" 'resource_monitor_stats()'
require_literal "$target" 'for attempt in 1 2 3; do'
require_literal "$target" '((attempt == 3)) || sleep 1'
require_literal "$target" 'resource monitor failed: category=%s'
require_literal "$target" 'resource ephemeral identity failed: category=%s'
resource_sample_block="$(sed -n '/^run_resource_sample()/,/^}/p' "$target")"
for literal in \
  'run_resource_child "$resource_browser_status_file"' \
  'run_resource_child "$resource_backup_status_file"' \
  'browser_status="$(read_resource_child_status "$resource_browser_status_file")"' \
  'backup_status="$(read_resource_child_status "$resource_backup_status_file")"' \
  'finalize_backup_proof "$backup_status" || finalize_status=$?' \
  '"$browser_status" "$backup_status" "$monitor_status" "$finalize_status" ||'; do
  grep -Fq -- "$literal" <<<"$resource_sample_block" ||
    fail "resource sample omitted durable child status handling: $literal"
done
if grep -Fq 'run_backup_proof &' <<<"$resource_sample_block"; then
  fail 'resource monitor included post-backup audit in the measured workload'
fi
resource_monitor_line="$(
  grep -n 'monitor_resource_workloads' <<<"$resource_sample_block" |
    cut -d: -f1
)"
resource_browser_wait_line="$(
  grep -n 'wait "$resource_browser_pid"' <<<"$resource_sample_block" |
    cut -d: -f1
)"
resource_backup_wait_line="$(
  grep -n 'wait "$resource_backup_pid"' <<<"$resource_sample_block" |
    cut -d: -f1
)"
resource_browser_status_line="$(
  grep -n 'browser_status="$(read_resource_child_status' \
    <<<"$resource_sample_block" |
    cut -d: -f1
)"
resource_backup_status_line="$(
  grep -n 'backup_status="$(read_resource_child_status' \
    <<<"$resource_sample_block" |
    cut -d: -f1
)"
resource_finalize_line="$(
  grep -n 'finalize_backup_proof "$backup_status"' \
    <<<"$resource_sample_block" |
    cut -d: -f1
)"
resource_merge_line="$(
  grep -n 'merge_resource_statuses' <<<"$resource_sample_block" |
    cut -d: -f1
)"
[[ "$resource_monitor_line" =~ ^[0-9]+$ &&
  "$resource_browser_wait_line" =~ ^[0-9]+$ &&
  "$resource_backup_wait_line" =~ ^[0-9]+$ &&
  "$resource_browser_status_line" =~ ^[0-9]+$ &&
  "$resource_backup_status_line" =~ ^[0-9]+$ &&
  "$resource_finalize_line" =~ ^[0-9]+$ &&
  "$resource_merge_line" =~ ^[0-9]+$ &&
  "$resource_monitor_line" -lt "$resource_browser_wait_line" &&
  "$resource_browser_wait_line" -lt "$resource_backup_wait_line" &&
  "$resource_backup_wait_line" -lt "$resource_browser_status_line" &&
  "$resource_browser_status_line" -lt "$resource_backup_status_line" &&
  "$resource_backup_status_line" -lt "$resource_finalize_line" &&
  "$resource_finalize_line" -lt "$resource_merge_line" ]] ||
  fail 'resource sample did not stop monitoring before backup finalization'
if grep -Fq 'wait "$resource_backup_pid" || backup_status=$?' \
  <<<"$resource_sample_block"; then
  fail 'resource sample depended on a long-retained Bash child status'
fi
if grep -Fq 'pause "$worker"' <<<"$resource_sample_block" ||
  grep -Fq 'unpause "$worker"' <<<"$resource_sample_block"; then
  fail 'resource proof faked worker/backup exclusion by pausing the worker'
fi
resource_monitor_block="$(
  sed -n '/^monitor_resource_workloads()/,/^}/p' "$target"
)"
resource_monitor_stats_source="$(
  sed -n '/^resource_monitor_stats()/,/^}/p' "$target"
)"
[[ -n "$resource_monitor_stats_source" ]] ||
  fail 'resource stats live-roster helper is absent'
backup_activity_source="$(
  sed -n '/^resource_is_backup_activity()/,/^}/p' "$target"
)"
[[ -n "$backup_activity_source" ]] ||
  fail 'backup activity classifier is absent'
run_backup_activity_case() {
  local case_name="$1"
  local container_name="$2"
  local service="$3"
  local oneoff="$4"
  local expected="$5"
  local actual=false
  if (
    eval "$backup_activity_source"
    live_project=happylearn-phase5-live-a1b2c3d4e5f6
    backup=happylearn_phase5_a1b2c3d4e5f6_backup
    resource_is_backup_activity "$container_name" "$service" "$oneoff"
  ); then
    actual=true
  fi
  [[ "$actual" == "$expected" ]] ||
    fail "backup activity mutation $case_name classified as $actual"
}
run_backup_activity_case storage_init \
  /happylearn-phase5-live-a1b2c3d4e5f6-backup-storage-init-run-a1 \
  backup-storage-init True true
run_backup_activity_case secrets_init \
  /happylearn-phase5-live-a1b2c3d4e5f6-backup-secrets-init-run-a1 \
  backup-secrets-init True true
run_backup_activity_case prepare \
  /happylearn-phase5-live-a1b2c3d4e5f6-backup-run-prepare \
  backup True true
run_backup_activity_case finish \
  /happylearn-phase5-live-a1b2c3d4e5f6-backup-run-finish \
  backup True true
run_backup_activity_case named_backup \
  /happylearn_phase5_a1b2c3d4e5f6_backup '' '' true
run_backup_activity_case app \
  /happylearn_phase5_a1b2c3d4e5f6_app app '' false
for literal in \
  'roster_policy=backup_snapshot' \
  'validate_required_resource_roster "$roster_policy"' \
  '[[ "$inspected_name" == "/$browser_runner" ]] &&' \
  'aggregate_container=true' \
  'aggregate_ids+=("$id")' \
  '"${aggregate_ids[@]}"' \
  'if ! resource_monitor_capture resource_state 15 inspect --format' \
  '--filter "id=${id}"' \
  '[[ -z "$current_listing" ]] && continue' \
  'validate_required_resource_roster running ||'; do
  grep -Fq -- "$literal" <<<"$resource_monitor_block" ||
    fail "resource monitor did not tolerate an owned ephemeral exit: $literal"
done
if grep -Fq 'production_ids' <<<"$resource_monitor_block"; then
  fail 'resource monitor retained a browser-excluding aggregate roster'
fi
grep -Fq '"$backup_activity_running" == true' \
  <<<"$resource_monitor_block" ||
  fail 'worker overlap gate still depends on heavy-stage activity only'
for category in \
  required_roster ephemeral_identity listing ownership resource_state \
  invariant command production_stats production_parse browser_stats \
  browser_parse final_roster; do
  grep -Fq -- "resource_monitor_failure $category" \
    <<<"$resource_monitor_block" ||
    fail "resource monitor omitted fixed failure category: $category"
done
resource_ephemeral_block="$(
  sed -n '/^validate_resource_ephemeral_identities()/,/^}/p' "$target"
)"
resource_ephemeral_container_block="$(
  sed -n '/^validate_resource_ephemeral_container()/,/^}/p' "$target"
)"
for label in com.docker.compose.service com.docker.compose.oneoff; do
  grep -Fq -- \
    "{{with index .Config.Labels \"$label\"}}{{.}}{{end}}" \
    <<<"$resource_ephemeral_container_block" ||
    fail "ephemeral identity did not normalize missing label: $label"
done
for category in \
  browser_listing browser_identity backup_listing backup_metadata \
  backup_identity; do
  grep -Fq -- "resource_ephemeral_failure $category" \
    <<<"$resource_ephemeral_block" ||
    fail "ephemeral monitor omitted fixed failure category: $category"
done
require_literal "$target" 'preserve_first_failure'
require_literal "$target" 'audit_container_metadata'
require_literal "$target" '--audit-container-metadata'
require_literal "$target" 'phase5-e2e-secret-marker'
require_literal "$target" 'AWS_SECRET_ACCESS_KEY'
require_literal "$target" 'HAPPYLEARN_DATABASE_URL'
require_literal "$target" \
  '{{json .Config.Env}}|{{json .Config.Entrypoint}}|{{json .Config.Cmd}}'

all_group_block="$(sed -n '/^  all)$/,/^    ;;$/p' "$target")"
desktop_line="$(grep -n 'run_all_desktop' <<<"$all_group_block" | cut -d: -f1)"
reopen_line="$(
  grep -n 'reopen_phase5_browser_alert' <<<"$all_group_block" | cut -d: -f1
)"
mobile_line="$(grep -n 'run_all_mobile' <<<"$all_group_block" | cut -d: -f1)"
[[ "$desktop_line" =~ ^[0-9]+$ &&
  "$reopen_line" =~ ^[0-9]+$ &&
  "$mobile_line" =~ ^[0-9]+$ &&
  "$desktop_line" -lt "$reopen_line" &&
  "$reopen_line" -lt "$mobile_line" ]] ||
  fail 'all group did not restore its deterministic alert between viewports'

require_literal "$target" 'trap early_cleanup EXIT'
require_literal "$target" 'trap cleanup EXIT'
require_literal "$target" "trap 'handle_harness_signal 129' HUP"
require_literal "$target" "trap 'handle_harness_signal 130' INT"
require_literal "$target" "trap 'handle_harness_signal 143' TERM"
require_literal "$target" '--signal-contract-probe'
require_literal "$target" 'remove_active_temporary_containers'
require_literal "$target" 'cancel_bounded_command'
require_literal "$target" 'sanitize-e2e-artifacts.sh'
require_literal "$target" 'publish-e2e-diagnostics.sh'
require_literal "$target" 'allowed_artifact_root="$repo_root/test-results"'
require_literal "$target" 'artifact_input="${E2E_ARTIFACT_DIR:-$allowed_artifact_root/phase5}"'
require_literal "$target" 'initialize_artifact_directory'
require_literal "$target" 'rm -f "$artifact_dir/resource-samples.tsv"'
require_literal "$target" 'rm -f "$artifact_dir/backup-id"'
require_literal "$target" 'init-e2e-artifacts.sh'
require_literal "$target" 'PHASE5_ARTIFACT_WRITE_PROBE'
require_literal "$target" '--user 1000:1000 --cap-drop ALL'
cleanup_block="$(sed -n '/^cleanup()/,/^}/p' "$target")"
if grep -Fq -- 'docker_bounded 30 rm -f "${temporary_containers[@]}"' "$target" ||
  grep -Fq -- 'docker_bounded 30 rm -f "${service_containers[@]}"' "$target" ||
  grep -Fq -- \
    '--filter "label=com.docker.compose.project=${live_project}"' \
    <<<"$cleanup_block"; then
  fail 'cleanup retained name-only or project-wide container deletion'
fi
require_literal "$target" 'docker_bounded 30 network rm "$expected_id"'
require_literal "$target" 'owned_volume_ledger="$tmpdir/owned-volumes.tsv"'
require_literal "$target" 'owned_image_ledger="$tmpdir/owned-images.tsv"'
require_literal "$target" \
  '"$reference" "$id" "$fixture_suffix" >>"$owned_image_ledger"'
require_literal "$target" \
  'docker_bounded 30 volume rm "$name"'
require_literal "$target" \
  'docker_bounded 60 image rm "$expected_id"'
require_literal "$target" \
  '--label "io.happylearn.phase5.e2e-owner=${fixture_suffix}"'

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/phase5-e2e-contract.XXXXXX")"
signal_target_pid=''
contract_cleanup() {
  if [[ "$signal_target_pid" =~ ^[1-9][0-9]*$ ]]; then
    kill -TERM "$signal_target_pid" 2>/dev/null || true
    wait "$signal_target_pid" 2>/dev/null || true
  fi
  rm -rf "$tmpdir"
}
trap contract_cleanup EXIT

contract_file_mode() {
  if stat -c '%a' "$1" >/dev/null 2>&1; then
    stat -c '%a' "$1"
  else
    stat -f '%Lp' "$1"
  fi
}

contract_file_owner() {
  if stat -c '%u' "$1" >/dev/null 2>&1; then
    stat -c '%u' "$1"
  else
    stat -f '%u' "$1"
  fi
}

contract_file_group() {
  if stat -c '%g' "$1" >/dev/null 2>&1; then
    stat -c '%g' "$1"
  else
    stat -f '%g' "$1"
  fi
}

assert_remote_s3_identity_resolution() {
  local candidate="$1"
  local identity_source uid
  identity_source="$(
    sed -n '/^resolve_remote_s3_identity()/,/^}/p' "$candidate"
  )"
  [[ -n "$identity_source" ]] || return 1
  for uid in 1001 501; do
    (
      id() {
        case "$1" in
          -u) printf '%s\n' "$uid" ;;
          -g) printf '127\n' ;;
          *) return 2 ;;
        esac
      }
      eval "$identity_source"
      remote_s3_uid=''
      remote_s3_owner=''
      resolve_remote_s3_identity
      [[ "$remote_s3_uid" == "$uid" &&
        "$remote_s3_owner" == "$uid:0" ]]
    ) || return 1
  done
  if (
    id() {
      case "$1" in
        -u) printf '0\n' ;;
        -g) printf '127\n' ;;
        *) return 2 ;;
      esac
    }
    eval "$identity_source"
    remote_s3_uid=''
    remote_s3_owner=''
    resolve_remote_s3_identity
  ); then
    return 1
  fi
}

assert_remote_s3_identity_wiring() {
  local candidate="$1"
  local literal
  for literal in \
    'phase5-data-init "$remote_s3_owner"' \
    'chown "$remote_owner" /remote' \
    'phase5-secret-init "$remote_s3_owner"' \
    'install_consumer remote-s3 "$remote_owner" 0400 restic-remote-access-key restic-remote-secret-key' \
    'secret_probe_remote_s3|${remote_s3_owner}|remote-s3|restic-remote-access-key|${remote_s3_owner}|400' \
    'type=volume,src=$secret_volume,dst=/run/phase5-secrets,volume-subpath=remote-s3,readonly' \
    'MINIO_ROOT_USER="$(cat /run/phase5-secrets/restic-remote-access-key)"' \
    'resolve_remote_s3_identity || {' \
    '--user "$remote_s3_owner" --cap-drop ALL' \
    'uid=${remote_s3_uid},gid=0,mode=0700'; do
    grep -Fq -- "$literal" "$candidate" || return 1
  done
}

assert_remote_s3_fixture_permissions() {
  local candidate="$1"
  local label="$2"
  local fixture_source fixture_root expected_uid expected_gid path
  fixture_source="$(sed -n '/^create_fixture_ca()/,/^}/p' "$candidate")"
  [[ -n "$fixture_source" ]] || return 1
  fixture_root="$tmpdir/remote-s3-fixture-$label"
  mkdir -p "$fixture_root/offline" "$fixture_root/ca-context" \
    "$fixture_root/remote-certs/CAs"
  chmod 0700 "$fixture_root" "$fixture_root/offline" \
    "$fixture_root/ca-context" "$fixture_root/remote-certs" \
    "$fixture_root/remote-certs/CAs"
  (
    umask 077
    case "$(uname -s)" in
      MINGW*|MSYS*) export MSYS2_ARG_CONV_EXCL='/CN=' ;;
    esac
    offline_dir="$fixture_root/offline"
    ca_context_dir="$fixture_root/ca-context"
    remote_cert_dir="$fixture_root/remote-certs"
    fixture_suffix=a1b2c3d4e5f6
    eval "$fixture_source"
    create_fixture_ca
  ) || return 1
  expected_uid="$(id -u)"
  expected_gid="$(id -g)"
  case "$(uname -s)" in
    MINGW*|MSYS*)
      grep -Fq 'chmod 0400 "$remote_cert_dir/private.key"' \
        <<<"$fixture_source" || return 1
      ;;
    *)
      [[ "$(contract_file_mode "$fixture_root/remote-certs")" == 700 &&
        "$(contract_file_mode "$fixture_root/remote-certs/CAs")" == 700 &&
        "$(contract_file_mode "$fixture_root/remote-certs/private.key")" == 400 &&
        "$(contract_file_mode "$fixture_root/remote-certs/public.crt")" == 600 &&
        "$(contract_file_mode "$fixture_root/remote-certs/CAs/ca.crt")" == 444 ]] ||
        return 1
      ;;
  esac
  for path in \
    "$fixture_root/remote-certs" \
    "$fixture_root/remote-certs/CAs" \
    "$fixture_root/remote-certs/private.key" \
    "$fixture_root/remote-certs/public.crt" \
    "$fixture_root/remote-certs/CAs/ca.crt"; do
    [[ "$(contract_file_owner "$path")" == "$expected_uid" &&
      "$(contract_file_group "$path")" == "$expected_gid" ]] || return 1
  done
}

replace_once() {
  local source="$1"
  local destination="$2"
  local needle="$3"
  local replacement="$4"
  awk -v needle="$needle" -v replacement="$replacement" '
    !replaced {
      position = index($0, needle)
      if (position > 0) {
        $0 = substr($0, 1, position - 1) replacement \
          substr($0, position + length(needle))
        replaced = 1
      }
    }
    { print }
    END { if (!replaced) exit 3 }
  ' "$source" >"$destination"
}

assert_remote_s3_identity_resolution "$target" ||
  fail 'remote S3 did not resolve to the non-root host fixture owner'
assert_remote_s3_identity_wiring "$target" ||
  fail 'remote S3 did not use one owner for bind mounts, volumes, and secrets'
assert_remote_s3_fixture_permissions "$target" safe ||
  fail 'remote S3 fixture permissions exposed its private key'

identity_mutant="$tmpdir/e2e-phase5-fixed-remote-owner.sh"
replace_once "$target" "$identity_mutant" \
  'remote_s3_owner="${remote_s3_uid}:0"' \
  'remote_s3_owner="1000:0"' ||
  fail 'remote S3 owner mutation could not be constructed'
if assert_remote_s3_identity_resolution "$identity_mutant"; then
  fail 'remote S3 identity contract accepted a fixed container owner'
fi

runtime_mutant="$tmpdir/e2e-phase5-fixed-remote-runtime-user.sh"
replace_once "$target" "$runtime_mutant" \
  '--user "$remote_s3_owner" --cap-drop ALL' \
  '--user 1000:0 --cap-drop ALL' ||
  fail 'remote S3 runtime-user mutation could not be constructed'
if assert_remote_s3_identity_wiring "$runtime_mutant"; then
  fail 'remote S3 identity contract accepted a mismatched runtime user'
fi

data_mutant="$tmpdir/e2e-phase5-fixed-remote-data-owner.sh"
replace_once "$target" "$data_mutant" \
  'chown "$remote_owner" /remote' 'chown 1000:0 /remote' ||
  fail 'remote S3 data-owner mutation could not be constructed'
if assert_remote_s3_identity_wiring "$data_mutant"; then
  fail 'remote S3 identity contract accepted mismatched data ownership'
fi

secret_mutant="$tmpdir/e2e-phase5-fixed-remote-secret-owner.sh"
replace_once "$target" "$secret_mutant" \
  'install_consumer remote-s3 "$remote_owner" 0400 restic-remote-access-key restic-remote-secret-key' \
  'install_consumer remote-s3 1000:0 0400 restic-remote-access-key restic-remote-secret-key' ||
  fail 'remote S3 secret-owner mutation could not be constructed'
if assert_remote_s3_identity_wiring "$secret_mutant"; then
  fail 'remote S3 identity contract accepted mismatched secret ownership'
fi

key_mutant="$tmpdir/e2e-phase5-permissive-remote-key.sh"
replace_once "$target" "$key_mutant" \
  'chmod 0400 "$remote_cert_dir/private.key"' \
  'chmod 0444 "$remote_cert_dir/private.key"' ||
  fail 'remote S3 private-key mutation could not be constructed'
if assert_remote_s3_fixture_permissions "$key_mutant" permissive-key; then
  fail 'remote S3 fixture contract accepted a world-readable private key'
fi

if [[ "${HAPPYLEARN_PHASE5_REMOTE_S3_CONTRACT_ONLY:-}" == 1 ]]; then
  printf 'phase 5 remote S3 identity contract: PASS\n'
  exit 0
fi

(
  eval "$resource_monitor_stats_source"
  stale_id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  core_id=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  stats_calls="$tmpdir/resource-stats-live-roster.calls"
  install -m 0600 /dev/null "$stats_calls"
  sleep() { :; }
  docker_capture_bounded() {
    local output_variable="$1" deadline="$2"
    shift 2
    [[ "$deadline" =~ ^[1-9][0-9]*$ ]] || return 97
    case "$1 $2" in
      'container ls')
        case "$*" in
          *"id=${core_id}"*) printf -v "$output_variable" '%s' "$core_id" ;;
          *"id=${stale_id}"*) printf -v "$output_variable" '%s' '' ;;
          *) return 96 ;;
        esac
        ;;
      'stats --no-stream')
        printf '%s\n' "$*" >>"$stats_calls"
        [[ "$*" == *"$core_id"* && "$*" != *"$stale_id"* ]] || return 95
        printf -v "$output_variable" '%s' '1.00%|64MiB / 1GiB'
        ;;
      *) return 94 ;;
    esac
  }
  resource_monitor_stats observed 30 "$core_id" "$stale_id" ||
    fail 'resource stats rejected a container that exited after enumeration'
  [[ "$observed" == '1.00%|64MiB / 1GiB' ]] ||
    fail 'resource stats did not preserve the live core sample'
  [[ "$(wc -l <"$stats_calls" | tr -d ' ')" == 1 ]] ||
    fail 'resource stats retried an already stale aggregate roster'
)

backup_finalize_source="$(
  sed -n '/^finalize_backup_proof()/,/^}/p' "$target"
)"
backup_finalization_failure_source="$(
  sed -n '/^backup_finalization_failure()/,/^}/p' "$target"
)"
run_backup_finalize_case() {
  local workflow_status="$1"
  local audit_status="$2"
  local write_status="$3"
  local expected_status="$4"
  local expected_events="$5"
  local expected_category="$6"
  (
    eval "$backup_finalization_failure_source"
    eval "$backup_finalize_source"
    finalize_stderr="$(mktemp "$tmpdir/backup-finalizer.XXXXXX")"
    finalize_events=''
    append_finalize_event() {
      if [[ -n "$finalize_events" ]]; then
        finalize_events+=$'\n'
      fi
      finalize_events+="$1"
    }
    remove_coordinator_one_shots() {
      [[ "$1" == audit ]] || return 99
      append_finalize_event audit
      return "$audit_status"
    }
    write_recovery_backup_id() {
      append_finalize_event write
      return "$write_status"
    }
    actual_status=0
    finalize_backup_proof "$workflow_status" 2>"$finalize_stderr" ||
      actual_status=$?
    [[ "$actual_status" -eq "$expected_status" ]] ||
      fail "backup finalizer returned $actual_status, expected $expected_status"
    [[ "$finalize_events" == "$expected_events" ]] ||
      fail "backup finalizer events were not lifecycle-safe: $finalize_events"
    if [[ -z "$expected_category" ]]; then
      [[ ! -s "$finalize_stderr" ]] ||
        fail 'backup finalizer emitted an unexpected diagnostic'
    else
      [[ "$(wc -l <"$finalize_stderr" | tr -d '[:space:]')" == 1 &&
        "$(<"$finalize_stderr")" == \
          "backup finalization failed: category=${expected_category}" ]] ||
        fail 'backup finalizer diagnostic was not fixed and safe'
    fi
  )
}

run_backup_finalize_case 0 0 0 0 $'audit\nwrite' ''
run_backup_finalize_case 23 0 0 23 'audit' ''
run_backup_finalize_case 0 41 0 41 'audit' one_shot_audit
run_backup_finalize_case 23 41 0 23 'audit' one_shot_audit
run_backup_finalize_case 0 0 42 42 $'audit\nwrite' recovery_evidence
run_backup_finalize_case 256 0 0 2 '' ''

write_recovery_backup_id_source="$(
  sed -n '/^write_recovery_backup_id()/,/^}/p' "$target"
)"
read_coordinator_run_id_source="$(
  sed -n '/^read_coordinator_run_id()/,/^}/p' "$target"
)"
recovery_evidence_failure_source="$(
  sed -n '/^recovery_evidence_failure()/,/^}/p' "$target"
)"
run_recovery_evidence_case() {
  local case_name="${1:?recovery evidence case required}"
  local expected_status="${2:?expected status required}"
  local expect_artifact="${3:?artifact expectation required}"
  local expected_category="${4:-}"
  (
    eval "$recovery_evidence_failure_source"
    eval "$read_coordinator_run_id_source"
    eval "$write_recovery_backup_id_source"
    evidence_root="$tmpdir/recovery-evidence-$case_name"
    mkdir -m 0700 "$evidence_root"
    tmpdir="$evidence_root"
    artifact_dir="$evidence_root/artifacts"
    mkdir -m 0700 "$artifact_dir"
    E2E_ARTIFACT_DIR="$artifact_dir"
    coordinator_run_id_file="$evidence_root/coordinator-run-id"
    postgres=phase5-postgres
    evidence_uuid=11111111-1111-4111-8111-111111111111
    stale_uuid=22222222-2222-4222-8222-222222222222
    evidence_snapshot=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    printf '%s\n' "$stale_uuid" >"$artifact_dir/backup-id"
    chmod 0600 "$artifact_dir/backup-id"
    recovery_backup_id=''
    case "$case_name" in
      handoff_missing)
        ;;
      handoff_symlink)
        printf '%s\n' "$evidence_uuid" \
          >"$evidence_root/coordinator-run-id.target"
        chmod 0600 "$evidence_root/coordinator-run-id.target"
        ln -s "$evidence_root/coordinator-run-id.target" \
          "$coordinator_run_id_file"
        ;;
      handoff_mode)
        printf '%s\n' "$evidence_uuid" >"$coordinator_run_id_file"
        chmod 0644 "$coordinator_run_id_file"
        ;;
      handoff_nonempty)
        printf '%s\n%s\n' "$evidence_uuid" extra \
          >"$coordinator_run_id_file"
        chmod 0600 "$coordinator_run_id_file"
        ;;
      *)
        printf '%s\n' "$evidence_uuid" >"$coordinator_run_id_file"
        chmod 0600 "$coordinator_run_id_file"
        ;;
    esac
    portable_file_mode() {
      if stat -f '%Lp' "$1" >/dev/null 2>&1; then
        stat -f '%Lp' "$1"
      else
        stat -c '%a' "$1"
      fi
    }
    portable_file_owner() {
      if stat -f '%u' "$1" >/dev/null 2>&1; then
        stat -f '%u' "$1"
      else
        stat -c '%u' "$1"
      fi
    }
    portable_file_size() {
      if stat -f '%z' "$1" >/dev/null 2>&1; then
        stat -f '%z' "$1"
      else
        stat -c '%s' "$1"
      fi
    }
    docker_bounded() {
      printf '%s\n' 'RAW_DAEMON_DETAIL recovery evidence' >&2
      [[ "$*" == *"WHERE id='${evidence_uuid}'::uuid"* &&
        "$*" != *"idempotency_key LIKE 'host-%'"* ]] ||
        return 44
      if [[ "$case_name" == docker_failure ]]; then
        return 42
      fi
      if [[ "$case_name" == invalid_evidence ]]; then
        printf '%s\n' invalid
      else
        printf '%s|%s|%s\n' \
          "$evidence_uuid" "$evidence_snapshot" "$evidence_snapshot"
      fi
    }
    install() {
      local destination="${!#}"
      if [[ "$case_name" == install_failure &&
        "$destination" == "$E2E_ARTIFACT_DIR"/.backup-id.?????? ]]; then
        printf '%s\n' 'RAW_INSTALL_DETAIL recovery evidence' >&2
        return 43
      fi
      command install "$@"
    }
    mv() {
      if [[ "$case_name" == move_failure ]]; then
        printf '%s\n' 'RAW_MOVE_DETAIL recovery evidence' >&2
        return 45
      fi
      command mv "$@"
    }
    if [[ "$case_name" == write_failure ]]; then
      tmpdir="$evidence_root/missing"
    fi
    unrelated_install="$evidence_root/unrelated-install"
    install -m 0600 /dev/null "$unrelated_install" ||
      fail "recovery evidence $case_name intercepted an unrelated install"
    rm -f "$unrelated_install"
    evidence_stderr="$evidence_root/stderr"
    actual_status=0
    write_recovery_backup_id 2>"$evidence_stderr" || actual_status=$?
    [[ "$actual_status" -eq "$expected_status" ]] ||
      fail "recovery evidence $case_name returned $actual_status, expected $expected_status"
    if [[ -z "$expected_category" ]]; then
      [[ ! -s "$evidence_stderr" ]] ||
        fail "recovery evidence $case_name exposed raw failure detail"
    else
      [[ "$(wc -l <"$evidence_stderr" | tr -d '[:space:]')" == 1 &&
        "$(<"$evidence_stderr")" == \
          "recovery evidence failed: category=${expected_category}" ]] ||
        fail "recovery evidence $case_name did not emit one fixed category"
    fi
    if [[ "$expect_artifact" == yes ]]; then
      [[ "$(<"$artifact_dir/backup-id")" == "$evidence_uuid" ]] ||
        fail "recovery evidence $case_name omitted the validated backup ID"
      [[ "$recovery_backup_id" == "$evidence_uuid" ]] ||
        fail "recovery evidence $case_name did not bind the current run"
    else
      [[ ! -e "$artifact_dir/backup-id" ]] ||
        fail "recovery evidence $case_name retained stale evidence"
      [[ -z "$recovery_backup_id" ]] ||
        fail "recovery evidence $case_name bound a failed run"
    fi
  )
}

run_recovery_evidence_case success 0 yes
run_recovery_evidence_case handoff_missing 1 no handoff
run_recovery_evidence_case handoff_symlink 1 no handoff
run_recovery_evidence_case handoff_mode 1 no handoff
run_recovery_evidence_case handoff_nonempty 1 no handoff
run_recovery_evidence_case docker_failure 42 no query
run_recovery_evidence_case invalid_evidence 1 no invalid
run_recovery_evidence_case write_failure 1 no write
run_recovery_evidence_case install_failure 43 no publish
run_recovery_evidence_case move_failure 45 no publish

resource_sample_source="$(
  sed -n '/^run_resource_sample()/,/^}/p' "$target"
)"
run_resource_sample_lifecycle_case() {
  local case_name="$1"
  local browser_result="$2"
  local backup_result="$3"
  local monitor_result="$4"
  local finalizer_result="$5"
  local evidence_result="$6"
  local expected_status="$7"
  local expected_events="$8"
  (
    eval "$resource_sample_source"
    case_root="$tmpdir/resource-lifecycle-$case_name"
    artifact_dir="$case_root/artifacts"
    mkdir -m 0700 "$case_root" "$artifact_dir"
    tmpdir="$case_root"
    resource_browser_status_file="$case_root/browser.status"
    resource_backup_status_file="$case_root/backup.status"
    resource_browser_pid=''
    resource_backup_pid=''
    lifecycle_events="$case_root/events"
    browser_done="$case_root/browser.done"
    backup_done="$case_root/backup.done"
    monitor_active=true
    install -m 0600 /dev/null "$lifecycle_events"
    append_lifecycle_event() {
      printf '%s\n' "$1" >>"$lifecycle_events"
    }
    run_resource_child() {
      local status_file="$1"
      local child_status=0
      shift
      "$@" || child_status=$?
      printf '%s\n' "$child_status" >"$status_file"
    }
    run_resource_browser_load() {
      install -m 0600 /dev/null "$browser_done"
      return "$browser_result"
    }
    run_backup_workflow() {
      install -m 0600 /dev/null "$backup_done"
      return "$backup_result"
    }
    monitor_resource_workloads() {
      local browser_pid="$1"
      local backup_pid="$2"
      local evidence="$3"
      local attempt
      [[ "$browser_pid" =~ ^[1-9][0-9]*$ &&
        "$backup_pid" =~ ^[1-9][0-9]*$ ]] ||
        return 97
      for attempt in {1..200}; do
        if [[ -s "$resource_browser_status_file" &&
          -s "$resource_backup_status_file" ]]; then
          break
        fi
        sleep 0.01
      done
      [[ -e "$browser_done" && -e "$backup_done" &&
        -s "$resource_browser_status_file" &&
        -s "$resource_backup_status_file" ]] ||
        return 96
      append_lifecycle_event monitor
      printf 'resource-evidence\n' >"$evidence"
      monitor_active=false
      return "$monitor_result"
    }
    read_resource_child_status() {
      local status_file="$1"
      local value
      case "$status_file" in
        "$resource_browser_status_file")
          append_lifecycle_event read_browser
          ;;
        "$resource_backup_status_file")
          append_lifecycle_event read_backup
          ;;
        *) return 95 ;;
      esac
      value="$(<"$status_file")"
      [[ "$value" =~ ^([0-9]|[1-9][0-9]{1,2})$ &&
        "$value" -le 255 ]] ||
        return 94
      printf '%s\n' "$value"
    }
    finalize_backup_proof() {
      local observed_backup_status="$1"
      [[ "$monitor_active" == false &&
        -e "$browser_done" &&
        -e "$backup_done" &&
        "$observed_backup_status" -eq "$backup_result" ]] ||
        return 93
      append_lifecycle_event finalize
      return "$finalizer_result"
    }
    merge_resource_statuses() {
      local merged=0 candidate
      [[ "$#" -eq 4 ]] || return 92
      append_lifecycle_event \
        "merge:$1,$2,$3,$4"
      for candidate in "$@"; do
        if ((merged == 0 && candidate != 0)); then
          merged="$candidate"
        fi
      done
      return "$merged"
    }
    validate_resource_evidence() {
      [[ "$1" == "$case_root/resource-evidence" ]] || return 91
      append_lifecycle_event validate
      return "$evidence_result"
    }
    actual_status=0
    run_resource_sample 1 2>"$case_root/stderr" || actual_status=$?
    [[ "$actual_status" -eq "$expected_status" ]] ||
      fail "resource lifecycle $case_name returned $actual_status, expected $expected_status"
    actual_events="$(<"$lifecycle_events")"
    [[ "$actual_events" == "$expected_events" ]] ||
      fail "resource lifecycle $case_name events were unsafe: $actual_events"
    if ((expected_status == 0)); then
      [[ -f "$artifact_dir/resource-samples.tsv" ]] ||
        fail "resource lifecycle $case_name omitted the validated report"
    else
      [[ ! -e "$artifact_dir/resource-samples.tsv" ]] ||
        fail "resource lifecycle $case_name installed an invalid report"
    fi
  )
}

run_resource_sample_lifecycle_case \
  success 0 0 0 0 0 0 \
  $'monitor\nread_browser\nread_backup\nfinalize\nmerge:0,0,0,0\nvalidate'
run_resource_sample_lifecycle_case \
  browser_priority 5 6 7 8 0 5 \
  $'monitor\nread_browser\nread_backup\nfinalize\nmerge:5,6,7,8'
run_resource_sample_lifecycle_case \
  finalizer_failure 0 0 0 8 0 8 \
  $'monitor\nread_browser\nread_backup\nfinalize\nmerge:0,0,0,8'
run_resource_sample_lifecycle_case \
  evidence_failure 0 0 0 0 9 9 \
  $'monitor\nread_browser\nread_backup\nfinalize\nmerge:0,0,0,0\nvalidate'

quiesce_source="$(
  sed -n '/^quiesce_restore_access_containers()/,/^}/p' "$target"
)"
restore_resource_absence_source="$(
  sed -n '/^verify_restore_resources_absent()/,/^}/p' "$target"
)"
for absence_label in \
  'label=io.happylearn.phase5.restore-access-backup-id=${backup_id}' \
  'label=io.happylearn.phase5.restore-backup-id=${backup_id}'; do
  grep -Fq -- "$absence_label" <<<"$restore_resource_absence_source" ||
    fail "restore resource absence check lost label: $absence_label"
done
run_quiesce_state_case() {
  local mode="${1:?quiesce mode required}"
  local expected_status="${2:?expected status required}"
  local expected_events="${3:?expected events required}"
  (
    eval "$quiesce_source"
    eval "$restore_resource_absence_source"
    live_project=happylearn-phase5-live-a1b2c3d4e5f6
    fixture_suffix=a1b2c3d4e5f6
    restore_controller=restore-controller
    restore_repository_handoff=restore-pre
    state_controller=true
    state_pre=false
    foreign_controller_name=false
    reappeared_controller_access=false
    controller_current_name="$restore_controller"
    pre_current_name="$restore_repository_handoff"
    if [[ "$mode" == success ]]; then
      state_pre=true
    elif [[ "$mode" == foreign_name_masks_label ]]; then
      foreign_controller_name=true
    elif [[ "$mode" == nested_resources ]]; then
      state_controller=false
    fi
    events=''
    append_quiesce_event() {
      if [[ -n "$events" ]]; then
        events+=$'\n'
      fi
      events+="$1"
    }
    container_id_for_name() {
      case "$1" in
        "$restore_controller")
          printf '%064d\n' 0 | tr 0 a
          ;;
        "$restore_repository_handoff")
          printf '%064d\n' 0 | tr 0 b
          ;;
        *) return 1 ;;
      esac
    }
    container_name_for_id() {
      case "$1" in
        aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa)
          printf '%s\n' "$restore_controller"
          ;;
        bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb)
          printf '%s\n' "$restore_repository_handoff"
          ;;
        *) return 1 ;;
      esac
    }
    container_kind_for_name() {
      case "$1" in
        "$restore_controller") printf 'controller\n' ;;
        "$restore_repository_handoff") printf 'repository-prepare\n' ;;
        *) return 1 ;;
      esac
    }
    container_current_name_for_name() {
      case "$1" in
        "$restore_controller") printf '%s\n' "$controller_current_name" ;;
        "$restore_repository_handoff") printf '%s\n' "$pre_current_name" ;;
        *) return 1 ;;
      esac
    }
    container_state_for_name() {
      case "$1" in
        "$restore_controller") printf '%s\n' "$state_controller" ;;
        "$restore_repository_handoff") printf '%s\n' "$state_pre" ;;
        *) return 1 ;;
      esac
    }
    mark_container_absent() {
      case "$1" in
        "$restore_controller") state_controller=false ;;
        "$restore_repository_handoff") state_pre=false ;;
        *) return 1 ;;
      esac
    }
    docker_capture_bounded() {
      local output_variable="${1:?output variable required}"
      local seconds="${2:?seconds required}"
      local argument='' last='' name='' kind='' state='' value=''
      local owner="$fixture_suffix" project="$live_project"
      local running=true access_filter=false nested_filter=false
      shift 2
      [[ "$seconds" == 15 ]] || return 99
      for argument in "$@"; do
        last="$argument"
        case "$argument" in
          label=io.happylearn.phase5.restore-access-backup-id=*)
            access_filter=true
            ;;
          label=io.happylearn.phase5.restore-backup-id=*)
            nested_filter=true
            ;;
          label=io.happylearn.phase5.restore-access-kind=*)
            kind="${argument##*=}"
            ;;
        esac
      done
      if [[ "${1:-}" == container && "${2:-}" == ls ]]; then
        if [[ "$last" == id=* ]]; then
          value="${last#id=}"
          name="$(container_name_for_id "$value")" || return 99
          state="$(container_state_for_name "$name")"
          append_quiesce_event \
            "id:${name}:$([[ "$state" == true ]] && printf present || printf absent)"
          if [[ "$state" != true ]]; then
            value=''
          fi
          printf -v "$output_variable" '%s' "$value"
          return 0
        fi
        if [[ "$access_filter" == true && -n "$kind" ]]; then
          case "$kind" in
            controller) name="$restore_controller" ;;
            repository-prepare) name="$restore_repository_handoff" ;;
            *) return 99 ;;
          esac
          state="$(container_state_for_name "$name")"
          if [[ "$name" == "$restore_controller" &&
            "$reappeared_controller_access" == true ]]; then
            value=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
            append_quiesce_event "label:${name}:reappeared"
          elif [[ "$state" == true ]]; then
            value="$(container_id_for_name "$name")"
            append_quiesce_event "label:${name}:present"
            if [[ "$mode" == renamed_before_inspect &&
              "$name" == "$restore_controller" ]]; then
              controller_current_name=restore-controller-renamed
            fi
          else
            value=''
            append_quiesce_event "label:${name}:absent"
          fi
          printf -v "$output_variable" '%s' "$value"
          return 0
        fi
        if [[ "$access_filter" == true ]]; then
          if [[ "$state_controller" == true || "$state_pre" == true ]]; then
            value=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
            append_quiesce_event resources:access:present
          else
            value=''
            append_quiesce_event resources:access:absent
          fi
          printf -v "$output_variable" '%s' "$value"
          return 0
        fi
        if [[ "$nested_filter" == true ]]; then
          if [[ "$mode" == nested_resources ]]; then
            value=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
            append_quiesce_event resources:container:present
          else
            append_quiesce_event resources:container:absent
          fi
          printf -v "$output_variable" '%s' "$value"
          return 0
        fi
        for name in \
          "$restore_controller" "$restore_repository_handoff"; do
          if [[ "$last" == "name=^/${name}\$" ]]; then
            break
          fi
        done
        [[ "$last" == "name=^/${name}\$" ]] || return 99
        state="$(container_state_for_name "$name")"
        if [[ "$name" == "$restore_controller" &&
          "$foreign_controller_name" == true ]]; then
          value=ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
          append_quiesce_event "name:${name}:foreign"
        elif [[ "$state" == true &&
          "$(container_current_name_for_name "$name")" == "$name" ]]; then
          value="$(container_id_for_name "$name")"
          append_quiesce_event "name:${name}:present"
        else
          value=''
          append_quiesce_event "name:${name}:absent"
        fi
        printf -v "$output_variable" '%s' "$value"
        return 0
      fi
      if [[ "${1:-}" == volume && "${2:-}" == ls &&
        "$last" == \
        "label=io.happylearn.phase5.restore-backup-id=11111111-1111-4111-8111-111111111111" ]]; then
        append_quiesce_event resources:volume:absent
        printf -v "$output_variable" '%s' ''
        return 0
      fi
      if [[ "${1:-}" == network && "${2:-}" == ls &&
        "$last" == \
        "label=io.happylearn.phase5.restore-backup-id=11111111-1111-4111-8111-111111111111" ]]; then
        append_quiesce_event resources:network:absent
        printf -v "$output_variable" '%s' ''
        return 0
      fi
      if [[ "${1:-}" == inspect ]]; then
        name="$(container_name_for_id "$last")" || return 99
        if [[ "$mode" == disappears_before_inspect ||
          "$mode" == disappears_foreign_name_takeover ||
          "$mode" == disappears_access_label_reappears ]]; then
          mark_container_absent "$name"
          if [[ "$mode" == disappears_foreign_name_takeover ]]; then
            foreign_controller_name=true
          elif [[ "$mode" == disappears_access_label_reappears ]]; then
            reappeared_controller_access=true
          fi
          append_quiesce_event "inspect:${name}:gone"
          return 1
        fi
        if [[ "$mode" == metadata_collision ]]; then
          owner=external-owner
        elif [[ "$mode" == rm_failure ]]; then
          running=false
        fi
        kind="$(container_kind_for_name "$name")"
        append_quiesce_event "inspect:${name}"
        printf -v "$output_variable" '%s' \
          "${last}|/$(container_current_name_for_name "$name")|${project}|${owner}|11111111-1111-4111-8111-111111111111|${kind}|${running}"
        return 0
      fi
      return 99
    }
    docker_bounded() {
      local id name
      if [[ "$#" == 5 && "$1" == 330 && "$2" == stop &&
        "$3" == --time && "$4" == 300 ]]; then
        id="$5"
        name="$(container_name_for_id "$id")" || return 99
        append_quiesce_event "stop:${name}"
        if [[ "$mode" == renamed_after_stop ]]; then
          controller_current_name=restore-controller-renamed
          return 0
        fi
        [[ "$mode" != stop_failure ]] || return 1
        if [[ "$mode" != still_visible ]]; then
          mark_container_absent "$name"
        fi
        return 0
      fi
      [[ "$#" == 4 && "$1" == 30 && "$2" == rm && "$3" == -f ]] ||
        return 99
      id="$4"
      name="$(container_name_for_id "$id")" || return 99
      append_quiesce_event "rm:${name}"
      [[ "$mode" != rm_failure &&
        "$mode" != stop_failure ]] ||
        return 1
      if [[ "$mode" != still_visible ]]; then
        mark_container_absent "$name"
      fi
    }
    docker() { return 98; }
    if quiesce_restore_access_containers \
      11111111-1111-4111-8111-111111111111; then
      actual_status=0
    else
      actual_status=$?
    fi
    [[ "$actual_status" == "$expected_status" &&
      "$events" == "$expected_events" ]]
    if [[ "$mode" == success ||
      "$mode" == disappears_before_inspect ||
      "$mode" == renamed_before_inspect ||
      "$mode" == renamed_after_stop ]]; then
      [[ "$state_controller" == false &&
        "$state_pre" == false ]]
    elif [[ "$mode" == foreign_name_masks_label ]]; then
      [[ "$state_controller" == false &&
        "$foreign_controller_name" == true ]]
    elif [[ "$mode" == disappears_foreign_name_takeover ]]; then
      [[ "$state_controller" == false &&
        "$foreign_controller_name" == true ]]
    elif [[ "$mode" == disappears_access_label_reappears ]]; then
      [[ "$state_controller" == false &&
        "$reappeared_controller_access" == true ]]
    elif [[ "$mode" != nested_resources ]]; then
      [[ "$state_controller" == true ]]
    else
      [[ "$state_controller" == false ]]
    fi
  ) || fail "restore access quiesce state machine failed for $mode"
}

run_quiesce_state_case success 0 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller\nstop:restore-controller\nid:restore-controller:absent\nname:restore-controller:absent\nlabel:restore-controller:absent\nname:restore-pre:present\nlabel:restore-pre:present\ninspect:restore-pre\nstop:restore-pre\nid:restore-pre:absent\nname:restore-pre:absent\nlabel:restore-pre:absent\nresources:container:absent\nresources:volume:absent\nresources:network:absent\nresources:access:absent'
run_quiesce_state_case metadata_collision 1 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller'
run_quiesce_state_case rm_failure 1 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller\nid:restore-controller:present\nrm:restore-controller'
run_quiesce_state_case stop_failure 1 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller\nstop:restore-controller\nid:restore-controller:present\nrm:restore-controller'
run_quiesce_state_case still_visible 1 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller\nstop:restore-controller\nid:restore-controller:present\nrm:restore-controller\nid:restore-controller:present'
run_quiesce_state_case disappears_before_inspect 0 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller:gone\nid:restore-controller:absent\nname:restore-controller:absent\nlabel:restore-controller:absent\nname:restore-pre:absent\nlabel:restore-pre:absent\nresources:container:absent\nresources:volume:absent\nresources:network:absent\nresources:access:absent'
run_quiesce_state_case disappears_foreign_name_takeover 1 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller:gone\nid:restore-controller:absent\nname:restore-controller:foreign'
run_quiesce_state_case disappears_access_label_reappears 1 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller:gone\nid:restore-controller:absent\nname:restore-controller:absent\nlabel:restore-controller:reappeared'
run_quiesce_state_case renamed_before_inspect 0 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller\nstop:restore-controller\nid:restore-controller:absent\nname:restore-controller:absent\nlabel:restore-controller:absent\nname:restore-pre:absent\nlabel:restore-pre:absent\nresources:container:absent\nresources:volume:absent\nresources:network:absent\nresources:access:absent'
run_quiesce_state_case renamed_after_stop 0 \
  $'name:restore-controller:present\nlabel:restore-controller:present\ninspect:restore-controller\nstop:restore-controller\nid:restore-controller:present\nrm:restore-controller\nid:restore-controller:absent\nname:restore-controller:absent\nlabel:restore-controller:absent\nname:restore-pre:absent\nlabel:restore-pre:absent\nresources:container:absent\nresources:volume:absent\nresources:network:absent\nresources:access:absent'
run_quiesce_state_case foreign_name_masks_label 1 \
  $'name:restore-controller:foreign\nlabel:restore-controller:present\ninspect:restore-controller\nstop:restore-controller\nid:restore-controller:absent\nname:restore-controller:foreign'
run_quiesce_state_case nested_resources 1 \
  $'name:restore-controller:absent\nlabel:restore-controller:absent\nname:restore-pre:absent\nlabel:restore-pre:absent\nresources:container:present'

recover_restore_source="$(
  sed -n '/^recover_active_restore_run()/,/^}/p' "$target"
)"
run_recover_restore_case() {
  local mode="${1:?recover mode required}"
  local expected_status="${2:?expected recover status required}"
  local expected_events="${3:?expected recover events required}"
  local expected_active="${4:?expected active state required}"
  (
    eval "$recover_restore_source"
    restore_run_active=true
    restore_active_backup_id=11111111-1111-4111-8111-111111111111
    events=''
    append_recover_event() {
      if [[ -n "$events" ]]; then
        events+=$'\n'
      fi
      events+="$1"
    }
    quiesce_restore_access_containers() {
      append_recover_event "quiesce:$1"
      [[ "$mode" != quiesce_failure ]]
    }
    verify_restore_resources_absent() {
      append_recover_event "verify:$1"
      [[ "$mode" != resource_failure ]]
    }
    if recover_active_restore_run; then
      actual_status=0
    else
      actual_status=$?
    fi
    [[ "$actual_status" == "$expected_status" &&
      "$events" == "$expected_events" &&
      "$restore_run_active" == "$expected_active" ]]
    if [[ "$expected_active" == true ]]; then
      [[ "$restore_active_backup_id" == \
        11111111-1111-4111-8111-111111111111 ]]
    else
      [[ -z "$restore_active_backup_id" ]]
    fi
  ) || fail "active restore cleanup recovery failed for $mode"
}

run_recover_restore_case success 0 \
  $'quiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111' \
  false
run_recover_restore_case quiesce_failure 1 \
  'quiesce:11111111-1111-4111-8111-111111111111' true
run_recover_restore_case resource_failure 1 \
  $'quiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111' \
  true

signal_handler_source="$(
  sed -n '/^handle_harness_signal()/,/^}/p' "$target"
)"
signal_recovery_events="$tmpdir/restore-active-signal.events"
if (
  eval "$recover_restore_source"
  eval "$signal_handler_source"
  restore_run_active=true
  restore_active_backup_id=11111111-1111-4111-8111-111111111111
  cancel_bounded_command() {
    printf '%s\n' cancel >>"$signal_recovery_events"
  }
  quiesce_restore_access_containers() {
    printf 'quiesce:%s\n' "$1" >>"$signal_recovery_events"
  }
  verify_restore_resources_absent() {
    printf 'verify:%s\n' "$1" >>"$signal_recovery_events"
  }
  cleanup() {
    printf 'cleanup:%s\n' "$1" >>"$signal_recovery_events"
    recover_active_restore_run
    printf 'active:%s:%s\n' \
      "$restore_run_active" "$restore_active_backup_id" \
      >>"$signal_recovery_events"
    exit "$1"
  }
  handle_harness_signal 143
); then
  fail 'restore-active signal recovery unexpectedly returned success'
else
  signal_recovery_status=$?
fi
[[ "$signal_recovery_status" == 143 ]] ||
  fail "restore-active signal recovery status=$signal_recovery_status"
signal_recovery_expected="$tmpdir/restore-active-signal.expected"
printf '%s\n' \
  cancel \
  cleanup:143 \
  quiesce:11111111-1111-4111-8111-111111111111 \
  verify:11111111-1111-4111-8111-111111111111 \
  active:false: \
  >"$signal_recovery_expected"
cmp -s "$signal_recovery_events" "$signal_recovery_expected" ||
  fail 'restore-active signal recovery order changed'

run_restore_route_case() {
  local platform="${1:?platform required}"
  local fake_controller_exit="${2:?controller exit required}"
  local expected_status="${3:?expected status required}"
  local expected_events="${4:?expected events required}"
  local fake_prepare_exit="${5:-0}"
  local fake_quiesce_exit="${6:-0}"
  local fake_resource_exit="${7:-0}"
  (
    eval "$restore_identity_source"
    eval "$restore_proof_block"
    route_dir="$tmpdir/restore-route-${platform}-${fake_controller_exit}-${fake_prepare_exit}-${fake_quiesce_exit}-${fake_resource_exit}"
    artifact_dir="$route_dir/artifacts"
    restore_report_dir="$route_dir/reports"
    mkdir -m 0700 "$route_dir" "$artifact_dir" "$restore_report_dir"
    backup_id=11111111-1111-4111-8111-111111111111
    printf '%s\n' "$backup_id" >"$artifact_dir/backup-id"
    recovery_backup_id="$backup_id"
    install -m 0600 /dev/null \
      "$restore_report_dir/restore-${backup_id}.json"
    live_project=happylearn-phase5-live-a1b2c3d4e5f6
    fixture_suffix=a1b2c3d4e5f6
    restore_controller=restore-controller
    restore_repository_handoff=restore-pre
    restore_run_active=false
    restore_active_backup_id=''
    restore_host_uid=''
    restore_host_gid=''
    restore_controller_uid=''
    restore_controller_gid=''
    restore_controller_requires_repository_prepare=false
    restore_docker_socket=''
    restore_docker_socket_group=''
    backup_host_root=/backup
    restore_control_dir=/control
    restore_controller_tmp=/controller-tmp
    restore_license_file=/license
    teacher_credential_file=/teacher-credential
    repo_root=/repo
    script_dir=/repo/scripts
    backup_image=backup-image
    app_image=app-image
    restore_controller_image=restore-controller-image
    events=''
    append_restore_event() {
      if [[ -n "$events" ]]; then
        events+=$'\n'
      fi
      events+="$1"
    }
    id() {
      case "$1" in
        -u) printf '501\n' ;;
        -g) printf '20\n' ;;
        *) return 99 ;;
      esac
    }
    uname() {
      [[ "$1" == -s ]] || return 99
      printf '%s\n' "$platform"
    }
    resolve_restore_docker_socket() {
      append_restore_event socket
      restore_docker_socket=/var/run/docker.sock
      restore_docker_socket_group=0
    }
    prepare_restore_repository_access() {
      [[ "$#" == 1 && "$1" == "$backup_id" ]] || return 98
      append_restore_event "prepare:$1"
      return "$fake_prepare_exit"
    }
    quiesce_restore_access_containers() {
      append_restore_event "quiesce:$1"
      return "$fake_quiesce_exit"
    }
    docker_bounded() {
      local previous='' argument='' controller_user=''
      [[ "$1" == 3600 && "$2" == run ]] || return 99
      shift 2
      for argument in "$@"; do
        if [[ "$previous" == --user ]]; then
          controller_user="$argument"
          previous=''
          continue
        fi
        if [[ "$argument" == --user ]]; then
          previous=--user
        fi
      done
      [[ -n "$controller_user" ]] || return 99
      append_restore_event "controller:${controller_user}"
      return "$fake_controller_exit"
    }
    verify_restore_resources_absent() {
      append_restore_event "verify:$1"
      return "$fake_resource_exit"
    }
    portable_file_mode() { printf '600\n'; }
    portable_file_owner() { printf '501\n'; }
    portable_file_group() { printf '20\n'; }
    docker() { return 98; }
    if run_restore_proof; then
      actual_status=0
    else
      actual_status=$?
    fi
    [[ "$actual_status" == "$expected_status" &&
      "$events" == "$expected_events" ]]
    if [[ "$fake_quiesce_exit" == 0 &&
      "$fake_resource_exit" == 0 ]]; then
      [[ "$restore_run_active" == false &&
        -z "$restore_active_backup_id" ]]
    else
      [[ "$restore_run_active" == true &&
        "$restore_active_backup_id" == "$backup_id" ]]
    fi
  ) || fail \
    "restore route state machine failed for $platform/$fake_controller_exit"
}

run_restore_route_case Darwin 0 0 \
  $'socket\ncontroller:0:0\nquiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111'
run_restore_route_case Linux 0 0 \
  $'socket\nprepare:11111111-1111-4111-8111-111111111111\ncontroller:501:20\nquiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111'
run_restore_route_case Darwin 42 42 \
  $'socket\ncontroller:0:0\nquiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111'
run_restore_route_case Darwin 143 143 \
  $'socket\ncontroller:0:0\nquiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111'
run_restore_route_case Linux 0 37 \
  $'socket\nprepare:11111111-1111-4111-8111-111111111111\nquiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111' \
  37
run_restore_route_case Darwin 0 17 \
  $'socket\ncontroller:0:0\nquiesce:11111111-1111-4111-8111-111111111111' \
  0 17 19
run_restore_route_case Darwin 0 19 \
  $'socket\ncontroller:0:0\nquiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111' \
  0 0 19

# Preserve the explicit failure priority:
# preflight > controller > quiesce > second resource verification.
run_restore_route_case Linux 0 37 \
  $'socket\nprepare:11111111-1111-4111-8111-111111111111\nquiesce:11111111-1111-4111-8111-111111111111' \
  37 17 19
run_restore_route_case Linux 0 37 \
  $'socket\nprepare:11111111-1111-4111-8111-111111111111\nquiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111' \
  37 0 19
run_restore_route_case Darwin 42 42 \
  $'socket\ncontroller:0:0\nquiesce:11111111-1111-4111-8111-111111111111' \
  0 17 19
run_restore_route_case Darwin 42 42 \
  $'socket\ncontroller:0:0\nquiesce:11111111-1111-4111-8111-111111111111\nverify:11111111-1111-4111-8111-111111111111' \
  0 0 19

if (
  eval "$restore_proof_block"
  stale_root="$tmpdir/restore-stale-binding"
  artifact_dir="$stale_root/artifacts"
  mkdir -p -m 0700 "$artifact_dir"
  printf '%s\n' 22222222-2222-4222-8222-222222222222 \
    >"$artifact_dir/backup-id"
  recovery_backup_id=''
  docker_bounded() {
    install -m 0600 /dev/null "$stale_root/docker-called"
    return 99
  }
  run_restore_proof
); then
  fail 'restore accepted a stale backup ID without current-run binding'
fi
[[ ! -e "$tmpdir/restore-stale-binding/docker-called" ]] ||
  fail 'stale backup ID reached the restore controller'

mkdir -p "$tmpdir/bin"
cat >"$tmpdir/bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${E2E_FAKE_DOCKER_LOG:?}"
if [[ "${E2E_FAKE_DOCKER_MODE:-unexpected}" == unexpected ]]; then
  touch "${E2E_UNEXPECTED_DOCKER_CALL:?}"
  exit 99
fi
if [[ "${E2E_FAKE_DOCKER_MODE:-}" == coordinator* ]]; then
  printf '%s\n' "RAW_DAEMON_DETAIL $*" >&2
fi
if [[ "${1:-}" == run && "$E2E_FAKE_DOCKER_MODE" == signal ]]; then
  name='' project='' owner='' previous=''
  for argument in "$@"; do
    if [[ "$previous" == --name ]]; then name="$argument"; previous=''; continue; fi
    if [[ "$previous" == --label ]]; then
      case "$argument" in
        com.docker.compose.project=*) project="${argument#*=}" ;;
        io.happylearn.phase5.e2e-owner=*) owner="${argument#*=}" ;;
      esac
      previous=''
      continue
    fi
    case "$argument" in --name|--label) previous="$argument" ;; esac
  done
  [[ -n "$name" && -n "$project" && -n "$owner" ]] || exit 95
  printf '%s|%s|%s|%s\n' \
    eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee \
    "$name" "$project" "$owner" \
    >"${E2E_FAKE_DOCKER_STATE:?}.ownership"
  printf '%s\n' "$$" >"${E2E_SIGNAL_CHILD_PID_FILE:?}"
  : >"${E2E_SIGNAL_READY_FILE:?}"
  exec sleep 300
fi
if [[ "${1:-}" == network && "${2:-}" == create &&
  "$E2E_FAKE_DOCKER_MODE" == ownership_signal* ]]; then
  name="${*: -1}"
  project='' owner='' previous=''
  for argument in "$@"; do
    if [[ "$previous" == --label ]]; then
      case "$argument" in
        com.docker.compose.project=*) project="${argument#*=}" ;;
        io.happylearn.phase5.e2e-owner=*) owner="${argument#*=}" ;;
      esac
      previous=''
      continue
    fi
    [[ "$argument" == --label ]] && previous=--label
  done
  [[ -n "$name" && -n "$project" && -n "$owner" ]] || exit 94
  if [[ "$E2E_FAKE_DOCKER_MODE" == ownership_signal_collision ]]; then
    owner=foreign-owner
  fi
  printf '%s|%s|%s|%s\n' \
    ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff \
    "$name" "$project" "$owner" \
    >"${E2E_FAKE_DOCKER_STATE:?}.network"
  printf '%s\n' "$$" >"${E2E_SIGNAL_CHILD_PID_FILE:?}"
  : >"${E2E_SIGNAL_READY_FILE:?}"
  exec sleep 300
fi
if [[ "${1:-}" == network && "${2:-}" == ls &&
  "$E2E_FAKE_DOCKER_MODE" == ownership_signal* ]]; then
  IFS='|' read -r id name project owner \
    <"${E2E_FAKE_DOCKER_STATE:?}.network"
  [[ "$*" == *"name=^${name}$"* ]] && printf '%s\n' "$id"
  exit 0
fi
if [[ "${1:-}" == network && "${2:-}" == inspect &&
  "$E2E_FAKE_DOCKER_MODE" == ownership_signal* ]]; then
  IFS='|' read -r id name project owner \
    <"${E2E_FAKE_DOCKER_STATE:?}.network"
  printf '%s|%s|%s\n' "$id" "$name" "$owner"
  exit 0
fi
if [[ "${1:-}" == network && "${2:-}" == rm &&
  "$E2E_FAKE_DOCKER_MODE" == ownership_signal* ]]; then
  exit 0
fi
if [[ "${1:-}" == create ]]; then
  state="${E2E_FAKE_DOCKER_STATE:?}"
  environment='PATH=/usr/bin'
  command='safe'
  previous=''
  for argument in "$@"; do
    if [[ "$previous" == --env-file ]]; then
      environment="$(sed -n '1p' "$argument")"
      previous=''
      continue
    fi
    if [[ "$previous" == -e || "$previous" == --env ]]; then
      environment="${environment}"$'\n'"${argument}"
      previous=''
      continue
    fi
    case "$argument" in
      --env-file|-e|--env) previous="$argument" ;;
      phase5-e2e-secret-marker) command="$argument" ;;
      /repository) command="$argument" ;;
    esac
  done
  printf '%s\n' "$environment" >"${state}.env"
  printf '%s\n' "$command" >"${state}.cmd"
  printf '%s\n' phase5-contract-canary
  exit 0
fi
if [[ "${1:-}" == inspect && "$*" == *'{{json .Config.Env}}'* ]]; then
  state="${E2E_FAKE_DOCKER_STATE:?}"
  environment="$(<"${state}.env")"
  command="$(<"${state}.cmd")"
  printf '["%s"]|["/bin/sh"]|["%s"]\n' "$environment" "$command"
  exit 0
fi
if [[ "${1:-}" == inspect && "$*" == *'{{range .Config.Env}}'* ]]; then
  state="${E2E_FAKE_DOCKER_STATE:?}"
  sed -n '1,$p' "${state}.env"
  exit 0
fi
if [[ "${1:-}" == inspect && "$*" == *'{{json .HostConfig.Binds}}'* ]]; then
  printf '%s\n' '[]|false|"none"'
  exit 0
fi
if [[ "${1:-}" == container && "${2:-}" == ls &&
  "$E2E_FAKE_DOCKER_MODE" == resource_identity* ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_list_failure ]] ||
    exit 98
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_not_found ]] ||
    exit 0
  if [[ "$E2E_FAKE_DOCKER_MODE" == resource_identity_id ]]; then
    printf '%s\n' \
      bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  else
    printf '%s\n' "${E2E_RESOURCE_ID:?}"
  fi
  exit 0
fi
if [[ "${1:-}" == inspect &&
  "$E2E_FAKE_DOCKER_MODE" == resource_identity* &&
  "$*" == *'com.docker.compose.service'* ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_inspect_failure ]] ||
    exit 98
  project="${E2E_RESOURCE_PROJECT:?}"
  owner="${E2E_RESOURCE_OWNER:?}"
  service="${E2E_RESOURCE_SERVICE:?}"
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_project ]] ||
    project=happylearn-phase5-live-ffffffffffff
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_owner ]] ||
    owner=ffffffffffff
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_service ]] ||
    service=redis
  running=true
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_stopped ]] ||
    running=false
  printf '%s|/%s|%s|%s|%s|%s\n' \
    "${E2E_RESOURCE_ID:?}" "${E2E_RESOURCE_NAME:?}" \
    "$project" "$owner" "$service" "$running"
  exit 0
fi
if [[ "${1:-}" == inspect &&
  "$E2E_FAKE_DOCKER_MODE" == resource_identity* &&
  "$*" == *'{{.State.OOMKilled}}'* ]]; then
  oom=false restart=0 nano_cpus=100000000 memory_bytes=134217728
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_unbounded ]] ||
    nano_cpus=0
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_memory_unbounded ]] ||
    memory_bytes=0
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_oom ]] || oom=true
  [[ "$E2E_FAKE_DOCKER_MODE" != resource_identity_restart ]] || restart=1
  printf '%s|%s|%s|%s\n' \
    "$oom" "$restart" "$nano_cpus" "$memory_bytes"
  exit 0
fi
if [[ "${1:-}" == inspect && "$*" == *'io.happylearn.phase5.e2e-owner'* ]]; then
  target="${*: -1}"
  case "$E2E_FAKE_DOCKER_MODE" in
    cleanup_owned)
      [[ "${E2E_CLEANUP_KIND:-container}" == container ]] || exit 97
      [[ "$target" == "${E2E_CLEANUP_NAME:?}" ||
        "$target" == "${E2E_CLEANUP_ID:?}" ]] || exit 97
      printf '%s|/%s|%s|%s\n' \
        "$E2E_CLEANUP_ID" "$E2E_CLEANUP_NAME" \
        "${E2E_CLEANUP_PROJECT:?}" "${E2E_CLEANUP_OWNER:?}"
      ;;
    cleanup_collision)
      [[ "${E2E_CLEANUP_KIND:-container}" == container ]] || exit 97
      [[ "$target" == "${E2E_CLEANUP_NAME:?}" ||
        "$target" == "${E2E_CLEANUP_ID:?}" ]] || exit 97
      printf '%s|/%s|%s|foreign-owner\n' \
        bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
        "$E2E_CLEANUP_NAME" "${E2E_CLEANUP_PROJECT:?}"
      ;;
    cleanup_inspect_failure)
      [[ "${E2E_CLEANUP_KIND:-container}" == container ]] || exit 97
      exit 98
      ;;
    cleanup_remove_failure)
      [[ "${E2E_CLEANUP_KIND:-container}" == container ]] || exit 97
      [[ "$target" == "${E2E_CLEANUP_NAME:?}" ||
        "$target" == "${E2E_CLEANUP_ID:?}" ]] || exit 97
      printf '%s|/%s|%s|%s\n' \
        "$E2E_CLEANUP_ID" "$E2E_CLEANUP_NAME" \
        "${E2E_CLEANUP_PROJECT:?}" "${E2E_CLEANUP_OWNER:?}"
      ;;
    cleanup_not_found)
      [[ "${E2E_CLEANUP_KIND:-container}" == container ]] || exit 97
      exit 97
      ;;
    signal)
      IFS='|' read -r id name project owner \
        <"${E2E_FAKE_DOCKER_STATE:?}.ownership"
      [[ "$target" == "$name" || "$target" == "$id" ]] || exit 1
      printf '%s|/%s|%s|%s\n' "$id" "$name" "$project" "$owner"
      ;;
    coordinator*)
      [[ "$E2E_FAKE_DOCKER_MODE" != coordinator_inspect_failure ]] ||
        exit 98
      owner="${E2E_COORDINATOR_OWNER:?}"
      [[ "$E2E_FAKE_DOCKER_MODE" != coordinator_collision ]] ||
        owner=ffffffffffff
      printf '%s|/%s|%s|True|%s\n' \
        "${E2E_COORDINATOR_ID:?}" phase5-coordinator-one-shot \
        "${E2E_COORDINATOR_PROJECT:?}" "$owner"
      ;;
    *) exit 96 ;;
  esac
  exit 0
fi
if [[ "${1:-}" == container && "${2:-}" == ls &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == container ]]; then
  if [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_not_found &&
    "$*" == *"name=^/${E2E_CLEANUP_NAME:?}$"* ]]; then
    printf '%s\n' "${E2E_CLEANUP_ID:?}"
  fi
  exit 0
fi
if [[ "${1:-}" == network && "${2:-}" == ls &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == network ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_not_found ]] || exit 0
  if [[ "$E2E_FAKE_DOCKER_MODE" == cleanup_collision ]]; then
    printf '%s\n' \
      bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  else
    printf '%s\n' "${E2E_CLEANUP_ID:?}"
  fi
  exit 0
fi
if [[ "${1:-}" == network && "${2:-}" == inspect &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == network ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_inspect_failure ]] || exit 98
  printf '%s|%s|%s\n' \
    "${E2E_CLEANUP_ID:?}" "${E2E_CLEANUP_NAME:?}" \
    "${E2E_CLEANUP_OWNER:?}"
  exit 0
fi
if [[ "${1:-}" == network && "${2:-}" == rm &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == network ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_remove_failure ]] || exit 98
  exit 0
fi
if [[ "${1:-}" == volume && "${2:-}" == ls &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == volume ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_not_found ]] || exit 0
  printf '%s\n' "${E2E_CLEANUP_NAME:?}"
  exit 0
fi
if [[ "${1:-}" == volume && "${2:-}" == inspect &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == volume ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_inspect_failure ]] || exit 98
  owner="${E2E_CLEANUP_OWNER:?}"
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_collision ]] || owner=ffffffffffff
  printf '%s|%s\n' "${E2E_CLEANUP_NAME:?}" "$owner"
  exit 0
fi
if [[ "${1:-}" == volume && "${2:-}" == rm &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == volume ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_remove_failure ]] || exit 98
  exit 0
fi
if [[ "${1:-}" == image && "${2:-}" == ls &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == image ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_not_found ]] || exit 0
  if [[ "$E2E_FAKE_DOCKER_MODE" == cleanup_collision ]]; then
    printf '%s\n' \
      sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  else
    printf 'sha256:%s\n' "${E2E_CLEANUP_ID:?}"
  fi
  exit 0
fi
if [[ "${1:-}" == image && "${2:-}" == inspect &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == image ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_inspect_failure ]] || exit 98
  if [[ "$E2E_FAKE_DOCKER_MODE" == cleanup_retag_race ]]; then
    touch "${E2E_FAKE_DOCKER_STATE:?}.retagged"
  fi
  printf 'sha256:%s|%s\n' \
    "${E2E_CLEANUP_ID:?}" "${E2E_CLEANUP_OWNER:?}"
  exit 0
fi
if [[ "${1:-}" == image && "${2:-}" == rm &&
  "$E2E_FAKE_DOCKER_MODE" == cleanup* &&
  "${E2E_CLEANUP_KIND:-container}" == image ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_remove_failure ]] || exit 98
  if [[ "$E2E_FAKE_DOCKER_MODE" == cleanup_retag_race ]]; then
    [[ -e "${E2E_FAKE_DOCKER_STATE:?}.retagged" &&
      "${3:-}" == "sha256:${E2E_CLEANUP_ID:?}" ]] ||
      exit 98
  fi
  exit 0
fi
if [[ "${1:-}" == container && "${2:-}" == ls &&
  "$E2E_FAKE_DOCKER_MODE" == coordinator* ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != coordinator_list_failure ]] || exit 98
  [[ "$E2E_FAKE_DOCKER_MODE" != coordinator_not_found ]] || exit 0
  if [[ "$E2E_FAKE_DOCKER_MODE" == coordinator_post_list_failure &&
    -e "${E2E_FAKE_DOCKER_STATE:?}.removed" ]]; then
    exit 98
  fi
  [[ ! -e "${E2E_FAKE_DOCKER_STATE:?}.removed" ]] || exit 0
  printf '%s\n' "${E2E_COORDINATOR_ID:?}"
  exit 0
fi
if [[ "${1:-}" == rm && "${2:-}" == -f &&
  "${E2E_CLEANUP_KIND:-container}" == container ]]; then
  [[ "$E2E_FAKE_DOCKER_MODE" != cleanup_remove_failure ]] || exit 98
  [[ "$E2E_FAKE_DOCKER_MODE" != coordinator_remove_failure ]] || exit 98
  if [[ "$E2E_FAKE_DOCKER_MODE" == coordinator* ]]; then
    touch "${E2E_FAKE_DOCKER_STATE:?}.removed"
  fi
  exit 0
fi
exit 97
FAKE_DOCKER
chmod 0700 "$tmpdir/bin/docker"

assert_fail_fast() {
  local license="$1" group="$2" expected="$3" status=0
  rm -f "$tmpdir/docker-called"
  PATH="$tmpdir/bin:$PATH" \
    E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE=unexpected \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$license" \
    HAPPYLEARN_E2E_GROUP="$group" \
    "$target" >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  [[ "$status" -eq 2 ]] || fail "fail-fast status was $status"
  grep -Fq "$expected" "$tmpdir/stderr" ||
    fail "fail-fast message did not contain: $expected"
  [[ ! -s "$tmpdir/stdout" ]] || fail 'fail-fast path wrote stdout'
  [[ ! -e "$tmpdir/docker-called" ]] || fail 'fail-fast path reached Docker'
}

readable_license="$tmpdir/minio.license"
: >"$readable_license"
assert_fail_fast relative-license phase5 \
  'absolute readable AIStor Free license file'
assert_fail_fast "$tmpdir/missing.license" phase5 \
  'absolute readable AIStor Free license file'
assert_fail_fast "$readable_license" invalid \
  'HAPPYLEARN_E2E_GROUP must be all, phase5, phase5-mobile, recovery, or resources'

run_resource_evidence_probe() {
  local mutation="$1"
  local expected_status="$2"
  local evidence="$tmpdir/resource-${mutation}.evidence"
  local status=0
  cat >"$evidence" <<'EVIDENCE'
resource_evidence_version=2
browser_included=true
owned_samples=true
saw_browser=true
saw_backup=true
saw_heavy=true
saw_worker=true
worker_backup_overlap=false
configured_limits_complete=true
peak_configured_cpu=2.000
peak_configured_memory_mib=4096.000
peak_live_cpu_percent=200.000
peak_live_memory_mib=4096.000
peak_browser_cpu_percent=10.000
peak_browser_memory_mib=256.000
oom_killed=false
max_restart_count=0
EVIDENCE
  case "$mutation" in
    safe) ;;
    old_version)
      sed -i.bak \
        's/^resource_evidence_version=2$/resource_evidence_version=1/' \
        "$evidence"
      ;;
    missing_browser_included)
      sed -i.bak \
        's/^browser_included=true$/browser_included=false/' "$evidence"
      ;;
    unowned)
      sed -i.bak 's/^owned_samples=true$/owned_samples=false/' "$evidence"
      ;;
    missing_browser)
      sed -i.bak 's/^saw_browser=true$/saw_browser=false/' "$evidence"
      ;;
    missing_backup)
      sed -i.bak 's/^saw_backup=true$/saw_backup=false/' "$evidence"
      ;;
    missing_heavy)
      sed -i.bak 's/^saw_heavy=true$/saw_heavy=false/' "$evidence"
      ;;
    missing_worker)
      sed -i.bak 's/^saw_worker=true$/saw_worker=false/' "$evidence"
      ;;
    overlap)
      sed -i.bak \
        's/^worker_backup_overlap=false$/worker_backup_overlap=true/' \
        "$evidence"
      ;;
    unbounded)
      sed -i.bak \
        's/^configured_limits_complete=true$/configured_limits_complete=false/' \
        "$evidence"
      ;;
    configured_cpu|browser_configured_cpu)
      sed -i.bak \
        's/^peak_configured_cpu=2.000$/peak_configured_cpu=2.001/' \
        "$evidence"
      ;;
    configured_memory|browser_configured_memory)
      sed -i.bak \
        's/^peak_configured_memory_mib=4096.000$/peak_configured_memory_mib=4096.001/' \
        "$evidence"
      ;;
    live_cpu|browser_live_cpu)
      sed -i.bak \
        's/^peak_live_cpu_percent=200.000$/peak_live_cpu_percent=200.001/' \
        "$evidence"
      ;;
    live_memory|browser_live_memory)
      sed -i.bak \
        's/^peak_live_memory_mib=4096.000$/peak_live_memory_mib=4096.001/' \
        "$evidence"
      ;;
    browser_memory)
      sed -i.bak \
        's/^peak_browser_memory_mib=256.000$/peak_browser_memory_mib=0.000/' \
        "$evidence"
      ;;
    oom)
      sed -i.bak 's/^oom_killed=false$/oom_killed=true/' "$evidence"
      ;;
    restart)
      sed -i.bak 's/^max_restart_count=0$/max_restart_count=1/' "$evidence"
      ;;
    *) fail "unknown resource evidence mutation: $mutation" ;;
  esac
  rm -f "${evidence}.bak"
  chmod 0600 "$evidence"
  PATH="$tmpdir/bin:$PATH" \
    E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE=unexpected \
    HAPPYLEARN_PHASE5_RESOURCE_CONTRACT=1 \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=resources \
    "$target" --resource-contract-probe "$evidence" \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  [[ "$status" -eq "$expected_status" ]] ||
    fail "resource evidence mutation $mutation returned $status, expected $expected_status"
  [[ ! -e "$tmpdir/docker-called" ]] ||
    fail "resource evidence mutation $mutation unexpectedly reached Docker"
}

run_resource_evidence_probe safe 0
for mutation in \
  old_version missing_browser_included unowned missing_browser missing_backup \
  missing_heavy missing_worker overlap \
  unbounded configured_cpu configured_memory live_cpu live_memory oom restart; do
  run_resource_evidence_probe "$mutation" 1
done
run_resource_evidence_probe browser_memory 1
for mutation in \
  browser_configured_cpu browser_configured_memory \
  browser_live_cpu browser_live_memory; do
  run_resource_evidence_probe "$mutation" 1
done

run_resource_status_probe() {
  local browser_status="$1"
  local backup_status="$2"
  local monitor_status="$3"
  local expected_status="$4"
  local status=0
  PATH="$tmpdir/bin:$PATH" \
    E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE=unexpected \
    HAPPYLEARN_PHASE5_RESOURCE_STATUS_CONTRACT=1 \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=resources \
    "$target" --resource-status-contract-probe \
      "$browser_status" "$backup_status" "$monitor_status" \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  [[ "$status" -eq "$expected_status" ]] ||
    fail "resource status probe returned $status, expected $expected_status"
}

run_resource_status_probe 0 0 0 0
run_resource_status_probe 5 0 0 5
run_resource_status_probe 0 6 7 6
run_resource_status_probe 0 0 8 8

run_resource_child_status_probe() {
  local mutation="$1"
  local expected_status="$2"
  local status_file="$tmpdir/resource-child-${mutation}.status"
  local status=0
  case "$mutation" in
    safe) install -m 0600 /dev/null "$status_file"; printf '17\n' >"$status_file" ;;
    malformed)
      install -m 0600 /dev/null "$status_file"
      printf 'not-a-status\n' >"$status_file"
      ;;
    multiple)
      install -m 0600 /dev/null "$status_file"
      printf '0\n1\n' >"$status_file"
      ;;
    out_of_range)
      install -m 0600 /dev/null "$status_file"
      printf '256\n' >"$status_file"
      ;;
    permissive)
      install -m 0600 /dev/null "$status_file"
      printf '0\n' >"$status_file"
      chmod 0644 "$status_file"
      ;;
    symlink)
      install -m 0600 /dev/null "${status_file}.target"
      printf '0\n' >"${status_file}.target"
      ln -s "${status_file}.target" "$status_file"
      ;;
    *) fail "unknown resource child status mutation: $mutation" ;;
  esac
  HAPPYLEARN_PHASE5_RESOURCE_CHILD_STATUS_CONTRACT=1 \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=resources \
    "$target" --resource-child-status-contract-probe "$status_file" \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  [[ "$status" -eq "$expected_status" ]] ||
    fail "resource child status probe $mutation returned $status, expected $expected_status"
  if [[ "$mutation" == safe ]]; then
    [[ "$(<"$tmpdir/stdout")" == 17 ]] ||
      fail 'resource child status probe did not return the recorded status'
  fi
}

run_resource_child_status_probe safe 0
for mutation in malformed multiple out_of_range permissive symlink; do
  run_resource_child_status_probe "$mutation" 1
done

run_resource_identity_probe() {
  local mode="$1"
  local expected_status="$2"
  local running_policy="${3:-running}"
  local service="${4:-worker}"
  local resource_id
  local status=0
  resource_id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  : >"$tmpdir/docker.log"
  PATH="$tmpdir/bin:$PATH" \
    E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE="$mode" \
    E2E_RESOURCE_NAME=phase5-resource-canary \
    E2E_RESOURCE_ID="$resource_id" \
    E2E_RESOURCE_PROJECT=happylearn-phase5-live-a1b2c3d4e5f6 \
    E2E_RESOURCE_OWNER=a1b2c3d4e5f6 \
    E2E_RESOURCE_SERVICE="$service" \
    HAPPYLEARN_PHASE5_RESOURCE_IDENTITY_CONTRACT=1 \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=resources \
    "$target" --resource-identity-contract-probe \
      phase5-resource-canary "$resource_id" \
      happylearn-phase5-live-a1b2c3d4e5f6 a1b2c3d4e5f6 "$service" \
      "$running_policy" \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  [[ "$status" -eq "$expected_status" ]] ||
    fail "resource identity probe $mode returned $status, expected $expected_status"
}

run_resource_identity_probe resource_identity_safe 0
for mode in \
  resource_identity_not_found resource_identity_list_failure \
  resource_identity_inspect_failure resource_identity_id \
  resource_identity_project resource_identity_owner resource_identity_service \
  resource_identity_unbounded resource_identity_memory_unbounded \
  resource_identity_oom resource_identity_restart; do
  run_resource_identity_probe "$mode" 1
done
run_resource_identity_probe resource_identity_stopped 1 running worker
run_resource_identity_probe \
  resource_identity_stopped 0 backup_snapshot worker
run_resource_identity_probe \
  resource_identity_stopped 0 backup_snapshot minio
run_resource_identity_probe \
  resource_identity_stopped 1 backup_snapshot redis

run_audit_probe() {
  local mode="$1"
  local expected_status="$2"
  local status=0 user_file=access_key password_file=secret_key
  local kms_file=kms_master_key config_file=config.env
  : >"$tmpdir/docker.log"
  case "$mode" in
    safe)
      PATH="$tmpdir/bin:$PATH" \
        E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
        E2E_FAKE_DOCKER_MODE="$mode" \
        E2E_FAKE_DOCKER_STATE="$tmpdir/canary-state" \
        docker create --name phase5-contract-canary \
          alpine:3.22.1 sleep 30 >/dev/null
      ;;
    env_file)
      printf '%s\n' \
        'AWS_SECRET_ACCESS_KEY=phase5-e2e-secret-marker' \
        >"$tmpdir/canary.env"
      PATH="$tmpdir/bin:$PATH" \
        E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
        E2E_FAKE_DOCKER_MODE="$mode" \
        E2E_FAKE_DOCKER_STATE="$tmpdir/canary-state" \
        docker create --name phase5-contract-canary \
          --env-file "$tmpdir/canary.env" alpine:3.22.1 sleep 30 >/dev/null
      ;;
    secret_env)
      PATH="$tmpdir/bin:$PATH" \
        E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
        E2E_FAKE_DOCKER_MODE="$mode" \
        E2E_FAKE_DOCKER_STATE="$tmpdir/canary-state" \
        docker create --name phase5-contract-canary \
          -e PHASE5_CANARY_SECRET=phase5-e2e-secret-marker \
          alpine:3.22.1 sleep 30 >/dev/null
      ;;
    literal_argv)
      PATH="$tmpdir/bin:$PATH" \
        E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
        E2E_FAKE_DOCKER_MODE="$mode" \
        E2E_FAKE_DOCKER_STATE="$tmpdir/canary-state" \
        docker create --name phase5-contract-canary \
          alpine:3.22.1 phase5-e2e-secret-marker >/dev/null
      ;;
    repository_locator)
      PATH="$tmpdir/bin:$PATH" \
        E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
        E2E_FAKE_DOCKER_MODE="$mode" \
        E2E_FAKE_DOCKER_STATE="$tmpdir/canary-state" \
        docker create --name phase5-contract-canary \
          alpine:3.22.1 /repository >/dev/null
      ;;
    aistor_defaults|aistor_user_mutation|aistor_password_mutation|\
      aistor_kms_mutation|aistor_config_mutation|aistor_empty_mutation)
      case "$mode" in
        aistor_user_mutation) user_file=/access_key ;;
        aistor_password_mutation) password_file=changed-secret-key ;;
        aistor_kms_mutation) kms_file=/kms_master_key ;;
        aistor_config_mutation) config_file=changed.env ;;
        aistor_empty_mutation) config_file='' ;;
      esac
      PATH="$tmpdir/bin:$PATH" \
        E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
        E2E_FAKE_DOCKER_MODE="$mode" \
        E2E_FAKE_DOCKER_STATE="$tmpdir/canary-state" \
        docker create --name phase5-contract-canary \
          -e "MINIO_ROOT_USER_FILE=$user_file" \
          -e "MINIO_ROOT_PASSWORD_FILE=$password_file" \
          -e "MINIO_KMS_SECRET_KEY_FILE=$kms_file" \
          -e "MINIO_CONFIG_ENV_FILE=$config_file" \
          alpine:3.22.1 sleep 30 >/dev/null
      ;;
  esac
  PATH="$tmpdir/bin:$PATH" \
    E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE="$mode" \
    E2E_FAKE_DOCKER_STATE="$tmpdir/canary-state" \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=phase5 \
    "$target" --audit-container-metadata phase5-contract-canary \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  if [[ "$status" -ne "$expected_status" ]]; then
    sed -n '1,20p' "$tmpdir/stderr" >&2
    fail "runtime audit $mode returned $status, expected $expected_status"
  fi
  grep -Fq 'inspect' "$tmpdir/docker.log" ||
    fail "runtime audit $mode did not execute Docker metadata inspection"
  grep -Fq 'create --name phase5-contract-canary' "$tmpdir/docker.log" ||
    fail "runtime audit $mode did not create its metadata canary"
}

run_audit_probe safe 0
run_audit_probe env_file 1
run_audit_probe secret_env 1
run_audit_probe literal_argv 1
run_audit_probe repository_locator 0
run_audit_probe aistor_defaults 0
run_audit_probe aistor_user_mutation 1
run_audit_probe aistor_password_mutation 1
run_audit_probe aistor_kms_mutation 1
run_audit_probe aistor_config_mutation 1
run_audit_probe aistor_empty_mutation 1

run_cleanup_probe() {
  local mode="$1"
  local expected_status="$2"
  local expect_removed="$3"
  local cleanup_id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  local status=0
  : >"$tmpdir/docker.log"
  PATH="$tmpdir/bin:$PATH" \
    E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE="$mode" \
    E2E_CLEANUP_NAME=phase5-cleanup-canary \
    E2E_CLEANUP_ID="$cleanup_id" \
    E2E_CLEANUP_PROJECT=happylearn-phase5-live-a1b2c3d4e5f6 \
    E2E_CLEANUP_OWNER=a1b2c3d4e5f6 \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=phase5 \
    "$target" --cleanup-container-ownership-probe \
      phase5-cleanup-canary "$cleanup_id" \
      happylearn-phase5-live-a1b2c3d4e5f6 a1b2c3d4e5f6 \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  [[ "$status" -eq "$expected_status" ]] ||
    fail "cleanup ownership probe $mode returned $status, expected $expected_status"
  if [[ "$expect_removed" == yes ]]; then
    grep -Fq "rm -f $cleanup_id" "$tmpdir/docker.log" ||
      fail 'owned cleanup probe did not remove the exact recorded ID'
  elif grep -Fq 'rm -f' "$tmpdir/docker.log"; then
    fail 'collision cleanup probe removed an unowned container'
  fi
}

run_cleanup_probe cleanup_owned 0 yes
run_cleanup_probe cleanup_collision 3 no

run_cleanup_failure_probe() {
  local mode="$1"
  local expected_status="$2"
  local expect_removed="$3"
  local original_status="${4:-0}"
  local cleanup_kind="${5:-container}"
  local cleanup_id='dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
  local cleanup_name=phase5-cleanup-failure-canary
  local removal_fragment
  local status=0
  case "$cleanup_kind" in
    container) removal_fragment="rm -f $cleanup_id" ;;
    network) removal_fragment="network rm $cleanup_id" ;;
    volume) removal_fragment="volume rm $cleanup_name" ;;
    image) removal_fragment="image rm sha256:$cleanup_id" ;;
    *) fail "unknown cleanup kind: $cleanup_kind" ;;
  esac
  : >"$tmpdir/docker.log"
  rm -f "$tmpdir/cleanup-state.retagged"
  PATH="$tmpdir/bin:$PATH" \
    E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE="$mode" \
    E2E_FAKE_DOCKER_STATE="$tmpdir/cleanup-state" \
    E2E_CLEANUP_NAME="$cleanup_name" \
    E2E_CLEANUP_ID="$cleanup_id" \
    E2E_CLEANUP_PROJECT=happylearn-phase5-live-a1b2c3d4e5f6 \
    E2E_CLEANUP_OWNER=a1b2c3d4e5f6 \
    E2E_CLEANUP_KIND="$cleanup_kind" \
    HAPPYLEARN_PHASE5_CLEANUP_FAILURE_CONTRACT=1 \
    HAPPYLEARN_PHASE5_CLEANUP_KIND="$cleanup_kind" \
    HAPPYLEARN_PHASE5_CLEANUP_ORIGINAL_STATUS="$original_status" \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=phase5 \
    "$target" --cleanup-failure-contract-probe \
      "$cleanup_name" "$cleanup_id" \
      happylearn-phase5-live-a1b2c3d4e5f6 a1b2c3d4e5f6 \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  if [[ "$status" -ne "$expected_status" ]]; then
    tail -n 160 "$tmpdir/stderr" >&2
    sed -n '1,80p' "$tmpdir/docker.log" >&2
    fail "cleanup failure probe $mode returned $status, expected $expected_status"
  fi
  if [[ "$expect_removed" == yes ]]; then
    grep -Fq "$removal_fragment" "$tmpdir/docker.log" ||
      fail "cleanup failure probe $cleanup_kind/$mode skipped exact removal"
  elif grep -Fq "$removal_fragment" "$tmpdir/docker.log"; then
    fail "cleanup failure probe $cleanup_kind/$mode removed an unowned or uncertain resource"
  fi
}

for cleanup_kind in container network volume image; do
  run_cleanup_failure_probe cleanup_owned 0 yes 0 "$cleanup_kind"
  run_cleanup_failure_probe cleanup_not_found 0 no 0 "$cleanup_kind"
  run_cleanup_failure_probe cleanup_inspect_failure 1 no 0 "$cleanup_kind"
  run_cleanup_failure_probe cleanup_remove_failure 1 yes 0 "$cleanup_kind"
  run_cleanup_failure_probe cleanup_collision 1 no 0 "$cleanup_kind"
done
run_cleanup_failure_probe cleanup_retag_race 0 yes 0 image
run_cleanup_failure_probe cleanup_remove_failure 42 yes 42

run_coordinator_one_shot_probe() {
  local docker_mode="${1:-coordinator}"
  local coordinator_mode="${2:-audit}"
  local expected_status="${3:-0}"
  local expected_ledger="${4:-empty}"
  local expected_category="${5:-}"
  local record="$tmpdir/coordinator-one-shots"
  local one_shot_id='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
  local project='happylearn-phase5-live-a1b2c3d4e5f6'
  local owner='a1b2c3d4e5f6'
  local status=0
  printf '%s\n' "$one_shot_id" >"$record"
  chmod 0600 "$record"
  printf '%s\n' PATH=/usr/bin >"$tmpdir/coordinator-state.env"
  if [[ "$docker_mode" == coordinator_audit_failure ]]; then
    printf '%s\n' phase5-e2e-secret-marker >"$tmpdir/coordinator-state.cmd"
  else
    printf '%s\n' safe >"$tmpdir/coordinator-state.cmd"
  fi
  rm -f "$tmpdir/coordinator-state.removed"
  : >"$tmpdir/docker.log"
  PATH="$tmpdir/bin:$PATH" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE="$docker_mode" \
    E2E_FAKE_DOCKER_STATE="$tmpdir/coordinator-state" \
    E2E_COORDINATOR_ID="$one_shot_id" \
    E2E_COORDINATOR_PROJECT="$project" \
    E2E_COORDINATOR_OWNER="$owner" \
    HAPPYLEARN_PHASE5_COORDINATOR_CONTRACT=1 \
    HAPPYLEARN_PHASE5_COORDINATOR_MODE="$coordinator_mode" \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=recovery \
    "$target" --coordinator-one-shot-probe \
      "$record" "$project" "$owner" \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  [[ "$status" -eq "$expected_status" ]] ||
    fail "coordinator probe $docker_mode/$coordinator_mode returned $status"
  if [[ -z "$expected_category" ]]; then
    [[ ! -s "$tmpdir/stderr" ]] ||
      fail "coordinator probe $docker_mode exposed raw failure detail"
  else
    [[ "$(wc -l <"$tmpdir/stderr" | tr -d '[:space:]')" == 1 &&
      "$(<"$tmpdir/stderr")" == \
        "coordinator one-shot failed: category=${expected_category}" ]] ||
      fail "coordinator probe $docker_mode did not emit one fixed category"
  fi
  if [[ "$expected_ledger" == empty ]]; then
    [[ ! -s "$record" ]] ||
      fail "coordinator probe $docker_mode retained a completed ledger"
  else
    grep -Fxq "$one_shot_id" "$record" ||
      fail "coordinator probe $docker_mode advanced an uncertain ledger"
  fi
  if [[ "$docker_mode" == coordinator ]]; then
    grep -Fq \
      'rm -f cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' \
      "$tmpdir/docker.log" ||
      fail 'coordinator one-shot probe did not remove the exact audited ID'
    grep -Fq '{{json .Config.Env}}' "$tmpdir/docker.log" ||
      fail 'coordinator one-shot probe skipped runtime metadata audit'
  fi
}

run_coordinator_one_shot_probe
run_coordinator_one_shot_probe coordinator_not_found cleanup 0 empty
run_coordinator_one_shot_probe \
  coordinator_not_found audit 1 retained missing
run_coordinator_one_shot_probe \
  coordinator_list_failure cleanup 1 retained listing
run_coordinator_one_shot_probe \
  coordinator_inspect_failure cleanup 1 retained metadata
run_coordinator_one_shot_probe \
  coordinator_audit_failure audit 1 retained audit
run_coordinator_one_shot_probe \
  coordinator_remove_failure cleanup 1 retained removal
run_coordinator_one_shot_probe \
  coordinator_post_list_failure cleanup 1 retained residual_listing
run_coordinator_one_shot_probe \
  coordinator_collision cleanup 1 retained identity

run_signal_probe() {
  local ready_file="$tmpdir/signal.ready"
  local child_pid_file="$tmpdir/signal-child.pid"
  local child_pid signal_status=0 attempt child_alive=false
  : >"$tmpdir/docker.log"
  PATH="$tmpdir/bin:$PATH" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE=signal \
    E2E_FAKE_DOCKER_STATE="$tmpdir/signal-state" \
    E2E_SIGNAL_READY_FILE="$ready_file" \
    E2E_SIGNAL_CHILD_PID_FILE="$child_pid_file" \
    HAPPYLEARN_PHASE5_SIGNAL_CONTRACT=1 \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=phase5 \
    "$target" --signal-contract-probe "$ready_file" "$child_pid_file" \
      >"$tmpdir/signal.stdout" 2>"$tmpdir/signal.stderr" &
  signal_target_pid=$!
  for attempt in $(seq 1 100); do
    [[ -f "$ready_file" && -f "$child_pid_file" ]] && break
    kill -0 "$signal_target_pid" 2>/dev/null ||
      fail 'signal probe exited before reaching its long-running Docker child'
    sleep 0.05
  done
  [[ -f "$ready_file" && -f "$child_pid_file" ]] ||
    fail 'signal probe did not become ready'
  child_pid="$(<"$child_pid_file")"
  [[ "$child_pid" =~ ^[1-9][0-9]*$ ]] ||
    fail 'signal probe exposed an invalid Docker child PID'
  kill -TERM "$signal_target_pid"
  if wait "$signal_target_pid"; then
    signal_status=0
  else
    signal_status=$?
  fi
  signal_target_pid=''
  kill -0 "$child_pid" 2>/dev/null && child_alive=true
  [[ "$signal_status" == 143 && "$child_alive" == false ]] ||
    fail "signal probe status=$signal_status child_alive=$child_alive"
  grep -Fq 'run --rm --name' "$tmpdir/docker.log" ||
    fail 'signal probe did not start the long-running --rm container'
  grep -Fq \
    'rm -f eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee' \
    "$tmpdir/docker.log" ||
    fail 'signal probe did not precisely remove its active container'
  [[ ! -s "$tmpdir/signal.stdout" ]] ||
    fail 'signal probe wrote unexpected stdout'
}

run_signal_probe

run_ownership_window_signal_probe() {
  local mode="$1"
  local ready_file="$tmpdir/${mode}.ready"
  local child_pid_file="$tmpdir/${mode}-child.pid"
  local child_pid signal_status=0 attempt child_alive=false
  : >"$tmpdir/docker.log"
  PATH="$tmpdir/bin:$PATH" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE="$mode" \
    E2E_FAKE_DOCKER_STATE="$tmpdir/${mode}-state" \
    E2E_SIGNAL_READY_FILE="$ready_file" \
    E2E_SIGNAL_CHILD_PID_FILE="$child_pid_file" \
    HAPPYLEARN_PHASE5_OWNERSHIP_SIGNAL_CONTRACT=1 \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=phase5 \
    "$target" --ownership-signal-contract-probe \
      "$ready_file" "$child_pid_file" \
      >"$tmpdir/${mode}.stdout" 2>"$tmpdir/${mode}.stderr" &
  signal_target_pid=$!
  for attempt in $(seq 1 100); do
    [[ -f "$ready_file" && -f "$child_pid_file" ]] && break
    kill -0 "$signal_target_pid" 2>/dev/null ||
      fail "ownership signal probe $mode exited before resource creation"
    sleep 0.05
  done
  [[ -f "$ready_file" && -f "$child_pid_file" ]] ||
    fail "ownership signal probe $mode did not become ready"
  child_pid="$(<"$child_pid_file")"
  [[ "$child_pid" =~ ^[1-9][0-9]*$ ]] ||
    fail "ownership signal probe $mode exposed an invalid Docker child PID"
  kill -TERM "$signal_target_pid"
  if wait "$signal_target_pid"; then
    signal_status=0
  else
    signal_status=$?
  fi
  signal_target_pid=''
  kill -0 "$child_pid" 2>/dev/null && child_alive=true
  [[ "$signal_status" == 143 && "$child_alive" == false ]] ||
    fail "ownership signal probe $mode status=$signal_status child_alive=$child_alive"
  if [[ "$mode" == ownership_signal ]]; then
    grep -Fq \
      'network rm ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff' \
      "$tmpdir/docker.log" ||
      fail 'ownership signal probe did not remove the exact owned network ID'
  elif grep -Fq 'network rm' "$tmpdir/docker.log"; then
    fail 'ownership signal probe removed an external collision network'
  fi
}

run_ownership_window_signal_probe ownership_signal
run_ownership_window_signal_probe ownership_signal_collision

mkdir -m 0700 "$tmpdir/live-root"
HAPPYLEARN_BACKUP_LIVE_ROOT="$tmpdir/live-root" \
HAPPYLEARN_BACKUP_IMAGE=happylearn-backup:phase5-contract \
HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
HAPPYLEARN_PHASE5_E2E_OWNER=a1b2c3d4e5f6 \
  docker compose \
    --project-name happylearn-phase5-live-a1b2c3d4e5f6 \
    --file "$repo_root/deploy/compose.dev.yml" \
    --file "$repo_root/deploy/compose.backup-live.yml" \
    --file "$e2e_overlay" \
    config --format json >"$tmpdir/compose.json"
node - "$tmpdir/compose.json" <<'NODE' || fail 'merged live Compose contract changed'
const fs = require("fs");
const config = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const services = config.services;
const hasTarget = (service, target) =>
  (services[service].volumes ?? []).some((volume) => volume.target === target);
const hasDependency = (service, dependency) =>
  Object.hasOwn(services[service].depends_on ?? {}, dependency);
if (
  !services.postgres.command.includes("ssl=on") ||
  !hasTarget("postgres", "/tls") ||
  !hasTarget("postgres", "/run/phase5-secrets") ||
  !hasDependency("postgres", "postgres-tls-init") ||
  !hasDependency("postgres", "phase5-secrets-init") ||
  !hasDependency("minio", "minio-data-init") ||
  !hasDependency("minio", "aistor-license-init") ||
  !hasTarget("minio", "/license") ||
  !String(services.minio.command).includes(
    "--license /license/minio.license",
  ) ||
  services["aistor-license-init"]?.labels?.[
    "io.happylearn.phase5.e2e-owner"
  ] !== "a1b2c3d4e5f6" ||
  !hasDependency("minio", "phase5-secrets-init") ||
  !String(services.worker.command).includes(
    "HAPPYLEARN_PHASE5_WORKER_EXECUTABLE:-/app/happylearn-worker",
  ) ||
  services.app.image ||
  services.worker.image
) {
  process.exit(1);
}
NODE

require_literal "$makefile" 'e2e-phase5:'
require_literal "$makefile" 'bash scripts/e2e-phase5.sh'
require_literal "$makefile" 'bash scripts/e2e-phase5_contract_test.sh'
node -e '
  const packageJSON = require(process.argv[1]);
  if (packageJSON.scripts?.["e2e-phase5"] !== "make e2e-phase5") {
    process.exit(1);
  }
' "$package_json" ||
  fail 'package.json did not expose e2e-phase5'

printf 'phase 5 E2E harness contract: PASS\n'
