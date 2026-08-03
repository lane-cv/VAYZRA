#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
target=$root/scripts/phase6-release_failure_matrix.sh
release=$root/scripts/prod-release.sh
rollback=$root/scripts/prod-rollback.sh
production_adapter=$root/scripts/phase6-release_failure_matrix_adapter.sh
harness=$root/scripts/e2e-phase6.sh
fail() { printf 'phase6 release failure matrix contract: FAIL: %s\n' "$1" >&2; exit 1; }
[[ -x $target && ! -L $target ]] || fail 'matrix missing or not executable'
[[ -x $production_adapter && ! -L $production_adapter ]] || fail 'production matrix adapter missing or not executable'

cases=(unsafe_secret insufficient_disk unavailable_digest pre_release_backup_failure drain_timeout migration_lock_conflict migration_failure app_readiness_failure worker_readiness_failure object_store_restart_failure smoke_failure signal_each_durable_step previous_image_success previous_image_incompatible previous_image_readiness_failure)
stages=(prepare inject durable_state maintenance traffic manifests schema recovery diagnostics cleanup)
for name in "${cases[@]}"; do grep -Fq "$name:" "$target" || fail "case missing: $name"; done
for stage in "${stages[@]}"; do grep -Fq "$stage" "$target" || fail "assertion stage missing: $stage"; done
for literal in HAPPYLEARN_PHASE6_FAILURE_MATRIX_ADAPTER HAPPYLEARN_PHASE6_FAILURE_MATRIX_ROOT HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE_DEADLINE_SECONDS HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE owner_only_executable owner_only_directory timeout sanitization cleanup; do
  grep -Fq "$literal" "$target" || fail "matrix invariant missing: $literal"
done
for literal in HAPPYLEARN_RELEASE_FAILURE_INJECTION 'mode == local' server_test_variable_rejected invalid_failure_injection; do
  grep -Fq "$literal" "$release" || fail "release injection boundary missing: $literal"
done
for literal in 'docker compose' prod-release.sh compose.prod.yml compose.prod.local.yml \
  unsafe-secrets /matrix-low-disk unavailable@sha256 signal-results \
  release-state.json operational_modes goose_db_version \
  rollback-diagnostics.json recovery/latest.json active-manifest.json \
  'password|credential|bearer|authorization|cookie'; do
  grep -Fq "$literal" "$production_adapter" || fail "production adapter invariant missing: $literal"
done
grep -Fq 'Caddyfile.maintenance' "$release" || fail 'release does not activate real maintenance configuration'
for literal in HAPPYLEARN_ROLLBACK_FAILURE_INJECTION previous_image_incompatible \
  previous_image_readiness_failure server_test_variable_rejected; do
  grep -Fq "$literal" "$rollback" || fail "rollback injection boundary missing: $literal"
done
grep -Fq 'HAPPYLEARN_E2E_GROUP=failure-matrix' "$target" || fail 'standalone matrix does not launch disposable production'
grep -Fq 'phase6-release_failure_matrix_adapter.sh' "$harness" || fail 'harness does not install the production adapter'
grep -Fq 'run_release_failure_matrix' "$harness" || fail 'harness does not execute the production adapter'
grep -Fq '.state=="traffic_open" and .result=="rolled_back"' "$production_adapter" || fail 'matrix does not assert the rollback terminal state'

tmp=$(mktemp -d "${TMPDIR:-/tmp}/phase6-release-matrix.XXXXXX")
trap 'rm -rf -- "$tmp"' EXIT HUP INT TERM
chmod 0700 "$tmp"
calls=$tmp/calls
adapter=$tmp/adapter
# The generated adapter expands these values when the fixture is executed.
# shellcheck disable=SC2016
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'printf "%s|%s|%s|%s\n" "$1" "$2" "$3" "$4" >>"${HAPPYLEARN_PHASE6_FAILURE_MATRIX_CALLS:?}"' \
  >"$adapter"
chmod 0700 "$adapter"

HAPPYLEARN_PHASE6_FAILURE_MATRIX_ADAPTER=$adapter \
HAPPYLEARN_PHASE6_FAILURE_MATRIX_ROOT=$tmp \
HAPPYLEARN_PHASE6_FAILURE_MATRIX_CALLS=$calls \
HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE_DEADLINE_SECONDS=5 \
  "$target" >"$tmp/output"

grep -Fxq 'phase6 release failure matrix: PASS (15/15)' "$tmp/output" || fail 'matrix summary invalid'
for name in "${cases[@]}"; do
  for stage in "${stages[@]}"; do
    [[ $(awk -F'|' -v s="$stage" -v n="$name" '$1==s && $2==n {count++} END {print count+0}' "$calls") == 1 ]] || fail "$name did not execute $stage exactly once"
  done
done
HAPPYLEARN_PHASE6_FAILURE_MATRIX_ADAPTER=$adapter \
HAPPYLEARN_PHASE6_FAILURE_MATRIX_ROOT=$tmp \
HAPPYLEARN_PHASE6_FAILURE_MATRIX_CALLS=$calls \
HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE=smoke_failure \
HAPPYLEARN_PHASE6_FAILURE_MATRIX_CASE_DEADLINE_SECONDS=5 \
  "$target" >"$tmp/selected-output"
grep -Fxq 'phase6 release failure matrix: PASS (1/1)' "$tmp/selected-output" || fail 'selected matrix summary invalid'
if rg -ni '(password|credential|bearer|private key|database url|redis url|access key|secret value)' "$tmp/output" >/dev/null; then fail 'output was not sanitized'; fi

chmod 0755 "$adapter"
if HAPPYLEARN_PHASE6_FAILURE_MATRIX_ADAPTER=$adapter HAPPYLEARN_PHASE6_FAILURE_MATRIX_ROOT=$tmp "$target" >/dev/null 2>&1; then fail 'non-owner-only adapter accepted'; fi
chmod 0700 "$adapter"
if HAPPYLEARN_PHASE6_FAILURE_MATRIX_ADAPTER=$adapter HAPPYLEARN_PHASE6_FAILURE_MATRIX_ROOT=. "$target" >/dev/null 2>&1; then fail 'relative matrix root accepted'; fi

bash -n "$target" "$production_adapter" "$release" "$rollback" "$harness"
printf '%s\n' 'phase6 release failure matrix contract: PASS'
