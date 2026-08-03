#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
sanitizer="$repo_root/scripts/sanitize-e2e-artifacts.sh"
publisher="$repo_root/scripts/publish-e2e-diagnostics.sh"
test -x "$publisher"
tmpdir="$(mktemp -d)"
tmpdir="$(cd "$tmpdir" && pwd -P)"
external_root="$(mktemp -d)"
external_root="$(cd "$external_root" && pwd -P)"
trap 'rm -rf "$tmpdir" "$external_root"' EXIT
safe_dir="$tmpdir/safe"
unsafe_dir="$tmpdir/phase5-unsafe"
legacy_unsafe_dir="$tmpdir/legacy-unsafe"
phase5_safe_dir="$tmpdir/phase5-safe"
source_evidence="$tmpdir/source-evidence"
unsafe_publish_dir="$tmpdir/unsafe-publish"
mkdir -p "$safe_dir/nested" "$unsafe_dir/nested" "$legacy_unsafe_dir" \
  "$phase5_safe_dir" "$source_evidence" "$unsafe_publish_dir"

file_mode() {
  local path="$1" mode
  if mode="$(stat -c '%a' "$path" 2>/dev/null)"; then
    printf '%s\n' "$mode"
  else
    stat -f '%Lp' "$path"
  fi
}

safety_bin="$tmpdir/safety-bin"
mkdir -p "$safety_bin"
cat > "$safety_bin/find" <<'SAFETY_FIND'
#!/bin/sh
touch "${SANITIZER_FIND_CALLED:?}"
exit 97
SAFETY_FIND
chmod +x "$safety_bin/find"

assert_rejected_without_find() {
  local artifact_dir="$1" marker="$2" status=0
  rm -f "$marker"
  PATH="$safety_bin:$PATH" SANITIZER_FIND_CALLED="$marker" \
    bash "$sanitizer" "$artifact_dir" || status=$?
  test "$status" -ne 0
  test ! -e "$marker"
}

assert_rejected_without_find / "$tmpdir/root-find-called"
mkdir -p "$tmpdir/relative-artifacts"
(
  cd "$tmpdir"
  assert_rejected_without_find relative-artifacts "$tmpdir/relative-find-called"
)

mkdir -p "$external_root/final-target"
printf '%s\n' final-symlink-sentinel > "$external_root/final-target/sentinel"
ln -s "$external_root/final-target" "$tmpdir/final-link"
final_symlink_status=0
bash "$sanitizer" "$tmpdir/final-link" || final_symlink_status=$?
test "$final_symlink_status" -ne 0
grep -Fxq final-symlink-sentinel "$external_root/final-target/sentinel"

mkdir -p "$external_root/ancestor-target/artifacts"
printf '%s\n' ancestor-symlink-sentinel \
  > "$external_root/ancestor-target/artifacts/sentinel"
ln -s "$external_root/ancestor-target" "$tmpdir/ancestor-link"
ancestor_symlink_status=0
bash "$sanitizer" "$tmpdir/ancestor-link/artifacts" \
  || ancestor_symlink_status=$?
test "$ancestor_symlink_status" -ne 0
grep -Fxq ancestor-symlink-sentinel \
  "$external_root/ancestor-target/artifacts/sentinel"

cat > "$safe_dir/containers.log" <<'SAFE_LOG'
diagnostics_version=1
container=happylearn_phase3_123_app
container=happylearn_phase4_123_fake_ai
state_status=running
exit_code=0
oom_killed=false
release_state=failed_safe
release_result=failed
rollback_failure_category=rollback_manifest_validation_failed
unknown_nonsecret_diagnostic=omitted
SAFE_LOG
touch "$safe_dir/nested/trace.zip" "$safe_dir/nested/failure.png" \
  "$safe_dir/nested/video.webm" "$safe_dir/nested/report.html"

bash "$sanitizer" "$safe_dir"
test "$(find "$safe_dir" -type f | wc -l | tr -d ' ')" -eq 1
test -f "$safe_dir/containers.log"
mode="$(file_mode "$safe_dir/containers.log")"
test "$mode" = 600
while IFS= read -r line; do
  case "$line" in
    diagnostics_version=1|container=happylearn_phase2_[A-Za-z0-9_-]*|container=happylearn_phase3_[A-Za-z0-9_-]*|container=happylearn_phase4_[A-Za-z0-9_-]*|container=happylearn_phase5_[A-Za-z0-9_-]*|state_status=created|state_status=running|state_status=paused|state_status=restarting|state_status=removing|state_status=exited|state_status=dead|exit_code=[0-9]*|oom_killed=true|oom_killed=false|release_state=[a-z_]*|release_result=[a-z_]*|rollback_failure_category=[a-z_]*|log_lines_omitted=[0-9]*) ;;
    *) echo "unexpected sanitizer output: $line" >&2; exit 1 ;;
  esac
done < "$safe_dir/containers.log"
grep -Fq 'log_lines_omitted=1' "$safe_dir/containers.log"

cat > "$unsafe_dir/containers.log" <<'PHASE5_MALICIOUS_LOG'
diagnostics_version=1
container=happylearn_phase5_123_backup
state_status=exited
exit_code=1
oom_killed=false
RESTIC_PASSWORD=phase5-restic-password-marker
AGE-SECRET-KEY-1PHASE5AGEIDENTITYMARKER
HAPPYLEARN_WEBHOOK_URL=https://webhook.example.test/hooks/phase5-marker
HAPPYLEARN_HOST_METRICS_HMAC_SECRET=phase5-hmac-token-marker
RESTIC_REPOSITORY=/var/lib/happylearn/backup/repository
PGDMP phase5-postgresql-custom-dump-signature
object_key=happylearn-originals/students/phase5-private-object
student_content=phase5-private-student-answer
request_target=/api/v1/admin/operations?access_token=phase5-query-string-marker
PHASE5_MALICIOUS_LOG
touch "$unsafe_dir/nested/trace.zip"
cp "$unsafe_dir/containers.log" "$source_evidence/backup-evidence.log"
source_evidence_hash="$(
  shasum -a 256 "$source_evidence/backup-evidence.log" | awk '{print $1}'
)"

cat > "$legacy_unsafe_dir/containers.log" <<'LEGACY_MALICIOUS_LOG'
diagnostics_version=1
container=happylearn_phase4_123_fake_ai
state_status=running
exit_code=0
oom_killed=false
body=first private line
second private body line
{"token":"json-token","password":"json-password","body":"json body"}
Authorization: Bearer header-secret
Cookie: session=header-cookie; csrf=header-csrf
X-CSRF-Token: csrf-header-secret
password: "colon-password"
secret='quoted-secret' token=equals-token
postgres://db-user:db-password@db/private?token=query-token&password=query-password
redis://:redis-password@redis/0
https://url-user:url-password@example.test/path?access_token=url-token&secret=url-secret
password%3Dencoded-password%26token%3Dencoded-token
HAPPYLEARN_AI_MASTER_KEY=master-key-material
provider_key=provider-key-material
encrypted_api_key=provider-ciphertext-material
prompt_text=[case:success] synthetic-prompt-content
message_text=synthetic-message-content
answer_text=synthetic-answer-content
object_key=ai-questions/student/private-object
MINIO_ROOT_USER=object-user
MINIO_ROOT_PASSWORD=object-password
HAPPYLEARN_DATABASE_URL=postgres://ai:db-secret@postgres/private
HAPPYLEARN_REDIS_URL=redis://:cache-secret@redis/0
LEGACY_MALICIOUS_LOG

assert_sanitizer_rejects() {
  local artifact_dir="$1" status=0
  bash "$sanitizer" "$artifact_dir" || status=$?
  test "$status" -ne 0
  test -z "$(find "$artifact_dir" -mindepth 1 -print -quit)"
}

phase5_marker_root="$tmpdir/phase5-markers"
mkdir -p "$phase5_marker_root"
while IFS='|' read -r marker_name marker_value; do
  marker_dir="$phase5_marker_root/$marker_name"
  mkdir -p "$marker_dir"
  printf '%s\n' \
    'diagnostics_version=1' \
    'container=happylearn_phase5_123_backup' \
    'state_status=exited' \
    'exit_code=1' \
    'oom_killed=false' \
    "$marker_value" > "$marker_dir/containers.log"
  assert_sanitizer_rejects "$marker_dir"
done <<'PHASE5_MARKERS'
restic-password|RESTIC_PASSWORD=phase5-restic-password-marker
age-identity|AGE-SECRET-KEY-1PHASE5AGEIDENTITYMARKER
webhook-url|HAPPYLEARN_WEBHOOK_URL=https://webhook.example.test/hooks/phase5-marker
hmac-token|HAPPYLEARN_HOST_METRICS_HMAC_SECRET=phase5-hmac-token-marker
repository-path|RESTIC_REPOSITORY=/var/lib/happylearn/backup/repository
postgres-dump|PGDMP phase5-postgresql-custom-dump-signature
object-key|object_key=happylearn-originals/students/phase5-private-object
student-content|student_content=phase5-private-student-answer
query-string|request_target=/api/v1/admin/operations?access_token=phase5-query-string-marker
PHASE5_MARKERS

assert_sanitizer_rejects "$unsafe_dir"
assert_sanitizer_rejects "$legacy_unsafe_dir"

legacy_marker_root="$tmpdir/legacy-markers"
mkdir -p "$legacy_marker_root"
while IFS='|' read -r marker_name marker_value; do
  marker_dir="$legacy_marker_root/$marker_name"
  mkdir -p "$marker_dir"
  printf '%s\n' \
    'diagnostics_version=1' \
    'container=happylearn_phase4_123_fake_ai' \
    'state_status=exited' \
    'exit_code=1' \
    'oom_killed=false' \
    "$marker_value" > "$marker_dir/containers.log"
  assert_sanitizer_rejects "$marker_dir"
done <<'LEGACY_MARKERS'
password|password=legacy-password-marker
token|token=legacy-token-marker
authorization|Authorization: Bearer legacy-auth-marker
cookie|Cookie: session=legacy-cookie-marker
database-url|HAPPYLEARN_DATABASE_URL=postgres://user:legacy-db-password@postgres/private
body|body=legacy-private-body-marker
url-encoded-token|token%3Dlegacy-encoded-token-marker
LEGACY_MARKERS

test "$source_evidence_hash" = "$(
  shasum -a 256 "$source_evidence/backup-evidence.log" | awk '{print $1}'
)"

cat > "$phase5_safe_dir/containers.log" <<'PHASE5_SAFE_LOG'
diagnostics_version=1
container=happylearn_phase5_123_backup
state_status=exited
exit_code=1
oom_killed=false
PHASE5_SAFE_LOG
bash "$sanitizer" "$phase5_safe_dir"
grep -Fxq 'container=happylearn_phase5_123_backup' \
  "$phase5_safe_dir/containers.log"

unsafe_publish_status=0
mkdir -p "$unsafe_dir"
cp "$source_evidence/backup-evidence.log" "$unsafe_dir/containers.log"
if bash "$sanitizer" "$unsafe_dir"; then
  "$publisher" "$unsafe_dir/containers.log" "$unsafe_publish_dir" unsafe
  unsafe_publish_status=$?
else
  unsafe_publish_status=$?
fi
test "$unsafe_publish_status" -ne 0
test -z "$(find "$unsafe_publish_dir" -mindepth 1 -print -quit)"

assert_no_publish_residue() {
  local artifact_dir="$1"
  test ! -e "$artifact_dir/containers.log"
  test -z "$(find "$artifact_dir" -maxdepth 1 -name '.containers.log.*.tmp' -print -quit)"
  ! grep -R -Fq 'raw-private-value' "$artifact_dir" 2>/dev/null
}

exercise_publish() {
  local scenario="$1" publish_dir="$tmpdir/publish-$1" command_dir="$tmpdir/commands-$1" status=0
  mkdir -p "$publish_dir" "$command_dir"
  cp "$safe_dir/containers.log" "$publish_dir/sanitized.log"
  printf '%s\n' raw-private-value > "$tmpdir/raw-$scenario.log"
  case "$scenario" in
    success) ;;
    install_fail)
      cat > "$command_dir/install" <<'MOCK_INSTALL'
#!/bin/sh
exit 73
MOCK_INSTALL
      chmod +x "$command_dir/install"
      ;;
    mv_fail)
      cat > "$command_dir/install" <<'MOCK_INSTALL'
#!/bin/sh
exec /usr/bin/install "$@"
MOCK_INSTALL
      cat > "$command_dir/mv" <<'MOCK_MV'
#!/bin/sh
exit 74
MOCK_MV
      chmod +x "$command_dir/install" "$command_dir/mv"
      ;;
  esac
  PATH="$command_dir:$PATH" "$publisher" "$publish_dir/sanitized.log" "$publish_dir" "$scenario" || status=$?
  if [[ "$scenario" == success ]]; then
    test "$status" -eq 0
    test -f "$publish_dir/containers.log"
    test ! -e "$publish_dir/.containers.log.$scenario.tmp"
    mode="$(file_mode "$publish_dir/containers.log")"
    test "$mode" = 600
    cmp -s "$publish_dir/sanitized.log" "$publish_dir/containers.log"
  else
    test "$status" -ne 0
    assert_no_publish_residue "$publish_dir"
  fi
}

exercise_publish success
exercise_publish install_fail
exercise_publish mv_fail

echo 'E2E artifact sanitization contract: PASS'
