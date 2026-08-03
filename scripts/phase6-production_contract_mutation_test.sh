#!/usr/bin/env bash
# shellcheck disable=SC2016 # Mutation snippets are evaluated later with MUTATION_DIR exported.
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
source_dir="$repo_root/deploy"
contract="$repo_root/scripts/phase6-production_contract_test.sh"

fail() {
  echo "Phase 6 production mutation contract: FAIL: $*" >&2
  exit 1
}

grep -Fq 'caddy:2.10.2-alpine@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d' "$contract" ||
  fail 'Caddy validation image is not immutable'
grep -Fq '300s docker pull "$caddy_image"' "$contract" ||
  fail 'fresh-runner Caddy validation image acquisition is not bounded'

for required in compose.prod.yml compose.prod.local.yml Caddyfile Caddyfile.local Caddyfile.maintenance Caddyfile.maintenance.local maintenance.html; do
  test -f "$source_dir/$required" || fail "missing deploy/$required"
done

tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT

run_mutation() {
  local name="$1"
  local mutation="$2"
  local case_dir="$tmp_root/$name"
  mkdir "$case_dir"
  cp "$source_dir/compose.prod.yml" "$source_dir/compose.prod.local.yml" "$source_dir/Caddyfile" \
    "$source_dir/Caddyfile.local" "$source_dir/Caddyfile.maintenance" "$source_dir/Caddyfile.maintenance.local" \
    "$source_dir/maintenance.html" "$case_dir/"
  MUTATION_DIR="$case_dir" bash -c "$mutation" || fail "could not apply mutation: $name"
  if HAPPYLEARN_PHASE6_DEPLOY_DIR="$case_dir" bash "$contract" >"$case_dir/output" 2>&1; then
    fail "contract accepted unsafe mutation: $name"
  fi
}

run_mutation publish-postgres \
  'sed -i "/^  postgres:/a\\    ports: [\"0.0.0.0:5432:5432\"]" "$MUTATION_DIR/compose.prod.yml"'
run_mutation public-private-network \
  'sed -i "/^  private:/,/^[^ ]/s/internal: true/internal: false/" "$MUTATION_DIR/compose.prod.yml"'
run_mutation floating-image \
  'sed -i "0,/image: /s|image: .*|image: postgres:latest|" "$MUTATION_DIR/compose.prod.yml"'
run_mutation docker-socket \
  'sed -i "/^  app:/a\\    volumes: [\"/var/run/docker.sock:/var/run/docker.sock\"]" "$MUTATION_DIR/compose.prod.yml"'
run_mutation writable-app-root \
  'sed -i "0,/read_only: true/s/read_only: true/read_only: false/" "$MUTATION_DIR/compose.prod.yml"'
run_mutation app-capabilities \
  'sed -i "0,/cap_drop: \[ALL\]/s/cap_drop: \[ALL\]/cap_drop: [NET_ADMIN]/" "$MUTATION_DIR/compose.prod.yml"'
run_mutation literal-password \
  'sed -i "/^    environment:/a\\      HAPPYLEARN_DATABASE_URL: postgres://literal:password@postgres/db" "$MUTATION_DIR/compose.prod.yml"'
run_mutation excess-resources \
  'sed -i "0,/cpus: /s/cpus: .*/cpus: 9.0/" "$MUTATION_DIR/compose.prod.yml"'
run_mutation proxy-internal \
  'sed -i "/handle \/internal\/\\*/a\\        reverse_proxy app:8080" "$MUTATION_DIR/Caddyfile"'
run_mutation query-bearing-log \
  'sed -i "s/log_append uri_path {http.request.uri.path}/log_append uri_path {http.request.uri}/" "$MUTATION_DIR/Caddyfile"'
run_mutation missing-upload-limit \
  'sed -i "/max_size 9MiB/d" "$MUTATION_DIR/Caddyfile"'
run_mutation missing-retry-after \
  'sed -i "/Retry-After/d" "$MUTATION_DIR/Caddyfile.maintenance"'

echo "Phase 6 production mutation contract: PASS (12/12)"
