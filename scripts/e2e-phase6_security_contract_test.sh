#!/usr/bin/env bash
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
target=$root/scripts/e2e-phase6_security.sh
fail() { printf 'phase6 security contract: FAIL: %s\n' "$1" >&2; exit 1; }
[[ -x $target && ! -L $target ]] || fail 'security runner missing or unsafe'
for literal in '--repository' '--live' 'govulncheck' 'pnpm audit --prod' 'trivy fs' 'trivy image' 'HIGH,CRITICAL' 'internal/metrics' 'internal/readiness' '308' '404' 'docker history' 'RepoDigests' 'timeout' 'trap cleanup' 'com.docker.compose.project' 'E2E_PHASE6_CA_FILE'; do
  grep -Fq -- "$literal" "$target" || fail "missing security invariant: $literal"
done
for forbidden in 'set -x' '--privileged' 'docker system prune' 'continue-on-error'; do ! grep -Fq -- "$forbidden" "$target" || fail "unsafe security behavior: $forbidden"; done
# shellcheck disable=SC2016
grep -Fq 'cd "$repo_root"' "$target" || fail 'security scans depend on caller working directory'
bash -n "$target"
printf '%s\n' 'phase6 security contract: PASS'
