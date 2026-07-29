#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="$repo_root/scripts/collect-host-metrics.sh"
live_target="$repo_root/scripts/host-metrics_live_test.sh"
makefile="$repo_root/Makefile"
package_json="$repo_root/package.json"

fail() {
  printf 'host metrics contract: FAIL: %s\n' "$1" >&2
  exit 1
}

[[ -f "$target" ]] || fail "collector script is missing"
[[ -x "$target" ]] || fail "collector script is not executable"
[[ -f "$live_target" && -x "$live_target" ]] ||
  fail "live integration gate is missing or not executable"

require_literal() {
  grep -Fq -- "$1" "$target" ||
    fail "missing required literal: $1"
}

forbid_pattern() {
  if grep -Eiq -- "$1" "$target"; then
    fail "forbidden pattern present: $1"
  fi
}

require_literal 'deploy/compose.dev.yml'
require_literal 'deploy/compose.prod.yml'
require_literal 'http://127.0.0.1:9090/internal/host-samples'
require_literal 'HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE'
require_literal 'HAPPYLEARN_BACKUP_HOST_PATH'
require_literal 'docker compose'
require_literal 'ps --format json'
require_literal 'stats --no-stream'
require_literal '--connect-timeout'
require_literal '--max-time'
require_literal '64 * 1024'
require_literal 'caddy app worker postgres redis minio'
require_literal 'postgres-tls-init minio-data-init backup-storage-init backup-secrets-init backup migrate restore acceptance'
require_literal 'host-sampler'
require_literal 'head -c "$((MAX_FILE_BYTES + 1))"'

forbid_pattern '(/var/run/docker\.sock|docker[[:space:]]+inspect)'
forbid_pattern '(^|[[:space:]])(env|printenv)([[:space:]]|$)'
forbid_pattern 'set[[:space:]]+-[^[:space:]]*x'
forbid_pattern 'docker[[:space:]]+(container[[:space:]]+)?logs'
forbid_pattern 'ps[[:space:]]+(aux|-[A-Za-z]*e)'
forbid_pattern '0\.0\.0\.0|host\.docker\.internal'
forbid_pattern 'openssl.*(-hmac|-macopt)'

grep -Fq 'host-metrics-contract:' "$makefile" ||
  fail "Makefile host-metrics-contract target missing"
grep -Fq 'host-metrics-live:' "$makefile" ||
  fail "Makefile host-metrics-live target missing"
grep -Fq '"host-metrics:contract"' "$package_json" ||
  fail "package host-metrics:contract script missing"
grep -Fq '"host-metrics:live"' "$package_json" ||
  fail "package host-metrics:live script missing"
for live_literal in \
  'postgres:18.4' \
  'redis:8.8' \
  'go build -o /out/host-sampler ./cmd/host-sampler' \
  'TestHostSamplerLiveIntegration' \
  'host metrics live: PASS samples=27 nonceKeys=1'; do
  grep -Fq -- "$live_literal" "$live_target" ||
    fail "live gate missing required literal: $live_literal"
done
if grep -Eq '(/var/run/docker\.sock|HOST_SAMPLER_BIN=.*fake|fake.*curl)' \
  "$live_target"; then
  fail "live gate stubs the sampler/endpoint or mounts the Docker socket"
fi

fixture_root="$(mktemp -d "${TMPDIR:-/tmp}/host-metrics-contract.XXXXXX")"
trap 'rm -rf "$fixture_root"' EXIT
fake_bin="$fixture_root/bin"
mkdir -p "$fake_bin"
command_log="$fixture_root/commands.log"
sampler_input="$fixture_root/sampler-input.json"
signed_body="$fixture_root/signed-body.json"
curl_body="$fixture_root/curl-body.json"
curl_headers="$fixture_root/curl-headers.txt"
stream_marker="$fixture_root/oversized-stream-completed"

cat >"$fake_bin/timeout" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
seconds="$1"
shift
printf 'timeout=%s command=%s\n' "$seconds" "$*" >>"$HOST_METRICS_COMMAND_LOG"
exec "$@"
EOF

cat >"$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'docker %s\n' "$*" >>"$HOST_METRICS_COMMAND_LOG"
if [[ "${HOST_METRICS_DOCKER_MODE:-valid}" == "truncated" && "$*" == *" ps --format json"* ]]; then
  printf '[{"ID":"app-id"'
  exit 0
fi
if [[ "${HOST_METRICS_DOCKER_MODE:-valid}" == "oversized" && "$*" == *" ps --format json"* ]]; then
  head -c 10485760 /dev/zero | tr '\0' x
  printf '%s\n' completed >"$HOST_METRICS_STREAM_MARKER"
  exit 0
fi
if [[ "$*" == *" ps --format json"* ]]; then
  case "${HOST_METRICS_DOCKER_MODE:-valid}" in
    dangerous)
      printf '%s\n' '[{"ID":"app-id","Service":"app","State":"running","Health":"healthy","Command":"normal-command","Environment":["PASSWORD=secret"],"Mounts":["/var/run/docker.sock"]}]'
      ;;
    unknown)
      printf '%s\n' '[{"ID":"unknown-id","Service":"customer-prod-db","State":"running","Health":"healthy","Command":"normal-command"}]'
      ;;
    *)
      printf '%s\n' '[{"ID":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","Service":"backup","State":"exited","Health":"","Command":"/app/backup","Publishers":[]},{"ID":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","Service":"redis","State":"exited","Health":"","Command":"redis-server","Publishers":[]},{"ID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Service":"app","State":"running","Health":"healthy","RestartCount":3,"Command":"/app/server","Publishers":[]}]'
      ;;
  esac
  exit 0
fi
if [[ "$*" == *"stats --no-stream "* ]]; then
  case "$*" in
    *aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa*)
      printf '%s\n' '{"CPUPerc":"12.50%","MemUsage":"1.5MiB / 2GiB","Name":"arbitrary-project-app-1","Container":"app-id"}'
      ;;
    *)
      exit 33
      ;;
  esac
  exit 0
fi
exit 34
EOF

cat >"$fake_bin/df" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'df %s\n' "$*" >>"$HOST_METRICS_COMMAND_LOG"
printf '%s\n' 'Filesystem 1024-blocks Used Available Capacity Mounted on'
case "${*: -1}" in
  /)
    printf '%s\n' '/dev/test 100 38 62 38% /'
    ;;
  "$HAPPYLEARN_BACKUP_HOST_PATH")
    printf '%s\n' '/dev/backup 100 75 25 75% /backup'
    ;;
  *)
    exit 36
    ;;
esac
EOF

cat >"$fake_bin/date" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  "-u +%Y-%m-%dT%H:%M:%SZ")
    printf '%s\n' '2026-07-30T04:05:06Z'
    ;;
  "-u +%s")
    printf '%s\n' '1785384306'
    ;;
  *)
    exit 35
    ;;
esac
EOF

cat >"$fake_bin/stat" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${HOST_METRICS_FAKE_BACKUP_OWNER:-}" &&
  $# -eq 3 &&
  "$1" =~ ^-(f|c)$ &&
  "$2" == "%u" &&
  "$3" == "$HAPPYLEARN_BACKUP_HOST_PATH" ]]; then
  printf '%s\n' "$HOST_METRICS_FAKE_BACKUP_OWNER"
  exit 0
fi
exec /usr/bin/stat "$@"
EOF

cat >"$fake_bin/host-sampler" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  payload)
    tee "$HOST_METRICS_SAMPLER_INPUT" >/dev/null
    printf '%s\n' '{"schemaVersion":1,"observedAt":"2026-07-30T04:05:06Z","services":[{"service":"app","up":true,"cpuPercent":12.5,"memoryBytes":1572864,"memoryLimitBytes":2147483648,"restarts":0},{"service":"redis","up":false,"cpuPercent":0,"memoryBytes":0,"memoryLimitBytes":0,"restarts":0}],"filesystems":[{"filesystem":"root","usedPercent":38}]}'
    ;;
  sign)
    [[ "$2" == "--secret-file" && "$4" == "--timestamp" && "$6" == "--nonce" ]] || exit 41
    [[ "$3" == "$HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE" ]] || exit 42
    [[ "$5" =~ ^[0-9]+$ && "$7" =~ ^[0-9a-f]{32}$ ]] || exit 43
    tee "$HOST_METRICS_SIGNED_BODY" >/dev/null
    printf '%s\n' 'sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    ;;
  *)
    exit 44
    ;;
esac
EOF

cat >"$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'curl %s\n' "$*" >>"$HOST_METRICS_COMMAND_LOG"
if [[ "${HOST_METRICS_CURL_MODE:-valid}" == "broken" ]]; then
  exit 7
fi
: >"$HOST_METRICS_CURL_HEADERS"
body_path=
endpoint=
while (($#)); do
  case "$1" in
    -H)
      printf '%s\n' "$2" >>"$HOST_METRICS_CURL_HEADERS"
      shift 2
      ;;
    --data-binary)
      body_path="${2#@}"
      shift 2
      ;;
    http://*)
      endpoint="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done
[[ "$endpoint" == "http://127.0.0.1:9090/internal/host-samples" ]] || exit 51
[[ -n "$body_path" ]] || exit 52
cp "$body_path" "$HOST_METRICS_CURL_BODY"
printf '204'
EOF

chmod 0755 \
  "$fake_bin/timeout" "$fake_bin/docker" "$fake_bin/df" "$fake_bin/date" \
  "$fake_bin/stat" "$fake_bin/host-sampler" "$fake_bin/curl"
secret_file="$fixture_root/host-hmac"
printf '%s\n' 'test-host-secret' >"$secret_file"
chmod 0600 "$secret_file"
backup_path="$fixture_root/backup"
mkdir -m 0700 "$backup_path"
backup_path="$(cd "$backup_path" && pwd -P)"

run_fixture() {
  env \
    PATH="$fake_bin:/usr/bin:/bin" \
    HOST_SAMPLER_BIN="$fake_bin/host-sampler" \
    HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE="$secret_file" \
    HAPPYLEARN_BACKUP_HOST_PATH="${HOST_METRICS_BACKUP_PATH_OVERRIDE:-$backup_path}" \
    HOST_METRICS_COMMAND_LOG="$command_log" \
    HOST_METRICS_SAMPLER_INPUT="$sampler_input" \
    HOST_METRICS_SIGNED_BODY="$signed_body" \
    HOST_METRICS_CURL_BODY="$curl_body" \
    HOST_METRICS_CURL_HEADERS="$curl_headers" \
    HOST_METRICS_STREAM_MARKER="$stream_marker" \
    HOST_METRICS_DOCKER_MODE="${HOST_METRICS_DOCKER_MODE:-valid}" \
    HOST_METRICS_CURL_MODE="${HOST_METRICS_CURL_MODE:-valid}" \
    bash "$target" --environment development
}

: >"$command_log"
run_fixture >/dev/null
if ! HOST_METRICS_FAKE_BACKUP_OWNER=10003 run_fixture >/dev/null 2>&1; then
  fail "fixed production backup owner UID 10003 was rejected"
fi
if HOST_METRICS_FAKE_BACKUP_OWNER=10004 run_fixture >/dev/null 2>&1; then
  fail "unapproved backup owner UID 10004 was accepted"
fi

jq -e '
  .schemaVersion == 1 and
  .observedAt == "2026-07-30T04:05:06Z" and
  .compose == [
    {"service":"backup","state":"exited","health":"","restarts":null},
    {"service":"redis","state":"exited","health":"","restarts":null},
    {"service":"app","state":"running","health":"healthy","restarts":3}
  ] and
  .stats == [
    {"service":"app","cpuPercent":"12.50%","memoryUsage":"1.5MiB / 2GiB"}
  ] and
  .filesystems == [
    {"filesystem":"root","usedPercent":"38%"},
    {"filesystem":"backup","usedPercent":"75%"}
  ]
' "$sampler_input" >/dev/null || fail "sampler did not receive only the selected safe fields"

cmp -s "$signed_body" "$curl_body" ||
  fail "signed bytes differ from submitted bytes"
grep -Fxq 'Content-Type: application/json' "$curl_headers" ||
  fail "content type header missing"
grep -Eq '^X-HL-Timestamp: [0-9]+$' "$curl_headers" ||
  fail "timestamp header missing"
grep -Eq '^X-HL-Nonce: [0-9a-f]{32}$' "$curl_headers" ||
  fail "nonce header missing"
grep -Fxq 'X-HL-Signature: sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "$curl_headers" ||
  fail "signature header missing"
grep -Fq 'timeout=10 command=docker compose -p happylearn-dev -f '"$repo_root"'/deploy/compose.dev.yml ps --format json' "$command_log" ||
  fail "development compose ps is not exact or bounded"
grep -Fq 'stats --no-stream' "$command_log" ||
  fail "bounded docker stats was not used"
grep -Fq 'df -Pk /' "$command_log" ||
  fail "root filesystem was not collected"
grep -Fq "df -Pk $backup_path" "$command_log" ||
  fail "fixed backup filesystem was not collected"

if grep -Eiq '(command|environment|mounts|password|docker\.sock|registry|logs|container|image|project)' "$sampler_input"; then
  fail "sampler input contains Docker-sensitive fields"
fi
if grep -Fq 'test-host-secret' "$command_log" "$sampler_input" "$signed_body" "$curl_body" "$curl_headers"; then
  fail "HMAC secret leaked to commands or artifacts"
fi

for mutation in dangerous unknown truncated; do
  if HOST_METRICS_DOCKER_MODE="$mutation" run_fixture >/dev/null 2>&1; then
    fail "Docker output mutation was accepted: $mutation"
  fi
done
rm -f "$stream_marker"
if HOST_METRICS_DOCKER_MODE=oversized run_fixture >/dev/null 2>&1; then
  fail "oversized Docker output was accepted"
fi
[[ ! -e "$stream_marker" ]] ||
  fail "oversized Docker output was read past MAX+1 before rejection"
if HOST_METRICS_CURL_MODE=broken run_fixture >/dev/null 2>&1; then
  fail "broken internal endpoint was accepted"
fi

unsafe_backup="$fixture_root/unsafe-backup"
mkdir -m 0770 "$unsafe_backup"
if HOST_METRICS_BACKUP_PATH_OVERRIDE="$unsafe_backup" \
  run_fixture >/dev/null 2>&1; then
  fail "group-writable backup filesystem path was accepted"
fi
readable_backup="$fixture_root/readable-backup"
mkdir -m 0750 "$readable_backup"
if HOST_METRICS_BACKUP_PATH_OVERRIDE="$readable_backup" \
  run_fixture >/dev/null 2>&1; then
  fail "non-0700 backup filesystem path was accepted"
fi
backup_link="$fixture_root/backup-link"
ln -s "$backup_path" "$backup_link"
if HOST_METRICS_BACKUP_PATH_OVERRIDE="$backup_link" \
  run_fixture >/dev/null 2>&1; then
  fail "symlinked backup filesystem path was accepted"
fi

printf '%s\n' 'host metrics contract: PASS'
