#!/bin/sh
set -eu

sanitized_log="${1:-}"
artifact_dir="${2:-}"
publish_id="${3:-}"
if [ ! -f "$sanitized_log" ] || [ -z "$artifact_dir" ] || [ -z "$publish_id" ]; then
  echo 'usage: publish-e2e-diagnostics.sh SANITIZED_LOG ARTIFACT_DIR PUBLISH_ID' >&2
  exit 2
fi
case "$publish_id" in *[!A-Za-z0-9._-]*) echo 'publish id contains unsafe characters' >&2; exit 2 ;; esac

final_log="$artifact_dir/containers.log"
publish_tmp="$artifact_dir/.containers.log.${publish_id}.tmp"
rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
if ! install -m 0600 "$sanitized_log" "$publish_tmp"; then
  rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
  exit 1
fi
if ! mv -f "$publish_tmp" "$final_log"; then
  rm -f "$final_log" "$publish_tmp" 2>/dev/null || true
  exit 1
fi
