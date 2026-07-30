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

remove_first_exact() {
  local source="$1"
  local destination="$2"
  local needle="$3"

  awk -v needle="$needle" '
    $0 == needle && !removed {
      removed = 1
      next
    }
    { print }
    END {
      if (!removed) {
        exit 1
      }
    }
  ' "$source" >"$destination"
}

mutate_in_job() {
  local source="$1"
  local destination="$2"
  local job_header="$3"
  local needle="$4"
  local replacement="$5"

  JOB_HEADER="$job_header" NEEDLE="$needle" REPLACEMENT="$replacement" awk '
    $0 == ENVIRON["JOB_HEADER"] {
      in_job = 1
    }
    in_job && $0 != ENVIRON["JOB_HEADER"] &&
      $0 ~ /^  [^[:space:]][^:]*:$/ {
      in_job = 0
    }
    in_job && $0 == ENVIRON["NEEDLE"] && !mutated {
      if (ENVIRON["REPLACEMENT"] != "") {
        print ENVIRON["REPLACEMENT"]
      }
      mutated = 1
      next
    }
    { print }
    END {
      if (!mutated) {
        exit 1
      }
    }
  ' "$source" >"$destination"
}

insert_after_first() {
  local source="$1"
  local destination="$2"
  local needle="$3"
  local insertion="$4"

  NEEDLE="$needle" INSERTION="$insertion" awk '
    { print }
    $0 == ENVIRON["NEEDLE"] && !inserted {
      print ENVIRON["INSERTION"]
      inserted = 1
    }
    END {
      if (!inserted) {
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

missing_license_group="$tmp_dir/missing-license-group.yml"
remove_first_exact "$source_workflow" "$missing_license_group" \
  '          sudo chgrp 0 "$license_file"'
expect_rejected "missing-license-group" "$missing_license_group" \
  "AIStor license must grant the container root group immediately after creation"

missing_license_mode="$tmp_dir/missing-license-mode.yml"
remove_first_exact "$source_workflow" "$missing_license_mode" \
  '          chmod 0440 "$license_file"'
expect_rejected "missing-license-mode" "$missing_license_mode" \
  "AIStor license must be made container-readable immediately after creation"

license_export_line='          printf '"'"'HAPPYLEARN_AISTOR_LICENSE_FILE=%s\n'"'"' "$license_file" >> "$GITHUB_ENV"'

license_revoked_after_export="$tmp_dir/license-revoked-after-export.yml"
insert_after_first "$source_workflow" "$license_revoked_after_export" "$license_export_line" \
  '          chmod 0000 "$license_file"'
expect_rejected "license-revoked-after-export" "$license_revoked_after_export" \
  "AIStor license configuration step must end immediately after exporting the path"

license_world_writable_after_export="$tmp_dir/license-world-writable-after-export.yml"
insert_after_first "$source_workflow" "$license_world_writable_after_export" "$license_export_line" \
  '          chmod 0666 "$license_file"'
expect_rejected "license-world-writable-after-export" "$license_world_writable_after_export" \
  "AIStor license configuration step must end immediately after exporting the path"

ordinary_go_step="      - run: GOENV=off GOFLAGS='' go test -p 1 ./... -count=1"

verify_continue_on_error="$tmp_dir/verify-continue-on-error.yml"
insert_after "$source_workflow" "$verify_continue_on_error" '  verify:' \
  '    continue-on-error: true'
expect_rejected "verify-continue-on-error" "$verify_continue_on_error" \
  "verify job contains a noncanonical top-level key"

verify_quoted_continue_on_error="$tmp_dir/verify-quoted-continue-on-error.yml"
insert_after "$source_workflow" "$verify_quoted_continue_on_error" '  verify:' \
  '    "continue-on-error": true'
expect_rejected "verify-quoted-continue-on-error" "$verify_quoted_continue_on_error" \
  "verify job contains a noncanonical top-level key"

verify_flow_continue_on_error="$tmp_dir/verify-flow-continue-on-error.yml"
insert_after "$source_workflow" "$verify_flow_continue_on_error" '  verify:' \
  '    <<: {"continue-on-error": true}'
expect_rejected "verify-flow-continue-on-error" "$verify_flow_continue_on_error" \
  "verify job contains a noncanonical top-level key"

verify_explicit_continue_on_error="$tmp_dir/verify-explicit-continue-on-error.yml"
insert_after "$source_workflow" "$verify_explicit_continue_on_error" '  verify:' \
  $'    ? "continue-on-error"\n    : true'
expect_rejected "verify-explicit-continue-on-error" "$verify_explicit_continue_on_error" \
  "verify job contains a noncanonical top-level key"

step_if_false="$tmp_dir/step-if-false.yml"
insert_after "$source_workflow" "$step_if_false" "$ordinary_go_step" \
  '        if: false'
expect_rejected "step-if-false" "$step_if_false" \
  "workflow conditions must be exactly four failure uploads/reports and one cleanup"

step_continue_on_error="$tmp_dir/step-continue-on-error.yml"
insert_after "$source_workflow" "$step_continue_on_error" "$ordinary_go_step" \
  '        continue-on-error: true'
expect_rejected "step-continue-on-error" "$step_continue_on_error" \
  "workflow step contains a noncanonical key"

step_working_directory="$tmp_dir/step-working-directory.yml"
insert_after "$source_workflow" "$step_working_directory" "$ordinary_go_step" \
  '        working-directory: web'
expect_rejected "step-working-directory" "$step_working_directory" \
  "workflow step contains a noncanonical key"

verify_defaults="$tmp_dir/verify-defaults.yml"
insert_after "$source_workflow" "$verify_defaults" '  verify:' \
  $'    defaults:\n      run:\n        working-directory: web'
expect_rejected "verify-defaults" "$verify_defaults" \
  "verify job contains a noncanonical top-level key"

verify_if="$tmp_dir/verify-if.yml"
insert_after "$source_workflow" "$verify_if" '  verify:' \
  '    if: false'
expect_rejected "verify-if" "$verify_if" \
  "verify job contains a noncanonical top-level key"

verify_quoted_if="$tmp_dir/verify-quoted-if.yml"
insert_after "$source_workflow" "$verify_quoted_if" '  verify:' \
  '    "if": false'
expect_rejected "verify-quoted-if" "$verify_quoted_if" \
  "verify job contains a noncanonical top-level key"

verify_single_quoted_if="$tmp_dir/verify-single-quoted-if.yml"
insert_after "$source_workflow" "$verify_single_quoted_if" '  verify:' \
  "    'if': false"
expect_rejected "verify-single-quoted-if" "$verify_single_quoted_if" \
  "verify job contains a noncanonical top-level key"

verify_flow_if="$tmp_dir/verify-flow-if.yml"
insert_after "$source_workflow" "$verify_flow_if" '  verify:' \
  '    <<: {"if": false}'
expect_rejected "verify-flow-if" "$verify_flow_if" \
  "verify job contains a noncanonical top-level key"

verify_explicit_if="$tmp_dir/verify-explicit-if.yml"
insert_after "$source_workflow" "$verify_explicit_if" '  verify:' \
  $'    ? "if"\n    : false'
expect_rejected "verify-explicit-if" "$verify_explicit_if" \
  "verify job contains a noncanonical top-level key"

root_defaults="$tmp_dir/root-defaults.yml"
insert_before "$source_workflow" "$root_defaults" 'jobs:' \
  $'defaults:\n  run:\n    working-directory: web\n'
expect_rejected "root-defaults" "$root_defaults" \
  "workflow permissions must be exactly contents: read"

root_quoted_defaults="$tmp_dir/root-quoted-defaults.yml"
insert_before "$source_workflow" "$root_quoted_defaults" 'jobs:' \
  $'"defaults":\n  "run":\n    "working-directory": internal/aiqa\n'
expect_rejected "root-quoted-defaults" "$root_quoted_defaults" \
  "workflow permissions must be exactly contents: read"

root_flow_defaults="$tmp_dir/root-flow-defaults.yml"
insert_before "$source_workflow" "$root_flow_defaults" 'jobs:' \
  $'"defaults": {"run": {"working-directory": "internal/aiqa"}}\n'
expect_rejected "root-flow-defaults" "$root_flow_defaults" \
  "workflow permissions must be exactly contents: read"

verify_goflags="$tmp_dir/verify-goflags.yml"
insert_after "$source_workflow" "$verify_goflags" '  verify:' \
  $'    env:\n      GOFLAGS: "-run=^$"'
expect_rejected "verify-goflags" "$verify_goflags" \
  "verify job contains a noncanonical top-level key"

verify_inline_quoted_goflags="$tmp_dir/verify-inline-quoted-goflags.yml"
insert_after "$source_workflow" "$verify_inline_quoted_goflags" '  verify:' \
  '    env: {"GOFLAGS": "-run=^$"}'
expect_rejected "verify-inline-quoted-goflags" "$verify_inline_quoted_goflags" \
  "verify job contains a noncanonical top-level key"

root_goflags="$tmp_dir/root-goflags.yml"
insert_before "$source_workflow" "$root_goflags" 'jobs:' \
  $'env:\n  GOFLAGS: "-run=^$"\n'
expect_rejected "root-goflags" "$root_goflags" \
  "workflow permissions must be exactly contents: read"

root_inline_quoted_goflags="$tmp_dir/root-inline-quoted-goflags.yml"
insert_before "$source_workflow" "$root_inline_quoted_goflags" 'jobs:' \
  $'env: {"GOFLAGS": "-run=^$"}\n'
expect_rejected "root-inline-quoted-goflags" "$root_inline_quoted_goflags" \
  "workflow permissions must be exactly contents: read"

verify_quoted_defaults="$tmp_dir/verify-quoted-defaults.yml"
insert_after "$source_workflow" "$verify_quoted_defaults" '  verify:' \
  $'    "defaults":\n      "run":\n        "working-directory": internal/aiqa'
expect_rejected "verify-quoted-defaults" "$verify_quoted_defaults" \
  "verify job contains a noncanonical top-level key"

verify_flow_defaults="$tmp_dir/verify-flow-defaults.yml"
insert_after "$source_workflow" "$verify_flow_defaults" '  verify:' \
  '    "defaults": {"run": {"working-directory": "internal/aiqa"}}'
expect_rejected "verify-flow-defaults" "$verify_flow_defaults" \
  "verify job contains a noncanonical top-level key"

verify_quoted_parent_env="$tmp_dir/verify-quoted-parent-env.yml"
insert_after "$source_workflow" "$verify_quoted_parent_env" '  verify:' \
  '    "env": {"GOFLAGS": "-run=^$"}'
expect_rejected "verify-quoted-parent-env" "$verify_quoted_parent_env" \
  "verify job contains a noncanonical top-level key"

root_quoted_parent_env="$tmp_dir/root-quoted-parent-env.yml"
insert_before "$source_workflow" "$root_quoted_parent_env" 'jobs:' \
  $'"env": {"GOFLAGS": "-run=^$"}\n'
expect_rejected "root-quoted-parent-env" "$root_quoted_parent_env" \
  "workflow permissions must be exactly contents: read"

github_env_injection="$tmp_dir/github-env-injection.yml"
insert_before "$source_workflow" "$github_env_injection" "$ordinary_go_step" \
  '      - run: echo "GOFLAGS=-run=^$" >> "$GITHUB_ENV"'
expect_rejected "github-env-injection" "$github_env_injection" \
  "workflow may write GITHUB_ENV only in hardened license steps"

verify_defaults_merge="$tmp_dir/verify-defaults-merge.yml"
insert_after "$source_workflow" "$verify_defaults_merge" '  verify:' \
  '    <<: {"defaults": {"run": {"working-directory": "internal/aiqa"}}}'
expect_rejected "verify-defaults-merge" "$verify_defaults_merge" \
  "verify job contains a noncanonical top-level key"

verify_explicit_defaults="$tmp_dir/verify-explicit-defaults.yml"
insert_after "$source_workflow" "$verify_explicit_defaults" '  verify:' \
  $'    ? "defaults"\n    : {"run": {"working-directory": "internal/aiqa"}}'
expect_rejected "verify-explicit-defaults" "$verify_explicit_defaults" \
  "verify job contains a noncanonical top-level key"

root_defaults_merge="$tmp_dir/root-defaults-merge.yml"
insert_before "$source_workflow" "$root_defaults_merge" 'jobs:' \
  $'<<: {"defaults": {"run": {"working-directory": "internal/aiqa"}}}\n'
expect_rejected "root-defaults-merge" "$root_defaults_merge" \
  "workflow permissions must be exactly contents: read"

root_permission_escalation="$tmp_dir/root-permission-escalation.yml"
remove_first_exact "$source_workflow" "$root_permission_escalation" \
  '  contents: read'
insert_after "$root_permission_escalation" \
  "$tmp_dir/root-permission-escalation-final.yml" 'permissions:' \
  '  contents: write'
expect_rejected "root-permission-escalation" \
  "$tmp_dir/root-permission-escalation-final.yml" \
  "workflow permissions must be exactly contents: read"

unknown_job="$tmp_dir/unknown-job.yml"
insert_before "$source_workflow" "$unknown_job" '  phase5-e2e:' \
  $'  surprise-e2e:\n    runs-on: ubuntu-24.04\n    timeout-minutes: 1\n    steps:\n      - run: true'
expect_rejected "unknown-job" "$unknown_job" \
  "workflow jobs must be exactly verify, phase2-e2e, phase3-e2e, and phase5-e2e"

phase5_job_permission="$tmp_dir/phase5-job-permission.yml"
insert_after "$source_workflow" "$phase5_job_permission" '  phase5-e2e:' \
  $'    permissions:\n      contents: write'
expect_rejected "phase5-job-permission" "$phase5_job_permission" \
  "phase5-e2e job contains a noncanonical top-level key"

phase5_missing_needs="$tmp_dir/phase5-missing-needs.yml"
mutate_in_job "$source_workflow" "$phase5_missing_needs" '  phase5-e2e:' \
  '    needs: verify' ''
expect_rejected "phase5-missing-needs" "$phase5_missing_needs" \
  "phase5-e2e job canonical keys are incomplete or duplicated"

phase5_wrong_runner="$tmp_dir/phase5-wrong-runner.yml"
mutate_in_job "$source_workflow" "$phase5_wrong_runner" '  phase5-e2e:' \
  '    runs-on: ubuntu-24.04' '    runs-on: ubuntu-latest'
expect_rejected "phase5-wrong-runner" "$phase5_wrong_runner" \
  "phase5-e2e job must match the closed acceptance and artifact contract"

phase5_wrong_timeout="$tmp_dir/phase5-wrong-timeout.yml"
mutate_in_job "$source_workflow" "$phase5_wrong_timeout" '  phase5-e2e:' \
  '    timeout-minutes: 120' '    timeout-minutes: 121'
expect_rejected "phase5-wrong-timeout" "$phase5_wrong_timeout" \
  "phase5-e2e job must match the closed acceptance and artifact contract"

phase5_unpinned_checkout="$tmp_dir/phase5-unpinned-checkout.yml"
mutate_in_job "$source_workflow" "$phase5_unpinned_checkout" '  phase5-e2e:' \
  '      - uses: actions/checkout@v6.0.2' \
  '      - uses: actions/checkout@main'
expect_rejected "phase5-unpinned-checkout" "$phase5_unpinned_checkout" \
  "workflow action is not allowlisted and pinned"

phase5_unknown_action="$tmp_dir/phase5-unknown-action.yml"
mutate_in_job "$source_workflow" "$phase5_unknown_action" '  phase5-e2e:' \
  '      - uses: actions/checkout@v6.0.2' \
  '      - uses: third-party/unknown@v1.0.0'
expect_rejected "phase5-unknown-action" "$phase5_unknown_action" \
  "workflow action is not allowlisted and pinned"

phase5_wrong_group="$tmp_dir/phase5-wrong-group.yml"
mutate_in_job "$source_workflow" "$phase5_wrong_group" '  phase5-e2e:' \
  '        run: HAPPYLEARN_E2E_GROUP=all make e2e-phase5' \
  '        run: HAPPYLEARN_E2E_GROUP=phase5 make e2e-phase5'
expect_rejected "phase5-wrong-group" "$phase5_wrong_group" \
  "phase5-e2e job must match the closed acceptance and artifact contract"

phase5_skipped_test="$tmp_dir/phase5-skipped-test.yml"
insert_after "$source_workflow" "$phase5_skipped_test" \
  '        run: HAPPYLEARN_E2E_GROUP=all make e2e-phase5' \
  '        if: false'
expect_rejected "phase5-skipped-test" "$phase5_skipped_test" \
  "workflow conditions must be exactly four failure uploads/reports and one cleanup"

phase5_continue_on_error="$tmp_dir/phase5-continue-on-error.yml"
insert_after "$source_workflow" "$phase5_continue_on_error" \
  '        run: HAPPYLEARN_E2E_GROUP=all make e2e-phase5' \
  '        continue-on-error: true'
expect_rejected "phase5-continue-on-error" "$phase5_continue_on_error" \
  "workflow step contains a noncanonical key"

phase5_shell_override="$tmp_dir/phase5-shell-override.yml"
insert_after "$source_workflow" "$phase5_shell_override" \
  '        run: HAPPYLEARN_E2E_GROUP=all make e2e-phase5' \
  '        shell: bash'
expect_rejected "phase5-shell-override" "$phase5_shell_override" \
  "workflow step contains a noncanonical key"

phase5_secret_print="$tmp_dir/phase5-secret-print.yml"
insert_before "$source_workflow" "$phase5_secret_print" \
  '      - name: Run isolated Phase 5 acceptance' \
  '      - run: printenv'
expect_rejected "phase5-secret-print" "$phase5_secret_print" \
  "workflow contains a secret-printing diagnostic step"

phase5_wrong_artifact_path="$tmp_dir/phase5-wrong-artifact-path.yml"
mutate_in_job "$source_workflow" "$phase5_wrong_artifact_path" \
  '  phase5-e2e:' '          path: test-results/phase5/containers.log' \
  '          path: test-results/phase5'
expect_rejected "phase5-wrong-artifact-path" "$phase5_wrong_artifact_path" \
  "phase5-e2e job must match the closed acceptance and artifact contract"

phase5_artifact_wildcard="$tmp_dir/phase5-artifact-wildcard.yml"
mutate_in_job "$source_workflow" "$phase5_artifact_wildcard" \
  '  phase5-e2e:' '          path: test-results/phase5/containers.log' \
  '          path: test-results/phase5/*'
expect_rejected "phase5-artifact-wildcard" "$phase5_artifact_wildcard" \
  "phase5-e2e job must match the closed acceptance and artifact contract"

phase5_wrong_retention="$tmp_dir/phase5-wrong-retention.yml"
mutate_in_job "$source_workflow" "$phase5_wrong_retention" '  phase5-e2e:' \
  '          retention-days: 7' '          retention-days: 30'
expect_rejected "phase5-wrong-retention" "$phase5_wrong_retention" \
  "phase5-e2e job must match the closed acceptance and artifact contract"

echo "CI Compose workflow mutation contract: PASS"
