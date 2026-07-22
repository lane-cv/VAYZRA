#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
sanitizer="$repo_root/scripts/sanitize-e2e-artifacts.sh"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/nested"
cat > "$tmpdir/containers.log" <<'MALICIOUS_LOG'
diagnostics_version=1
container=happylearn_phase3_123_app
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
MALICIOUS_LOG
touch "$tmpdir/nested/trace.zip" "$tmpdir/nested/failure.png" "$tmpdir/nested/video.webm" "$tmpdir/nested/report.html"

bash "$sanitizer" "$tmpdir"
test "$(find "$tmpdir" -type f | wc -l | tr -d ' ')" -eq 1
test -f "$tmpdir/containers.log"
mode="$(stat -f '%Lp' "$tmpdir/containers.log" 2>/dev/null || stat -c '%a' "$tmpdir/containers.log")"
test "$mode" = 600
! grep -Eqi 'private|json-token|json-password|header-secret|header-cookie|header-csrf|csrf-header-secret|colon-password|quoted-secret|equals-token|db-user|db-password|query-token|query-password|redis-password|url-user|url-password|url-token|url-secret|encoded-password|encoded-token|authorization|cookie|x-csrf|bearer|postgres://|redis://|https://' "$tmpdir/containers.log"
while IFS= read -r line; do
  case "$line" in
    diagnostics_version=1|container=happylearn_phase2_[A-Za-z0-9_-]*|container=happylearn_phase3_[A-Za-z0-9_-]*|state_status=created|state_status=running|state_status=paused|state_status=restarting|state_status=removing|state_status=exited|state_status=dead|exit_code=[0-9]*|oom_killed=true|oom_killed=false|log_lines_omitted=[0-9]*) ;;
    *) echo "unexpected sanitizer output: $line" >&2; exit 1 ;;
  esac
done < "$tmpdir/containers.log"
grep -Fq 'log_lines_omitted=' "$tmpdir/containers.log"

echo 'E2E artifact sanitization contract: PASS'
