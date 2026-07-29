#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="$repo_root/scripts/collect-host-metrics.sh"
makefile="$repo_root/Makefile"
package_json="$repo_root/package.json"

fail() {
  printf 'host metrics contract: FAIL: %s\n' "$1" >&2
  exit 1
}

[[ -f "$target" ]] || fail "collector script is missing"
[[ -x "$target" ]] || fail "collector script is not executable"

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
require_literal 'docker compose'
require_literal 'ps --format json'
require_literal 'stats --no-stream'
require_literal '--connect-timeout'
require_literal '--max-time'
require_literal '64 * 1024'
require_literal 'caddy app worker postgres redis minio'
require_literal 'host-sampler'

forbid_pattern '(/var/run/docker\.sock|docker[[:space:]]+inspect)'
forbid_pattern '(^|[[:space:]])(env|printenv)([[:space:]]|$)'
forbid_pattern 'set[[:space:]]+-[^[:space:]]*x'
forbid_pattern 'docker[[:space:]]+(container[[:space:]]+)?logs'
forbid_pattern 'ps[[:space:]]+(aux|-[A-Za-z]*e)'
forbid_pattern '0\.0\.0\.0|host\.docker\.internal'
forbid_pattern 'openssl.*(-hmac|-macopt)'

grep -Fq 'host-metrics-contract:' "$makefile" ||
  fail "Makefile host-metrics-contract target missing"
grep -Fq '"host-metrics:contract"' "$package_json" ||
  fail "package host-metrics:contract script missing"

fixture_root="$(mktemp -d "${TMPDIR:-/tmp}/host-metrics-contract.XXXXXX")"
trap 'rm -rf "$fixture_root"' EXIT
fake_bin="$fixture_root/bin"
mkdir -p "$fake_bin"
command_log="$fixture_root/commands.log"
sampler_input="$fixture_root/sampler-input.json"
signed_body="$fixture_root/signed-body.json"
curl_body="$fixture_root/curl-body.json"
curl_headers="$fixture_root/curl-headers.txt"

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
  head -c 70000 /dev/zero | tr '\0' x
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
      printf '%s\n' '[{"ID":"redis-id","Service":"redis","State":"exited","Health":"","Command":"redis-server","Publishers":[]},{"ID":"app-id","Service":"app","State":"running","Health":"healthy","Command":"/app/server","Publishers":[]}]'
      ;;
  esac
  exit 0
fi
if [[ "$*" == *"stats --no-stream "* ]]; then
  case "$*" in
    *app-id*)
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
printf '%s\n' '/dev/test 100 38 62 38% /'
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
  "$fake_bin/host-sampler" "$fake_bin/curl"
secret_file="$fixture_root/host-hmac"
printf '%s\n' 'test-host-secret' >"$secret_file"
chmod 0600 "$secret_file"

run_fixture() {
  env \
    PATH="$fake_bin:/usr/bin:/bin" \
    HOST_SAMPLER_BIN="$fake_bin/host-sampler" \
    HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE="$secret_file" \
    HOST_METRICS_COMMAND_LOG="$command_log" \
    HOST_METRICS_SAMPLER_INPUT="$sampler_input" \
    HOST_METRICS_SIGNED_BODY="$signed_body" \
    HOST_METRICS_CURL_BODY="$curl_body" \
    HOST_METRICS_CURL_HEADERS="$curl_headers" \
    HOST_METRICS_DOCKER_MODE="${HOST_METRICS_DOCKER_MODE:-valid}" \
    HOST_METRICS_CURL_MODE="${HOST_METRICS_CURL_MODE:-valid}" \
    bash "$target" --environment development
}

: >"$command_log"
run_fixture >/dev/null

jq -e '
  .schemaVersion == 1 and
  .observedAt == "2026-07-30T04:05:06Z" and
  .compose == [
    {"service":"redis","state":"exited","health":"","restarts":0},
    {"service":"app","state":"running","health":"healthy","restarts":0}
  ] and
  .stats == [
    {"service":"app","cpuPercent":"12.50%","memoryUsage":"1.5MiB / 2GiB"}
  ] and
  .filesystems == [{"filesystem":"root","usedPercent":"38%"}]
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

if grep -Eiq '(command|environment|mounts|password|docker\.sock|registry|logs|container|image|project)' "$sampler_input"; then
  fail "sampler input contains Docker-sensitive fields"
fi
if grep -Fq 'test-host-secret' "$command_log" "$sampler_input" "$signed_body" "$curl_body" "$curl_headers"; then
  fail "HMAC secret leaked to commands or artifacts"
fi

for mutation in dangerous unknown truncated oversized; do
  if HOST_METRICS_DOCKER_MODE="$mutation" run_fixture >/dev/null 2>&1; then
    fail "Docker output mutation was accepted: $mutation"
  fi
done
if HOST_METRICS_CURL_MODE=broken run_fixture >/dev/null 2>&1; then
  fail "broken internal endpoint was accepted"
fi

printf '%s\n' 'host metrics contract: PASS'
