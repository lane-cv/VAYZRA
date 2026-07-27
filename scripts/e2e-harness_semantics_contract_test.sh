#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
phase2="$repo_root/scripts/e2e-phase2.sh"
phase3="$repo_root/scripts/e2e-phase3.sh"
phase4="$repo_root/scripts/e2e-phase4.sh"
library="$repo_root/scripts/e2e-harness-lib.sh"

for file in "$library" "$phase2" "$phase3" "$phase4"; do bash -n "$file"; done
for script in "$phase2" "$phase3" "$phase4"; do
  ! grep -Fq 'HAPPYLEARN_E2E_CONTRACT_MODE' "$script"
  ! grep -Eq '^[[:space:]]*docker[[:space:]]' "$script"
  if [[ "$script" == "$phase4" ]]; then
    test "$(grep -Ec 'docker_bounded [0-9]+ build' "$script")" -eq 4
  else
    test "$(grep -Ec 'docker_bounded [0-9]+ build' "$script")" -eq 2
  fi
  grep -Fq 'cancel_bounded_command' "$script"
done
grep -Fq -- '--read-only --user 1000:1000' "$phase2"
grep -Fq 'runner_init=' "$phase2"

tmpdir="$(mktemp -d)"
semantics_nonce="$(date +%s)-$RANDOM"
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/bin"
license="$tmpdir/dummy-license"
: > "$license"

cat > "$tmpdir/bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -Eeuo pipefail
state="${FAKE_DOCKER_STATE:?}"
scenario="${FAKE_DOCKER_SCENARIO:?}"
mkdir -p "$state/resources/containers" "$state/resources/networks" "$state/resources/volumes" "$state/resources/images" "$state/markers"
if [[ "$scenario" == hang && "$*" == *_data_init* && ! -f "$state/markers/startup-delay-used" ]]; then
  touch "$state/markers/startup-delay-used"
  sleep 5
fi
touch "${FAKE_DOCKER_READY_FILE:?}"
printf '%s\n' "$*" >> "$state/calls.log"

last="${*: -1}"
case "${1:-}" in
  build)
    tag=''
    while (( $# )); do if [[ "$1" == -t ]]; then tag="${2:?}"; break; fi; shift; done
    safe_tag="${tag//\//_}"; safe_tag="${safe_tag//:/_}"
    touch "$state/resources/images/$safe_tag"
    ;;
  network)
    if [[ "${2:-}" == create ]]; then touch "$state/resources/networks/$last"
    elif [[ "${2:-}" == rm ]]; then rm -f "$state/resources/networks/$last"; printf 'network-rm\n' >> "$state/order.log"
    fi
    ;;
  volume)
    if [[ "${2:-}" == create ]]; then touch "$state/resources/volumes/$last"
    elif [[ "${2:-}" == rm ]]; then shift 2; for name in "$@"; do rm -f "$state/resources/volumes/$name"; done; printf 'volume-rm\n' >> "$state/order.log"
    fi
    ;;
  image)
    shift 2; for name in "$@"; do safe_name="${name//\//_}"; safe_name="${safe_name//:/_}"; rm -f "$state/resources/images/$safe_name"; done; printf 'image-rm\n' >> "$state/order.log"
    ;;
  ps)
    find "$state/resources/containers" -type f -exec basename {} \; 2>/dev/null || true
    ;;
  inspect)
    exit 1
    ;;
  logs)
    printf '%s\n' 'Authorization: Bearer fake-secret' 'body=fake private body'
    ;;
  rm)
    shift; [[ "${1:-}" == -f ]] && shift
    for name in "$@"; do rm -f "$state/resources/containers/$name"; done
    printf 'container-rm\n' >> "$state/order.log"
    if [[ "$scenario" == cleanup_hang && ! -f "$state/markers/cleanup-hung" ]]; then
      touch "$state/markers/cleanup-hung"
      trap 'touch "$state/markers/cleanup-terminated"; exit 143' TERM INT
      while :; do sleep .1; done
    fi
    ;;
  run)
    name=''
    args=("$@")
    for ((index=0; index<${#args[@]}; index+=1)); do
      if [[ "${args[$index]}" == --name ]]; then name="${args[$((index+1))]}"; break; fi
    done
    [[ -n "$name" ]] || exit 2
    touch "$state/resources/containers/$name"
    if [[ "$name" == *_data_init ]]; then
      if [[ "$scenario" == sanitizer_fail || "$scenario" == publish_install_fail || "$scenario" == publish_mv_fail || "$scenario" == phase4_diagnostic ]]; then
        service="${name%_data_init}_postgres"
        touch "$state/resources/containers/$service"
      fi
      if [[ "$scenario" == cleanup_hang || "$scenario" == diagnostics_write_fail || "$scenario" == sanitizer_fail || "$scenario" == publish_install_fail || "$scenario" == publish_mv_fail || "$scenario" == phase4_diagnostic ]]; then
        touch "$state/markers/normal-command-started"
        exit 9
      fi
      trap 'touch "$state/markers/deadline-terminated"; exit 143' TERM INT
      touch "$state/markers/normal-command-started"
      while :; do sleep .1; done
    fi
    rm -f "$state/resources/containers/$name"
    ;;
esac
FAKE_DOCKER
chmod +x "$tmpdir/bin/docker"

cat > "$tmpdir/bin/bash" <<'FAKE_BASH'
#!/bin/bash
set -Eeuo pipefail
if [[ "${FAKE_DOCKER_SCENARIO:-}" == sanitizer_fail && "${1:-}" == */sanitize-e2e-artifacts.sh ]]; then
  touch "${FAKE_DOCKER_STATE:?}/markers/sanitizer-failed"
  exit 86
fi
exec /bin/bash "$@"
FAKE_BASH
chmod +x "$tmpdir/bin/bash"

cat > "$tmpdir/bin/install" <<'FAKE_INSTALL'
#!/bin/bash
set -Eeuo pipefail
last="${*: -1}"
if [[ "${FAKE_DOCKER_SCENARIO:-}" == diagnostics_write_fail && "$last" == */diagnostics ]]; then
  exit 72
fi
if [[ "${FAKE_DOCKER_SCENARIO:-}" == publish_install_fail && "$last" == */.containers.log.*.tmp ]]; then
  touch "${FAKE_DOCKER_STATE:?}/markers/publish-install-failed"
  exit 73
fi
exec /usr/bin/install "$@"
FAKE_INSTALL
cat > "$tmpdir/bin/mv" <<'FAKE_MV'
#!/bin/bash
set -Eeuo pipefail
if [[ "${FAKE_DOCKER_SCENARIO:-}" == publish_mv_fail && "${*: -1}" == */containers.log ]]; then
  touch "${FAKE_DOCKER_STATE:?}/markers/publish-mv-failed"
  exit 74
fi
exec /bin/mv "$@"
FAKE_MV
chmod +x "$tmpdir/bin/install" "$tmpdir/bin/mv"

snapshot() {
  find "$1/resources" -type f -print 2>/dev/null | sed "s#^$1/resources/##" | sort
}

assert_safe_upload_directory() {
  local artifact_path="$1" line
  local final_log="$artifact_path/containers.log"
  if [[ -e "$artifact_path" ]]; then
    ! grep -R -Fq 'fake-secret' "$artifact_path" 2>/dev/null
  fi
  [[ -f "$final_log" ]] || return 0
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in
      diagnostics_version=1|container=happylearn_phase2_*|container=happylearn_phase3_*|container=happylearn_phase4_*|state_status=created|state_status=running|state_status=paused|state_status=restarting|state_status=removing|state_status=exited|state_status=dead|exit_code=[0-9]*|oom_killed=true|oom_killed=false|log_lines_omitted=[0-9]*) ;;
      *) return 1 ;;
    esac
  done < "$final_log"
}

run_case() {
  local script="$1" scenario="$2" state artifact_path status=0 expected_status failure_reasons=''
  state="$tmpdir/state-$(basename "$script")-$scenario"
  artifact_path="$repo_root/test-results/.harness-semantics-${semantics_nonce}-$(basename "$script")-$scenario"
  rm -rf "$artifact_path"
  mkdir -p "$state/resources/containers" "$state/resources/networks" "$state/resources/volumes" "$state/resources/images" "$state/markers"
  if [[ "$scenario" == diagnostics_write_fail || "$scenario" == sanitizer_fail || "$scenario" == publish_install_fail || "$scenario" == publish_mv_fail || "$scenario" == phase4_diagnostic ]]; then expected_status=9; else expected_status='nonzero'; fi
  local before after started_at elapsed pid=''
  before="$(snapshot "$state")"
  started_at="$(date +%s)"
  if [[ "$scenario" == interrupt ]]; then
    PATH="$tmpdir/bin:$PATH" FAKE_DOCKER_STATE="$state" FAKE_DOCKER_SCENARIO="$scenario" \
      HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS=3 HAPPYLEARN_E2E_TEST_READY_FILE="$state/ready" FAKE_DOCKER_READY_FILE="$state/ready" HAPPYLEARN_AISTOR_LICENSE_FILE="$license" E2E_ARTIFACT_DIR="$artifact_path" \
      /bin/bash "$script" >"$state/stdout" 2>"$state/stderr" &
    pid=$!
    for _ in $(seq 1 1200); do [[ -f "$state/markers/normal-command-started" ]] && break; sleep .05; done
    [[ -f "$state/markers/normal-command-started" ]] || failure_reasons+=$'\nnormal command never started'
    sleep .2
    kill -TERM "$pid" 2>/dev/null || failure_reasons+=$'\nfailed to signal harness'
    wait "$pid" || status=$?
  else
    PATH="$tmpdir/bin:$PATH" FAKE_DOCKER_STATE="$state" FAKE_DOCKER_SCENARIO="$scenario" \
      HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS=3 HAPPYLEARN_E2E_TEST_READY_FILE="$state/ready" FAKE_DOCKER_READY_FILE="$state/ready" HAPPYLEARN_AISTOR_LICENSE_FILE="$license" E2E_ARTIFACT_DIR="$artifact_path" \
      /bin/bash "$script" >"$state/stdout" 2>"$state/stderr" || status=$?
  fi
  elapsed=$(( $(date +%s) - started_at ))
  after="$(snapshot "$state")"
  [[ "$status" -ne 0 ]] || failure_reasons+=$'\nexpected non-zero status'
  if [[ "$expected_status" != nonzero && "$status" -ne "$expected_status" ]]; then failure_reasons+=$'\noriginal status was not preserved'; fi
  (( elapsed <= 90 )) || failure_reasons+=$'\nelapsed time exceeded 90-second loaded-host budget'
  [[ "$after" == "$before" ]] || failure_reasons+=$'\nresource inventory changed'
  [[ -f "$state/markers/normal-command-started" ]] || failure_reasons+=$'\nnormal command marker missing'
  if [[ "$scenario" == hang && ! -f "$state/markers/deadline-terminated" ]]; then failure_reasons+=$'\noperation deadline marker missing'; fi
  if [[ "$scenario" == cleanup_hang && ! -f "$state/markers/cleanup-hung" ]]; then failure_reasons+=$'\ncleanup hang marker missing'; fi
  if [[ "$scenario" == cleanup_hang && ! -f "$state/markers/cleanup-terminated" ]]; then failure_reasons+=$'\ncleanup deadline marker missing'; fi
  if [[ "$scenario" == sanitizer_fail && ! -f "$state/markers/sanitizer-failed" ]]; then failure_reasons+=$'\nsanitizer failure marker missing'; fi
  if [[ "$scenario" == publish_install_fail && ! -f "$state/markers/publish-install-failed" ]]; then failure_reasons+=$'\npublish install failure marker missing'; fi
  if [[ "$scenario" == publish_mv_fail && ! -f "$state/markers/publish-mv-failed" ]]; then failure_reasons+=$'\npublish mv failure marker missing'; fi
  if [[ "$scenario" == diagnostics_write_fail || "$scenario" == sanitizer_fail || "$scenario" == publish_install_fail || "$scenario" == publish_mv_fail || "$scenario" == phase4_diagnostic ]]; then
    assert_safe_upload_directory "$artifact_path" || failure_reasons+=$'\nupload directory contains raw or non-allowlisted diagnostics'
    [[ -z "$(find "$artifact_path" -maxdepth 1 -name '.containers.log.*.tmp' -print -quit 2>/dev/null)" ]] || failure_reasons+=$'\npublish temporary file remained'
  fi
  local container_line network_line volume_line image_line
  container_line="$(grep -n '^container-rm$' "$state/order.log" 2>/dev/null | head -1 | cut -d: -f1 || true)"
  network_line="$(grep -n '^network-rm$' "$state/order.log" 2>/dev/null | head -1 | cut -d: -f1 || true)"
  volume_line="$(grep -n '^volume-rm$' "$state/order.log" 2>/dev/null | head -1 | cut -d: -f1 || true)"
  image_line="$(grep -n '^image-rm$' "$state/order.log" 2>/dev/null | head -1 | cut -d: -f1 || true)"
  if [[ ! "$container_line" =~ ^[0-9]+$ || ! "$network_line" =~ ^[0-9]+$ || ! "$volume_line" =~ ^[0-9]+$ || ! "$image_line" =~ ^[0-9]+$ ]]; then
    failure_reasons+=$'\ncleanup order markers incomplete'
  elif ! (( container_line < network_line && network_line < volume_line && volume_line < image_line )); then
    failure_reasons+=$'\ncleanup order is incorrect'
  fi
  if [[ -n "$failure_reasons" ]]; then
    {
      printf 'E2E harness semantics failure\nphase=%s\nscenario=%s\nstatus=%s\nelapsed_seconds=%s\nreasons=%s\n' "$(basename "$script")" "$scenario" "$status" "$elapsed" "$failure_reasons"
      printf '%s\n' '--- resources before ---' "$before" '--- resources after ---' "$after" '--- resource diff ---'
      diff -u <(printf '%s\n' "$before") <(printf '%s\n' "$after") || true
      for diagnostic_file in calls.log order.log stdout stderr; do
        printf '%s\n' "--- $diagnostic_file ---"
        if [[ -f "$state/$diagnostic_file" ]]; then sed -n '1,240p' "$state/$diagnostic_file"; else printf '%s\n' '<missing>'; fi
      done
    } >&2
    rm -rf "$artifact_path"
    return 1
  fi
  rm -rf "$artifact_path"
}

for script in "$phase2" "$phase3" "$phase4"; do
  run_case "$script" interrupt
  run_case "$script" hang
  run_case "$script" cleanup_hang
  run_case "$script" diagnostics_write_fail
  run_case "$script" sanitizer_fail
  run_case "$script" publish_install_fail
  run_case "$script" publish_mv_fail
done
run_case "$phase4" phase4_diagnostic

echo 'E2E harness semantics contract: PASS'
