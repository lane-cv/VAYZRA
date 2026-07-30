#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd -P)"
source "$script_dir/e2e-harness-lib.sh"
repo_root="$(cd "$script_dir/.." && pwd -P)"

canonicalize_directory_target() {
  local input="${1:?path required}" probe part suffix='' base
  [[ "$input" == /* ]] || return 1
  probe="${input%/}"
  [[ -n "$probe" ]] || return 1
  while [[ ! -e "$probe" ]]; do
    part="$(basename "$probe")"
    [[ -n "$part" && "$part" != . && "$part" != .. ]] || return 1
    suffix="/$part$suffix"
    base="$(dirname "$probe")"
    [[ "$base" != "$probe" ]] || return 1
    probe="$base"
  done
  [[ -d "$probe" ]] || return 1
  base="$(cd "$probe" && pwd -P)" || return 1
  printf '%s%s\n' "$base" "$suffix"
}

license_file="${HAPPYLEARN_AISTOR_LICENSE_FILE:-}"
if [[ -z "$license_file" || "$license_file" != /* || ! -r "$license_file" ]]; then
  printf '%s\n' \
    'HAPPYLEARN_AISTOR_LICENSE_FILE must be an absolute readable AIStor Free license file' >&2
  exit 2
fi
license_file="$(cd "$(dirname "$license_file")" && pwd -P)/$(basename "$license_file")"

e2e_group="${HAPPYLEARN_E2E_GROUP:-all}"
case "$e2e_group" in
  all|phase5|phase5-mobile|recovery|resources) ;;
  *)
    printf '%s\n' \
      'HAPPYLEARN_E2E_GROUP must be all, phase5, phase5-mobile, recovery, or resources' >&2
    exit 2
    ;;
esac

probe_mode=''
probe_arguments=()
case "$#" in
  0) ;;
  2)
    [[ "$1" == '--audit-container-metadata' ]] || {
      printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
      exit 2
    }
    probe_mode=audit
    probe_arguments=("$2")
    ;;
  3)
    [[ "$1" == '--signal-contract-probe' ]] || {
      printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
      exit 2
    }
    probe_mode=signal
    probe_arguments=("$2" "$3")
    ;;
  4)
    [[ "$1" == '--coordinator-one-shot-probe' ]] || {
      printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
      exit 2
    }
    probe_mode=coordinator
    probe_arguments=("$2" "$3" "$4")
    ;;
  5)
    [[ "$1" == '--cleanup-container-ownership-probe' ]] || {
      printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
      exit 2
    }
    probe_mode=cleanup
    probe_arguments=("${@:2}")
    ;;
  *)
    printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
    exit 2
    ;;
esac

nonce="$(date +%s)-$$-$RANDOM"
compact_nonce="${nonce//-/}"
fixture_suffix="$(openssl rand -hex 6)"
[[ "$fixture_suffix" =~ ^[0-9a-f]{12}$ ]] || exit 2
live_project="happylearn-phase5-live-${fixture_suffix}"
prefix="happylearn_phase5_${nonce}"
network="${live_project}_happylearn"
postgres="${live_project}-postgres-1"
redis="${live_project}-redis-1"
primary_aistor="${live_project}-minio-1"
remote_s3="${prefix}_remote_s3"
app="${live_project}-app-1"
worker="${live_project}-worker-1"
processing_supervisor="$worker"
fake_ai="${prefix}_fake_ai"
backup="${prefix}_backup"
host_sample="${prefix}_host_sample"
browser_runner="${prefix}_browser_runner"
secret_init="${prefix}_secret_init"
data_init="${prefix}_data_init"
runner_init="${prefix}_runner_init"
fixture_runner="${prefix}_fixture_runner"
install_runner="${prefix}_install_runner"
admin_init="${prefix}_admin_init"
artifact_init="${prefix}_artifact_init"
artifact_write_probe="${prefix}_artifact_write_probe"
primary_volume="${live_project}_minio_data"
remote_volume="${prefix}_remote_data"
secret_volume="${prefix}_secrets"
runtime_secret_volume="${live_project}_phase5_runtime_secrets"
backup_secret_volume="${live_project}_backup_secrets"
postgres_tls_volume="${live_project}_postgres_tls"
app_secret_volume="${live_project}_app_secrets"
runner_volume="${prefix}_runner"
fixture_volume="${prefix}_fixtures"
repository_volume="${prefix}_repository"
state_volume="${prefix}_state"
app_image="${live_project}-app"
worker_image="happylearn-worker:phase5-base-${nonce}"
backup_image="happylearn-backup:phase5-${nonce}"
backup_base_image="happylearn-backup:phase5-base-${nonce}"
fake_ai_image="happylearn-fake-ai:phase5-${nonce}"
supervisor_image="${live_project}-worker"
host_sample_image="happylearn-host-sample:phase5-${nonce}"
playwright_image="mcr.microsoft.com/playwright:v1.57.0-noble"
init_image="alpine:3.22.1@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1"
aistor_image="quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120"
database=happylearn
compose_file="$repo_root/deploy/compose.dev.yml"
compose_live_file="$repo_root/deploy/compose.backup-live.yml"
compose_e2e_live_file="$repo_root/deploy/compose.phase5-e2e-live.yml"
artifact_init_script="$script_dir/init-e2e-artifacts.sh"

allowed_artifact_root="$repo_root/test-results"
artifact_input="${E2E_ARTIFACT_DIR:-$allowed_artifact_root/phase5}"
allowed_artifact_root_canonical="$(canonicalize_directory_target "$allowed_artifact_root")" || {
  printf '%s\n' 'repository test-results root cannot be resolved safely' >&2
  exit 2
}
artifact_dir="$(canonicalize_directory_target "$artifact_input")" || {
  printf '%s\n' \
    'E2E_ARTIFACT_DIR must be an absolute safe directory below repository test-results' >&2
  exit 2
}
if [[ "$allowed_artifact_root_canonical" != "$allowed_artifact_root" ||
      "$artifact_dir" == "$allowed_artifact_root_canonical" ||
      "$artifact_dir" != "$allowed_artifact_root_canonical"/* ]]; then
  printf '%s\n' \
    'E2E_ARTIFACT_DIR must be an absolute safe directory below repository test-results' >&2
  exit 2
fi

temp_base="$(cd "${TMPDIR:-/tmp}" && pwd -P)"
tmpdir="$(mktemp -d "$temp_base/happylearn-phase5-e2e.XXXXXX")"
chmod 0700 "$tmpdir"
secret_source_dir="$tmpdir/secrets"
backup_host_root="$tmpdir/backup"
ca_context_dir="$tmpdir/ca-context"
remote_cert_dir="$tmpdir/remote-certs"
offline_dir="$tmpdir/offline"
restore_control_dir="$tmpdir/restore-control"
restore_report_dir="$tmpdir/restore-report"
owned_container_ledger="$tmpdir/owned-containers.tsv"
owned_network_ledger="$tmpdir/owned-networks.tsv"
owned_volume_ledger="$tmpdir/owned-volumes.tsv"
owned_image_ledger="$tmpdir/owned-images.tsv"
coordinator_one_shot_file="$backup_host_root/coordinator-one-shots"
mkdir -m 0700 "$secret_source_dir" "$backup_host_root" \
  "$backup_host_root/secrets" "$backup_host_root/repository" \
  "$backup_host_root/state" "$backup_host_root/runtime-secrets" \
  "$backup_host_root/runtime-secrets/postgres" \
  "$backup_host_root/runtime-secrets/minio" \
  "$backup_host_root/runtime-secrets/app" \
  "$backup_host_root/runtime-secrets/worker" \
  "$backup_host_root/runtime-secrets/remote-s3" \
  "$ca_context_dir" "$remote_cert_dir" "$remote_cert_dir/CAs" \
  "$offline_dir" \
  "$restore_control_dir" "$restore_report_dir"
for ledger in \
  "$owned_container_ledger" "$owned_network_ledger" \
  "$owned_volume_ledger" "$owned_image_ledger"; do
  install -m 0600 /dev/null "$ledger"
done
install -m 0600 /dev/null "$coordinator_one_shot_file"
early_cleanup() {
  local exit_status=$?
  trap - EXIT HUP INT TERM
  rm -rf "$tmpdir"
  exit "$exit_status"
}
handle_early_signal() {
  local signal_status="${1:?signal status required}"
  trap '' HUP INT TERM
  trap - EXIT
  rm -rf "$tmpdir"
  exit "$signal_status"
}
trap early_cleanup EXIT
trap 'handle_early_signal 129' HUP
trap 'handle_early_signal 130' INT
trap 'handle_early_signal 143' TERM

umask 077
new_secret() {
  openssl rand -base64 36 | tr -d '\n'
}
for secret_name in \
  ai-master database-password object-access object-secret metrics-bearer \
  host-metrics-hmac restic-local-password restic-remote-password \
  restic-remote-access-key restic-remote-secret-key \
  webhook-authorization login-throttle provider-key control-token \
  admin-password student-password student-new-password; do
  new_secret >"$secret_source_dir/$secret_name"
done
printf '%s' '/repository' >"$secret_source_dir/restic-local-repository"
printf 's3:https://remote-s3:9000/happylearn-backups' \
  >"$secret_source_dir/restic-remote-repository"
printf '%s' 'https://hooks.example.invalid/happylearn-phase5' \
  >"$secret_source_dir/webhook-url"
chmod 0600 "$secret_source_dir/"*
teacher_credential_file="$secret_source_dir/restore-teacher.json"
printf '{"username":"admin","password":"%s"}\n' \
  "$(<"$secret_source_dir/admin-password")" >"$teacher_credential_file"
chmod 0400 "$teacher_credential_file"

secret_probe_containers=(
  "${prefix}_secret_probe_postgres"
  "${prefix}_secret_probe_minio"
  "${prefix}_secret_probe_app"
  "${prefix}_secret_probe_worker"
  "${prefix}_secret_probe_backup"
)
temporary_containers=(
  "$secret_init" "$data_init" "$runner_init" "$fixture_runner"
  "$install_runner" "$admin_init" "$artifact_init" "$artifact_write_probe"
  "$backup" "$host_sample" "$browser_runner"
  "${secret_probe_containers[@]}"
)
service_containers=(
  "$fake_ai" "$app" "$worker" "$remote_s3" "$primary_aistor" "$redis" "$postgres"
)
owned_volumes=(
  "$runner_volume" "$fixture_volume" "$secret_volume" "$repository_volume"
  "$state_volume" "$remote_volume" "$primary_volume" \
  "$runtime_secret_volume" "$backup_secret_volume" \
  "$postgres_tls_volume" "$app_secret_volume"
)
owned_images=(
  "$host_sample_image" "$supervisor_image" "$fake_ai_image" "$backup_image"
  "$backup_base_image"
  "$worker_image" "$app_image"
)

compose_live() {
  HAPPYLEARN_BACKUP_LIVE_ROOT="$backup_host_root" \
  HAPPYLEARN_BACKUP_IMAGE="$backup_image" \
  HAPPYLEARN_AISTOR_LICENSE_FILE="$license_file" \
  HAPPYLEARN_BACKUP_DATABASE_NAME=happylearn \
  HAPPYLEARN_PHASE5_E2E_OWNER="$fixture_suffix" \
    docker compose \
      --project-name "$live_project" \
      --file "$compose_file" \
      --file "$compose_live_file" \
      --file "$compose_e2e_live_file" \
      "$@"
}

docker_capture_bounded() {
  local output_variable="${1:?output variable required}"
  local seconds="${2:?deadline required}"
  shift 2
  local capture_file="$tmpdir/docker-capture"
  local captured_value
  install -m 0600 /dev/null "$capture_file"
  docker_bounded "$seconds" "$@" >"$capture_file" || return 1
  captured_value="$(<"$capture_file")"
  rm -f "$capture_file"
  printf -v "$output_variable" '%s' "$captured_value"
}

record_owned_container() {
  local name="${1:?container name required}"
  local expected_project="${2:?project required}"
  local expected_owner="${3:?owner required}"
  local metadata id inspected_name inspected_project inspected_owner extra
  docker_capture_bounded metadata 15 inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$name" || return 1
  IFS='|' read -r id inspected_name inspected_project inspected_owner extra \
    <<<"$metadata"
  [[ "$id" =~ ^[a-f0-9]{64}$ &&
    "$inspected_name" == "/$name" &&
    "$inspected_project" == "$expected_project" &&
    "$inspected_owner" == "$expected_owner" &&
    -z "$extra" ]] ||
    return 1
  printf '%s\t%s\t%s\t%s\n' \
    "$name" "$id" "$expected_project" "$expected_owner" \
    >>"$owned_container_ledger"
}

remove_owned_container_if_match() {
  local name="${1:?container name required}"
  local expected_id="${2:?container id required}"
  local expected_project="${3:?project required}"
  local expected_owner="${4:?owner required}"
  local metadata id inspected_name inspected_project inspected_owner extra
  docker_capture_bounded metadata 15 inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$name" 2>/dev/null || return 0
  IFS='|' read -r id inspected_name inspected_project inspected_owner extra \
    <<<"$metadata"
  [[ "$id" == "$expected_id" &&
    "$inspected_name" == "/$name" &&
    "$inspected_project" == "$expected_project" &&
    "$inspected_owner" == "$expected_owner" &&
    -z "$extra" ]] ||
    return 0
  docker_bounded 30 rm -f "$expected_id" >/dev/null
}

remove_active_temporary_containers() {
  local name metadata id inspected_name inspected_project inspected_owner extra
  for name in "${temporary_containers[@]}"; do
    if ! docker_capture_bounded metadata 15 inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$name" 2>/dev/null; then
      continue
    fi
    IFS='|' read -r id inspected_name inspected_project inspected_owner extra \
      <<<"$metadata"
    [[ "$id" =~ ^[a-f0-9]{64}$ &&
      "$inspected_name" == "/$name" &&
      "$inspected_project" == "$live_project" &&
      "$inspected_owner" == "$fixture_suffix" &&
      -z "$extra" ]] ||
      continue
    docker_bounded 30 rm -f "$id" >/dev/null 2>&1 || true
  done
}

record_owned_network() {
  local name="${1:?network name required}"
  local metadata id inspected_name inspected_owner extra
  docker_capture_bounded metadata 15 network inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$name" || return 1
  IFS='|' read -r id inspected_name inspected_owner extra <<<"$metadata"
  [[ "$id" =~ ^[a-f0-9]{64}$ &&
    "$inspected_name" == "$name" &&
    "$inspected_owner" == "$fixture_suffix" &&
    -z "$extra" ]] ||
    return 1
  printf '%s\t%s\t%s\n' "$name" "$id" "$fixture_suffix" \
    >>"$owned_network_ledger"
}

record_owned_volume() {
  local name="${1:?volume name required}"
  local metadata inspected_name inspected_owner extra
  docker_capture_bounded metadata 15 volume inspect --format \
      '{{.Name}}|{{index .Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$name" || return 1
  IFS='|' read -r inspected_name inspected_owner extra <<<"$metadata"
  [[ "$inspected_name" == "$name" &&
    "$inspected_owner" == "$fixture_suffix" &&
    -z "$extra" ]] ||
    return 1
  printf '%s\t%s\n' "$name" "$fixture_suffix" >>"$owned_volume_ledger"
}

record_owned_image() {
  local reference="${1:?image reference required}"
  local id
  docker_capture_bounded id 15 image inspect --format '{{.Id}}' \
    "$reference" || return 1
  [[ "$id" =~ ^sha256:[a-f0-9]{64}$ ]] || return 1
  printf '%s\t%s\n' "$reference" "$id" >>"$owned_image_ledger"
}

artifact_target_is_safe() {
  local current
  [[ -d "$artifact_dir" && ! -L "$artifact_dir" ]] || return 1
  current="$(canonicalize_directory_target "$artifact_dir")" || return 1
  [[ "$current" == "$artifact_dir" &&
     "$current" != "$allowed_artifact_root_canonical" &&
     "$current" == "$allowed_artifact_root_canonical"/* ]]
}

audit_container_metadata() {
  local container metadata env_entries host_config entry key value
  local secret_path secret_value
  [[ "$#" -ge 1 ]] || return 1
  for container in "$@"; do
    docker_capture_bounded metadata 15 inspect --format \
        '{{json .Config.Env}}|{{json .Config.Entrypoint}}|{{json .Config.Cmd}}' \
        "$container" || return 1
    if grep -Fq 'phase5-e2e-secret-marker' <<<"$metadata"; then
      printf 'Phase 5 runtime metadata contained the secret canary\n' >&2
      return 1
    fi
    docker_capture_bounded env_entries 15 inspect --format \
      '{{range .Config.Env}}{{println .}}{{end}}' "$container" ||
      return 1
    while IFS= read -r entry; do
      [[ -n "$entry" && "$entry" == *=* ]] || continue
      key="${entry%%=*}"
      value="${entry#*=}"
      case "$key" in
        POSTGRES_PASSWORD_FILE)
          [[ "$value" == /run/phase5-secrets/password ]] || return 1
          ;;
        HAPPYLEARN_METRICS_BEARER_SECRET_FILE)
          [[ "$value" == /run/phase5-secrets/metrics-bearer ]] || return 1
          ;;
        HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE)
          [[ "$value" == /run/phase5-secrets/host-metrics-hmac ]] || return 1
          ;;
        HAPPYLEARN_WEBHOOK_URL_SECRET_FILE)
          [[ "$value" == /run/phase5-secrets/webhook-url ]] || return 1
          ;;
        HAPPYLEARN_WEBHOOK_AUTHORIZATION_SECRET_FILE)
          [[ "$value" == /run/phase5-secrets/webhook-authorization ]] ||
            return 1
          ;;
        *_FILE)
          printf 'Phase 5 runtime metadata used an unapproved file environment\n' >&2
          return 1
          ;;
        POSTGRES_PASSWORD|MINIO_ROOT_USER|MINIO_ROOT_PASSWORD|\
          HAPPYLEARN_DATABASE_URL|HAPPYLEARN_LOGIN_THROTTLE_SECRET|\
          HAPPYLEARN_AI_MASTER_KEY|HAPPYLEARN_MINIO_ACCESS_KEY|\
          HAPPYLEARN_MINIO_SECRET_KEY|HAPPYLEARN_METRICS_BEARER_SECRET|\
          HAPPYLEARN_HOST_METRICS_HMAC_SECRET|HAPPYLEARN_WEBHOOK_URL|\
          HAPPYLEARN_WEBHOOK_AUTHORIZATION|RESTIC_PASSWORD|\
          AWS_ACCESS_KEY_ID|AWS_SECRET_ACCESS_KEY|E2E_ADMIN_PASSWORD|\
          E2E_STUDENT_PASSWORD|E2E_STUDENT_NEW_PASSWORD|\
          E2E_AI_PROVIDER_KEY|E2E_AI_PROCESSING_CONTROL_TOKEN)
          printf 'Phase 5 runtime metadata contained a secret environment key\n' >&2
          return 1
          ;;
      esac
    done <<<"$env_entries"
    for secret_path in "$secret_source_dir"/*; do
      [[ -f "$secret_path" && ! -L "$secret_path" ]] || continue
      secret_value="$(<"$secret_path")"
      [[ ${#secret_value} -ge 8 ]] || continue
      if grep -Fq "$secret_value" <<<"$metadata"; then
        printf 'Phase 5 runtime metadata contained a literal secret\n' >&2
        return 1
      fi
    done
    docker_capture_bounded host_config 15 inspect --format \
        '{{json .HostConfig.Binds}}|{{json .HostConfig.Privileged}}|{{json .HostConfig.NetworkMode}}' \
        "$container" || return 1
    if grep -Fq '/var/run/docker.sock' <<<"$host_config" ||
      [[ "$host_config" == *'|true|'* ||
        "$host_config" == *'|"host"'* ]]; then
      printf 'Phase 5 runtime metadata contained unsafe host access\n' >&2
      return 1
    fi
  done
}

portable_file_mode() {
  if stat -c '%a' "$1" >/dev/null 2>&1; then
    stat -c '%a' "$1"
  else
    stat -f '%Lp' "$1"
  fi
}

portable_file_owner() {
  if stat -c '%u' "$1" >/dev/null 2>&1; then
    stat -c '%u' "$1"
  else
    stat -f '%u' "$1"
  fi
}

remove_coordinator_one_shots() {
  local audit_required="${1:?audit mode required}"
  local expected_id metadata inspected_id inspected_name inspected_project
  local inspected_oneoff inspected_owner extra removal_status=0
  [[ "$audit_required" == audit || "$audit_required" == cleanup ]] ||
    return 1
  [[ -f "$coordinator_one_shot_file" &&
    ! -L "$coordinator_one_shot_file" &&
    "$(portable_file_mode "$coordinator_one_shot_file")" == 600 &&
    "$(portable_file_owner "$coordinator_one_shot_file")" == "$(id -u)" ]] ||
    return 1
  while IFS= read -r expected_id; do
    [[ -n "$expected_id" ]] || continue
    [[ "$expected_id" =~ ^[a-f0-9]{64}$ ]] || {
      removal_status=1
      continue
    }
    if ! docker_capture_bounded metadata 15 inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.oneoff"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$expected_id" 2>/dev/null; then
      [[ "$audit_required" == cleanup ]] || removal_status=1
      continue
    fi
    IFS='|' read -r inspected_id inspected_name inspected_project \
      inspected_oneoff inspected_owner extra <<<"$metadata"
    if [[ "$inspected_id" != "$expected_id" ||
      "$inspected_name" != /* ||
      "$inspected_project" != "$live_project" ||
      "$inspected_oneoff" != True ||
      "$inspected_owner" != "$fixture_suffix" ||
      -n "$extra" ]]; then
      removal_status=1
      continue
    fi
    if [[ "$audit_required" == audit ]] &&
      ! audit_container_metadata "$expected_id"; then
      removal_status=1
      continue
    fi
    docker_bounded 30 rm -f "$expected_id" >/dev/null || {
      removal_status=1
      continue
    }
    if docker_bounded 15 inspect "$expected_id" >/dev/null 2>&1; then
      removal_status=1
    fi
  done <"$coordinator_one_shot_file"
  if ((removal_status == 0)); then
    install -m 0600 /dev/null "$coordinator_one_shot_file" ||
      removal_status=1
  fi
  return "$removal_status"
}

diagnostics() {
  local staging_dir="$tmpdir/diagnostics"
  local staging_log="$staging_dir/containers.log"
  local final_log="$artifact_dir/containers.log"
  local publish_tmp="$artifact_dir/.containers.log.${compact_nonce}.tmp"
  artifact_target_is_safe || return 0
  rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
  rm -rf "$staging_dir" || return 0
  install -d -m 0700 "$staging_dir" || return 0
  install -m 0600 /dev/null "$staging_log" || return 0
  printf 'diagnostics_version=1\n' >"$staging_log" || return 0
  for container in "${service_containers[@]}"; do
    if docker_bounded 15 ps -a --format '{{.Names}}' |
      grep -Fxq "$container"; then
      printf 'container=%s\n' "$container" >>"$staging_log" || true
      docker_bounded 15 inspect \
        --format 'state_status={{.State.Status}}' "$container" \
        >>"$staging_log" 2>&1 || true
      docker_bounded 15 inspect \
        --format 'exit_code={{.State.ExitCode}}' "$container" \
        >>"$staging_log" 2>&1 || true
      docker_bounded 15 inspect \
        --format 'oom_killed={{.State.OOMKilled}}' "$container" \
        >>"$staging_log" 2>&1 || true
      docker_bounded 20 logs --tail 200 "$container" \
        >>"$staging_log" 2>&1 || true
    fi
  done
  if ! bash "$script_dir/sanitize-e2e-artifacts.sh" "$staging_dir"; then
    rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
    return 0
  fi
  if ! "$script_dir/publish-e2e-diagnostics.sh" \
    "$staging_log" "$artifact_dir" "$compact_nonce"; then
    rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
  fi
}

cleanup() {
  local exit_status="${1:-$?}"
  local sanitizer_status=0
  local recorded_name recorded_id recorded_project recorded_owner extra
  local metadata inspected_id inspected_name inspected_owner
  local recorded_reference
  trap - EXIT HUP INT TERM
  set +e
  cancel_bounded_command || true
  remove_active_temporary_containers || true
  if ((exit_status != 0)); then diagnostics || true; fi
  if artifact_target_is_safe; then
    bash "$script_dir/sanitize-e2e-artifacts.sh" "$artifact_dir" ||
      sanitizer_status=$?
  else
    sanitizer_status=2
  fi
  if ((sanitizer_status != 0)); then
    if artifact_target_is_safe; then
      find "$artifact_dir" -mindepth 1 -delete 2>/dev/null || true
    fi
    if ((exit_status == 0)); then exit_status=$sanitizer_status; fi
  fi
  remove_coordinator_one_shots cleanup || true
  while IFS=$'\t' read -r recorded_name recorded_id recorded_project \
    recorded_owner extra; do
    [[ -n "$recorded_name" && -n "$recorded_id" && -z "$extra" ]] || continue
    remove_owned_container_if_match \
      "$recorded_name" "$recorded_id" "$recorded_project" "$recorded_owner" ||
      true
  done <"$owned_container_ledger"
  while IFS=$'\t' read -r recorded_name recorded_id recorded_owner extra; do
    [[ -n "$recorded_name" && -n "$recorded_id" && -z "$extra" ]] || continue
    docker_capture_bounded metadata 15 network inspect --format \
        '{{.Id}}|{{.Name}}|{{index .Labels "io.happylearn.phase5.e2e-owner"}}' \
        "$recorded_name" 2>/dev/null || continue
    IFS='|' read -r inspected_id inspected_name inspected_owner extra \
      <<<"$metadata"
    if [[ "$inspected_id" == "$recorded_id" &&
      "$inspected_name" == "$recorded_name" &&
      "$inspected_owner" == "$recorded_owner" &&
      -z "$extra" ]]; then
      docker_bounded 30 network rm "$recorded_id" >/dev/null 2>&1 || true
    fi
  done <"$owned_network_ledger"
  while IFS=$'\t' read -r recorded_name recorded_owner extra; do
    [[ -n "$recorded_name" && -z "$extra" ]] || continue
    docker_capture_bounded metadata 15 volume inspect --format \
        '{{.Name}}|{{index .Labels "io.happylearn.phase5.e2e-owner"}}' \
        "$recorded_name" 2>/dev/null || continue
    IFS='|' read -r inspected_name inspected_owner extra <<<"$metadata"
    if [[ "$inspected_name" == "$recorded_name" &&
      "$inspected_owner" == "$recorded_owner" &&
      -z "$extra" ]]; then
      docker_bounded 30 volume rm "$recorded_name" >/dev/null 2>&1 || true
    fi
  done <"$owned_volume_ledger"
  while IFS=$'\t' read -r recorded_reference recorded_id extra; do
    [[ -n "$recorded_reference" && -n "$recorded_id" && -z "$extra" ]] ||
      continue
    docker_capture_bounded inspected_id 15 image inspect --format '{{.Id}}' \
      "$recorded_reference" 2>/dev/null || continue
    if [[ "$inspected_id" == "$recorded_id" ]]; then
      docker_bounded 60 image rm "$recorded_reference" \
        >/dev/null 2>&1 || true
    fi
  done <"$owned_image_ledger"
  rm -rf "$tmpdir" || true
  exit "$exit_status"
}
handle_harness_signal() {
  local signal_status="${1:?signal status required}"
  [[ "$signal_status" =~ ^(129|130|143)$ ]] || return 1
  trap '' HUP INT TERM
  cancel_bounded_command || true
  trap - EXIT
  cleanup "$signal_status"
}
if [[ -z "$probe_mode" || "$probe_mode" == signal ]]; then
  trap cleanup EXIT
  trap 'handle_harness_signal 129' HUP
  trap 'handle_harness_signal 130' INT
  trap 'handle_harness_signal 143' TERM
fi

wait_for() {
  local label="$1" container="$2"
  shift 2
  local attempt
  for attempt in $(seq 1 120); do
    if docker_bounded 15 "$@"; then return 0; fi
    if [[ "$(docker_bounded 15 inspect --format '{{.State.Running}}' \
      "$container" 2>/dev/null || true)" != true ]]; then
      printf '%s exited before becoming ready\n' "$label" >&2
      return 1
    fi
    sleep 1
  done
  printf 'timed out waiting for %s\n' "$label" >&2
  return 1
}

create_fixture_ca() {
  openssl genrsa -out "$offline_dir/ca.key" 2048 >/dev/null 2>&1
  openssl req -x509 -new -sha256 -days 2 \
    -key "$offline_dir/ca.key" \
    -subj "/CN=HappyLearn Phase 5 E2E CA ${fixture_suffix}" \
    -out "$offline_dir/ca.crt" >/dev/null 2>&1
  openssl genrsa -out "$remote_cert_dir/private.key" 2048 >/dev/null 2>&1
  openssl req -new -sha256 \
    -key "$remote_cert_dir/private.key" \
    -subj '/CN=remote-s3' \
    -out "$offline_dir/remote.csr" >/dev/null 2>&1
  printf '%s\n' \
    'subjectAltName=DNS:remote-s3' \
    'extendedKeyUsage=serverAuth' \
    >"$offline_dir/remote.ext"
  openssl x509 -req -sha256 -days 2 \
    -in "$offline_dir/remote.csr" \
    -CA "$offline_dir/ca.crt" \
    -CAkey "$offline_dir/ca.key" \
    -CAcreateserial \
    -extfile "$offline_dir/remote.ext" \
    -out "$remote_cert_dir/public.crt" >/dev/null 2>&1
  install -m 0444 "$offline_dir/ca.crt" "$remote_cert_dir/CAs/ca.crt"
  install -m 0444 "$offline_dir/ca.crt" "$ca_context_dir/ca.crt"
  chmod 0400 "$remote_cert_dir/private.key"
}

build_images() {
  local reference
  for reference in "${owned_images[@]}"; do
    if docker_bounded 15 image inspect "$reference" >/dev/null 2>&1; then
      printf 'randomized Phase 5 image reference collided: %s\n' \
        "$reference" >&2
      return 1
    fi
  done
  docker_bounded 900 build -t "$app_image" "$repo_root"
  record_owned_image "$app_image"
  docker_bounded 1200 build -f "$repo_root/Dockerfile.worker" \
    -t "$worker_image" "$repo_root"
  record_owned_image "$worker_image"
  docker_bounded 1200 build -f "$repo_root/Dockerfile.backup" \
    -t "$backup_base_image" "$repo_root"
  record_owned_image "$backup_base_image"
  docker_bounded 600 build \
    --build-arg "HAPPYLEARN_BACKUP_BASE_IMAGE=${backup_base_image}" \
    -f "$repo_root/deploy/Dockerfile.backup-live-ca" \
    -t "$backup_image" "$ca_context_dir"
  record_owned_image "$backup_image"
  docker_bounded 900 build -f "$repo_root/Dockerfile.fake-ai" \
    -t "$fake_ai_image" "$repo_root"
  record_owned_image "$fake_ai_image"
  docker_bounded 900 build -f "$repo_root/Dockerfile.e2e-worker" \
    --build-arg "WORKER_IMAGE=$worker_image" \
    -t "$supervisor_image" "$repo_root"
  record_owned_image "$supervisor_image"
  docker_bounded 900 build -f - -t "$host_sample_image" "$repo_root" <<'DOCKERFILE'
FROM golang:1.26.5-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/host-sampler cmd/host-sampler
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/host-sampler ./cmd/host-sampler
FROM debian:12.12-slim
RUN apt-get update && apt-get install --no-install-recommends -y ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN useradd --uid 10004 --gid 0 --no-create-home --shell /usr/sbin/nologin host-sample
COPY --from=build --chown=10004:0 /out/host-sampler /app/host-sampler
RUN chmod 0555 /app/host-sampler
USER 10004:0
ENTRYPOINT ["/app/host-sampler"]
DOCKERFILE
  record_owned_image "$host_sample_image"
  docker_bounded 60 run --rm --network none \
    --entrypoint /usr/local/bin/age-keygen \
    "$backup_image" >"$secret_source_dir/age-identity" 2>/dev/null
  chmod 0600 "$secret_source_dir/age-identity"
  HAPPYLEARN_BACKUP_AGE_RECIPIENT="$(
    docker_bounded 60 run --rm --interactive --network none \
      --entrypoint /usr/local/bin/age-keygen \
      "$backup_image" -y <"$secret_source_dir/age-identity"
  )"
  [[ "$HAPPYLEARN_BACKUP_AGE_RECIPIENT" =~ ^age1[0-9a-z]+$ ]]
  export HAPPYLEARN_BACKUP_AGE_RECIPIENT
}

populate_live_secret_sources() {
  install -m 0400 "$secret_source_dir/database-password" \
    "$backup_host_root/secrets/database_password"
  install -m 0400 "$secret_source_dir/restic-local-repository" \
    "$backup_host_root/secrets/local_repository"
  install -m 0400 "$secret_source_dir/restic-local-password" \
    "$backup_host_root/secrets/local_password"
  install -m 0400 "$secret_source_dir/restic-remote-repository" \
    "$backup_host_root/secrets/remote_repository"
  install -m 0400 "$secret_source_dir/restic-remote-password" \
    "$backup_host_root/secrets/remote_password"
  install -m 0400 "$secret_source_dir/restic-remote-access-key" \
    "$backup_host_root/secrets/remote_access_key_id"
  install -m 0400 "$secret_source_dir/restic-remote-secret-key" \
    "$backup_host_root/secrets/remote_secret_access_key"

  install -m 0400 "$secret_source_dir/database-password" \
    "$backup_host_root/runtime-secrets/postgres/password"
  {
    printf 'MINIO_ROOT_USER=%s\n' "$(<"$secret_source_dir/object-access")"
    printf 'MINIO_ROOT_PASSWORD=%s\n' "$(<"$secret_source_dir/object-secret")"
  } >"$backup_host_root/runtime-secrets/minio/runtime.env"
  {
    printf 'HAPPYLEARN_DATABASE_URL=postgres://happylearn:%s@postgres:5432/happylearn?sslmode=disable\n' \
      "$(<"$secret_source_dir/database-password")"
    printf 'HAPPYLEARN_LOGIN_THROTTLE_SECRET=%s\n' \
      "$(<"$secret_source_dir/login-throttle")"
    printf 'HAPPYLEARN_AI_MASTER_KEY=%s\n' \
      "$(<"$secret_source_dir/ai-master")"
    printf 'HAPPYLEARN_MINIO_ACCESS_KEY=%s\n' \
      "$(<"$secret_source_dir/object-access")"
    printf 'HAPPYLEARN_MINIO_SECRET_KEY=%s\n' \
      "$(<"$secret_source_dir/object-secret")"
    printf '%s\n' \
      'HAPPYLEARN_PUBLIC_ORIGIN=http://app:8080' \
      'HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=true'
  } >"$backup_host_root/runtime-secrets/app/runtime.env"
  for name in metrics-bearer host-metrics-hmac webhook-url \
    webhook-authorization; do
    install -m 0400 "$secret_source_dir/$name" \
      "$backup_host_root/runtime-secrets/app/$name"
  done
  {
    printf 'HAPPYLEARN_DATABASE_URL=postgres://happylearn:%s@postgres:5432/happylearn?sslmode=disable\n' \
      "$(<"$secret_source_dir/database-password")"
    printf 'HAPPYLEARN_LOGIN_THROTTLE_SECRET=%s\n' \
      "$(<"$secret_source_dir/login-throttle")"
    printf 'HAPPYLEARN_MINIO_ACCESS_KEY=%s\n' \
      "$(<"$secret_source_dir/object-access")"
    printf 'HAPPYLEARN_MINIO_SECRET_KEY=%s\n' \
      "$(<"$secret_source_dir/object-secret")"
    printf 'E2E_AI_PROCESSING_CONTROL_TOKEN=%s\n' \
      "$(<"$secret_source_dir/control-token")"
    printf '%s\n' \
      'HAPPYLEARN_PHASE5_WORKER_EXECUTABLE=/app/e2e-processing-supervisor'
  } >"$backup_host_root/runtime-secrets/worker/runtime.env"
  {
    printf 'MINIO_ROOT_USER=%s\n' \
      "$(<"$secret_source_dir/restic-remote-access-key")"
    printf 'MINIO_ROOT_PASSWORD=%s\n' \
      "$(<"$secret_source_dir/restic-remote-secret-key")"
    printf '%s\n' 'SSL_CERT_FILE=/certs/CAs/ca.crt'
  } >"$backup_host_root/runtime-secrets/remote-s3/runtime.env"
  chmod 0400 \
    "$backup_host_root/runtime-secrets/minio/runtime.env" \
    "$backup_host_root/runtime-secrets/app/runtime.env" \
    "$backup_host_root/runtime-secrets/worker/runtime.env" \
    "$backup_host_root/runtime-secrets/remote-s3/runtime.env"
}

initialize_artifact_directory() {
  docker_bounded 120 run --rm --name "$artifact_init" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --network none --read-only --user 0:0 --cap-drop ALL \
    --cap-add CHOWN --cap-add DAC_OVERRIDE \
    --security-opt no-new-privileges --memory 16m --cpus .05 \
    --tmpfs /tmp:rw,noexec,nosuid,size=4m \
    -v "$artifact_init_script:/init-e2e-artifacts.sh:ro" \
    -v "$artifact_dir:/artifacts" \
    "$init_image" /bin/sh /init-e2e-artifacts.sh /artifacts
  docker_bounded 60 run --rm --name "$artifact_write_probe" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --network none --read-only --user 1000:1000 --cap-drop ALL \
    --security-opt no-new-privileges --memory 16m --cpus .05 \
    --mount "type=bind,src=$artifact_dir/results,dst=/artifacts" \
    "$init_image" /bin/sh -eu -c \
    'printf "%s\n" PHASE5_ARTIFACT_WRITE_PROBE > /artifacts/.write-probe; test "$(stat -c "%u:%g:%a" /artifacts/.write-probe)" = 1000:1000:644; rm /artifacts/.write-probe'
}

initialize_resources() {
  install -d -m 0700 "$artifact_dir"
  artifact_target_is_safe || {
    printf '%s\n' 'E2E_ARTIFACT_DIR changed during validation' >&2
    return 2
  }
  initialize_artifact_directory
  docker_bounded 60 network create --internal \
    --label "com.docker.compose.project=${live_project}" \
    --label 'com.docker.compose.network=happylearn' \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    "$network" >/dev/null
  record_owned_network "$network"
  local volume compose_volume=''
  for volume in "${owned_volumes[@]}"; do
    compose_volume=''
    case "$volume" in
      "$primary_volume") compose_volume=minio_data ;;
      "$runtime_secret_volume") compose_volume=phase5_runtime_secrets ;;
      "$backup_secret_volume") compose_volume=backup_secrets ;;
      "$postgres_tls_volume") compose_volume=postgres_tls ;;
      "$app_secret_volume") compose_volume=app_secrets ;;
    esac
    if [[ -n "$compose_volume" ]]; then
      docker_bounded 60 volume create \
        --label "com.docker.compose.project=${live_project}" \
        --label "com.docker.compose.volume=${compose_volume}" \
        --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
        "$volume" >/dev/null
    else
      docker_bounded 60 volume create \
        --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
        "$volume" >/dev/null
    fi
    record_owned_volume "$volume"
  done
  docker_bounded 120 run --rm --name "$data_init" --network none \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 0:0 --cap-drop ALL --cap-add CHOWN \
    --security-opt no-new-privileges --memory 32m --cpus .05 \
    -v "$primary_volume:/primary" -v "$remote_volume:/remote" \
    -v "$repository_volume:/repository" -v "$state_volume:/state" \
    "$init_image" /bin/sh -eu -c \
    'chmod 0750 /primary /remote; chown 1000:0 /primary /remote; chmod 0700 /repository /state; chown 10003:0 /repository /state; test "$(stat -c "%u:%g:%a" /primary)" = 1000:0:750; test "$(stat -c "%u:%g:%a" /remote)" = 1000:0:750; test "$(stat -c "%u:%g:%a" /repository)" = 10003:0:700; test "$(stat -c "%u:%g:%a" /state)" = 10003:0:700'
  docker_bounded 120 run --rm --name "$runner_init" --network none \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 0:0 --cap-drop ALL --cap-add CHOWN \
    --security-opt no-new-privileges --memory 32m --cpus .05 \
    -v "$runner_volume:/workspace" -v "$fixture_volume:/fixtures" \
    "$init_image" /bin/sh -eu -c \
    'chmod 0700 /workspace /fixtures; chown 1000:1000 /workspace /fixtures; test "$(stat -c "%u:%g:%a" /workspace)" = 1000:1000:700; test "$(stat -c "%u:%g:%a" /fixtures)" = 1000:1000:700'
}

initialize_secret_volume() {
  docker_bounded 120 run --rm --name "$secret_init" --network none \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 0:0 --cap-drop ALL --cap-add CHOWN \
    --cap-add DAC_READ_SEARCH --security-opt no-new-privileges \
    --memory 32m --cpus .05 --tmpfs /tmp:rw,noexec,nosuid,size=4m \
    -v "$secret_source_dir:/secrets:ro" -v "$secret_volume:/owned-secrets" \
    "$init_image" /bin/sh -eu -c '
      install_consumer() {
        consumer="$1"; owner="$2"; shift 2
        directory="/owned-secrets/$consumer"
        mkdir "$directory"
        for name in "$@"; do
          target="$directory/$name"
          cp "/secrets/$name" "$target"
          chmod 0400 "$target"
          chown "$owner" "$target"
          test "$(stat -c "%u:%g:%a" "$target")" = "$owner:400"
        done
        chmod 0500 "$directory"
        chown "$owner" "$directory"
        test "$(stat -c "%u:%g:%a" "$directory")" = "$owner:500"
      }
      install_consumer postgres 999:999 database-password
      install_consumer primary-aistor 1000:0 object-access object-secret
      install_consumer remote-s3 1000:0 restic-remote-access-key restic-remote-secret-key
      install_consumer app 10001:10001 ai-master database-password object-access object-secret metrics-bearer host-metrics-hmac webhook-url webhook-authorization login-throttle
      install_consumer worker 10002:10002 database-password object-access object-secret login-throttle
      install_consumer backup 10003:0 database-password restic-local-repository restic-local-password restic-remote-repository restic-remote-password restic-remote-access-key restic-remote-secret-key age-identity
      install_consumer fake-ai 10003:10003 provider-key
      install_consumer supervisor 10002:10002 control-token
      install_consumer browser 1000:1000 admin-password student-password student-new-password provider-key control-token
      install_consumer host-sample 10004:0 metrics-bearer host-metrics-hmac
      chmod 0500 /owned-secrets
    '
}

verify_secret_consumer_reads() {
  local probe_name uid consumer secret expected
  while IFS='|' read -r probe_name uid consumer secret expected; do
    docker_bounded 60 run --rm --name "${prefix}_${probe_name}" \
      --label "com.docker.compose.project=${live_project}" \
      --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
      --network none --read-only --user "$uid" --cap-drop ALL \
      --security-opt no-new-privileges --memory 16m --cpus .05 \
      --mount "type=volume,src=$secret_volume,dst=/run/secrets,volume-subpath=$consumer,readonly" \
      "$init_image" /bin/sh -eu -c \
      'test "$(stat -c "%u:%g:%a" "/run/secrets/'"$secret"'")" = "'"$expected"':400"; test -s "/run/secrets/'"$secret"'"'
  done <<'PROBES'
secret_probe_postgres|999:999|postgres|database-password|999:999
secret_probe_minio|1000:0|primary-aistor|object-access|1000:0
secret_probe_app|10001:10001|app|ai-master|10001:10001
secret_probe_worker|10002:10002|worker|database-password|10002:10002
secret_probe_backup|10003:0|backup|restic-local-password|10003:0
PROBES
  : '999:999' '1000:0' '10001:10001' '10002:10002' '10003:0'
}

start_dependencies() {
  if ! compose_live up --no-build --abort-on-container-exit \
    --exit-code-from phase5-secrets-init phase5-secrets-init; then
    record_owned_container \
      "${live_project}-phase5-secrets-init-1" \
      "$live_project" "$fixture_suffix" || true
    return 1
  fi
  record_owned_container \
    "${live_project}-phase5-secrets-init-1" \
    "$live_project" "$fixture_suffix"
  if ! compose_live up --detach --no-build postgres redis minio; then
    for name in \
      "${live_project}-postgres-tls-init-1" \
      "${live_project}-minio-data-init-1" \
      "$postgres" "$redis" "$primary_aistor"; do
      record_owned_container "$name" "$live_project" "$fixture_suffix" || true
    done
    return 1
  fi
  for name in \
    "${live_project}-postgres-tls-init-1" \
    "${live_project}-minio-data-init-1" \
    "$postgres" "$redis" "$primary_aistor"; do
    record_owned_container "$name" "$live_project" "$fixture_suffix"
  done
  docker_bounded 60 run -d --name "$remote_s3" --network "$network" \
    --network-alias remote-s3 --read-only --user 1000:0 --cap-drop ALL \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --security-opt no-new-privileges --memory 384m --cpus .1 \
    --tmpfs /tmp:rw,noexec,nosuid,size=16m,uid=1000,gid=0,mode=0700 \
    -v "$remote_volume:/data" -v "$license_file:/minio.license:ro" \
    --mount "type=volume,src=$runtime_secret_volume,dst=/run/phase5-secrets,volume-subpath=remote-s3,readonly" \
    --mount "type=bind,src=$remote_cert_dir,dst=/certs,readonly" \
    --entrypoint /bin/sh "$aistor_image" -eu -c \
    'set -a; . /run/phase5-secrets/runtime.env; set +a; exec minio server /data --address :9000 --console-address :9001 --certs-dir /certs --license /minio.license' \
    >/dev/null
  record_owned_container "$remote_s3" "$live_project" "$fixture_suffix"
  wait_for PostgreSQL "$postgres" exec "$postgres" \
    pg_isready -U happylearn -d "$database"
  wait_for Redis "$redis" exec "$redis" redis-cli ping
  wait_for primary-AIStor "$primary_aistor" exec "$primary_aistor" \
    curl --fail --silent http://127.0.0.1:9000/minio/health/live
  wait_for remote-S3 "$remote_s3" exec "$remote_s3" \
    curl --fail --silent --cacert /certs/CAs/ca.crt \
      --resolve remote-s3:9000:127.0.0.1 \
      https://remote-s3:9000/minio/health/live
  docker_bounded 60 exec "$remote_s3" /bin/sh -eu -c '
    set -a
    . /run/phase5-secrets/runtime.env
    set +a
    export MC_CONFIG_DIR=/tmp/mc
    client=
    for candidate in /usr/bin/mc /usr/local/bin/mc /usr/bin/mcli /usr/local/bin/mcli; do
      if test -x "$candidate"; then client="$candidate"; break; fi
    done
    test -n "$client"
    export MC_HOST_phase5="https://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@remote-s3:9000"
    "$client" mb --ignore-existing phase5/happylearn-backups >/dev/null
  '
}

start_application() {
  if ! compose_live up --detach --no-build --no-deps app; then
    record_owned_container "$app" "$live_project" "$fixture_suffix" || true
    return 1
  fi
  record_owned_container "$app" "$live_project" "$fixture_suffix"
  wait_for application "$app" exec "$app" \
    curl --fail --silent http://127.0.0.1:8080/api/v1/health/ready
  docker_bounded 60 run -d --name "$fake_ai" --network "container:$app" \
    --read-only --user 10003:10003 --cap-drop ALL \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --security-opt no-new-privileges --memory 64m --cpus .05 \
    --tmpfs /tmp:rw,noexec,nosuid,size=4m,uid=10003,gid=10003,mode=0700 \
    --mount "type=volume,src=$secret_volume,dst=/run/secrets,volume-subpath=fake-ai,readonly" \
    --entrypoint /bin/sh "$fake_ai_image" -eu -c \
    'export E2E_AI_PROVIDER_KEY="$(cat /run/secrets/provider-key)"; exec /app/fake-ai-provider' \
    >/dev/null
  record_owned_container "$fake_ai" "$live_project" "$fixture_suffix"
  wait_for fake-provider "$fake_ai" exec "$fake_ai" \
    curl --fail --silent http://127.0.0.1:8090/health/live
  if ! compose_live up --detach --no-build --no-deps worker; then
    record_owned_container "$worker" "$live_project" "$fixture_suffix" || true
    return 1
  fi
  record_owned_container "$worker" "$live_project" "$fixture_suffix"
  wait_for worker "$worker" exec "$worker" \
    curl --fail --silent http://127.0.0.1:8081/ready
  wait_for processing-supervisor "$processing_supervisor" exec \
    "$processing_supervisor" \
    curl --fail --silent http://127.0.0.1:8092/health/live
}

verify_compose_service_claims() {
  local service expected_name actual_id compose_id
  while IFS='|' read -r service expected_name; do
    actual_id="$(docker_bounded 15 inspect --format '{{.Id}}' "$expected_name")"
    compose_id="$(compose_live ps --quiet "$service")"
    [[ "$actual_id" =~ ^[a-f0-9]{64}$ &&
      "$compose_id" == "$actual_id" ]] ||
      return 1
  done <<CLAIMS
postgres|$postgres
redis|$redis
minio|$primary_aistor
app|$app
worker|$worker
CLAIMS
  compose_live stop --timeout 30 worker
  [[ "$(docker_bounded 15 inspect --format '{{.State.Status}}' "$worker")" == exited ]]
  compose_live start worker
  wait_for worker-after-compose-start "$worker" exec "$worker" \
    curl --fail --silent http://127.0.0.1:8081/ready
  compose_id="$(compose_live ps --quiet worker)"
  actual_id="$(docker_bounded 15 inspect --format '{{.Id}}' "$worker")"
  [[ "$compose_id" == "$actual_id" ]]
}

create_teacher_and_sample() {
  docker_bounded 120 run --rm --name "$admin_init" --network "$network" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 10001:10001 --cap-drop ALL \
    --security-opt no-new-privileges --memory 128m --cpus .1 \
    --tmpfs /tmp:rw,noexec,nosuid,size=16m \
    -e HAPPYLEARN_ENV=development \
    -e HAPPYLEARN_PUBLIC_ORIGIN=http://app:8080 \
    -e HAPPYLEARN_MINIO_ENDPOINT=minio:9000 \
    -e HAPPYLEARN_MINIO_ORIGINALS_BUCKET=happylearn-originals \
    -e HAPPYLEARN_MINIO_PREVIEWS_BUCKET=happylearn-previews \
    --mount "type=volume,src=$secret_volume,dst=/run/secrets-app,volume-subpath=app,readonly" \
    --mount "type=volume,src=$secret_volume,dst=/run/secrets-browser,volume-subpath=browser,readonly" \
    --entrypoint /bin/sh "$app_image" -eu -c \
    'export HAPPYLEARN_AI_MASTER_KEY="$(cat /run/secrets-app/ai-master)" HAPPYLEARN_DATABASE_URL="postgres://happylearn:$(cat /run/secrets-app/database-password)@postgres:5432/'"$database"'?sslmode=disable" HAPPYLEARN_REDIS_URL="redis://redis:6379/0" HAPPYLEARN_LOGIN_THROTTLE_SECRET="$(cat /run/secrets-app/login-throttle)" HAPPYLEARN_MINIO_ACCESS_KEY="$(cat /run/secrets-app/object-access)" HAPPYLEARN_MINIO_SECRET_KEY="$(cat /run/secrets-app/object-secret)"; exec /app/happylearn-admin create-teacher --username admin --display-name "Phase 5 Teacher" --password-file /run/secrets-browser/admin-password'
  docker_bounded 120 run --rm --name "$host_sample" --network "$network" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 10004:0 --cap-drop ALL \
    --security-opt no-new-privileges --memory 64m --cpus .05 \
    --tmpfs /tmp:rw,noexec,nosuid,size=8m,uid=10004,gid=0,mode=0700 \
    --mount "type=volume,src=$secret_volume,dst=/run/secrets,volume-subpath=host-sample,readonly" \
    --entrypoint /bin/sh "$host_sample_image" -eu -c '
      observed="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      timestamp="$(date -u +%s)"
      nonce="$(od -An -N16 -tx1 /dev/urandom | tr -d "[:space:]")"
      printf "{\"schemaVersion\":1,\"observedAt\":\"%s\",\"compose\":[{\"service\":\"app\",\"state\":\"running\",\"health\":\"healthy\",\"restarts\":0}],\"stats\":[{\"service\":\"app\",\"cpuPercent\":\"0%%\",\"memoryUsage\":\"1MiB / 256MiB\"}],\"filesystems\":[{\"filesystem\":\"root\",\"usedPercent\":\"1%%\"},{\"filesystem\":\"backup\",\"usedPercent\":\"1%%\"}]}\n" "$observed" | /app/host-sampler payload >/tmp/payload
      signature="$(/app/host-sampler sign --secret-file /run/secrets/host-metrics-hmac --timestamp "$timestamp" --nonce "$nonce" </tmp/payload)"
      curl --fail --silent --show-error -X POST http://app:9090/internal/host-samples -H "Authorization: Bearer $(cat /run/secrets/metrics-bearer)" -H "Content-Type: application/json" -H "X-HL-Timestamp: $timestamp" -H "X-HL-Nonce: $nonce" -H "X-HL-Signature: $signature" --data-binary @/tmp/payload >/dev/null
    '
  docker_bounded 120 run --rm --name "$backup" --network "$network" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 10003:0 --cap-drop ALL \
    --security-opt no-new-privileges --memory 128m --cpus .1 \
    --tmpfs /tmp:rw,noexec,nosuid,size=8m,uid=10003,gid=0,mode=0700 \
    --mount "type=volume,src=$secret_volume,dst=/run/secrets,volume-subpath=backup,readonly" \
    --entrypoint /bin/sh "$backup_image" -eu -c \
    'test -x /app/happylearn-backup; test -r /run/secrets/database-password; test -r /run/secrets/restic-local-password; test -r /run/secrets/age-identity'
}

seed_phase5_browser_data() {
  docker_bounded 120 exec --interactive "$postgres" \
    psql --username happylearn --dbname happylearn \
      --no-psqlrc --set ON_ERROR_STOP=1 <<'SQL'
INSERT INTO operational_alerts(
  id,dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary,trace_id,
  consecutive_failures,consecutive_successes,version
) VALUES (
  '54000000-0000-4000-8000-000000000001',
  'phase5-e2e-open-backup-alert','backup','warning','open',
  clock_timestamp()-interval '10 minutes',clock_timestamp()-interval '1 minute',
  7200,3600,'Phase 5 deterministic backup freshness warning',
  'phase5-e2e-seed',3,0,1
);

INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,started_at,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  remote_snapshot_id,manifest_sha256,logical_bytes,stored_bytes,
  local_expires_at,remote_expires_at
) VALUES (
  '53000000-0000-4000-8000-000000000001',
  'phase5-e2e-browser-seed','manual','succeeded',
  clock_timestamp()-interval '30 minutes',
  clock_timestamp()-interval '29 minutes',
  clock_timestamp()-interval '28 minutes',
  25,'phase5-e2e-seed-key',
  repeat('1',64),repeat('2',64),decode(repeat('a',64),'hex'),
  4096,2048,clock_timestamp()+interval '7 days',
  clock_timestamp()+interval '30 days'
);

INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,
  verified_at,expires_at
) VALUES
  (
    '53000000-0000-4000-8000-000000000001','manifest','local',
    repeat('1',64),decode(repeat('b',64),'hex'),1024,
    clock_timestamp()-interval '28 minutes',clock_timestamp()+interval '7 days'
  ),
  (
    '53000000-0000-4000-8000-000000000001','manifest','remote',
    repeat('2',64),decode(repeat('c',64),'hex'),1024,
    clock_timestamp()-interval '27 minutes',clock_timestamp()+interval '30 days'
  );

INSERT INTO restore_verifications(
  id,backup_run_id,state,started_at,finished_at,restored_migration_version,
  database_row_counts,checked_object_count,missing_object_count,
  unexpected_object_count,session_revocation_verified,rto_seconds,
  report_sha256
) VALUES (
  '55000000-0000-4000-8000-000000000001',
  '53000000-0000-4000-8000-000000000001','succeeded',
  clock_timestamp()-interval '20 minutes',
  clock_timestamp()-interval '18 minutes',25,'{"users":1}'::jsonb,
  0,0,0,true,120,decode(repeat('d',64),'hex')
);
SQL
}

prepare_browser_workspace() {
  docker_bounded 300 run --rm --name "$fixture_runner" --network none \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 1000:1000 --cap-drop ALL \
    --security-opt no-new-privileges --memory 512m --cpus .5 \
    --tmpfs /tmp:rw,noexec,nosuid,size=256m,uid=1000,gid=1000,mode=0700 \
    -w /tmp --entrypoint /bin/bash -v "$repo_root:/src:ro" \
    -v "$fixture_volume:/fixtures" "$worker_image" \
    /src/scripts/generate-phase2-fixtures.sh /fixtures
  docker_bounded 600 run --rm --name "$install_runner" --network bridge \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 1000:1000 --cap-drop ALL \
    --security-opt no-new-privileges --memory 1024m --cpus .5 \
    --tmpfs /tmp:rw,noexec,nosuid,size=64m \
    -v "$repo_root:/source:ro" -v "$runner_volume:/workspace" \
    --entrypoint /bin/bash -e COREPACK_HOME=/workspace/.corepack \
    -e XDG_DATA_HOME=/workspace/.xdg -e PNPM_HOME=/workspace/.pnpm \
    "$playwright_image" -lc \
    '/source/scripts/copy-e2e-workspace.sh /source /workspace && cd /workspace && corepack pnpm install --frozen-lockfile --store-dir /workspace/.pnpm-store'
}

run_browser_command() {
  local command="$1"
  docker_bounded 1800 run --rm --name "$browser_runner" --network "$network" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 1000:1000 --shm-size 384m --memory 1024m --cpus .5 \
    --cap-drop ALL --security-opt no-new-privileges \
    --tmpfs /tmp:rw,noexec,nosuid,size=128m,uid=1000,gid=1000,mode=0700 \
    -v "$runner_volume:/workspace" -v "$fixture_volume:/fixtures:ro" \
    -v "$artifact_dir/results:/artifacts/results" -w /workspace \
    -e COREPACK_HOME=/workspace/.corepack -e XDG_DATA_HOME=/workspace/.xdg \
    -e PNPM_HOME=/workspace/.pnpm -e E2E_BASE_URL=http://app:8080 \
    -e E2E_FIXTURE_DIR=/fixtures \
    -e E2E_AI_PROVIDER_BASE_URL=http://localhost:8090/v1 \
    -e E2E_AI_PROVIDER_COUNTS_URL=http://app:8090/test/counts \
    -e E2E_AI_PROCESSING_CONTROL_URL=http://processing-supervisor:8092 \
    --mount "type=volume,src=$secret_volume,dst=/run/secrets,volume-subpath=browser,readonly" \
    --entrypoint /bin/bash "$playwright_image" -eu -c \
    'export E2E_ADMIN_PASSWORD="$(cat /run/secrets/admin-password)" E2E_STUDENT_PASSWORD="$(cat /run/secrets/student-password)" E2E_STUDENT_NEW_PASSWORD="$(cat /run/secrets/student-new-password)" E2E_AI_PROVIDER_KEY="$(cat /run/secrets/provider-key)" E2E_AI_PROCESSING_CONTROL_TOKEN="$(cat /run/secrets/control-token)"; '"$command"
}

run_phase5_desktop() {
  run_browser_command \
    'E2E_OUTPUT_DIR=/artifacts/results/phase5 corepack pnpm exec playwright test tests/e2e/operations.spec.ts tests/e2e/backup-restore.spec.ts --project=chromium'
}

run_phase5_mobile() {
  run_browser_command \
    'E2E_OUTPUT_DIR=/artifacts/results/phase5-mobile corepack pnpm exec playwright test tests/e2e/operations.spec.ts --project=mobile --grep @phase5-mobile'
}

run_all_desktop() {
  run_browser_command \
    '. /workspace/scripts/e2e-harness-lib.sh; test_status=0; E2E_OUTPUT_DIR=/artifacts/results/phase1 corepack pnpm exec playwright test tests/e2e/auth-students.spec.ts tests/e2e/teaching.spec.ts --project=chromium || test_status="$(preserve_first_failure "$test_status" "$?")"; E2E_OUTPUT_DIR=/artifacts/results/phase2 corepack pnpm exec playwright test tests/e2e/files.spec.ts tests/e2e/learning.spec.ts --project=chromium || test_status="$(preserve_first_failure "$test_status" "$?")"; E2E_OUTPUT_DIR=/artifacts/results/phase3 corepack pnpm exec playwright test tests/e2e/questions.spec.ts tests/e2e/notifications.spec.ts --project=chromium || test_status="$(preserve_first_failure "$test_status" "$?")"; E2E_OUTPUT_DIR=/artifacts/results/phase4 corepack pnpm exec playwright test tests/e2e/ai-questions.spec.ts tests/e2e/ai-admin.spec.ts tests/e2e/ai-privacy.spec.ts --project=chromium || test_status="$(preserve_first_failure "$test_status" "$?")"; E2E_OUTPUT_DIR=/artifacts/results/phase5 corepack pnpm exec playwright test tests/e2e/operations.spec.ts tests/e2e/backup-restore.spec.ts --project=chromium || test_status="$(preserve_first_failure "$test_status" "$?")"; exit "$test_status"'
}

run_all_mobile() {
  run_browser_command \
    '. /workspace/scripts/e2e-harness-lib.sh; test_status=0; E2E_OUTPUT_DIR=/artifacts/results/phase4-mobile corepack pnpm exec playwright test tests/e2e/ai-questions.spec.ts tests/e2e/ai-admin.spec.ts tests/e2e/ai-privacy.spec.ts --project=mobile --grep @phase4-mobile || test_status="$(preserve_first_failure "$test_status" "$?")"; E2E_OUTPUT_DIR=/artifacts/results/phase5-mobile corepack pnpm exec playwright test tests/e2e/operations.spec.ts --project=mobile --grep @phase5-mobile || test_status="$(preserve_first_failure "$test_status" "$?")"; exit "$test_status"'
}

run_backup_proof() {
  local backup_status=0 one_shot_status=0
  HAPPYLEARN_BACKUP_LIVE_TEST=1 \
  HAPPYLEARN_BACKUP_LIVE_PROJECT="$live_project" \
  HAPPYLEARN_BACKUP_LIVE_ROOT="$backup_host_root" \
  HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$backup_host_root/secrets" \
  HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$backup_host_root/repository" \
  HAPPYLEARN_BACKUP_STATE_DIRECTORY="$backup_host_root/state" \
  HAPPYLEARN_BACKUP_LOCK_DIRECTORY="$backup_host_root/host.lock" \
  HAPPYLEARN_BACKUP_IMAGE="$backup_image" \
  HAPPYLEARN_BACKUP_DATABASE_NAME=happylearn \
  HAPPYLEARN_BACKUP_DATABASE_SSLMODE=require \
  HAPPYLEARN_BACKUP_AGE_RECIPIENT="$HAPPYLEARN_BACKUP_AGE_RECIPIENT" \
  HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID="phase5-e2e-${fixture_suffix}" \
  HAPPYLEARN_AISTOR_LICENSE_FILE="$license_file" \
  HAPPYLEARN_PHASE5_E2E_OWNER="$fixture_suffix" \
    run_bounded 3600 bash "$script_dir/phase5-backup.sh" \
      --project happylearn-dev --trigger manual ||
    backup_status=$?
  remove_coordinator_one_shots audit || one_shot_status=$?
  if ((backup_status != 0)); then return "$backup_status"; fi
  if ((one_shot_status != 0)); then return "$one_shot_status"; fi
  write_recovery_backup_id
}

write_recovery_backup_id() {
  local evidence backup_id local_snapshot_id remote_snapshot_id extra
  local temporary="$tmpdir/recovery-backup-id"
  evidence="$(
    docker_bounded 120 exec "$postgres" \
      psql --username happylearn --dbname happylearn \
        --no-psqlrc --quiet --tuples-only --no-align \
        --set ON_ERROR_STOP=1 --command \
        "SELECT id::text || '|' || local_snapshot_id || '|' || remote_snapshot_id
         FROM backup_runs
         WHERE state='succeeded'
           AND idempotency_key LIKE 'host-%'
           AND local_snapshot_id ~ '^[0-9a-f]{64}$'
           AND remote_snapshot_id ~ '^[0-9a-f]{64}$'
         ORDER BY finished_at DESC,id DESC
         LIMIT 1;"
  )"
  IFS='|' read -r backup_id local_snapshot_id remote_snapshot_id extra \
    <<<"$evidence"
  [[ "$backup_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ &&
    "$local_snapshot_id" =~ ^[0-9a-f]{64}$ &&
    "$remote_snapshot_id" =~ ^[0-9a-f]{64}$ &&
    -z "$extra" ]] ||
    return 1
  printf '%s\n' "$backup_id" >"$temporary"
  install -m 0600 "$temporary" "$artifact_dir/backup-id"
}

run_restore_proof() {
  local backup_id_file="$artifact_dir/backup-id"
  [[ -f "$backup_id_file" && ! -L "$backup_id_file" ]] || return 1
  local backup_id
  backup_id="$(<"$backup_id_file")"
  [[ "$backup_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$backup_host_root/repository" \
  HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$backup_host_root/secrets" \
  HAPPYLEARN_RESTORE_CONTROL_DIRECTORY="$restore_control_dir" \
  HAPPYLEARN_RESTORE_REPORT_DIRECTORY="$restore_report_dir" \
  HAPPYLEARN_AISTOR_LICENSE_FILE="$license_file" \
  HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE="$teacher_credential_file" \
  HAPPYLEARN_BACKUP_IMAGE="$backup_image" \
  HAPPYLEARN_RESTORE_APP_IMAGE="$app_image" \
    run_bounded 3600 bash "$script_dir/phase5-restore-verify.sh" \
      --backup-id "$backup_id"
}

run_resource_sample() {
  local duration="${1:?duration required}"
  local report="$artifact_dir/resource-samples.tsv"
  local deadline=$((SECONDS + duration))
  local temporary="$tmpdir/resource-sample"
  install -m 0600 /dev/null "$temporary"
  docker_bounded 30 pause "$worker" >/dev/null
  while ((SECONDS < deadline)); do
    docker_bounded 30 stats --no-stream \
      --format '{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}' \
      "$app" "$worker" "$postgres" "$redis" "$primary_aistor" \
      "$remote_s3" >>"$temporary"
    sleep 30
  done
  docker_bounded 30 unpause "$worker" >/dev/null
  install -m 0600 "$temporary" "$report"
}

run_signal_contract_probe() {
  local ready_file="${1:?ready file required}"
  local child_pid_file="${2:?child pid file required}"
  [[ "${HAPPYLEARN_PHASE5_SIGNAL_CONTRACT:-}" == 1 &&
    "$ready_file" == /* &&
    "$child_pid_file" == /* &&
    ! -e "$ready_file" &&
    ! -e "$child_pid_file" ]] ||
    return 2
  docker_bounded 300 run --rm --name "$browser_runner" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --network none --read-only --user 1000:1000 --cap-drop ALL \
    --security-opt no-new-privileges "$init_image" sleep 300
}

if [[ "$probe_mode" == audit ]]; then
  audit_container_metadata "${probe_arguments[0]}"
  exit 0
fi
if [[ "$probe_mode" == cleanup ]]; then
  remove_owned_container_if_match \
    "${probe_arguments[0]}" "${probe_arguments[1]}" \
    "${probe_arguments[2]}" "${probe_arguments[3]}"
  exit 0
fi
if [[ "$probe_mode" == coordinator ]]; then
  coordinator_probe_file="${probe_arguments[0]}"
  coordinator_probe_project="${probe_arguments[1]}"
  coordinator_probe_owner="${probe_arguments[2]}"
  if [[ "${HAPPYLEARN_PHASE5_COORDINATOR_CONTRACT:-}" != 1 ||
    "$coordinator_probe_file" != /* ||
    ! -f "$coordinator_probe_file" ||
    -L "$coordinator_probe_file" ||
    ! "$coordinator_probe_project" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ ||
    ! "$coordinator_probe_owner" =~ ^[a-f0-9]{12}$ ||
    "$coordinator_probe_project" != "happylearn-phase5-live-${coordinator_probe_owner}" ]]; then
    exit 2
  fi
  coordinator_one_shot_file="$coordinator_probe_file"
  live_project="$coordinator_probe_project"
  fixture_suffix="$coordinator_probe_owner"
  remove_coordinator_one_shots audit
  exit 0
fi
if [[ "$probe_mode" == signal ]]; then
  run_signal_contract_probe \
    "${probe_arguments[0]}" "${probe_arguments[1]}"
  exit 0
fi

create_fixture_ca
build_images
populate_live_secret_sources
initialize_resources
initialize_secret_volume
verify_secret_consumer_reads
start_dependencies
start_application
verify_compose_service_claims
create_teacher_and_sample
seed_phase5_browser_data
audit_container_metadata \
  "$postgres" "$redis" "$primary_aistor" "$remote_s3" \
  "$app" "$worker" "$fake_ai"
case "$e2e_group" in
  all|phase5|phase5-mobile) prepare_browser_workspace ;;
esac

case "$e2e_group" in
  phase5)
    run_phase5_desktop
    ;;
  phase5-mobile)
    run_phase5_mobile
    ;;
  recovery)
    run_backup_proof
    run_restore_proof
    ;;
  resources)
    run_resource_sample 1800
    ;;
  all)
    status=0
    run_all_desktop || status="$(preserve_first_failure "$status" "$?")"
    run_all_mobile || status="$(preserve_first_failure "$status" "$?")"
    run_backup_proof || status="$(preserve_first_failure "$status" "$?")"
    run_restore_proof || status="$(preserve_first_failure "$status" "$?")"
    run_resource_sample 60 ||
      status="$(preserve_first_failure "$status" "$?")"
    exit "$status"
    ;;
esac
