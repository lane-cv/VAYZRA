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
    case "$1" in
      --audit-container-metadata) probe_mode=audit ;;
      --resource-contract-probe) probe_mode=resource ;;
      --resource-child-status-contract-probe)
        probe_mode=resource_child_status
        ;;
      *)
        printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
        exit 2
        ;;
    esac
    probe_arguments=("$2")
    ;;
  3)
    case "$1" in
      --signal-contract-probe) probe_mode=signal ;;
      --ownership-signal-contract-probe) probe_mode=ownership_signal ;;
      *)
        printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
        exit 2
        ;;
    esac
    probe_arguments=("$2" "$3")
    ;;
  4)
    case "$1" in
      --coordinator-one-shot-probe) probe_mode=coordinator ;;
      --resource-status-contract-probe) probe_mode=resource_status ;;
      *)
        printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
        exit 2
        ;;
    esac
    probe_arguments=("$2" "$3" "$4")
    ;;
  5)
    case "$1" in
      --cleanup-container-ownership-probe) probe_mode=cleanup ;;
      --cleanup-failure-contract-probe) probe_mode=cleanup_failure ;;
      *)
        printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
        exit 2
        ;;
    esac
    probe_arguments=("${@:2}")
    ;;
  6)
    [[ "$1" == '--resource-identity-contract-probe' ]] || {
      printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
      exit 2
    }
    probe_mode=resource_identity
    probe_arguments=("${@:2}")
    ;;
  7)
    [[ "$1" == '--resource-identity-contract-probe' ]] || {
      printf '%s\n' 'invalid Phase 5 E2E harness arguments' >&2
      exit 2
    }
    probe_mode=resource_identity
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
restore_controller="${prefix}_restore_controller"
restore_repository_handoff="${prefix}_restore_repository_handoff"
secret_init="${prefix}_secret_init"
data_init="${prefix}_data_init"
runner_init="${prefix}_runner_init"
fixture_runner="${prefix}_fixture_runner"
install_runner="${prefix}_install_runner"
admin_init="${prefix}_admin_init"
artifact_init="${prefix}_artifact_init"
artifact_write_probe="${prefix}_artifact_write_probe"
age_keygen_runner="${prefix}_age_keygen"
age_recipient_runner="${prefix}_age_recipient"
primary_volume="${live_project}_minio_data"
aistor_license_volume="${live_project}_aistor_license_runtime"
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
restore_controller_image="happylearn-restore-controller:phase5-${nonce}"
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
restore_license_file="$tmpdir/restore-minio.license"
restore_controller_tmp="$tmpdir/restore-controller-tmp"
restore_docker_socket=''
restore_docker_socket_group=''
restore_host_uid=''
restore_host_gid=''
restore_controller_uid=''
restore_controller_gid=''
restore_controller_requires_repository_prepare=false
restore_run_active=false
restore_active_backup_id=''
owned_container_ledger="$tmpdir/owned-containers.tsv"
owned_network_ledger="$tmpdir/owned-networks.tsv"
owned_volume_ledger="$tmpdir/owned-volumes.tsv"
owned_image_ledger="$tmpdir/owned-images.tsv"
container_intent_ledger="$tmpdir/container-intents.tsv"
network_intent_ledger="$tmpdir/network-intents.tsv"
volume_intent_ledger="$tmpdir/volume-intents.tsv"
image_intent_ledger="$tmpdir/image-intents.tsv"
host_sample_dockerfile="$tmpdir/Dockerfile.host-sampler"
seed_sql_file="$tmpdir/phase5-browser-seed.sql"
coordinator_one_shot_file="$backup_host_root/coordinator-one-shots"
coordinator_run_id_file="$backup_host_root/coordinator-run-id"
recovery_backup_id=''
resource_browser_pid=''
resource_backup_pid=''
resource_child_status_file=''
resource_browser_status_file="$tmpdir/resource-browser.status"
resource_backup_status_file="$tmpdir/resource-backup.status"
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
mkdir -m 0700 "$restore_controller_tmp"
for ledger in \
  "$owned_container_ledger" "$owned_network_ledger" \
  "$owned_volume_ledger" "$owned_image_ledger" \
  "$container_intent_ledger" "$network_intent_ledger" \
  "$volume_intent_ledger" "$image_intent_ledger"; do
  install -m 0600 /dev/null "$ledger"
done
install -m 0600 /dev/null "$coordinator_one_shot_file"
install -m 0600 /dev/null "$coordinator_run_id_file"
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
new_ai_master() {
  openssl rand -base64 32 | tr -d '\n'
}
new_database_password() {
  openssl rand -hex 32
}
new_ai_master >"$secret_source_dir/ai-master"
new_database_password >"$secret_source_dir/database-password"
for secret_name in \
  object-access object-secret metrics-bearer \
  host-metrics-hmac restic-local-password restic-remote-password \
  restic-remote-access-key restic-remote-secret-key \
  webhook-authorization login-throttle provider-key control-token \
  admin-password student-password student-new-password; do
  new_secret >"$secret_source_dir/$secret_name"
done
printf '%s' '/repository' >"$secret_source_dir/restic-local-repository"
printf 's3:https://remote-s3:9000/happylearn-backups' \
  >"$secret_source_dir/restic-remote-repository"
printf '%s' 'http://localhost:8080/happylearn-phase5' \
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
  "${prefix}_secret_probe_admin"
  "${prefix}_secret_probe_worker"
  "${prefix}_secret_probe_backup"
)
temporary_containers=(
  "$secret_init" "$data_init" "$runner_init" "$fixture_runner"
  "$install_runner" "$admin_init" "$artifact_init" "$artifact_write_probe"
  "$age_keygen_runner" "$age_recipient_runner"
  "$backup" "$host_sample" "$browser_runner" "$restore_controller"
  "$restore_repository_handoff"
  "${secret_probe_containers[@]}"
)
service_containers=(
  "$fake_ai" "$app" "$worker" "$remote_s3" "$primary_aistor" "$redis" "$postgres"
)
owned_volumes=(
  "$runner_volume" "$fixture_volume" "$secret_volume" "$repository_volume"
  "$state_volume" "$remote_volume" "$primary_volume" \
  "$aistor_license_volume" \
  "$runtime_secret_volume" "$backup_secret_volume" \
  "$postgres_tls_volume" "$app_secret_volume"
)
owned_images=(
  "$host_sample_image" "$supervisor_image" "$fake_ai_image" "$backup_image"
  "$backup_base_image"
  "$worker_image" "$app_image" "$restore_controller_image"
)

persist_resource_intents() {
  local name
  for name in \
    "${service_containers[@]}" \
    "${live_project}-phase5-secrets-init-1" \
    "${live_project}-postgres-tls-init-1" \
    "${live_project}-minio-data-init-1" \
    "${live_project}-aistor-license-init-1" \
    "${temporary_containers[@]}"; do
    printf '%s\t%s\t%s\n' \
      "$name" "$live_project" "$fixture_suffix" \
      >>"$container_intent_ledger"
  done
  printf '%s\t%s\n' "$network" "$fixture_suffix" >"$network_intent_ledger"
  for name in "${owned_volumes[@]}"; do
    printf '%s\t%s\n' "$name" "$fixture_suffix" >>"$volume_intent_ledger"
  done
  for name in "${owned_images[@]}"; do
    printf '%s\t%s\n' "$name" "$fixture_suffix" >>"$image_intent_ledger"
  done
}
persist_resource_intents

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
  local capture_file
  local captured_value
  capture_file="$(mktemp "$tmpdir/docker-capture.XXXXXX")" || return 1
  chmod 0600 "$capture_file"
  if ! docker_bounded "$seconds" "$@" >"$capture_file"; then
    rm -f "$capture_file"
    return 1
  fi
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
  local listing metadata id inspected_name inspected_project inspected_owner
  local extra
  [[ "$name" =~ ^[A-Za-z0-9_.-]+$ &&
    "$expected_id" =~ ^[a-f0-9]{64}$ &&
    "$expected_project" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ &&
    "$expected_owner" =~ ^[a-f0-9]{12}$ ]] ||
    return 1
  docker_capture_bounded listing 15 container ls --all --quiet --no-trunc \
    --filter "name=^/${name}$" || return 1
  [[ -n "$listing" ]] || return 0
  [[ "$listing" =~ ^[a-f0-9]{64}$ ]] || return 1
  id="$listing"
  docker_capture_bounded metadata 15 inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$id" 2>/dev/null || return 1
  IFS='|' read -r id inspected_name inspected_project inspected_owner \
    extra <<<"$metadata"
  [[ "$id" == "$expected_id" &&
    "$inspected_name" == "/$name" &&
    "$inspected_project" == "$expected_project" &&
    "$inspected_owner" == "$expected_owner" &&
    -z "$extra" ]] ||
    return 3
  docker_bounded 30 rm -f "$expected_id" >/dev/null || return 1
}

remove_owned_network_if_match() {
  local name="${1:?network name required}"
  local expected_id="${2:?network id required}"
  local expected_owner="${3:?owner required}"
  local listing metadata inspected_id inspected_name inspected_owner extra
  [[ "$name" =~ ^[a-z0-9_-]+$ &&
    "$expected_id" =~ ^[a-f0-9]{64}$ &&
    "$expected_owner" =~ ^[a-f0-9]{12}$ ]] ||
    return 1
  docker_capture_bounded listing 15 network ls --quiet --no-trunc \
    --filter "name=^${name}$" || return 1
  [[ -n "$listing" ]] || return 0
  [[ "$listing" == "$expected_id" ]] || return 3
  docker_capture_bounded metadata 15 network inspect --format \
    '{{.Id}}|{{.Name}}|{{index .Labels "io.happylearn.phase5.e2e-owner"}}' \
    "$expected_id" || return 1
  IFS='|' read -r inspected_id inspected_name inspected_owner extra \
    <<<"$metadata"
  [[ "$inspected_id" == "$expected_id" &&
    "$inspected_name" == "$name" &&
    "$inspected_owner" == "$expected_owner" &&
    -z "$extra" ]] ||
    return 3
  docker_bounded 30 network rm "$expected_id" >/dev/null || return 1
}

remove_owned_volume_if_match() {
  local name="${1:?volume name required}"
  local expected_owner="${2:?owner required}"
  local listing metadata inspected_name inspected_owner extra
  [[ "$name" =~ ^[A-Za-z0-9_.-]+$ &&
    "$expected_owner" =~ ^[a-f0-9]{12}$ ]] ||
    return 1
  docker_capture_bounded listing 15 volume ls --quiet \
    --filter "name=^${name}$" || return 1
  [[ -n "$listing" ]] || return 0
  [[ "$listing" == "$name" ]] || return 3
  docker_capture_bounded metadata 15 volume inspect --format \
    '{{.Name}}|{{index .Labels "io.happylearn.phase5.e2e-owner"}}' \
    "$name" || return 1
  IFS='|' read -r inspected_name inspected_owner extra <<<"$metadata"
  [[ "$inspected_name" == "$name" &&
    "$inspected_owner" == "$expected_owner" &&
    -z "$extra" ]] ||
    return 3
  docker_bounded 30 volume rm "$name" >/dev/null || return 1
}

remove_owned_image_if_match() {
  local reference="${1:?image reference required}"
  local expected_id="${2:?image id required}"
  local expected_owner="${3:?owner required}"
  local listing metadata inspected_id inspected_owner extra
  [[ "$reference" =~ ^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9_.-]+)?$ &&
    "$expected_id" =~ ^sha256:[a-f0-9]{64}$ &&
    "$expected_owner" =~ ^[a-f0-9]{12}$ ]] ||
    return 1
  docker_capture_bounded listing 15 image ls --quiet --no-trunc \
    "$reference" || return 1
  [[ -n "$listing" ]] || return 0
  [[ "$listing" == "$expected_id" ]] || return 3
  docker_capture_bounded metadata 15 image inspect --format \
    '{{.Id}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
    "$expected_id" || return 1
  IFS='|' read -r inspected_id inspected_owner extra <<<"$metadata"
  [[ "$inspected_id" == "$expected_id" &&
    "$inspected_owner" == "$expected_owner" &&
    -z "$extra" ]] ||
    return 3
  docker_bounded 60 image rm "$expected_id" >/dev/null || return 1
}

remove_intended_containers() {
  local intended_name intended_project intended_owner extra listing id metadata
  local inspected_id inspected_name inspected_project inspected_owner
  local removal_status=0
  [[ -f "$container_intent_ledger" &&
    ! -L "$container_intent_ledger" &&
    "$(portable_file_mode "$container_intent_ledger")" == 600 &&
    "$(portable_file_owner "$container_intent_ledger")" == "$(id -u)" ]] ||
    return 1
  while IFS=$'\t' read -r intended_name intended_project intended_owner \
    extra; do
    [[ -n "$intended_name" &&
      "$intended_name" =~ ^[A-Za-z0-9_.-]+$ &&
      "$intended_project" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ &&
      "$intended_owner" =~ ^[a-f0-9]{12}$ &&
      -z "$extra" ]] || {
      removal_status=1
      continue
    }
    if ! docker_capture_bounded listing 15 container ls --all --quiet \
      --no-trunc --filter "name=^/${intended_name}$"; then
      removal_status=1
      continue
    fi
    [[ -n "$listing" ]] || continue
    [[ "$listing" =~ ^[a-f0-9]{64}$ ]] || {
      removal_status=1
      continue
    }
    id="$listing"
    if ! docker_capture_bounded metadata 15 inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$id"; then
      removal_status=1
      continue
    fi
    IFS='|' read -r inspected_id inspected_name inspected_project \
      inspected_owner extra <<<"$metadata"
    if [[ "$inspected_id" != "$id" ||
      "$inspected_name" != "/$intended_name" ||
      -n "$extra" ]]; then
      removal_status=1
      continue
    fi
    if [[ "$inspected_project" != "$intended_project" ||
      "$inspected_owner" != "$intended_owner" ]]; then
      continue
    fi
    docker_bounded 30 rm -f "$id" >/dev/null || removal_status=1
  done <"$container_intent_ledger"
  return "$removal_status"
}

remove_active_temporary_containers() {
  local name metadata id inspected_name inspected_project inspected_owner extra
  for name in "${temporary_containers[@]}"; do
    case "$name" in
      "$restore_controller"|"$restore_repository_handoff")
        continue
        ;;
    esac
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

quiesce_restore_access_containers() {
  local backup_id="${1:?backup ID required}"
  local name kind listing labelled_listing id_listing metadata id
  local inspected_id inspected_name inspected_project inspected_owner
  local inspected_backup_id inspected_kind inspected_running extra
  local access_container
  [[ "$backup_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  for access_container in \
    "$restore_controller|controller" \
    "$restore_repository_handoff|repository-prepare"; do
    IFS='|' read -r name kind extra <<<"$access_container"
    [[ "$name" =~ ^[A-Za-z0-9_.-]+$ &&
      "$kind" =~ ^(controller|repository-prepare)$ &&
      -z "$extra" ]] ||
      return 1
    docker_capture_bounded listing 15 container ls --all --quiet \
      --no-trunc --filter "name=^/${name}$" ||
      return 1
    docker_capture_bounded labelled_listing 15 container ls --all --quiet \
      --no-trunc \
      --filter "label=io.happylearn.phase5.restore-access-backup-id=${backup_id}" \
      --filter "label=com.docker.compose.project=${live_project}" \
      --filter "label=io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
      --filter "label=io.happylearn.phase5.restore-access-kind=${kind}" ||
      return 1
    [[ -z "$listing" || "$listing" =~ ^[a-f0-9]{64}$ ]] || return 1
    [[ -z "$labelled_listing" ||
      "$labelled_listing" =~ ^[a-f0-9]{64}$ ]] ||
      return 1
    if [[ -n "$labelled_listing" ]]; then
      id="$labelled_listing"
    elif [[ -n "$listing" ]]; then
      id="$listing"
    else
      continue
    fi
    if ! docker_capture_bounded metadata 15 inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}|{{index .Config.Labels "io.happylearn.phase5.restore-access-backup-id"}}|{{index .Config.Labels "io.happylearn.phase5.restore-access-kind"}}|{{.State.Running}}' \
      "$id"; then
      docker_capture_bounded id_listing 15 container ls --all --quiet \
        --no-trunc --filter "id=${id}" ||
        return 1
      [[ -z "$id_listing" ]] || return 1
      docker_capture_bounded listing 15 container ls --all --quiet \
        --no-trunc --filter "name=^/${name}$" ||
        return 1
      [[ -z "$listing" ]] || return 1
      docker_capture_bounded labelled_listing 15 container ls --all --quiet \
        --no-trunc \
        --filter "label=io.happylearn.phase5.restore-access-backup-id=${backup_id}" \
        --filter "label=com.docker.compose.project=${live_project}" \
        --filter "label=io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
        --filter "label=io.happylearn.phase5.restore-access-kind=${kind}" ||
        return 1
      [[ -z "$labelled_listing" ]] || return 1
      continue
    fi
    IFS='|' read -r inspected_id inspected_name inspected_project \
      inspected_owner inspected_backup_id inspected_kind inspected_running \
      extra <<<"$metadata"
    [[ "$inspected_id" == "$id" &&
      "$inspected_name" =~ ^/[A-Za-z0-9_.-]+$ &&
      "$inspected_project" == "$live_project" &&
      "$inspected_owner" == "$fixture_suffix" &&
      "$inspected_backup_id" == "$backup_id" &&
      "$inspected_kind" == "$kind" &&
      "$inspected_running" =~ ^(true|false)$ &&
      -z "$extra" ]] ||
      return 1
    if [[ "$inspected_running" == true ]]; then
      docker_bounded 330 stop --time 300 "$id" >/dev/null || true
    fi
    docker_capture_bounded id_listing 15 container ls --all --quiet \
      --no-trunc --filter "id=${id}" ||
      return 1
    if [[ -n "$id_listing" ]]; then
      [[ "$id_listing" == "$id" ]] || return 1
      docker_bounded 30 rm -f "$id" >/dev/null || return 1
      docker_capture_bounded id_listing 15 container ls --all --quiet \
        --no-trunc --filter "id=${id}" ||
        return 1
      [[ -z "$id_listing" ]] || return 1
    fi
    docker_capture_bounded listing 15 container ls --all --quiet \
      --no-trunc --filter "name=^/${name}$" ||
      return 1
    [[ -z "$listing" ]] || return 1
    docker_capture_bounded labelled_listing 15 container ls --all --quiet \
      --no-trunc \
      --filter "label=io.happylearn.phase5.restore-access-backup-id=${backup_id}" \
      --filter "label=com.docker.compose.project=${live_project}" \
      --filter "label=io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
      --filter "label=io.happylearn.phase5.restore-access-kind=${kind}" ||
      return 1
    [[ -z "$labelled_listing" ]] || return 1
  done
  verify_restore_resources_absent "$backup_id"
}

recover_active_restore_run() {
  [[ "$restore_run_active" == true ]] || return 0
  local backup_id="$restore_active_backup_id"
  [[ "$backup_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  quiesce_restore_access_containers "$backup_id" &&
    verify_restore_resources_absent "$backup_id" ||
    return 1
  restore_run_active=false
  restore_active_backup_id=''
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

remove_intended_networks() {
  local intended_name intended_owner extra listing id metadata
  local inspected_id inspected_name inspected_owner
  local removal_status=0
  [[ -f "$network_intent_ledger" &&
    ! -L "$network_intent_ledger" &&
    "$(portable_file_mode "$network_intent_ledger")" == 600 &&
    "$(portable_file_owner "$network_intent_ledger")" == "$(id -u)" ]] ||
    return 1
  while IFS=$'\t' read -r intended_name intended_owner extra; do
    [[ -n "$intended_name" &&
      "$intended_name" =~ ^[a-z0-9_-]+$ &&
      "$intended_owner" =~ ^[a-f0-9]{12}$ &&
      -z "$extra" ]] || {
      removal_status=1
      continue
    }
    if ! docker_capture_bounded listing 15 network ls --quiet --no-trunc \
      --filter "name=^${intended_name}$"; then
      removal_status=1
      continue
    fi
    [[ -n "$listing" ]] || continue
    [[ "$listing" =~ ^[a-f0-9]{64}$ ]] || {
      removal_status=1
      continue
    }
    id="$listing"
    if ! docker_capture_bounded metadata 15 network inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$id"; then
      removal_status=1
      continue
    fi
    IFS='|' read -r inspected_id inspected_name inspected_owner extra \
      <<<"$metadata"
    if [[ "$inspected_id" != "$id" ||
      "$inspected_name" != "$intended_name" ||
      -n "$extra" ]]; then
      removal_status=1
      continue
    fi
    [[ "$inspected_owner" == "$intended_owner" ]] || continue
    docker_bounded 30 network rm "$id" >/dev/null ||
      removal_status=1
  done <"$network_intent_ledger"
  return "$removal_status"
}

remove_intended_volumes() {
  local intended_name intended_owner extra listing metadata
  local inspected_name inspected_owner
  local removal_status=0
  [[ -f "$volume_intent_ledger" &&
    ! -L "$volume_intent_ledger" &&
    "$(portable_file_mode "$volume_intent_ledger")" == 600 &&
    "$(portable_file_owner "$volume_intent_ledger")" == "$(id -u)" ]] ||
    return 1
  while IFS=$'\t' read -r intended_name intended_owner extra; do
    [[ -n "$intended_name" &&
      "$intended_name" =~ ^[A-Za-z0-9_.-]+$ &&
      "$intended_owner" =~ ^[a-f0-9]{12}$ &&
      -z "$extra" ]] || {
      removal_status=1
      continue
    }
    if ! docker_capture_bounded listing 15 volume ls --quiet \
      --filter "name=^${intended_name}$"; then
      removal_status=1
      continue
    fi
    [[ -n "$listing" ]] || continue
    [[ "$listing" == "$intended_name" ]] || {
      removal_status=1
      continue
    }
    if ! docker_capture_bounded metadata 15 volume inspect --format \
      '{{.Name}}|{{index .Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$intended_name"; then
      removal_status=1
      continue
    fi
    IFS='|' read -r inspected_name inspected_owner extra <<<"$metadata"
    if [[ "$inspected_name" != "$intended_name" || -n "$extra" ]]; then
      removal_status=1
      continue
    fi
    [[ "$inspected_owner" == "$intended_owner" ]] || continue
    docker_bounded 30 volume rm "$intended_name" >/dev/null ||
      removal_status=1
  done <"$volume_intent_ledger"
  return "$removal_status"
}

remove_intended_images() {
  local intended_reference intended_owner extra listing metadata
  local inspected_id inspected_owner
  local removal_status=0
  [[ -f "$image_intent_ledger" &&
    ! -L "$image_intent_ledger" &&
    "$(portable_file_mode "$image_intent_ledger")" == 600 &&
    "$(portable_file_owner "$image_intent_ledger")" == "$(id -u)" ]] ||
    return 1
  while IFS=$'\t' read -r intended_reference intended_owner extra; do
    [[ "$intended_reference" =~ ^[a-z0-9][a-z0-9._/-]*(:[A-Za-z0-9_.-]+)?$ &&
      "$intended_owner" =~ ^[a-f0-9]{12}$ &&
      -z "$extra" ]] || {
      removal_status=1
      continue
    }
    if ! docker_capture_bounded listing 15 image ls --quiet --no-trunc \
      "$intended_reference"; then
      removal_status=1
      continue
    fi
    [[ -n "$listing" ]] || continue
    [[ "$listing" =~ ^sha256:[a-f0-9]{64}$ ]] || {
      removal_status=1
      continue
    }
    if ! docker_capture_bounded metadata 15 image inspect --format \
      '{{.Id}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$listing"; then
      removal_status=1
      continue
    fi
    IFS='|' read -r inspected_id inspected_owner extra <<<"$metadata"
    if [[ "$inspected_id" != "$listing" || -n "$extra" ]]; then
      removal_status=1
      continue
    fi
    [[ "$inspected_owner" == "$intended_owner" ]] || continue
    docker_bounded 60 image rm "$listing" >/dev/null ||
      removal_status=1
  done <"$image_intent_ledger"
  return "$removal_status"
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
  local metadata id inspected_owner extra
  docker_capture_bounded metadata 15 image inspect --format \
    '{{.Id}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
    "$reference" || return 1
  IFS='|' read -r id inspected_owner extra <<<"$metadata"
  [[ "$id" =~ ^sha256:[a-f0-9]{64}$ &&
    "$inspected_owner" == "$fixture_suffix" &&
    -z "$extra" ]] ||
    return 1
  printf '%s\t%s\t%s\n' \
    "$reference" "$id" "$fixture_suffix" >>"$owned_image_ledger"
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
  local secret_path secret_name secret_value
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
        MINIO_ROOT_USER_FILE)
          [[ "$value" == access_key ]] || return 1
          ;;
        MINIO_ROOT_PASSWORD_FILE)
          [[ "$value" == secret_key ]] || return 1
          ;;
        MINIO_KMS_SECRET_KEY_FILE)
          [[ "$value" == kms_master_key ]] || return 1
          ;;
        MINIO_CONFIG_ENV_FILE)
          [[ "$value" == config.env ]] || return 1
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
      secret_name="${secret_path##*/}"
      secret_value="$(<"$secret_path")"
      [[ ${#secret_value} -ge 8 ]] || continue
      if [[ "$secret_name" == restic-local-repository &&
        "$secret_value" == /repository ]]; then
        continue
      fi
      if grep -Fq "$secret_value" <<<"$metadata"; then
        printf \
          'Phase 5 runtime metadata contained a literal secret from %s\n' \
          "$secret_name" >&2
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

portable_file_size() {
  if stat -f '%z' "$1" >/dev/null 2>&1; then
    stat -f '%z' "$1"
  else
    stat -c '%s' "$1"
  fi
}

portable_file_group() {
  if stat -c '%g' "$1" >/dev/null 2>&1; then
    stat -c '%g' "$1"
  else
    stat -f '%g' "$1"
  fi
}

validate_resource_evidence() {
  local evidence="${1:?resource evidence required}"
  local key value
  local configured_cpu configured_memory live_cpu live_memory
  local browser_cpu browser_memory restart_count
  [[ "$evidence" == /* &&
    -f "$evidence" &&
    ! -L "$evidence" &&
    "$(portable_file_mode "$evidence")" == 600 &&
    "$(portable_file_owner "$evidence")" == "$(id -u)" ]] ||
    return 1
  [[ "$(wc -l <"$evidence" | tr -d '[:space:]')" == 17 ]] || return 1
  while IFS='=' read -r key value; do
    [[ -n "$key" && -n "$value" && "$value" != *'='* ]] || return 1
    case "$key" in
      resource_evidence_version|browser_included|owned_samples|saw_browser|\
        saw_backup|saw_heavy|saw_worker|worker_backup_overlap|\
        configured_limits_complete|\
        peak_configured_cpu|\
        peak_configured_memory_mib|peak_live_cpu_percent|\
        peak_live_memory_mib|peak_browser_cpu_percent|\
        peak_browser_memory_mib|oom_killed|max_restart_count) ;;
      *) return 1 ;;
    esac
    [[ "$(grep -Ec "^${key}=" "$evidence")" == 1 ]] || return 1
  done <"$evidence"
  grep -Fxq 'resource_evidence_version=2' "$evidence" &&
    grep -Fxq 'browser_included=true' "$evidence" &&
    grep -Fxq 'owned_samples=true' "$evidence" &&
    grep -Fxq 'saw_browser=true' "$evidence" &&
    grep -Fxq 'saw_backup=true' "$evidence" &&
    grep -Fxq 'saw_heavy=true' "$evidence" &&
    grep -Fxq 'saw_worker=true' "$evidence" &&
    grep -Fxq 'worker_backup_overlap=false' "$evidence" &&
    grep -Fxq 'configured_limits_complete=true' "$evidence" &&
    grep -Fxq 'oom_killed=false' "$evidence" ||
    return 1
  configured_cpu="$(sed -n 's/^peak_configured_cpu=//p' "$evidence")"
  configured_memory="$(
    sed -n 's/^peak_configured_memory_mib=//p' "$evidence"
  )"
  live_cpu="$(sed -n 's/^peak_live_cpu_percent=//p' "$evidence")"
  live_memory="$(sed -n 's/^peak_live_memory_mib=//p' "$evidence")"
  browser_cpu="$(sed -n 's/^peak_browser_cpu_percent=//p' "$evidence")"
  browser_memory="$(
    sed -n 's/^peak_browser_memory_mib=//p' "$evidence"
  )"
  restart_count="$(sed -n 's/^max_restart_count=//p' "$evidence")"
  for value in \
    "$configured_cpu" "$configured_memory" "$live_cpu" "$live_memory" \
    "$browser_cpu" "$browser_memory"; do
    [[ "$value" =~ ^[0-9]+([.][0-9]{1,3})?$ ]] || return 1
  done
  [[ "$restart_count" =~ ^[0-9]+$ && "$restart_count" == 0 ]] || return 1
  awk -v value="$configured_cpu" 'BEGIN { exit !(value > 0 && value <= 2) }' &&
    awk -v value="$configured_memory" \
      'BEGIN { exit !(value > 0 && value <= 4096) }' &&
    awk -v value="$live_cpu" 'BEGIN { exit !(value >= 0 && value <= 200) }' &&
    awk -v value="$live_memory" \
      'BEGIN { exit !(value > 0 && value <= 4096) }' &&
    awk -v value="$browser_cpu" 'BEGIN { exit !(value >= 0) }' &&
    awk -v value="$browser_memory" 'BEGIN { exit !(value > 0) }'
}

coordinator_one_shot_failure() {
  local category="${1:?failure category required}"
  case "$category" in
    argument|ledger|id|listing|missing|collision|metadata|identity|audit|\
      removal|residual_listing|residual|ledger_reset) ;;
    *) category=unavailable ;;
  esac
  printf 'coordinator one-shot failed: category=%s\n' "$category" >&2
}

remove_coordinator_one_shots() {
  local audit_required="${1:?audit mode required}"
  local expected_id listing metadata inspected_id inspected_name inspected_project
  local inspected_oneoff inspected_owner extra removal_status=0
  local ledger_mode ledger_owner
  [[ "$audit_required" == audit || "$audit_required" == cleanup ]] || {
    coordinator_one_shot_failure argument
    return 1
  }
  ledger_mode="$(
    portable_file_mode "$coordinator_one_shot_file" 2>/dev/null
  )" || ledger_mode=''
  ledger_owner="$(
    portable_file_owner "$coordinator_one_shot_file" 2>/dev/null
  )" || ledger_owner=''
  [[ -f "$coordinator_one_shot_file" &&
    ! -L "$coordinator_one_shot_file" &&
    "$ledger_mode" == 600 &&
    "$ledger_owner" == "$(id -u)" ]] || {
    coordinator_one_shot_failure ledger
    return 1
  }
  if ! { exec 9<"$coordinator_one_shot_file"; } 2>/dev/null; then
    coordinator_one_shot_failure ledger
    return 1
  fi
  while IFS= read -r expected_id; do
    [[ -n "$expected_id" ]] || continue
    [[ "$expected_id" =~ ^[a-f0-9]{64}$ ]] || {
      coordinator_one_shot_failure id
      removal_status=1
      continue
    }
    if ! docker_capture_bounded listing 15 container ls --all --quiet \
      --no-trunc --filter "id=${expected_id}" 2>/dev/null; then
      coordinator_one_shot_failure listing
      removal_status=1
      continue
    fi
    if [[ -z "$listing" ]]; then
      if [[ "$audit_required" == audit ]]; then
        coordinator_one_shot_failure missing
        removal_status=1
      fi
      continue
    fi
    if [[ "$listing" != "$expected_id" ]]; then
      coordinator_one_shot_failure collision
      removal_status=1
      continue
    fi
    if ! docker_capture_bounded metadata 15 inspect --format \
      '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.oneoff"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}' \
      "$expected_id" 2>/dev/null; then
      coordinator_one_shot_failure metadata
      removal_status=1
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
      coordinator_one_shot_failure identity
      removal_status=1
      continue
    fi
    if [[ "$audit_required" == audit ]] &&
      ! audit_container_metadata "$expected_id" 2>/dev/null; then
      coordinator_one_shot_failure audit
      removal_status=1
      continue
    fi
    docker_bounded 30 rm -f "$expected_id" >/dev/null 2>&1 || {
      coordinator_one_shot_failure removal
      removal_status=1
      continue
    }
    if ! docker_capture_bounded listing 15 container ls --all --quiet \
      --no-trunc --filter "id=${expected_id}" 2>/dev/null; then
      coordinator_one_shot_failure residual_listing
      removal_status=1
    elif [[ -n "$listing" ]]; then
      coordinator_one_shot_failure residual
      removal_status=1
    fi
  done <&9
  exec 9<&-
  if ((removal_status == 0)); then
    install -m 0600 /dev/null "$coordinator_one_shot_file" 2>/dev/null || {
      coordinator_one_shot_failure ledger_reset
      removal_status=1
    }
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
  local sanitizer_status=0 cleanup_status=0
  local resource_report="$artifact_dir/resource-samples.tsv"
  local preserved_resource_evidence="$tmpdir/preserved-resource-evidence"
  local preserve_resource_evidence=false
  local recorded_name recorded_id recorded_project recorded_owner extra
  local recorded_reference
  trap - EXIT HUP INT TERM
  set +e
  cancel_resource_workloads || cleanup_status=1
  cancel_bounded_command || cleanup_status=1
  remove_active_temporary_containers || cleanup_status=1
  recover_active_restore_run || cleanup_status=1
  if ((exit_status != 0)); then diagnostics || true; fi
  if [[ -e "$resource_report" ]]; then
    if [[ -f "$resource_report" &&
      ! -L "$resource_report" ]] &&
      validate_resource_evidence "$resource_report" &&
      install -m 0600 "$resource_report" "$preserved_resource_evidence"; then
      preserve_resource_evidence=true
    else
      sanitizer_status=1
    fi
  fi
  if artifact_target_is_safe; then
    bash "$script_dir/sanitize-e2e-artifacts.sh" "$artifact_dir" ||
      sanitizer_status=$?
  else
    sanitizer_status=2
  fi
  if ((sanitizer_status == 0)) &&
    [[ "$preserve_resource_evidence" == true ]]; then
    if ! artifact_target_is_safe ||
      ! install -m 0600 "$preserved_resource_evidence" "$resource_report"; then
      sanitizer_status=1
    fi
  fi
  if ((sanitizer_status != 0)); then
    if artifact_target_is_safe; then
      find "$artifact_dir" -mindepth 1 -delete 2>/dev/null || true
    fi
    if ((exit_status == 0)); then exit_status=$sanitizer_status; fi
  fi
  remove_coordinator_one_shots cleanup || cleanup_status=1
  while IFS=$'\t' read -r recorded_name recorded_id recorded_project \
    recorded_owner extra; do
    [[ -n "$recorded_name" && -n "$recorded_id" && -z "$extra" ]] || {
      cleanup_status=1
      continue
    }
    remove_owned_container_if_match \
      "$recorded_name" "$recorded_id" "$recorded_project" "$recorded_owner" ||
      cleanup_status=1
  done <"$owned_container_ledger"
  remove_intended_containers || cleanup_status=1
  while IFS=$'\t' read -r recorded_name recorded_id recorded_owner extra; do
    [[ -n "$recorded_name" && -n "$recorded_id" && -z "$extra" ]] || {
      cleanup_status=1
      continue
    }
    remove_owned_network_if_match \
      "$recorded_name" "$recorded_id" "$recorded_owner" ||
      cleanup_status=1
  done <"$owned_network_ledger"
  remove_intended_networks || cleanup_status=1
  while IFS=$'\t' read -r recorded_name recorded_owner extra; do
    [[ -n "$recorded_name" && -z "$extra" ]] || {
      cleanup_status=1
      continue
    }
    remove_owned_volume_if_match "$recorded_name" "$recorded_owner" ||
      cleanup_status=1
  done <"$owned_volume_ledger"
  remove_intended_volumes || cleanup_status=1
  while IFS=$'\t' read -r recorded_reference recorded_id recorded_owner \
    extra; do
    [[ -n "$recorded_reference" && -n "$recorded_id" &&
      -n "$recorded_owner" && -z "$extra" ]] || {
      cleanup_status=1
      continue
    }
    remove_owned_image_if_match \
      "$recorded_reference" "$recorded_id" "$recorded_owner" ||
      cleanup_status=1
  done <"$owned_image_ledger"
  remove_intended_images || cleanup_status=1
  rm -rf "$tmpdir" || cleanup_status=1
  if ((exit_status == 0 && cleanup_status != 0)); then
    exit_status="$cleanup_status"
  fi
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

cancel_resource_workloads() {
  local pid
  for pid in "$resource_browser_pid" "$resource_backup_pid"; do
    [[ "$pid" =~ ^[1-9][0-9]*$ ]] || continue
    kill -TERM "$pid" 2>/dev/null || true
  done
  for pid in "$resource_browser_pid" "$resource_backup_pid"; do
    [[ "$pid" =~ ^[1-9][0-9]*$ ]] || continue
    wait "$pid" 2>/dev/null || true
  done
  resource_browser_pid=''
  resource_backup_pid=''
}

run_resource_child() {
  local status_file="${1:?resource child status file required}"
  local status=0
  shift
  resource_child_status_file="$status_file"
  trap - EXIT
  trap 'handle_resource_child_signal 129' HUP
  trap 'handle_resource_child_signal 130' INT
  trap 'handle_resource_child_signal 143' TERM
  "$@" || status=$?
  record_resource_child_status "$resource_child_status_file" "$status" ||
    status=125
  return "$status"
}

record_resource_child_status() {
  local status_file="${1:?resource child status file required}"
  local status="${2:?resource child status required}"
  [[ "$status_file" == /* &&
    -f "$status_file" &&
    ! -L "$status_file" &&
    "$(portable_file_mode "$status_file")" == 600 &&
    "$(portable_file_owner "$status_file")" == "$(id -u)" &&
    "$status" =~ ^([0-9]|[1-9][0-9]{1,2})$ &&
    "$status" -le 255 ]] ||
    return 1
  printf '%s\n' "$status" >"$status_file"
}

read_resource_child_status() {
  local status_file="${1:?resource child status file required}"
  local status
  [[ "$status_file" == /* &&
    -f "$status_file" &&
    ! -L "$status_file" &&
    "$(portable_file_mode "$status_file")" == 600 &&
    "$(portable_file_owner "$status_file")" == "$(id -u)" &&
    "$(wc -l <"$status_file" | tr -d '[:space:]')" == 1 ]] ||
    return 1
  status="$(<"$status_file")"
  [[ "$status" =~ ^([0-9]|[1-9][0-9]{1,2})$ &&
    "$status" -le 255 ]] ||
    return 1
  printf '%s\n' "$status"
}

handle_resource_child_signal() {
  local signal_status="${1:?resource child signal status required}"
  [[ "$signal_status" =~ ^(129|130|143)$ ]] || return 1
  trap '' HUP INT TERM
  cancel_bounded_command || true
  record_resource_child_status \
    "$resource_child_status_file" "$signal_status" || true
  exit "$signal_status"
}

if [[ -z "$probe_mode" ||
  "$probe_mode" == signal ||
  "$probe_mode" == ownership_signal ]]; then
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

create_owned_network() {
  docker_bounded 60 network create --internal \
    --label "com.docker.compose.project=${live_project}" \
    --label 'com.docker.compose.network=happylearn' \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    "$network" >/dev/null
  record_owned_network "$network"
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

prepare_restore_license() {
  [[ ! -e "$restore_license_file" && ! -L "$restore_license_file" ]] ||
    return 1
  install -m 0400 "$license_file" "$restore_license_file"
  [[ -f "$restore_license_file" &&
    ! -L "$restore_license_file" &&
    "$(portable_file_mode "$restore_license_file")" == 400 &&
    "$(portable_file_owner "$restore_license_file")" == "$(id -u)" ]]
}

build_restore_controller_image() {
  docker_bounded 900 build \
    --file "$repo_root/deploy/Dockerfile.restore-live-controller" \
    --target restore_live_controller \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --tag "$restore_controller_image" \
    "$repo_root"
  record_owned_image "$restore_controller_image"
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
  docker_bounded 900 build \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    -t "$app_image" "$repo_root"
  record_owned_image "$app_image"
  if [[ "$e2e_group" == all ]]; then
    docker_bounded 1200 build -f "$repo_root/Dockerfile.worker" \
      --no-cache-filter runtime \
      --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
      -t "$worker_image" "$repo_root"
  else
    docker_bounded 1200 build -f "$repo_root/Dockerfile.worker" \
      --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
      -t "$worker_image" "$repo_root"
  fi
  record_owned_image "$worker_image"
  docker_bounded 1200 build -f "$repo_root/Dockerfile.backup" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    -t "$backup_base_image" "$repo_root"
  record_owned_image "$backup_base_image"
  docker_bounded 600 build \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --build-arg "HAPPYLEARN_BACKUP_BASE_IMAGE=${backup_base_image}" \
    -f "$repo_root/deploy/Dockerfile.backup-live-ca" \
    -t "$backup_image" "$ca_context_dir"
  record_owned_image "$backup_image"
  docker_bounded 900 build -f "$repo_root/Dockerfile.fake-ai" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    -t "$fake_ai_image" "$repo_root"
  record_owned_image "$fake_ai_image"
  docker_bounded 900 build -f "$repo_root/Dockerfile.e2e-worker" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --build-arg "WORKER_IMAGE=$worker_image" \
    -t "$supervisor_image" "$repo_root"
  record_owned_image "$supervisor_image"
  if [[ -e "$host_sample_dockerfile" || -L "$host_sample_dockerfile" ]]; then
    printf '%s\n' 'host sampler Dockerfile path already exists' >&2
    return 1
  fi
  cat >"$host_sample_dockerfile" <<'DOCKERFILE'
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
  chmod 0600 "$host_sample_dockerfile"
  if [[ ! -f "$host_sample_dockerfile" ||
        -L "$host_sample_dockerfile" ||
        "$(portable_file_mode "$host_sample_dockerfile")" != 600 ]]; then
    printf '%s\n' 'host sampler Dockerfile was not materialized safely' >&2
    return 1
  fi
  docker_bounded 900 build -f "$host_sample_dockerfile" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    -t "$host_sample_image" "$repo_root"
  record_owned_image "$host_sample_image"
  case "$e2e_group" in
    all|recovery) build_restore_controller_image ;;
  esac
  docker_bounded 60 run --rm --name "$age_keygen_runner" --network none \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --entrypoint /usr/local/bin/age-keygen \
    "$backup_image" >"$secret_source_dir/age-identity" 2>/dev/null
  chmod 0600 "$secret_source_dir/age-identity"
  HAPPYLEARN_BACKUP_AGE_RECIPIENT="$(
    docker_bounded 60 run --rm --read-only \
      --user "$(id -u):$(id -g)" \
      --name "$age_recipient_runner" --network none \
      --label "com.docker.compose.project=${live_project}" \
      --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
      --mount "type=bind,src=$secret_source_dir/age-identity,dst=/input/age-identity,readonly" \
      --entrypoint /usr/local/bin/age-keygen \
      "$backup_image" -y /input/age-identity
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
  rm -f "$artifact_dir/resource-samples.tsv"
  rm -f "$artifact_dir/backup-id"
  initialize_artifact_directory
  create_owned_network
  local volume compose_volume=''
  for volume in "${owned_volumes[@]}"; do
    compose_volume=''
    case "$volume" in
      "$primary_volume") compose_volume=minio_data ;;
      "$aistor_license_volume") compose_volume=aistor_license_runtime ;;
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
        consumer="$1"; owner="$2"; mode="$3"; shift 3
        case "$mode" in
          0400|0600) ;;
          *) exit 2 ;;
        esac
        directory="/owned-secrets/$consumer"
        mkdir "$directory"
        for name in "$@"; do
          target="$directory/$name"
          cp "/secrets/$name" "$target"
          chmod "$mode" "$target"
          chown "$owner" "$target"
          test "$(stat -c "%u:%g:%a" "$target")" = "$owner:${mode#0}"
        done
        chmod 0500 "$directory"
        chown "$owner" "$directory"
        test "$(stat -c "%u:%g:%a" "$directory")" = "$owner:500"
      }
      install_consumer postgres 999:999 0400 database-password
      install_consumer primary-aistor 1000:0 0400 object-access object-secret
      install_consumer remote-s3 1000:0 0400 restic-remote-access-key restic-remote-secret-key
      install_consumer app 10001:10001 0400 ai-master database-password object-access object-secret metrics-bearer host-metrics-hmac webhook-url webhook-authorization login-throttle
      install_consumer worker 10002:10002 0400 database-password object-access object-secret login-throttle
      install_consumer backup 10003:0 0400 database-password restic-local-repository restic-local-password restic-remote-repository restic-remote-password restic-remote-access-key restic-remote-secret-key age-identity
      install_consumer fake-ai 10003:10003 0400 provider-key
      install_consumer supervisor 10002:10002 0400 control-token
      install_consumer admin 10001:10001 0600 admin-password
      install_consumer browser 1000:1000 0400 admin-password student-password student-new-password provider-key control-token
      install_consumer host-sample 10004:0 0400 metrics-bearer host-metrics-hmac
      chmod 0500 /owned-secrets
    '
}

verify_secret_consumer_reads() {
  local probe_name uid consumer secret expected mode
  while IFS='|' read -r probe_name uid consumer secret expected mode; do
    [[ "$mode" == 400 || "$mode" == 600 ]] || return 2
    docker_bounded 60 run --rm --name "${prefix}_${probe_name}" \
      --label "com.docker.compose.project=${live_project}" \
      --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
      --network none --read-only --user "$uid" --cap-drop ALL \
      --security-opt no-new-privileges --memory 16m --cpus .05 \
      --mount "type=volume,src=$secret_volume,dst=/run/secrets,volume-subpath=$consumer,readonly" \
      "$init_image" /bin/sh -eu -c \
      'test "$(stat -c "%u:%g:%a" "/run/secrets/'"$secret"'")" = "'"$expected"':'"$mode"'"; test -s "/run/secrets/'"$secret"'"'
  done <<'PROBES'
secret_probe_postgres|999:999|postgres|database-password|999:999|400
secret_probe_minio|1000:0|primary-aistor|object-access|1000:0|400
secret_probe_app|10001:10001|app|ai-master|10001:10001|400
secret_probe_admin|10001:10001|admin|admin-password|10001:10001|600
secret_probe_worker|10002:10002|worker|database-password|10002:10002|400
secret_probe_backup|10003:0|backup|restic-local-password|10003:0|400
PROBES
  : '999:999' '1000:0' '10001:10001' '10002:10002' '10003:0'
}

run_compose_one_shot() {
  local service="${1:?service required}"
  local name="${live_project}-${service}-1"
  case "$service" in
    phase5-secrets-init|postgres-tls-init|minio-data-init|aistor-license-init) ;;
    *) return 2 ;;
  esac
  if ! compose_live up --no-build --abort-on-container-exit \
    --exit-code-from "$service" "$service"; then
    record_owned_container \
      "$name" "$live_project" "$fixture_suffix" || true
    return 1
  fi
  record_owned_container \
    "$name" \
    "$live_project" "$fixture_suffix"
}

start_dependencies() {
  run_compose_one_shot phase5-secrets-init
  run_compose_one_shot postgres-tls-init
  run_compose_one_shot minio-data-init
  run_compose_one_shot aistor-license-init
  if ! compose_live up --detach --no-build --no-deps postgres redis minio; then
    for name in "$postgres" "$redis" "$primary_aistor"; do
      record_owned_container "$name" "$live_project" "$fixture_suffix" || true
    done
    return 1
  fi
  for name in "$postgres" "$redis" "$primary_aistor"; do
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
  compose_live up --detach --no-build --no-deps worker
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
    --mount "type=volume,src=$secret_volume,dst=/run/secrets-admin,volume-subpath=admin,readonly" \
    --entrypoint /bin/sh "$app_image" -eu -c \
    'export HAPPYLEARN_AI_MASTER_KEY="$(cat /run/secrets-app/ai-master)" HAPPYLEARN_DATABASE_URL="postgres://happylearn:$(cat /run/secrets-app/database-password)@postgres:5432/'"$database"'?sslmode=disable" HAPPYLEARN_REDIS_URL="redis://redis:6379/0" HAPPYLEARN_LOGIN_THROTTLE_SECRET="$(cat /run/secrets-app/login-throttle)" HAPPYLEARN_MINIO_ACCESS_KEY="$(cat /run/secrets-app/object-access)" HAPPYLEARN_MINIO_SECRET_KEY="$(cat /run/secrets-app/object-secret)"; exec /app/happylearn-admin create-teacher --username admin --display-name "Phase 5 Teacher" --password-file /run/secrets-admin/admin-password'
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
  if [[ -e "$seed_sql_file" || -L "$seed_sql_file" ]]; then
    printf '%s\n' 'Phase 5 browser seed path already exists' >&2
    return 1
  fi
  cat >"$seed_sql_file" <<'SQL'
INSERT INTO operational_alerts(
  id,dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary,trace_id,
  consecutive_failures,consecutive_successes,version
) VALUES (
  '54000000-0000-4000-8000-000000000001',
  'phase5_e2e_open_backup_alert','backup','warning','open',
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
  chmod 0600 "$seed_sql_file"
  if [[ ! -f "$seed_sql_file" ||
        -L "$seed_sql_file" ||
        "$(portable_file_mode "$seed_sql_file")" != 600 ]]; then
    printf '%s\n' 'Phase 5 browser seed was not materialized safely' >&2
    return 1
  fi
  docker_bounded 120 exec "$postgres" \
    psql --username happylearn --dbname happylearn \
      --no-psqlrc --set ON_ERROR_STOP=1 \
      --command "$(<"$seed_sql_file")"
}

reopen_phase5_browser_alert() {
  local remaining
  docker_bounded 120 exec "$postgres" \
    psql --username happylearn --dbname happylearn \
      --no-psqlrc --set ON_ERROR_STOP=1 --command "
UPDATE operational_alerts
SET state='open',acknowledged_by=NULL,acknowledged_at=NULL,resolved_at=NULL,
    consecutive_failures=3,consecutive_successes=0,version=version+1
WHERE id='54000000-0000-4000-8000-000000000001';"
  docker_capture_bounded remaining 120 exec "$postgres" \
    psql --username happylearn --dbname happylearn \
      --no-psqlrc --quiet --tuples-only --no-align \
      --set ON_ERROR_STOP=1 --command \
      "SELECT count(*) FROM operational_alerts
       WHERE id='54000000-0000-4000-8000-000000000001'
         AND state='open' AND acknowledged_by IS NULL
         AND acknowledged_at IS NULL AND resolved_at IS NULL
         AND consecutive_failures>=2;"
  [[ "$remaining" == 1 ]]
}

remove_phase5_browser_backup_seed() {
  local remaining
  docker_bounded 120 exec "$postgres" \
    psql --username happylearn --dbname happylearn \
      --no-psqlrc --set ON_ERROR_STOP=1 --command "
BEGIN;
DELETE FROM restore_verifications WHERE backup_run_id='53000000-0000-4000-8000-000000000001';
DELETE FROM backup_artifacts WHERE backup_run_id='53000000-0000-4000-8000-000000000001';
DELETE FROM backup_runs WHERE id='53000000-0000-4000-8000-000000000001';
COMMIT;"
  docker_capture_bounded remaining 120 exec "$postgres" \
    psql --username happylearn --dbname happylearn \
      --no-psqlrc --quiet --tuples-only --no-align \
      --set ON_ERROR_STOP=1 --command \
      "SELECT count(*) FROM backup_runs
       WHERE id='53000000-0000-4000-8000-000000000001';"
  [[ "$remaining" == 0 ]]
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
  local command="$1" timeout="${2:-1800}"
  [[ "$timeout" =~ ^[1-9][0-9]*$ && "$timeout" -le 3600 ]] || return 2
  docker_bounded "$timeout" run --rm --name "$browser_runner" --network "$network" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --read-only --user 1000:1000 --shm-size 384m --memory 896m --cpus .4 \
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

run_backup_workflow() {
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
      --project happylearn-dev --trigger manual
}

backup_finalization_failure() {
  local category="${1:?failure category required}"
  case "$category" in
    one_shot_audit|recovery_evidence) ;;
    *) category=unavailable ;;
  esac
  printf 'backup finalization failed: category=%s\n' "$category" >&2
}

finalize_backup_proof() {
  local backup_status="${1:?backup status required}"
  local one_shot_status=0 recovery_evidence_status=0
  [[ "$backup_status" =~ ^([0-9]|[1-9][0-9]{1,2})$ &&
    "$backup_status" -le 255 ]] ||
    return 2
  remove_coordinator_one_shots audit || one_shot_status=$?
  if ((one_shot_status != 0)); then
    backup_finalization_failure one_shot_audit
  fi
  if ((backup_status != 0)); then return "$backup_status"; fi
  if ((one_shot_status != 0)); then return "$one_shot_status"; fi
  write_recovery_backup_id || recovery_evidence_status=$?
  if ((recovery_evidence_status != 0)); then
    backup_finalization_failure recovery_evidence
    return "$recovery_evidence_status"
  fi
  return 0
}

run_backup_proof() {
  local backup_status=0
  run_backup_workflow || backup_status=$?
  finalize_backup_proof "$backup_status"
}

recovery_evidence_failure() {
  local category="${1:?failure category required}"
  case "$category" in
    invalidate|handoff|query|invalid|write|publish) ;;
    *) category=unavailable ;;
  esac
  printf 'recovery evidence failed: category=%s\n' "$category" >&2
}

read_coordinator_run_id() {
  local run_id
  local handoff_mode handoff_owner handoff_size
  handoff_mode="$(
    portable_file_mode "$coordinator_run_id_file" 2>/dev/null
  )" || handoff_mode=''
  handoff_owner="$(
    portable_file_owner "$coordinator_run_id_file" 2>/dev/null
  )" || handoff_owner=''
  handoff_size="$(
    portable_file_size "$coordinator_run_id_file" 2>/dev/null
  )" || handoff_size=''
  [[ -f "$coordinator_run_id_file" &&
    ! -L "$coordinator_run_id_file" &&
    "$handoff_mode" == 600 &&
    "$handoff_owner" == "$(id -u)" &&
    "$handoff_size" == 37 ]] ||
    return 1
  if ! { exec 8<"$coordinator_run_id_file"; } 2>/dev/null; then
    return 1
  fi
  if ! IFS= read -r run_id <&8; then
    exec 8<&-
    return 1
  fi
  exec 8<&-
  [[ "$run_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  printf '%s\n' "$run_id"
}

write_recovery_backup_id() {
  local expected_backup_id evidence
  local backup_id local_snapshot_id remote_snapshot_id extra
  local temporary="$tmpdir/recovery-backup-id"
  local staging='' published=''
  local invalidate_status=0 query_status=0 write_status=0
  local staging_status=0 install_status=0 move_status=0
  recovery_backup_id=''
  rm -f "$artifact_dir/backup-id" 2>/dev/null ||
    invalidate_status=$?
  if ((invalidate_status != 0)); then
    recovery_evidence_failure invalidate
    return "$invalidate_status"
  fi
  expected_backup_id="$(read_coordinator_run_id)" || {
    recovery_evidence_failure handoff
    return 1
  }
  evidence="$(
    docker_bounded 120 exec "$postgres" \
      psql --username happylearn --dbname happylearn \
        --no-psqlrc --quiet --tuples-only --no-align \
        --set ON_ERROR_STOP=1 --command \
        "SELECT id::text || '|' || local_snapshot_id || '|' || remote_snapshot_id
         FROM backup_runs
         WHERE id='${expected_backup_id}'::uuid
           AND state='succeeded'
           AND local_snapshot_id ~ '^[0-9a-f]{64}$'
           AND remote_snapshot_id ~ '^[0-9a-f]{64}$'
         ORDER BY finished_at DESC,id DESC
         LIMIT 1;" 2>/dev/null
  )" || query_status=$?
  if ((query_status != 0)); then
    recovery_evidence_failure query
    return "$query_status"
  fi
  IFS='|' read -r backup_id local_snapshot_id remote_snapshot_id extra \
    <<<"$evidence"
  [[ "$backup_id" == "$expected_backup_id" &&
    "$backup_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ &&
    "$local_snapshot_id" =~ ^[0-9a-f]{64}$ &&
    "$remote_snapshot_id" =~ ^[0-9a-f]{64}$ &&
    -z "$extra" ]] || {
    recovery_evidence_failure invalid
    return 1
  }
  printf '%s\n' "$backup_id" 2>/dev/null >"$temporary" ||
    write_status=$?
  if ((write_status != 0)); then
    recovery_evidence_failure write
    return "$write_status"
  fi
  staging="$(
    mktemp "$artifact_dir/.backup-id.XXXXXX" 2>/dev/null
  )" || staging_status=$?
  if ((staging_status != 0)); then
    recovery_evidence_failure publish
    return "$staging_status"
  fi
  install -m 0600 "$temporary" "$staging" 2>/dev/null ||
    install_status=$?
  if ((install_status != 0)); then
    rm -f "$staging" 2>/dev/null || true
    recovery_evidence_failure publish
    return "$install_status"
  fi
  mv -f "$staging" "$artifact_dir/backup-id" 2>/dev/null ||
    move_status=$?
  if ((move_status != 0)); then
    rm -f "$staging" 2>/dev/null || true
    recovery_evidence_failure publish
    return "$move_status"
  fi
  if [[ -f "$artifact_dir/backup-id" &&
    ! -L "$artifact_dir/backup-id" &&
    "$(portable_file_mode "$artifact_dir/backup-id" 2>/dev/null)" == 600 &&
    "$(portable_file_owner "$artifact_dir/backup-id" 2>/dev/null)" == "$(id -u)" &&
    "$(portable_file_size "$artifact_dir/backup-id" 2>/dev/null)" == 37 ]] &&
    IFS= read -r published <"$artifact_dir/backup-id" 2>/dev/null &&
    [[ "$published" == "$backup_id" ]]; then
    recovery_backup_id="$backup_id"
    return 0
  fi
  rm -f "$artifact_dir/backup-id" 2>/dev/null || true
  recovery_evidence_failure publish
  return 1
}

resolve_restore_docker_socket() {
  local endpoint
  endpoint="$(
    docker_bounded 15 context inspect \
      --format '{{.Endpoints.docker.Host}}'
  )" || return 1
  [[ "$endpoint" == unix:///* &&
    "$endpoint" != *$'\n'* &&
    "$endpoint" != *$'\r'* ]] ||
    return 1
  restore_docker_socket="${endpoint#unix://}"
  [[ -S "$restore_docker_socket" &&
    ! -L "$restore_docker_socket" ]] ||
    return 1
  restore_docker_socket_group="$(portable_file_group "$restore_docker_socket")" ||
    return 1
  [[ "$restore_docker_socket_group" =~ ^[0-9]+$ ]]
}

resolve_restore_controller_identity() {
  restore_host_uid="$(id -u)" || return 1
  restore_host_gid="$(id -g)" || return 1
  [[ "$restore_host_uid" =~ ^[0-9]+$ &&
    "$restore_host_gid" =~ ^[0-9]+$ ]] ||
    return 1
  restore_controller_uid="$restore_host_uid"
  restore_controller_gid="$restore_host_gid"
  restore_controller_requires_repository_prepare=false
  case "$(uname -s)" in
    Darwin)
      restore_controller_uid=0
      restore_controller_gid=0
      ;;
    Linux)
      [[ "$restore_host_uid" != 0 ]] || return 1
      restore_controller_requires_repository_prepare=true
      ;;
    *) return 1 ;;
  esac
}

prepare_restore_repository_access() {
  local backup_id="${1:?backup ID required}"
  local host_uid host_gid handoff_status=0
  [[ "$backup_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  host_uid="$(id -u)"
  host_gid="$(id -g)"
  [[ "$host_uid" =~ ^[0-9]+$ && "$host_gid" =~ ^[0-9]+$ ]] ||
    return 1
  docker_bounded 300 run --rm --name "$restore_repository_handoff" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --label "io.happylearn.phase5.restore-access-backup-id=${backup_id}" \
    --label "io.happylearn.phase5.restore-access-kind=repository-prepare" \
    --network none --read-only --user 0:0 --cap-drop ALL \
    --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER \
    --security-opt no-new-privileges --memory 128m --cpus .1 \
    --mount "type=bind,src=$backup_host_root/repository,dst=/repository" \
    --entrypoint /usr/bin/timeout "$backup_image" \
    --foreground --kill-after=10s 240s /bin/sh -ceu '
      uid="$1"
      gid="$2"
      case "$uid:$gid" in
        *[!0-9:]* | :* | *: | *:*:*) exit 1 ;;
      esac
      repository_find_empty() {
        result="$(find -P /repository -xdev "$@")" || return 1
        test -z "$result"
      }
      repository_find_empty -type l -print -quit
      repository_find_empty ! -type d ! -type f -print -quit
      repository_find_empty -type f -links +1 -print -quit
      chown -R -- "$uid:$gid" /repository
      chmod 0700 /repository
      test "$(stat -c "%u:%g:%a" /repository)" = "$uid:$gid:700"
      repository_find_empty ! -user "$uid" -print -quit
      repository_find_empty ! -group "$gid" -print -quit
    ' handoff "$host_uid" "$host_gid" ||
    handoff_status=$?
  ((handoff_status == 0)) || return "$handoff_status"
  [[ "$(portable_file_owner "$backup_host_root/repository")" == "$host_uid" &&
    "$(portable_file_mode "$backup_host_root/repository")" == 700 ]]
}

verify_restore_resources_absent() {
  local backup_id="${1:?backup ID required}" class resources
  [[ "$backup_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  for class in container volume network; do
    case "$class" in
      container)
        docker_capture_bounded resources 15 container ls --all --quiet \
          --filter \
          "label=io.happylearn.phase5.restore-backup-id=${backup_id}" ||
          return 1
        ;;
      volume)
        docker_capture_bounded resources 15 volume ls --quiet \
          --filter \
          "label=io.happylearn.phase5.restore-backup-id=${backup_id}" ||
          return 1
        ;;
      network)
        docker_capture_bounded resources 15 network ls --quiet \
          --filter \
          "label=io.happylearn.phase5.restore-backup-id=${backup_id}" ||
          return 1
        ;;
    esac
    [[ -z "$resources" ]] || return 1
  done
  docker_capture_bounded resources 15 container ls --all --quiet \
    --filter \
    "label=io.happylearn.phase5.restore-access-backup-id=${backup_id}" ||
    return 1
  [[ -z "$resources" ]]
}

run_restore_proof() {
  local backup_id_file="$artifact_dir/backup-id"
  local backup_id preflight_status=0 controller_status=0 quiesce_status=0
  local resource_status=0 report_file
  [[ "$recovery_backup_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  [[ -f "$backup_id_file" && ! -L "$backup_id_file" ]] || return 1
  backup_id="$(<"$backup_id_file")"
  [[ "$backup_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  [[ "$backup_id" == "$recovery_backup_id" ]] || return 1
  resolve_restore_docker_socket || preflight_status=$?
  if ((preflight_status == 0)); then
    resolve_restore_controller_identity || preflight_status=$?
  fi
  if ((preflight_status == 0)); then
    restore_active_backup_id="$backup_id"
    restore_run_active=true
    if [[ "$restore_controller_requires_repository_prepare" == true ]]; then
      prepare_restore_repository_access "$backup_id" ||
        preflight_status=$?
    fi
  fi
  if ((preflight_status == 0)); then
    docker_bounded 3600 run --rm --name "$restore_controller" \
    --label "com.docker.compose.project=${live_project}" \
    --label "io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --label "io.happylearn.phase5.restore-access-backup-id=${backup_id}" \
    --label "io.happylearn.phase5.restore-access-kind=controller" \
    --network none --read-only \
    --user "$restore_controller_uid:$restore_controller_gid" \
    --group-add "$restore_docker_socket_group" \
    --cap-drop ALL --security-opt no-new-privileges \
    --memory 256m --memory-swap 256m --cpus .2 --pids-limit 128 \
    --tmpfs "/tmp:rw,noexec,nosuid,size=16m,uid=$restore_controller_uid,gid=$restore_controller_gid,mode=0700" \
    --mount "type=bind,src=$restore_docker_socket,dst=/var/run/docker.sock" \
    --mount "type=bind,src=$repo_root,dst=$repo_root,readonly" \
    --mount "type=bind,src=$backup_host_root/repository,dst=$backup_host_root/repository" \
    --mount "type=bind,src=$backup_host_root/secrets,dst=$backup_host_root/secrets,readonly" \
    --mount "type=bind,src=$restore_control_dir,dst=$restore_control_dir" \
    --mount "type=bind,src=$restore_report_dir,dst=$restore_report_dir" \
    --mount "type=bind,src=$restore_controller_tmp,dst=$restore_controller_tmp" \
    --mount "type=bind,src=$restore_license_file,dst=$restore_license_file,readonly" \
    --mount "type=bind,src=$teacher_credential_file,dst=$teacher_credential_file,readonly" \
    --env 'DOCKER_HOST=unix:///var/run/docker.sock' \
    --env "TMPDIR=$restore_controller_tmp" \
    --env "RESTORE_SCRIPT=$script_dir/phase5-restore-verify.sh" \
    --env "BACKUP_ID=$backup_id" \
    --env "HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY=$backup_host_root/repository" \
    --env "HAPPYLEARN_BACKUP_SECRET_DIRECTORY=$backup_host_root/secrets" \
    --env "HAPPYLEARN_RESTORE_CONTROL_DIRECTORY=$restore_control_dir" \
    --env "HAPPYLEARN_RESTORE_REPORT_DIRECTORY=$restore_report_dir" \
    --env "HAPPYLEARN_AISTOR_LICENSE_FILE=$restore_license_file" \
    --env "HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE=$teacher_credential_file" \
    --env "HAPPYLEARN_BACKUP_IMAGE=$backup_image" \
    --env "HAPPYLEARN_RESTORE_APP_IMAGE=$app_image" \
    --entrypoint /bin/bash "$restore_controller_image" -ceu \
    'exec /bin/bash "$RESTORE_SCRIPT" --backup-id "$BACKUP_ID"' ||
    controller_status=$?
  fi
  if [[ "$restore_run_active" == true ]]; then
    quiesce_restore_access_containers "$backup_id" ||
      quiesce_status=$?
    if ((quiesce_status == 0)); then
      verify_restore_resources_absent "$backup_id" ||
        resource_status=$?
      if ((resource_status == 0)); then
        restore_run_active=false
        restore_active_backup_id=''
      fi
    fi
  fi
  ((preflight_status == 0)) || return "$preflight_status"
  ((controller_status == 0)) || return "$controller_status"
  ((quiesce_status == 0)) || return "$quiesce_status"
  ((resource_status == 0)) || return "$resource_status"
  report_file="$restore_report_dir/restore-${backup_id}.json"
  [[ -f "$report_file" &&
    ! -L "$report_file" &&
    "$(portable_file_mode "$report_file")" == 600 &&
    "$(portable_file_owner "$report_file")" == "$restore_host_uid" &&
    "$(portable_file_group "$report_file")" == "$restore_host_gid" ]]
}

run_resource_browser_load() {
  local duration="${1:?duration required}"
  [[ "$duration" =~ ^[1-9][0-9]*$ ]] || return 2
  run_browser_command \
    'deadline=$((SECONDS + '"$duration"')); resource_success=0; while ((SECONDS < deadline)); do if corepack pnpm exec playwright screenshot --wait-for-timeout 500 http://app:8080/login /tmp/phase5-resource.png >/dev/null 2>&1; then resource_success=1; fi; done; test "$resource_success" = 1' \
    "$((duration + 300))"
}

merge_resource_statuses() {
  local merged=0 candidate
  [[ "$#" -ge 1 ]] || return 2
  for candidate in "$@"; do
    [[ "$candidate" =~ ^([0-9]|[1-9][0-9]{1,2})$ &&
      "$candidate" -le 255 ]] ||
      return 2
    if ((merged == 0 && candidate != 0)); then merged="$candidate"; fi
  done
  return "$merged"
}

parse_resource_stats() {
  awk -F'|' '
    function memory_mib(value, number) {
      number = value
      sub(/[A-Za-z]+$/, "", number)
      if (value ~ /TiB$/) return number * 1048576
      if (value ~ /GiB$/) return number * 1024
      if (value ~ /MiB$/) return number
      if (value ~ /KiB$/) return number / 1024
      if (value ~ /TB$/) return number * 1000000 / 1.048576
      if (value ~ /GB$/) return number * 1000 / 1.048576
      if (value ~ /MB$/) return number / 1.048576
      if (value ~ /kB$/) return number / 1048.576
      if (value ~ /B$/) return number / 1048576
      exit 2
    }
    {
      cpu = $1
      sub(/%$/, "", cpu)
      split($2, usage, /[[:space:]]+/)
      if (cpu !~ /^[0-9]+([.][0-9]+)?$/ || usage[1] == "") exit 2
      cpu_sum += cpu
      memory_sum += memory_mib(usage[1])
    }
    END {
      if (NR == 0) exit 2
      printf "%.3f|%.3f\n", cpu_sum, memory_sum
    }
  '
}

validate_resource_container_identity() {
  local expected_name="${1:?container name required}"
  local expected_id="${2:?container id required}"
  local expected_project="${3:?project required}"
  local expected_owner="${4:?owner required}"
  local expected_service="${5:?service required}"
  local running_policy="${6:-running}"
  local listing metadata inspected_id inspected_name inspected_project
  local inspected_owner inspected_service extra resource_state
  local container_running container_oom restart_count nano_cpus memory_bytes
  [[ "$expected_name" =~ ^[A-Za-z0-9_.-]+$ &&
    "$expected_id" =~ ^[a-f0-9]{64}$ &&
    "$expected_project" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ &&
    "$expected_owner" =~ ^[a-f0-9]{12}$ &&
    "$expected_service" =~ ^(postgres|redis|minio|app|worker)$ &&
    "$running_policy" =~ ^(running|backup_snapshot)$ ]] ||
    return 1
  docker_capture_bounded listing 15 container ls --all --quiet --no-trunc \
    --filter "name=^/${expected_name}$" || return 1
  [[ "$listing" == "$expected_id" ]] || return 1
  docker_capture_bounded metadata 15 inspect --format \
    '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}|{{index .Config.Labels "com.docker.compose.service"}}|{{.State.Running}}' \
    "$expected_id" || return 1
  IFS='|' read -r inspected_id inspected_name inspected_project \
    inspected_owner inspected_service container_running extra <<<"$metadata"
  [[ "$inspected_id" == "$expected_id" &&
    "$inspected_name" == "/$expected_name" &&
    "$inspected_project" == "$expected_project" &&
    "$inspected_owner" == "$expected_owner" &&
    "$inspected_service" == "$expected_service" &&
    -z "$extra" ]] ||
    return 1
  if [[ "$container_running" != true ]] &&
    [[ "$running_policy" != backup_snapshot ||
      ! "$expected_service" =~ ^(minio|worker)$ ||
      "$container_running" != false ]]; then
    return 1
  fi
  docker_capture_bounded resource_state 15 inspect --format \
    '{{.State.OOMKilled}}|{{.RestartCount}}|{{.HostConfig.NanoCpus}}|{{.HostConfig.Memory}}' \
    "$expected_id" || return 1
  IFS='|' read -r container_oom restart_count nano_cpus memory_bytes extra \
    <<<"$resource_state"
  [[ "$container_oom" == false &&
    "$restart_count" == 0 &&
    "$nano_cpus" =~ ^[1-9][0-9]*$ &&
    "$memory_bytes" =~ ^[1-9][0-9]*$ &&
    -z "$extra" ]]
}

validate_required_resource_roster() {
  local running_policy="${1:-running}"
  local specification expected_name expected_service
  local recorded_name recorded_id recorded_project recorded_owner extra
  local matched
  [[ "$running_policy" =~ ^(running|backup_snapshot)$ ]] || return 1
  [[ -f "$owned_container_ledger" &&
    ! -L "$owned_container_ledger" &&
    "$(portable_file_mode "$owned_container_ledger")" == 600 &&
    "$(portable_file_owner "$owned_container_ledger")" == "$(id -u)" ]] ||
    return 1
  for specification in \
    "$postgres|postgres" "$redis|redis" "$primary_aistor|minio" \
    "$app|app" "$worker|worker"; do
    expected_name="${specification%%|*}"
    expected_service="${specification##*|}"
    matched=0
    while IFS=$'\t' read -r recorded_name recorded_id recorded_project \
      recorded_owner extra; do
      [[ "$recorded_name" == "$expected_name" ]] || continue
      ((matched += 1))
      [[ -n "$recorded_id" &&
        "$recorded_project" == "$live_project" &&
        "$recorded_owner" == "$fixture_suffix" &&
        -z "$extra" ]] ||
        return 1
      validate_resource_container_identity \
        "$recorded_name" "$recorded_id" "$recorded_project" \
        "$recorded_owner" "$expected_service" "$running_policy" ||
        return 1
    done <"$owned_container_ledger"
    [[ "$matched" == 1 ]] || return 1
  done
}

validate_resource_ephemeral_container() {
  local expected_id="${1:?container id required}"
  local expected_name="${2:?container name required}"
  local expected_service="${3-}"
  local expected_oneoff="${4-}"
  local metadata inspected_id inspected_name inspected_project inspected_owner
  local inspected_service inspected_oneoff extra resource_state
  local current_listing
  local container_oom restart_count nano_cpus memory_bytes
  [[ "$expected_id" =~ ^[a-f0-9]{64}$ &&
    "$expected_name" =~ ^[A-Za-z0-9_.-]+$ &&
    "$expected_service" =~ ^(backup)?$ &&
    "$expected_oneoff" =~ ^(True)?$ ]] ||
    return 1
  if ! docker_capture_bounded metadata 15 inspect --format \
    '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}|{{with index .Config.Labels "com.docker.compose.service"}}{{.}}{{end}}|{{with index .Config.Labels "com.docker.compose.oneoff"}}{{.}}{{end}}' \
    "$expected_id"; then
    docker_capture_bounded current_listing 15 container ls --all --quiet \
      --no-trunc --filter "id=${expected_id}" || return 1
    [[ -z "$current_listing" ]]
    return
  fi
  IFS='|' read -r inspected_id inspected_name inspected_project \
    inspected_owner inspected_service inspected_oneoff extra <<<"$metadata"
  [[ "$inspected_id" == "$expected_id" &&
    "$inspected_name" == "/$expected_name" &&
    "$inspected_project" == "$live_project" &&
    "$inspected_owner" == "$fixture_suffix" &&
    "$inspected_service" == "$expected_service" &&
    "$inspected_oneoff" == "$expected_oneoff" &&
    -z "$extra" ]] ||
    return 1
  if ! docker_capture_bounded resource_state 15 inspect --format \
    '{{.State.OOMKilled}}|{{.RestartCount}}|{{.HostConfig.NanoCpus}}|{{.HostConfig.Memory}}' \
    "$expected_id"; then
    docker_capture_bounded current_listing 15 container ls --all --quiet \
      --no-trunc --filter "id=${expected_id}" || return 1
    [[ -z "$current_listing" ]]
    return
  fi
  IFS='|' read -r container_oom restart_count nano_cpus memory_bytes extra \
    <<<"$resource_state"
  [[ "$container_oom" == false &&
    "$restart_count" == 0 &&
    "$nano_cpus" =~ ^[1-9][0-9]*$ &&
    "$memory_bytes" =~ ^[1-9][0-9]*$ &&
    -z "$extra" ]]
}

resource_ephemeral_failure() {
  local category="${1:?failure category required}"
  case "$category" in
    browser_listing|browser_identity|backup_listing|backup_metadata|\
      backup_identity) ;;
    *) category=unavailable ;;
  esac
  printf 'resource ephemeral identity failed: category=%s\n' \
    "$category" >&2
}

validate_resource_ephemeral_identities() {
  local listing id metadata inspected_id inspected_name extra current_listing
  docker_capture_bounded listing 15 container ls --all --quiet --no-trunc \
    --filter "name=^/${browser_runner}$" || {
    resource_ephemeral_failure browser_listing
    return 1
  }
  if [[ -n "$listing" ]]; then
    [[ "$listing" =~ ^[a-f0-9]{64}$ ]] || {
      resource_ephemeral_failure browser_identity
      return 1
    }
    validate_resource_ephemeral_container \
      "$listing" "$browser_runner" '' '' || {
      resource_ephemeral_failure browser_identity
      return 1
    }
  fi
  docker_capture_bounded listing 15 container ls --all --quiet --no-trunc \
    --filter "name=^/${live_project}-backup-run-" || {
    resource_ephemeral_failure backup_listing
    return 1
  }
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    [[ "$id" =~ ^[a-f0-9]{64}$ ]] || {
      resource_ephemeral_failure backup_metadata
      return 1
    }
    if ! docker_capture_bounded metadata 15 inspect --format \
      '{{.Id}}|{{.Name}}' "$id"; then
      docker_capture_bounded current_listing 15 container ls --all --quiet \
        --no-trunc --filter "id=${id}" || {
        resource_ephemeral_failure backup_metadata
        return 1
      }
      [[ -z "$current_listing" ]] && continue
      resource_ephemeral_failure backup_metadata
      return 1
    fi
    IFS='|' read -r inspected_id inspected_name extra <<<"$metadata"
    [[ "$inspected_id" == "$id" &&
      "$inspected_name" =~ ^/${live_project}-backup-run-[A-Za-z0-9_.-]+$ &&
      -z "$extra" ]] || {
      resource_ephemeral_failure backup_metadata
      return 1
    }
    validate_resource_ephemeral_container \
      "$id" "${inspected_name#/}" backup True || {
      resource_ephemeral_failure backup_identity
      return 1
    }
  done <<<"$listing"
}

resource_monitor_retry() {
  local attempt
  for attempt in 1 2 3; do
    if "$@"; then return 0; fi
    ((attempt == 3)) || sleep 1
  done
  return 1
}

resource_monitor_capture() {
  local output_variable="${1:?output variable required}"
  local seconds="${2:?deadline required}"
  shift 2
  resource_monitor_retry \
    docker_capture_bounded "$output_variable" "$seconds" "$@"
}

resource_monitor_stats() {
  local output_variable="${1:?output variable required}"
  local seconds="${2:?deadline required}"
  shift 2
  local ids=("$@") live_ids=()
  local attempt id listing listing_failed
  ((${#ids[@]} > 0)) || {
    printf -v "$output_variable" '%s' ''
    return 0
  }
  for attempt in 1 2 3; do
    live_ids=()
    listing_failed=false
    for id in "${ids[@]}"; do
      if ! docker_capture_bounded listing 15 container ls --quiet --no-trunc \
        --filter "id=${id}"; then
        listing_failed=true
        break
      fi
      [[ -z "$listing" ]] || live_ids+=("$id")
    done
    if [[ "$listing_failed" == false ]]; then
      if ((${#live_ids[@]} == 0)); then
        printf -v "$output_variable" '%s' ''
        return 0
      fi
      if docker_capture_bounded "$output_variable" "$seconds" stats \
        --no-stream --format '{{.CPUPerc}}|{{.MemUsage}}' \
        "${live_ids[@]}"; then
        return 0
      fi
    fi
    ((attempt == 3)) || sleep 1
  done
  return 1
}

resource_monitor_failure() {
  local category="${1:?failure category required}"
  case "$category" in
    required_roster|ephemeral_identity|listing|ownership|resource_state|\
      invariant|command|production_stats|production_parse|browser_stats|\
      browser_parse|final_roster) ;;
    *) category=unavailable ;;
  esac
  printf 'resource monitor failed: category=%s\n' "$category" >&2
}

resource_is_backup_activity() {
  local container_name="${1:?container name required}"
  local service="${2-}"
  local oneoff="${3-}"
  [[ "$container_name" == /* &&
    "$service" =~ ^(backup|backup-storage-init|backup-secrets-init)?$ &&
    "$oneoff" =~ ^(True)?$ ]] ||
    return 1
  case "$service" in
    backup|backup-storage-init|backup-secrets-init) return 0 ;;
  esac
  [[ "$container_name" == "/$backup" ]] && return 0
  [[ "$oneoff" == True ]] || return 1
  case "${container_name#/}" in
    "${live_project}-backup-run-"*|\
      "${live_project}-backup-storage-init-run-"*|\
      "${live_project}-backup-secrets-init-run-"*) return 0 ;;
  esac
  return 1
}

resource_worker_backup_overlap_now() {
  local snapshot name service oneoff extra
  local worker_running=false backup_running=false
  resource_monitor_capture snapshot 15 ps \
    --filter "label=com.docker.compose.project=${live_project}" \
    --filter "label=io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
    --format '{{.Names}}|{{.Label "com.docker.compose.service"}}|{{.Label "com.docker.compose.oneoff"}}' ||
    return 2
  while IFS='|' read -r name service oneoff extra; do
    [[ -n "$name" ]] || continue
    [[ "$name" =~ ^[A-Za-z0-9_.-]+$ &&
      "$service" =~ ^(postgres|redis|minio|app|worker|backup|backup-storage-init|backup-secrets-init)?$ &&
      "$oneoff" =~ ^(True|False)?$ && -z "$extra" ]] ||
      return 2
    [[ "$service" == worker ]] && worker_running=true
    if resource_is_backup_activity "/$name" "$service" "$oneoff"; then
      backup_running=true
    fi
  done <<<"$snapshot"
  [[ "$worker_running" == true && "$backup_running" == true ]]
}

monitor_resource_workloads() {
  local browser_pid="${1:?browser pid required}"
  local backup_pid="${2:?backup pid required}"
  local evidence="${3:?evidence path required}"
  local saw_browser=false saw_backup=false saw_heavy=false saw_worker=false
  local owned_samples=true worker_backup_overlap=false oom_killed=false
  local configured_limits_complete=true
  local peak_configured_cpu=0 peak_configured_memory_mib=0
  local peak_live_cpu_percent=0 peak_live_memory_mib=0
  local peak_browser_cpu_percent=0 peak_browser_memory_mib=0
  local max_restart_count=0
  local listing id ownership inspected_id inspected_name inspected_project
  local inspected_owner service oneoff extra state
  local resource_state current_listing container_oom restart_count
  local roster_policy
  local nano_cpus memory_bytes
  local command worker_running heavy_running backup_activity_running browser_id
  local configured_cpu configured_memory stats sample live_cpu live_memory
  local browser_stats browser_sample browser_cpu browser_memory
  local aggregate_container overlap_status
  local aggregate_ids=()
  install -m 0600 /dev/null "$evidence"
  while kill -0 "$browser_pid" 2>/dev/null ||
    kill -0 "$backup_pid" 2>/dev/null; do
    roster_policy=running
    if kill -0 "$backup_pid" 2>/dev/null; then
      roster_policy=backup_snapshot
    fi
    resource_monitor_retry \
      validate_required_resource_roster "$roster_policy" || {
      resource_monitor_failure required_roster
      return 1
    }
    resource_monitor_retry validate_resource_ephemeral_identities || {
      resource_monitor_failure ephemeral_identity
      return 1
    }
    resource_monitor_capture listing 15 ps --all --no-trunc \
      --filter "label=com.docker.compose.project=${live_project}" \
      --filter "label=io.happylearn.phase5.e2e-owner=${fixture_suffix}" \
      --format '{{.ID}}' || {
      resource_monitor_failure listing
      return 1
    }
    worker_running=false
    heavy_running=false
    backup_activity_running=false
    browser_id=''
    configured_cpu=0
    configured_memory=0
    aggregate_ids=()
    while IFS= read -r id; do
      [[ -n "$id" ]] || continue
      [[ "$id" =~ ^[a-f0-9]{64}$ ]] || {
        resource_monitor_failure invariant
        return 1
      }
      if ! resource_monitor_capture ownership 15 inspect --format \
        '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}|{{with index .Config.Labels "com.docker.compose.service"}}{{.}}{{end}}|{{with index .Config.Labels "com.docker.compose.oneoff"}}{{.}}{{end}}|{{.State.Running}}' \
        "$id" 2>/dev/null; then
        if resource_monitor_retry \
          docker_bounded 15 inspect "$id" >/dev/null 2>&1; then
          resource_monitor_failure ownership
          return 1
        fi
        continue
      fi
      IFS='|' read -r inspected_id inspected_name inspected_project \
        inspected_owner service oneoff state extra <<<"$ownership"
      if [[ "$inspected_id" != "$id" ||
        "$inspected_name" != /* ||
        "$inspected_project" != "$live_project" ||
        "$inspected_owner" != "$fixture_suffix" ||
        ! "$state" =~ ^(true|false)$ ||
        -n "$extra" ]]; then
        owned_samples=false
        resource_monitor_failure invariant
        return 1
      fi
      if ! resource_monitor_capture resource_state 15 inspect --format \
        '{{.State.OOMKilled}}|{{.RestartCount}}|{{.HostConfig.NanoCpus}}|{{.HostConfig.Memory}}' \
        "$id"; then
        resource_monitor_capture current_listing 15 container ls --all --quiet \
          --no-trunc --filter "id=${id}" || {
          resource_monitor_failure resource_state
          return 1
        }
        [[ -z "$current_listing" ]] && continue
        resource_monitor_failure resource_state
        return 1
      fi
      IFS='|' read -r container_oom restart_count nano_cpus \
        memory_bytes extra <<<"$resource_state"
      [[ "$container_oom" =~ ^(true|false)$ &&
        "$restart_count" =~ ^[0-9]+$ &&
        "$nano_cpus" =~ ^[0-9]+$ &&
        "$memory_bytes" =~ ^[0-9]+$ &&
        -z "$extra" ]] || {
        resource_monitor_failure invariant
        return 1
      }
      [[ "$container_oom" == false ]] || oom_killed=true
      if ((restart_count > max_restart_count)); then
        max_restart_count="$restart_count"
      fi
      if [[ "$inspected_name" == "/$browser_runner" &&
        "$state" == true ]]; then
        browser_id="$id"
      fi
      if [[ "$state" == true ]] &&
        resource_is_backup_activity "$inspected_name" "$service" "$oneoff"; then
        backup_activity_running=true
        saw_backup=true
      fi
      if [[ "$service" == backup ]]; then
        resource_monitor_capture command 15 inspect --format \
          '{{json .Config.Cmd}}' "$id" || {
          resource_monitor_failure command
          return 1
        }
        if {
          [[ "$command" == *'/app/happylearn-backup'* &&
            "$command" =~ \"(snapshot|verify|sync)\" ]]
        } ||
          [[ "$command" == *'happylearn-backup-retention'* ]] ||
          {
            [[ "$command" == *'restic'* && "$command" == *'check'* ]]
          }; then
          if [[ "$state" == true ]]; then
            heavy_running=true
            saw_heavy=true
          fi
        fi
      fi
      if [[ "$state" == true ]]; then
        aggregate_container=false
        if [[ "$service" == worker ]]; then
          worker_running=true
          saw_worker=true
        fi
        case "$service" in
          postgres|redis|minio|app|worker|backup|backup-storage-init|\
            backup-secrets-init) aggregate_container=true ;;
        esac
        [[ "$inspected_name" == "/$browser_runner" ]] &&
          aggregate_container=true
        if [[ "$aggregate_container" == true ]]; then
          aggregate_ids+=("$id")
          if [[ "$nano_cpus" == 0 || "$memory_bytes" == 0 ]]; then
            configured_limits_complete=false
          fi
          configured_cpu="$(
            awk -v total="$configured_cpu" -v value="$nano_cpus" \
              'BEGIN { printf "%.6f", total + value / 1000000000 }'
          )"
          configured_memory="$(
            awk -v total="$configured_memory" -v value="$memory_bytes" \
              'BEGIN { printf "%.6f", total + value / 1048576 }'
          )"
        fi
      fi
    done <<<"$listing"
    if [[ "$worker_running" == true &&
      "$backup_activity_running" == true ]]; then
      overlap_status=0
      resource_worker_backup_overlap_now || overlap_status=$?
      case "$overlap_status" in
        0) worker_backup_overlap=true ;;
        1) ;;
        *)
          resource_monitor_failure listing
          return 1
          ;;
      esac
    fi
    peak_configured_cpu="$(
      awk -v old="$peak_configured_cpu" -v new="$configured_cpu" \
        'BEGIN { printf "%.3f", (new > old ? new : old) }'
    )"
    peak_configured_memory_mib="$(
      awk -v old="$peak_configured_memory_mib" -v new="$configured_memory" \
        'BEGIN { printf "%.3f", (new > old ? new : old) }'
    )"
    if ((${#aggregate_ids[@]} > 0)); then
      resource_monitor_stats stats 30 "${aggregate_ids[@]}" || {
        resource_monitor_failure production_stats
        return 1
      }
      sample="$(printf '%s\n' "$stats" | parse_resource_stats)" || {
        resource_monitor_failure production_parse
        return 1
      }
      live_cpu="${sample%%|*}"
      live_memory="${sample##*|}"
      peak_live_cpu_percent="$(
        awk -v old="$peak_live_cpu_percent" -v new="$live_cpu" \
          'BEGIN { printf "%.3f", (new > old ? new : old) }'
      )"
      peak_live_memory_mib="$(
        awk -v old="$peak_live_memory_mib" -v new="$live_memory" \
          'BEGIN { printf "%.3f", (new > old ? new : old) }'
      )"
    fi
    if [[ -n "$browser_id" ]]; then
      if ! resource_monitor_capture browser_stats 30 stats --no-stream \
        --format '{{.CPUPerc}}|{{.MemUsage}}' "$browser_id"; then
        resource_monitor_capture listing 15 container ls --all --quiet \
          --no-trunc --filter "id=${browser_id}" || {
          resource_monitor_failure browser_stats
          return 1
        }
        [[ -z "$listing" ]] && continue
        resource_monitor_failure browser_stats
        return 1
      fi
      browser_sample="$(
        printf '%s\n' "$browser_stats" | parse_resource_stats
      )" || {
        resource_monitor_failure browser_parse
        return 1
      }
      browser_cpu="${browser_sample%%|*}"
      browser_memory="${browser_sample##*|}"
      peak_browser_cpu_percent="$(
        awk -v old="$peak_browser_cpu_percent" -v new="$browser_cpu" \
          'BEGIN { printf "%.3f", (new > old ? new : old) }'
      )"
      peak_browser_memory_mib="$(
        awk -v old="$peak_browser_memory_mib" -v new="$browser_memory" \
          'BEGIN { printf "%.3f", (new > old ? new : old) }'
      )"
      saw_browser=true
    fi
    sleep 0.2
  done
  resource_monitor_retry validate_required_resource_roster running || {
    resource_monitor_failure final_roster
    return 1
  }
  {
    printf '%s\n' \
      'resource_evidence_version=2' \
      'browser_included=true' \
      "owned_samples=$owned_samples" \
      "saw_browser=$saw_browser" \
      "saw_backup=$saw_backup" \
      "saw_heavy=$saw_heavy" \
      "saw_worker=$saw_worker" \
      "worker_backup_overlap=$worker_backup_overlap" \
      "configured_limits_complete=$configured_limits_complete" \
      "peak_configured_cpu=$peak_configured_cpu" \
      "peak_configured_memory_mib=$peak_configured_memory_mib" \
      "peak_live_cpu_percent=$peak_live_cpu_percent" \
      "peak_live_memory_mib=$peak_live_memory_mib" \
      "peak_browser_cpu_percent=$peak_browser_cpu_percent" \
      "peak_browser_memory_mib=$peak_browser_memory_mib" \
      "oom_killed=$oom_killed" \
      "max_restart_count=$max_restart_count"
  } >"$evidence"
}

run_resource_sample() {
  local duration="${1:?duration required}"
  local report="$artifact_dir/resource-samples.tsv"
  local temporary="$tmpdir/resource-evidence"
  local browser_status=0 backup_status=0 monitor_status=0 finalize_status=0
  local validation_status=0
  local child_status_file status=0
  for child_status_file in \
    "$resource_browser_status_file" "$resource_backup_status_file"; do
    [[ ! -e "$child_status_file" && ! -L "$child_status_file" ]] ||
      return 1
    install -m 0600 /dev/null "$child_status_file"
  done
  run_resource_child "$resource_browser_status_file" \
    run_resource_browser_load "$duration" &
  resource_browser_pid=$!
  run_resource_child "$resource_backup_status_file" run_backup_workflow &
  resource_backup_pid=$!
  monitor_resource_workloads \
    "$resource_browser_pid" "$resource_backup_pid" "$temporary" ||
    monitor_status=$?
  wait "$resource_browser_pid" 2>/dev/null || true
  resource_browser_pid=''
  wait "$resource_backup_pid" 2>/dev/null || true
  resource_backup_pid=''
  browser_status="$(read_resource_child_status "$resource_browser_status_file")" ||
    browser_status=125
  backup_status="$(read_resource_child_status "$resource_backup_status_file")" ||
    backup_status=125
  finalize_backup_proof "$backup_status" || finalize_status=$?
  merge_resource_statuses \
    "$browser_status" "$backup_status" "$monitor_status" "$finalize_status" ||
    status=$?
  if ((status != 0)); then
    printf \
      'resource sample failed: browser=%s backup=%s monitor=%s finalize=%s\n' \
      "$browser_status" "$backup_status" "$monitor_status" \
      "$finalize_status" >&2
    return "$status"
  fi
  validate_resource_evidence "$temporary" || validation_status=$?
  if ((validation_status != 0)); then
    printf 'resource evidence validation failed:\n' >&2
    sed -n '1,40p' "$temporary" >&2
    return "$validation_status"
  fi
  install -m 0600 "$temporary" "$report" || return $?
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

run_ownership_signal_contract_probe() {
  local ready_file="${1:?ready file required}"
  local child_pid_file="${2:?child pid file required}"
  [[ "${HAPPYLEARN_PHASE5_OWNERSHIP_SIGNAL_CONTRACT:-}" == 1 &&
    "$ready_file" == /* &&
    "$child_pid_file" == /* &&
    ! -e "$ready_file" &&
    ! -e "$child_pid_file" ]] ||
    return 2
  create_owned_network
}

run_cleanup_failure_contract_probe() {
  local cleanup_name="${1:?cleanup name required}"
  local cleanup_id="${2:?cleanup id required}"
  local cleanup_project="${3:?cleanup project required}"
  local cleanup_owner="${4:?cleanup owner required}"
  local original_status="${HAPPYLEARN_PHASE5_CLEANUP_ORIGINAL_STATUS:-0}"
  local cleanup_kind="${HAPPYLEARN_PHASE5_CLEANUP_KIND:-container}"
  [[ "${HAPPYLEARN_PHASE5_CLEANUP_FAILURE_CONTRACT:-}" == 1 &&
    "$cleanup_name" =~ ^[A-Za-z0-9_.-]+$ &&
    "$cleanup_id" =~ ^[a-f0-9]{64}$ &&
    "$cleanup_project" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ &&
    "$cleanup_owner" =~ ^[a-f0-9]{12}$ &&
    "$cleanup_project" == "happylearn-phase5-live-${cleanup_owner}" &&
    "$cleanup_kind" =~ ^(container|network|volume|image)$ &&
    "$original_status" =~ ^([0-9]|[1-9][0-9]{1,2})$ &&
    "$original_status" -le 255 ]] ||
    return 2
  fixture_suffix="$cleanup_owner"
  live_project="$cleanup_project"
  temporary_containers=("${cleanup_name}-temporary-absent")
  service_containers=("${cleanup_name}-service-absent")
  allowed_artifact_root="$tmpdir"
  allowed_artifact_root_canonical="$tmpdir"
  artifact_dir="$tmpdir/artifacts"
  install -d -m 0700 "$artifact_dir"
  for ledger in \
    "$owned_container_ledger" "$owned_network_ledger" \
    "$owned_volume_ledger" "$owned_image_ledger" \
    "$container_intent_ledger" "$network_intent_ledger" \
    "$volume_intent_ledger" "$image_intent_ledger"; do
    install -m 0600 /dev/null "$ledger"
  done
  case "$cleanup_kind" in
    container)
      printf '%s\t%s\t%s\t%s\n' \
        "$cleanup_name" "$cleanup_id" "$cleanup_project" "$cleanup_owner" \
        >"$owned_container_ledger"
      ;;
    network)
      printf '%s\t%s\t%s\n' \
        "$cleanup_name" "$cleanup_id" "$cleanup_owner" \
        >"$owned_network_ledger"
      ;;
    volume)
      printf '%s\t%s\n' "$cleanup_name" "$cleanup_owner" \
        >"$owned_volume_ledger"
      ;;
    image)
      printf '%s\tsha256:%s\t%s\n' \
        "$cleanup_name" "$cleanup_id" "$cleanup_owner" \
        >"$owned_image_ledger"
      ;;
  esac
  cleanup "$original_status"
}

if [[ "$probe_mode" == audit ]]; then
  audit_container_metadata "${probe_arguments[0]}"
  exit 0
fi
if [[ "$probe_mode" == resource ]]; then
  [[ "${HAPPYLEARN_PHASE5_RESOURCE_CONTRACT:-}" == 1 ]] || exit 2
  validate_resource_evidence "${probe_arguments[0]}"
  exit $?
fi
if [[ "$probe_mode" == resource_child_status ]]; then
  [[ "${HAPPYLEARN_PHASE5_RESOURCE_CHILD_STATUS_CONTRACT:-}" == 1 ]] ||
    exit 2
  read_resource_child_status "${probe_arguments[0]}"
  exit $?
fi
if [[ "$probe_mode" == cleanup ]]; then
  remove_owned_container_if_match \
    "${probe_arguments[0]}" "${probe_arguments[1]}" \
    "${probe_arguments[2]}" "${probe_arguments[3]}"
  exit 0
fi
if [[ "$probe_mode" == cleanup_failure ]]; then
  run_cleanup_failure_contract_probe \
    "${probe_arguments[0]}" "${probe_arguments[1]}" \
    "${probe_arguments[2]}" "${probe_arguments[3]}"
  exit 0
fi
if [[ "$probe_mode" == coordinator ]]; then
  coordinator_probe_file="${probe_arguments[0]}"
  coordinator_probe_project="${probe_arguments[1]}"
  coordinator_probe_owner="${probe_arguments[2]}"
  coordinator_probe_mode="${HAPPYLEARN_PHASE5_COORDINATOR_MODE:-audit}"
  if [[ "${HAPPYLEARN_PHASE5_COORDINATOR_CONTRACT:-}" != 1 ||
    "$coordinator_probe_file" != /* ||
    ! -f "$coordinator_probe_file" ||
    -L "$coordinator_probe_file" ||
    ! "$coordinator_probe_project" =~ ^happylearn-phase5-live-[a-f0-9]{12}$ ||
    ! "$coordinator_probe_owner" =~ ^[a-f0-9]{12}$ ||
    "$coordinator_probe_project" != "happylearn-phase5-live-${coordinator_probe_owner}" ||
    ! "$coordinator_probe_mode" =~ ^(audit|cleanup)$ ]]; then
    exit 2
  fi
  coordinator_one_shot_file="$coordinator_probe_file"
  live_project="$coordinator_probe_project"
  fixture_suffix="$coordinator_probe_owner"
  remove_coordinator_one_shots "$coordinator_probe_mode"
  exit 0
fi
if [[ "$probe_mode" == resource_status ]]; then
  [[ "${HAPPYLEARN_PHASE5_RESOURCE_STATUS_CONTRACT:-}" == 1 ]] || exit 2
  merge_resource_statuses \
    "${probe_arguments[0]}" "${probe_arguments[1]}" \
    "${probe_arguments[2]}"
  exit $?
fi
if [[ "$probe_mode" == resource_identity ]]; then
  [[ "${HAPPYLEARN_PHASE5_RESOURCE_IDENTITY_CONTRACT:-}" == 1 ]] || exit 2
  validate_resource_container_identity \
    "${probe_arguments[0]}" "${probe_arguments[1]}" \
    "${probe_arguments[2]}" "${probe_arguments[3]}" \
    "${probe_arguments[4]}" "${probe_arguments[5]:-running}"
  exit $?
fi
if [[ "$probe_mode" == signal ]]; then
  run_signal_contract_probe \
    "${probe_arguments[0]}" "${probe_arguments[1]}"
  exit 0
fi
if [[ "$probe_mode" == ownership_signal ]]; then
  run_ownership_signal_contract_probe \
    "${probe_arguments[0]}" "${probe_arguments[1]}"
  exit 0
fi

create_fixture_ca
prepare_restore_license
build_images
populate_live_secret_sources
initialize_resources
initialize_secret_volume
verify_secret_consumer_reads
start_dependencies
start_application
verify_compose_service_claims
create_teacher_and_sample
case "$e2e_group" in
  all|phase5|phase5-mobile) seed_phase5_browser_data ;;
esac
audit_container_metadata \
  "$postgres" "$redis" "$primary_aistor" "$remote_s3" \
  "$app" "$worker" "$fake_ai"
case "$e2e_group" in
  all|phase5|phase5-mobile|resources) prepare_browser_workspace ;;
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
    reopen_phase5_browser_alert ||
      status="$(preserve_first_failure "$status" "$?")"
    run_all_mobile || status="$(preserve_first_failure "$status" "$?")"
    remove_phase5_browser_backup_seed ||
      status="$(preserve_first_failure "$status" "$?")"
    run_resource_sample 60 ||
      status="$(preserve_first_failure "$status" "$?")"
    run_restore_proof || status="$(preserve_first_failure "$status" "$?")"
    exit "$status"
    ;;
esac
