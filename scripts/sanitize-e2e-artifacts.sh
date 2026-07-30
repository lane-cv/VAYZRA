#!/usr/bin/env bash
set -Eeuo pipefail

artifact_dir="${1:?artifact directory required}"

validate_artifact_target() {
  local component current='' path_components=()
  [[ "$artifact_dir" == /* && "$artifact_dir" != / ]] || return 1
  case "/${artifact_dir#/}/" in
    *//*|*/./*|*/../*) return 1 ;;
  esac
  IFS='/' read -r -a path_components <<< "${artifact_dir#/}"
  for component in "${path_components[@]}"; do
    [[ -n "$component" && "$component" != . && "$component" != .. ]] \
      || return 1
    current="$current/$component"
    [[ ! -L "$current" ]] || return 1
  done
}

validate_artifact_target || exit 2
[[ -d "$artifact_dir" ]] || exit 0

purge_artifacts() {
  validate_artifact_target || return 1
  find "$artifact_dir" -mindepth 1 -delete 2>/dev/null
}

reject_artifacts() {
  purge_artifacts || exit 2
  exit 1
}

unsafe_entry="$(
  find "$artifact_dir" -mindepth 1 ! -type f ! -type d -print -quit \
    2>/dev/null || true
)"
[[ -z "$unsafe_entry" ]] || reject_artifacts

log="$artifact_dir/containers.log"
if [[ ! -e "$log" ]]; then
  purge_artifacts || exit 2
  exit 0
fi
[[ -f "$log" && ! -L "$log" ]] || reject_artifacts

forbidden_patterns=(
  '(^|[^[:alnum:]_])(password|passwd|token|secret|api[_-]?key|master[_-]?key|provider[_-]?key|encrypted[_-]?api[_-]?key)[[:space:]]*[:=]'
  '"(password|passwd|token|secret|body)"[[:space:]]*:'
  '(^|[[:space:]])(Authorization|Proxy-Authorization|Cookie|Set-Cookie|X-CSRF-Token)[[:space:]]*:'
  '(^|[^[:alnum:]_])(body|prompt[_-]?text|message[_-]?text|answer[_-]?text|student[_-]?content)[[:space:]]*[:=]'
  '(postgres|postgresql|redis|https?)://'
  '(^|[^[:alnum:]_])(password|passwd|token|secret|key)(=|%3[Dd])'
  '(%3[Dd]|[?&])[A-Za-z0-9_.%~-]*(password|passwd|token|secret|key)[A-Za-z0-9_.%~-]*(=|%3[Dd])'
  'HAPPYLEARN_(AI_MASTER_KEY|DATABASE_URL|REDIS_URL)[[:space:]]*='
  'MINIO_ROOT_(USER|PASSWORD)[[:space:]]*='
  'RESTIC_PASSWORD[[:space:]]*='
  'AGE-SECRET-KEY-1'
  'HAPPYLEARN_WEBHOOK_URL[[:space:]]*='
  'HAPPYLEARN_HOST_METRICS_HMAC_SECRET[[:space:]]*='
  'RESTIC_REPOSITORY[[:space:]]*=|/var/lib/happylearn/backup/repository'
  'PGDMP|--[[:space:]]+PostgreSQL database dump'
  'object[_-]?key[[:space:]]*[:=]'
  'request[_-]?target[[:space:]]*=.*\?[A-Za-z0-9_.%~-]+='
)

for pattern in "${forbidden_patterns[@]}"; do
  scan_status=0
  LC_ALL=C grep -aEiq -- "$pattern" "$log" || scan_status=$?
  case "$scan_status" in
    0) reject_artifacts ;;
    1) ;;
    *) reject_artifacts ;;
  esac
done

validate_artifact_target || exit 2
find "$artifact_dir" -type f ! -name 'containers.log' -delete
validate_artifact_target || exit 2
find "$artifact_dir" -depth -type d ! -path "$artifact_dir" -empty -delete
temporary="$(mktemp "${log}.XXXXXX")"
cleanup_temporary() {
  [[ -z "${temporary:-}" ]] || rm -f -- "$temporary"
}
trap cleanup_temporary EXIT
omitted=0
while IFS= read -r line || [[ -n "$line" ]]; do
  case "$line" in
    diagnostics_version=1|state_status=created|state_status=running|state_status=paused|state_status=restarting|state_status=removing|state_status=exited|state_status=dead|oom_killed=true|oom_killed=false)
      printf '%s\n' "$line" >> "$temporary"
      ;;
    container=happylearn_phase2_*|container=happylearn_phase3_*|container=happylearn_phase4_*|container=happylearn_phase5_*)
      if [[ "$line" =~ ^container=happylearn_phase[2345]_[A-Za-z0-9_-]+$ ]]; then printf '%s\n' "$line" >> "$temporary"; else omitted=$((omitted+1)); fi
      ;;
    exit_code=*)
      if [[ "$line" =~ ^exit_code=[0-9]+$ ]]; then printf '%s\n' "$line" >> "$temporary"; else omitted=$((omitted+1)); fi
      ;;
    *) omitted=$((omitted+1)) ;;
  esac
done < "$log"
printf 'log_lines_omitted=%d\n' "$omitted" >> "$temporary"
chmod 0600 "$temporary"
mv "$temporary" "$log"
temporary=''
