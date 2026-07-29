#!/usr/bin/env bash

HOST_METRICS_BACKUP_RUNTIME_UID=10003

host_metrics_path_mode() {
  local path="$1"
  if stat -f '%Lp' "$path" >/dev/null 2>&1; then
    stat -f '%Lp' "$path"
  else
    stat -c '%a' "$path"
  fi
}

host_metrics_path_owner() {
  local path="$1"
  if stat -f '%u' "$path" >/dev/null 2>&1; then
    stat -f '%u' "$path"
  else
    stat -c '%u' "$path"
  fi
}

validate_host_metrics_backup_path() {
  local path="$1"
  local component current mode owner mode_value
  local -a components
  [[ "$path" == /* && "$path" != "/" &&
    "$path" != *$'\n'* && "$path" != *$'\r'* &&
    "$path" != */ && "$path" != *//* ]] || return 1

  IFS='/' read -r -a components <<<"${path#/}" || return 1
  ((${#components[@]} > 0)) || return 1
  current=
  for component in "${components[@]}"; do
    [[ -n "$component" && "$component" != "." && "$component" != ".." ]] ||
      return 1
    current="$current/$component"
    [[ ! -L "$current" && -d "$current" ]] || return 1
  done

  mode="$(host_metrics_path_mode "$path")" || return 1
  owner="$(host_metrics_path_owner "$path")" || return 1
  [[ "$mode" =~ ^[0-7]{3,4}$ && "$owner" =~ ^[0-9]+$ ]] || return 1
  mode_value=$((8#$mode))
  ((owner == EUID || owner == 0 ||
    owner == HOST_METRICS_BACKUP_RUNTIME_UID)) || return 1
  ((mode_value == 8#700))
}
