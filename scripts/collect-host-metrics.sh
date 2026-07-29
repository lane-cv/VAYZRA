#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INTERNAL_ENDPOINT='http://127.0.0.1:9090/internal/host-samples'
MAX_FILE_BYTES=$((64 * 1024))
service_allowlist=(caddy app worker postgres redis minio)

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
[[ -n "$secret_file" && -f "$compose_file" ]] || die
sampler_bin="${HOST_SAMPLER_BIN:-$ROOT/.tools/bin/host-sampler}"
[[ -x "$sampler_bin" ]] || die

if command -v timeout >/dev/null 2>&1; then
  timeout_bin="$(command -v timeout)"
elif command -v gtimeout >/dev/null 2>&1; then
  timeout_bin="$(command -v gtimeout)"
else
  die
fi

run_bounded() {
  local seconds="$1"
  shift
  "$timeout_bin" "$seconds" "$@"
}

check_bounded_file() {
  local path="$1"
  local size
  size="$(wc -c <"$path")" || return 1
  ((size > 0 && size <= MAX_FILE_BYTES))
}

temporary="$(mktemp -d "${TMPDIR:-/tmp}/happylearn-host-metrics.XXXXXX")" ||
  die
chmod 0700 "$temporary" || die
cleanup() {
  rm -rf "$temporary"
}
trap cleanup EXIT HUP INT TERM

compose_raw="$temporary/compose-ps.json"
if ! run_bounded 10 docker compose \
  -p "$compose_project" \
  -f "$compose_file" \
  ps --format json >"$compose_raw"; then
  die
fi
check_bounded_file "$compose_raw" || die

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
if ! jq -ce '
  if all(.[];
    (.Service == "caddy" or .Service == "app" or .Service == "worker" or
     .Service == "postgres" or .Service == "redis" or .Service == "minio"))
  then
    map({
      service: .Service,
      state: .State,
      health: (.Health // ""),
      restarts: (.RestartCount // 0)
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
for service in "${service_allowlist[@]}"; do
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
        (.[0].ID | length) > 0
     then .[0].ID
     else error("missing container")
     end' "$compose_all")" || die
  stats_raw="$temporary/stats-$service.json"
  if ! run_bounded 10 docker stats --no-stream \
    --format '{{json .}}' "$container_id" >"$stats_raw"; then
    die
  fi
  check_bounded_file "$stats_raw" || die
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
if ! run_bounded 10 df -Pk / >"$filesystem_raw"; then
  die
fi
check_bounded_file "$filesystem_raw" || die
root_used_percent="$(
  awk '
    NR == 2 && $5 ~ /^(0|[1-9][0-9]*)%$/ {
      print $5
      found = 1
    }
    END { if (!found) exit 1 }
  ' "$filesystem_raw"
)" || die

observed_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')" || die
sampler_input="$temporary/sampler-input.json"
jq -cn \
  --arg observedAt "$observed_at" \
  --slurpfile compose "$compose_selected" \
  --slurpfile stats "$stats_selected" \
  --arg usedPercent "$root_used_percent" \
  '{
    schemaVersion: 1,
    observedAt: $observedAt,
    compose: $compose[0],
    stats: $stats[0],
    filesystems: [{filesystem: "root", usedPercent: $usedPercent}]
  }' >"$sampler_input" || die
check_bounded_file "$sampler_input" || die

payload="$temporary/payload.json"
if ! run_bounded 10 "$sampler_bin" payload \
  <"$sampler_input" >"$payload"; then
  die
fi
check_bounded_file "$payload" || die

timestamp="$(date -u '+%s')" || die
[[ "$timestamp" =~ ^[0-9]+$ ]] || die
nonce="$(od -An -N16 -tx1 /dev/urandom | tr -d '[:space:]')" || die
[[ "$nonce" =~ ^[0-9a-f]{32}$ ]] || die
signature="$(
  run_bounded 10 "$sampler_bin" sign \
    --secret-file "$secret_file" \
    --timestamp "$timestamp" \
    --nonce "$nonce" \
    <"$payload"
)" || die
[[ "$signature" =~ ^sha256=[0-9a-f]{64}$ ]] || die

status="$(
  run_bounded 10 curl \
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
    "$INTERNAL_ENDPOINT"
)" || die
[[ "$status" == "204" ]] || die

printf '%s\n' 'host metrics collection: PASS'
