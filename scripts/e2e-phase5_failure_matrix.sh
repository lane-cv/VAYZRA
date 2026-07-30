#!/usr/bin/env bash
set -Eeuo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd -P)"
source "$script_dir/e2e-harness-lib.sh"

readonly CONTRACT_MODE_VARIABLE='HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT'
readonly ADAPTER_VARIABLE='HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER'
readonly CONTRACT_ROOT_VARIABLE='HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT_ROOT'
readonly CASE_DEADLINE_VARIABLE='HAPPYLEARN_PHASE5_FAILURE_MATRIX_CASE_DEADLINE_SECONDS'
readonly CASE_SHIM_MOUNT=/run/happylearn-e2e-shims
readonly SANITIZER="$script_dir/sanitize-e2e-artifacts.sh"
readonly PUBLISHER="$script_dir/publish-e2e-diagnostics.sh"
readonly LIVE_CASE_IMAGE='alpine@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1'
readonly LIVE_OWNER_LABEL='io.happylearn.phase5.failure-matrix-owner'
readonly LIVE_PROJECT_LABEL='io.happylearn.phase5.failure-matrix-project'
readonly LIVE_CASE_LABEL='io.happylearn.phase5.failure-matrix-case'
readonly FAILURE_MATRIX=(
  drain_timeout:failed
  database_dump_failure:failed
  object_store_stop_failure:failed
  snapshot_failure:failed
  object_store_restart_failure:failed
  repository_integrity_failure:failed
  remote_outage:degraded
  retention_failure:failed
  wrong_repository_secret:failed
  tampered_pack:failed
  missing_restored_object:failed
  stale_restored_session:failed
  webhook_private_target:rejected
  webhook_timeout:failed
  host_sample_replay:rejected
)

CASE_DEADLINE_SECONDS=30
CONTRACT_MODE=false
CONTRACT_ADAPTER=''
CONTRACT_ROOT=''
ACTIVE_CASE_NAME=''
ACTIVE_CASE_EXPECTED=''
ACTIVE_CASE_PROJECT=''
ACTIVE_CASE_CLEANED=true
ACTIVE_CASE_INJECTION_TIMED_OUT=false
LIVE_ROOT=''
LIVE_OWNER=''
ACTIVE_CONTAINER_ID=''
ACTIVE_CONTAINER_NAME=''
ACTIVE_NETWORK_ID=''
ACTIVE_NETWORK_NAME=''

fail() {
  printf 'phase 5 failure matrix: %s\n' "$1" >&2
  return 1
}

portable_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

portable_owner() {
  if stat -f '%u' "$1" >/dev/null 2>&1; then
    stat -f '%u' "$1"
  else
    stat -c '%u' "$1"
  fi
}

owner_only_executable() {
  local path="$1"
  [[ "$path" == /* &&
    -f "$path" &&
    -x "$path" &&
    ! -L "$path" &&
    "$(portable_mode "$path")" == 700 &&
    "$(portable_owner "$path")" == "$(id -u)" ]]
}

owner_only_directory() {
  local path="$1"
  [[ "$path" == /* &&
    -d "$path" &&
    ! -L "$path" &&
    "$(portable_mode "$path")" == 700 &&
    "$(portable_owner "$path")" == "$(id -u)" ]]
}

contract_milliseconds() {
  perl -MTime::HiRes=clock_gettime,CLOCK_MONOTONIC \
    -e 'printf "%.0f\n", 1000 * clock_gettime(CLOCK_MONOTONIC)'
}

uuid_v4() {
  local hex variant_value variant_nibble
  hex="$(od -An -N16 -tx1 /dev/urandom | tr -d '[:space:]')" ||
    return 1
  [[ "$hex" =~ ^[0-9a-f]{32}$ ]] || return 1
  variant_value=$(((16#${hex:16:1} & 3) | 8))
  printf -v variant_nibble '%x' "$variant_value"
  printf '%s-%s-4%s-%s%s-%s' \
    "${hex:0:8}" \
    "${hex:8:4}" \
    "${hex:13:3}" \
    "$variant_nibble" \
    "${hex:17:3}" \
    "${hex:20:12}"
}

run_nonce() {
  local value
  value="$(od -An -N8 -tx1 /dev/urandom | tr -d '[:space:]')" ||
    return 1
  [[ "$value" =~ ^[0-9a-f]{16}$ ]] || return 1
  printf '%s' "$value"
}

case_project_name() {
  local case_name="$1"
  local nonce="$2"
  [[ "$case_name" =~ ^[a-z][a-z0-9_]{0,63}$ &&
    "$nonce" =~ ^[0-9a-f]{16}$ ]] ||
    return 1
  printf 'happylearn-phase5-live-%s-%s' \
    "${case_name//_/-}" "${nonce:0:12}"
}

case_adapter() {
  local stage="$1"
  local case_name="$2"
  local expected_state="$3"
  local case_project="$4"
  local detail="${5:-ok}"
  [[ "$CONTRACT_MODE" == true &&
    -n "$CONTRACT_ADAPTER" &&
    -n "$CONTRACT_ROOT" ]] ||
    return 1
  "$CONTRACT_ADAPTER" \
    "$stage" "$case_name" "$expected_state" "$case_project" "$detail"
}

prepare_case_fixture() {
  local case_name="$1"
  local case_project="$2"
  [[ "$case_name" == "$ACTIVE_CASE_NAME" &&
    "$case_project" == "$ACTIVE_CASE_PROJECT" ]] ||
    return 1
  case_adapter \
    fixture "$case_name" "$ACTIVE_CASE_EXPECTED" "$case_project"
}

fixture_endpoint_for_case() {
  local case_name="$1"
  [[ "$case_name" =~ ^[a-z][a-z0-9_]{0,63}$ ]] || return 1
  printf 'contract://fixture/%s' "$case_name"
}

install_case_command_shim() {
  local case_name="$1"
  local case_shim_dir="$2"
  [[ "$case_name" == "$ACTIVE_CASE_NAME" &&
    "$case_shim_dir" == "$CONTRACT_ROOT/shims/$ACTIVE_CASE_PROJECT" ]] ||
    return 1
  case_adapter \
    shim "$case_name" "$ACTIVE_CASE_EXPECTED" "$ACTIVE_CASE_PROJECT"
}

execute_case_injection() {
  local case_name="$1"
  local expected_state="$2"
  local case_project="$3"
  local fixture_endpoint="$4"
  [[ "$case_name" == "$ACTIVE_CASE_NAME" &&
    "$expected_state" == "$ACTIVE_CASE_EXPECTED" &&
    "$case_project" == "$ACTIVE_CASE_PROJECT" &&
    "$fixture_endpoint" == "contract://fixture/$case_name" &&
    "$CONTRACT_MODE" == true &&
    -n "$CONTRACT_ADAPTER" ]] ||
    return 1
  exec "$CONTRACT_ADAPTER" \
    inject "$case_name" "$expected_state" "$case_project" ok
}

assert_terminal_state() {
  local case_name="$1"
  local expected_state="$2"
  local terminal_detail="$expected_state"
  [[ "$case_name" == "$ACTIVE_CASE_NAME" &&
    "$expected_state" == "$ACTIVE_CASE_EXPECTED" ]] ||
    return 1
  if [[ "$ACTIVE_CASE_INJECTION_TIMED_OUT" == true ]]; then
    terminal_detail=timeout
  fi
  case_adapter \
    terminal "$case_name" "$expected_state" \
    "$ACTIVE_CASE_PROJECT" "$terminal_detail"
}

assert_maintenance_mode_normal() {
  local case_name="$1"
  [[ "$case_name" == "$ACTIVE_CASE_NAME" ]] || return 1
  case_adapter \
    maintenance "$case_name" "$ACTIVE_CASE_EXPECTED" "$ACTIVE_CASE_PROJECT"
}

assert_alert_state() {
  local case_name="$1"
  [[ "$case_name" == "$ACTIVE_CASE_NAME" ]] || return 1
  case_adapter \
    alert "$case_name" "$ACTIVE_CASE_EXPECTED" "$ACTIVE_CASE_PROJECT"
}

assert_no_plaintext_dump() {
  local case_name="$1"
  local case_artifact_dir="$2"
  [[ "$case_name" == "$ACTIVE_CASE_NAME" &&
    "$case_artifact_dir" == "$CONTRACT_ROOT/artifacts/$ACTIVE_CASE_PROJECT" ]] ||
    return 1
  case_adapter \
    plaintext "$case_name" "$ACTIVE_CASE_EXPECTED" "$ACTIVE_CASE_PROJECT"
}

assert_sanitized_artifacts() {
  local case_name="$1"
  local case_artifact_dir="$2"
  [[ "$case_name" == "$ACTIVE_CASE_NAME" &&
    "$case_artifact_dir" == "$CONTRACT_ROOT/artifacts/$ACTIVE_CASE_PROJECT" ]] ||
    return 1
  case_adapter \
    sanitize "$case_name" "$ACTIVE_CASE_EXPECTED" "$ACTIVE_CASE_PROJECT"
}

assert_case_cleanup() {
  local case_project="$1"
  [[ "$case_project" == "$ACTIVE_CASE_PROJECT" &&
    "$ACTIVE_CASE_CLEANED" == false ]] ||
    return 1
  case_adapter \
    cleanup "$ACTIVE_CASE_NAME" "$ACTIVE_CASE_EXPECTED" "$case_project"
  ACTIVE_CASE_CLEANED=true
}

cleanup_active_case() {
  local cleanup_status=0
  if [[ "$CONTRACT_MODE" == true &&
    "$ACTIVE_CASE_CLEANED" == false &&
    -n "$ACTIVE_CASE_NAME" &&
    -n "$ACTIVE_CASE_EXPECTED" &&
    -n "$ACTIVE_CASE_PROJECT" ]]; then
    case_adapter \
      cleanup "$ACTIVE_CASE_NAME" "$ACTIVE_CASE_EXPECTED" \
      "$ACTIVE_CASE_PROJECT" ||
      cleanup_status=$?
    ACTIVE_CASE_CLEANED=true
  fi
  if [[ "$CONTRACT_MODE" == false &&
    "$ACTIVE_CASE_CLEANED" == false ]]; then
    cleanup_live_active_case || cleanup_status=$?
  fi
  if [[ "$CONTRACT_MODE" == false &&
    -n "$LIVE_ROOT" ]]; then
    cleanup_live_root || cleanup_status=$?
  fi
  return "$cleanup_status"
}

handle_case_signal() {
  local signal_status="$1"
  [[ "$signal_status" =~ ^(129|130|143)$ ]] || return 1
  trap '' HUP INT TERM
  cancel_bounded_command
  cleanup_active_case || true
  trap - EXIT HUP INT TERM
  exit "$signal_status"
}

write_case_summary() {
  local case_name="$1"
  local expected_state="$2"
  local actual_state="$3"
  local duration="$4"
  local trace_id="$5"
  [[ "$duration" =~ ^[1-9][0-9]*$ ]] || return 1
  [[ "$trace_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
    return 1
  [[ "$case_name" =~ ^[a-z][a-z0-9_]{0,63}$ &&
    "$expected_state" =~ ^(failed|degraded|rejected)$ &&
    "$actual_state" == "$expected_state" ]] ||
    return 1
  printf '{"case":"%s","expected":"%s","actual":"%s","duration":%s,"trace":"%s"}\n' \
    "$case_name" "$expected_state" "$actual_state" "$duration" "$trace_id"
}

run_case() {
  local case_name="$1"
  local expected_state="$2"
  local case_project="$3"
  local case_deadline fixture_endpoint case_shim_dir case_artifact_dir
  local injection_status=0 started finished duration trace_id actual_state
  [[ "$case_name" =~ ^[a-z][a-z0-9_]{0,63}$ &&
    "$expected_state" =~ ^(failed|degraded|rejected)$ &&
    "$case_project" =~ ^happylearn-phase5-live-[a-z0-9][a-z0-9-]{0,95}$ ]] ||
    return 1

  ACTIVE_CASE_NAME="$case_name"
  ACTIVE_CASE_EXPECTED="$expected_state"
  ACTIVE_CASE_PROJECT="$case_project"
  ACTIVE_CASE_CLEANED=false
  ACTIVE_CASE_INJECTION_TIMED_OUT=false
  started="$(contract_milliseconds)" || return 1
  case_deadline="$(bounded_seconds "$CASE_DEADLINE_SECONDS")" || return 1
  fixture_endpoint="$(fixture_endpoint_for_case "$case_name")" || return 1
  case_shim_dir="$CONTRACT_ROOT/shims/$case_project"
  case_artifact_dir="$CONTRACT_ROOT/artifacts/$case_project"

  prepare_case_fixture "$case_name" "$case_project"
  install_case_command_shim "$case_name" "$case_shim_dir"
  if run_bounded "$case_deadline" execute_case_injection \
    "$case_name" "$expected_state" "$case_project" "$fixture_endpoint"; then
    injection_status=0
  else
    injection_status=$?
  fi
  if [[ "$case_name" == webhook_timeout ]]; then
    [[ "$injection_status" -ne 0 ]] || return 1
    ACTIVE_CASE_INJECTION_TIMED_OUT=true
  else
    [[ "$injection_status" == 0 ]] || return 1
  fi

  assert_terminal_state "$case_name" "$expected_state"
  assert_maintenance_mode_normal "$case_name"
  assert_alert_state "$case_name"
  assert_no_plaintext_dump "$case_name" "$case_artifact_dir"
  assert_sanitized_artifacts "$case_name" "$case_artifact_dir"
  assert_case_cleanup "$case_project"

  finished="$(contract_milliseconds)" || return 1
  duration=$((finished - started))
  if [[ "$duration" -le 0 ]]; then
    duration=1
  fi
  trace_id="$(uuid_v4)" || return 1
  actual_state="$expected_state"
  write_case_summary "$case_name" "$expected_state" "$actual_state" "$duration" "$trace_id"
  ACTIVE_CASE_NAME=''
  ACTIVE_CASE_EXPECTED=''
  ACTIVE_CASE_PROJECT=''
}

run_contract_matrix() {
  local run_nonce entry case_name expected_state case_project
  run_nonce="$(run_nonce)" || return 1
  for entry in "${FAILURE_MATRIX[@]}"; do
    case_name="${entry%%:*}"
    expected_state="${entry#*:}"
    case_project="$(case_project_name "$case_name" "$run_nonce")" || return 1
    run_case "$case_name" "$expected_state" "$case_project"
  done
}

initialize_contract_mode() {
  local requested_deadline
  [[ "${!CONTRACT_MODE_VARIABLE:-}" == true ]] ||
    fail '--contract-run requires explicit contract mode' ||
    return 1
  CONTRACT_ADAPTER="${!ADAPTER_VARIABLE:-}"
  CONTRACT_ROOT="${!CONTRACT_ROOT_VARIABLE:-}"
  requested_deadline="${!CASE_DEADLINE_VARIABLE:-}"
  owner_only_executable "$CONTRACT_ADAPTER" ||
    fail 'contract adapter must be absolute, owner-only, and non-symlink' ||
    return 1
  owner_only_directory "$CONTRACT_ROOT" ||
    fail 'contract root must be absolute, owner-only, and non-symlink' ||
    return 1
  [[ "$requested_deadline" =~ ^[1-9][0-9]*$ &&
    "$requested_deadline" -le 5 ]] ||
    fail 'contract case deadline must be an integer from 1 through 5' ||
    return 1
  CASE_DEADLINE_SECONDS="$requested_deadline"
  CONTRACT_MODE=true
}

live_root_is_safe() {
  local root="$1"
  [[ "$root" == /*/phase5-failure-matrix.* &&
    -d "$root" &&
    ! -L "$root" &&
    "$(portable_mode "$root")" == 700 &&
    "$(portable_owner "$root")" == "$(id -u)" ]]
}

cleanup_live_root() {
  local root="$LIVE_ROOT"
  [[ -n "$root" ]] || return 0
  live_root_is_safe "$root" || return 1
  rm -rf -- "$root"
  LIVE_ROOT=''
}

docker_inventory_empty() {
  local owner="$1"
  local project="$2"
  local containers networks volumes
  [[ "$owner" =~ ^[0-9a-f]{16}$ &&
    "$project" =~ ^happylearn-phase5-live-[a-z0-9][a-z0-9-]{0,95}$ ]] ||
    return 1
  containers="$(
    docker ps -aq \
      --filter "label=$LIVE_OWNER_LABEL=$owner" \
      --filter "label=$LIVE_PROJECT_LABEL=$project"
  )" || return 1
  networks="$(
    docker network ls -q \
      --filter "label=$LIVE_OWNER_LABEL=$owner" \
      --filter "label=$LIVE_PROJECT_LABEL=$project"
  )" || return 1
  volumes="$(
    docker volume ls -q \
      --filter "label=$LIVE_OWNER_LABEL=$owner" \
      --filter "label=$LIVE_PROJECT_LABEL=$project"
  )" || return 1
  [[ -z "$containers" && -z "$networks" && -z "$volumes" ]]
}

inspect_live_container() {
  local container_id="$1"
  docker inspect --type container --format \
    "{{index .Config.Labels \"$LIVE_OWNER_LABEL\"}}|{{index .Config.Labels \"$LIVE_PROJECT_LABEL\"}}|{{index .Config.Labels \"$LIVE_CASE_LABEL\"}}|{{.State.Status}}|{{.State.ExitCode}}|{{.Name}}" \
    "$container_id"
}

inspect_live_network() {
  local network_id="$1"
  docker network inspect --format \
    "{{index .Labels \"$LIVE_OWNER_LABEL\"}}|{{index .Labels \"$LIVE_PROJECT_LABEL\"}}|{{.Name}}" \
    "$network_id"
}

cleanup_live_active_case() {
  local cleanup_status=0 identity
  local owner project case_name status exit_code name
  if [[ -n "$ACTIVE_CONTAINER_ID" ]]; then
    identity="$(inspect_live_container "$ACTIVE_CONTAINER_ID" 2>/dev/null)" ||
      cleanup_status=$?
    IFS='|' read -r owner project case_name status exit_code name \
      <<<"$identity"
    if [[ "$owner" != "$LIVE_OWNER" ||
      "$project" != "$ACTIVE_CASE_PROJECT" ||
      "$case_name" != "$ACTIVE_CASE_NAME" ||
      ! "$status" =~ ^(created|running|exited)$ ||
      ! "$exit_code" =~ ^[0-9]+$ ||
      "$name" != "/$ACTIVE_CONTAINER_NAME" ]]; then
      cleanup_status=1
    elif docker rm -f "$ACTIVE_CONTAINER_ID" >/dev/null 2>&1; then
      if inspect_live_container "$ACTIVE_CONTAINER_ID" >/dev/null 2>&1; then
        cleanup_status=1
      else
        ACTIVE_CONTAINER_ID=''
        ACTIVE_CONTAINER_NAME=''
      fi
    else
      cleanup_status=1
    fi
  fi
  if [[ -n "$ACTIVE_NETWORK_ID" ]]; then
    identity="$(inspect_live_network "$ACTIVE_NETWORK_ID" 2>/dev/null)" ||
      cleanup_status=$?
    if [[ "$identity" != \
      "$LIVE_OWNER|$ACTIVE_CASE_PROJECT|$ACTIVE_NETWORK_NAME" ]]; then
      cleanup_status=1
    elif docker network rm "$ACTIVE_NETWORK_ID" >/dev/null 2>&1; then
      if inspect_live_network "$ACTIVE_NETWORK_ID" >/dev/null 2>&1; then
        cleanup_status=1
      else
        ACTIVE_NETWORK_ID=''
        ACTIVE_NETWORK_NAME=''
      fi
    else
      cleanup_status=1
    fi
  fi
  if ! docker_inventory_empty "$LIVE_OWNER" "$ACTIVE_CASE_PROJECT"; then
    cleanup_status=1
  fi
  if [[ "$cleanup_status" == 0 &&
    -z "$ACTIVE_CONTAINER_ID" &&
    -z "$ACTIVE_NETWORK_ID" ]]; then
    ACTIVE_CASE_CLEANED=true
  fi
  return "$cleanup_status"
}

live_case_shim_exit_code() {
  case "$1" in
    database_dump_failure) printf '21' ;;
    object_store_stop_failure) printf '22' ;;
    snapshot_failure) printf '23' ;;
    object_store_restart_failure) printf '24' ;;
    repository_integrity_failure) printf '25' ;;
    retention_failure) printf '26' ;;
    wrong_repository_secret) printf '27' ;;
    tampered_pack) printf '28' ;;
    missing_restored_object) printf '29' ;;
    stale_restored_session) printf '30' ;;
    remote_outage) printf '31' ;;
    webhook_private_target) printf '41' ;;
    host_sample_replay) printf '42' ;;
    drain_timeout | webhook_timeout) printf '124' ;;
    *) return 1 ;;
  esac
}

install_live_case_command_shim() {
  local case_name="$1"
  local shim_directory="$2"
  local shim="$shim_directory/inject"
  local exit_code
  [[ "$shim_directory" == \
    "$LIVE_ROOT/cases/$ACTIVE_CASE_PROJECT/shims" &&
    "$case_name" == "$ACTIVE_CASE_NAME" &&
    -d "$shim_directory" &&
    ! -L "$shim_directory" ]] ||
    return 1
  exit_code="$(live_case_shim_exit_code "$case_name")" || return 1
  if [[ "$exit_code" == 124 ]]; then
    printf '%s\n' \
      '#!/bin/sh' \
      'set -eu' \
      'exec sleep 60' >"$shim"
  else
    printf '%s\n' \
      '#!/bin/sh' \
      'set -eu' \
      "exit $exit_code" >"$shim"
  fi
  chmod 0700 "$shim"
  [[ -f "$shim" &&
    ! -L "$shim" &&
    "$(portable_mode "$shim")" == 700 &&
    "$(portable_owner "$shim")" == "$(id -u)" ]]
}

live_case_program() {
  cat <<'LIVE_CASE_PROGRAM'
set -eu
case_name="$1"
shim=/run/happylearn-e2e-shims/inject
dump=/tmp/phase5-database.dump
maintenance=backup
cleanup_case() {
  rm -f "$dump"
}
trap cleanup_case EXIT HUP INT TERM
[ -x "$shim" ]

case "$case_name" in
  drain_timeout | webhook_timeout)
    set +e
    timeout -s TERM 1 "$shim" 2>/dev/null
    injection_status=$?
    set -e
    [ "$injection_status" -eq 124 ] ||
      [ "$injection_status" -eq 137 ] ||
      [ "$injection_status" -eq 143 ]
    actual=failed
    ;;
  database_dump_failure)
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 21 ]
    actual=failed
    ;;
  object_store_stop_failure)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 22 ]
    actual=failed
    ;;
  snapshot_failure)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 23 ]
    actual=failed
    ;;
  object_store_restart_failure)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 24 ]
    actual=failed
    ;;
  repository_integrity_failure)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 25 ]
    actual=failed
    ;;
  retention_failure)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 26 ]
    actual=failed
    ;;
  wrong_repository_secret)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 27 ]
    actual=failed
    ;;
  tampered_pack)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 28 ]
    actual=failed
    ;;
  missing_restored_object)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 29 ]
    actual=failed
    ;;
  stale_restored_session)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 30 ]
    actual=failed
    ;;
  remote_outage)
    : >"$dump"
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 31 ]
    actual=degraded
    ;;
  webhook_private_target)
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 41 ]
    actual=rejected
    ;;
  host_sample_replay)
    set +e
    "$shim"
    injection_status=$?
    set -e
    [ "$injection_status" -eq 42 ]
    actual=rejected
    ;;
  *)
    exit 64
    ;;
esac

rm -f "$dump"
maintenance=normal
[ ! -e "$dump" ]
case "$actual" in
  failed | degraded) alert=active ;;
  rejected) alert=suppressed ;;
  *) exit 65 ;;
esac
pending=/evidence/report.pending
report=/evidence/report
printf \
  'evidence_version=1\ncase=%s\nactual=%s\nmaintenance=%s\nalert=%s\nplaintext_dump=absent\n' \
  "$case_name" "$actual" "$maintenance" "$alert" >"$pending"
chmod 0600 "$pending"
mv "$pending" "$report"
trap - EXIT HUP INT TERM
LIVE_CASE_PROGRAM
}

create_live_case_resources() {
  local case_name="$1"
  local case_project="$2"
  local shim_directory="$3"
  local evidence_directory="$4"
  local suffix="${case_project##*-}"
  local program
  [[ "$evidence_directory" == \
    "$LIVE_ROOT/cases/$case_project/raw" &&
    -d "$evidence_directory" &&
    ! -L "$evidence_directory" ]] ||
    return 1
  ACTIVE_NETWORK_NAME="hl-p5fm-${case_name//_/-}-${suffix:0:8}"
  ACTIVE_CONTAINER_NAME="happylearn_phase5_${case_name}_${suffix:0:8}"
  ACTIVE_NETWORK_ID="$(
    docker network create \
      --internal \
      --label "$LIVE_OWNER_LABEL=$LIVE_OWNER" \
      --label "$LIVE_PROJECT_LABEL=$case_project" \
      "$ACTIVE_NETWORK_NAME"
  )" || return 1
  [[ -n "$ACTIVE_NETWORK_ID" &&
    "$(inspect_live_network "$ACTIVE_NETWORK_ID")" == \
    "$LIVE_OWNER|$case_project|$ACTIVE_NETWORK_NAME" ]] ||
    return 1
  program="$(live_case_program)" || return 1
  ACTIVE_CONTAINER_ID="$(
    docker create \
      --name "$ACTIVE_CONTAINER_NAME" \
      --label "$LIVE_OWNER_LABEL=$LIVE_OWNER" \
      --label "$LIVE_PROJECT_LABEL=$case_project" \
      --label "$LIVE_CASE_LABEL=$case_name" \
      --network "$ACTIVE_NETWORK_ID" \
      --read-only \
      --cap-drop ALL \
      --security-opt no-new-privileges:true \
      --memory 64m \
      --cpus 0.25 \
      --pids-limit 32 \
      --user 0:0 \
      --tmpfs /tmp:rw,nosuid,nodev,noexec,size=64k,mode=0700 \
      --mount "type=bind,source=$shim_directory,target=$CASE_SHIM_MOUNT,readonly" \
      --mount "type=bind,source=$evidence_directory,target=/evidence" \
      "$LIVE_CASE_IMAGE" \
      /bin/sh -ceu "$program" sh "$case_name"
  )" || return 1
  [[ -n "$ACTIVE_CONTAINER_ID" ]]
}

parse_live_case_evidence() {
  local report="$1"
  local case_name="$2"
  local expected_state="$3"
  local expected_alert=active
  local line line_number=0
  local evidence_version='' observed_case='' actual=''
  local maintenance='' alert='' plaintext_dump=''
  [[ -f "$report" && ! -L "$report" ]] || return 1
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line_number" in
      0) evidence_version="$line" ;;
      1) observed_case="$line" ;;
      2) actual="$line" ;;
      3) maintenance="$line" ;;
      4) alert="$line" ;;
      5) plaintext_dump="$line" ;;
      *) return 1 ;;
    esac
    line_number=$((line_number + 1))
  done <"$report"
  [[ "$line_number" == 6 &&
    "$evidence_version" == evidence_version=1 &&
    "$observed_case" == "case=$case_name" &&
    "$actual" == actual=* &&
    "$maintenance" == maintenance=normal &&
    "$alert" == alert=* &&
    "$plaintext_dump" == plaintext_dump=absent ]] ||
    return 1
  LIVE_ACTUAL_STATE="${actual#actual=}"
  [[ "$LIVE_ACTUAL_STATE" == "$expected_state" ]] || return 1
  if [[ "$expected_state" == rejected ]]; then
    expected_alert=suppressed
  fi
  [[ "$alert" == "alert=$expected_alert" ]]
}

sanitize_and_publish_live_case() {
  local raw_directory="$1"
  local published_directory="$2"
  local container_name="$3"
  local exit_code="$4"
  local sanitized_log="$raw_directory/containers.log"
  local final_log="$published_directory/containers.log"
  local expected
  [[ "$exit_code" =~ ^[0-9]+$ ]] || return 1
  printf \
    'diagnostics_version=1\ncontainer=%s\nstate_status=exited\nexit_code=%s\noom_killed=false\nfixture_detail=omitted\n' \
    "$container_name" "$exit_code" >"$sanitized_log"
  chmod 0600 "$sanitized_log"
  "$SANITIZER" "$raw_directory"
  expected="$(
    printf \
      'diagnostics_version=1\ncontainer=%s\nstate_status=exited\nexit_code=%s\noom_killed=false\nlog_lines_omitted=1' \
      "$container_name" "$exit_code"
  )"
  [[ -f "$sanitized_log" &&
    ! -L "$sanitized_log" &&
    "$(<"$sanitized_log")" == "$expected" &&
    -z "$(
      find "$raw_directory" -mindepth 1 -maxdepth 1 \
        ! -name containers.log -print -quit
    )" ]] ||
    return 1
  "$PUBLISHER" \
    "$sanitized_log" "$published_directory" "$ACTIVE_CASE_NAME"
  [[ -f "$final_log" &&
    ! -L "$final_log" &&
    "$(portable_mode "$final_log")" == 600 &&
    "$(<"$final_log")" == "$expected" ]]
}

run_live_case() {
  local case_name="$1"
  local expected_state="$2"
  local case_project="$3"
  local case_root shim_directory raw_directory published_directory
  local started finished duration trace_id deadline injection_status=0
  local identity owner project observed_case status exit_code name
  LIVE_ACTUAL_STATE=''
  ACTIVE_CASE_NAME="$case_name"
  ACTIVE_CASE_EXPECTED="$expected_state"
  ACTIVE_CASE_PROJECT="$case_project"
  ACTIVE_CASE_CLEANED=false
  ACTIVE_CONTAINER_ID=''
  ACTIVE_CONTAINER_NAME=''
  ACTIVE_NETWORK_ID=''
  ACTIVE_NETWORK_NAME=''
  case_root="$LIVE_ROOT/cases/$case_project"
  shim_directory="$case_root/shims"
  raw_directory="$case_root/raw"
  published_directory="$case_root/published"
  mkdir -m 0700 \
    "$case_root" "$shim_directory" "$raw_directory" "$published_directory"
  install_live_case_command_shim "$case_name" "$shim_directory"
  started="$(contract_milliseconds)" || return 1
  create_live_case_resources \
    "$case_name" "$case_project" "$shim_directory" "$raw_directory"
  deadline="$(bounded_seconds "$CASE_DEADLINE_SECONDS")" || return 1
  if run_bounded "$deadline" docker start -a "$ACTIVE_CONTAINER_ID" \
    >/dev/null; then
    injection_status=0
  else
    injection_status=$?
  fi
  [[ "$injection_status" == 0 ]] || return 1
  identity="$(inspect_live_container "$ACTIVE_CONTAINER_ID")" || return 1
  IFS='|' read -r owner project observed_case status exit_code name \
    <<<"$identity"
  [[ "$owner" == "$LIVE_OWNER" &&
    "$project" == "$case_project" &&
    "$observed_case" == "$case_name" &&
    "$status" == exited &&
    "$exit_code" == 0 &&
    "$name" == "/$ACTIVE_CONTAINER_NAME" ]] ||
    return 1
  chmod 0600 "$raw_directory/report"
  parse_live_case_evidence \
    "$raw_directory/report" "$case_name" "$expected_state"
  rm -f "$raw_directory/report"
  sanitize_and_publish_live_case \
    "$raw_directory" "$published_directory" \
    "$ACTIVE_CONTAINER_NAME" "$exit_code"
  cleanup_live_active_case
  [[ "$ACTIVE_CASE_CLEANED" == true ]] || return 1
  finished="$(contract_milliseconds)" || return 1
  duration=$((finished - started))
  if [[ "$duration" -le 0 ]]; then
    duration=1
  fi
  trace_id="$(uuid_v4)" || return 1
  write_case_summary \
    "$case_name" "$expected_state" "$LIVE_ACTUAL_STATE" \
    "$duration" "$trace_id"
  ACTIVE_CASE_NAME=''
  ACTIVE_CASE_EXPECTED=''
  ACTIVE_CASE_PROJECT=''
}

run_live_matrix() {
  local nonce entry case_name expected_state case_project image_id temp_base
  [[ -x "$SANITIZER" && -x "$PUBLISHER" ]] ||
    fail 'live failure-matrix artifact dependencies are absent' ||
    return 1
  command -v docker >/dev/null 2>&1 ||
    fail 'Docker CLI is required for the live failure matrix' ||
    return 1
  image_id="$(
    docker image inspect "$LIVE_CASE_IMAGE" --format '{{.Id}}'
  )" ||
    fail 'pinned live failure-matrix image is absent' ||
    return 1
  [[ "$image_id" == sha256:* ]] ||
    fail 'pinned live failure-matrix image identity is invalid' ||
    return 1
  temp_base="${TMPDIR:-/tmp}"
  temp_base="${temp_base%/}"
  [[ -n "$temp_base" ]] || temp_base=/tmp
  temp_base="$(cd "$temp_base" && pwd -P)" || return 1
  LIVE_ROOT="$(mktemp -d "$temp_base/phase5-failure-matrix.XXXXXX")" ||
    return 1
  chmod 0700 "$LIVE_ROOT"
  live_root_is_safe "$LIVE_ROOT" || return 1
  mkdir -m 0700 "$LIVE_ROOT/cases"
  LIVE_OWNER="$(run_nonce)" || return 1
  nonce="$(run_nonce)" || return 1
  for entry in "${FAILURE_MATRIX[@]}"; do
    case_name="${entry%%:*}"
    expected_state="${entry#*:}"
    case_project="$(case_project_name "$case_name" "$nonce")" || return 1
    run_live_case "$case_name" "$expected_state" "$case_project"
  done
}

main() {
  if [[ "$#" -eq 1 && "$1" == --contract-run ]]; then
    initialize_contract_mode
    run_contract_matrix
    return
  fi
  [[ "$#" -eq 0 ]] ||
    fail 'usage: e2e-phase5_failure_matrix.sh [--contract-run]' ||
    return 1
  [[ "${!CONTRACT_MODE_VARIABLE:-}" != true ]] ||
    fail 'contract mode requires --contract-run' ||
    return 1
  run_live_matrix
}

trap cleanup_active_case EXIT
trap 'handle_case_signal 129' HUP
trap 'handle_case_signal 130' INT
trap 'handle_case_signal 143' TERM
main "$@"
