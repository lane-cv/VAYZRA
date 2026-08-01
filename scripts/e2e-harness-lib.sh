#!/usr/bin/env bash

# Shared deadline primitives for disposable E2E harnesses. The optional test
# controls only apply with the deadline cap enabled, so production deadlines
# cannot be postponed or bypassed through the readiness handshake.
E2E_ACTIVE_COMMAND_PID=''

preserve_first_failure() {
  local current="${1:?current status required}" candidate="${2:?candidate status required}"
  [[ "$current" =~ ^[0-9]+$ && "$candidate" =~ ^[1-9][0-9]*$ ]] || return 2
  if (( current == 0 )); then printf '%s\n' "$candidate"; else printf '%s\n' "$current"; fi
}

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
  if [[ -n "$E2E_ACTIVE_COMMAND_PID" ]]; then
    local grace grace_deadline grace_tick_limit grace_ticks=0
    grace="$(bounded_grace_seconds)"
    grace_deadline=$((SECONDS + grace + 1))
    grace_tick_limit=$((grace * 10))
    kill -TERM "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || true
    while kill -0 "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null &&
      (( grace_ticks < grace_tick_limit && SECONDS < grace_deadline )); do
      sleep .1
      grace_ticks=$((grace_ticks + 1))
    done
    if kill -0 "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null; then
      kill -KILL "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || true
    fi
    wait "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || true
  fi
  E2E_ACTIVE_COMMAND_PID=''
}

run_bounded() {
  local seconds grace command_status=0 ready_file=''
  local deadline deadline_tick_limit elapsed_ticks=0
  local ready_deadline ready_ticks=0
  local grace_deadline grace_tick_limit grace_ticks=0
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
    ready_deadline=$((SECONDS + 61))
    while (( ready_ticks < 600 && SECONDS < ready_deadline )); do
      [[ -e "$ready_file" ]] && break
      if ! kill -0 "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null; then
        wait "$E2E_ACTIVE_COMMAND_PID" || command_status=$?
        E2E_ACTIVE_COMMAND_PID=''
        return "$command_status"
      fi
      sleep .1
      ready_ticks=$((ready_ticks + 1))
    done
    if [[ ! -e "$ready_file" ]]; then
      cancel_bounded_command
      return 124
    fi
  fi
  deadline=$((SECONDS + seconds + 1))
  deadline_tick_limit=$((seconds * 10))
  while kill -0 "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null &&
    (( elapsed_ticks < deadline_tick_limit && SECONDS < deadline )); do
    sleep .1
    elapsed_ticks=$((elapsed_ticks + 1))
  done
  if kill -0 "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null; then
    kill -TERM "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || true
    grace_deadline=$((SECONDS + grace + 1))
    grace_tick_limit=$((grace * 10))
    while kill -0 "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null &&
      (( grace_ticks < grace_tick_limit && SECONDS < grace_deadline )); do
      sleep .1
      grace_ticks=$((grace_ticks + 1))
    done
    if kill -0 "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null; then
      kill -KILL "$E2E_ACTIVE_COMMAND_PID" 2>/dev/null || true
    fi
  fi
  wait "$E2E_ACTIVE_COMMAND_PID" || command_status=$?
  E2E_ACTIVE_COMMAND_PID=''
  return "$command_status"
}

docker_bounded() {
  local seconds="${1:?deadline required}"; shift
  run_bounded "$seconds" docker "$@"
}
