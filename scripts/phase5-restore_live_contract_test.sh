#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
umask 077

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TARGET="$ROOT/scripts/phase5-restore_live_test.sh"
CONTROLLER_DOCKERFILE="$ROOT/deploy/Dockerfile.restore-live-controller"
CONTRACT_ROOT="$(mktemp -d \
  "${TMPDIR:-/tmp}/phase5-restore-live-contract.XXXXXX")"

cleanup_contract() {
  if [[ "$CONTRACT_ROOT" == \
      "${TMPDIR:-/tmp}/phase5-restore-live-contract."* &&
    -d "$CONTRACT_ROOT" && ! -L "$CONTRACT_ROOT" ]]; then
    chmod -R u+rwX "$CONTRACT_ROOT" 2>/dev/null || true
    rm -rf "$CONTRACT_ROOT"
  fi
}
trap cleanup_contract EXIT HUP INT TERM

fail() {
  printf 'phase5 restore live contract: %s\n' "$1" >&2
  exit 1
}

require_literal() {
  local value="$1"
  grep -Fq -- "$value" "$TARGET" ||
    fail "live gate missing literal: $value"
}

require_pattern() {
  local value="$1"
  grep -Eq -- "$value" "$TARGET" ||
    fail "live gate missing pattern: $value"
}

forbid_pattern() {
  local value="$1"
  if grep -Eiq -- "$value" "$TARGET"; then
    fail "live gate contains forbidden pattern: $value"
  fi
}

portable_sha256_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256
  else
    return 1
  fi
}

[[ -f "$TARGET" && ! -L "$TARGET" ]] ||
  fail 'scripts/phase5-restore_live_test.sh is absent'
[[ -f "$CONTROLLER_DOCKERFILE" && ! -L "$CONTROLLER_DOCKERFILE" ]] ||
  fail 'deploy/Dockerfile.restore-live-controller is absent'
bash -n "$TARGET"

require_literal 'set -euo pipefail'
require_literal 'build_restore_controller'
require_literal 'run_restore_controller'
require_literal 'docker build \'
require_literal '--file "$CONTROLLER_DOCKERFILE" \'
require_literal '--mount "type=bind,src=$DOCKER_SOCKET,dst=/var/run/docker.sock"'
require_literal '--mount "type=bind,src=$ROOT,dst=$ROOT,readonly"'
require_literal '--mount "type=bind,src=$FIXTURE_ROOT/repository,dst=$FIXTURE_ROOT/repository"'
require_literal '--mount "type=bind,src=$FIXTURE_ROOT/control,dst=$FIXTURE_ROOT/control"'
require_literal '--mount "type=bind,src=$FIXTURE_ROOT/report,dst=$FIXTURE_ROOT/report"'
require_literal '--mount "type=bind,src=$FIXTURE_ROOT/controller-tmp,dst=$FIXTURE_ROOT/controller-tmp"'
require_literal '--mount "type=bind,src=$FIXTURE_ROOT/secrets,dst=$FIXTURE_ROOT/secrets,readonly"'
require_literal '--env "TMPDIR=$FIXTURE_ROOT/controller-tmp"'
require_literal '--env "RESTORE_SCRIPT=$RESTORE_SCRIPT"'
require_literal '--env "RESTORE_STAGE_FILE=$RESTORE_STAGE_FILE"'
require_literal '--env "BACKUP_ID=$BACKUP_ID"'
require_literal '--user "$HOST_UID:$HOST_GID"'
require_literal '--group-add "$CONTROLLER_SOCKET_GID"'
require_literal 'controller_supplementary_groups_match "$2" "$3"'
require_literal 'test "$(stat -c "%g" /var/run/docker.sock)" = "$3"'
require_literal 'test "$(stat -c "%a" /var/run/docker.sock)" = "$4"'
require_literal 'write_restore_stage wrapper_preflight'
require_literal 'write_restore_stage verifier_started'
require_literal 'source "$RESTORE_SCRIPT" --backup-id "$BACKUP_ID"'
require_literal 'valid_restore_stage'
require_literal 'owner_private_regular_file "$RESTORE_STAGE_FILE" 600 64'
require_literal 'mv -f "$temporary" "$RESTORE_STAGE_FILE"'
require_literal 'category=${failure_category} stage=${failure_stage}'
forbid_pattern '/usr/bin/bash'
require_literal 'controller_identity_matches'
require_literal 'docker container rm --force "$CONTROLLER_ID"'
require_literal '[[ -S "$DOCKER_SOCKET" && ! -L "$DOCKER_SOCKET" ]]'
require_literal "readonly DEFAULT_LICENSE_SOURCE='/Users/lane/Downloads/minio.license'"
require_literal 'PROJECT="happylearn-phase5-live-${FIXTURE_SUFFIX}"'
require_literal 'FIXTURE_ROOT="$(mktemp -d \'
require_literal 'install -m 0400 "$LICENSE_SOURCE" "$LICENSE_COPY"'
require_literal 'mkdir -m 0700 \'
require_literal 'chmod 0600 "$TEACHER_PASSWORD_FILE"'
require_literal 'chmod 0400 "$TEACHER_CREDENTIAL_FILE"'
require_literal 'owner_private_regular_file "$password_file" 600 4096'
require_literal 'owner_only_regular_file "$credential_file" 4096'
require_literal 'compose build app worker'
require_literal 'docker build --file "$ROOT/Dockerfile.backup"'
require_literal '--entrypoint /app/happylearn-admin'
require_literal 'create-teacher'
require_literal '--password-file /run/secrets/teacher-password'
require_literal 'bash "$BACKUP_SCRIPT" --project happylearn-dev --trigger manual'
require_literal "WHERE state='succeeded'"
require_literal 'count(*)=1'
require_literal 'prepare_restore_repository_access'
require_literal 'restore_backup_repository_access'
require_literal 'compose run --rm --no-deps \'
require_literal 'backup-storage-init'
require_literal 'find -P /repository -xdev -type l -print -quit'
require_literal 'find -P /repository -xdev ! -type d ! -type f -print -quit'
require_literal 'find -P /repository -xdev -type f -links +1 -print -quit'
require_literal 'chown -R -- "$uid:$gid" /repository'
require_literal 'chown -R -- 10003:0 /repository'
require_literal '[[ "$HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY" == "$FIXTURE_ROOT/repository" ]]'
require_literal 'restic --no-cache \'
require_literal '--repository-file /run/secrets/local_repository \'
require_literal '--password-file /run/secrets/local_password \'
require_literal 'cat config \'
require_literal 'HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE="$TEACHER_CREDENTIAL_FILE"'
require_literal 'parse_sanitized_report "$REPORT_FILE" "$BACKUP_ID" "$EXPECTED_MANIFEST_SHA256"'
require_literal 'assert_restore_resources_absent "$BACKUP_ID"'
require_literal 'assert_secret_absent "$TEACHER_PASSWORD_FILE" "$TEACHER_CREDENTIAL_FILE" "$RESTORE_LOG" "$REPORT_FILE"'
require_literal 'assert_single_line_secret_absent "$FIXTURE_ROOT/secrets/local_password" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE"'
require_literal 'assert_single_line_secret_absent "$FIXTURE_ROOT/secrets/database_password" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE"'
require_literal 'assert_single_line_secret_absent "$LICENSE_COPY" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE"'
require_literal 'assert_age_identity_absent "$FIXTURE_ROOT/offline/age.identity" "$BACKUP_LOG" "$RESTORE_LOG" "$REPORT_FILE"'
require_literal 'extract_restore_failure_category "$RESTORE_LOG"'
require_literal 'remove_labeled_project_volumes'
require_literal 'volume_identity_matches "$volume"'
require_literal "case \"\$compose_volume\" in"
require_literal 'minio_data | postgres_tls | backup_secrets)'
require_literal 'docker volume rm "$volume"'
require_literal 'trap on_exit EXIT'
require_literal "printf 'phase5 restore live: PASS"
require_literal 'if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then'
require_literal 'main "$@"'

require_literal '"$duration" -lt "$RTO_LIMIT_SECONDS"'
require_pattern 'isolation404ProbeCount[^[:cntrl:]]*2'
require_pattern 'reportSHA256'
require_pattern 'manifestSHA256'
require_pattern 'verificationReportSHA256'
require_pattern 'evidenceSHA256'
require_pattern 'rowCountTotal'
require_pattern 'missingObjectCount'
require_pattern 'unexpectedObjectCount'
require_pattern 'activeSessionCount'

forbid_pattern 'phase5-backup_live_test\.sh'
forbid_pattern 'docker[[:space:]]+system[[:space:]]+prune'
forbid_pattern 'docker[[:space:]]+(container[[:space:]]+|volume[[:space:]]+|network[[:space:]]+)?prune'
forbid_pattern 'HAPPYLEARN_[A-Z0-9_]*(PASSWORD|SECRET_KEY|ACCESS_KEY)[[:space:]]*='
forbid_pattern 'create-teacher[^[:cntrl:]]*--password[=[:space:]]'
forbid_pattern 'restore-http-probe'
forbid_pattern 'student-isolation-probe'
forbid_pattern '--privileged'
forbid_pattern '--network[=[:space:]]+host'
forbid_pattern '--mount "type=bind,src=\$FIXTURE_ROOT,dst=\$FIXTURE_ROOT"'
forbid_pattern 'bash[[:space:]]+-x'
forbid_pattern 'set[[:space:]]+-x'
forbid_pattern 'PS4='

valid_stage_body="$(sed -n '/^valid_restore_stage()/,/^}/p' "$TARGET")"
for stage in \
  wrapper_preflight verifier_started paths_validate_started \
  restore_lock_started report_lock_started identity_init_started \
  workspace_init_started orphan_reap_started network_create_started \
  volume_create_started repository_check_started snapshot_select_started \
  snapshot_restore_started object_restore_started license_init_started \
  postgres_start_started database_restore_started dependencies_start_started \
  sessions_revoke_started app_start_started app_ready_wait_started \
  restore_check_started counts_load_started http_probe_started \
  report_publish_started; do
  [[ "$(printf '%s\n' "$valid_stage_body" |
    grep -Eoc "(^|[ |])${stage}([ |)])")" == 1 ]] ||
    fail "restore stage allowlist is missing or duplicates: $stage"
done

grep -Fq \
  'FROM docker@sha256:be132a9f282288de4afaf63379dff75711fda0147c6b72a9df44e51841402144 AS docker_cli' \
  "$CONTROLLER_DOCKERFILE" ||
  fail 'restore controller Docker CLI base is not pinned'
grep -Fq 'RUN apk add --no-cache bash coreutils util-linux' \
  "$CONTROLLER_DOCKERFILE" ||
  fail 'restore controller adds more than the required shell/lock tools'
grep -Fq 'RUN docker --version && bash --version && flock --version' \
  "$CONTROLLER_DOCKERFILE" ||
  fail 'restore controller does not verify its fixed tools'
grep -Fq 'LABEL io.happylearn.test-only="phase5-restore-live-controller"' \
  "$CONTROLLER_DOCKERFILE" ||
  fail 'restore controller is not explicitly test-only'
grep -Fq 'CMD ["/bin/bash"]' "$CONTROLLER_DOCKERFILE" ||
  fail 'restore controller does not use the Alpine bash path'
if grep -Fq '/usr/bin/bash' "$CONTROLLER_DOCKERFILE"; then
  fail 'restore controller uses a nonexistent Alpine bash path'
fi
if grep -Fq 'Dockerfile.restore-live-controller' \
  "$ROOT/deploy/compose.dev.yml" \
  "$ROOT/deploy/compose.ci.yml" \
  "$ROOT/deploy/compose.backup-live.yml"; then
  fail 'test-only restore controller entered a Compose artifact'
fi

portable_file_body="$(
  sed -n '/^portable_sha256_file()/,/^}/p' "$TARGET"
)"
[[ "$(printf '%s\n' "$portable_file_body" |
  grep -Fc "sed -n '1s/[[:space:]].*\$//p'")" == 1 ]] ||
  fail 'portable file SHA parser does not contain exactly one sed stage'
age_body="$(sed -n '/^generate_age_identity()/,/^}/p' "$TARGET")"
[[ "$(printf '%s\n' "$age_body" | grep -Fc -- '--entrypoint')" == 2 ]] ||
  fail 'Age identity commands do not each contain exactly one entrypoint'
restore_body="$(sed -n '/^run_empty_restore()/,/^}/p' "$TARGET")"
[[ "$(printf '%s\n' "$restore_body" |
  grep -Fc 'export HAPPYLEARN_RESTORE_CONTROL_DIRECTORY=')" == 1 ]] ||
  fail 'restore control directory is exported more than once'

trap_line="$(grep -nF 'trap on_exit EXIT' "$TARGET" | tail -n1 | cut -d: -f1)"
fixture_line="$(grep -nE '^[[:space:]]*create_fixture$' "$TARGET" | tail -n1 | cut -d: -f1)"
[[ -n "$trap_line" && -n "$fixture_line" &&
  "$trap_line" -lt "$fixture_line" ]] ||
  fail 'cleanup trap must precede fixture creation'

backup_line="$(grep -nE '^[[:space:]]*run_source_backup$' "$TARGET" | tail -n1 | cut -d: -f1)"
handoff_line="$(grep -nE '^[[:space:]]*prepare_restore_repository_access$' "$TARGET" | tail -n1 | cut -d: -f1)"
restore_line="$(grep -nE '^[[:space:]]*run_empty_restore$' "$TARGET" | tail -n1 | cut -d: -f1)"
return_line="$(grep -nE '^[[:space:]]*restore_backup_repository_access$' "$TARGET" | tail -n1 | cut -d: -f1)"
cleanup_line="$(grep -nF 'cleanup_live 0' "$TARGET" | tail -n1 | cut -d: -f1)"
[[ -n "$backup_line" && -n "$handoff_line" && -n "$restore_line" &&
  -n "$return_line" && -n "$cleanup_line" &&
  "$backup_line" -lt "$handoff_line" &&
  "$handoff_line" -lt "$restore_line" &&
  "$restore_line" -lt "$return_line" &&
  "$return_line" -lt "$cleanup_line" ]] ||
  fail 'repository ownership transitions are ordered incorrectly'

# Source only pure validators. The main guard must keep Docker untouched.
# shellcheck source=phase5-restore_live_test.sh
source "$TARGET"

RESTORE_STAGE_FILE="$CONTRACT_ROOT/restore.stage"
printf 'repository_check_started\n' >"$RESTORE_STAGE_FILE"
chmod 0600 "$RESTORE_STAGE_FILE"
[[ "$(extract_restore_failure_stage)" == repository_check_started ]] ||
  fail 'fixed restore stage marker was rejected'
printf 'unlisted_dynamic_stage\n' >"$RESTORE_STAGE_FILE"
chmod 0600 "$RESTORE_STAGE_FILE"
if extract_restore_failure_stage >/dev/null; then
  fail 'unlisted restore stage marker was accepted'
fi
printf 'repository_check_started\nsnapshot_select_started\n' \
  >"$RESTORE_STAGE_FILE"
chmod 0600 "$RESTORE_STAGE_FILE"
if extract_restore_failure_stage >/dev/null; then
  fail 'multi-line restore stage marker was accepted'
fi

BACKUP_ID='11111111-1111-4111-8111-111111111111'
MANIFEST_SHA256='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
VERIFICATION_SHA256='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
EVIDENCE_SHA256='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
CANONICAL_FILE="$CONTRACT_ROOT/report.canonical"
REPORT_FILE="$CONTRACT_ROOT/restore-${BACKUP_ID}.json"

printf '%s\n' \
  'schemaVersion=1' \
  "backupId=$BACKUP_ID" \
  "manifestSHA256=$MANIFEST_SHA256" \
  "verificationReportSHA256=$VERIFICATION_SHA256" \
  "evidenceSHA256=$EVIDENCE_SHA256" \
  'durationSeconds=42' \
  'migrationVersion=20' \
  'rowCountTotal=3' \
  'checkedObjectCount=0' \
  'missingObjectCount=0' \
  'unexpectedObjectCount=0' \
  'activeSessionCount=0' \
  'isolation404ProbeCount=2' \
  >"$CANONICAL_FILE"
REPORT_SHA256="$(
  portable_sha256_stdin <"$CANONICAL_FILE" |
    sed -n '1s/[[:space:]].*$//p'
)"
[[ "$REPORT_SHA256" =~ ^[a-f0-9]{64}$ ]] ||
  fail 'contract could not create report hash'
printf '%s\n' \
  "{\"schemaVersion\":1,\"backupId\":\"$BACKUP_ID\",\"manifestSHA256\":\"$MANIFEST_SHA256\",\"verificationReportSHA256\":\"$VERIFICATION_SHA256\",\"evidenceSHA256\":\"$EVIDENCE_SHA256\",\"durationSeconds\":42,\"migrationVersion\":20,\"rowCountTotal\":3,\"checkedObjectCount\":0,\"missingObjectCount\":0,\"unexpectedObjectCount\":0,\"activeSessionCount\":0,\"isolation404ProbeCount\":2,\"reportSHA256\":\"$REPORT_SHA256\"}" \
  >"$REPORT_FILE"
chmod 0600 "$REPORT_FILE"

parse_sanitized_report "$REPORT_FILE" "$BACKUP_ID" "$MANIFEST_SHA256" ||
  fail 'strict valid report was rejected'
[[ "$LIVE_REPORT_DURATION_SECONDS" == 42 &&
  "$LIVE_REPORT_ROW_COUNT_TOTAL" == 3 &&
  "$LIVE_REPORT_ISOLATION_404_PROBE_COUNT" == 2 ]] ||
  fail 'valid report fields were not loaded'

cp "$REPORT_FILE" "$CONTRACT_ROOT/wrong-404.json"
sed -i.bak 's/"isolation404ProbeCount":2/"isolation404ProbeCount":1/' \
  "$CONTRACT_ROOT/wrong-404.json"
rm -f "$CONTRACT_ROOT/wrong-404.json.bak"
if parse_sanitized_report \
  "$CONTRACT_ROOT/wrong-404.json" "$BACKUP_ID" "$MANIFEST_SHA256"; then
  fail 'report with one isolation 404 was accepted'
fi

cp "$REPORT_FILE" "$CONTRACT_ROOT/wrong-manifest.json"
sed -i.bak \
  's/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd/' \
  "$CONTRACT_ROOT/wrong-manifest.json"
rm -f "$CONTRACT_ROOT/wrong-manifest.json.bak"
if parse_sanitized_report \
  "$CONTRACT_ROOT/wrong-manifest.json" "$BACKUP_ID" "$MANIFEST_SHA256"; then
  fail 'report with the wrong manifest hash was accepted'
fi

cp "$REPORT_FILE" "$CONTRACT_ROOT/wrong-backup.json"
sed -i.bak \
  's/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/' \
  "$CONTRACT_ROOT/wrong-backup.json"
rm -f "$CONTRACT_ROOT/wrong-backup.json.bak"
if parse_sanitized_report \
  "$CONTRACT_ROOT/wrong-backup.json" "$BACKUP_ID" "$MANIFEST_SHA256"; then
  fail 'report with the wrong backup ID was accepted'
fi

cp "$REPORT_FILE" "$CONTRACT_ROOT/wrong-report-hash.json"
sed -i.bak \
  's/"reportSHA256":"[a-f0-9]*"/"reportSHA256":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"/' \
  "$CONTRACT_ROOT/wrong-report-hash.json"
rm -f "$CONTRACT_ROOT/wrong-report-hash.json.bak"
if parse_sanitized_report \
  "$CONTRACT_ROOT/wrong-report-hash.json" "$BACKUP_ID" "$MANIFEST_SHA256"; then
  fail 'report with an invalid canonical hash was accepted'
fi

cp "$REPORT_FILE" "$CONTRACT_ROOT/extra-field.json"
sed -i.bak 's/}$/,"secret":"forbidden"}$/' \
  "$CONTRACT_ROOT/extra-field.json"
rm -f "$CONTRACT_ROOT/extra-field.json.bak"
if parse_sanitized_report \
  "$CONTRACT_ROOT/extra-field.json" "$BACKUP_ID" "$MANIFEST_SHA256"; then
  fail 'report with an unknown field was accepted'
fi

SECRET_FILE="$CONTRACT_ROOT/single-line.secret"
CLEAN_ARTIFACT="$CONTRACT_ROOT/clean.log"
LEAKED_ARTIFACT="$CONTRACT_ROOT/leaked.log"
printf 'phase5-live-secret-marker\n' >"$SECRET_FILE"
printf 'sanitized\n' >"$CLEAN_ARTIFACT"
printf 'leaked phase5-live-secret-marker\n' >"$LEAKED_ARTIFACT"
chmod 0400 "$SECRET_FILE"
chmod 0600 "$CLEAN_ARTIFACT" "$LEAKED_ARTIFACT"
assert_single_line_secret_absent "$SECRET_FILE" "$CLEAN_ARTIFACT" ||
  fail 'clean artifact was rejected by secret scanner'
if assert_single_line_secret_absent "$SECRET_FILE" "$LEAKED_ARTIFACT"; then
  fail 'secret scanner accepted a leaked value'
fi

SAFE_FAILURE_LOG="$CONTRACT_ROOT/safe-failure.log"
UNSAFE_FAILURE_LOG="$CONTRACT_ROOT/unsafe-failure.log"
printf '%s\n' \
  'untrusted diagnostic prefix' \
  'phase5_restore: restore_http_probe_failed' \
  >"$SAFE_FAILURE_LOG"
printf '%s\n' \
  'phase5_restore: bad category /private/secret' \
  >"$UNSAFE_FAILURE_LOG"
[[ "$(extract_restore_failure_category "$SAFE_FAILURE_LOG")" == \
  restore_http_probe_failed ]] ||
  fail 'safe restore failure category was not extracted'
if extract_restore_failure_category "$UNSAFE_FAILURE_LOG" >/dev/null; then
  fail 'unsafe restore failure detail was accepted'
fi

printf 'phase5 restore live contract: PASS\n'
