#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

readonly HL_PROD_PROJECT='happylearn-prod'
readonly HL_DOCKER_TIMEOUT='120s'

hl_fail() {
  local category=${1:-internal_failure}
  printf '{"status":"fail","category":"%s"}\n' "$category" >&2
  return 1
}

hl_pass() {
  local category=${1:-complete}
  printf '{"status":"pass","category":"%s"}\n' "$category"
}

hl_require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then hl_fail 'dependency_unavailable'; return 1; fi
}

hl_canonical_path() {
  local path=${1:-} kind=${2:-any} resolved owner
  if [[ -z $path || $path != /* || $path == / || $path == "${HOME:-/nonexistent}" ]]; then hl_fail 'unsafe_path'; return 1; fi
  if [[ $path == *'*'* || $path == *'?'* || $path == *'['* || $path == *$'\n'* || $path == *$'\r'* ]]; then hl_fail 'unsafe_path'; return 1; fi
  if [[ -L $path ]]; then hl_fail 'unsafe_path'; return 1; fi
  if ! resolved=$(realpath -e -- "$path" 2>/dev/null); then hl_fail 'path_unavailable'; return 1; fi
  if [[ $resolved != "$path" ]]; then hl_fail 'noncanonical_path'; return 1; fi
  case $kind in
    directory) if [[ ! -d $path ]]; then hl_fail 'path_type_invalid'; return 1; fi ;;
    file) if [[ ! -f $path ]]; then hl_fail 'path_type_invalid'; return 1; fi ;;
    any) if [[ ! -e $path ]]; then hl_fail 'path_type_invalid'; return 1; fi ;;
    *) hl_fail 'internal_failure'; return 1 ;;
  esac
  if ! owner=$(stat -c '%u' -- "$path" 2>/dev/null); then hl_fail 'path_unavailable'; return 1; fi
  if [[ $owner != "$(id -u)" ]]; then hl_fail 'path_owner_invalid'; return 1; fi
  printf '%s\n' "$resolved"
}

hl_secure_file() {
  local path mode
  path=$(hl_canonical_path "$1" file) || return
  if ! mode=$(stat -c '%a' -- "$path"); then hl_fail 'file_mode_invalid'; return 1; fi
  if (( (8#$mode & 8#077) != 0 )); then hl_fail 'file_mode_invalid'; return 1; fi
  if [[ ! -s $path ]]; then hl_fail 'file_empty'; return 1; fi
}

hl_secure_directory() {
  local path mode
  path=$(hl_canonical_path "$1" directory) || return
  if ! mode=$(stat -c '%a' -- "$path"); then hl_fail 'directory_mode_invalid'; return 1; fi
  if (( (8#$mode & 8#077) != 0 )); then hl_fail 'directory_mode_invalid'; return 1; fi
}

hl_secret_directory() {
  local path=${1:-} resolved mode owner
  [[ -n $path && $path == /* && ! -L $path && -d $path ]] || { hl_fail 'secret_directory_invalid'; return 1; }
  resolved=$(realpath -e -- "$path" 2>/dev/null) || { hl_fail 'secret_directory_invalid'; return 1; }
  [[ $resolved == "$path" ]] || { hl_fail 'secret_directory_invalid'; return 1; }
  mode=$(stat -c '%a' -- "$path") || { hl_fail 'secret_directory_invalid'; return 1; }
  owner=$(stat -c '%u' -- "$path") || { hl_fail 'secret_directory_invalid'; return 1; }
  [[ $mode == 711 && ( $owner == 0 || $owner == "$(id -u)" ) ]] || { hl_fail 'secret_directory_invalid'; return 1; }
}

hl_service_secret() {
  local path=${1:-} expected_uid=${2:-} max_bytes=${3:-} resolved mode owner size
  [[ -n $path && $path == /* && ! -L $path && -f $path && $expected_uid =~ ^[0-9]+$ && $max_bytes =~ ^[1-9][0-9]*$ ]] || { hl_fail 'secret_file_invalid'; return 1; }
  resolved=$(realpath -e -- "$path" 2>/dev/null) || { hl_fail 'secret_file_invalid'; return 1; }
  [[ $resolved == "$path" ]] || { hl_fail 'secret_file_invalid'; return 1; }
  IFS=' ' read -r mode owner size < <(stat -c '%a %u %s' -- "$path") || { hl_fail 'secret_file_invalid'; return 1; }
  [[ $mode == 600 && $owner == "$expected_uid" ]] || { hl_fail 'secret_file_invalid'; return 1; }
  (( size >= 1 && size <= max_bytes )) || { hl_fail 'secret_file_invalid'; return 1; }
}

hl_acquire_release_lock() {
  local state_dir=$1 lock_path
  hl_secure_directory "$state_dir"
  lock_path="$state_dir/release.lock"
  if [[ ! -e $lock_path ]]; then
    if ! (umask 077; : >"$lock_path"); then hl_fail 'lock_unavailable'; return 1; fi
  fi
  hl_canonical_path "$lock_path" file >/dev/null
  if [[ $(stat -c '%a' -- "$lock_path") != 600 ]]; then hl_fail 'file_mode_invalid'; return 1; fi
  exec {HL_RELEASE_LOCK_FD}<>"$lock_path"
  if ! flock -n "$HL_RELEASE_LOCK_FD"; then hl_fail 'release_busy'; return 1; fi
  export HL_RELEASE_LOCK_FD
}

hl_atomic_json_write() {
  local target=$1 body=$2 directory temporary
  directory=$(dirname -- "$target")
  hl_secure_directory "$directory"
  if ! jq -e . >/dev/null 2>&1 <<<"$body"; then hl_fail 'state_json_invalid'; return 1; fi
  if ! temporary=$(mktemp --tmpdir="$directory" '.state.XXXXXX'); then hl_fail 'state_write_failed'; return 1; fi
  chmod 0600 "$temporary"
  if ! printf '%s\n' "$body" >"$temporary"; then
    rm -f -- "$temporary"
    hl_fail 'state_write_failed'
    return 1
  fi
  if ! sync "$temporary" || ! mv -f -- "$temporary" "$target"; then
    rm -f -- "$temporary"
    hl_fail 'state_write_failed'
    return 1
  fi
}

hl_atomic_json_write_owned() {
  local target=$1 body=$2 uid=$3 directory temporary
  [[ $uid =~ ^[1-9][0-9]*$ ]] || { hl_fail 'state_owner_invalid'; return 1; }
  directory=$(dirname -- "$target")
  [[ -d $directory && ! -L $directory && $(stat -c '%u:%g:%a' -- "$directory") == "$uid:$uid:700" ]] || { hl_fail 'state_owner_invalid'; return 1; }
  jq -e . >/dev/null 2>&1 <<<"$body" || { hl_fail 'state_json_invalid'; return 1; }
  temporary=$(mktemp --tmpdir="$directory" '.input.XXXXXX') || { hl_fail 'state_write_failed'; return 1; }
  if ! chmod 0600 "$temporary" || ! chown "$uid:$uid" "$temporary" || ! printf '%s\n' "$body" >"$temporary" || ! sync "$temporary" || ! mv -f -- "$temporary" "$target"; then
    rm -f -- "$temporary"
    hl_fail 'state_write_failed'
    return 1
  fi
}

hl_compose() {
  local project_dir=$1 env_file=$2 compose_project=$HL_PROD_PROJECT
  local -a compose_files=(-f "$project_dir/deploy/compose.prod.yml")
  shift 2
  if [[ -n ${HAPPYLEARN_LOCAL_COMPOSE_PROJECT:-} ]]; then
    [[ $HAPPYLEARN_LOCAL_COMPOSE_PROJECT =~ ^happylearn_phase6_[a-z0-9]+_[a-z0-9]+$ ]] || { hl_fail local_project_invalid; return 1; }
    compose_project=$HAPPYLEARN_LOCAL_COMPOSE_PROJECT
    [[ -f $project_dir/deploy/compose.prod.local.yml && ! -L $project_dir/deploy/compose.prod.local.yml ]] || { hl_fail local_compose_unavailable; return 1; }
    compose_files+=(-f "$project_dir/deploy/compose.prod.local.yml")
  fi
  timeout --foreground --kill-after=10s "$HL_DOCKER_TIMEOUT" \
    docker compose --project-name "$compose_project" \
      --project-directory "$project_dir" --env-file "$env_file" \
      "${compose_files[@]}" "$@"
}

hl_wait_until() {
  local deadline=$((SECONDS + ${1:?timeout required}))
  shift
  until "$@"; do
    if (( SECONDS >= deadline )); then hl_fail 'bounded_wait_expired'; return 1; fi
    sleep 2
  done
}
