#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
phase2="$repo_root/scripts/e2e-phase2.sh"
phase3="$repo_root/scripts/e2e-phase3.sh"
library="$repo_root/scripts/e2e-harness-lib.sh"

for file in "$library" "$phase2" "$phase3"; do bash -n "$file"; done
for script in "$phase2" "$phase3"; do
  ! grep -Fq 'HAPPYLEARN_E2E_CONTRACT_MODE' "$script"
  ! grep -Eq '^[[:space:]]*docker[[:space:]]' "$script"
  test "$(grep -Ec 'docker_bounded [0-9]+ build' "$script")" -eq 2
  grep -Fq 'cancel_bounded_command' "$script"
done
grep -Fq -- '--read-only --user 1000:1000' "$phase2"
grep -Fq 'runner_init=' "$phase2"

tmpdir="$(mktemp -d)"
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
      trap 'exit 143' TERM INT
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
      touch "$state/markers/normal-command-started"
      if [[ "$scenario" == cleanup_hang ]]; then exit 9; fi
      trap 'exit 143' TERM INT
      while :; do sleep .1; done
    fi
    rm -f "$state/resources/containers/$name"
    ;;
esac
FAKE_DOCKER
chmod +x "$tmpdir/bin/docker"

snapshot() {
  find "$1/resources" -type f -print 2>/dev/null | sed "s#^$1/resources/##" | sort
}

run_case() {
  local script="$1" scenario="$2" state status=0
  state="$tmpdir/state-$(basename "$script")-$scenario"
  mkdir -p "$state/resources/containers" "$state/resources/networks" "$state/resources/volumes" "$state/resources/images" "$state/markers"
  local before after started_at elapsed
  before="$(snapshot "$state")"
  started_at="$(date +%s)"
  if [[ "$scenario" == interrupt ]]; then
    PATH="$tmpdir/bin:$PATH" FAKE_DOCKER_STATE="$state" FAKE_DOCKER_SCENARIO="$scenario" \
      HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS=1 HAPPYLEARN_AISTOR_LICENSE_FILE="$license" E2E_ARTIFACT_DIR="$state/artifacts" \
      bash "$script" >"$state/stdout" 2>"$state/stderr" &
    local pid=$!
    for _ in $(seq 1 50); do [[ -f "$state/markers/normal-command-started" ]] && break; sleep .05; done
    test -f "$state/markers/normal-command-started"
    kill -TERM "$pid"
    wait "$pid" || status=$?
  else
    PATH="$tmpdir/bin:$PATH" FAKE_DOCKER_STATE="$state" FAKE_DOCKER_SCENARIO="$scenario" \
      HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS=1 HAPPYLEARN_AISTOR_LICENSE_FILE="$license" E2E_ARTIFACT_DIR="$state/artifacts" \
      bash "$script" >"$state/stdout" 2>"$state/stderr" || status=$?
  fi
  test "$status" -ne 0
  elapsed=$(( $(date +%s) - started_at ))
  test "$elapsed" -le 12
  after="$(snapshot "$state")"
  test "$after" = "$before"
  test -f "$state/markers/normal-command-started"
  [[ "$scenario" != cleanup_hang ]] || test -f "$state/markers/cleanup-hung"
  local container_line network_line volume_line image_line
  container_line="$(grep -n '^container-rm$' "$state/order.log" | head -1 | cut -d: -f1)"
  network_line="$(grep -n '^network-rm$' "$state/order.log" | head -1 | cut -d: -f1)"
  volume_line="$(grep -n '^volume-rm$' "$state/order.log" | head -1 | cut -d: -f1)"
  image_line="$(grep -n '^image-rm$' "$state/order.log" | head -1 | cut -d: -f1)"
  test "$container_line" -lt "$network_line"
  test "$network_line" -lt "$volume_line"
  test "$volume_line" -lt "$image_line"
}

for script in "$phase2" "$phase3"; do
  run_case "$script" interrupt
  run_case "$script" hang
  run_case "$script" cleanup_hang
done

echo 'E2E harness semantics contract: PASS'
