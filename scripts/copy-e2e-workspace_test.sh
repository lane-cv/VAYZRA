#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

source_dir="$tmpdir/source"
destination_dir="$tmpdir/destination"
mkdir -p "$source_dir/node_modules/root-package" "$source_dir/web/node_modules/web-package" "$destination_dir"
printf 'workspace\n' > "$source_dir/package.json"
printf 'host dependency\n' > "$source_dir/node_modules/root-package/index.js"
printf 'host dependency\n' > "$source_dir/web/node_modules/web-package/index.js"

"$repo_root/scripts/copy-e2e-workspace.sh" "$source_dir" "$destination_dir"

test -f "$destination_dir/package.json"
test ! -e "$destination_dir/node_modules"
test ! -e "$destination_dir/web/node_modules"
