#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd -P)"
target="$repo_root/scripts/e2e-phase5.sh"
harness_lib="$repo_root/scripts/e2e-harness-lib.sh"
e2e_overlay="$repo_root/deploy/compose.phase5-e2e-live.yml"
makefile="$repo_root/Makefile"
package_json="$repo_root/package.json"

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
[[ -f "$makefile" && -f "$package_json" ]] ||
  fail 'Phase 5 command entrypoint files are absent'
bash -n "$target"

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
require_literal "$target" \
  'install -m 0400 "$license_file" "$restore_license_file"'
require_literal "$target" \
  '"$(portable_file_mode "$restore_license_file")" == 400'
require_literal "$target" \
  '--env "HAPPYLEARN_AISTOR_LICENSE_FILE=$restore_license_file"'
require_literal "$target" \
  '--target restore_live_controller'
require_literal "$target" \
  'prepare_restore_repository_access'
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
if grep -Fq -- '--group-add 0' "$target"; then
  fail 'restore controller assumed the Docker socket belongs to group 0'
fi
restore_proof_block="$(sed -n '/^run_restore_proof()/,/^}/p' "$target")"
if grep -Fq -- \
  'run_bounded 3600 bash "$script_dir/phase5-restore-verify.sh"' \
  <<<"$restore_proof_block"; then
  fail 'restore proof depended on host flock instead of the pinned controller'
fi
handoff_line="$(
  grep -n 'prepare_restore_repository_access' <<<"$restore_proof_block" |
    cut -d: -f1
)"
controller_line="$(
  grep -n 'docker_bounded 3600 run --rm --name "$restore_controller"' \
    <<<"$restore_proof_block" |
    cut -d: -f1
)"
[[ "$handoff_line" =~ ^[0-9]+$ &&
  "$controller_line" =~ ^[0-9]+$ &&
  "$handoff_line" -lt "$controller_line" ]] ||
  fail 'restore controller started before the repository ownership handoff'
if grep -Fq -- 'chmod 0400 "$license_file"' "$target"; then
  fail 'restore preparation mutated the caller-owned license file'
fi
require_literal "$target" \
  'compose_file="$repo_root/deploy/compose.dev.yml"'
require_literal "$target" \
  'compose_live_file="$repo_root/deploy/compose.backup-live.yml"'
require_literal "$target" \
  'compose_e2e_live_file="$repo_root/deploy/compose.phase5-e2e-live.yml"'
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
  'compose_live up --detach --no-build --no-deps postgres redis minio'
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
require_literal "$target" '--project=chromium'
require_literal "$target" 'run_phase5_mobile'
require_literal "$target" '--project=mobile --grep @phase5-mobile'
require_literal "$target" 'run_all_desktop'
for phase in phase1 phase2 phase3 phase4 phase5 phase4-mobile phase5-mobile; do
  require_literal "$target" "/artifacts/results/$phase"
done
require_literal "$target" 'run_backup_proof'
require_literal "$target" 'phase5-backup.sh'
require_literal "$target" 'run_restore_proof'
require_literal "$target" 'phase5-restore-verify.sh'
require_literal "$target" 'seed_phase5_browser_data'
require_literal "$target" 'INSERT INTO operational_alerts'
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
require_literal "$target" 'remove_coordinator_one_shots audit'
require_literal "$target" 'remove_coordinator_one_shots cleanup'
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
  'resource sample failed: browser=%s backup=%s monitor=%s'
require_literal "$target" 'run_backup_proof &'
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
require_literal "$target" 'worker_heavy_overlap'
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
require_literal "$target" 'for attempt in 1 2 3; do'
require_literal "$target" '((attempt == 3)) || sleep 1'
require_literal "$target" 'resource monitor failed: category=%s'
require_literal "$target" 'resource ephemeral identity failed: category=%s'
resource_sample_block="$(sed -n '/^run_resource_sample()/,/^}/p' "$target")"
for literal in \
  'run_resource_child "$resource_browser_status_file"' \
  'run_resource_child "$resource_backup_status_file"' \
  'browser_status="$(read_resource_child_status "$resource_browser_status_file")"' \
  'backup_status="$(read_resource_child_status "$resource_backup_status_file")"'; do
  grep -Fq -- "$literal" <<<"$resource_sample_block" ||
    fail "resource sample omitted durable child status handling: $literal"
done
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
for literal in \
  'roster_policy=backup_snapshot' \
  'validate_required_resource_roster "$roster_policy"' \
  'if ! resource_monitor_capture resource_state 15 inspect --format' \
  '--filter "id=${id}"' \
  '[[ -z "$current_listing" ]] && continue' \
  'validate_required_resource_roster running ||'; do
  grep -Fq -- "$literal" <<<"$resource_monitor_block" ||
    fail "resource monitor did not tolerate an owned ephemeral exit: $literal"
done
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
mkdir -p "$tmpdir/bin"
cat >"$tmpdir/bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${E2E_FAKE_DOCKER_LOG:?}"
if [[ "${E2E_FAKE_DOCKER_MODE:-unexpected}" == unexpected ]]; then
  touch "${E2E_UNEXPECTED_DOCKER_CALL:?}"
  exit 99
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
resource_evidence_version=1
owned_samples=true
saw_browser=true
saw_backup=true
saw_heavy=true
saw_worker=true
worker_heavy_overlap=false
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
        's/^worker_heavy_overlap=false$/worker_heavy_overlap=true/' \
        "$evidence"
      ;;
    unbounded)
      sed -i.bak \
        's/^configured_limits_complete=true$/configured_limits_complete=false/' \
        "$evidence"
      ;;
    configured_cpu)
      sed -i.bak \
        's/^peak_configured_cpu=2.000$/peak_configured_cpu=2.001/' \
        "$evidence"
      ;;
    configured_memory)
      sed -i.bak \
        's/^peak_configured_memory_mib=4096.000$/peak_configured_memory_mib=4096.001/' \
        "$evidence"
      ;;
    live_cpu)
      sed -i.bak \
        's/^peak_live_cpu_percent=200.000$/peak_live_cpu_percent=200.001/' \
        "$evidence"
      ;;
    live_memory)
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
  unowned missing_browser missing_backup missing_heavy missing_worker overlap \
  unbounded configured_cpu configured_memory live_cpu live_memory oom restart; do
  run_resource_evidence_probe "$mutation" 1
done
run_resource_evidence_probe browser_memory 1

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
  local record="$tmpdir/coordinator-one-shots"
  local one_shot_id='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
  local project='happylearn-phase5-live-a1b2c3d4e5f6'
  local owner='a1b2c3d4e5f6'
  local status=0
  printf '%s\n' "$one_shot_id" >"$record"
  chmod 0600 "$record"
  printf '%s\n' PATH=/usr/bin >"$tmpdir/coordinator-state.env"
  printf '%s\n' safe >"$tmpdir/coordinator-state.cmd"
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
run_coordinator_one_shot_probe coordinator_not_found audit 1 retained
run_coordinator_one_shot_probe coordinator_list_failure cleanup 1 retained
run_coordinator_one_shot_probe coordinator_inspect_failure cleanup 1 retained
run_coordinator_one_shot_probe coordinator_remove_failure cleanup 1 retained
run_coordinator_one_shot_probe coordinator_post_list_failure cleanup 1 retained
run_coordinator_one_shot_probe coordinator_collision cleanup 1 retained

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
