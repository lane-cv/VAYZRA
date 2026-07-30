#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="$repo_root/scripts/collect-host-metrics.sh"
compose="$repo_root/deploy/compose.dev.yml"
development_metrics_bearer="$repo_root/deploy/fixtures/development-metrics-bearer-do-not-use-in-production"
development_host_hmac="$repo_root/deploy/fixtures/development-host-metrics-hmac-do-not-use-in-production"
path_helper="$repo_root/scripts/host-metrics-path.sh"
uid_target="$repo_root/scripts/host-metrics_uid_contract_test.sh"
live_target="$repo_root/scripts/host-metrics_live_test.sh"
makefile="$repo_root/Makefile"
package_json="$repo_root/package.json"

fail() {
  printf 'host metrics contract: FAIL: %s\n' "$1" >&2
  exit 1
}

[[ -f "$target" ]] || fail "collector script is missing"
[[ -x "$target" ]] || fail "collector script is not executable"
[[ -f "$compose" ]] || fail "development Compose file is missing"
[[ -f "$development_metrics_bearer" && -f "$development_host_hmac" ]] ||
  fail "development-only monitoring secret fixtures are missing"
for development_fixture in \
  "$development_metrics_bearer" "$development_host_hmac"; do
  grep -Fq 'do-not-use-in-production' "$development_fixture" ||
    fail "development monitoring fixture is not explicitly non-production"
done
[[ -f "$path_helper" ]] || fail "backup path validator is missing"
[[ -f "$uid_target" && -x "$uid_target" ]] ||
  fail "cross-UID backup path contract is missing or not executable"
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
require_literal 'app-secrets-init postgres-tls-init minio-data-init backup-storage-init backup-secrets-init backup migrate restore acceptance'
require_literal 'host-sampler'
require_literal 'head -c "$((MAX_FILE_BYTES + 1))"'
require_literal 'source "$ROOT/scripts/host-metrics-path.sh"'
require_literal 'validate_host_metrics_backup_path "$backup_path"'
require_literal '[[ -n "$secret_file" && -n "$backup_path" && -f "$compose_file" ]] || die'

require_compose_literal() {
  grep -Fq -- "$1" "$compose" ||
    fail "development Compose missing host-metrics wiring: $1"
}

require_compose_literal 'HAPPYLEARN_INTERNAL_LISTEN: ":9090"'
require_compose_literal 'HAPPYLEARN_METRICS_BEARER_SECRET_FILE: /run/secrets/metrics-bearer'
require_compose_literal 'HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE: /run/secrets/host-metrics-hmac'
require_compose_literal '"127.0.0.1:9090:9090"'
require_compose_literal '  app-secrets-init:'
require_compose_literal 'source: metrics_bearer_secret'
require_compose_literal 'target: /source/metrics-bearer'
require_compose_literal 'source: host_metrics_hmac_secret'
require_compose_literal 'target: /source/host-metrics-hmac'
require_compose_literal 'chown 10001:10001 "/secrets/.$${name}.new"'
require_compose_literal 'chmod 0400 "/secrets/.$${name}.new"'
require_compose_literal 'test "$(stat -c '"'"'%u:%g:%a'"'"' "/secrets/$${name}")" = '"'"'10001:10001:400'"'"''
require_compose_literal 'app_secrets:/run/secrets:ro'
require_compose_literal 'app-secrets-init:'
require_compose_literal 'condition: service_completed_successfully'

grep -Fq 'HOST_METRICS_BACKUP_RUNTIME_UID=10003' "$path_helper" ||
  fail "fixed backup runtime UID changed"
if grep -Fq 'cd "$path"' "$path_helper"; then
  fail "backup path validation enters the protected target directory"
fi

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
license_file="$fixture_root/minio.license"
metrics_bearer_file="$fixture_root/metrics-bearer"
host_hmac_file="$fixture_root/host-hmac-compose"
printf '%s\n' 'development-license-fixture' >"$license_file"
printf '%s\n' 'development-metrics-bearer-fixture' >"$metrics_bearer_file"
printf '%s\n' 'development-host-hmac-fixture' >"$host_hmac_file"
chmod 0600 "$license_file" "$metrics_bearer_file" "$host_hmac_file"

render_monitoring_compose() {
  local source="$1"
  local destination="$2"
  env \
    HAPPYLEARN_AISTOR_LICENSE_FILE="$license_file" \
    HAPPYLEARN_METRICS_BEARER_SECRET_FILE="$metrics_bearer_file" \
    HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE="$host_hmac_file" \
    docker compose --profile '*' -f "$source" config --format json \
      >"$destination"
}

validate_monitoring_compose() {
  jq -e \
    --arg metrics_source "$metrics_bearer_file" \
    --arg hmac_source "$host_hmac_file" '
    any(.services.app.ports[]?;
      .host_ip == "127.0.0.1" and
      (.published | tostring) == "9090" and
      (.target | tostring) == "9090") and
    .services.app.environment.HAPPYLEARN_INTERNAL_LISTEN == ":9090" and
    .services.app.environment.HAPPYLEARN_METRICS_BEARER_SECRET_FILE ==
      "/run/secrets/metrics-bearer" and
    .services.app.environment.HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE ==
      "/run/secrets/host-metrics-hmac" and
    ((.services.app.secrets // []) | length) == 0 and
    .services["app-secrets-init"].user == "0:0" and
    .services["app-secrets-init"].network_mode == "none" and
    .services["app-secrets-init"].restart == "no" and
    (.services["app-secrets-init"].cap_drop | index("ALL")) != null and
    (.services["app-secrets-init"].cap_add | sort) ==
      ["CHOWN","DAC_OVERRIDE","FOWNER"] and
    any(.services["app-secrets-init"].secrets[]?;
      .source == "metrics_bearer_secret" and
      .target == "/source/metrics-bearer") and
    any(.services["app-secrets-init"].secrets[]?;
      .source == "host_metrics_hmac_secret" and
      .target == "/source/host-metrics-hmac") and
    any(.services["app-secrets-init"].volumes[]?;
      .type == "volume" and .source == "app_secrets" and
      .target == "/secrets" and
      ((has("read_only") | not) or .read_only == false)) and
    any(.services.app.volumes[]?;
      .type == "volume" and .source == "app_secrets" and
      .target == "/run/secrets" and .read_only == true) and
    .services.app.depends_on["app-secrets-init"].condition ==
      "service_completed_successfully" and
    any(.services["app-secrets-init"].command[]?;
      contains("cp \"/source/$${name}\" \"/secrets/.$${name}.new\"")) and
    any(.services["app-secrets-init"].command[]?;
      contains("chown 10001:10001 \"/secrets/.$${name}.new\"")) and
    any(.services["app-secrets-init"].command[]?;
      contains("chmod 0400 \"/secrets/.$${name}.new\"")) and
    any(.services["app-secrets-init"].command[]?;
      contains("10001:10001:400")) and
    .secrets.metrics_bearer_secret.file == $metrics_source and
    .secrets.host_metrics_hmac_secret.file == $hmac_source
  ' "$1" >/dev/null
}

resolved_compose="$fixture_root/compose.json"
render_monitoring_compose "$compose" "$resolved_compose" ||
  fail "development Compose host-metrics wiring does not render"
validate_monitoring_compose "$resolved_compose" ||
  fail "development Compose host-metrics wiring is incomplete"

default_compose="$fixture_root/compose-default.json"
env -u HAPPYLEARN_METRICS_BEARER_SECRET_FILE \
  -u HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE \
  HAPPYLEARN_AISTOR_LICENSE_FILE="$license_file" \
  docker compose --profile '*' -f "$compose" config --format json \
    >"$default_compose" ||
  fail "development Compose monitoring defaults do not render"
jq -e \
  --arg metrics_source "$development_metrics_bearer" \
  --arg hmac_source "$development_host_hmac" '
    .secrets.metrics_bearer_secret.file == $metrics_source and
    .secrets.host_metrics_hmac_secret.file == $hmac_source
  ' "$default_compose" >/dev/null ||
  fail "development Compose defaults are not fixed development-only fixtures"

remove_exact_line() {
  local source="$1"
  local line="$2"
  local destination="$3"
  awk -v exact="$line" '$0 != exact' "$source" >"$destination"
}

remove_secret_mount() {
  local source="$1"
  local secret="$2"
  local destination="$3"
  awk -v marker="      - source: $secret" '
    skip > 0 { skip--; next }
    $0 == marker { skip=1; next }
    { print }
  ' "$source" >"$destination"
}

remove_dependency() {
  local source="$1"
  local destination="$2"
  awk '
    skip > 0 { skip--; next }
    $0 == "      app-secrets-init:" { skip=1; next }
    { print }
  ' "$source" >"$destination"
}

remove_app_secret_volume() {
  local source="$1"
  local destination="$2"
  awk '
    held != "" {
      if (held == "    volumes:" &&
          $0 == "      - app_secrets:/run/secrets:ro") {
        held = ""
        next
      }
      print held
      held = ""
    }
    $0 == "    volumes:" {
      held = $0
      next
    }
    { print }
    END {
      if (held != "") {
        print held
      }
    }
  ' "$source" >"$destination"
}

for mutation in \
  'port|      - "127.0.0.1:9090:9090"' \
  'metrics_env|      HAPPYLEARN_METRICS_BEARER_SECRET_FILE: /run/secrets/metrics-bearer' \
  'host_env|      HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE: /run/secrets/host-metrics-hmac'; do
  name="${mutation%%|*}"
  line="${mutation#*|}"
  mutated="$fixture_root/compose-$name.yml"
  mutated_json="$fixture_root/compose-$name.json"
  remove_exact_line "$compose" "$line" "$mutated"
  render_monitoring_compose "$mutated" "$mutated_json" ||
    fail "development Compose mutation did not render: $name"
  if validate_monitoring_compose "$mutated_json"; then
    fail "development Compose mutation was accepted: $name"
  fi
done

for secret in metrics_bearer_secret host_metrics_hmac_secret; do
  mutated="$fixture_root/compose-mount-$secret.yml"
  mutated_json="$fixture_root/compose-mount-$secret.json"
  remove_secret_mount "$compose" "$secret" "$mutated"
  render_monitoring_compose "$mutated" "$mutated_json" ||
    fail "development Compose secret mutation did not render: $secret"
  if validate_monitoring_compose "$mutated_json"; then
    fail "development Compose secret mutation was accepted: $secret"
  fi
done

for mutation in \
  'copy|          cp "/source/$${name}" "/secrets/.$${name}.new"' \
  'owner|          chown 10001:10001 "/secrets/.$${name}.new"' \
  'mode|          chmod 0400 "/secrets/.$${name}.new"'; do
  name="${mutation%%|*}"
  line="${mutation#*|}"
  mutated="$fixture_root/compose-$name.yml"
  mutated_json="$fixture_root/compose-$name.json"
  remove_exact_line "$compose" "$line" "$mutated"
  render_monitoring_compose "$mutated" "$mutated_json" ||
    fail "development Compose init-copy mutation did not render: $name"
  if validate_monitoring_compose "$mutated_json"; then
    fail "development Compose init-copy mutation was accepted: $name"
  fi
done

mutated="$fixture_root/compose-app-volume.yml"
mutated_json="$fixture_root/compose-app-volume.json"
remove_app_secret_volume "$compose" "$mutated"
render_monitoring_compose "$mutated" "$mutated_json" ||
  fail "development Compose app secret-volume mutation did not render"
if validate_monitoring_compose "$mutated_json"; then
  fail "development Compose app secret-volume mutation was accepted"
fi

mutated="$fixture_root/compose-dependency.yml"
mutated_json="$fixture_root/compose-dependency.json"
remove_dependency "$compose" "$mutated"
render_monitoring_compose "$mutated" "$mutated_json" ||
  fail "development Compose dependency mutation did not render"
if validate_monitoring_compose "$mutated_json"; then
  fail "development Compose dependency mutation was accepted"
fi

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
      printf '%s\n' '[{"ID":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","Service":"backup","State":"exited","Health":"","Command":"/app/backup","Publishers":[]},{"ID":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","Service":"redis","State":"exited","Health":"","Command":"redis-server","Publishers":[]},{"ID":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","Service":"app-secrets-init","State":"exited","Health":"","Command":"/bin/sh","Publishers":[]},{"ID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Service":"app","State":"running","Health":"healthy","RestartCount":3,"Command":"/app/server","Publishers":[]}]'
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

jq -e '
  .schemaVersion == 1 and
  .observedAt == "2026-07-30T04:05:06Z" and
  .compose == [
    {"service":"backup","state":"exited","health":"","restarts":null},
    {"service":"redis","state":"exited","health":"","restarts":null},
    {"service":"app-secrets-init","state":"exited","health":"","restarts":null},
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

bash "$uid_target"
printf '%s\n' 'host metrics contract: PASS'
