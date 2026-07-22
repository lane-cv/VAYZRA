#!/usr/bin/env bash
set -Eeuo pipefail

artifact_dir="${1:?artifact directory required}"
[[ -d "$artifact_dir" ]] || exit 0
find "$artifact_dir" -type f ! -name 'containers.log' -delete
find "$artifact_dir" -depth -type d ! -path "$artifact_dir" -empty -delete
log="$artifact_dir/containers.log"
[[ -f "$log" ]] || exit 0
temporary="$(mktemp "${log}.XXXXXX")"
sed -E \
  -e 's#postgres://[^[:space:]]+#postgres://REDACTED#g' \
  -e 's#redis://[^[:space:]]+#redis://REDACTED#g' \
  -e 's#(password|secret|token|authorization|cookie|body)=([^[:space:]]+)#\1=REDACTED#gI' \
  -e 's#(Bearer)[[:space:]]+[^[:space:]]+#\1 REDACTED#gI' \
  "$log" > "$temporary"
chmod 0600 "$temporary"
mv "$temporary" "$log"
