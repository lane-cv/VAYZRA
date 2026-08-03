#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

readonly FAILURE_MATRIX=(
  unsafe_secret:preflight_rejected
  insufficient_disk:preflight_rejected
  unavailable_digest:preflight_rejected
  pre_release_backup_failure:pre_maintenance_failed
  drain_timeout:maintenance_failed_safe
  migration_lock_conflict:previous_image_rolled_back
  migration_failure:previous_image_rolled_back
  app_readiness_failure:previous_image_rolled_back
  worker_readiness_failure:previous_image_rolled_back
  object_store_restart_failure:previous_image_rolled_back
  smoke_failure:previous_image_rolled_back
  signal_each_durable_step:resumable_or_failed_safe
  previous_image_success:previous_image_rolled_back
  previous_image_incompatible:maintenance_failed_safe
  previous_image_readiness_failure:maintenance_failed_safe
)
readonly ASSERTION_STAGES=(durable_state maintenance traffic manifests schema recovery diagnostics)
readonly adapter_variable=HAPPYLEARN_PHASE6_FAILURE_MATRIX_ADAPTER
readonly root_variable=HAPPYLEARN_PHASE6_FAILURE_MATRIX_ROOT
readonly deadline_variable=HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE_DEADLINE_SECONDS
readonly selected_case_variable=HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE

adapter=${HAPPYLEARN_PHASE6_FAILURE_MATRIX_ADAPTER:-}
matrix_root=${HAPPYLEARN_PHASE6_FAILURE_MATRIX_ROOT:-}
case_deadline=${HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE_DEADLINE_SECONDS:-900}
selected_case=${HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE:-}
active_case=''
active_expected=''
active_clean=true

fail() { printf 'phase6 release failure matrix: FAIL: %s\n' "$1" >&2; exit 1; }

if [[ -z $adapter && -z $matrix_root ]]; then
  exec env HAPPYLEARN_E2E_GROUP=failure-matrix \
    "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/e2e-phase6.sh"
fi
[[ -n $adapter && -n $matrix_root ]] || fail 'adapter configuration is incomplete'
portable_mode() {
  if stat -c '%a' -- "$1" >/dev/null 2>&1; then stat -c '%a' -- "$1"; else stat -f '%Lp' -- "$1"; fi
}
portable_owner() {
  if stat -c '%u' -- "$1" >/dev/null 2>&1; then stat -c '%u' -- "$1"; else stat -f '%u' -- "$1"; fi
}
owner_only_executable() {
  [[ $1 == /* && -f $1 && -x $1 && ! -L $1 && $(portable_mode "$1") == 700 && $(portable_owner "$1") == "$(id -u)" ]]
}
owner_only_directory() {
  [[ $1 == /* && -d $1 && ! -L $1 && $(portable_mode "$1") == 700 && $(portable_owner "$1") == "$(id -u)" ]]
}

[[ $case_deadline =~ ^[1-9][0-9]*$ ]] || fail "$deadline_variable is invalid"
(( case_deadline <= 14400 )) || fail "$deadline_variable is invalid"
if [[ -n $selected_case ]]; then
  [[ $selected_case =~ ^[a-z][a-z0-9_]{0,63}$ ]] || fail "$selected_case_variable is invalid"
  selected_known=false
  for entry in "${FAILURE_MATRIX[@]}"; do
    [[ ${entry%%:*} != "$selected_case" ]] || selected_known=true
  done
  [[ $selected_known == true ]] || fail "$selected_case_variable is invalid"
fi
owner_only_executable "$adapter" || fail "$adapter_variable is unsafe"
owner_only_directory "$matrix_root" || fail "$root_variable is unsafe"
[[ -x $(command -v timeout) ]] || fail 'timeout unavailable'

invoke_adapter() {
  local stage=$1 case_name=$2 expected=$3
  timeout --foreground --kill-after=5s "${case_deadline}s" "$adapter" \
    "$stage" "$case_name" "$expected" "$matrix_root"
}

cleanup_active_case() {
  [[ -n $active_case && $active_clean == false ]] || return 0
  if ! invoke_adapter cleanup "$active_case" "$active_expected"; then
    printf 'phase6 release failure matrix: FAIL: cleanup %s\n' "$active_case" >&2
    return 1
  fi
  active_clean=true
}
trap 'cleanup_active_case' EXIT
trap 'cleanup_active_case; exit 129' HUP
trap 'cleanup_active_case; exit 130' INT
trap 'cleanup_active_case; exit 143' TERM

passed=0
total=0
for entry in "${FAILURE_MATRIX[@]}"; do
  case_name=${entry%%:*}
  expected=${entry#*:}
  [[ -z $selected_case || $case_name == "$selected_case" ]] || continue
  total=$((total + 1))
  [[ $case_name =~ ^[a-z][a-z0-9_]{0,63}$ && $expected =~ ^[a-z][a-z0-9_]{0,63}$ ]] || fail 'invalid matrix definition'
  active_case=$case_name
  active_expected=$expected
  active_clean=false

  invoke_adapter prepare "$case_name" "$expected" || fail "prepare $case_name"
  # The adapter is supplied by the disposable Phase 6 harness. Its inject stage
  # invokes the real local-mode production scripts and real Compose services.
  invoke_adapter inject "$case_name" "$expected" || fail "inject $case_name"
  for stage in "${ASSERTION_STAGES[@]}"; do
    # Every assertion is evidence-backed; diagnostics performs sanitization and
    # rejects credentials, secret values, PII, endpoints, and response bodies.
    invoke_adapter "$stage" "$case_name" "$expected" || fail "$stage $case_name"
  done
  cleanup_active_case || fail "cleanup $case_name"
  passed=$((passed + 1))
  printf 'phase6 release failure matrix: PASS: %s\n' "$case_name"
done

trap - EXIT HUP INT TERM
printf 'phase6 release failure matrix: PASS (%d/%d)\n' "$passed" "$total"
