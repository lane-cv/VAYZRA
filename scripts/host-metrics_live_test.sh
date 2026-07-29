#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
suffix="$(date -u '+%Y%m%d%H%M%S')-$$"
postgres_container="vayzra-host-metrics-postgres-$suffix"
redis_container="vayzra-host-metrics-redis-$suffix"
fixture_root="$(mktemp -d "${TMPDIR:-/tmp}/host-metrics-live.XXXXXX")"

cleanup() {
  docker container rm --force \
    "$postgres_container" "$redis_container" >/dev/null 2>&1 || true
  rm -rf "$fixture_root"
}
trap cleanup EXIT HUP INT TERM

fail() {
  printf '%s\n' 'host metrics live: FAIL' >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail
docker info >/dev/null 2>&1 || fail

docker run --detach \
  --name "$postgres_container" \
  --publish 127.0.0.1::5432 \
  --env POSTGRES_USER=happylearn \
  --env POSTGRES_PASSWORD=happylearn_dev \
  --env POSTGRES_DB=happylearn \
  postgres:18.4 >/dev/null || fail
docker run --detach \
  --name "$redis_container" \
  --publish 127.0.0.1::6379 \
  redis:8.8 >/dev/null || fail

postgres_ready=0
redis_ready=0
for _ in $(seq 1 60); do
  if docker exec "$postgres_container" \
    pg_isready -U happylearn -d happylearn >/dev/null 2>&1; then
    postgres_ready=1
  fi
  if docker exec "$redis_container" \
    redis-cli ping 2>/dev/null | grep -Fxq PONG; then
    redis_ready=1
  fi
  if ((postgres_ready == 1 && redis_ready == 1)); then
    break
  fi
  sleep 1
done
((postgres_ready == 1 && redis_ready == 1)) || fail

postgres_port="$(
  docker port "$postgres_container" 5432/tcp |
    awk -F: 'NR == 1 { print $NF }'
)" || fail
redis_port="$(
  docker port "$redis_container" 6379/tcp |
    awk -F: 'NR == 1 { print $NF }'
)" || fail
[[ "$postgres_port" =~ ^[0-9]+$ && "$redis_port" =~ ^[0-9]+$ ]] || fail

case "$(uname -s)" in
  Darwin)
    host_goos=darwin
    ;;
  Linux)
    host_goos=linux
    ;;
  *)
    fail
    ;;
esac
case "$(uname -m)" in
  arm64 | aarch64)
    host_goarch=arm64
    ;;
  x86_64 | amd64)
    host_goarch=amd64
    ;;
  *)
    fail
    ;;
esac

module_cache="${HAPPYLEARN_HOST_METRICS_GO_MOD_CACHE:-$fixture_root/gomod}"
build_cache="${HAPPYLEARN_HOST_METRICS_GO_BUILD_CACHE:-$fixture_root/gocache}"
mkdir -p "$module_cache" "$build_cache"
docker run --rm \
  --volume "$ROOT:/src:ro" \
  --volume "$fixture_root:/out" \
  --volume "$module_cache:/gomod" \
  --volume "$build_cache:/gocache" \
  --env GOMODCACHE=/gomod \
  --env GOCACHE=/gocache \
  --env GOOS="$host_goos" \
  --env GOARCH="$host_goarch" \
  --env CGO_ENABLED=0 \
  --workdir /src \
  golang:1.26.5-bookworm \
  sh -ceu '
    go build -o /out/host-sampler ./cmd/host-sampler
    go test -c -o /out/host-sampler-live.test ./cmd/host-sampler
  ' || fail
chmod 0700 "$fixture_root/host-sampler" \
  "$fixture_root/host-sampler-live.test" || fail

HAPPYLEARN_HOST_METRICS_LIVE=1 \
HAPPYLEARN_TEST_DATABASE_URL="postgres://happylearn:happylearn_dev@127.0.0.1:$postgres_port/happylearn?sslmode=disable&connect_timeout=5" \
HAPPYLEARN_TEST_REDIS_ADDR="127.0.0.1:$redis_port" \
HAPPYLEARN_HOST_METRICS_LIVE_SAMPLER="$fixture_root/host-sampler" \
HAPPYLEARN_HOST_METRICS_REPOSITORY_ROOT="$ROOT" \
  "$fixture_root/host-sampler-live.test" \
    -test.run '^TestHostSamplerLiveIntegration$' \
    -test.count=1 \
    -test.v || fail

printf '%s\n' 'host metrics live: PASS samples=27 nonceKeys=1'
