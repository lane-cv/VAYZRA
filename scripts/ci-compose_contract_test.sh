#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
base="$repo_root/deploy/compose.dev.yml"
ci="$repo_root/deploy/compose.ci.yml"
workflow="$repo_root/.github/workflows/verify.yml"

fail() {
  echo "CI Compose host-port contract: FAIL: $*" >&2
  exit 1
}

exact_line() {
  local needle="$1"
  local matches

  matches="$(grep -nFx -- "$needle" "$workflow" || true)"
  test -n "$matches" || fail "missing workflow line: $needle"
  test "$matches" = "${matches%%$'\n'*}" ||
    fail "workflow line must occur exactly once: $needle"
  printf '%s\n' "${matches%%:*}"
}

line_is() {
  local line_number="$1"
  local expected="$2"
  local actual

  actual="$(sed -n "${line_number}p" "$workflow")"
  test "$actual" = "$expected" ||
    fail "unexpected workflow line $line_number: expected: $expected"
}

test -f "$ci" || fail "missing deploy/compose.ci.yml"

ci_structure="$(
  sed -e '/^[[:space:]]*#/d' -e '/^[[:space:]]*$/d' "$ci"
)"
expected_ci_structure=$'networks:\n  happylearn:\n    internal: false'
test "$ci_structure" = "$expected_ci_structure" ||
  fail "CI override must contain only networks.happylearn.internal=false"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
if test -z "${HAPPYLEARN_AISTOR_LICENSE_FILE:-}"; then
  HAPPYLEARN_AISTOR_LICENSE_FILE="$tmp_dir/minio.license"
  export HAPPYLEARN_AISTOR_LICENSE_FILE
  touch "$HAPPYLEARN_AISTOR_LICENSE_FILE"
fi

base_json="$tmp_dir/base.json"
merged_json="$tmp_dir/merged.json"
docker compose -f "$base" config --format json >"$base_json"
docker compose -f "$base" -f "$ci" config --format json >"$merged_json"

jq -e '.networks.happylearn.internal == true' "$base_json" >/dev/null ||
  fail "base networks.happylearn.internal must be true"
jq -e '
  (.networks.happylearn | type == "object") and
  ((.networks.happylearn | has("internal") | not) or
   .networks.happylearn.internal == false)
' "$merged_json" >/dev/null ||
  fail "merged networks.happylearn.internal must be effectively false"

for config in "$base_json" "$merged_json"; do
  jq -e '
    [.services[]?.ports[]?] as $ports |
    ($ports | length) > 0 and
    all($ports[]; .host_ip == "127.0.0.1")
  ' "$config" >/dev/null ||
    fail "every published port must bind strictly to 127.0.0.1"
done

startup_name_line="$(exact_line '      - name: Start private integration dependencies')"
startup_run_line="$(exact_line '        run: docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml up -d --wait --wait-timeout 120 postgres redis minio')"
probe_name_line="$(exact_line '      - name: Verify host integration ports')"
probe_run_line="$((probe_name_line + 1))"
probe_for_line="$((probe_name_line + 2))"
probe_timeout_line="$((probe_name_line + 3))"
probe_done_line="$((probe_name_line + 4))"
merged_validation_line="$(exact_line '      - run: docker compose -f deploy/compose.dev.yml -f deploy/compose.ci.yml config --quiet')"
cleanup_name_line="$(exact_line '      - name: Stop integration dependencies')"
cleanup_if_line="$(exact_line '        if: always()')"
cleanup_run_line="$(exact_line '        run: docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml down --volumes --remove-orphans')"

test "$startup_run_line" -eq "$((startup_name_line + 1))" ||
  fail "startup command must belong to the named startup step"
test "$probe_name_line" -eq "$((startup_run_line + 1))" ||
  fail "host-port probe must immediately follow dependency startup"
line_is "$probe_run_line" '        run: |'
line_is "$probe_for_line" '          for port in 54329 56379 59000; do'
line_is "$probe_timeout_line" '            timeout 30 bash -c "until </dev/tcp/127.0.0.1/$port; do sleep 1; done"'
line_is "$probe_done_line" '          done'
go_test_before_probe_line="$(
  awk -v probe_line="$probe_name_line" '
    function starts_go_test(command) {
      sub(/^[[:space:]]+/, "", command)
      sub(/^["\047]/, "", command)
      return command ~ /^go[[:space:]]+test([[:space:]]|$)/
    }

    NR >= probe_line { next }

    in_run_block {
      if ($0 ~ /^          /) {
        command = $0
        sub(/^          /, "", command)
        if (command !~ /^[[:space:]]*#/ && starts_go_test(command)) {
          print NR
          exit
        }
        next
      }
      in_run_block = 0
    }

    /^(      - |        )run:[[:space:]]*/ {
      command = $0
      sub(/^[[:space:]]*(- )?run:[[:space:]]*/, "", command)
      if (command ~ /^[|>][+-]?([[:space:]]+#.*)?$/) {
        in_run_block = 1
      } else if (starts_go_test(command)) {
        print NR
        exit
      }
    }
  ' "$workflow"
)"
test -z "$go_test_before_probe_line" ||
  fail "go test run step at line $go_test_before_probe_line precedes host-port verification"
test "$merged_validation_line" -lt "$cleanup_name_line" ||
  fail "merged Compose validation must precede cleanup"
test "$cleanup_if_line" -eq "$((cleanup_name_line + 1))" &&
  test "$cleanup_run_line" -eq "$((cleanup_if_line + 1))" ||
  fail "cleanup must always run with the startup project and Compose files"

echo 'CI Compose host-port contract: PASS'
