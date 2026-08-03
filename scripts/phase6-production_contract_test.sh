#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
deploy_dir="${HAPPYLEARN_PHASE6_DEPLOY_DIR:-$repo_root/deploy}"
scope="${HAPPYLEARN_PHASE6_CONTRACT_SCOPE:-all}"

fail() {
  echo "Phase 6 production contract: FAIL: $*" >&2
  exit 1
}

case "$scope" in
  all|compose|caddy|caddy-runtime) ;;
  *) fail "HAPPYLEARN_PHASE6_CONTRACT_SCOPE must be all, compose, caddy, or caddy-runtime" ;;
esac

require_file() {
  test -f "$1" || fail "missing ${1#"$repo_root/"}"
}

assert_compose() {
  local compose_file="$deploy_dir/compose.prod.yml"
  local local_override="$deploy_dir/compose.prod.local.yml"
  require_file "$compose_file"
  require_file "$local_override"
  command -v docker >/dev/null || fail "docker is required"
  command -v jq >/dev/null || fail "jq is required"

  local tmp_dir
  tmp_dir="$(mktemp -d)"
  trap 'rm -rf "$tmp_dir"' EXIT
  mkdir -m 0700 "$tmp_dir/secrets" "$tmp_dir/backups" "$tmp_dir/releases"

  local secret
  for secret in postgres-password redis-password minio-access-key \
    minio-secret-key aistor-license login-throttle ai-master-key \
    metrics-bearer host-metrics-hmac backup-password backup-age-identity \
    database-url redis-url app-database-url app-redis-url app-login-throttle \
    app-minio-access-key app-minio-secret-key app-ai-master-key \
    worker-database-url worker-redis-url worker-login-throttle \
    worker-minio-access-key worker-minio-secret-key worker-ai-master-key \
    backup-database-url backup-database-password backup-local-repository; do
    printf 'phase6-contract-secret-%s\n' "$secret" >"$tmp_dir/secrets/$secret"
    chmod 0600 "$tmp_dir/secrets/$secret"
  done

  local digest=0000000000000000000000000000000000000000000000000000000000000000
  local env_file="$tmp_dir/production.env"
  cat >"$env_file" <<EOF
COMPOSE_PROJECT_NAME=happylearn-prod
HAPPYLEARN_DOMAIN=learn.example.invalid
HAPPYLEARN_TIMEZONE=Asia/Shanghai
HAPPYLEARN_APP_IMAGE=registry.example.invalid/happylearn/app@sha256:$digest
HAPPYLEARN_WORKER_IMAGE=registry.example.invalid/happylearn/worker@sha256:$digest
HAPPYLEARN_MIGRATE_IMAGE=registry.example.invalid/happylearn/migrate@sha256:$digest
HAPPYLEARN_BACKUP_IMAGE=registry.example.invalid/happylearn/backup@sha256:$digest
HAPPYLEARN_CADDY_IMAGE=caddy@sha256:$digest
HAPPYLEARN_POSTGRES_IMAGE=postgres@sha256:$digest
HAPPYLEARN_REDIS_IMAGE=redis@sha256:$digest
HAPPYLEARN_MINIO_IMAGE=quay.io/minio/aistor/minio@sha256:$digest
HAPPYLEARN_BACKUP_HOST_PATH=$tmp_dir/backups
HAPPYLEARN_RELEASE_STATE_PATH=$tmp_dir/releases
HAPPYLEARN_BACKUP_AGE_RECIPIENT=age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp5m40h
HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID=phase6-contract-key
HAPPYLEARN_SECRET_DIR=$tmp_dir/secrets
HAPPYLEARN_CADDYFILE=$deploy_dir/Caddyfile
HAPPYLEARN_MAINTENANCE_CADDYFILE=$deploy_dir/Caddyfile.maintenance
HAPPYLEARN_MAINTENANCE_FILE=$deploy_dir/maintenance.html
EOF
  chmod 0600 "$env_file"

  local model="$tmp_dir/model.json"
  local local_model="$tmp_dir/local-model.json"
  docker compose -p happylearn-prod --profile '*' --env-file "$env_file" \
    -f "$compose_file" config --format json >"$model" ||
    fail "production Compose model does not render"
  docker compose -p happylearn-prod --profile '*' --env-file "$env_file" \
    -f "$compose_file" -f "$local_override" config --format json >"$local_model" ||
    fail "local production Compose model does not render"

  jq -e '.name == "happylearn-prod"' "$model" >/dev/null ||
    fail "project name must be happylearn-prod"
  jq -e '(.services | keys) == ["acceptance","app","backup","caddy","migrate","minio","minio-volume-init","postgres","redis","release-control","restore","worker"]' "$model" >/dev/null ||
    fail "service set is not exact"
  jq -e '
    .services["minio-volume-init"] as $init |
    $init.user == "0:0" and $init.read_only == true and $init.restart == "no" and
    $init.command == ["chown 1000:10003 /data && chmod 2750 /data"] and
    ($init.cap_drop | index("ALL")) != null and
    ($init.cap_add == ["CHOWN","FOWNER"]) and
    ($init.security_opt | index("no-new-privileges:true")) != null and
    (.services.minio.user == "1000:10003") and
    (.services.minio.depends_on["minio-volume-init"].condition == "service_completed_successfully") and
    (.services.backup.user == "10003:10003") and
    any(.services.backup.volumes[]; .target == "/source/aistor" and .read_only == true)
  ' "$model" >/dev/null || fail "AIStor volume ownership initializer is not narrowly constrained"
  jq -e '(.networks | keys) == ["edge","private"] and .networks.private.internal == true' "$model" >/dev/null ||
    fail "edge/private network topology is invalid"
  jq -e '
    [.services | to_entries[] | .key as $service | .value.ports[]? |
      {service: $service, host_ip: (.host_ip // ""), published: (.published|tostring), target: (.target|tostring)}] as $ports |
    ($ports | length) == 2 and
    all($ports[]; .service == "caddy" and .host_ip == "0.0.0.0" and
      ((.published == "80" and .target == "80") or (.published == "443" and .target == "443")))
  ' "$model" >/dev/null || fail "only Caddy may publish TCP 80 and 443"
  jq -e '
    all(.services | to_entries[];
      (.value.privileged // false) == false and
      (.value.network_mode // "") != "host" and
      all(.value.volumes[]?; ((.source // "") | contains("docker.sock") | not) and ((.target // "") | contains("docker.sock") | not)))
  ' "$model" >/dev/null || fail "privileged, host-network, or Docker socket access is forbidden"
  jq -e --arg normal "$deploy_dir/Caddyfile" --arg maintenance "$deploy_dir/Caddyfile.maintenance" --arg asset "$deploy_dir/maintenance.html" '
    (.services.caddy.volumes | map(select(.target == "/etc/caddy/Caddyfile")) | .[0]) as $normal_mount |
    (.services.caddy.volumes | map(select(.target == "/etc/caddy/Caddyfile.maintenance")) | .[0]) as $maintenance_mount |
    (.services.caddy.volumes | map(select(.target == "/srv/maintenance.html")) | .[0]) as $asset_mount |
    $normal_mount.type == "bind" and $normal_mount.source == $normal and $normal_mount.read_only == true and
    $maintenance_mount.type == "bind" and $maintenance_mount.source == $maintenance and $maintenance_mount.read_only == true and
    $asset_mount.type == "bind" and $asset_mount.source == $asset and $asset_mount.read_only == true
  ' "$model" >/dev/null || fail "Caddy configuration and maintenance asset mounts must be explicit read-only files"
  jq -e '
    all(.services[]; (.image | test("^[^[:space:]]+@sha256:[0-9a-f]{64}$")))
  ' "$model" >/dev/null || fail "every image must use an immutable sha256 digest"
  jq -e '
	. as $model |
    ["caddy","app","worker","postgres","redis","minio"] as $long |
    all($long[];
      . as $name |
      ($model.services[$name].healthcheck != null) and
      (($model.services[$name].restart // "") != "" and $model.services[$name].restart != "no") and
      (($model.services[$name].mem_limit // 0) > 0) and
      (($model.services[$name].cpus // 0) > 0))
  ' "$model" >/dev/null || fail "every long-running service needs health, restart, memory, and CPU limits"
  jq -e '
    all([.services.app,.services.worker][];
      .read_only == true and (.user // "") != "" and (.user | startswith("0") | not) and
      (.cap_drop | index("ALL")) != null and
      (.security_opt | index("no-new-privileges:true")) != null)
  ' "$model" >/dev/null || fail "app and worker hardening is incomplete"
  jq -e '
    all(.services[];
      .logging.driver == "json-file" and
      .logging.options["max-size"] == "10m" and
      .logging.options["max-file"] == "5")
  ' "$model" >/dev/null || fail "log rotation must be bounded for every service"
  jq -e '
    all(.services[];
      all((.environment // {}) | to_entries[];
        (.key | test("(PASSWORD|SECRET|TOKEN|AUTHORIZATION|ACCESS_KEY|DATABASE_URL|REDIS_URL)$") | not) or
        (.key | endswith("_FILE"))))
  ' "$model" >/dev/null || fail "secret values must not be embedded in environment blocks"
  jq -e '
    [.services | to_entries[] | .value.user as $user | .value.secrets[]? |
      {source: .source, user: $user}] |
    sort_by(.source) | group_by(.source) |
    all(.[]; ([.[].user] | unique | length) == 1)
  ' "$model" >/dev/null ||
    fail "a file-backed secret must not be shared across different container UIDs"
  jq -e '
    . as $model |
    ["caddy","app","worker","postgres","redis","minio"] as $steady |
    ($steady | map(. as $n | $model.services[$n].cpus) | add) as $steady_cpu |
    (["caddy","app","postgres","redis","minio","backup"] |
      map(. as $n | $model.services[$n].cpus) | add) as $backup_cpu |
    ($steady_cpu > 1.849 and $steady_cpu < 1.851) and
    ($steady | map(. as $n | ($model.services[$n].mem_limit | tonumber)) | add) == (3072 * 1024 * 1024) and
    ($backup_cpu > 1.099 and $backup_cpu < 1.101) and
    (["caddy","app","postgres","redis","minio","backup"] |
      map(. as $n | ($model.services[$n].mem_limit | tonumber)) | add) == (1792 * 1024 * 1024)
  ' "$model" >/dev/null || fail "steady or worker-drained backup resource arithmetic is invalid"
  jq -e '
    . as $model |
    ([$model.services | to_entries[] | .key as $service | .value.ports[]? |
      {service: $service, host_ip: (.host_ip // "")}]) as $ports |
    $model.networks.private.internal == true and
    ($ports | length) == 2 and
    all($ports[]; .service == "caddy" and .host_ip == "127.0.0.1") and
    all([$model.services.app,$model.services.worker][];
      .read_only == true and (.cap_drop | index("ALL")) != null and
      (.security_opt | index("no-new-privileges:true")) != null)
  ' "$local_model" >/dev/null ||
    fail "local override must preserve private-network and runtime hardening while binding loopback high ports"
  jq -e '
    .services.app.environment.HAPPYLEARN_LOCAL_AI_ALLOW_PRIVATE_PROVIDER == "true" and
    .services.worker.environment.HAPPYLEARN_LOCAL_AI_ALLOW_PRIVATE_PROVIDER == "true" and
    .services.app.environment.HAPPYLEARN_LOCAL_OBJECTSTORE_SKIP_LIFECYCLE_BOOTSTRAP == "true" and
    .services.worker.environment.HAPPYLEARN_LOCAL_OBJECTSTORE_SKIP_LIFECYCLE_BOOTSTRAP == "true"
  ' "$local_model" >/dev/null || fail "local acceptance provider control is missing"
  jq -e '
    all([.services.app,.services.worker][];
      (.environment.HAPPYLEARN_LOCAL_AI_ALLOW_PRIVATE_PROVIDER // "") == "" and
      (.environment.HAPPYLEARN_LOCAL_OBJECTSTORE_SKIP_LIFECYCLE_BOOTSTRAP // "") == "")
  ' "$model" >/dev/null || fail "local acceptance provider control leaked into production"

  rm -rf "$tmp_dir"
  trap - EXIT
}

assert_caddy() {
  local normal="$deploy_dir/Caddyfile"
  local maintenance="$deploy_dir/Caddyfile.maintenance"
  local local_tls="$deploy_dir/Caddyfile.local"
  local local_maintenance="$deploy_dir/Caddyfile.maintenance.local"
  local asset="$deploy_dir/maintenance.html"
  require_file "$normal"
  require_file "$maintenance"
  require_file "$local_tls"
  require_file "$local_maintenance"
  require_file "$asset"

  for file in "$normal" "$local_tls"; do
    grep -Eq 'handle[[:space:]]+/internal/\*' "$file" || fail "$(basename "$file") must handle /internal/* locally"
    grep -Eq 'respond[[:space:]].*404' "$file" || fail "$(basename "$file") must return 404 for /internal/*"
    grep -Eq 'request_body[[:space:]]*\{' "$file" || fail "$(basename "$file") must declare request body limits"
    grep -Eq 'max_size[[:space:]]+2MiB' "$file" || fail "$(basename "$file") must cap ordinary requests at 2 MiB"
    grep -Eq 'max_size[[:space:]]+9MiB' "$file" || fail "$(basename "$file") must cap upload requests at 9 MiB"
    grep -Eq 'flush_interval[[:space:]]+-1' "$file" || fail "$(basename "$file") must disable SSE buffering"
    grep -Eq 'read_timeout[[:space:]]+5m' "$file" || fail "$(basename "$file") must keep a bounded long SSE timeout"
    grep -Eq 'reverse_proxy[[:space:]]+app:8080' "$file" || fail "$(basename "$file") may proxy only to app:8080"
    grep -Fq 'Content-Security-Policy' "$file" || fail "$(basename "$file") is missing CSP"
    grep -Fq 'X-Content-Type-Options' "$file" || fail "$(basename "$file") is missing nosniff"
    grep -Fq 'Referrer-Policy' "$file" || fail "$(basename "$file") is missing referrer policy"
    grep -Fq 'Permissions-Policy' "$file" || fail "$(basename "$file") is missing permissions policy"
    grep -Fq 'X-Frame-Options' "$file" || fail "$(basename "$file") is missing frame policy"
    grep -Eq 'log_append[[:space:]]+uri_path[[:space:]]+\{http.request.uri.path\}' "$file" ||
      fail "$(basename "$file") logs must use the sanitized path"
    grep -Eq 'request>uri[[:space:]]+delete' "$file" ||
      fail "$(basename "$file") must delete query-bearing URIs from access logs"
    if awk '
      /handle \/internal\/\*/ { internal=1; next }
      internal && /^[[:space:]]*}/ { internal=0 }
      internal && /reverse_proxy/ { found=1 }
      END { exit found ? 0 : 1 }
    ' "$file"; then
      fail "$(basename "$file") must never proxy /internal/*"
    fi
  done

  grep -Fq 'Strict-Transport-Security' "$normal" || fail "public Caddyfile must enable HSTS"
  ! grep -Fq 'Strict-Transport-Security' "$local_tls" || fail "local TLS must not enable HSTS"
  ! grep -Fq 'Strict-Transport-Security' "$local_maintenance" || fail "local maintenance TLS must not enable HSTS"
  grep -Eq 'tls[[:space:]]+internal' "$local_tls" || fail "local Caddyfile must use local TLS"
  grep -Eq 'tls[[:space:]]+internal' "$local_maintenance" || fail "local maintenance Caddyfile must use local TLS"
  grep -Eq 'redir[[:space:]].*https://' "$normal" || fail "public HTTP must redirect to HTTPS"

  grep -Eq '(respond[[:space:]].*503|status[[:space:]]+503)' "$maintenance" || fail "maintenance mode must return 503"
  for maintenance_config in "$maintenance" "$local_maintenance"; do
    grep -Eq '@read[[:space:]]+method[[:space:]]+GET[[:space:]]+HEAD' "$maintenance_config" ||
      fail "$(basename "$maintenance_config") must restrict static serving to GET and HEAD"
    grep -Fq 'respond "Service Unavailable" 503' "$maintenance_config" ||
      fail "$(basename "$maintenance_config") must fail closed with 503 for non-read methods"
  done
  grep -Fq 'Retry-After' "$maintenance" || fail "maintenance mode must emit Retry-After"
  grep -Fq 'maintenance.html' "$maintenance" || fail "maintenance mode must serve the static asset"
  ! grep -Eq 'reverse_proxy' "$maintenance" || fail "maintenance mode must never proxy to the app"

  test "$(wc -c <"$asset")" -lt 16384 || fail "maintenance page must remain below 16 KiB"
  ! grep -Eiq '<(script|form|link|img|iframe)|https?://' "$asset" ||
    fail "maintenance page must be self-contained"

}

assert_caddy_runtime() {
  assert_caddy
  command -v docker >/dev/null || fail "docker is required for Caddy runtime validation"
  local caddy_image="${HAPPYLEARN_PHASE6_CADDY_VALIDATION_IMAGE:-caddy:2.10.2-alpine@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d}"
  if ! docker image inspect "$caddy_image" >/dev/null 2>&1; then
    timeout --foreground --kill-after=10s 300s docker pull "$caddy_image" >/dev/null ||
      fail "pinned Caddy validation image is unavailable: $caddy_image"
  fi
  local config
  for config in Caddyfile Caddyfile.local Caddyfile.maintenance Caddyfile.maintenance.local; do
    docker run --rm \
      -e HAPPYLEARN_DOMAIN=learn.example.invalid \
      -v "$deploy_dir:/phase6-config:ro" \
      "$caddy_image" caddy validate \
      --config "/phase6-config/$config" --adapter caddyfile >/dev/null 2>&1 ||
      fail "$config is not accepted by Caddy"
  done
}

case "$scope" in
  all) assert_compose; assert_caddy ;;
  compose) assert_compose ;;
  caddy) assert_caddy ;;
  caddy-runtime) assert_caddy_runtime ;;
esac

echo "Phase 6 production contract: PASS ($scope)"
