#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

readonly usage='Usage: scripts/render-systemd.sh --project-dir <absolute-directory> --deployment-user <account> --output-dir <absolute-directory>'
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
readonly script_dir
readonly template_dir=$script_dir/../deploy/systemd
project_dir='' deployment_user='' output_dir=''

die() { printf '%s\n' "$usage" >&2; exit 2; }
while (($#)); do
  (($# >= 2)) || die
  case $1 in
    --project-dir) project_dir=$2 ;;
    --deployment-user) deployment_user=$2 ;;
    --output-dir) output_dir=$2 ;;
    *) die ;;
  esac
  shift 2
done

canonical_directory() {
  local path=$1 resolved
  [[ $path == /* && $path != / && $path != *$'\n'* && $path != *$'\r'* && ! -L $path && -d $path ]] || return 1
  resolved=$(realpath -e -- "$path") || return 1
  [[ $resolved == "$path" ]] || return 1
  printf '%s\n' "$resolved"
}

project_dir=$(canonical_directory "$project_dir") || die
output_dir=$(canonical_directory "$output_dir") || die
[[ $project_dir != "$output_dir" && $deployment_user =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || die
id "$deployment_user" >/dev/null 2>&1 || die
deployment_group=$(id -gn "$deployment_user") || die
[[ $deployment_group =~ ^[a-z_][a-z0-9_-]{0,30}$ ]] || die
[[ $(stat -c '%u' -- "$output_dir") == "$(id -u)" ]] || die
output_mode=$(stat -c '%a' -- "$output_dir") || die
(( (8#$output_mode & 8#022) == 0 )) || die

docker_path=$(realpath -e -- "$(command -v docker)") || die
timeout_path=$(realpath -e -- "$(command -v timeout)") || die
sha256_path=$(realpath -e -- "$(command -v sha256sum)") || die
for path in "$docker_path" "$timeout_path" "$sha256_path"; do
  [[ $path == /* && -x $path ]] || die
done

readonly -a units=(
  happylearn-compose.service
  happylearn-host-sample.service happylearn-host-sample.timer
  happylearn-backup-dispatch.service happylearn-backup-dispatch.timer
  happylearn-backup-retry.service happylearn-backup-retry.timer
  happylearn-backup-retention.service happylearn-backup-retention.timer
  happylearn-restore-verify.service happylearn-restore-verify.timer
)

for unit in "${units[@]}"; do
  template=$template_dir/$unit
  target=$output_dir/$unit
  [[ -f $template && ! -L $template && ( ! -e $target || -f $target && ! -L $target ) ]] || die
  content=$(<"$template")
  content=${content//@PROJECT_DIR@/$project_dir}
  content=${content//@DEPLOY_USER@/$deployment_user}
  content=${content//@DEPLOY_GROUP@/$deployment_group}
  content=${content//@DOCKER@/$docker_path}
  content=${content//@TIMEOUT@/$timeout_path}
  [[ $content != *'@PROJECT_DIR@'* && $content != *'@DEPLOY_USER@'* &&
     $content != *'@DEPLOY_GROUP@'* && $content != *'@DOCKER@'* &&
     $content != *'@TIMEOUT@'* ]] || die
  temporary=$(mktemp --tmpdir="$output_dir" ".${unit}.XXXXXX") || die
  if ! chmod 0600 "$temporary" || ! printf '%s\n' "$content" >"$temporary" ||
    ! sync "$temporary" || ! mv -f -- "$temporary" "$target"; then
    rm -f -- "$temporary"
    die
  fi
  hash=$($sha256_path "$target") || die
  hash=${hash%% *}
  [[ $hash =~ ^[a-f0-9]{64}$ ]] || die
  printf '%s %s\n' "$unit" "$hash"
done
