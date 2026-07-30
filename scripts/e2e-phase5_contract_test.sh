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

require_literal "$target" 'source "$script_dir/e2e-harness-lib.sh"'
require_literal "$target" '"$license_file" != /*'
require_literal "$target" '! -r "$license_file"'
require_literal "$target" 'prefix="happylearn_phase5_'
require_pattern "$target" \
  'docker_bounded 60 network create --internal([[:space:]\\]|$)'
require_literal "$target" 'case "$e2e_group" in'
require_literal "$target" 'all|phase5|phase5-mobile|recovery|resources)'
require_literal "$target" \
  'live_project="happylearn-phase5-live-${fixture_suffix}"'
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
require_literal "$target" 'umask 077'
require_literal "$target" 'chmod 0600 "$secret_source_dir/"*'
require_literal "$target" 'chmod 0400 "$target"'
require_literal "$target" 'chmod 0500 "$directory"'
require_literal "$target" '--cap-add DAC_READ_SEARCH'
secret_init_block="$(sed -n \
  '/^initialize_secret_volume()/,/^}/p' "$target")"
if grep -Fq -- '--cap-add FOWNER' <<<"$secret_init_block" ||
  grep -Fq -- '--cap-add DAC_OVERRIDE' <<<"$secret_init_block"; then
  fail 'secret init retained an unnecessary mutation capability'
fi
require_literal "$target" 'verify_secret_consumer_reads'
for uid in 999:999 1000:0 10001:10001 10002:10002 10003:0; do
  require_literal "$target" "'$uid'"
done
require_literal "$target" \
  'test "$(stat -c "%u:%g:%a" "$target")" = "$owner:400"'
require_literal "$target" \
  'test "$(stat -c "%u:%g:%a" "$directory")" = "$owner:500"'
require_literal "$target" 'owned_container_ledger="$tmpdir/owned-containers.tsv"'
require_literal "$target" 'record_owned_container'
require_literal "$target" 'remove_owned_container_if_match'
require_literal "$target" \
  '{{.Id}}|{{.Name}}|{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "io.happylearn.phase5.e2e-owner"}}'
require_literal "$target" '--cleanup-container-ownership-probe'

require_literal "$target" \
  'compose_live up --detach --no-build --no-deps app'
require_literal "$target" \
  'compose_live up --detach --no-build --no-deps worker'
require_literal "$target" 'verify_compose_service_claims'
require_literal "$target" 'compose_live ps --quiet "$service"'
require_literal "$target" 'compose_live stop --timeout 30 worker'
require_literal "$target" 'compose_live start worker'
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
require_literal "$target" 'HAPPYLEARN_RESTORE_CONTROL_DIRECTORY="$restore_control_dir"'
require_literal "$target" 'HAPPYLEARN_RESTORE_REPORT_DIRECTORY="$restore_report_dir"'
if grep -Eq 'phase5-(backup|restore)_live_test\.sh' "$target"; then
  fail 'an unsafe live fixture was used as a black-box acceptance proof'
fi
require_literal "$target" 'run_resource_sample 60'
require_literal "$target" 'run_resource_sample 1800'
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
require_literal "$target" 'init-e2e-artifacts.sh'
require_literal "$target" 'PHASE5_ARTIFACT_WRITE_PROBE'
require_literal "$target" '--user 1000:1000 --cap-drop ALL'
if grep -Fq -- 'docker_bounded 30 rm -f "${temporary_containers[@]}"' "$target" ||
  grep -Fq -- 'docker_bounded 30 rm -f "${service_containers[@]}"' "$target" ||
  grep -Fq -- '--filter "label=com.docker.compose.project=${live_project}"' "$target"; then
  fail 'cleanup retained name-only or project-wide container deletion'
fi
require_literal "$target" 'docker_bounded 30 network rm "$recorded_id"'
require_literal "$target" 'owned_volume_ledger="$tmpdir/owned-volumes.tsv"'
require_literal "$target" 'owned_image_ledger="$tmpdir/owned-images.tsv"'
require_literal "$target" \
  'docker_bounded 30 volume rm "$recorded_name"'
require_literal "$target" \
  'docker_bounded 60 image rm "$recorded_reference"'

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
      environment="$argument"
      previous=''
      continue
    fi
    case "$argument" in
      --env-file|-e|--env) previous="$argument" ;;
      phase5-e2e-secret-marker) command="$argument" ;;
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
  sed -n '1p' "${state}.env"
  exit 0
fi
if [[ "${1:-}" == inspect && "$*" == *'{{json .HostConfig.Binds}}'* ]]; then
  printf '%s\n' '[]|false|"none"'
  exit 0
fi
if [[ "${1:-}" == inspect && "$*" == *'io.happylearn.phase5.e2e-owner'* ]]; then
  target="${*: -1}"
  case "$E2E_FAKE_DOCKER_MODE" in
    cleanup_owned)
      printf '%s|/%s|%s|%s\n' \
        "${E2E_CLEANUP_ID:?}" "$target" \
        "${E2E_CLEANUP_PROJECT:?}" "${E2E_CLEANUP_OWNER:?}"
      ;;
    cleanup_collision)
      printf 'foreign-id|/%s|%s|foreign-owner\n' \
        "$target" "${E2E_CLEANUP_PROJECT:?}"
      ;;
    signal)
      IFS='|' read -r id name project owner \
        <"${E2E_FAKE_DOCKER_STATE:?}.ownership"
      [[ "$target" == "$name" || "$target" == "$id" ]] || exit 1
      printf '%s|/%s|%s|%s\n' "$id" "$name" "$project" "$owner"
      ;;
    coordinator)
      printf '%s|/%s|%s|True|%s\n' \
        "${E2E_COORDINATOR_ID:?}" phase5-coordinator-one-shot \
        "${E2E_COORDINATOR_PROJECT:?}" "${E2E_COORDINATOR_OWNER:?}"
      ;;
    *) exit 96 ;;
  esac
  exit 0
fi
if [[ "${1:-}" == rm && "${2:-}" == -f ]]; then
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

run_audit_probe() {
  local mode="$1"
  local expected_status="$2"
  local status=0
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

run_cleanup_probe() {
  local mode="$1"
  local expect_removed="$2"
  local status=0
  : >"$tmpdir/docker.log"
  PATH="$tmpdir/bin:$PATH" \
    E2E_UNEXPECTED_DOCKER_CALL="$tmpdir/docker-called" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE="$mode" \
    E2E_CLEANUP_ID=phase5-owned-id \
    E2E_CLEANUP_PROJECT=happylearn-phase5-live-a1b2c3d4e5f6 \
    E2E_CLEANUP_OWNER=a1b2c3d4e5f6 \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=phase5 \
    "$target" --cleanup-container-ownership-probe \
      phase5-cleanup-canary phase5-owned-id \
      happylearn-phase5-live-a1b2c3d4e5f6 a1b2c3d4e5f6 \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  [[ "$status" -eq 0 ]] ||
    fail "cleanup ownership probe $mode returned $status"
  if [[ "$expect_removed" == yes ]]; then
    grep -Fq 'rm -f phase5-owned-id' "$tmpdir/docker.log" ||
      fail 'owned cleanup probe did not remove the exact recorded ID'
  elif grep -Fq 'rm -f' "$tmpdir/docker.log"; then
    fail 'collision cleanup probe removed an unowned container'
  fi
}

run_cleanup_probe cleanup_owned yes
run_cleanup_probe cleanup_collision no

run_coordinator_one_shot_probe() {
  local record="$tmpdir/coordinator-one-shots"
  local one_shot_id='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
  local project='happylearn-phase5-live-a1b2c3d4e5f6'
  local owner='a1b2c3d4e5f6'
  local status=0
  printf '%s\n' "$one_shot_id" >"$record"
  chmod 0600 "$record"
  printf '%s\n' PATH=/usr/bin >"$tmpdir/coordinator-state.env"
  printf '%s\n' safe >"$tmpdir/coordinator-state.cmd"
  : >"$tmpdir/docker.log"
  PATH="$tmpdir/bin:$PATH" \
    E2E_FAKE_DOCKER_LOG="$tmpdir/docker.log" \
    E2E_FAKE_DOCKER_MODE=coordinator \
    E2E_FAKE_DOCKER_STATE="$tmpdir/coordinator-state" \
    E2E_COORDINATOR_ID="$one_shot_id" \
    E2E_COORDINATOR_PROJECT="$project" \
    E2E_COORDINATOR_OWNER="$owner" \
    HAPPYLEARN_PHASE5_COORDINATOR_CONTRACT=1 \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$readable_license" \
    HAPPYLEARN_E2E_GROUP=recovery \
    "$target" --coordinator-one-shot-probe \
      "$record" "$project" "$owner" \
      >"$tmpdir/stdout" 2>"$tmpdir/stderr" || status=$?
  [[ "$status" -eq 0 && ! -s "$record" ]] ||
    fail "coordinator one-shot probe returned $status or retained its ledger"
  grep -Fq \
    'rm -f cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' \
    "$tmpdir/docker.log" ||
    fail 'coordinator one-shot probe did not remove the exact audited ID'
  grep -Fq '{{json .Config.Env}}' "$tmpdir/docker.log" ||
    fail 'coordinator one-shot probe skipped runtime metadata audit'
}

run_coordinator_one_shot_probe

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
