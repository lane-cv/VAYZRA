#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
base="$repo_root/deploy/compose.dev.yml"
ci="$repo_root/deploy/compose.ci.yml"
workflow="${HAPPYLEARN_CI_COMPOSE_CONTRACT_WORKFLOW:-$repo_root/.github/workflows/verify.yml}"

fail() {
  echo "CI Compose host-port contract: FAIL: $*" >&2
  exit 1
}

assert_log_rotation() {
  local config="$1"
  local label="$2"

  if jq -e '
    (.services | type == "object" and length > 0) and
    all(
      .services | to_entries[];
      .value.logging == {
        "driver": "json-file",
        "options": {
          "max-file": "5",
          "max-size": "10m"
        }
      }
    )
  ' "$config" >/dev/null; then
    return 0
  fi

  echo "CI Compose log-rotation contract: FAIL: every parsed service in $label must use json-file with max-size=10m and max-file=5" >&2
  return 1
}

assert_expected_services() {
  local config="$1"
  local label="$2"
  local expected_services='[
    "app",
    "app-secrets-init",
    "backup",
    "backup-secrets-init",
    "backup-storage-init",
    "minio",
    "minio-data-init",
    "postgres",
    "postgres-tls-init",
    "redis",
    "worker"
  ]'

  if jq -e --argjson expected "$expected_services" \
    '(.services | keys) == $expected' "$config" >/dev/null; then
    return 0
  fi

  echo "CI Compose log-rotation contract: FAIL: $label must contain the complete expected service set" >&2
  return 1
}

verify_log_rotation_mutations() {
  local config="$1"
  local label="$2"
  local mutated="$tmp_dir/log-rotation-mutated.json"
  local service
  local option

  while IFS= read -r service; do
    for option in max-size max-file; do
      jq --arg service "$service" --arg option "$option" \
        'del(.services[$service].logging.options[$option])' \
        "$config" >"$mutated"
      if assert_log_rotation "$mutated" "$label mutation" >/dev/null 2>&1; then
        fail "log-rotation assertion accepted $label service $service without $option"
      fi
    done

    jq --arg service "$service" \
      'del(.services[$service].logging.driver)' \
      "$config" >"$mutated"
    if assert_log_rotation "$mutated" "$label mutation" >/dev/null 2>&1; then
      fail "log-rotation assertion accepted $label service $service without logging.driver"
    fi
  done < <(jq -r '.services | keys[]' "$config")
}

test -f "$workflow" ||
  fail "workflow contract input must be a regular file: $workflow"

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

exact_line_between() {
  local needle="$1"
  local first_line="$2"
  local last_line="$3"
  local matches

  matches="$(
    awk -v needle="$needle" -v first="$first_line" -v last="$last_line" '
      NR >= first && NR <= last && $0 == needle { print NR }
    ' "$workflow"
  )"
  test -n "$matches" || fail "missing verify-job workflow line: $needle"
  test "$matches" = "${matches%%$'\n'*}" ||
    fail "verify-job workflow line must occur exactly once: $needle"
  printf '%s\n' "$matches"
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
docker compose --profile '*' -f "$base" config --format json >"$base_json"
docker compose --profile '*' -f "$base" -f "$ci" config --format json >"$merged_json"

assert_expected_services "$base_json" "base Compose config" ||
  fail "base Compose service-set contract is not satisfied"
assert_expected_services "$merged_json" "base+CI merged Compose config" ||
  fail "merged Compose service-set contract is not satisfied"
assert_log_rotation "$base_json" "base Compose config" ||
  fail "base Compose log-rotation contract is not satisfied"
assert_log_rotation "$merged_json" "base+CI merged Compose config" ||
  fail "merged Compose log-rotation contract is not satisfied"
verify_log_rotation_mutations "$base_json" "base Compose config"
verify_log_rotation_mutations "$merged_json" "base+CI merged Compose config"

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

verify_job_line="$(exact_line '  verify:')"
verify_job_end="$(
  awk -v verify_line="$verify_job_line" '
    NR > verify_line && /^  [^[:space:]][^:]*:$/ {
      print NR - 1
      exit
    }
  ' "$workflow"
)"
test -n "$verify_job_end" || fail "verify job has no following job boundary"

license_permission_violation="$(
  awk '
    $0 == "          printf \047%s\047 \"\$AISTOR_LICENSE\" > \"\$license_file\"" {
      configured++
      if ((getline) <= 0 || $0 != "          sudo chgrp 0 \"\$license_file\"") {
        failed = 1
        print "AIStor license must grant the container root group immediately after creation"
        exit
      }
      if ((getline) <= 0 || $0 != "          chmod 0440 \"\$license_file\"") {
        failed = 1
        print "AIStor license must be made container-readable immediately after creation"
        exit
      }
      if ((getline) <= 0 || $0 != "          printf \047HAPPYLEARN_AISTOR_LICENSE_FILE=%s\\n\047 \"\$license_file\" >> \"\$GITHUB_ENV\"") {
        failed = 1
        print "AIStor license path must be exported immediately after permission hardening"
        exit
      }
      while ((getline) > 0) {
        if ($0 ~ /^[[:space:]]*$/) {
          continue
        }
        if ($0 ~ /^      - /) {
          break
        }
        failed = 1
        print "AIStor license configuration step must end immediately after exporting the path"
        exit
      }
    }
    END {
      if (!failed && configured != 3) {
        print "workflow must configure exactly three hardened AIStor license files"
      }
    }
  ' "$workflow"
)"
test -z "$license_permission_violation" || fail "$license_permission_violation"

workflow_structure_violation="$(
  awk -v verify_first="$verify_job_line" -v verify_last="$verify_job_end" '
    /^[^[:space:]#]/ {
      if ($0 !~ /^(name|on|permissions|jobs):/) {
        print "workflow contains a noncanonical top-level key at line " NR
        failed = 1
        exit
      }
      key = $0
      sub(/:.*/, "", key)
      root_count[key]++
    }

    NR >= verify_first && NR <= verify_last &&
      /^    [^[:space:]#]/ {
      if ($0 !~ /^    (runs-on|timeout-minutes|steps):/) {
        print "verify job contains a noncanonical top-level key at line " NR
        failed = 1
        exit
      }
      key = $0
      sub(/^    /, "", key)
      sub(/:.*/, "", key)
      verify_count[key]++
    }

    END {
      if (failed) {
        exit
      }
      if (root_count["name"] != 1 ||
          root_count["on"] != 1 ||
          root_count["permissions"] != 1 ||
          root_count["jobs"] != 1) {
        print "workflow canonical top-level keys must each occur exactly once"
        exit
      }
      if (verify_count["runs-on"] != 1 ||
          verify_count["timeout-minutes"] != 1 ||
          verify_count["steps"] != 1) {
        print "verify job canonical top-level keys must each occur exactly once"
      }
    }
  ' "$workflow"
)"
test -z "$workflow_structure_violation" || fail "$workflow_structure_violation"

startup_name_line="$(exact_line_between '      - name: Start private integration dependencies' "$verify_job_line" "$verify_job_end")"
startup_run_line="$(exact_line_between '        run: docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml up -d --wait --wait-timeout 120 postgres redis minio' "$verify_job_line" "$verify_job_end")"
probe_name_line="$(exact_line_between '      - name: Verify host integration ports' "$verify_job_line" "$verify_job_end")"
probe_run_line="$((probe_name_line + 1))"
probe_for_line="$((probe_name_line + 2))"
probe_timeout_line="$((probe_name_line + 3))"
probe_done_line="$((probe_name_line + 4))"
go_test_line="$(exact_line_between "      - run: GOENV=off GOFLAGS='' go test -p 1 ./... -count=1" "$verify_job_line" "$verify_job_end")"
go_race_test_line="$(exact_line_between "      - run: GOENV=off GOFLAGS='' go test -race -p 1 ./... -count=1" "$verify_job_line" "$verify_job_end")"
minio_probe_name_line="$(exact_line_between '      - name: Verify authenticated MinIO readiness' "$verify_job_line" "$verify_job_end")"
minio_probe_run_line="$((minio_probe_name_line + 1))"
minio_probe_command_line="$((minio_probe_name_line + 2))"
merged_validation_line="$(exact_line_between '      - run: docker compose -f deploy/compose.dev.yml -f deploy/compose.ci.yml config --quiet' "$verify_job_line" "$verify_job_end")"
report_name_line="$(exact_line_between '      - name: Report integration dependency failure' "$verify_job_line" "$verify_job_end")"
report_if_line="$((report_name_line + 1))"
report_run_line="$((report_name_line + 2))"
report_ps_line="$((report_name_line + 3))"
report_logs_line="$((report_name_line + 4))"
cleanup_name_line="$(exact_line_between '      - name: Stop integration dependencies' "$verify_job_line" "$verify_job_end")"
cleanup_if_line="$(exact_line_between '        if: always()' "$verify_job_line" "$verify_job_end")"
cleanup_run_line="$(exact_line_between '        run: docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml down --volumes --remove-orphans' "$verify_job_line" "$verify_job_end")"

test "$startup_run_line" -eq "$((startup_name_line + 1))" ||
  fail "startup command must belong to the named startup step"
test "$probe_name_line" -eq "$((startup_run_line + 1))" ||
  fail "host-port probe must immediately follow dependency startup"
line_is "$probe_run_line" '        run: |'
line_is "$probe_for_line" '          for port in 54329 56379 59000; do'
line_is "$probe_timeout_line" '            timeout 30 bash -c "until </dev/tcp/127.0.0.1/$port; do sleep 1; done"'
line_is "$probe_done_line" '          done'
test "$minio_probe_name_line" -eq "$((probe_done_line + 1))" ||
  fail "authenticated MinIO readiness must immediately follow host-port verification"
line_is "$minio_probe_run_line" '        run: |'
line_is "$minio_probe_command_line" "          timeout 30 docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml exec -T minio /bin/sh -ceu 'until mc alias set local http://127.0.0.1:9000 \"\$MINIO_ROOT_USER\" \"\$MINIO_ROOT_PASSWORD\" >/dev/null 2>&1 && mc ls local >/dev/null 2>&1; do sleep 1; done' || { echo 'MinIO did not accept an authenticated ListBuckets request within 30s.' >&2; exit 1; }"
test "$go_test_line" -gt "$minio_probe_command_line" ||
  fail "ordinary Go tests must follow authenticated MinIO readiness"
test "$go_race_test_line" -gt "$go_test_line" ||
  fail "race-enabled Go tests must follow ordinary Go tests"
for repository_go_test_line in "$go_test_line" "$go_race_test_line"; do
  next_step_line="$(sed -n "$((repository_go_test_line + 1))p" "$workflow")"
  case "$next_step_line" in
    '      - '*) ;;
    *) fail "repository Go test steps must be standalone" ;;
  esac
done
noncanonical_repository_go_test_line="$(
  awk -v first="$verify_job_line" -v last="$verify_job_end" '
    function is_repository_go_test(command) {
      return command ~ /go[[:space:]]+test([[:space:]]|$)/ &&
        command ~ /(^|[[:space:]"\047;|&()])\.\/\.\.\.($|[[:space:]"\047;|&()])/
    }

    function inspect_run(command, line_number, canonical) {
      if (!canonical && is_repository_go_test(command)) {
        print line_number
        exit
      }
    }

    NR < first || NR > last { next }

    in_run_block {
      if ($0 ~ /^[[:space:]]*$/) {
        block_command = block_command " "
        next
      }
      if ($0 ~ /^          /) {
        command = $0
        sub(/^          /, "", command)
        block_command = block_command " " command
        next
      }
      in_run_block = 0
      inspect_run(block_command, run_line, 0)
      block_command = ""
    }

    /^(      - |        )run:[[:space:]]*/ {
      run_yaml = $0
      command = $0
      sub(/^[[:space:]]*(- )?run:[[:space:]]*/, "", command)
      if (command ~ /^[|>][+-]?([[:space:]]+#.*)?$/) {
        in_run_block = 1
        run_line = NR
        block_command = ""
      } else {
        canonical = run_yaml == "      - run: GOENV=off GOFLAGS=\047\047 go test -p 1 ./... -count=1" ||
          run_yaml == "      - run: GOENV=off GOFLAGS=\047\047 go test -race -p 1 ./... -count=1"
        inspect_run(command, NR, canonical)
      }
    }

    END {
      if (in_run_block) {
        inspect_run(block_command, run_line, 0)
      }
    }
  ' "$workflow"
)"
test -z "$noncanonical_repository_go_test_line" ||
  fail "noncanonical repository-wide Go test run at verify-job line $noncanonical_repository_go_test_line"
go_test_before_probe_line="$(
  awk -v first="$verify_job_line" -v probe_line="$probe_name_line" '
    function starts_go_test(command) {
      sub(/^[[:space:]]+/, "", command)
      sub(/^["\047]/, "", command)
      return command ~ /^go[[:space:]]+test([[:space:]]|$)/
    }

    NR < first || NR >= probe_line { next }

    in_run_block {
      if ($0 ~ /^[[:space:]]*$/) { next }
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
test "$go_test_line" -lt "$report_name_line" &&
  test "$go_race_test_line" -lt "$report_name_line" &&
  test "$go_race_test_line" -lt "$cleanup_name_line" ||
  fail "repository Go tests must finish before failure reporting and cleanup"
test "$merged_validation_line" -lt "$cleanup_name_line" ||
  fail "merged Compose validation must precede cleanup"
line_is "$report_if_line" '        if: failure()'
line_is "$report_run_line" '        run: |'
line_is "$report_ps_line" '          docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml ps'
line_is "$report_logs_line" '          docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml logs --no-color --tail 100 minio'
test "$cleanup_name_line" -eq "$((report_logs_line + 1))" ||
  fail "cleanup must immediately follow the two-command dependency failure report"
report_block="$(
  sed -n "${report_name_line},${report_logs_line}p" "$workflow"
)"
if grep -Eiq -- 'printenv|docker[[:space:]]+inspect|cat.*license|MINIO_ROOT_(USER|PASSWORD)' <<<"$report_block"; then
  fail "dependency failure report must not expose environment, container metadata, or license credentials"
fi
test "$cleanup_if_line" -eq "$((cleanup_name_line + 1))" &&
  test "$cleanup_run_line" -eq "$((cleanup_if_line + 1))" ||
  fail "cleanup must always run with the startup project and Compose files"

echo 'CI Compose host-port contract: PASS'
