#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd -P)"
target="${E2E_PHASE5_FAILURE_MATRIX_CONTRACT_TARGET:-$repo_root/scripts/e2e-phase5_failure_matrix.sh}"
harness_lib="$repo_root/scripts/e2e-harness-lib.sh"
sanitizer="$repo_root/scripts/sanitize-e2e-artifacts.sh"
publisher="$repo_root/scripts/publish-e2e-diagnostics.sh"

readonly contract_mode_variable='HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT'
readonly adapter_variable='HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER'
readonly contract_root_variable='HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT_ROOT'
readonly case_deadline_variable='HAPPYLEARN_PHASE5_FAILURE_MATRIX_CASE_DEADLINE_SECONDS'
readonly contract_run_argument='--contract-run'
readonly hang_case='webhook_timeout'
readonly lifecycle_stages='fixture shim inject terminal maintenance alert plaintext sanitize cleanup'
readonly outer_deadline_seconds=12
readonly live_outer_deadline_seconds=60

expected_entries=(
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

fail() {
  printf 'phase 5 failure matrix contract: %s\n' "$1" >&2
  exit 1
}

require_literal() {
  grep -Fq -- "$2" "$1" ||
    fail "$(basename "$1") missing literal: $2"
}

require_pattern() {
  grep -Eq -- "$2" "$1" ||
    fail "$(basename "$1") missing pattern: $2"
}

forbid_pattern() {
  if grep -Eiq -- "$2" "$1"; then
    fail "$(basename "$1") contains forbidden pattern: $2"
  fi
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

contract_milliseconds() {
  perl -MTime::HiRes=clock_gettime,CLOCK_MONOTONIC \
    -e 'printf "%.0f\n", 1000 * clock_gettime(CLOCK_MONOTONIC)'
}

[[ -f "$target" ]] ||
  fail 'scripts/e2e-phase5_failure_matrix.sh is absent'
[[ -x "$target" ]] ||
  fail 'scripts/e2e-phase5_failure_matrix.sh is not executable'
[[ -f "$harness_lib" ]] || fail 'shared harness library is absent'
[[ -x "$sanitizer" ]] || fail 'artifact sanitizer is absent'
[[ -x "$publisher" ]] || fail 'diagnostic publisher is absent'
bash -n "$target"

require_literal "$target" 'set -Eeuo pipefail'
require_literal "$target" 'source "$script_dir/e2e-harness-lib.sh"'
require_literal "$target" 'readonly FAILURE_MATRIX=('
require_literal "$target" 'run_case() {'
require_literal "$target" 'run_case "$case_name" "$expected_state"'
require_literal "$target" "$contract_run_argument"
require_literal "$target" "$contract_mode_variable"
require_literal "$target" "$adapter_variable"
require_literal "$target" "$contract_root_variable"
require_literal "$target" "$case_deadline_variable"

for entry in "${expected_entries[@]}"; do
  require_literal "$target" "$entry"
done

require_literal "$target" 'case_project_name'
require_literal "$target" 'happylearn-phase5-live-'
require_literal "$target" 'case_project="$(case_project_name "$case_name" "$run_nonce")"'
require_literal "$target" 'case_deadline="$(bounded_seconds "$CASE_DEADLINE_SECONDS")"'
require_literal "$target" 'run_bounded "$case_deadline" execute_case_injection'
require_pattern "$target" 'CASE_DEADLINE_SECONDS=[1-9][0-9]*'

require_literal "$target" 'prepare_case_fixture "$case_name" "$case_project"'
require_literal "$target" 'fixture_endpoint_for_case "$case_name"'
require_literal "$target" 'install_case_command_shim "$case_name" "$case_shim_dir"'
require_literal "$target" 'assert_terminal_state "$case_name" "$expected_state"'
require_literal "$target" 'assert_maintenance_mode_normal "$case_name"'
require_literal "$target" 'assert_alert_state "$case_name"'
require_literal "$target" 'assert_no_plaintext_dump "$case_name" "$case_artifact_dir"'
require_literal "$target" 'assert_sanitized_artifacts "$case_name" "$case_artifact_dir"'
require_literal "$target" 'assert_case_cleanup "$case_project"'
require_literal "$target" 'cleanup_active_case'
require_literal "$target" 'trap cleanup_active_case EXIT'
require_literal "$target" 'trap '\''handle_case_signal 129'\'' HUP'
require_literal "$target" 'trap '\''handle_case_signal 130'\'' INT'
require_literal "$target" 'trap '\''handle_case_signal 143'\'' TERM'
require_literal "$target" 'sanitize-e2e-artifacts.sh'
require_literal "$target" 'publish-e2e-diagnostics.sh'

require_literal "$target" \
  'write_case_summary "$case_name" "$expected_state" "$actual_state" "$duration" "$trace_id"'
require_literal "$target" \
  'printf '\''{"case":"%s","expected":"%s","actual":"%s","duration":%s,"trace":"%s"}\n'\'''
require_pattern "$target" \
  '\[\[ "\$duration" =~ \^\[1-9\]\[0-9\]\*\$ \]\]'
require_pattern "$target" \
  '\[\[ "\$trace_id" =~ \^\[0-9a-f\]\{8\}-\[0-9a-f\]\{4\}-4\[0-9a-f\]\{3\}-\[89ab\]\[0-9a-f\]\{3\}-\[0-9a-f\]\{12\}\$ \]\]'

forbid_pattern "$target" \
  'HAPPYLEARN_(BACKUP|RESTORE|ALERT|HOST)_(FAIL|FAILURE|FAULT|INJECT|INJECTION|SHIM|CASE)='
forbid_pattern "$target" \
  '(^|[;&|[:space:]])(env|printenv|set[[:space:]]+-[^[:space:]]*x)([;&|[:space:]]|$)'
forbid_pattern "$target" \
  'docker[[:space:]]+(system|container|image|network|volume)[[:space:]]+prune'
forbid_pattern "$target" '(/var/run/docker\.sock|docker\.sock)'
forbid_pattern "$target" '--emit-contract-summary'

# The contract runner uses the same deadline primitive as the future target,
# but in a separate process. This outer deadline catches a broken case deadline
# without providing target-side lifecycle evidence.
source "$harness_lib"

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/phase5-failure-contract.XXXXXX")"
ACTIVE_SIGNAL_TARGET_PID=''
cleanup_contract() {
  if [[ "$ACTIVE_SIGNAL_TARGET_PID" =~ ^[1-9][0-9]*$ ]] &&
    kill -0 "$ACTIVE_SIGNAL_TARGET_PID" 2>/dev/null; then
    kill -TERM "$ACTIVE_SIGNAL_TARGET_PID" 2>/dev/null || true
    sleep 0.1
    kill -KILL "$ACTIVE_SIGNAL_TARGET_PID" 2>/dev/null || true
    wait "$ACTIVE_SIGNAL_TARGET_PID" 2>/dev/null || true
  fi
  chmod -R u+rwX "$tmpdir" 2>/dev/null || true
  rm -rf "$tmpdir"
}
trap cleanup_contract EXIT
chmod 0700 "$tmpdir"
mkdir -m 0700 "$tmpdir/bin" "$tmpdir/runs" "$tmpdir/mutants"

unexpected_external_log="$tmpdir/unexpected-external.log"
for external_name in \
  curl restic psql pg_dump mc aws \
  sanitize-e2e-artifacts.sh publish-e2e-diagnostics.sh
do
  external_path="$tmpdir/bin/$external_name"
  {
    printf '%s\n' '#!/usr/bin/env bash'
    printf '%s\n' 'set -Eeuo pipefail'
    printf 'printf '\''%%s|%%s\\n'\'' '\''%s'\'' "$*" >>"${HAPPYLEARN_PHASE5_FAILURE_MATRIX_UNEXPECTED_EXTERNAL_LOG:?}"\n' \
      "$external_name"
    printf '%s\n' 'exit 99'
  } >"$external_path"
  chmod 0700 "$external_path"
done

fake_docker="$tmpdir/bin/docker"
cat >"$fake_docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

root="${HAPPYLEARN_PHASE5_FAILURE_MATRIX_FAKE_DOCKER_ROOT:?}"
call_log="$root/calls"
containers="$root/containers"
networks="$root/networks"
mkdir -p "$containers" "$networks"
printf '%s' "$1" >>"$call_log"
printf '|%q' "${@:2}" >>"$call_log"
printf '\n' >>"$call_log"

record_value() {
  local record="$1"
  local key="$2"
  sed -n "s/^${key}=//p" "$record"
}

write_value() {
  local record="$1"
  local key="$2"
  local value="$3"
  local pending="${record}.pending"
  awk -v key="$key" -v value="$value" '
    BEGIN { found = 0 }
    index($0, key "=") == 1 {
      print key "=" value
      found = 1
      next
    }
    { print }
    END {
      if (!found) print key "=" value
    }
  ' "$record" >"$pending"
  mv "$pending" "$record"
}

case "${1:-}" in
  version)
    printf '29.6.1\n'
    ;;
  image)
    [[ "${2:-}" == inspect ]] || exit 64
    printf 'sha256:phase5failurematrixfixture\n'
    ;;
  network)
    case "${2:-}" in
      create)
        shift 2
        owner=''
        project=''
        name=''
        while [[ "$#" -gt 0 ]]; do
          case "$1" in
            --internal) shift ;;
            --label)
              value="${2:?}"
              case "$value" in
                io.happylearn.phase5.failure-matrix-owner=*)
                  owner="${value#*=}"
                  ;;
                io.happylearn.phase5.failure-matrix-project=*)
                  project="${value#*=}"
                  ;;
              esac
              shift 2
              ;;
            --*) exit 64 ;;
            *)
              [[ -z "$name" ]] || exit 64
              name="$1"
              shift
              ;;
          esac
        done
        [[ -n "$owner" && -n "$project" && -n "$name" ]] || exit 64
        id="network-$name"
        record="$networks/$id"
        [[ ! -e "$record" ]] || exit 65
        printf 'owner=%s\nproject=%s\nname=%s\n' \
          "$owner" "$project" "$name" >"$record"
        printf '%s\n' "$id"
        ;;
      inspect)
        shift 2
        [[ "${1:-}" == --format ]] || exit 64
        shift 2
        id="${1:?}"
        record="$networks/$id"
        [[ -f "$record" ]] || exit 1
        printf '%s|%s|%s\n' \
          "$(record_value "$record" owner)" \
          "$(record_value "$record" project)" \
          "$(record_value "$record" name)"
        ;;
      rm)
        id="${3:?}"
        record="$networks/$id"
        [[ -f "$record" ]] || exit 1
        rm -f "$record"
        printf '%s\n' "$id"
        ;;
      ls)
        find "$networks" -mindepth 1 -maxdepth 1 -type f \
          -exec basename {} \; | sort
        ;;
      *) exit 64 ;;
    esac
    ;;
  create)
    shift
    owner=''
    project=''
    case_name=''
    name=''
    evidence=''
    image_seen=false
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --name)
          name="${2:?}"
          shift 2
          ;;
        --label)
          value="${2:?}"
          case "$value" in
            io.happylearn.phase5.failure-matrix-owner=*)
              owner="${value#*=}"
              ;;
            io.happylearn.phase5.failure-matrix-project=*)
              project="${value#*=}"
              ;;
            io.happylearn.phase5.failure-matrix-case=*)
              case_name="${value#*=}"
              ;;
          esac
          shift 2
          ;;
        --network|--security-opt|--memory|--cpus|--pids-limit|--user|--tmpfs)
          shift 2
          ;;
        --mount)
          value="${2:?}"
          if [[ "$value" == type=bind,source=*,target=/evidence ]]; then
            evidence="${value#type=bind,source=}"
            evidence="${evidence%,target=/evidence}"
          fi
          shift 2
          ;;
        --read-only)
          shift
          ;;
        --cap-drop)
          [[ "${2:-}" == ALL ]] || exit 64
          shift 2
          ;;
        alpine@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1)
          image_seen=true
          shift
          break
          ;;
        *) exit 64 ;;
      esac
    done
    [[ "$image_seen" == true &&
      -n "$owner" &&
      -n "$project" &&
      -n "$case_name" &&
      -n "$name" &&
      -n "$evidence" &&
      "${*: -1}" == "$case_name" ]] ||
      exit 64
    id="container-$name"
    record="$containers/$id"
    [[ ! -e "$record" ]] || exit 65
    printf \
      'owner=%s\nproject=%s\ncase=%s\nname=%s\nevidence=%s\nstatus=created\nexit_code=0\n' \
      "$owner" "$project" "$case_name" "$name" "$evidence" >"$record"
    printf '%s\n' "$id"
    ;;
  start)
    shift
    [[ "${1:-}" == -a ]] || exit 64
    id="${2:?}"
    record="$containers/$id"
    [[ -f "$record" ]] || exit 1
    case_name="$(record_value "$record" case)"
    write_value "$record" status running
    if [[ "$case_name" == \
      "${HAPPYLEARN_PHASE5_FAILURE_MATRIX_FAKE_DOCKER_HANG_CASE:-}" ]]; then
      : >"$root/hang.started"
      printf '%s\n' "$$" >"$root/hang.pid"
      exec sleep 30
    fi
    case "$case_name" in
      remote_outage)
        actual=degraded
        alert=active
        ;;
      webhook_private_target | host_sample_replay)
        actual=rejected
        alert=suppressed
        ;;
      *)
        actual=failed
        alert=active
        ;;
    esac
    maintenance=normal
    plaintext=absent
    case "${HAPPYLEARN_PHASE5_FAILURE_MATRIX_FAKE_DOCKER_MUTATION:-}" in
      terminal) actual=succeeded ;;
      maintenance) maintenance=backup ;;
      alert) alert=unknown ;;
      plaintext) plaintext=present ;;
      '') ;;
      *) exit 64 ;;
    esac
    report="$(record_value "$record" evidence)/report"
    printf \
      'evidence_version=1\ncase=%s\nactual=%s\nmaintenance=%s\nalert=%s\nplaintext_dump=%s\n' \
      "$case_name" "$actual" "$maintenance" "$alert" "$plaintext" >"$report"
    write_value "$record" status exited
    ;;
  inspect)
    shift
    [[ "${1:-}" == --type && "${2:-}" == container &&
      "${3:-}" == --format ]] ||
      exit 64
    shift 4
    id="${1:?}"
    record="$containers/$id"
    [[ -f "$record" ]] || exit 1
    printf '%s|%s|%s|%s|%s|%s\n' \
      "$(record_value "$record" owner)" \
      "$(record_value "$record" project)" \
      "$(record_value "$record" case)" \
      "$(record_value "$record" status)" \
      "$(record_value "$record" exit_code)" \
      "/$(record_value "$record" name)"
    ;;
  cp)
    source="${2:?}"
    destination="${3:?}"
    id="${source%%:*}"
    [[ "${source#*:}" == /evidence/report ]] || exit 64
    record="$containers/$id"
    [[ -f "$record" && -f "${record}.report" ]] || exit 1
    cp "${record}.report" "$destination"
    ;;
  rm)
    shift
    [[ "${1:-}" == -f ]] || exit 64
    id="${2:?}"
    record="$containers/$id"
    [[ -f "$record" ]] || exit 1
    rm -f "$record" "${record}.report"
    printf '%s\n' "$id"
    ;;
  ps)
    find "$containers" -mindepth 1 -maxdepth 1 -type f \
      ! -name '*.report' -exec basename {} \; | sort
    ;;
  volume)
    [[ "${2:-}" == ls ]] || exit 64
    ;;
  *)
    exit 64
    ;;
esac
FAKE_DOCKER
chmod 0700 "$fake_docker"

adapter="$tmpdir/bin/phase5-failure-matrix-adapter"
cat >"$adapter" <<'FAKE_ADAPTER'
#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

root="${HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT_ROOT:?}"
call_log="${HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_CALL_LOG:?}"
omit_stage="${HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_OMIT_STAGE:-}"
hang_case="${HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_HANG_CASE:-}"
hang_seconds="${HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_HANG_SECONDS:-30}"

if stat -f '%Lp' "$call_log" >/dev/null 2>&1; then
  call_log_mode="$(stat -f '%Lp' "$call_log")"
  call_log_owner="$(stat -f '%u' "$call_log")"
else
  call_log_mode="$(stat -c '%a' "$call_log")"
  call_log_owner="$(stat -c '%u' "$call_log")"
fi
[[ "$call_log" == /* &&
  -f "$call_log" &&
  ! -L "$call_log" &&
  "$call_log_mode" == 600 &&
  "$call_log_owner" == "$(id -u)" ]] ||
  exit 64
printf 'called\n' >>"$call_log"

if stat -f '%Lp' "$root" >/dev/null 2>&1; then
  root_mode="$(stat -f '%Lp' "$root")"
  root_owner="$(stat -f '%u' "$root")"
else
  root_mode="$(stat -c '%a' "$root")"
  root_owner="$(stat -c '%u' "$root")"
fi
[[ "$root" == /* &&
  -d "$root" &&
  ! -L "$root" &&
  "$root_mode" == 700 &&
  "$root_owner" == "$(id -u)" ]] ||
  exit 64
[[ "$#" -ge 4 && "$#" -le 5 ]] || exit 64
stage="$1"
case_name="$2"
expected="$3"
project="$4"
detail="${5:-ok}"

[[ "$case_name" =~ ^[a-z][a-z0-9_]{0,63}$ ]] || exit 64
[[ "$expected" =~ ^(failed|degraded|rejected)$ ]] || exit 64
[[ "$project" =~ ^happylearn-phase5-live-[a-z0-9][a-z0-9-]{0,95}$ ]] ||
  exit 64
[[ "$detail" =~ ^(ok|failed|degraded|rejected|timeout)$ ]] || exit 64
case "$stage" in
  fixture | shim | inject | terminal | maintenance | alert | plaintext | \
    sanitize | cleanup) ;;
  *) exit 64 ;;
esac

inventory="$root/inventory/$project"
case "$stage" in
  fixture)
    [[ ! -e "$inventory" && ! -L "$inventory" ]] || exit 65
    printf '1\n' >"$inventory"
    chmod 0600 "$inventory"
    ;;
  cleanup)
    [[ -f "$inventory" && ! -L "$inventory" ]] || exit 65
    printf '0\n' >"$inventory"
    chmod 0600 "$inventory"
    ;;
  *)
    [[ -f "$inventory" && ! -L "$inventory" ]] || exit 65
    ;;
esac

if [[ "$stage" != "$omit_stage" ]]; then
  printf '%s|%s|%s|%s|%s\n' \
    "$case_name" "$expected" "$project" "$stage" "$detail" \
    >>"$root/events"
fi

if [[ "$stage" == inject && "$case_name" == "$hang_case" ]]; then
  : >"$root/hang.started"
  printf '%s\n' "$$" >"$root/hang.pid"
  exec sleep "$hang_seconds"
fi
FAKE_ADAPTER
chmod 0700 "$adapter"

[[ "$adapter" == /* &&
  -f "$adapter" &&
  ! -L "$adapter" &&
  "$(portable_mode "$adapter")" == 700 &&
  "$(portable_owner "$adapter")" == "$(id -u)" ]] ||
  fail 'contract adapter is not absolute, owner-only, regular, and owned'

new_run_root() {
  local name="$1"
  local root="$tmpdir/runs/$name"
  [[ "$name" =~ ^[a-z0-9-]+$ && ! -e "$root" && ! -L "$root" ]] ||
    return 1
  mkdir -m 0700 "$root" "$root/inventory"
  : >"$root/events"
  : >"$root/adapter.calls"
  : >"$root/stdout"
  : >"$root/stderr"
  chmod 0600 \
    "$root/events" "$root/adapter.calls" \
    "$root/stdout" "$root/stderr"
  printf '%s' "$root"
}

run_candidate() {
  local candidate="$1"
  local root="$2"
  local adapter_path="$3"
  local omit_stage="${4:-}"
  local working_directory="${5:-$repo_root}"
  local adapter_contract_root="${6:-$root}"
  local status=0
  (
    cd "$working_directory"
    PATH="$tmpdir/bin:/usr/bin:/bin:/opt/homebrew/bin" \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT=true \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER="$adapter_path" \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_CALL_LOG="$root/adapter.calls" \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT_ROOT="$adapter_contract_root" \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_CASE_DEADLINE_SECONDS=1 \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_HANG_CASE="$hang_case" \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_HANG_SECONDS=30 \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_OMIT_STAGE="$omit_stage" \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_UNEXPECTED_EXTERNAL_LOG="$unexpected_external_log" \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_REAL_TARGET="$target" \
      HAPPYLEARN_PHASE5_FAILURE_MATRIX_CANNED_SUMMARY="$tmpdir/canned-summary.jsonl" \
      run_bounded "$outer_deadline_seconds" \
      "$candidate" "$contract_run_argument" \
      >"$root/stdout" 2>"$root/stderr" ||
      status=$?
    printf '%s\n' "$status" >"$root/status"
  )
}

validate_summary() {
  local summary="$1"
  local traces="$2"
  local count index=0 line entry expected_case expected_state
  local actual_case actual_expected actual_state duration trace
  count="$(wc -l <"$summary" | tr -d '[:space:]')"
  [[ "$count" == 15 ]] ||
    fail "summary JSONL line count was $count instead of 15"
  : >"$traces"
  chmod 0600 "$traces"
  while IFS= read -r line; do
    entry="${expected_entries[$index]}"
    expected_case="${entry%%:*}"
    expected_state="${entry#*:}"
    printf '%s\n' "$line" |
      /usr/bin/jq -e '
        type == "object" and
        (keys_unsorted == ["case","expected","actual","duration","trace"]) and
        (.case | type == "string") and
        (.expected | type == "string") and
        (.actual | type == "string") and
        (.duration | type == "number" and . > 0 and floor == .) and
        (.trace | type == "string")
      ' >/dev/null ||
      fail "summary line $((index + 1)) was not the exact five-key schema"
    actual_case="$(printf '%s\n' "$line" | /usr/bin/jq -r '.case')"
    actual_expected="$(printf '%s\n' "$line" | /usr/bin/jq -r '.expected')"
    actual_state="$(printf '%s\n' "$line" | /usr/bin/jq -r '.actual')"
    duration="$(printf '%s\n' "$line" | /usr/bin/jq -r '.duration')"
    trace="$(printf '%s\n' "$line" | /usr/bin/jq -r '.trace')"
    [[ "$actual_case" == "$expected_case" &&
      "$actual_expected" == "$expected_state" &&
      "$actual_state" == "$expected_state" &&
      "$duration" =~ ^[1-9][0-9]*$ &&
      "$trace" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] ||
      fail "summary line $((index + 1)) did not match ordered case $expected_case"
    printf '%s\n' "$trace" >>"$traces"
    index=$((index + 1))
  done <"$summary"
  [[ "$index" == 15 &&
    "$(sort "$traces" | uniq | wc -l | tr -d '[:space:]')" == 15 ]] ||
    fail 'summary trace UUIDv4 values were not unique'
}

validate_lifecycle() {
  local root="$1"
  local projects="$2"
  local expected_count=0 count entry expected_case expected_state
  local expected_stage expected_detail line actual_case actual_expected
  local actual_project actual_stage actual_detail extra project=''
  local index=0
  for expected_stage in $lifecycle_stages; do
    expected_count=$((expected_count + 1))
  done
  expected_count=$((expected_count * ${#expected_entries[@]}))
  count="$(wc -l <"$root/events" | tr -d '[:space:]')"
  [[ "$count" == "$expected_count" ]] ||
    fail "lifecycle event count was $count instead of $expected_count"
  : >"$projects"
  chmod 0600 "$projects"
  exec 7<"$root/events"
  for entry in "${expected_entries[@]}"; do
    expected_case="${entry%%:*}"
    expected_state="${entry#*:}"
    project=''
    for expected_stage in $lifecycle_stages; do
      IFS= read -r line <&7 ||
        fail "lifecycle ended before $expected_case/$expected_stage"
      IFS='|' read -r actual_case actual_expected actual_project \
        actual_stage actual_detail extra <<<"$line"
      [[ -z "$extra" &&
        "$actual_case" == "$expected_case" &&
        "$actual_expected" == "$expected_state" &&
        "$actual_stage" == "$expected_stage" ]] ||
        fail "lifecycle mismatch at $expected_case/$expected_stage"
      if [[ -z "$project" ]]; then
        project="$actual_project"
        [[ "$project" =~ ^happylearn-phase5-live-[a-z0-9][a-z0-9-]{0,95}$ ]] ||
          fail "case $expected_case used an invalid project"
        printf '%s\n' "$project" >>"$projects"
      else
        [[ "$actual_project" == "$project" ]] ||
          fail "case $expected_case changed project during its lifecycle"
      fi
      expected_detail=ok
      if [[ "$expected_stage" == terminal ]]; then
        if [[ "$expected_case" == "$hang_case" ]]; then
          expected_detail=timeout
        else
          expected_detail="$expected_state"
        fi
      fi
      [[ "$actual_detail" == "$expected_detail" ]] ||
        fail "case $expected_case stage $expected_stage had detail $actual_detail"
    done
    [[ -f "$root/inventory/$project" &&
      ! -L "$root/inventory/$project" &&
      "$(portable_mode "$root/inventory/$project")" == 600 &&
      "$(<"$root/inventory/$project")" == 0 ]] ||
      fail "case $expected_case cleanup inventory was not zero"
    index=$((index + 1))
  done
  exec 7<&-
  [[ "$index" == 15 &&
    "$(sort "$projects" | uniq | wc -l | tr -d '[:space:]')" == 15 ]] ||
    fail 'case project names were not unique'
  [[ "$(wc -l <"$root/adapter.calls" | tr -d '[:space:]')" == 135 ]] ||
    fail 'target did not route every lifecycle action through the adapter'
}

validate_candidate_output() {
  local root="$1"
  local traces="$root/traces"
  local projects="$root/projects"
  [[ "$(<"$root/status")" == 0 ]] ||
    fail "contract target exited with status $(<"$root/status")"
  validate_lifecycle "$root" "$projects"
  validate_summary "$root/stdout" "$traces"
  [[ ! -s "$root/stderr" ]] ||
    fail 'contract target wrote unexpected stderr'
  [[ ! -e "$unexpected_external_log" ]] ||
    fail 'contract target bypassed the fake adapter'
}

expect_contract_mode_rejection() {
  local root candidate_status=0
  root="$(new_run_root contract-mode-gate)"
  PATH="$tmpdir/bin:/usr/bin:/bin:/opt/homebrew/bin" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER="$adapter" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_CALL_LOG="$root/adapter.calls" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT_ROOT="$root" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_UNEXPECTED_EXTERNAL_LOG="$unexpected_external_log" \
    run_bounded 3 "$target" "$contract_run_argument" \
    >"$root/stdout" 2>"$root/stderr" ||
    candidate_status=$?
  [[ "$candidate_status" -ne 0 &&
    ! -s "$root/events" &&
    ! -s "$root/adapter.calls" ]] ||
    fail '--contract-run was accepted outside contract mode'
  [[ ! -e "$unexpected_external_log" ]] ||
    fail '--contract-run outside contract mode reached an external command'
}

expect_adapter_path_rejection() {
  local name="$1"
  local adapter_path="$2"
  local working_directory="${3:-$repo_root}"
  local root
  root="$(new_run_root "adapter-$name")"
  run_candidate "$target" "$root" "$adapter_path" '' "$working_directory"
  [[ "$(<"$root/status")" != 0 &&
    ! -s "$root/events" &&
    ! -s "$root/adapter.calls" &&
    ! -e "$unexpected_external_log" ]] ||
    fail "invalid $name adapter path was accepted"
}

expect_contract_root_rejection() {
  local name="$1"
  local adapter_contract_root="$2"
  local inspected_root="$3"
  local working_directory="${4:-$repo_root}"
  local root
  root="$(new_run_root "root-$name")"
  run_candidate \
    "$target" "$root" "$adapter" '' \
    "$working_directory" "$adapter_contract_root"
  [[ "$(<"$root/status")" != 0 &&
    ! -s "$root/events" &&
    ! -s "$root/adapter.calls" &&
    ! -e "$unexpected_external_log" &&
    -z "$(find "$inspected_root" -mindepth 1 -maxdepth 1 -print -quit)" ]] ||
    fail "invalid $name contract root was accepted or modified"
}

expect_dynamic_rejection() {
  local name="$1"
  local candidate="$2"
  local omit_stage="${3:-}"
  local expected_failure="$4"
  local root
  root="$(new_run_root "mutant-$name")"
  run_candidate "$candidate" "$root" "$adapter" "$omit_stage"
  if (validate_candidate_output "$root") \
    >"$root/validation.stdout" 2>"$root/validation.stderr"; then
    fail "$name mutation survived the dynamic contract"
  fi
  if [[ -n "$expected_failure" ]]; then
    grep -Fq "$expected_failure" "$root/validation.stderr" ||
      fail "$name mutation failed for the wrong reason"
  fi
}

wait_for_file() {
  local path="$1"
  local attempt
  for attempt in $(seq 1 100); do
    [[ -f "$path" && ! -L "$path" ]] && return 0
    sleep 0.05
  done
  return 1
}

run_signal_probe() {
  local root target_pid adapter_pid signal_status=0
  local adapter_alive=false target_alive=false
  local event_count inventory_count project line
  root="$(new_run_root signal-probe)"
  PATH="$tmpdir/bin:/usr/bin:/bin:/opt/homebrew/bin" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT=true \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER="$adapter" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_CALL_LOG="$root/adapter.calls" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_CONTRACT_ROOT="$root" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_CASE_DEADLINE_SECONDS=5 \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_HANG_CASE=drain_timeout \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_ADAPTER_HANG_SECONDS=30 \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_UNEXPECTED_EXTERNAL_LOG="$unexpected_external_log" \
    HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS=1 \
    "$target" "$contract_run_argument" \
    >"$root/stdout" 2>"$root/stderr" &
  target_pid=$!
  ACTIVE_SIGNAL_TARGET_PID="$target_pid"
  wait_for_file "$root/hang.started" ||
    fail 'signal probe did not reach the hanging adapter'
  wait_for_file "$root/hang.pid" ||
    fail 'signal probe did not expose the hanging adapter PID'
  adapter_pid="$(<"$root/hang.pid")"
  [[ "$adapter_pid" =~ ^[1-9][0-9]*$ ]] ||
    fail 'signal probe exposed an invalid adapter PID'
  kill -TERM "$target_pid"
  if wait "$target_pid"; then
    signal_status=0
  else
    signal_status=$?
  fi
  ACTIVE_SIGNAL_TARGET_PID=''
  kill -0 "$target_pid" 2>/dev/null && target_alive=true
  kill -0 "$adapter_pid" 2>/dev/null && adapter_alive=true
  if [[ "$adapter_alive" == true ]]; then
    kill -TERM "$adapter_pid" 2>/dev/null || true
    sleep 0.1
    kill -KILL "$adapter_pid" 2>/dev/null || true
  fi
  event_count="$(wc -l <"$root/events" | tr -d '[:space:]')"
  inventory_count="$(
    find "$root/inventory" -mindepth 1 -maxdepth 1 -type f |
      wc -l |
      tr -d '[:space:]'
  )"
  project="$(
    sed -n '1s/^[^|]*|[^|]*|\([^|]*\)|.*$/\1/p' "$root/events"
  )"
  [[ "$signal_status" == 143 &&
    "$target_alive" == false &&
    "$adapter_alive" == false &&
    "$event_count" == 4 &&
    "$inventory_count" == 1 &&
    "$project" =~ ^happylearn-phase5-live-drain-timeout-[0-9a-f]{12}$ &&
    -f "$root/inventory/$project" &&
    "$(<"$root/inventory/$project")" == 0 &&
    ! -s "$root/stdout" &&
    ! -s "$root/stderr" &&
    ! -e "$unexpected_external_log" ]] ||
    fail "signal probe failed status=$signal_status target_alive=$target_alive adapter_alive=$adapter_alive events=$event_count inventory=$inventory_count"
  line="$(
    sed -n '1p;2p;3p;4p' "$root/events"
  )"
  [[ "$line" == \
"drain_timeout|failed|$project|fixture|ok
drain_timeout|failed|$project|shim|ok
drain_timeout|failed|$project|inject|ok
drain_timeout|failed|$project|cleanup|ok" ]] ||
    fail 'signal probe lifecycle was not exact or entered a later case'
}

new_live_root() {
  local name="$1"
  local root="$tmpdir/runs/live-$name"
  [[ "$name" =~ ^[a-z0-9-]+$ && ! -e "$root" && ! -L "$root" ]] ||
    return 1
  mkdir -m 0700 "$root" "$root/containers" "$root/networks"
  : >"$root/calls"
  : >"$root/stdout"
  : >"$root/stderr"
  chmod 0600 "$root/calls" "$root/stdout" "$root/stderr"
  printf '%s' "$root"
}

run_live_candidate() {
  local root="$1"
  local mutation="${2:-}"
  local hang="${3:-}"
  local candidate="${4:-$target}"
  local status=0
  PATH="$tmpdir/bin:/usr/bin:/bin:/opt/homebrew/bin" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_FAKE_DOCKER_ROOT="$root" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_FAKE_DOCKER_MUTATION="$mutation" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_FAKE_DOCKER_HANG_CASE="$hang" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_UNEXPECTED_EXTERNAL_LOG="$unexpected_external_log" \
    run_bounded "$live_outer_deadline_seconds" \
      env HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS=1 "$candidate" \
    >"$root/stdout" 2>"$root/stderr" ||
    status=$?
  printf '%s\n' "$status" >"$root/status"
}

validate_live_calls() {
  local root="$1"
  local projects="$root/projects"
  local create_count start_count copy_count container_rm_count
  local network_create_count network_rm_count line project
  create_count="$(grep -c '^create|' "$root/calls" || true)"
  start_count="$(grep -c '^start|' "$root/calls" || true)"
  copy_count="$(grep -c '^cp|' "$root/calls" || true)"
  container_rm_count="$(grep -c '^rm|' "$root/calls" || true)"
  network_create_count="$(grep -c '^network|create|' "$root/calls" || true)"
  network_rm_count="$(grep -c '^network|rm|' "$root/calls" || true)"
  [[ "$create_count" == 15 &&
    "$start_count" == 15 &&
    "$copy_count" == 0 &&
    "$container_rm_count" == 15 &&
    "$network_create_count" == 15 &&
    "$network_rm_count" == 15 ]] ||
    fail "live Docker lifecycle was not exact create=$create_count start=$start_count copy=$copy_count container_rm=$container_rm_count network_create=$network_create_count network_rm=$network_rm_count"

  : >"$projects"
  chmod 0600 "$projects"
  while IFS= read -r line; do
    [[ "$line" == *'--read-only'* &&
      "$line" == *'--cap-drop|ALL'* &&
      "$line" == *'--security-opt|no-new-privileges:true'* &&
      "$line" == *'--pids-limit|'* &&
      "$line" == *'--memory|'* &&
      "$line" == *'--cpus|'* &&
      "$line" == *'--tmpfs|'* &&
      "$line" == *'--mount|'* &&
      "$line" == *'/run/happylearn-e2e-shims'* &&
      "$line" == *'alpine@sha256:4bcff63911fcb4448bd4fdacec207030997caf25e9bea4045fa6c8c44de311d1'* ]] ||
      fail 'live case container omitted an isolation control or pinned image'
    project="$(
      sed -n \
        's/.*io\.happylearn\.phase5\.failure-matrix-project=\([^|]*\).*/\1/p' \
        <<<"$line"
    )"
    [[ "$project" =~ ^happylearn-phase5-live-[a-z0-9][a-z0-9-]{0,95}$ ]] ||
      fail 'live case container used an invalid project label'
    printf '%s\n' "$project" >>"$projects"
  done < <(grep '^create|' "$root/calls")
  [[ "$(sort "$projects" | uniq | wc -l | tr -d '[:space:]')" == 15 ]] ||
    fail 'live case project labels were not unique'
  [[ -z "$(find "$root/containers" -mindepth 1 -maxdepth 1 -print -quit)" &&
    -z "$(find "$root/networks" -mindepth 1 -maxdepth 1 -print -quit)" ]] ||
    fail 'live Docker inventory was not empty after all cases'
}

validate_live_candidate() {
  local root="$1"
  [[ "$(<"$root/status")" == 0 ]] ||
    fail "live target exited with status $(<"$root/status")"
  validate_summary "$root/stdout" "$root/traces"
  validate_live_calls "$root"
  [[ ! -s "$root/stderr" ]] ||
    fail 'live target wrote unexpected stderr'
  [[ ! -e "$unexpected_external_log" ]] ||
    fail 'live target invoked an unexpected external command'
}

expect_live_rejection() {
  local name="$1"
  local mutation="${2:-}"
  local hang="${3:-}"
  local candidate="${4:-$target}"
  local root
  root="$(new_live_root "reject-$name")"
  run_live_candidate "$root" "$mutation" "$hang" "$candidate"
  [[ "$(<"$root/status")" != 0 &&
    ! -s "$root/stdout" &&
    ! -s "$root/stderr" &&
    -z "$(find "$root/containers" -mindepth 1 -maxdepth 1 -print -quit)" &&
    -z "$(find "$root/networks" -mindepth 1 -maxdepth 1 -print -quit)" &&
    "$(grep -c '^create|' "$root/calls" || true)" == 1 &&
    "$(grep -c '^rm|' "$root/calls" || true)" == 1 &&
    "$(grep -c '^network|create|' "$root/calls" || true)" == 1 &&
    "$(grep -c '^network|rm|' "$root/calls" || true)" == 1 &&
    ! -e "$unexpected_external_log" ]] ||
    fail "live $name mutation was accepted or left residue"
}

expect_live_signal_cleanup() {
  local root target_pid docker_pid signal_status=0
  local target_alive=false docker_alive=false
  root="$(new_live_root signal)"
  PATH="$tmpdir/bin:/usr/bin:/bin:/opt/homebrew/bin" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_FAKE_DOCKER_ROOT="$root" \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_FAKE_DOCKER_MUTATION='' \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_FAKE_DOCKER_HANG_CASE=drain_timeout \
    HAPPYLEARN_PHASE5_FAILURE_MATRIX_UNEXPECTED_EXTERNAL_LOG="$unexpected_external_log" \
    HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS=1 \
    "$target" >"$root/stdout" 2>"$root/stderr" &
  target_pid=$!
  ACTIVE_SIGNAL_TARGET_PID="$target_pid"
  wait_for_file "$root/hang.started" ||
    fail 'live signal probe did not reach the hanging Docker command'
  wait_for_file "$root/hang.pid" ||
    fail 'live signal probe did not expose the Docker command PID'
  docker_pid="$(<"$root/hang.pid")"
  [[ "$docker_pid" =~ ^[1-9][0-9]*$ ]] ||
    fail 'live signal probe exposed an invalid Docker command PID'
  kill -TERM "$target_pid"
  if wait "$target_pid"; then
    signal_status=0
  else
    signal_status=$?
  fi
  ACTIVE_SIGNAL_TARGET_PID=''
  kill -0 "$target_pid" 2>/dev/null && target_alive=true
  kill -0 "$docker_pid" 2>/dev/null && docker_alive=true
  if [[ "$docker_alive" == true ]]; then
    kill -TERM "$docker_pid" 2>/dev/null || true
    sleep 0.1
    kill -KILL "$docker_pid" 2>/dev/null || true
  fi
  [[ "$signal_status" == 143 &&
    "$target_alive" == false &&
    "$docker_alive" == false &&
    ! -s "$root/stdout" &&
    ! -s "$root/stderr" &&
    -z "$(find "$root/containers" -mindepth 1 -maxdepth 1 -print -quit)" &&
    -z "$(find "$root/networks" -mindepth 1 -maxdepth 1 -print -quit)" &&
    "$(grep -c '^create|' "$root/calls" || true)" == 1 &&
    "$(grep -c '^rm|' "$root/calls" || true)" == 1 &&
    "$(grep -c '^network|create|' "$root/calls" || true)" == 1 &&
    "$(grep -c '^network|rm|' "$root/calls" || true)" == 1 &&
    ! -e "$unexpected_external_log" ]] ||
    fail "live signal cleanup failed status=$signal_status target_alive=$target_alive docker_alive=$docker_alive"
}

expect_contract_mode_rejection

relative_adapter='bin/phase5-failure-matrix-adapter'
expect_adapter_path_rejection relative "$relative_adapter" "$tmpdir"

permissive_adapter="$tmpdir/bin/permissive-adapter"
cp "$adapter" "$permissive_adapter"
chmod 0755 "$permissive_adapter"
expect_adapter_path_rejection permissive "$permissive_adapter"

symlink_adapter="$tmpdir/bin/symlink-adapter"
ln -s "$adapter" "$symlink_adapter"
expect_adapter_path_rejection symlink "$symlink_adapter"

relative_contract_root="$tmpdir/unsafe-relative-root"
mkdir -m 0700 "$relative_contract_root"
expect_contract_root_rejection \
  relative unsafe-relative-root "$relative_contract_root" "$tmpdir"

permissive_contract_root="$tmpdir/permissive-root"
mkdir -m 0755 "$permissive_contract_root"
expect_contract_root_rejection \
  permissive "$permissive_contract_root" "$permissive_contract_root"

symlink_contract_target="$tmpdir/symlink-root-target"
symlink_contract_root="$tmpdir/symlink-root"
mkdir -m 0700 "$symlink_contract_target"
ln -s "$symlink_contract_target" "$symlink_contract_root"
expect_contract_root_rejection \
  symlink "$symlink_contract_root" "$symlink_contract_target"

run_signal_probe

baseline_root="$(new_run_root baseline)"
baseline_started="$(contract_milliseconds)" ||
  fail 'contract monotonic clock is unavailable'
run_candidate "$target" "$baseline_root" "$adapter"
baseline_finished="$(contract_milliseconds)" ||
  fail 'contract monotonic clock failed'
baseline_duration=$((baseline_finished - baseline_started))
validate_candidate_output "$baseline_root"
[[ -f "$baseline_root/hang.started" &&
  -f "$baseline_root/hang.pid" &&
  "$(<"$baseline_root/hang.pid")" =~ ^[1-9][0-9]*$ &&
  "$baseline_duration" -ge 900 &&
  "$baseline_duration" -lt $((outer_deadline_seconds * 1000)) ]] ||
  fail 'hang injection did not reach and respect the small case deadline'
if kill -0 "$(<"$baseline_root/hang.pid")" 2>/dev/null; then
  fail 'hang injection process survived the case deadline'
fi

early_return_mutant="$tmpdir/mutants/early-return.sh"
awk '
  { print }
  $0 == "run_case() {" {
    print "  return 0"
    inserted++
  }
  END {
    if (inserted != 1) exit 42
  }
' "$target" >"$early_return_mutant" ||
  fail 'could not create the run_case early-return mutation'
chmod 0700 "$early_return_mutant"
expect_dynamic_rejection \
  early-return "$early_return_mutant" '' ''

expect_dynamic_rejection \
  missing-alert-stage "$target" alert \
  'lifecycle event count was 120 instead of 135'

canned_summary="$tmpdir/canned-summary.jsonl"
: >"$canned_summary"
canned_index=1
for entry in "${expected_entries[@]}"; do
  canned_case="${entry%%:*}"
  canned_state="${entry#*:}"
  printf -v canned_tail '%012d' "$canned_index"
  printf \
    '{"case":"%s","expected":"%s","actual":"%s","duration":1,"trace":"00000000-0000-4000-8000-%s"}\n' \
    "$canned_case" "$canned_state" "$canned_state" "$canned_tail" \
    >>"$canned_summary"
  canned_index=$((canned_index + 1))
done
chmod 0600 "$canned_summary"

canned_branch_mutant="$tmpdir/mutants/canned-branch.sh"
cat >"$canned_branch_mutant" <<'CANNED_BRANCH'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "${1:-}" == --contract-run ]]; then
  exec /bin/cat "${HAPPYLEARN_PHASE5_FAILURE_MATRIX_CANNED_SUMMARY:?}"
fi
exec "${HAPPYLEARN_PHASE5_FAILURE_MATRIX_REAL_TARGET:?}" "$@"
CANNED_BRANCH
chmod 0700 "$canned_branch_mutant"
expect_dynamic_rejection \
  canned-branch "$canned_branch_mutant" '' \
  'lifecycle event count was 0 instead of 135'

live_baseline_root="$(new_live_root baseline)"
run_live_candidate "$live_baseline_root"
validate_live_candidate "$live_baseline_root"

expect_live_rejection terminal terminal
expect_live_rejection maintenance maintenance
expect_live_rejection alert alert
expect_live_rejection plaintext plaintext
expect_live_rejection deadline '' drain_timeout

sanitizer_mutant_directory="$tmpdir/mutants/live-sanitizer"
mkdir -m 0700 "$sanitizer_mutant_directory"
cp \
  "$target" \
  "$harness_lib" \
  "$sanitizer" \
  "$publisher" \
  "$sanitizer_mutant_directory/"
sanitizer_mutant="$sanitizer_mutant_directory/e2e-phase5_failure_matrix.sh"
awk '
  $0 == "  \"$SANITIZER\" \"$raw_directory\"" {
    print "  : \"$raw_directory\""
    replaced++
    next
  }
  { print }
  END {
    if (replaced != 1) exit 42
  }
' "$target" >"$sanitizer_mutant.pending" ||
  fail 'could not create the live sanitizer-bypass mutation'
mv "$sanitizer_mutant.pending" "$sanitizer_mutant"
chmod 0700 "$sanitizer_mutant"
expect_live_rejection sanitizer-bypass '' '' "$sanitizer_mutant"

expect_live_signal_cleanup

printf 'phase 5 failure matrix contract: PASS cases=15 lifecycle=135 live=15 timeouts=2 signals=2 inventory=0 mutations=8\n'
