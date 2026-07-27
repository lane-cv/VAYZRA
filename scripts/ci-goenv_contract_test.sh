#!/usr/bin/env bash
set -Eeuo pipefail

fail() {
  echo "CI Go environment contract: FAIL: $*" >&2
  exit 1
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

printf '%s\n' \
  'module example.com/ci-goenv-contract' \
  '' \
  'go 1.26.0' \
  >"$tmp_dir/go.mod"
printf '%s\n' \
  'package sentinel' \
  '' \
  'import (' \
  '	"os"' \
  '	"testing"' \
  ')' \
  '' \
  'func TestSentinel(t *testing.T) {' \
  '	if err := os.WriteFile(os.Getenv("SENTINEL_FILE"), []byte("executed\n"), 0o600); err != nil {' \
  '		t.Fatal(err)' \
  '	}' \
  '}' \
  >"$tmp_dir/sentinel_test.go"

runner="$tmp_dir/run-contract.sh"
printf '%s\n' \
  '#!/bin/sh' \
  'set -eu' \
  'cd "$WORKSPACE"' \
  'GOENV="$WORKSPACE/goenv" "$GO_BIN" env -w "GOFLAGS=-run=^$"' \
  'rm -f "$WORKSPACE/sentinel-ran"' \
  'GOENV="$WORKSPACE/goenv" GOFLAGS="" SENTINEL_FILE="$WORKSPACE/sentinel-ran" "$GO_BIN" test ./... -count=1 >/dev/null' \
  'test ! -e "$WORKSPACE/sentinel-ran" || { echo "GOFLAGS empty unexpectedly overrode GOENV" >&2; exit 1; }' \
  'GOENV=off GOFLAGS="" SENTINEL_FILE="$WORKSPACE/sentinel-ran" "$GO_BIN" test ./... -count=1 >/dev/null' \
  'grep -Fxq executed "$WORKSPACE/sentinel-ran" || { echo "GOENV=off did not execute the sentinel test" >&2; exit 1; }' \
  >"$runner"
chmod +x "$runner"

if command -v go >/dev/null 2>&1; then
  WORKSPACE="$tmp_dir" GO_BIN="$(command -v go)" "$runner" ||
    fail "isolated host Go behavior check failed"
elif command -v docker >/dev/null 2>&1; then
  docker run --rm \
    -e WORKSPACE=/work \
    -e GO_BIN=go \
    -v "$tmp_dir:/work" \
    -w /work \
    golang:1.26.5 \
    /bin/sh /work/run-contract.sh ||
    fail "isolated golang:1.26.5 behavior check failed"
else
  fail "neither Go nor Docker is available for the behavior check"
fi

echo "CI Go environment contract: PASS"
