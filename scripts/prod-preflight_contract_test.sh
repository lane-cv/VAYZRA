#!/usr/bin/env bash
set -Eeuo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
script=$root/scripts/prod-preflight.sh
[[ -f $script ]] || { echo 'prod preflight contract: FAIL'; exit 1; }
for literal in '--project-dir' '--env-file' '--manifest' '--mode' '--expected-host-address' 'image_not_immutable' 'configuration_hash_mismatch' 'recovery_too_old' 'public_port_occupied' 'compose.prod.yml' 'docker manifest inspect' 'HAPPYLEARN_LOCAL_' 'FAILURE_INJECTION' 'server_test_variable_rejected'; do
  grep -F -- "$literal" "$script" >/dev/null || { echo "prod preflight contract: FAIL: $literal"; exit 1; }
done
for forbidden in ' compose up' ' compose down' ' compose start' ' compose stop' 'apt-get' 'dnf ' 'ufw ' 'firewall-cmd' 'pg_restore' 'restic restore'; do
  ! grep -F -- "$forbidden" "$script" >/dev/null || { echo "prod preflight contract: FAIL: mutation $forbidden"; exit 1; }
done
bash -n "$script"

fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
chmod 0700 "$fixture"
mkdir -p "$fixture/project/deploy" "$fixture/secrets" "$fixture/backups" "$fixture/state/recovery" "$fixture/state/backup-workflows" "$fixture/state/release-input" "$fixture/bin"
chmod 0700 "$fixture/project" "$fixture/project/deploy" "$fixture/secrets" "$fixture/backups" "$fixture/state" "$fixture/state/recovery" "$fixture/state/backup-workflows" "$fixture/state/release-input" "$fixture/bin"
printf 'services: {}\n' >"$fixture/project/deploy/compose.prod.yml"
printf ':80 { respond "ok" 200 }\n' >"$fixture/project/deploy/Caddyfile"
printf ':80 { respond "maintenance" 503 }\n' >"$fixture/project/deploy/Caddyfile.maintenance"
printf 'maintenance\n' >"$fixture/project/deploy/maintenance.html"
printf 'resolved-compose\n' >"$fixture/resolved"
chmod 0600 "$fixture/project/deploy/"* "$fixture/resolved"

secret_names=(postgres-password redis-password minio-access-key minio-secret-key aistor-license app-database-url app-redis-url app-login-throttle app-minio-access-key app-minio-secret-key app-ai-master-key worker-database-url worker-redis-url worker-login-throttle worker-minio-access-key worker-minio-secret-key worker-ai-master-key metrics-bearer host-metrics-hmac backup-password backup-age-identity backup-database-password backup-local-repository)
for name in "${secret_names[@]}"; do printf 'fixture-value\n' >"$fixture/secrets/$name"; chmod 0600 "$fixture/secrets/$name"; done
chmod 0711 "$fixture/secrets"
digest=$(printf 'a%.0s' {1..64})
compose_hash=$(sha256sum "$fixture/resolved" | awk '{print $1}')
caddy_hash=$(sha256sum "$fixture/project/deploy/Caddyfile" | awk '{print $1}')
verified_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
printf '{"status":"verified","evidenceId":"backup-fixture","verifiedAt":"%s"}\n' "$verified_at" >"$fixture/state/recovery/latest.json"
chmod 0600 "$fixture/state/recovery/latest.json"

cat >"$fixture/env" <<EOF
COMPOSE_PROJECT_NAME=happylearn-prod
HAPPYLEARN_DOMAIN=learn.example.invalid
HAPPYLEARN_TIMEZONE=Asia/Shanghai
HAPPYLEARN_APP_IMAGE=example/app@sha256:$digest
HAPPYLEARN_WORKER_IMAGE=example/worker@sha256:$digest
HAPPYLEARN_MIGRATE_IMAGE=example/migrate@sha256:$digest
HAPPYLEARN_BACKUP_IMAGE=example/backup@sha256:$digest
HAPPYLEARN_CADDY_IMAGE=example/caddy@sha256:$digest
HAPPYLEARN_POSTGRES_IMAGE=example/postgres@sha256:$digest
HAPPYLEARN_REDIS_IMAGE=example/redis@sha256:$digest
HAPPYLEARN_MINIO_IMAGE=example/minio@sha256:$digest
HAPPYLEARN_BACKUP_HOST_PATH=$fixture/backups
HAPPYLEARN_RELEASE_STATE_PATH=$fixture/state
HAPPYLEARN_SECRET_DIR=$fixture/secrets
HAPPYLEARN_CADDYFILE=$fixture/project/deploy/Caddyfile
HAPPYLEARN_MAINTENANCE_CADDYFILE=$fixture/project/deploy/Caddyfile.maintenance
HAPPYLEARN_MAINTENANCE_FILE=$fixture/project/deploy/maintenance.html
EOF
chmod 0600 "$fixture/env"
cat >"$fixture/manifest" <<EOF
{"version":"6.0.0","commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","builtAt":"2026-08-01T00:00:00Z","images":{"app":"example/app@sha256:$digest","worker":"example/worker@sha256:$digest","migrate":"example/migrate@sha256:$digest","backup":"example/backup@sha256:$digest","caddy":"example/caddy@sha256:$digest","postgres":"example/postgres@sha256:$digest","redis":"example/redis@sha256:$digest","minio":"example/minio@sha256:$digest"},"minSchemaVersion":27,"maxSchemaVersion":27,"composeSha256":"$compose_hash","caddySha256":"$caddy_hash","backupEvidenceId":"backup-fixture","createdBy":"contract-test","createdAt":"2026-08-01T00:00:00Z"}
EOF
chmod 0600 "$fixture/manifest"
cat >"$fixture/bin/docker" <<'EOF'
#!/usr/bin/env bash
if [[ ${FIXTURE_IMAGE_UNAVAILABLE:-0} == 1 && ( ${1:-} == manifest || ${1:-} == image ) ]]; then exit 1; fi
if [[ ${1:-} == compose ]]; then
  [[ ${FIXTURE_COMPOSE_INVALID:-0} != 1 ]] || exit 1
  cat "$FIXTURE_RESOLVED"
  exit 0
fi
exit 0
EOF
chmod 0755 "$fixture/bin/docker"
cat >"$fixture/bin/df" <<'EOF'
#!/usr/bin/env bash
if [[ ${FIXTURE_SMALL_DISK:-0} == 1 ]]; then printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\nfixture 100 99 1 99%% /\n'; exit 0; fi
exec /usr/bin/df "$@"
EOF
chmod 0755 "$fixture/bin/df"
cat >"$fixture/bin/stat" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
format=''
if [[ ${1:-} == -c ]]; then format=${2:-}; shift 2; fi
[[ ${1:-} != -- ]] || shift
path=${1:-}
if [[ $format == '%a %u %s' && $path == "$FIXTURE_ROOT/secrets/"* ]]; then
  name=${path##*/}; uid=''
  case $name in
    postgres-password|redis-password) uid=999 ;;
    minio-access-key|minio-secret-key|aistor-license) uid=1000 ;;
    app-*|metrics-bearer|host-metrics-hmac) uid=10001 ;;
    worker-*) uid=10002 ;;
    backup-*) uid=10003 ;;
  esac
  [[ -n $uid ]] || exit 1
  printf '%s %s %s\n' "$(/usr/bin/stat -c %a -- "$path")" "$uid" "$(/usr/bin/stat -c %s -- "$path")"
  exit 0
fi
if [[ $format == '%u:%g:%a' ]]; then
  case $path in
    "$FIXTURE_ROOT/backups"|"$FIXTURE_ROOT/state/backup-workflows") printf '10003:0:700\n'; exit 0 ;;
    "$FIXTURE_ROOT/state/release-input") printf '10001:10001:700\n'; exit 0 ;;
  esac
fi
if [[ -n $format ]]; then exec /usr/bin/stat -c "$format" -- "$path"; fi
exec /usr/bin/stat "$@"
EOF
chmod 0755 "$fixture/bin/stat"
export FIXTURE_RESOLVED="$fixture/resolved"
export FIXTURE_ROOT="$fixture"
PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local | grep -F '"category":"preflight_passed"' >/dev/null || { echo 'prod preflight contract: FAIL: valid fixture'; exit 1; }

mv "$fixture/secrets/app-database-url" "$fixture/secrets/app-database-url.safe"
printf 'unsafe\n' >"$fixture/secrets/app-database-url"
chmod 0640 "$fixture/secrets/app-database-url"
if PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: unsafe secret'; exit 1; fi
rm "$fixture/secrets/app-database-url"
mv "$fixture/secrets/app-database-url.safe" "$fixture/secrets/app-database-url"

mv "$fixture/secrets/app-database-url" "$fixture/secrets/app-database-url.real"
ln -s "$fixture/secrets/app-database-url.real" "$fixture/secrets/app-database-url"
if PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: symlink secret'; exit 1; fi
rm "$fixture/secrets/app-database-url"
mv "$fixture/secrets/app-database-url.real" "$fixture/secrets/app-database-url"

if FIXTURE_SMALL_DISK=1 PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: small disk'; exit 1; fi
if FIXTURE_COMPOSE_INVALID=1 PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: malformed compose'; exit 1; fi
if FIXTURE_IMAGE_UNAVAILABLE=1 PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: unavailable image'; exit 1; fi

cp "$fixture/env" "$fixture/env.saved"
sed -i "s#example/app@sha256:$digest#example/app:latest#" "$fixture/env"
if PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: floating image'; exit 1; fi
mv "$fixture/env.saved" "$fixture/env"

cp "$fixture/manifest" "$fixture/manifest.saved"
sed -i "s#$compose_hash#$(printf 'f%.0s' {1..64})#" "$fixture/manifest"
if PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: hash mismatch'; exit 1; fi
mv "$fixture/manifest.saved" "$fixture/manifest"

mv "$fixture/state/recovery/latest.json" "$fixture/state/recovery/latest.saved"
if PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: missing recovery'; exit 1; fi
mv "$fixture/state/recovery/latest.saved" "$fixture/state/recovery/latest.json"

printf '{"previousManifestSha256":"%s"}\n' "$(printf 'e%.0s' {1..64})" >"$fixture/state/release-state.json"
chmod 0600 "$fixture/state/release-state.json"
if PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: unavailable previous manifest'; exit 1; fi
rm "$fixture/state/release-state.json"

printf '{"status":"verified","evidenceId":"backup-fixture","verifiedAt":"2020-01-01T00:00:00Z"}\n' >"$fixture/state/recovery/latest.json"
if PATH="$fixture/bin:$PATH" bash "$script" --project-dir "$fixture/project" --env-file "$fixture/env" --manifest "$fixture/manifest" --mode local >/dev/null 2>&1; then echo 'prod preflight contract: FAIL: stale recovery'; exit 1; fi
echo 'prod preflight contract: PASS'
