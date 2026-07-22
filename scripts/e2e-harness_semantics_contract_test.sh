#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
phase2="$repo_root/scripts/e2e-phase2.sh"
phase3="$repo_root/scripts/e2e-phase3.sh"

bash -n "$phase2"
bash -n "$phase3"
test "$(HAPPYLEARN_E2E_CONTRACT_MODE=print bash "$phase3")" = 'group=all'
for script in "$phase2" "$phase3"; do
  ! grep -Eq '^[[:space:]]*docker (build|run)' "$script"
  test "$(grep -Ec 'run_bounded [0-9]+ docker build' "$script")" -eq 2
  while IFS= read -r line; do
    [[ "$line" == *' --name '* ]]
  done < <(grep -E 'run_bounded [0-9]+ docker run' "$script")
done

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/bin" "$tmpdir/state/containers" "$tmpdir/state/networks" "$tmpdir/state/volumes"
cat > "$tmpdir/bin/docker" <<'FAKE_DOCKER'
#!/usr/bin/env bash
set -Eeuo pipefail
state="${FAKE_DOCKER_STATE:?}"
printf '%s\n' "$*" >> "$state/calls.log"
case "${1:-} ${2:-}" in
  'network create') touch "$state/networks/${*: -1}" ;;
  'volume create') touch "$state/volumes/${*: -1}" ;;
  'network rm') test -z "$(find "$state/containers" -type f -print -quit)"; rm -f "$state/networks/${3:-}"; printf 'network-rm\n' >> "$state/order.log" ;;
  'volume rm') shift 2; for name in "$@"; do rm -f "$state/volumes/$name"; done; printf 'volume-rm\n' >> "$state/order.log" ;;
  'rm -f') shift 2; for name in "$@"; do rm -f "$state/containers/$name"; done; printf 'container-rm\n' >> "$state/order.log" ;;
  'run --name') name="${3:?}"; touch "$state/containers/$name" "$state/started"; trap 'exit 143' TERM INT; while [[ -f "$state/containers/$name" ]]; do sleep .1; done ;;
  *) exit 0 ;;
esac
FAKE_DOCKER
chmod +x "$tmpdir/bin/docker"

PATH="$tmpdir/bin:$PATH" FAKE_DOCKER_STATE="$tmpdir/state" HAPPYLEARN_E2E_CONTRACT_MODE=interrupt bash "$phase3" &
pid=$!
for _ in $(seq 1 50); do [[ -f "$tmpdir/state/started" ]] && break; sleep .1; done
test -f "$tmpdir/state/started"
kill -TERM "$pid"
wait "$pid" || true
test -z "$(find "$tmpdir/state/containers" "$tmpdir/state/networks" "$tmpdir/state/volumes" -type f -print -quit)"
test "$(awk '!seen[$0]++ { print }' "$tmpdir/state/order.log")" = $'container-rm\nnetwork-rm\nvolume-rm'

echo 'E2E harness semantics contract: PASS'
