#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
sanitizer="$repo_root/scripts/sanitize-e2e-artifacts.sh"
publisher="$repo_root/scripts/publish-e2e-diagnostics.sh"
test -x "$publisher"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/nested"

file_mode() {
  local path="$1" mode
  if mode="$(stat -c '%a' "$path" 2>/dev/null)"; then
    printf '%s\n' "$mode"
  else
    stat -f '%Lp' "$path"
  fi
}

cat > "$tmpdir/containers.log" <<'MALICIOUS_LOG'
diagnostics_version=1
container=happylearn_phase3_123_app
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
MALICIOUS_LOG
touch "$tmpdir/nested/trace.zip" "$tmpdir/nested/failure.png" "$tmpdir/nested/video.webm" "$tmpdir/nested/report.html"

bash "$sanitizer" "$tmpdir"
test "$(find "$tmpdir" -type f | wc -l | tr -d ' ')" -eq 1
test -f "$tmpdir/containers.log"
mode="$(file_mode "$tmpdir/containers.log")"
test "$mode" = 600
! grep -Eqi 'private|json-token|json-password|header-secret|header-cookie|header-csrf|csrf-header-secret|colon-password|quoted-secret|equals-token|db-user|db-password|query-token|query-password|redis-password|url-user|url-password|url-token|url-secret|encoded-password|encoded-token|authorization|cookie|x-csrf|bearer|postgres://|redis://|https://|master-key-material|provider-key-material|provider-ciphertext-material|synthetic-prompt|synthetic-message|synthetic-answer|private-object|object-user|object-password|db-secret|cache-secret' "$tmpdir/containers.log"
while IFS= read -r line; do
  case "$line" in
    diagnostics_version=1|container=happylearn_phase2_[A-Za-z0-9_-]*|container=happylearn_phase3_[A-Za-z0-9_-]*|container=happylearn_phase4_[A-Za-z0-9_-]*|state_status=created|state_status=running|state_status=paused|state_status=restarting|state_status=removing|state_status=exited|state_status=dead|exit_code=[0-9]*|oom_killed=true|oom_killed=false|log_lines_omitted=[0-9]*) ;;
    *) echo "unexpected sanitizer output: $line" >&2; exit 1 ;;
  esac
done < "$tmpdir/containers.log"
grep -Fq 'log_lines_omitted=' "$tmpdir/containers.log"

assert_no_publish_residue() {
  local artifact_dir="$1"
  test ! -e "$artifact_dir/containers.log"
  test -z "$(find "$artifact_dir" -maxdepth 1 -name '.containers.log.*.tmp' -print -quit)"
  ! grep -R -Fq 'raw-private-value' "$artifact_dir" 2>/dev/null
}

exercise_publish() {
  local scenario="$1" publish_dir="$tmpdir/publish-$1" command_dir="$tmpdir/commands-$1" status=0
  mkdir -p "$publish_dir" "$command_dir"
  cp "$tmpdir/containers.log" "$publish_dir/sanitized.log"
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
