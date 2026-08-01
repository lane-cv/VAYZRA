#!/usr/bin/env bash
set -Eeuo pipefail

readonly evidence=/opt/happylearn/phase5-backup-failure.evidence
case_name="${1:-}"
[[ "$#" == 1 &&
  "$case_name" =~ ^(drain_timeout|object_store_stop_failure|snapshot_failure|object_store_restart_failure|remote_outage|retention_failure)$ &&
  -f "$evidence" &&
  ! -L "$evidence" ]] ||
  exit 64

line="$(
  grep -E \
    "^PHASE5_FAILURE_EVIDENCE case=${case_name} actual=(failed|degraded) maintenance=normal alert=active plaintext_dump=absent$" \
    "$evidence"
)" || exit 65
[[ "$(grep -Fc "case=${case_name} " "$evidence")" == 1 ]] || exit 66
printf '%s\n' "$line"
printf '%s\n' "--- PASS: phase5-backup-contract/$case_name (0.00s)"
