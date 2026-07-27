#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
base="$repo_root/deploy/compose.dev.yml"
ci="$repo_root/deploy/compose.ci.yml"
workflow="$repo_root/.github/workflows/verify.yml"
compose_args='-f deploy/compose.dev.yml -f deploy/compose.ci.yml'

grep -Fq 'internal: true' "$base"
test -f "$ci"
grep -Fq 'internal: false' "$ci"
test "$(grep -Fc -- "$compose_args" "$workflow")" -ge 3
grep -Fq 'Verify host integration ports' "$workflow"
for port in 54329 56379 59000; do
  grep -Fq "$port" "$workflow"
done

echo 'CI Compose host-port contract: PASS'
