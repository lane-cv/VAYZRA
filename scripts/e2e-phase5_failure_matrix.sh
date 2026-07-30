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

run_live_matrix() {
  [[ -x "$SANITIZER" && -x "$PUBLISHER" ]] ||
    fail 'live failure-matrix artifact dependencies are absent' ||
    return 1
  fail 'live failure-matrix orchestration is blocked on the pending Phase 5 harness integration'
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
