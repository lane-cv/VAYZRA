#!/usr/bin/env bash
set -Eeuo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: copy-e2e-workspace.sh SOURCE DESTINATION" >&2
  exit 2
fi

source_dir="$1"
destination_dir="$2"

tar -C "$source_dir" \
  --exclude='./.git' \
  --exclude='./.tools' \
  --exclude='./.superpowers' \
  --exclude='./test-results' \
  --exclude='./node_modules' \
  --exclude='./web/node_modules' \
  -cf - . | tar -C "$destination_dir" -xf -
