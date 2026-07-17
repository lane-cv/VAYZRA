#!/usr/bin/env bash
set -Eeuo pipefail

nonce="$(date +%s)-$RANDOM"
prefix="happylearn_e2e_${nonce}"
network="${prefix}_net"
postgres="${prefix}_postgres"
redis="${prefix}_redis"
app="${prefix}_app"
database="${prefix}"
tmpdir="$(mktemp -d)"
redis_db=15
admin_password="E2E Admin ${nonce}!"
student_password="E2E Temporary ${nonce}!"
student_new_password="E2E Changed ${nonce}!"

cleanup() {
  status=$?
  docker rm -f "$app" >/dev/null 2>&1 || true
  if docker ps -a --format '{{.Names}}' | grep -Fxq "$postgres" && [[ "$database" == happylearn_e2e_* ]]; then
    docker exec "$postgres" psql -U happylearn -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE IF EXISTS \"$database\"" >/dev/null 2>&1 || true
  fi
  if docker ps -a --format '{{.Names}}' | grep -Fxq "$redis"; then docker exec "$redis" redis-cli -n "$redis_db" FLUSHDB >/dev/null 2>&1 || true; fi
  docker rm -f "$postgres" "$redis" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf "$tmpdir"
  exit "$status"
}
trap cleanup EXIT INT TERM

wait_for() {
  local label="$1" container="$2"; shift 2
  local attempt=1 max_attempts=60
  until "$@"; do
    if (( attempt >= max_attempts )); then
      echo "timed out waiting for $label after ${max_attempts}s" >&2
      docker ps -a --filter "name=^/${container}$" --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}' >&2 || true
      docker inspect --format '{{json .State}}' "$container" >&2 || true
      docker logs --tail 100 "$container" >&2 || true
      return 1
    fi
    sleep 1
    ((attempt++))
  done
}

docker network create "$network" >/dev/null
docker run -d --name "$postgres" --network "$network" -e POSTGRES_USER=happylearn -e POSTGRES_PASSWORD=happylearn_e2e -e POSTGRES_DB=postgres postgres:18.4 >/dev/null
docker run -d --name "$redis" --network "$network" redis:8.8 >/dev/null
wait_for "PostgreSQL readiness" "$postgres" docker exec "$postgres" pg_isready -U happylearn -d postgres >/dev/null
docker exec "$postgres" psql -U happylearn -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"$database\"" >/dev/null

port=$((20000 + RANDOM % 20000))
base_url="http://127.0.0.1:$port"
env_file="$tmpdir/app.env"
umask 077
cat > "$env_file" <<EOF
HAPPYLEARN_ENV=development
HAPPYLEARN_LISTEN=:8080
HAPPYLEARN_DATABASE_URL=postgres://happylearn:happylearn_e2e@postgres:5432/$database?sslmode=disable
HAPPYLEARN_REDIS_URL=redis://redis:6379/$redis_db
HAPPYLEARN_LOGIN_THROTTLE_SECRET=local-e2e-throttle-secret-$nonce
HAPPYLEARN_PUBLIC_ORIGIN=$base_url
EOF

docker build -t happylearn:e2e . >/dev/null
docker run -d --name "$app" --read-only --tmpfs /tmp:rw,noexec,nosuid,size=16m --user 10001:10001 --network "$network" --network-alias app --env-file "$env_file" -p 127.0.0.1:$port:8080 happylearn:e2e >/dev/null
wait_for "application readiness" "$app" curl --fail --silent "$base_url/api/v1/health/ready" >/dev/null

docker run --rm --network "$network" -e HAPPYLEARN_ENV=development -e HAPPYLEARN_DATABASE_URL="postgres://happylearn:happylearn_e2e@postgres:5432/$database?sslmode=disable" -e HAPPYLEARN_REDIS_URL="redis://redis:6379/$redis_db" -e HAPPYLEARN_LOGIN_THROTTLE_SECRET="local-e2e-throttle-secret-$nonce" -e HAPPYLEARN_PUBLIC_ORIGIN="$base_url" -e E2E_ADMIN_PASSWORD="$admin_password" -v "$PWD:/src:ro" -w /src golang:1.26.5-bookworm sh -c 'umask 077; printf %s "$E2E_ADMIN_PASSWORD" >/tmp/password; go run ./cmd/admin create-teacher --username admin --display-name "E2E Teacher" --password-file /tmp/password; rm -f /tmp/password' >/dev/null

E2E_BASE_URL="$base_url" E2E_ADMIN_PASSWORD="$admin_password" E2E_STUDENT_PASSWORD="$student_password" E2E_STUDENT_NEW_PASSWORD="$student_new_password" pnpm exec playwright test tests/e2e/auth-students.spec.ts
