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

write_report_fixture() {
  local path="$1"
  local duration="$2"
  local migration="$3"
  local row_total="$4"
  local checked="$5"
  local missing="$6"
  local unexpected="$7"
  local active="$8"
  local isolation="$9"
  local canonical="$CONTRACT_ROOT/report-input.canonical"
  local report_sha256
  printf '%s\n' \
    'schemaVersion=1' \
    'backupId=11111111-1111-4111-8111-111111111111' \
    'manifestSHA256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
    'verificationReportSHA256=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
    'evidenceSHA256=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' \
    "durationSeconds=$duration" \
    "migrationVersion=$migration" \
    "rowCountTotal=$row_total" \
    "checkedObjectCount=$checked" \
    "missingObjectCount=$missing" \
    "unexpectedObjectCount=$unexpected" \
    "activeSessionCount=$active" \
    "isolation404ProbeCount=$isolation" \
    >"$canonical"
  report_sha256="$(
    portable_sha256_stdin <"$canonical" |
      sed -n '1s/[[:space:]].*$//p'
  )"
  printf '%s\n' \
    "{\"schemaVersion\":1,\"backupId\":\"11111111-1111-4111-8111-111111111111\",\"manifestSHA256\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"verificationReportSHA256\":\"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"evidenceSHA256\":\"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\",\"durationSeconds\":$duration,\"migrationVersion\":$migration,\"rowCountTotal\":$row_total,\"checkedObjectCount\":$checked,\"missingObjectCount\":$missing,\"unexpectedObjectCount\":$unexpected,\"activeSessionCount\":$active,\"isolation404ProbeCount\":$isolation,\"reportSHA256\":\"$report_sha256\"}" \
    >"$path"
  chmod 0600 "$path"
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
  'FROM docker@sha256:be132a9f282288de4afaf63379dff75711fda0147c6b72a9df44e51841402144 AS restore_live_controller' \
  "$CONTROLLER_DOCKERFILE" ||
  fail 'restore controller Docker CLI base is not pinned'
grep -Fq \
  'RUN apk add --no-cache bash=5.3.9-r1 coreutils=9.11-r0 util-linux=2.42.1-r0' \
  "$CONTROLLER_DOCKERFILE" ||
  fail 'restore controller shell/lock tools are not pinned'
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
grep -Fq \
  'FROM golang@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS restore_live_fixture_build' \
  "$CONTROLLER_DOCKERFILE" &&
  grep -Fq 'FROM scratch AS restore_live_fixture' \
    "$CONTROLLER_DOCKERFILE" &&
  grep -Fq 'USER 10004:10004' "$CONTROLLER_DOCKERFILE" &&
  grep -Fq \
    'ENTRYPOINT ["/app/happylearn-restore-live-fixture"]' \
    "$CONTROLLER_DOCKERFILE" ||
  fail 'source-object fixture target is not pinned and non-root'
if grep -Fq 'restore_live_fixture' \
  "$ROOT/deploy/compose.dev.yml" \
  "$ROOT/deploy/compose.ci.yml" \
  "$ROOT/deploy/compose.backup-live.yml"; then
  fail 'test-only source-object fixture entered a Compose artifact'
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
  'checkedObjectCount=2' \
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
  "{\"schemaVersion\":1,\"backupId\":\"$BACKUP_ID\",\"manifestSHA256\":\"$MANIFEST_SHA256\",\"verificationReportSHA256\":\"$VERIFICATION_SHA256\",\"evidenceSHA256\":\"$EVIDENCE_SHA256\",\"durationSeconds\":42,\"migrationVersion\":20,\"rowCountTotal\":3,\"checkedObjectCount\":2,\"missingObjectCount\":0,\"unexpectedObjectCount\":0,\"activeSessionCount\":0,\"isolation404ProbeCount\":2,\"reportSHA256\":\"$REPORT_SHA256\"}" \
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

REVIEW_FAILURES=()

record_review_contract() {
  local finding="$1"
  local check="$2"
  if ! "$check"; then
    REVIEW_FAILURES+=("$finding")
  fi
}

review_source_object_fixture() (
  local calls="$CONTRACT_ROOT/source-fixture.calls"
  local sql="$CONTRACT_ROOT/source-fixture.sql"
  : >"$calls"
  : >"$sql"
  PROJECT='happylearn-phase5-live-111111111111'
  FIXTURE_SUFFIX='111111111111'
  HOST_UID="$(id -u)"
  HOST_GID="$(id -g)"
  FIXTURE_IMAGE='happylearn-restore-live-fixture:phase5-live-111111111111'
  FIXTURE_CONFIG_FILE="$CONTRACT_ROOT/source-fixture.json"
  FIXTURE_ORIGINAL_FILE="$CONTRACT_ROOT/source-original.bin"
  FIXTURE_PREVIEW_FILE="$CONTRACT_ROOT/source-preview.bin"
  FIXTURE_OBJECT_LOG="$CONTRACT_ROOT/source-fixture.log"
  FIXTURE_ORIGINAL_KEY='phase5-restore-live/111111111111/original.bin'
  FIXTURE_PREVIEW_KEY='phase5-restore-live/111111111111/preview.bin'
  FIXTURE_ORIGINAL_SIZE=31
  FIXTURE_PREVIEW_SIZE=30
  FIXTURE_ORIGINAL_SHA256='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  FIXTURE_PREVIEW_SHA256='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
  SOURCE_MINIO_ACCESS_KEY='fixture-access-key'
  SOURCE_MINIO_SECRET_KEY='fixture-secret-key'
  printf '{}\n' >"$FIXTURE_CONFIG_FILE"
  printf 'original\n' >"$FIXTURE_ORIGINAL_FILE"
  printf 'preview\n' >"$FIXTURE_PREVIEW_FILE"
  chmod 0400 \
    "$FIXTURE_CONFIG_FILE" \
    "$FIXTURE_ORIGINAL_FILE" \
    "$FIXTURE_PREVIEW_FILE"
  docker() {
    printf '%s\n' "$*" >>"$calls"
    printf 'phase5_restore_fixture: PASS originalBytes=31 previewBytes=30\n'
  }
  db_query() {
    tee "$sql" >/dev/null
  }
  db_scalar() {
    printf '1|1|1|31|%s|30|%s\n' \
      "$FIXTURE_ORIGINAL_SHA256" \
      "$FIXTURE_PREVIEW_SHA256"
  }
  build_source_object_fixture
  verify_source_object_fixture
  grep -Fq -- '--network happylearn-phase5-live-111111111111_happylearn' \
    "$calls" &&
    grep -Fq -- \
      "src=$FIXTURE_CONFIG_FILE,dst=/run/secrets/restore-live-object-fixture.json,readonly" \
      "$calls" &&
    grep -Fq 'INSERT INTO files' "$sql" &&
    grep -Fq 'INSERT INTO file_versions' "$sql" &&
    grep -Fq 'INSERT INTO file_previews' "$sql" &&
    ! grep -Fq "$SOURCE_MINIO_ACCESS_KEY" "$calls" &&
    ! grep -Fq "$SOURCE_MINIO_SECRET_KEY" "$calls"
)

review_report_semantics() {
  local report="$CONTRACT_ROOT/zero-checked.rehashed.json"
  write_report_fixture "$report" 42 20 3 0 0 0 0 2
  ! parse_sanitized_report "$report" "$BACKUP_ID" "$MANIFEST_SHA256"
}

review_bounded_controller_wait() (
  local status started
  FIXTURE_ROOT="$CONTRACT_ROOT/controller-wait"
  mkdir -m 0700 "$FIXTURE_ROOT" "$FIXTURE_ROOT/control"
  CONTROLLER_WAIT_STATUS_FILE="$FIXTURE_ROOT/control/controller.wait"
  CONTROLLER_WAIT_PID=''
  CONTROLLER_EXIT_STATUS=''
  docker() {
    [[ "$1 $2" == 'container wait' ]] || return 1
    sleep 2
    printf '0\n'
  }
  started=$SECONDS
  status=0
  wait_restore_controller_bounded \
    aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1 ||
    status=$?
  [[ "$status" == 124 &&
    $((SECONDS - started)) -le 2 &&
    -z "$CONTROLLER_WAIT_PID" ]] ||
    return 1

  CONTROLLER_WAIT_STATUS_FILE="$FIXTURE_ROOT/control/controller.wait"
  docker() {
    [[ "$1 $2" == 'container wait' ]] || return 1
    printf '7\n'
  }
  wait_restore_controller_bounded \
    aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2 &&
    [[ "$CONTROLLER_EXIT_STATUS" == 7 &&
      ! -e "$CONTROLLER_WAIT_STATUS_FILE" ]]
)

exercise_cleanup_signal() {
  local count="$1"
  local root="$CONTRACT_ROOT/signal-$count"
  local pid index
  mkdir -m 0700 "$root"
  (
    CLEANED=false
    PROJECT=''
    FIXTURE_ROOT=''
    remove_restore_controller() {
      : >"$root/started"
      sleep 1
    }
    remove_created_images() {
      : >"$root/finished"
    }
    cleanup_live 0
    : >"$root/complete"
  ) >/dev/null 2>&1 &
  pid=$!
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    [[ -e "$root/started" ]] && break
    sleep 0.05
  done
  [[ -e "$root/started" ]] || {
    wait "$pid" 2>/dev/null || true
    return 1
  }
  for ((index = 0; index < count; index++)); do
    kill -TERM "$pid" 2>/dev/null || true
    sleep 0.05
  done
  wait "$pid" 2>/dev/null || true
  [[ -e "$root/finished" && -e "$root/complete" ]]
}

review_cleanup_signals() {
  exercise_cleanup_signal 1 && exercise_cleanup_signal 2
}

review_license_source_contract() (
  declare -F valid_license_source >/dev/null || return 1
  local valid="$CONTRACT_ROOT/license.valid"
  local valid600="$CONTRACT_ROOT/license.valid-600"
  local bad_mode="$CONTRACT_ROOT/license.bad-mode"
  local symlink="$CONTRACT_ROOT/license.link"
  local oversized="$CONTRACT_ROOT/license.oversized"
  printf 'license\n' >"$valid"
  cp "$valid" "$valid600"
  cp "$valid" "$bad_mode"
  cp "$valid" "$oversized"
  chmod 0400 "$valid"
  chmod 0600 "$valid600"
  chmod 0644 "$bad_mode"
  dd if=/dev/zero of="$oversized" bs=65537 count=1 \
    >/dev/null 2>&1
  chmod 0600 "$oversized"
  ln -s "$valid" "$symlink"
  valid_license_source "$valid" &&
    valid_license_source "$valid600" &&
    ! valid_license_source "$bad_mode" &&
    ! valid_license_source "$symlink" &&
    ! valid_license_source "$oversized" ||
    return 1
  portable_owner() {
    printf '99999\n'
  }
  ! valid_license_source "$valid"
)

review_graceful_restore_cleanup() (
  local calls="$CONTRACT_ROOT/controller-cleanup.calls"
  local controller_id
  controller_id="$(printf 'a%.0s' {1..64})"
  PROJECT='happylearn-phase5-live-111111111111'
  FIXTURE_SUFFIX='111111111111'
  CONTROLLER_NAME="${PROJECT}-restore-controller"
  CONTROLLER_ID=''
  CONTROLLER_CREATED=false
  CONTROLLER_WAIT_PID=''
  CONTROLLER_WAIT_STATUS_FILE=''
  local exists=true running=true stop_fails=false wrong_label=false
  : >"$calls"
  docker() {
    printf '%s\n' "$*" >>"$calls"
    if [[ "$1 $2" == 'container inspect' ]]; then
      [[ "$exists" == true ]] || return 1
      if [[ "$3" == --format && "$4" == *'.State.Running'* ]]; then
        printf '%s\n' "$running"
      elif [[ "$3" == --format ]]; then
        if [[ "$wrong_label" == true ]]; then
          printf '%s|/%s|wrong-project|%s\n' \
            "$controller_id" "$CONTROLLER_NAME" "$FIXTURE_SUFFIX"
        else
          printf '%s|/%s|%s|%s\n' \
            "$controller_id" "$CONTROLLER_NAME" \
            "$PROJECT" "$FIXTURE_SUFFIX"
        fi
      fi
      return 0
    fi
    if [[ "$1 $2" == 'container stop' ]]; then
      [[ "$stop_fails" == false ]] || return 1
      running=false
      return 0
    fi
    if [[ "$1 $2" == 'container rm' ]]; then
      exists=false
      running=false
      return 0
    fi
    return 1
  }
  remove_restore_controller
  grep -Fq \
    "container stop --signal TERM --timeout 30 $controller_id" "$calls" &&
    grep -Fq "container rm $controller_id" "$calls" &&
    ! grep -Fq "container rm --force $controller_id" "$calls" ||
    return 1

  : >"$calls"
  exists=true
  running=true
  stop_fails=true
  CONTROLLER_ID=''
  CONTROLLER_CREATED=false
  remove_restore_controller
  grep -Fq \
    "container stop --signal TERM --timeout 30 $controller_id" "$calls" &&
    grep -Fq "container rm --force $controller_id" "$calls" ||
    return 1

  : >"$calls"
  exists=true
  running=true
  stop_fails=false
  wrong_label=true
  CONTROLLER_ID=''
  CONTROLLER_CREATED=false
  ! remove_restore_controller &&
    ! grep -Eq 'container (stop|rm)' "$calls" ||
    return 1
  review_owned_restore_resource_cleanup
)

review_owned_restore_resource_cleanup() (
  local calls="$CONTRACT_ROOT/restore-resource-cleanup.calls"
  local fixture_owner project container_name volume_name network_name
  local container_id network_id
  fixture_owner="$(printf 'e%.0s' {1..64})"
  project="happylearn-phase5-restore-${fixture_owner:0:12}"
  container_name="$project-app"
  volume_name="$project-postgres"
  network_name="$project-network"
  container_id="$(printf 'f%.0s' {1..64})"
  network_id="$(printf '9%.0s' {1..64})"
  BACKUP_ID='11111111-1111-4111-8111-111111111111'
  local container_exists=true volume_exists=true network_exists=true
  local wrong_kind=false
  : >"$calls"
  docker() {
    printf '%s\n' "$*" >>"$calls"
    case "$1 $2" in
      'container ls')
        [[ "$container_exists" == false ]] ||
          printf '%s\n' "$container_name"
        ;;
      'container inspect')
        [[ "$container_exists" == true ]] || return 1
        if [[ "$wrong_kind" == true ]]; then
          printf '%s|/%s|%s|%s|wrong-kind|%s\n' \
            "$container_id" "$container_name" "$project" \
            "$fixture_owner" "$BACKUP_ID"
        else
          printf '%s|/%s|%s|%s|app|%s\n' \
            "$container_id" "$container_name" "$project" \
            "$fixture_owner" "$BACKUP_ID"
        fi
        ;;
      'container rm')
        [[ "$3" == --force && "$4" == "$container_id" ]] || return 1
        container_exists=false
        ;;
      'volume ls')
        [[ "$volume_exists" == false ]] || printf '%s\n' "$volume_name"
        ;;
      'volume inspect')
        [[ "$volume_exists" == true ]] || return 1
        printf '%s|%s|%s|%s|postgres|%s\n' \
          "$volume_name" "$volume_name" "$project" \
          "$fixture_owner" "$BACKUP_ID"
        ;;
      'volume rm')
        [[ "$3" == "$volume_name" ]] || return 1
        volume_exists=false
        ;;
      'network ls')
        [[ "$network_exists" == false ]] || printf '%s\n' "$network_name"
        ;;
      'network inspect')
        [[ "$network_exists" == true ]] || return 1
        printf '%s|%s|%s|%s|network|%s\n' \
          "$network_id" "$network_name" "$project" \
          "$fixture_owner" "$BACKUP_ID"
        ;;
      'network rm')
        [[ "$3" == "$network_id" ]] || return 1
        network_exists=false
        ;;
      'ps -aq')
        [[ "$container_exists" == false ]] || printf '%s\n' "$container_id"
        ;;
      *) return 1 ;;
    esac
  }
  cleanup_owned_restore_resources || return 1
  [[ "$container_exists" == false &&
    "$volume_exists" == false &&
    "$network_exists" == false ]] ||
    return 1
  grep -Fq "container rm --force $container_id" "$calls" &&
    grep -Fq "volume rm $volume_name" "$calls" &&
    grep -Fq "network rm $network_id" "$calls" ||
    return 1

  : >"$calls"
  container_exists=true
  volume_exists=false
  network_exists=false
  wrong_kind=true
  ! cleanup_owned_restore_resources &&
    ! grep -Fq 'container rm' "$calls"
)

review_image_build_registration() (
  declare -F pre_register_image_references >/dev/null || return 1
  local calls="$CONTRACT_ROOT/image-registration.calls"
  local image_id
  image_id="sha256:$(printf 'b%.0s' {1..64})"
  : >"$calls"
  CREATED_IMAGE_RECORD="$CONTRACT_ROOT/image-registration.record"
  : >"$CREATED_IMAGE_RECORD"
  chmod 0600 "$CREATED_IMAGE_RECORD"
  FIXTURE_ROOT="$CONTRACT_ROOT/image-registration"
  mkdir -m 0700 "$FIXTURE_ROOT" "$FIXTURE_ROOT/control"
  BACKUP_IMAGE='happylearn-backup:phase5-restore-live-111111111111'
  APP_IMAGE='happylearn-phase5-live-111111111111-app'
  WORKER_IMAGE='happylearn-phase5-live-111111111111-worker'
  CONTROLLER_IMAGE='happylearn-restore-controller:phase5-live-111111111111'
  FIXTURE_IMAGE='happylearn-restore-live-fixture:phase5-live-111111111111'
  local current_reference='' current_id=''
  local list_fails=false
  docker() {
    printf 'docker %s\n' "$*" >>"$calls"
    if [[ "$1" == image && "$2" == inspect ]]; then
      if [[ "${@: -1}" == "$current_reference" && -n "$current_id" ]]; then
        printf '%s\n' "$current_id"
        return 0
      fi
      return 1
    fi
    if [[ "$1 $2" == 'image ls' ]]; then
      [[ "$list_fails" == false ]]
      return
    fi
    if [[ "$1 $2" == 'image rm' &&
      "$3" == "$current_reference" ]]; then
      current_id=''
      return 0
    fi
  }
  pre_register_image_references || return 1
  [[ "$(wc -l <"$CREATED_IMAGE_RECORD" | tr -d '[:space:]')" == 5 ]] ||
    return 1
  current_reference="$BACKUP_IMAGE"
  current_id="$image_id"
  remove_created_images || return 1
  [[ -z "$current_id" ]] || return 1

  : >"$CREATED_IMAGE_RECORD"
  : >"$calls"
  current_reference="$BACKUP_IMAGE"
  current_id="$image_id"
  pre_register_image_references || return 1
  remove_created_images || return 1
  [[ "$current_id" == "$image_id" ]] &&
    ! grep -Fq "image rm $BACKUP_IMAGE" "$calls" ||
    return 1

  : >"$CREATED_IMAGE_RECORD"
  : >"$calls"
  current_reference=''
  current_id=''
  pre_register_image_references || return 1
  current_reference="$BACKUP_IMAGE"
  current_id="$image_id"
  record_image "$BACKUP_IMAGE" || return 1
  : >"$calls"
  current_id="sha256:$(printf '8%.0s' {1..64})"
  ! remove_created_images &&
    ! grep -Fq "image rm $BACKUP_IMAGE" "$calls" ||
    return 1

  : >"$CREATED_IMAGE_RECORD"
  current_reference=''
  current_id=''
  list_fails=true
  ! pre_register_image_references
)

review_repository_cleanup_handoff() (
  local calls="$CONTRACT_ROOT/repository-cleanup.calls"
  local image_id repository hardlink special
  image_id="sha256:$(printf 'c%.0s' {1..64})"
  FIXTURE_ROOT="$(mktemp -d \
    "${TMPDIR:-/tmp}/phase5-restore-live.XXXXXX")"
  trap 'chmod -R u+rwX "$FIXTURE_ROOT" 2>/dev/null || true
    rm -rf "$FIXTURE_ROOT"' EXIT
  repository="$FIXTURE_ROOT/repository"
  mkdir -m 0700 "$repository"
  printf 'repository fixture\n' >"$repository/data"
  chmod 0600 "$repository/data"
  PROJECT='happylearn-phase5-live-111111111111'
  FIXTURE_SUFFIX='111111111111'
  HOST_UID="$(id -u)"
  HOST_GID="$(id -g)"
  BACKUP_IMAGE='happylearn-backup:phase5-restore-live-111111111111'
  CREATED_IMAGE_RECORD="$FIXTURE_ROOT/created-images"
  printf '%s|absent|%s\n' \
    "$BACKUP_IMAGE" "$image_id" >"$CREATED_IMAGE_RECORD"
  chmod 0600 "$CREATED_IMAGE_RECORD"
  : >"$calls"
  docker() {
    printf '%s\n' "$*" >>"$calls"
    if [[ "$1 $2" == 'image inspect' ]]; then
      printf '%s\n' "$image_id"
      return 0
    fi
    [[ "$1" == run ]]
  }
  REPOSITORY_HOST_HANDOFF=false
  handoff_repository_to_host_for_cleanup || return 1
  [[ "$REPOSITORY_HOST_HANDOFF" == true ]] || return 1
  grep -Fq -- '--cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE' \
    "$calls" &&
    grep -Fq -- '--network none --read-only --user 0:0' "$calls" ||
    return 1

  hardlink="$repository/data.hardlink"
  ln "$repository/data" "$hardlink"
  REPOSITORY_HOST_HANDOFF=false
  ! handoff_repository_to_host_for_cleanup || return 1
  rm -f "$hardlink"
  special="$repository/special"
  mkfifo "$special"
  REPOSITORY_HOST_HANDOFF=false
  ! handoff_repository_to_host_for_cleanup
)

review_controller_create_capture() (
  local controller_id
  controller_id="$(printf 'd%.0s' {1..64})"
  PROJECT='happylearn-phase5-live-111111111111'
  FIXTURE_SUFFIX='111111111111'
  CONTROLLER_NAME="${PROJECT}-restore-controller"
  CONTROLLER_ID=''
  CONTROLLER_CREATED=false
  local daemon_fails=false discover_status=0
  docker() {
    [[ "$daemon_fails" == false ]] || return 1
    [[ "$1 $2 $3" == 'container inspect --format' ]] || return 1
    printf '%s|/%s|%s|%s\n' \
      "$controller_id" "$CONTROLLER_NAME" "$PROJECT" "$FIXTURE_SUFFIX"
  }
  discover_restore_controller &&
    [[ "$CONTROLLER_CREATED" == true &&
      "$CONTROLLER_ID" == "$controller_id" ]] &&
    grep -Fq -- '--cidfile "$CONTROLLER_CID_FILE"' "$TARGET" ||
    return 1
  daemon_fails=true
  CONTROLLER_ID=''
  CONTROLLER_CREATED=false
  discover_restore_controller || discover_status=$?
  [[ "$discover_status" == 1 ]]
)

review_controller_socket_groups() (
  local body groups
  body="$(
    sed -n \
      '/^        controller_supplementary_groups_match() {/,/^        }/p' \
      "$TARGET"
  )"
  [[ -n "$body" ]] || return 1
  eval "$body"
  id() {
    [[ "${1:-}" == -G ]] || return 1
    printf '%s\n' "$groups"
  }
  groups='20 0'
  controller_supplementary_groups_match 20 0 || return 1
  groups='20 0 999'
  ! controller_supplementary_groups_match 20 0 || return 1
  groups='20'
  ! controller_supplementary_groups_match 20 0
)

review_project_fallback_cleanup() (
  local fixture_container_id fixture_container_name fixture_volume_name
  local fixture_network_id fixture_network_name
  fixture_container_id="$(printf '6%.0s' {1..64})"
  fixture_network_id="$(printf '7%.0s' {1..64})"
  PROJECT='happylearn-phase5-live-111111111111'
  FIXTURE_SUFFIX='111111111111'
  fixture_container_name="${PROJECT}-app-1"
  fixture_volume_name="${PROJECT}_minio_data"
  fixture_network_name="${PROJECT}_happylearn"
  local container_exists=true volume_exists=true network_exists=true
  docker() {
    case "$1 $2" in
      'container ls')
        [[ "$container_exists" == false ]] ||
          printf '%s\n' "$fixture_container_id"
        ;;
      'container inspect')
        [[ "$container_exists" == true ]] || return 1
        printf '%s|/%s|%s|app|False||\n' \
          "$fixture_container_id" "$fixture_container_name" "$PROJECT"
        ;;
      'container rm')
        [[ "$3" == --force && "$4" == "$fixture_container_id" ]] ||
          return 1
        container_exists=false
        ;;
      'volume ls')
        [[ "$volume_exists" == false ]] ||
          printf '%s\n' "$fixture_volume_name"
        ;;
      'volume inspect')
        [[ "$volume_exists" == true ]] || return 1
        printf '%s|%s|minio_data\n' "$fixture_volume_name" "$PROJECT"
        ;;
      'volume rm')
        [[ "$3" == "$fixture_volume_name" ]] || return 1
        volume_exists=false
        ;;
      'ps -aq')
        return 0
        ;;
      'network ls')
        [[ "$network_exists" == false ]] ||
          printf '%s\n' "$fixture_network_name"
        ;;
      'network inspect')
        [[ "$network_exists" == true ]] || return 1
        printf '%s|%s|%s|happylearn\n' \
          "$fixture_network_id" "$fixture_network_name" "$PROJECT"
        ;;
      'network rm')
        [[ "$3" == "$fixture_network_id" ]] || return 1
        network_exists=false
        ;;
      *) return 1 ;;
    esac
  }
  remove_labeled_project_containers &&
    remove_labeled_project_volumes &&
    remove_labeled_project_networks &&
    [[ "$container_exists" == false &&
      "$volume_exists" == false &&
      "$network_exists" == false ]]
)

review_dynamic_fault_windows() (
  local root calls cleanup_status=0
  review_controller_socket_groups || return 1
  review_project_fallback_cleanup || return 1
  root="$(mktemp -d \
    "${TMPDIR:-/tmp}/phase5-restore-live.XXXXXX")"
  mkdir -m 0700 "$root/repository"
  calls="$CONTRACT_ROOT/dynamic-cleanup.calls"
  : >"$calls"
  CLEANED=false
  PROJECT='happylearn-phase5-live-111111111111'
  FIXTURE_SUFFIX='111111111111'
  FIXTURE_ROOT="$root"
  REPOSITORY_HOST_HANDOFF=false
  remove_restore_controller() {
    printf 'controller\n' >>"$calls"
  }
  cleanup_owned_restore_resources() {
    printf 'restore-resources\n' >>"$calls"
  }
  handoff_repository_to_host_for_cleanup() {
    printf 'repository-handoff\n' >>"$calls"
    REPOSITORY_HOST_HANDOFF=true
  }
  compose() {
    printf 'compose-partial\n' >>"$calls"
    return 1
  }
  remove_labeled_project_volumes() {
    printf 'project-volumes\n' >>"$calls"
  }
  remove_created_images() {
    printf 'images\n' >>"$calls"
  }
  docker() {
    return 0
  }
  cleanup_live 0 || cleanup_status=$?
  [[ "$cleanup_status" == 1 &&
    ! -e "$root" &&
    "$(sed -n '1p' "$calls")" == controller &&
    "$(sed -n '2p' "$calls")" == restore-resources &&
    "$(sed -n '3p' "$calls")" == repository-handoff &&
    "$(sed -n '4p' "$calls")" == compose-partial &&
    "$(tail -n1 "$calls")" == images ]]
)

review_controller_packages_pinned() {
  grep -Eq \
    'apk add --no-cache bash=[^[:space:]]+ coreutils=[^[:space:]]+ util-linux=[^[:space:]]+' \
    "$CONTROLLER_DOCKERFILE"
}

record_review_contract C0_source_objects review_source_object_fixture
record_review_contract I1_report_semantics review_report_semantics
record_review_contract I2_controller_deadline review_bounded_controller_wait
record_review_contract I3_cleanup_signals review_cleanup_signals
record_review_contract I4_license_source review_license_source_contract
record_review_contract I5_restore_cleanup review_graceful_restore_cleanup
record_review_contract I6_image_window review_image_build_registration
record_review_contract I7_repository_cleanup review_repository_cleanup_handoff
record_review_contract I8_controller_capture review_controller_create_capture
record_review_contract I9_dynamic_windows review_dynamic_fault_windows
record_review_contract M1_apk_pinning review_controller_packages_pinned

if [[ "${#REVIEW_FAILURES[@]}" -ne 0 ]]; then
  printf 'phase5 restore live contract: review RED %s\n' \
    "${REVIEW_FAILURES[*]}" >&2
  exit 1
fi

printf 'phase5 restore live contract: PASS\n'
