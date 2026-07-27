#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
env_example="$repo_root/.env.example"
compose="$repo_root/deploy/compose.dev.yml"
runbook="$repo_root/docs/runbooks/phase4-ai-qanda.md"
local_runbook="$repo_root/docs/runbooks/local-development.md"
operations="$repo_root/scripts/phase4-ai-operations.sh"

test -f "$runbook"
test -x "$operations"

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
grep -Fq 'scripts/phase4-ai-operations.sh write-env .secrets/ai-master-key .secrets/ai.env' "$runbook"
grep -Fq -- '--env-file .env --env-file .secrets/ai.env' "$runbook"
! grep -Fq 'install -m 0600 /dev/null .env' "$runbook"
grep -Fq 'scripts/phase4-ai-operations.sh verify-secret-absence' "$runbook"
grep -Fq 'scripts/phase4-ai-operations.sh run-with-env .env .secrets/ai.env go run ./cmd/server' "$local_runbook"
grep -Fq 'scripts/phase4-ai-operations.sh run-with-env .env .secrets/ai.env go run ./cmd/admin' "$local_runbook"
! grep -Fq 'set -a; . ./.env; set +a' "$local_runbook"
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

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/bin"
printf '%s\n' \
  'HAPPYLEARN_EXISTING_SENTINEL=preserved' \
  'HAPPYLEARN_AI_MASTER_KEY=base-fallback-marker' \
  'HAPPYLEARN_AI_MASTER_KEY_VERSION=9' \
  'HAPPYLEARN_AI_GLOBAL_CONCURRENCY=7' > "$tmpdir/base.env"
printf '%s\n' 'old-ai-env-sentinel' > "$tmpdir/ai.env"
printf '%s\n' 'MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=' > "$tmpdir/master-key"
printf '%s\n' 'synthetic-provider-secret' > "$tmpdir/provider-key"
printf '%s\n' 'synthetic-cookie' > "$tmpdir/admin.cookies"
chmod 0600 "$tmpdir"/*.env "$tmpdir/master-key" "$tmpdir/provider-key" "$tmpdir/admin.cookies"

"$operations" write-env "$tmpdir/master-key" "$tmpdir/ai.env"
grep -Fq 'HAPPYLEARN_EXISTING_SENTINEL=preserved' "$tmpdir/base.env"
grep -Fq 'HAPPYLEARN_AI_MASTER_KEY=' "$tmpdir/ai.env"
! grep -Fq 'old-ai-env-sentinel' "$tmpdir/ai.env"
test "$(stat -f '%Lp' "$tmpdir/ai.env" 2>/dev/null || stat -c '%a' "$tmpdir/ai.env")" = 600
cat > "$tmpdir/compose.yml" <<'COMPOSE_PROBE'
services:
  probe:
    image: busybox
    environment:
      HAPPYLEARN_AI_GLOBAL_CONCURRENCY: ${HAPPYLEARN_AI_GLOBAL_CONCURRENCY}
COMPOSE_PROBE
docker compose --env-file "$tmpdir/base.env" --env-file "$tmpdir/ai.env" \
  -f "$tmpdir/compose.yml" config > "$tmpdir/compose.resolved"
grep -Eq 'HAPPYLEARN_AI_GLOBAL_CONCURRENCY: "?2"?' "$tmpdir/compose.resolved"

printf '%s\n' 'old-ai-env-sentinel' > "$tmpdir/ai.env"
printf '%s\n' 'invalid-key' > "$tmpdir/invalid-key"
if "$operations" write-env "$tmpdir/invalid-key" "$tmpdir/ai.env" >"$tmpdir/write.stdout" 2>"$tmpdir/write.stderr"; then
  echo 'invalid key unexpectedly replaced AI environment' >&2
  exit 1
fi
grep -Fxq 'old-ai-env-sentinel' "$tmpdir/ai.env"
! grep -Fq 'invalid-key' "$tmpdir/write.stdout" "$tmpdir/write.stderr"

"$operations" write-env "$tmpdir/master-key" "$tmpdir/ai.env"
"$operations" run-with-env "$tmpdir/base.env" "$tmpdir/ai.env" \
  bash -c '
    [[ "$HAPPYLEARN_EXISTING_SENTINEL" == preserved ]]
    [[ "$HAPPYLEARN_AI_MASTER_KEY" != base-fallback-marker ]]
    [[ "$HAPPYLEARN_AI_MASTER_KEY_VERSION" == 1 ]]
    [[ "$HAPPYLEARN_AI_GLOBAL_CONCURRENCY" == 2 ]]
    printf "%s\n" host-env-ok > "$1"
  ' _ "$tmpdir/host-env-result"
grep -Fxq 'host-env-ok' "$tmpdir/host-env-result"
if "$operations" run-with-env "$tmpdir/base.env" "$tmpdir/missing-ai.env" \
  bash -c 'touch "$1"' _ "$tmpdir/missing-ai-command-ran" \
  >"$tmpdir/missing.stdout" 2>"$tmpdir/missing.stderr"; then
  echo 'missing AI environment unexpectedly allowed host command' >&2
  exit 1
fi
test ! -e "$tmpdir/missing-ai-command-ran"
! grep -Fq 'base-fallback-marker' "$tmpdir/missing.stdout" "$tmpdir/missing.stderr"

cat > "$tmpdir/bin/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
if [[ "${PHASE4_FAKE_CURL_MODE:-ok}" == fail ]]; then
  printf '%s\n' 'private-api-failure-body'
  exit 22
fi
printf '%s\n' '{"data":[{"hasKey":true}]}'
FAKE_CURL
cat > "$tmpdir/bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
if [[ "${PHASE4_FAKE_DOCKER_MODE:-ok}" == fail ]]; then
  printf '%s\n' 'private-log-failure-body'
  exit 71
fi
printf '%s\n' 'safe-category-only-log'
FAKE_DOCKER
chmod +x "$tmpdir/bin/curl" "$tmpdir/bin/docker"

assert_producer_failure() {
  local producer="$1" expected_exit="$2" verify_tmp private_body command_exit=0
  verify_tmp="$tmpdir/verify-$producer"
  if [[ "$producer" == curl ]]; then
    private_body='private-api-failure-body'
  else
    private_body='private-log-failure-body'
  fi
  mkdir -m 0700 "$verify_tmp"
  PATH="$tmpdir/bin:$PATH" TMPDIR="$verify_tmp" \
    PHASE4_FAKE_CURL_MODE="$([[ "$producer" == curl ]] && echo fail || echo ok)" \
    PHASE4_FAKE_DOCKER_MODE="$([[ "$producer" == docker ]] && echo fail || echo ok)" \
    "$operations" verify-secret-absence "$tmpdir/provider-key" "$tmpdir/admin.cookies" \
      >"$tmpdir/$producer.stdout" 2>"$tmpdir/$producer.stderr" || command_exit=$?
  test "$command_exit" -eq "$expected_exit"
  ! grep -Fq 'synthetic-provider-secret' "$tmpdir/$producer.stdout" "$tmpdir/$producer.stderr"
  ! grep -Fq "$private_body" "$tmpdir/$producer.stdout" "$tmpdir/$producer.stderr"
  test -z "$(find "$verify_tmp" -mindepth 1 -print -quit)"
}

assert_producer_failure curl 1
assert_producer_failure docker 1
mkdir -m 0700 "$tmpdir/verify-success"
PATH="$tmpdir/bin:$PATH" TMPDIR="$tmpdir/verify-success" \
  "$operations" verify-secret-absence "$tmpdir/provider-key" "$tmpdir/admin.cookies"

echo 'phase 4 operations documentation contract: PASS'
