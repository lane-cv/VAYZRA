#!/usr/bin/env bash

# Shared deadline primitives for disposable E2E harnesses. The optional test
# controls only apply with the deadline cap enabled, so production deadlines
# cannot be postponed or bypassed through the readiness handshake.
E2E_ACTIVE_COMMAND_PID=''
E2E_ACTIVE_TIMER_PID=''

bounded_seconds() {
  local requested="${1:?deadline required}" cap="${HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS:-}"
  if [[ -n "$cap" ]]; then
    [[ "$cap" =~ ^[1-9][0-9]*$ ]] || { echo 'HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS must be a positive integer' >&2; return 2; }
    if (( cap < requested )); then printf '%s\n' "$cap"; return; fi
  fi
  printf '%s\n' "$requested"
}

bounded_grace_seconds() {
  local cap="${HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS:-}"
  if [[ -n "$cap" && "$cap" =~ ^[1-9][0-9]*$ && "$cap" -lt 10 ]]; then printf '%s\n' "$cap"; else printf '10\n'; fi
}

cancel_bounded_command() {
  if [[ -n "$E2E_ACTIVE_TIMER_PID" ]]; then kill "$E2E_ACTIVE_TIMER_PID" 2>/dev/null || true; wait "$E2E_ACTIVE_TIMER_PID" 2>/dev/null || true; fi
  if [[ -n "$E2E_ACTIVE_COMMAND_PID" ]]; then
    kill -TERM "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || true
    sleep "$(bounded_grace_seconds)"
    kill -KILL "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || true
    wait "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || true
  fi
  E2E_ACTIVE_TIMER_PID=''
  E2E_ACTIVE_COMMAND_PID=''
}

run_bounded() {
  local seconds grace command_status=0 ready_file='' ready_attempt
  seconds="$(bounded_seconds "${1:?deadline required}")"; shift
  grace="$(bounded_grace_seconds)"
  if [[ -n "${HAPPYLEARN_E2E_TEST_READY_FILE:-}" ]]; then
    [[ -n "${HAPPYLEARN_E2E_TEST_DEADLINE_SECONDS:-}" ]] || { echo 'test readiness requires the test deadline cap' >&2; return 2; }
    ready_file="$HAPPYLEARN_E2E_TEST_READY_FILE"
    [[ "$ready_file" == /* ]] || { echo 'HAPPYLEARN_E2E_TEST_READY_FILE must be absolute' >&2; return 2; }
    rm -f "$ready_file" || return 2
  fi
  "$@" & E2E_ACTIVE_COMMAND_PID=$!
  if [[ -n "$ready_file" ]]; then
    for ready_attempt in $(seq 1 600); do
      [[ -e "$ready_file" ]] && break
      if ! kill -0 "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null; then
        wait "$E2E_ACTIVE_COMMAND_PID" || command_status=$?
        E2E_ACTIVE_COMMAND_PID=''
        return "$command_status"
      fi
      sleep .1
    done
    if [[ ! -e "$ready_file" ]]; then
      cancel_bounded_command
      return 124
    fi
  fi
  (
    sleep "$seconds"
    kill -TERM "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || exit 0
    sleep "$grace"
    kill -KILL "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || true
  ) & E2E_ACTIVE_TIMER_PID=$!
  wait "$E2E_ACTIVE_COMMAND_PID" || command_status=$?
  kill "$E2E_ACTIVE_TIMER_PID" 2>/dev/null || true
  wait "$E2E_ACTIVE_TIMER_PID" 2>/dev/null || true
  E2E_ACTIVE_COMMAND_PID=''
  E2E_ACTIVE_TIMER_PID=''
  return "$command_status"
}

docker_bounded() {
  local seconds="${1:?deadline required}"; shift
  run_bounded "$seconds" docker "$@"
}
