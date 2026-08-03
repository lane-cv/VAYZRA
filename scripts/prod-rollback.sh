#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
readonly SCRIPT_DIR
readonly COMMON="$SCRIPT_DIR/prod-common.sh"
[[ -f $COMMON && ! -L $COMMON ]] || exit 1
# shellcheck source=prod-common.sh
# shellcheck disable=SC1091
source "$COMMON"

readonly -a ROLLBACK_STATES=(rollback_diagnostics rollback_compatibility rollback_stopped rollback_started rollback_ready rollback_smoke_passed environment_restored rolled_back normal traffic_open)
project_dir='' env_file='' mode='' expected_host=''
while (($#)); do
  case $1 in
    --project-dir|--env-file|--mode|--expected-host-address)
      (($# >= 2)) || { hl_fail invalid_arguments; exit 1; }
      case $1 in
        --project-dir) project_dir=$2 ;;
        --env-file) env_file=$2 ;;
        --mode) mode=$2 ;;
        --expected-host-address) expected_host=$2 ;;
      esac
      shift 2 ;;
    *) hl_fail invalid_arguments; exit 1 ;;
  esac
done
[[ $(id -u) == 0 ]] || { hl_fail root_required; exit 1; }
case $mode in local|server) ;; *) hl_fail invalid_arguments; exit 1 ;; esac
[[ $mode == local && -z $expected_host || $mode == server && -n $expected_host ]] || { hl_fail invalid_arguments; exit 1; }

project_dir=$(hl_canonical_path "$project_dir" directory)
env_file=$(hl_canonical_path "$env_file" file)
hl_secure_file "$env_file"

declare -A env=()
while IFS= read -r line || [[ -n $line ]]; do
  [[ -z $line || $line == \#* ]] && continue
  [[ $line =~ ^([A-Z][A-Z0-9_]*)=([^[:cntrl:]]*)$ ]] || { hl_fail environment_invalid; exit 1; }
  env["${BASH_REMATCH[1]}"]=${BASH_REMATCH[2]}
done <"$env_file"
if [[ $mode == server ]]; then
  for key in "${!env[@]}"; do
    [[ $key != HAPPYLEARN_LOCAL_* && $key != *FAILURE_INJECTION* && $key != *FAILURE_MATRIX* ]] || { hl_fail server_test_variable_rejected; exit 1; }
  done
  while IFS= read -r key; do
    [[ $key != HAPPYLEARN_LOCAL_* && $key != *FAILURE_INJECTION* && $key != *FAILURE_MATRIX* ]] || { hl_fail server_test_variable_rejected; exit 1; }
  done < <(compgen -v)
fi
rollback_failure_injection=${HAPPYLEARN_ROLLBACK_FAILURE_INJECTION:-}
if [[ -n $rollback_failure_injection ]]; then
  [[ $mode == local ]] || { hl_fail server_test_variable_rejected; exit 1; }
  case $rollback_failure_injection in
    previous_image_incompatible|previous_image_readiness_failure) ;;
    *) hl_fail invalid_failure_injection; exit 1 ;;
  esac
fi

state_dir=${env[HAPPYLEARN_RELEASE_STATE_PATH]:-}
state_dir=$(hl_canonical_path "$state_dir" directory)
hl_secure_directory "$state_dir"
inherited_lock_valid=false
if [[ ${HL_RELEASE_LOCK_FD:-} =~ ^[0-9]+$ && -e /proc/$$/fd/${HL_RELEASE_LOCK_FD:-0} ]] &&
  [[ $(readlink -f -- "/proc/$$/fd/${HL_RELEASE_LOCK_FD}") == "$state_dir/release.lock" ]] &&
  flock -n "$HL_RELEASE_LOCK_FD"; then
  inherited_lock_valid=true
fi
if [[ $inherited_lock_valid != true ]]; then
  hl_acquire_release_lock "$state_dir"
fi

state_file="$state_dir/release-state.json"
active_manifest="$state_dir/active-manifest.json"
previous_manifest="$state_dir/previous-manifest.json"
failed_manifest="$state_dir/failed-manifest.json"
release_input="$state_dir/release-input"
candidate_manifest="$release_input/candidate-manifest.json"
rollback_env="$state_dir/rollback.env"
diagnostics="$state_dir/rollback-diagnostics.json"
runbook="$project_dir/docs/runbooks/phase6-release-rollback.md"

hl_secure_file "$state_file"
release_id=$(jq -er '.releaseId | select(type=="string" and length>0)' "$state_file")
trace_id=$(jq -er '.traceId | select(type=="string" and test("^[0-9a-f]{32}$"))' "$state_file")
early_failed_safe() {
  local category=$1 now body
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  body=$(jq -c --arg state failed_safe --arg result failed --arg updated "$now" --arg category "$category" '.state=$state | .result=$result | .updatedAt=$updated | .rollbackFailureCategory=$category' "$state_file")
  hl_atomic_json_write "$state_file" "$body" || true
  printf '{"status":"fail","category":"rollback_failed_safe","failureCategory":"%s","traceId":"%s","runbook":"%s"}\n' "$category" "$trace_id" "$runbook" >&2
  exit 1
}
expected_previous_hash=$(jq -r '.previousManifestSha256 // empty' "$state_file") || early_failed_safe previous_manifest_hash_invalid
[[ -n $expected_previous_hash ]] || early_failed_safe previous_manifest_missing
[[ $expected_previous_hash =~ ^[0-9a-f]{64}$ ]] || early_failed_safe previous_manifest_hash_invalid

selected_previous=''
for possible in "$active_manifest" "$previous_manifest"; do
  [[ -f $possible && ! -L $possible ]] || continue
  [[ $(sha256sum "$possible" | awk '{print $1}') == "$expected_previous_hash" ]] || continue
  selected_previous=$possible
  break
done
[[ -n $selected_previous ]] || {
  if [[ ! -f $active_manifest && ! -f $previous_manifest ]]; then early_failed_safe previous_manifest_missing; else early_failed_safe previous_manifest_hash_invalid; fi
}
hl_secure_file "$selected_previous"

current_state=$(jq -r '.state // empty' "$state_file")
result=$(jq -r '.result // empty' "$state_file")
case $current_state in
  schema_compatible|migrated|services_started|ready|smoke_passed|activated|rollback_diagnostics|rollback_compatibility|rollback_stopped|rollback_started|rollback_ready|rollback_smoke_passed|rolled_back|normal) ;;
  *) hl_fail rollback_outside_allowed_interval; exit 1 ;;
esac
[[ $result != succeeded ]] || { hl_fail rollback_outside_allowed_interval; exit 1; }

persist_state() {
  local next_state=$1 next_result=${2:-rollback_pending} now body
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  body=$(jq -c --arg state "$next_state" --arg result "$next_result" --arg updated "$now" '.state=$state | .result=$result | .updatedAt=$updated' "$state_file")
  hl_atomic_json_write "$state_file" "$body"
  current_state=$next_state
  result=$next_result
}

rollback_index() {
  local wanted=$1 index
  for index in "${!ROLLBACK_STATES[@]}"; do
    [[ ${ROLLBACK_STATES[$index]} != "$wanted" ]] || { printf '%s\n' "$index"; return; }
  done
  return 1
}

completed() {
  local target=$1 current_index target_index
  current_index=$(rollback_index "$current_state" 2>/dev/null) || return 1
  target_index=$(rollback_index "$target")
  (( current_index >= target_index ))
}

on_failure() {
  local status=${1:-1} failure_category body
  trap - ERR HUP INT TERM
  failure_category=${rollback_failure_category:-rollback_execution_failed}
  body=$(jq -c --arg state failed_safe --arg result failed --arg category "$failure_category" '.state=$state | .result=$result | .rollbackFailureCategory=$category' "$state_file")
  hl_atomic_json_write "$state_file" "$body" || true
  printf '{"status":"fail","category":"rollback_failed_safe","failureCategory":"%s","traceId":"%s","runbook":"%s"}\n' "$failure_category" "$trace_id" "$runbook" >&2
  exit "$status"
}
trap 'on_failure $?' ERR
trap 'on_failure 129' HUP
trap 'on_failure 130' INT
trap 'on_failure 143' TERM

release_control_ready() {
  [[ -n $(hl_compose "$project_dir" "$env_file" --profile release ps --status running --quiet release-control) ]] &&
    hl_compose "$project_dir" "$env_file" --profile release logs --no-color release-control 2>/dev/null | grep -Fq release_mode_ready
}
if [[ $current_state != normal || $result != rolled_back ]]; then
  hl_compose "$project_dir" "$env_file" --profile release up --detach --no-deps release-control >/dev/null
  hl_wait_until 60 release_control_ready
fi
hl_compose "$project_dir" "$env_file" exec -T caddy caddy reload --config /etc/caddy/Caddyfile.maintenance --adapter caddyfile >/dev/null

if ! completed rollback_diagnostics; then
  rollback_failure_category=rollback_diagnostics_failed
  app_running=false; worker_running=false; caddy_running=false
  [[ -z $(hl_compose "$project_dir" "$env_file" ps --status running --quiet app) ]] || app_running=true
  [[ -z $(hl_compose "$project_dir" "$env_file" ps --status running --quiet worker) ]] || worker_running=true
  [[ -z $(hl_compose "$project_dir" "$env_file" ps --status running --quiet caddy) ]] || caddy_running=true
  log_tail=$(hl_compose "$project_dir" "$env_file" logs --no-color --tail 40 app worker 2>&1 | head -c 32768 || true)
  log_hash=$(printf '%s' "$log_tail" | sha256sum | awk '{print $1}')
  log_bytes=${#log_tail}
  unset log_tail
  body=$(jq -cn --arg releaseId "$release_id" --arg traceId "$trace_id" --arg capturedAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg logHash "$log_hash" --argjson logBytes "$log_bytes" --argjson app "$app_running" --argjson worker "$worker_running" --argjson caddy "$caddy_running" '{releaseId:$releaseId,traceId:$traceId,capturedAt:$capturedAt,services:{appRunning:$app,workerRunning:$worker,caddyRunning:$caddy},boundedLogTail:{sha256:$logHash,bytes:$logBytes}}')
  hl_atomic_json_write "$diagnostics" "$body"
  persist_state rollback_diagnostics
fi

if ! completed rollback_compatibility; then
  rollback_failure_category=rollback_manifest_state_failed
  [[ ! -f $candidate_manifest ]] || hl_atomic_json_write "$failed_manifest" "$(<"$candidate_manifest")"
  hl_atomic_json_write_owned "$candidate_manifest" "$(<"$selected_previous")" 10001
  rollback_failure_category=rollback_manifest_fields_invalid
  app_image=$(jq -er '.images.app | select(test("^[^[:space:]@]+@sha256:[0-9a-f]{64}$"))' "$selected_previous")
  worker_image=$(jq -er '.images.worker | select(test("^[^[:space:]@]+@sha256:[0-9a-f]{64}$"))' "$selected_previous")
  migrate_image=$(jq -er '.images.migrate | select(test("^[^[:space:]@]+@sha256:[0-9a-f]{64}$"))' "$selected_previous")
  rollback_failure_category=rollback_environment_build_failed
  temporary=$(mktemp --tmpdir="$state_dir" '.rollback-env.XXXXXX')
  chmod 0600 "$temporary"
  while IFS= read -r line || [[ -n $line ]]; do
    case $line in HAPPYLEARN_APP_IMAGE=*|HAPPYLEARN_WORKER_IMAGE=*|HAPPYLEARN_MIGRATE_IMAGE=*) continue ;; esac
    printf '%s\n' "$line" >>"$temporary"
  done <"$env_file"
  printf 'HAPPYLEARN_APP_IMAGE=%s\nHAPPYLEARN_WORKER_IMAGE=%s\nHAPPYLEARN_MIGRATE_IMAGE=%s\n' "$app_image" "$worker_image" "$migrate_image" >>"$temporary"
  sync "$temporary"
  mv -f -- "$temporary" "$rollback_env"
  unset app_image worker_image migrate_image
  rollback_failure_category=rollback_manifest_validation_failed
  hl_compose "$project_dir" "$rollback_env" --profile release run --rm --no-deps --entrypoint /app/happylearn-release-manifest acceptance validate --file /release-input/candidate-manifest.json >/dev/null
  rollback_failure_category=rollback_schema_query_failed
  live_schema=$(hl_compose "$project_dir" "$env_file" exec -T postgres psql --username happylearn --dbname happylearn --no-psqlrc --quiet --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT COALESCE(MAX(version_id),0) FROM goose_db_version WHERE is_applied;" | tr -d '[:space:]')
  [[ $live_schema =~ ^[0-9]+$ ]] || false
  rollback_failure_category=rollback_compatibility_check_failed
  if [[ $rollback_failure_injection == previous_image_incompatible ]]; then
    rollback_failure_category=schema_incompatible
    false
  fi
  if ! hl_compose "$project_dir" "$rollback_env" --profile release run --rm --no-deps --entrypoint /app/happylearn-release-manifest acceptance compatible --file /release-input/candidate-manifest.json --schema-version "$live_schema" >/dev/null; then
    rollback_failure_category=schema_incompatible
    false
  fi
  persist_state rollback_compatibility
fi

if ! completed rollback_stopped; then
  rollback_failure_category=rollback_stop_failed
  hl_compose "$project_dir" "$env_file" stop --timeout 60 worker app >/dev/null
  persist_state rollback_stopped
fi
if ! completed rollback_started; then
  rollback_failure_category=rollback_start_failed
  hl_compose "$project_dir" "$rollback_env" up --detach --no-deps app worker >/dev/null
  persist_state rollback_started
fi

# shellcheck disable=SC2016
app_ready() { hl_compose "$project_dir" "$rollback_env" exec -T app sh -eu -c 'curl --fail --silent -H "Authorization: Bearer $(cat /run/secrets/metrics-bearer)" http://127.0.0.1:9090/internal/readiness >/dev/null'; }
worker_ready() { hl_compose "$project_dir" "$rollback_env" exec -T worker curl --fail --silent http://127.0.0.1:8081/ready >/dev/null; }
if ! completed rollback_ready; then
  rollback_failure_category=rollback_readiness_failed
  [[ $rollback_failure_injection != previous_image_readiness_failure ]] || false
  hl_wait_until 300 app_ready
  hl_wait_until 300 worker_ready
  persist_state rollback_ready
fi
if ! completed rollback_smoke_passed; then
  rollback_failure_category=rollback_smoke_failed
  hl_compose "$project_dir" "$rollback_env" --profile release run --rm --no-deps acceptance >/dev/null
  persist_state rollback_smoke_passed
fi
if ! completed environment_restored; then
  rollback_failure_category=rollback_environment_restore_failed
  env_directory=$(dirname -- "$env_file")
  replacement=$(mktemp --tmpdir="$env_directory" '.production-env.XXXXXX')
  install -o 0 -g 0 -m 0600 "$rollback_env" "$replacement"
  sync "$replacement"
  mv -f -- "$replacement" "$env_file"
  sync "$env_directory"
  persist_state environment_restored
fi
if ! completed rolled_back; then
  rollback_failure_category=rollback_activation_failed
  hl_atomic_json_write "$active_manifest" "$(<"$selected_previous")"
  persist_state rolled_back rolled_back
fi
if ! completed normal; then
  rollback_failure_category=rollback_normal_mode_failed
  hl_compose "$project_dir" "$env_file" --profile release stop --timeout 10 release-control >/dev/null
  normal=$(hl_compose "$project_dir" "$env_file" exec -T postgres psql --username happylearn --dbname happylearn --no-psqlrc --quiet --tuples-only --no-align --command "SELECT mode FROM operational_modes WHERE singleton_id=true;" | tr -d '[:space:]')
  [[ $normal == normal ]]
  persist_state normal rolled_back
fi

edge_status() {
  # Expansions are intentionally evaluated by the shell inside the container.
  # shellcheck disable=SC2016
  hl_compose "$project_dir" "$rollback_env" exec -T app sh -eu -c '
    address=$(getent ahostsv4 caddy | awk "NR==1 {print \$1}")
    test -n "$address"
    curl --insecure --silent --output /dev/null --write-out "%{http_code}" --resolve "$1:443:$address" "https://$1/api/v1/health/live"
  ' _ "${env[HAPPYLEARN_DOMAIN]}"
}
traffic_ready() { [[ $(edge_status) == 200 ]]; }
if ! completed traffic_open; then
  rollback_failure_category=rollback_traffic_open_failed
  hl_compose "$project_dir" "$rollback_env" exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
  hl_wait_until 60 traffic_ready
  persist_state traffic_open rolled_back
fi

trap - ERR HUP INT TERM
printf '{"status":"pass","category":"release_rolled_back","releaseId":"%s","traceId":"%s"}\n' "$release_id" "$trace_id"
