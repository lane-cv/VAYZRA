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

readonly -a RELEASE_STATES=(preflight backup_started backup_verified release_mode maintenance drained images_pulled schema_compatible migrated services_started ready smoke_passed activated normal traffic_open succeeded)
project_dir='' env_file='' manifest='' version='' mode='' expected_host='' confirmed=false
while (($#)); do
  case $1 in
    --project-dir|--env-file|--manifest|--version|--mode|--expected-host-address)
      (($# >= 2)) || { hl_fail invalid_arguments; exit 1; }
      case $1 in
        --project-dir) project_dir=$2 ;;
        --env-file) env_file=$2 ;;
        --manifest) manifest=$2 ;;
        --version) version=$2 ;;
        --mode) mode=$2 ;;
        --expected-host-address) expected_host=$2 ;;
      esac
      shift 2 ;;
    --confirm-maintenance-window) confirmed=true; shift ;;
    *) hl_fail invalid_arguments; exit 1 ;;
  esac
done
[[ $confirmed == true && ( $mode == local || $mode == server ) ]] || { hl_fail invalid_arguments; exit 1; }
[[ $(id -u) == 0 ]] || { hl_fail root_required; exit 1; }
[[ $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-+][0-9A-Za-z.-]+)?$ ]] || { hl_fail invalid_version; exit 1; }
[[ $mode == local && -z $expected_host || $mode == server && -n $expected_host ]] || { hl_fail invalid_arguments; exit 1; }

project_dir=$(hl_canonical_path "$project_dir" directory)
env_file=$(hl_canonical_path "$env_file" file)
manifest=$(hl_canonical_path "$manifest" file)
hl_secure_file "$env_file"
hl_secure_file "$manifest"
[[ $(jq -r '.version // empty' "$manifest") == "$version" ]] || { hl_fail version_mismatch; exit 1; }
if [[ $mode == server ]]; then
  [[ -z $(git -C "$project_dir" status --porcelain --untracked-files=no 2>/dev/null) ]] || { hl_fail dirty_checkout; exit 1; }
fi

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
release_failure_injection=${HAPPYLEARN_RELEASE_FAILURE_INJECTION:-}
if [[ -n $release_failure_injection ]]; then
  [[ $mode == local ]] || { hl_fail server_test_variable_rejected; exit 1; }
  case $release_failure_injection in
    pre_release_backup_failure|drain_timeout|migration_lock_conflict|migration_failure|app_readiness_failure|worker_readiness_failure|object_store_restart_failure|smoke_failure|signal:*) ;;
    *) hl_fail invalid_failure_injection; exit 1 ;;
  esac
fi
state_dir=${env[HAPPYLEARN_RELEASE_STATE_PATH]:-}
state_dir=$(hl_canonical_path "$state_dir" directory)
hl_secure_directory "$state_dir"
hl_acquire_release_lock "$state_dir"

state_file="$state_dir/release-state.json"
active_manifest="$state_dir/active-manifest.json"
previous_manifest="$state_dir/previous-manifest.json"
release_input="$state_dir/release-input"
candidate_manifest="$release_input/candidate-manifest.json"
release_history="$state_dir/release-history"
if [[ -f $state_file ]]; then
  hl_secure_file "$state_file"
  terminal_result=$(jq -r '.result // empty' "$state_file")
  terminal_state=$(jq -r '.state // empty' "$state_file")
  terminal_id=$(jq -r '.releaseId // empty' "$state_file")
  if [[ $terminal_state == succeeded && $terminal_result == succeeded || $terminal_state == normal && $terminal_result == rolled_back ]]; then
    [[ $terminal_id =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] || { hl_fail release_history_invalid; exit 1; }
    if [[ ! -d $release_history ]]; then install -d -o 0 -g 0 -m 0700 "$release_history"; fi
    hl_secure_directory "$release_history"
    [[ ! -e $release_history/$terminal_id.json ]] || { hl_fail release_history_conflict; exit 1; }
    mv -- "$state_file" "$release_history/$terminal_id.json"
    sync "$release_history"
  fi
  unset terminal_result terminal_state terminal_id
fi
manifest_hash=$(sha256sum "$manifest" | awk '{print $1}')
previous_hash=''
[[ ! -f $active_manifest ]] || previous_hash=$(sha256sum "$active_manifest" | awk '{print $1}')
if [[ -f $active_manifest && $previous_hash == "$manifest_hash" ]]; then
  [[ -f $state_file && ! -L $state_file &&
    $(jq -r '.manifestSha256 // empty' "$state_file") == "$manifest_hash" &&
    $(jq -r '.state // empty' "$state_file") =~ ^(activated|normal|traffic_open)$ &&
    $(jq -r '.result // empty' "$state_file") =~ ^(pending|failed)$ ]] || {
    hl_fail already_active
    exit 1
  }
fi

release_id='' trace_id='' current_state='' attempt=1 started_at='' backup_evidence='' result=pending
if [[ -f $state_file ]]; then
  hl_secure_file "$state_file"
  [[ $(jq -r '.manifestSha256 // empty' "$state_file") == "$manifest_hash" ]] || { hl_fail state_manifest_mismatch; exit 1; }
  release_id=$(jq -r '.releaseId' "$state_file")
  trace_id=$(jq -r '.traceId' "$state_file")
  current_state=$(jq -r '.state' "$state_file")
  attempt=$(( $(jq -r '.attempt' "$state_file") + 1 ))
  started_at=$(jq -r '.startedAt' "$state_file")
  backup_evidence=$(jq -r '.backupEvidenceId // empty' "$state_file")
  result=$(jq -r '.result' "$state_file")
  previous_hash=$(jq -r '.previousManifestSha256 // empty' "$state_file")
  [[ -z $previous_hash || $previous_hash =~ ^[0-9a-f]{64}$ ]] || { hl_fail release_history_invalid; exit 1; }
  [[ $current_state != failed_safe ]] || { hl_fail release_not_resumable; exit 1; }
  [[ $result == pending || $result == failed ]] || { hl_fail release_not_resumable; exit 1; }
else
  release_id=$(cat /proc/sys/kernel/random/uuid)
  trace_id=${release_id//-/}
  started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
fi

persist_state() {
  local state=$1 next_result=${2:-pending} now body
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  body=$(jq -cn --arg releaseId "$release_id" --arg manifest "$manifest_hash" --arg previous "$previous_hash" --arg state "$state" --argjson attempt "$attempt" --arg started "$started_at" --arg updated "$now" --arg backup "$backup_evidence" --arg trace "$trace_id" --arg result "$next_result" '{releaseId:$releaseId,manifestSha256:$manifest,previousManifestSha256:$previous,state:$state,attempt:$attempt,startedAt:$started,updatedAt:$updated,backupEvidenceId:$backup,traceId:$trace,result:$result}')
  hl_atomic_json_write "$state_file" "$body"
  current_state=$state
  result=$next_result
}

inject_after_state() {
  local state=$1 trigger=''
  case $release_failure_injection in
    pre_release_backup_failure) trigger=backup_started ;;
    drain_timeout) trigger=maintenance ;;
    app_readiness_failure|worker_readiness_failure|object_store_restart_failure) trigger=services_started ;;
    smoke_failure) trigger=ready ;;
    signal:*)
      [[ ${release_failure_injection#signal:} == "$state" ]] || return 0
      kill -TERM "$$"
      return 1
      ;;
    '') return 0 ;;
  esac
  [[ $state != "$trigger" ]] || return 1
}

checkpoint() {
  persist_state "$@"
  inject_after_state "$1"
}

state_index() {
  local wanted=$1 index
  for index in "${!RELEASE_STATES[@]}"; do [[ ${RELEASE_STATES[$index]} != "$wanted" ]] || { printf '%s\n' "$index"; return; }; done
  return 1
}

maintenance_reached=false
if [[ -n $current_state ]] && (( $(state_index "$current_state" 2>/dev/null || echo -1) >= 4 )); then maintenance_reached=true; fi
rollback_allowed=false
if [[ -n $current_state ]] && (( $(state_index "$current_state" 2>/dev/null || echo -1) >= 8 && $(state_index "$current_state" 2>/dev/null || echo 99) < 14 )); then
  rollback_allowed=true
fi
on_failure() {
  local status=${1:-1}
  trap - ERR HUP INT TERM
  if (( status != 0 )); then
    if [[ $current_state == succeeded && $result == succeeded ]]; then
      :
    elif state_at_least traffic_open; then
      # Traffic reopening is itself a verified durable postcondition. Preserve
      # it as a resumable checkpoint instead of claiming failed-safe while the
      # edge is already open. Resume reproves the edge before marking success.
      persist_state traffic_open failed || true
    elif [[ $rollback_allowed == true ]]; then
      failure_body=$(jq -c --argjson status "$status" --arg state "${current_state:-unknown}" '.result="failed" | .originalFailureStatus=$status | .originalFailureState=$state' "$state_file")
      hl_atomic_json_write "$state_file" "$failure_body" || true
      rollback_args=(--project-dir "$project_dir" --env-file "$env_file" --mode "$mode")
      [[ $mode != server ]] || rollback_args+=(--expected-host-address "$expected_host")
      "$SCRIPT_DIR/prod-rollback.sh" "${rollback_args[@]}" >/dev/null || true
    elif [[ $maintenance_reached == true ]]; then
      persist_state failed_safe failed || true
    else
      persist_state "${current_state:-preflight}" failed || true
    fi
    printf '{"status":"fail","category":"release_failed","traceId":"%s"}\n' "$trace_id" >&2
  fi
  exit "$status"
}
trap 'on_failure $?' ERR
trap 'on_failure 129' HUP
trap 'on_failure 130' INT
trap 'on_failure 143' TERM

completed() {
  local target=$1
  [[ -n $current_state ]] || return 1
  local current_index target_index
  current_index=$(state_index "$current_state")
  target_index=$(state_index "$target")
  if [[ $result == failed && $current_index == "$target_index" ]]; then return 1; fi
  (( current_index >= target_index ))
}

state_at_least() {
  local target=$1
  [[ -n $current_state ]] && (( $(state_index "$current_state") >= $(state_index "$target") ))
}

release_control_ready() {
  [[ -n $(hl_compose "$project_dir" "$env_file" --profile release ps --status running --quiet release-control) ]] &&
    hl_compose "$project_dir" "$env_file" --profile release logs --no-color release-control 2>/dev/null | grep -Fq release_mode_ready
}

if state_at_least release_mode && ! state_at_least normal; then
  hl_compose "$project_dir" "$env_file" --profile release up --detach --no-deps release-control >/dev/null
  hl_wait_until 60 release_control_ready
fi
if state_at_least maintenance && ! state_at_least traffic_open; then
  hl_compose "$project_dir" "$env_file" exec -T caddy caddy reload --config /etc/caddy/Caddyfile.maintenance --adapter caddyfile >/dev/null
  maintenance_reached=true
fi
if state_at_least activated; then
  [[ -f $active_manifest && $(sha256sum "$active_manifest" | awk '{print $1}') == "$manifest_hash" ]]
fi
if state_at_least normal; then
  reproved_mode=$(hl_compose "$project_dir" "$env_file" exec -T postgres psql --username happylearn --dbname happylearn --no-psqlrc --quiet --tuples-only --no-align --command "SELECT mode FROM operational_modes WHERE singleton_id=true;" | tr -d '[:space:]')
  [[ $reproved_mode == normal ]]
fi

preflight_args=(--project-dir "$project_dir" --env-file "$env_file" --manifest "$manifest" --mode "$mode")
[[ $mode != server ]] || preflight_args+=(--expected-host-address "$expected_host")
"$SCRIPT_DIR/prod-preflight.sh" "${preflight_args[@]}" >/dev/null
if ! completed preflight; then
  hl_atomic_json_write_owned "$candidate_manifest" "$(<"$manifest")" 10001
  checkpoint preflight
fi
if completed preflight; then
  hl_atomic_json_write_owned "$candidate_manifest" "$(<"$manifest")" 10001
fi

if ! completed backup_started; then checkpoint backup_started; fi
if ! completed backup_verified; then
  backup_json=$("$SCRIPT_DIR/prod-backup.sh" --project-dir "$project_dir" --env-file "$env_file" --release-id "$release_id")
  backup_evidence=$(jq -r 'select(.status=="pass") | .evidenceId' <<<"$backup_json")
  [[ $backup_evidence =~ ^[0-9a-f-]{36}$ ]] || false
  checkpoint backup_verified
fi
if completed backup_verified; then
  recovery_evidence="$state_dir/recovery/pre-release.json"
  hl_secure_file "$recovery_evidence"
  [[ $(jq -r '.status // empty' "$recovery_evidence") == verified &&
    $(jq -r '.releaseId // empty' "$recovery_evidence") == "$release_id" &&
    $(jq -r '.evidenceId // empty' "$recovery_evidence") == "$backup_evidence" ]] || false
fi

if ! completed release_mode; then
  hl_compose "$project_dir" "$env_file" --profile release up --detach --no-deps release-control >/dev/null
  hl_wait_until 60 release_control_ready
  checkpoint release_mode
fi

edge_status() {
  # Expansions are intentionally evaluated by the shell inside the container.
  # shellcheck disable=SC2016
  hl_compose "$project_dir" "$env_file" exec -T app sh -eu -c '
    address=$(getent ahostsv4 caddy | awk "NR==1 {print \$1}")
    test -n "$address"
    curl --insecure --silent --output /dev/null --write-out "%{http_code}" --resolve "$1:443:$address" "https://$1/api/v1/health/live"
  ' _ "${env[HAPPYLEARN_DOMAIN]}"
}
traffic_ready() { [[ $(edge_status) == 200 ]]; }
if state_at_least maintenance && ! state_at_least traffic_open; then
  [[ $(edge_status) == 503 ]]
fi

if ! completed maintenance; then
  hl_compose "$project_dir" "$env_file" exec -T caddy caddy reload --config /etc/caddy/Caddyfile.maintenance --adapter caddyfile >/dev/null
  [[ $(edge_status) == 503 ]]
  maintenance_reached=true
  checkpoint maintenance
fi

active_count() {
  hl_compose "$project_dir" "$env_file" exec -T postgres psql --username happylearn --dbname happylearn --no-psqlrc --quiet --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT (SELECT count(*) FROM ai_runs WHERE status='streaming')+(SELECT count(*) FROM file_processing_jobs WHERE state='running')+(SELECT count(*) FROM outbox_events WHERE lease_owner IS NOT NULL AND lease_until>clock_timestamp())+(SELECT count(*) FROM file_processing_artifacts WHERE cleanup_lease_owner IS NOT NULL AND cleanup_lease_until>clock_timestamp());" | tr -d '[:space:]' | grep -Fxq 0
}
if state_at_least drained && ! state_at_least services_started; then
  active_count
  [[ -z $(hl_compose "$project_dir" "$env_file" ps --status running --quiet app worker) ]]
fi
if ! completed drained; then
  hl_wait_until 600 active_count
  hl_compose "$project_dir" "$env_file" stop --timeout 60 worker app >/dev/null
  checkpoint drained
fi

pull_release_images() {
  local pull_attempt
  for pull_attempt in 1 2 3; do
    if hl_compose "$project_dir" "$env_file" --profile '*' pull \
      app worker migrate backup caddy postgres redis minio >/dev/null; then
      return 0
    fi
    ((pull_attempt < 3)) || return 1
    sleep "$((pull_attempt * 2))"
  done
  return 1
}

if ! completed images_pulled; then
  pull_release_images
  checkpoint images_pulled
fi

current_schema() {
  local output
  output=$(hl_compose "$project_dir" "$env_file" --profile release run --rm --no-deps migrate current-schema) || return 1
  jq -er 'select(.status=="pass") | .schemaVersion | select(type=="number" and .>=0 and .==floor)' <<<"$output"
}
if ! completed schema_compatible; then
  target_schema=$(current_schema)
  minimum=$(jq -r '.minSchemaVersion' "$manifest"); maximum=$(jq -r '.maxSchemaVersion' "$manifest")
  [[ $target_schema =~ ^[0-9]+$ ]] && (( target_schema >= minimum && target_schema <= maximum ))
  checkpoint schema_compatible
else
  target_schema=$(current_schema)
  minimum=$(jq -r '.minSchemaVersion' "$manifest"); maximum=$(jq -r '.maxSchemaVersion' "$manifest")
  [[ $target_schema =~ ^[0-9]+$ ]] && (( target_schema >= minimum && target_schema <= maximum ))
fi

if ! completed migrated; then
  rollback_allowed=true
  case $release_failure_injection in
    migration_lock_conflict|migration_failure) false ;;
  esac
  hl_compose "$project_dir" "$env_file" --profile release run --rm --no-deps migrate >/dev/null
  checkpoint migrated
fi

if ! completed services_started; then
  hl_compose "$project_dir" "$env_file" up --detach --no-deps app worker >/dev/null
  checkpoint services_started
elif ! state_at_least traffic_open; then
  hl_compose "$project_dir" "$env_file" up --detach --no-deps app worker >/dev/null
fi

# shellcheck disable=SC2016
app_ready() { hl_compose "$project_dir" "$env_file" exec -T app sh -eu -c 'curl --fail --silent -H "Authorization: Bearer $(cat /run/secrets/metrics-bearer)" http://127.0.0.1:9090/internal/readiness >/dev/null'; }
worker_ready() { hl_compose "$project_dir" "$env_file" exec -T worker curl --fail --silent http://127.0.0.1:8081/ready >/dev/null; }
if state_at_least ready; then hl_wait_until 300 app_ready; hl_wait_until 300 worker_ready; fi
if ! completed ready; then hl_wait_until 300 app_ready; hl_wait_until 300 worker_ready; checkpoint ready; fi

if state_at_least smoke_passed; then
  hl_compose "$project_dir" "$env_file" --profile release run --rm --no-deps acceptance >/dev/null
fi
if ! completed smoke_passed; then
  hl_compose "$project_dir" "$env_file" --profile release run --rm --no-deps acceptance >/dev/null
  checkpoint smoke_passed
fi

if ! completed activated; then
  [[ ! -f $active_manifest ]] || hl_atomic_json_write "$previous_manifest" "$(<"$active_manifest")"
  hl_atomic_json_write "$active_manifest" "$(<"$manifest")"
  checkpoint activated
fi

if ! completed normal; then
  hl_compose "$project_dir" "$env_file" --profile release stop --timeout 10 release-control >/dev/null
  normal=$(hl_compose "$project_dir" "$env_file" exec -T postgres psql --username happylearn --dbname happylearn --no-psqlrc --quiet --tuples-only --no-align --command "SELECT mode FROM operational_modes WHERE singleton_id=true;" | tr -d '[:space:]')
  [[ $normal == normal ]]
  checkpoint normal
fi

if state_at_least traffic_open; then hl_wait_until 60 traffic_ready; fi
if ! completed traffic_open; then
  rollback_allowed=false
  hl_compose "$project_dir" "$env_file" exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
  hl_wait_until 60 traffic_ready
  checkpoint traffic_open
fi
if ! completed succeeded; then checkpoint succeeded succeeded; fi
trap - ERR HUP INT TERM
printf '{"status":"pass","category":"release_succeeded","releaseId":"%s","traceId":"%s"}\n' "$release_id" "$trace_id"
