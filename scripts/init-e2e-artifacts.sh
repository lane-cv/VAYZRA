#!/bin/sh
set -eu

destination="${1:-}"
if [ -z "$destination" ] || [ "${destination#/}" = "$destination" ] || [ "$destination" = / ]; then
  echo "usage: $0 /absolute/disposable-artifact-directory" >&2
  exit 2
fi

rm -f "$destination/containers.log" "$destination"/.containers.log.*
mkdir -p "$destination/results"
chown -R 0:0 "$destination/results"
chmod 0700 "$destination/results"
chown -R 1000:1000 "$destination/results"
