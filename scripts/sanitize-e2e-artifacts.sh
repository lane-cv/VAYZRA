#!/usr/bin/env bash
set -Eeuo pipefail

artifact_dir="${1:?artifact directory required}"
[[ -d "$artifact_dir" ]] || exit 0
find "$artifact_dir" -type f ! -name 'containers.log' -delete
find "$artifact_dir" -depth -type d ! -path "$artifact_dir" -empty -delete
log="$artifact_dir/containers.log"
[[ -f "$log" ]] || exit 0
temporary="$(mktemp "${log}.XXXXXX")"
omitted=0
while IFS= read -r line || [[ -n "$line" ]]; do
  case "$line" in
    diagnostics_version=1|state_status=created|state_status=running|state_status=paused|state_status=restarting|state_status=removing|state_status=exited|state_status=dead|oom_killed=true|oom_killed=false)
      printf '%s\n' "$line" >> "$temporary"
      ;;
    container=happylearn_phase2_*|container=happylearn_phase3_*|container=happylearn_phase4_*)
      if [[ "$line" =~ ^container=happylearn_phase[234]_[A-Za-z0-9_-]+$ ]]; then printf '%s\n' "$line" >> "$temporary"; else omitted=$((omitted+1)); fi
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
