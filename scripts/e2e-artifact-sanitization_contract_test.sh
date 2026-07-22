#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
sanitizer="$repo_root/scripts/sanitize-e2e-artifacts.sh"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/nested"
printf '%s\n' 'password=hunter2 token=abc cookie=session authorization=bearer body=student-private-content postgres://user:pass@db redis://:pass@redis' > "$tmpdir/containers.log"
touch "$tmpdir/nested/trace.zip" "$tmpdir/nested/failure.png" "$tmpdir/nested/video.webm" "$tmpdir/nested/report.html"

bash "$sanitizer" "$tmpdir"
test "$(find "$tmpdir" -type f | wc -l | tr -d ' ')" -eq 1
test -f "$tmpdir/containers.log"
! grep -Eqi 'hunter2|abc|session|bearer|student-private-content|user:pass|:pass@' "$tmpdir/containers.log"
grep -Fq 'password=REDACTED' "$tmpdir/containers.log"
grep -Fq 'body=REDACTED' "$tmpdir/containers.log"

echo 'E2E artifact sanitization contract: PASS'
