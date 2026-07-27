#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
env_example="$repo_root/.env.example"
compose="$repo_root/deploy/compose.dev.yml"
runbook="$repo_root/docs/runbooks/phase4-ai-qanda.md"
local_runbook="$repo_root/docs/runbooks/local-development.md"

test -f "$runbook"

grep -Fq 'HAPPYLEARN_AI_MASTER_KEY=REPLACE_WITH_STANDARD_BASE64_OF_32_RANDOM_BYTES' "$env_example"
for setting in \
  'HAPPYLEARN_AI_MASTER_KEY_VERSION=1' \
  'HAPPYLEARN_AI_BUSINESS_TIMEZONE=Asia/Shanghai' \
  'HAPPYLEARN_AI_GLOBAL_CONCURRENCY=2' \
  'HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY=1' \
  'HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=false'; do
  grep -Fq "$setting" "$env_example"
  grep -Fq "${setting%%=*}:" "$compose"
done

app_block="$(sed -n '/^  app:/,/^  worker:/p' "$compose")"
worker_block="$(sed -n '/^  worker:/,/^networks:/p' "$compose")"
grep -Fq 'HAPPYLEARN_AI_MASTER_KEY:' <<<"$app_block"
! grep -Fq 'HAPPYLEARN_AI_' <<<"$worker_block"
grep -Fq 'mem_limit: 256m' <<<"$app_block"
grep -Fq 'cpus: 0.2' <<<"$app_block"
grep -Fq 'internal: true' "$compose"
! grep -Eiq 'fake.?ai|provider.?url' "$compose"

grep -Fq 'openssl rand -base64 32 > .secrets/ai-master-key' "$runbook"
grep -Fq 'chmod 0600 .secrets/ai-master-key' "$runbook"
grep -Fq 'docker stats --no-stream happylearn-dev-app-1 happylearn-dev-worker-1' "$runbook"
grep -Fq '"SELECT status,count(*) FROM ai_runs GROUP BY status ORDER BY status;"' "$runbook"
grep -Fq 'runner_lost' "$runbook"
grep -Fq 'quota_estimation_anomaly' "$runbook"
grep -Fq '逐一把所有学生覆盖项设为“继承”或“停用”' "$runbook"
grep -Fq 'Phase 3' "$runbook"
grep -Fq 'phase4-ai-qanda.md' "$local_runbook"

# Diagnostic SQL may select only UUIDs, timestamps, status/category fields and
# aggregate counts. These content-bearing or secret-bearing columns are never
# valid operands in an operational SELECT.
! rg -n -P 'SELECT(?:(?! FROM ).)*(message|prompt|_key|key_|body|payload|object|url)' "$runbook"

echo 'phase 4 operations documentation contract: PASS'
