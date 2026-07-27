#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
contract="$repo_root/scripts/ci-compose_contract_test.sh"
source_workflow="$repo_root/.github/workflows/verify.yml"

fail() {
  echo "CI Compose workflow mutation contract: FAIL: $*" >&2
  exit 1
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

if test -z "${HAPPYLEARN_AISTOR_LICENSE_FILE:-}"; then
  HAPPYLEARN_AISTOR_LICENSE_FILE="$tmp_dir/minio.license"
  export HAPPYLEARN_AISTOR_LICENSE_FILE
  touch "$HAPPYLEARN_AISTOR_LICENSE_FILE"
fi

insert_after() {
  local source="$1"
  local destination="$2"
  local needle="$3"
  local insertion="$4"

  INSERTION="$insertion" awk -v needle="$needle" '
    { print }
    $0 == needle {
      print ENVIRON["INSERTION"]
      found++
    }
    END {
      if (found != 1) {
        exit 1
      }
    }
  ' "$source" >"$destination"
}

insert_before() {
  local source="$1"
  local destination="$2"
  local needle="$3"
  local insertion="$4"

  INSERTION="$insertion" awk -v needle="$needle" '
    $0 == needle {
      print ENVIRON["INSERTION"]
      found++
    }
    { print }
    END {
      if (found != 1) {
        exit 1
      }
    }
  ' "$source" >"$destination"
}

expect_rejected() {
  local name="$1"
  local workflow="$2"
  local expected="$3"
  local output="$tmp_dir/$name.out"

  if HAPPYLEARN_CI_COMPOSE_CONTRACT_WORKFLOW="$workflow" bash "$contract" >"$output" 2>&1; then
    fail "accepted workflow mutation: $name"
  fi
  grep -Fq -- "$expected" "$output" ||
    fail "workflow mutation $name failed for the wrong reason"
}

expect_accepted() {
  local name="$1"
  local workflow="$2"
  local output="$tmp_dir/$name.out"

  HAPPYLEARN_CI_COMPOSE_CONTRACT_WORKFLOW="$workflow" bash "$contract" >"$output" 2>&1 ||
    fail "rejected safely neutralized workflow mutation: $name"
}

pristine="$tmp_dir/pristine.yml"
cp "$source_workflow" "$pristine"
HAPPYLEARN_CI_COMPOSE_CONTRACT_WORKFLOW="$pristine" bash "$contract" >/dev/null ||
  fail "unmodified workflow copy does not satisfy the contract"

ordinary_go_step="      - run: GOFLAGS='' go test -p 1 ./... -count=1"

verify_continue_on_error="$tmp_dir/verify-continue-on-error.yml"
insert_after "$source_workflow" "$verify_continue_on_error" '  verify:' \
  '    continue-on-error: true'
expect_rejected "verify-continue-on-error" "$verify_continue_on_error" \
  "verify job must not set continue-on-error"

verify_quoted_continue_on_error="$tmp_dir/verify-quoted-continue-on-error.yml"
insert_after "$source_workflow" "$verify_quoted_continue_on_error" '  verify:' \
  '    "continue-on-error": true'
expect_rejected "verify-quoted-continue-on-error" "$verify_quoted_continue_on_error" \
  "verify job must not set continue-on-error"

verify_flow_continue_on_error="$tmp_dir/verify-flow-continue-on-error.yml"
insert_after "$source_workflow" "$verify_flow_continue_on_error" '  verify:' \
  '    <<: {"continue-on-error": true}'
expect_rejected "verify-flow-continue-on-error" "$verify_flow_continue_on_error" \
  "verify job must not set continue-on-error"

step_if_false="$tmp_dir/step-if-false.yml"
insert_after "$source_workflow" "$step_if_false" "$ordinary_go_step" \
  '        if: false'
expect_rejected "step-if-false" "$step_if_false" \
  "repository Go test steps must be standalone"

step_continue_on_error="$tmp_dir/step-continue-on-error.yml"
insert_after "$source_workflow" "$step_continue_on_error" "$ordinary_go_step" \
  '        continue-on-error: true'
expect_rejected "step-continue-on-error" "$step_continue_on_error" \
  "repository Go test steps must be standalone"

step_working_directory="$tmp_dir/step-working-directory.yml"
insert_after "$source_workflow" "$step_working_directory" "$ordinary_go_step" \
  '        working-directory: web'
expect_rejected "step-working-directory" "$step_working_directory" \
  "repository Go test steps must be standalone"

verify_defaults="$tmp_dir/verify-defaults.yml"
insert_after "$source_workflow" "$verify_defaults" '  verify:' \
  $'    defaults:\n      run:\n        working-directory: web'
expect_rejected "verify-defaults" "$verify_defaults" \
  "verify job must not override the run working directory"

verify_if="$tmp_dir/verify-if.yml"
insert_after "$source_workflow" "$verify_if" '  verify:' \
  '    if: false'
expect_rejected "verify-if" "$verify_if" \
  "verify job must not have an if condition"

root_defaults="$tmp_dir/root-defaults.yml"
insert_before "$source_workflow" "$root_defaults" 'jobs:' \
  $'defaults:\n  run:\n    working-directory: web\n'
expect_rejected "root-defaults" "$root_defaults" \
  "workflow must not override the run working directory"

verify_goflags="$tmp_dir/verify-goflags.yml"
insert_after "$source_workflow" "$verify_goflags" '  verify:' \
  $'    env:\n      GOFLAGS: "-run=^$"'
expect_rejected "verify-goflags" "$verify_goflags" \
  "workflow and verify job must not set GOFLAGS"

verify_inline_quoted_goflags="$tmp_dir/verify-inline-quoted-goflags.yml"
insert_after "$source_workflow" "$verify_inline_quoted_goflags" '  verify:' \
  '    env: {"GOFLAGS": "-run=^$"}'
expect_rejected "verify-inline-quoted-goflags" "$verify_inline_quoted_goflags" \
  "workflow and verify job must not set GOFLAGS"

root_goflags="$tmp_dir/root-goflags.yml"
insert_before "$source_workflow" "$root_goflags" 'jobs:' \
  $'env:\n  GOFLAGS: "-run=^$"\n'
expect_rejected "root-goflags" "$root_goflags" \
  "workflow and verify job must not set GOFLAGS"

root_inline_quoted_goflags="$tmp_dir/root-inline-quoted-goflags.yml"
insert_before "$source_workflow" "$root_inline_quoted_goflags" 'jobs:' \
  $'env: {"GOFLAGS": "-run=^$"}\n'
expect_rejected "root-inline-quoted-goflags" "$root_inline_quoted_goflags" \
  "workflow and verify job must not set GOFLAGS"

verify_quoted_parent_env="$tmp_dir/verify-quoted-parent-env.yml"
insert_after "$source_workflow" "$verify_quoted_parent_env" '  verify:' \
  '    "env": {"GOFLAGS": "-run=^$"}'
expect_accepted "verify-quoted-parent-env" "$verify_quoted_parent_env"

root_quoted_parent_env="$tmp_dir/root-quoted-parent-env.yml"
insert_before "$source_workflow" "$root_quoted_parent_env" 'jobs:' \
  $'"env": {"GOFLAGS": "-run=^$"}\n'
expect_accepted "root-quoted-parent-env" "$root_quoted_parent_env"

github_env_injection="$tmp_dir/github-env-injection.yml"
insert_before "$source_workflow" "$github_env_injection" "$ordinary_go_step" \
  '      - run: echo "GOFLAGS=-run=^$" >> "$GITHUB_ENV"'
expect_accepted "github-env-injection" "$github_env_injection"

echo "CI Compose workflow mutation contract: PASS"
