#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/host-metrics-path.sh"
INTERNAL_ENDPOINT='http://127.0.0.1:9090/internal/host-samples'
MAX_FILE_BYTES=$((64 * 1024))
monitored_service_allowlist=(caddy app worker postgres redis minio)
auxiliary_service_allowlist=(postgres-tls-init minio-data-init backup-storage-init backup-secrets-init backup migrate restore acceptance)

die() {
  printf '%s\n' 'host metrics collection failed' >&2
  exit 1
}

[[ $# -eq 2 && "$1" == "--environment" ]] || die
case "$2" in
  development)
    compose_file="$ROOT/deploy/compose.dev.yml"
    compose_project='happylearn-dev'
    ;;
  production)
    compose_file="$ROOT/deploy/compose.prod.yml"
    compose_project='happylearn-prod'
    ;;
  *)
    die
    ;;
esac

secret_file="${HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE:-}"
backup_path="${HAPPYLEARN_BACKUP_HOST_PATH:-}"
[[ -n "$secret_file" && -n "$backup_path" && -f "$compose_file" ]] || die
sampler_bin="${HOST_SAMPLER_BIN:-$ROOT/.tools/bin/host-sampler}"
[[ -x "$sampler_bin" ]] || die

if command -v timeout >/dev/null 2>&1; then
  timeout_bin="$(command -v timeout)"
elif command -v gtimeout >/dev/null 2>&1; then
  timeout_bin="$(command -v gtimeout)"
else
  die
fi

check_bounded_file() {
  local path="$1"
  local size
  size="$(wc -c <"$path")" || return 1
  ((size > 0 && size <= MAX_FILE_BYTES))
}

run_bounded_capture() {
  local destination="$1"
  local seconds="$2"
  local statuses
  shift 2
  "$timeout_bin" "$seconds" "$@" |
    head -c "$((MAX_FILE_BYTES + 1))" >"$destination"
  statuses=("${PIPESTATUS[@]}")
  ((statuses[0] == 0 && statuses[1] == 0)) || return 1
  check_bounded_file "$destination"
}

validate_host_metrics_backup_path "$backup_path" || die

temporary="$(mktemp -d "${TMPDIR:-/tmp}/happylearn-host-metrics.XXXXXX")" ||
  die
chmod 0700 "$temporary" || die
cleanup() {
  rm -rf "$temporary"
}
trap cleanup EXIT HUP INT TERM

compose_raw="$temporary/compose-ps.json"
if ! run_bounded_capture "$compose_raw" 10 docker compose \
  -p "$compose_project" \
  -f "$compose_file" \
  ps --format json; then
  die
fi

compose_all="$temporary/compose-all.json"
if ! jq -sce '
  if length == 1 and (.[0] | type) == "array" then .[0] else . end
  | if type != "array" or
       any(.[];
         type != "object" or
         has("Environment") or has("Env") or has("Mounts") or
         has("ImageRegistryAuth") or has("RegistryAuth") or has("Logs"))
    then error("unsafe compose rows")
    else .
    end
' "$compose_raw" >"$compose_all"; then
  die
fi
check_bounded_file "$compose_all" || die

compose_selected="$temporary/compose-selected.json"
compose_allowlist_json="$(
  printf '%s\n' \
    "${monitored_service_allowlist[@]}" \
    "${auxiliary_service_allowlist[@]}" |
    jq -Rsc 'split("\n")[:-1]'
)" || die
if ! jq -ce --argjson allowlist "$compose_allowlist_json" '
  if all(.[];
    (.Service | type) == "string" and
    (.Service as $service | $allowlist | index($service)) != null)
  then
    map({
      service: .Service,
      state: .State,
      health: (.Health // ""),
      restarts: (if has("RestartCount") then .RestartCount else null end)
    })
  else
    error("unknown service")
  end
' "$compose_all" >"$compose_selected"; then
  die
fi
check_bounded_file "$compose_selected" || die

stats_selected="$temporary/stats-selected.json"
printf '%s\n' '[]' >"$stats_selected"
for service in "${monitored_service_allowlist[@]}"; do
  state="$(jq -er --arg service "$service" \
    '[.[] | select(.service == $service)] |
     if length > 1 then error("duplicate service")
     elif length == 0 then "absent"
     else .[0].state
     end' "$compose_selected")" || die
  [[ "$state" == "running" ]] || continue

  container_id="$(jq -er --arg service "$service" \
    '[.[] | select(.Service == $service)] |
     if length == 1 and (.[0].ID | type) == "string" and
        (.[0].ID | test("^[0-9a-f]{12,64}$"))
     then .[0].ID
     else error("missing container")
     end' "$compose_all")" || die
  stats_raw="$temporary/stats-$service.json"
  if ! run_bounded_capture "$stats_raw" 10 docker stats --no-stream \
    --format '{{json .}}' "$container_id"; then
    die
  fi
  stats_record="$(jq -sce --arg service "$service" '
    if length != 1 or (.[0] | type) != "object" or
       (.[0] | has("Environment")) or (.[0] | has("Env")) or
       (.[0] | has("Mounts")) or (.[0] | has("ImageRegistryAuth")) or
       (.[0] | has("RegistryAuth")) or (.[0] | has("Logs"))
    then error("unsafe stats row")
    else {
      service: $service,
      cpuPercent: .[0].CPUPerc,
      memoryUsage: .[0].MemUsage
    }
    end
  ' "$stats_raw")" || die
  next_stats="$temporary/stats-next.json"
  jq -ce --argjson record "$stats_record" '. + [$record]' \
    "$stats_selected" >"$next_stats" || die
  mv "$next_stats" "$stats_selected" || die
done

filesystem_raw="$temporary/filesystem-root.txt"
if ! run_bounded_capture "$filesystem_raw" 10 df -Pk /; then
  die
fi
root_used_percent="$(
  awk '
    NR == 2 && $5 ~ /^(0|[1-9][0-9]*)%$/ {
      print $5
      found = 1
    }
    END { if (!found) exit 1 }
  ' "$filesystem_raw"
)" || die

backup_filesystem_raw="$temporary/filesystem-backup.txt"
if ! run_bounded_capture "$backup_filesystem_raw" 10 df -Pk "$backup_path"; then
  die
fi
backup_used_percent="$(
  awk '
    NR == 2 && $5 ~ /^(0|[1-9][0-9]*)%$/ {
      print $5
      found = 1
    }
    END { if (!found) exit 1 }
  ' "$backup_filesystem_raw"
)" || die

observed_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')" || die
sampler_input="$temporary/sampler-input.json"
jq -cn \
  --arg observedAt "$observed_at" \
  --slurpfile compose "$compose_selected" \
  --slurpfile stats "$stats_selected" \
  --arg rootUsedPercent "$root_used_percent" \
  --arg backupUsedPercent "$backup_used_percent" \
  '{
    schemaVersion: 1,
    observedAt: $observedAt,
    compose: $compose[0],
    stats: $stats[0],
    filesystems: [
      {filesystem: "root", usedPercent: $rootUsedPercent},
      {filesystem: "backup", usedPercent: $backupUsedPercent}
    ]
  }' >"$sampler_input" || die
check_bounded_file "$sampler_input" || die

payload="$temporary/payload.json"
if ! run_bounded_capture "$payload" 10 "$sampler_bin" payload \
  <"$sampler_input"; then
  die
fi

timestamp="$(date -u '+%s')" || die
[[ "$timestamp" =~ ^[0-9]+$ ]] || die
nonce="$(od -An -N16 -tx1 /dev/urandom | tr -d '[:space:]')" || die
[[ "$nonce" =~ ^[0-9a-f]{32}$ ]] || die
signature_file="$temporary/signature.txt"
if ! run_bounded_capture "$signature_file" 10 "$sampler_bin" sign \
    --secret-file "$secret_file" \
    --timestamp "$timestamp" \
    --nonce "$nonce" \
    <"$payload"; then
  die
fi
signature="$(tr -d '\n' <"$signature_file")" || die
[[ "$signature" =~ ^sha256=[0-9a-f]{64}$ ]] || die

status_file="$temporary/http-status.txt"
if ! run_bounded_capture "$status_file" 10 curl \
    --silent \
    --show-error \
    --output /dev/null \
    --write-out '%{http_code}' \
    --request POST \
    --connect-timeout 3 \
    --max-time 10 \
    -H 'Content-Type: application/json' \
    -H "X-HL-Timestamp: $timestamp" \
    -H "X-HL-Nonce: $nonce" \
    -H "X-HL-Signature: $signature" \
    --data-binary "@$payload" \
    "$INTERNAL_ENDPOINT"; then
  die
fi
status="$(tr -d '\n' <"$status_file")" || die
[[ "$status" == "204" ]] || die

printf '%s\n' 'host metrics collection: PASS'
