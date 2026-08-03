#!/usr/bin/env bash
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
local_doc=$root/docs/runbooks/phase6-local-production-acceptance.md
server_doc=$root/docs/runbooks/phase6-real-server-acceptance.md
review_doc=$root/docs/superpowers/plans/2026-07-28-phase6-final-review.md
fail() { printf 'phase6 docs contract: FAIL: %s\n' "$1" >&2; exit 1; }
[[ -f $local_doc && ! -L $local_doc && -f $server_doc && ! -L $server_doc &&
  -f $review_doc && ! -L $review_doc ]] || fail 'runbooks or final review missing or unsafe'
for literal in prerequisites install regression mobile recovery release rollback restart security resources all failure-matrix '15 项' '16 个持久化状态' '缺失 AIStor 对象' 'test-results/phase6' sanitizer '30 分钟' '零残留' 'Ctrl-C' 'HAPPYLEARN_LOCAL_OBJECTSTORE_SKIP_LIFECYCLE_BOOTSTRAP=true' 'AIStor Free license' 'host-samples' '延迟桶' 'worker 已停止' 'manual backup' 'fail closed'; do grep -Fqi -- "$literal" "$local_doc" || fail "local runbook missing: $literal"; done
for literal in Ubuntu '只读' preflight systemd DNS firewall TLS reboot restore RTO desktop mobile timer alert rollback observation 'v1.0.0-rc.1' 'v1.0.0'; do grep -Fqi -- "$literal" "$server_doc" || fail "server runbook missing: $literal"; done
sentence='Phase 6 repository production-ready; real-server acceptance pending.'
[[ $(grep -Fxc -- "$sentence" "$local_doc") == 1 && $(grep -Fxc -- "$sentence" "$server_doc") == 1 ]] || fail 'exact boundary sentence missing'
grep -Fq '独立' "$server_doc" || fail 'separate approvals missing'
grep -Fq '禁止' "$server_doc" || fail 'early completion prohibition missing'
for heading in 'Scope and commits' 'Specification coverage' 'Correctness and failure handling' \
  'Security and privacy' 'Backup, restore, release, and rollback evidence' \
  'Production topology and resources' 'Browser, mobile, restart, and cleanup evidence' \
  'CI and operator documentation' 'Findings' 'Fixes and re-verification' \
  'Repository-ready result' 'Real-server work still pending'; do
  grep -Fxq "## $heading" "$review_doc" || fail "final review missing heading: $heading"
done
grep -Fq 'No real server, DNS, firewall, public certificate, service installation, reboot, production restore switch, `v1.0.0-rc.1`, or `v1.0.0` action was performed.' "$review_doc" || fail 'final review real-server boundary missing'
printf '%s\n' 'phase6 docs contract: PASS'
