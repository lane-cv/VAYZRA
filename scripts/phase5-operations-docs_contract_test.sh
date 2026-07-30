#!/usr/bin/env bash
set -Eeuo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
RUNBOOK="$ROOT/docs/runbooks/phase5-operations-backup.md"
MAKEFILE="$ROOT/Makefile"

fail() {
  printf 'phase 5 operations docs contract: %s\n' "$1" >&2
  exit 1
}

require_literal() {
  local file="$1"
  local literal="$2"
  grep -Fq -- "$literal" "$file" ||
    fail "missing literal in ${file#"$ROOT/"}: $literal"
}

require_heading() {
  local heading="$1"
  grep -Fxq "$heading" "$RUNBOOK" ||
    fail "missing exact runbook section: $heading"
}

test -f "$RUNBOOK" || fail 'missing docs/runbooks/phase5-operations-backup.md'

for heading in \
  '## 所有者专用密钥与权限检查' \
  '## 本地与可选 S3 仓库初始化' \
  '## 每日计划与手动备份' \
  '## healthy、degraded 与 failed 判读' \
  '## 告警确认与 Webhook 测试' \
  '## 安全诊断' \
  '## 空环境恢复验证' \
  '## RPO 与 RTO 测量' \
  '## Web UI 禁止破坏性恢复' \
  '## 仓库凭据或 Age 身份丢失' \
  '## 清理、保留与磁盘压力' \
  '## 回滚至 Phase 4 且保留 Phase 5 数据'; do
  require_heading "$heading"
done

# All operator-provided material is visibly distinguished from repository
# examples. Fixed project and storage paths keep every command narrowly scoped.
require_literal "$RUNBOOK" '<OPERATOR_VALUE>'
require_literal "$RUNBOOK" '/var/lib/happylearn/backup/secrets'
require_literal "$RUNBOOK" '/var/lib/happylearn/backup/repository'
require_literal "$RUNBOOK" '/var/lib/happylearn/backup/state'
require_literal "$RUNBOOK" 'install -d -m 0700'
require_literal "$RUNBOOK" 'chmod 0400'
require_literal "$RUNBOOK" "stat -c '%a %U:%G %n'"
require_literal "$RUNBOOK" 'HAPPYLEARN_BACKUP_SECRET_DIRECTORY=/var/lib/happylearn/backup/secrets'
require_literal "$RUNBOOK" 'HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY=/var/lib/happylearn/backup/repository'
require_literal "$RUNBOOK" 'HAPPYLEARN_BACKUP_STATE_DIRECTORY=/var/lib/happylearn/backup/state'
require_literal "$RUNBOOK" 'HAPPYLEARN_BACKUP_AGE_RECIPIENT='
require_literal "$RUNBOOK" 'HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID='
require_literal "$RUNBOOK" 'local_repository'
require_literal "$RUNBOOK" 'local_password'
require_literal "$RUNBOOK" 'remote_repository'
require_literal "$RUNBOOK" 'remote_password'
require_literal "$RUNBOOK" 'remote_access_key_id'
require_literal "$RUNBOOK" 'remote_secret_access_key'
require_literal "$RUNBOOK" 's3:https://<OPERATOR_VALUE>'

require_literal "$RUNBOOK" \
  'scripts/phase5-backup.sh --project happylearn-dev --trigger scheduled'
require_literal "$RUNBOOK" \
  'scripts/phase5-backup.sh --project happylearn-dev --trigger manual'
require_literal "$RUNBOOK" '03:00'
require_literal "$RUNBOOK" 'Asia/Shanghai'
require_literal "$RUNBOOK" '/admin/settings'
require_literal "$RUNBOOK" '/admin/backups'
require_literal "$RUNBOOK" '/admin/alerts'
require_literal "$RUNBOOK" '/api/v1/admin/operations/webhook-test'
require_literal "$RUNBOOK" '确认不会把告警设为 resolved'
require_literal "$RUNBOOK" 'HAPPYLEARN_WEBHOOK_URL_SECRET_FILE'
require_literal "$RUNBOOK" 'HAPPYLEARN_WEBHOOK_AUTHORIZATION_SECRET_FILE'

require_literal "$RUNBOOK" \
  "scripts/phase5-restore-verify.sh --backup-id '<OPERATOR_VALUE>'"
require_literal "$RUNBOOK" 'happylearn-phase5-restore-'
require_literal "$RUNBOOK" 'session_revocation_verified'
require_literal "$RUNBOOK" 'RPO'
require_literal "$RUNBOOK" '24 小时'
require_literal "$RUNBOOK" 'RTO'
require_literal "$RUNBOOK" '4 小时'
require_literal "$RUNBOOK" '教师 Web UI 不提供恢复执行入口'
require_literal "$RUNBOOK" 'Restic 仓库密码'
require_literal "$RUNBOOK" 'Age X25519 identity'
require_literal "$RUNBOOK" 'keep-daily 7'
require_literal "$RUNBOOK" 'keep-daily 30'
require_literal "$RUNBOOK" 'keep-monthly 12'
require_literal "$RUNBOOK" '75%'
require_literal "$RUNBOOK" '90%'
require_literal "$RUNBOOK" '不得执行 down migration'
require_literal "$RUNBOOK" '不得删除 Phase 5'

# Future acceptance commands are named for hand-off, but the runbook must say
# that this Task 5 does not create them.
require_literal "$RUNBOOK" 'HAPPYLEARN_E2E_GROUP=recovery make e2e-phase5'
require_literal "$RUNBOOK" 'scripts/e2e-phase5_failure_matrix.sh'
require_literal "$RUNBOOK" 'Task 5 不创建这些 harness'

# A destructive restore warning must appear immediately before the only
# restore-verification invocation. The verifier itself is constrained to a
# unique empty project, but operators must still treat restore as destructive.
restore_line="$(
  grep -nF "scripts/phase5-restore-verify.sh --backup-id '<OPERATOR_VALUE>'" \
    "$RUNBOOK" | cut -d: -f1
)"
warning_line="$(
  grep -nF '**破坏性恢复警告：**' "$RUNBOOK" | cut -d: -f1
)"
[[ "$restore_line" =~ ^[0-9]+$ && "$warning_line" =~ ^[0-9]+$ ]] ||
  fail 'restore command or destructive warning is not unique'
((warning_line < restore_line && restore_line - warning_line <= 4)) ||
  fail 'destructive warning must immediately precede restore verification'

# Runbooks must not contain broad cleanup, production data mutation, direct
# credentials, or examples that can accidentally target an unrelated project.
if rg -n \
  'rm[[:space:]]+-r|find .*-(delete|exec rm)|docker (system|volume|network) prune|down --volumes|git clean|DROP (DATABASE|TABLE)|TRUNCATE|DELETE FROM|pg_restore .*--clean' \
  "$RUNBOOK"; then
  fail 'runbook contains a broad or destructive automatic command'
fi
if rg -n \
  '(password|secret|authorization|token)[[:space:]]*[:=][[:space:]]*["'\'']?[A-Za-z0-9+/._:-]{12,}' \
  "$RUNBOOK"; then
  fail 'runbook appears to embed a credential value'
fi
if rg -n \
  'docker compose(?!.*(?:-p|--project-name)[[:space:]]+happylearn-dev)' \
  "$RUNBOOK" --pcre2; then
  fail 'docker compose command lacks the exact happylearn-dev project'
fi

# Diagnostic SQL may select state, time, count, category, and boolean evidence
# only. Content- or secret-bearing fields are forbidden in SELECT projections.
if rg -n -P \
  'SELECT(?:(?! FROM ).)*(message|prompt|_key|key_|body|payload|object|url|credential|snapshot_id)' \
  "$RUNBOOK"; then
  fail 'runbook diagnostic SELECT projects forbidden data'
fi

require_literal "$MAKEFILE" 'phase5-operations-docs-contract:'
require_literal "$MAKEFILE" \
  'bash scripts/phase5-operations-docs_contract_test.sh'

printf '%s\n' 'phase 5 operations documentation contract: PASS'
