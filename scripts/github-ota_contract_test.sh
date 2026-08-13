#!/usr/bin/env bash
# shellcheck disable=SC2016
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
deploy_script="$repo_root/scripts/deploy-from-github.sh"
release_workflow="${HAPPYLEARN_RELEASE_WORKFLOW:-$repo_root/.github/workflows/release.yml}"
verify_workflow="$repo_root/.github/workflows/verify.yml"
phase6_workflow="$repo_root/.github/workflows/phase6.yml"
compose_override="$repo_root/deploy/compose.github.yml"
compose_base="$repo_root/deploy/compose.dev.yml"
app_dockerfile="$repo_root/Dockerfile"
worker_dockerfile="$repo_root/Dockerfile.worker"
agent_dockerfile="$repo_root/deploy/Dockerfile.update-agent"
dockerignore="$repo_root/.dockerignore"

fail() {
  printf 'GitHub OTA contract: FAIL: %s\n' "$1" >&2
  exit 1
}

contract_node() {
  if command -v node >/dev/null 2>&1; then
    node "$@"
  else
    node.exe "$@"
  fi
}

require_literal() {
  local file=$1 literal=$2
  grep -Fq -- "$literal" "$file" || {
    if [[ ${HAPPYLEARN_CONTRACT_PROBE:-0} == 1 ]]; then return 1; fi
    fail "missing contract in ${file#"$repo_root"/}: $literal"
  }
}

require_workflow_order() {
  local file=$1 before=$2 after=$3 before_line after_line
  before_line=$(grep -nF -- "$before" "$file" | head -n 1 | cut -d: -f1) || return 1
  after_line=$(grep -nF -- "$after" "$file" | head -n 1 | cut -d: -f1) || return 1
  [[ $before_line -lt $after_line ]]
}

require_line_count() {
  local file=$1 literal=$2 expected_count=$3 actual_count
  actual_count=$(grep -Fxc -- "$literal" "$file" || true)
  [[ $actual_count -eq $expected_count ]]
}

require_literal_count() {
  local file=$1 literal=$2 expected_count=$3 actual_count
  actual_count=$(grep -Fc -- "$literal" "$file" || true)
  [[ $actual_count -eq $expected_count ]]
}

release_create_is_preflight_guarded() {
  local file=$1 preflight_line else_line create_line
  preflight_line=$(grep -nF -- '          releases_json=$(gh api --paginate --slurp "/repos/${GITHUB_REPOSITORY}/releases?per_page=100")' "$file" | head -n 1 | cut -d: -f1) || return 1
  else_line=$(awk -v start="$preflight_line" 'NR > start && /^            0\)$/ { print NR; exit }' "$file") || return 1
  create_line=$(grep -nF -- 'gh release create "$GITHUB_REF_NAME" "$archive_file" "$checksum_file"' "$file" | head -n 1 | cut -d: -f1) || return 1
  [[ -n $else_line && $preflight_line -lt $else_line && $else_line -lt $create_line ]] || return 1
  require_literal_count "$file" 'gh release create "$GITHUB_REF_NAME" "$archive_file" "$checksum_file"' 1
}

validate_contract_archive() {
  local archive_file=$1 archive_root=$2 archive_listing entry relative_entry result=0
  declare -A seen_entries=()
  archive_listing=$(mktemp)
  tar -tzf "$archive_file" > "$archive_listing" || result=1
  if ((result == 0)); then
  mapfile -t archive_entries < "$archive_listing"
    ((${#archive_entries[@]} > 0)) || result=1
    for entry in "${archive_entries[@]}"; do
      [[ -n $entry && $entry == "${archive_root}/"* && $entry != /* ]] || result=1
      [[ -z ${seen_entries[$entry]+_} ]] || result=1
      seen_entries["$entry"]=1
      relative_entry=${entry#"${archive_root}/"}
      [[ -z $relative_entry || ( $relative_entry != .. && $relative_entry != ../* && $relative_entry != */.. && $relative_entry != */../* ) ]] || result=1
    done
  fi
  rm -f -- "$archive_listing"
  return "$result"
}

validate_contract_checksum() {
  local package_dir=$1 archive_name=$2 expected_checksum result=0
  expected_checksum=$(mktemp)
  (
    cd "$package_dir"
    sha256sum "$archive_name" > "$expected_checksum"
    cmp -s "$expected_checksum" SHA256SUMS && sha256sum --check SHA256SUMS
  ) || result=1
  rm -f -- "$expected_checksum"
  return "$result"
}

validate_contract_release_assets() {
  local release_json=$1 archive_name=$2 archive_size=$3 archive_digest=$4 checksum_size=$5 checksum_digest=$6
  contract_node - "$release_json" "$archive_name" "$archive_size" "$archive_digest" "$checksum_size" "$checksum_digest" <<'NODE'
const [releaseJson, archiveName, archiveSize, archiveDigest, checksumSize, checksumDigest] = process.argv.slice(2);
const release = JSON.parse(releaseJson);
const expected = new Map([
  [archiveName, { size: Number(archiveSize), digest: archiveDigest }],
  ['SHA256SUMS', { size: Number(checksumSize), digest: checksumDigest }],
]);
const assets = release.assets;
process.exit(
  Array.isArray(assets) && assets.length === expected.size &&
  assets.every((asset) => {
    const expectedAsset = expected.get(asset.name);
    return expectedAsset !== undefined && asset.state === 'uploaded' &&
      asset.size === expectedAsset.size && asset.digest === expectedAsset.digest;
  }) ? 0 : 1,
);
NODE
}

release_package_contract() {
  local file=$1 upload_paths expected_upload_paths
  require_literal "$file" 'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2' || return 1
  require_literal "$file" 'actions/download-artifact@018cc2cf5baa6db3ef3c5f8a56943fffe632ef53 # v6.0.0' || return 1
  require_literal "$file" 'git archive --format=tar --prefix="${archive_root}/" "$RELEASE_COMMIT"' || return 1
  require_literal "$file" 'gzip -n > "$archive_file"' || return 1
  require_literal "$file" 'tar -tzf "$archive_file" > "$archive_listing"' || return 1
  require_literal "$file" '[[ -z ${seen_entries[$entry]+_} ]]' || return 1
  require_literal "$file" 'sha256sum "$archive_name" > SHA256SUMS' || return 1
  require_literal "$file" 'cmp -s "$expected_checksum" SHA256SUMS' || return 1
  require_literal "$file" 'sha256sum --check SHA256SUMS' || return 1
  require_literal "$file" 'name: github-release-package-${{ github.run_id }}' || return 1
  require_literal "$file" 'if-no-files-found: error' || return 1
  require_literal "$file" 'retention-days: 1' || return 1
  expected_upload_paths=$'            ${{ runner.temp }}/github-release-package/${{ steps.package.outputs.archive_name }}\n            ${{ runner.temp }}/github-release-package/SHA256SUMS'
  upload_paths=$(awk '
    /^      - name: Upload release package$/ { upload=1 }
    upload && /^          path: \|$/ { paths=1; next }
    paths && /^            / { print; next }
    paths { exit }
  ' "$file")
  [[ $upload_paths == "$expected_upload_paths" ]] || return 1
  require_literal "$file" "actual_files=\$(find \"\$package_dir\" -mindepth 1 -printf '%P\\n' | LC_ALL=C sort)" || return 1
  require_literal "$file" '[[ "$top_level_files" == "$expected_files" && "$actual_files" == "$expected_files" ]]' || return 1
  require_literal "$file" 'gh release create "$GITHUB_REF_NAME" "$archive_file" "$checksum_file"' || return 1
  require_literal "$file" '--title "$GITHUB_REF_NAME" --draft' || return 1
  require_literal "$file" 'gh release edit "$GITHUB_REF_NAME" --repo "$GITHUB_REPOSITORY" --draft=false' || return 1
  require_literal "$file" "X-GitHub-Api-Version: 2026-03-10" || return 1
  require_literal "$file" '[[ $(jq -r .tag_name <<<"$final_release_json") == "$GITHUB_REF_NAME" ]]' || return 1
  require_literal "$file" '[[ $(jq -r .draft <<<"$final_release_json") == false ]]' || return 1
  require_literal "$file" '[[ $(jq -r .prerelease <<<"$final_release_json") == false ]]' || return 1
  require_literal "$file" '[[ $(jq -r .immutable <<<"$final_release_json") == true ]]' || return 1
  require_literal "$file" "release_assets=\$(jq -r '[.assets[].name] | sort | .[]' <<<\"\$final_release_json\")" || return 1
  require_literal "$file" '[[ "$release_assets" == "$expected_files" ]]' || return 1
  require_literal "$file" 'releases_json=$(gh api --paginate --slurp "/repos/${GITHUB_REPOSITORY}/releases?per_page=100")' || return 1
  require_literal "$file" "matching_releases=\$(jq -c --arg tag \"\$GITHUB_REF_NAME\" '[.[] | .[] | select(.tag_name == \$tag)]' <<<\"\$releases_json\")" || return 1
  require_literal "$file" 'matching_count=$(jq length <<<"$matching_releases")' || return 1
  require_literal "$file" 'case "$matching_count" in' || return 1
  require_literal "$file" '            0)' || return 1
  require_literal "$file" '            1)' || return 1
  require_literal "$file" '            *)' || return 1
  require_literal "$file" 'Existing release query returned $matching_count entries for $GITHUB_REF_NAME; refusing ambiguous mutation.' || return 1
  require_literal "$file" 'release_assets_are_complete "$release_json"' || return 1
  require_literal "$file" '[[ $(jq -r .draft <<<"$release_json") == true ]]' || return 1
  require_literal "$file" '[[ $(jq -r .draft <<<"$release_json") == false && $(jq -r .immutable <<<"$release_json") == true ]] || {' || return 1
  require_literal "$file" 'Existing draft release for $GITHUB_REF_NAME is partial or unexpected' || return 1
  require_literal "$file" 'Existing published release for $GITHUB_REF_NAME is not the expected immutable terminal state' || return 1
  require_literal "$file" 'release_assets_are_complete "$release_json" || {' || return 1
  require_literal "$file" 'release_assets_are_complete "$final_release_json"' || return 1
  require_literal_count "$file" 'release_assets_are_complete "$release_json" || {' 2 || return 1
  require_literal "$file" '.state == "uploaded" and' || return 1
  require_literal "$file" '(.size == $archive_size and .digest == $archive_digest)' || return 1
  require_literal "$file" '(.size == $checksum_size and .digest == $checksum_digest)' || return 1
  require_line_count "$file" '            gh release edit "$GITHUB_REF_NAME" --repo "$GITHUB_REPOSITORY" --draft=false' 1 || return 1
  release_create_is_preflight_guarded "$file" || return 1
  require_workflow_order "$file" '--title "$GITHUB_REF_NAME" --draft' 'gh release edit "$GITHUB_REF_NAME" --repo "$GITHUB_REPOSITORY" --draft=false' || return 1
  require_workflow_order "$file" 'gh release edit "$GITHUB_REF_NAME" --repo "$GITHUB_REPOSITORY" --draft=false' "X-GitHub-Api-Version: 2026-03-10" || return 1
}

release_package_negative_probes() {
  local fixture archive_name='VAYZRA-v0.1.2.tar.gz' archive_root='VAYZRA-v0.1.2'
  fixture=$(mktemp -d)
  mkdir -p "$fixture/$archive_root"
  printf 'release package probe\n' > "$fixture/$archive_root/file"
  tar -C "$fixture" -czf "$fixture/$archive_name" "$archive_root"

  cp "$fixture/$archive_name" "$fixture/truncated.tar.gz"
  truncate -s -8 "$fixture/truncated.tar.gz"
  if validate_contract_archive "$fixture/truncated.tar.gz" "$archive_root"; then
    fail 'archive validator accepted a truncated archive'
  fi

  tar -C "$fixture" -cf "$fixture/duplicate.tar" "$archive_root/file"
  tar -C "$fixture" -rf "$fixture/duplicate.tar" "$archive_root/file"
  gzip -n < "$fixture/duplicate.tar" > "$fixture/duplicate.tar.gz"
  if validate_contract_archive "$fixture/duplicate.tar.gz" "$archive_root"; then
    fail 'archive validator accepted duplicate members'
  fi

  tar -C "$fixture" --transform="s#^#${archive_root}/../#" -czf "$fixture/traversal.tar.gz" "$archive_root/file"
  if validate_contract_archive "$fixture/traversal.tar.gz" "$archive_root"; then
    fail 'archive validator accepted a traversal member'
  fi

  (
    cd "$fixture"
    sha256sum "$archive_name" > SHA256SUMS
    printf '\357\273\277' | cat - SHA256SUMS > checksum-with-bom
    mv checksum-with-bom SHA256SUMS
  )
  if validate_contract_checksum "$fixture" "$archive_name"; then
    fail 'checksum validator accepted a BOM manifest'
  fi
  (
    cd "$fixture"
    sha256sum "$archive_name" > SHA256SUMS
    sha256sum "$archive_name" >> SHA256SUMS
  )
  if validate_contract_checksum "$fixture" "$archive_name"; then
    fail 'checksum validator accepted duplicate checksum entries'
  fi

  archive_size=$(stat -c %s "$fixture/$archive_name")
  checksum_size=$(stat -c %s "$fixture/SHA256SUMS")
  archive_digest="sha256:$(sha256sum "$fixture/$archive_name" | awk '{print $1}')"
  checksum_digest="sha256:$(sha256sum "$fixture/SHA256SUMS" | awk '{print $1}')"
  release_json=$(printf '{"assets":[{"name":"%s","state":"uploaded","size":%s,"digest":"%s"},{"name":"SHA256SUMS","state":"uploaded","size":%s,"digest":"%s"}]}' \
    "$archive_name" "$archive_size" "$archive_digest" "$checksum_size" "$checksum_digest")
  validate_contract_release_assets "$release_json" "$archive_name" "$archive_size" "$archive_digest" "$checksum_size" "$checksum_digest" ||
    fail 'release asset validator rejected the exact uploaded local assets'
  for mutation in state size digest duplicate; do
    case $mutation in
      state) mutated_release_json=$(contract_node - "$release_json" <<'NODE'
const release = JSON.parse(process.argv[2]); release.assets[0].state = 'starter'; process.stdout.write(JSON.stringify(release));
NODE
) ;;
      size) mutated_release_json=$(contract_node - "$release_json" <<'NODE'
const release = JSON.parse(process.argv[2]); release.assets[0].size += 1; process.stdout.write(JSON.stringify(release));
NODE
) ;;
      digest) mutated_release_json=$(contract_node - "$release_json" <<'NODE'
const release = JSON.parse(process.argv[2]); release.assets[0].digest = 'sha256:wrong'; process.stdout.write(JSON.stringify(release));
NODE
) ;;
      duplicate) mutated_release_json=$(contract_node - "$release_json" <<'NODE'
const release = JSON.parse(process.argv[2]); release.assets.push(release.assets[0]); process.stdout.write(JSON.stringify(release));
NODE
) ;;
    esac
    if validate_contract_release_assets "$mutated_release_json" "$archive_name" "$archive_size" "$archive_digest" "$checksum_size" "$checksum_digest"; then
      fail "release asset validator accepted $mutation mutation"
    fi
  done
  rm -rf -- "$fixture"
}

release_package_workflow_mutation_probes() {
  local fixture
  fixture=$(mktemp)
  cp "$release_workflow" "$fixture"

  sed '/--title "\$GITHUB_REF_NAME" --draft$/s/ --draft$//' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted create without draft'; fi

  awk '
    /^            \$\{\{ runner\.temp \}\}\/github-release-package\/SHA256SUMS$/ { print; print "            ${{ runner.temp }}/github-release-package/EXTRA"; next }
    { print }
  ' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted an extra upload path'; fi

  sed '/actual_files=$(find "\$package_dir" -mindepth 1 -printf/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted missing recursive downloaded-file closure'; fi

  sed '/\[\[ "\$release_assets" == "\$expected_files" \]\]/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted missing final asset closure'; fi

  awk '
    { lines[NR]=$0 }
    /gh release create "\$GITHUB_REF_NAME" "\$archive_file" "\$checksum_file"/ { create=NR }
    /gh release edit "\$GITHUB_REF_NAME" --repo "\$GITHUB_REPOSITORY" --draft=false/ { edit=NR }
    END {
      for (line=1; line<=NR; line++) {
        if (line == create) print lines[edit]
        if (line != edit) print lines[line]
      }
    }
  ' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted edit before create'; fi

  sed '/X-GitHub-Api-Version: 2026-03-10/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted missing API version'; fi

  sed '/\[\[ $(jq -r .tag_name <<<"\$final_release_json") == "\$GITHUB_REF_NAME" \]\]/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted missing final tag assertion'; fi

  sed '/\[\[ $(jq -r .draft <<<"\$final_release_json") == false \]\]/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted missing final draft assertion'; fi

  sed '/\[\[ $(jq -r .prerelease <<<"\$final_release_json") == false \]\]/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted missing final prerelease assertion'; fi

  sed '/\[\[ $(jq -r .immutable <<<"\$final_release_json") == true \]\]/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted missing immutable assertion'; fi

  sed '/releases_json=$(gh api --paginate --slurp/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted an unguarded release-query failure'; fi

  sed '/\.state == "uploaded" and/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted release assets without uploaded state checks'; fi

  sed '/(\.size == \$archive_size and \.digest == \$archive_digest)/d' "$release_workflow" > "$fixture"
  if (HAPPYLEARN_CONTRACT_PROBE=1 release_package_contract "$fixture"); then fail 'release contract accepted release assets without archive size and digest checks'; fi

  rm -f -- "$fixture"
}

dockerfile_images_pinned() {
  local file=$1 line image alias
  declare -A stages=()
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line =~ ^#[[:space:]]*syntax=([^[:space:]]+) ]]; then
      [[ ${BASH_REMATCH[1]} =~ ^[^@[:space:]]+:[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]] || return 1
      continue
    fi
    if [[ $line =~ ^[[:space:]]*[Ff][Rr][Oo][Mm][[:space:]]+([^[:space:]]+)([[:space:]]+[Aa][Ss][[:space:]]+([A-Za-z0-9_.-]+))? ]]; then
      image=${BASH_REMATCH[1]}
      alias=${BASH_REMATCH[3]:-}
      if [[ ! -v "stages[$image]" ]] &&
        [[ ! $image =~ ^[^@[:space:]]+:[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]]; then
        return 1
      fi
      if [[ -n $alias ]]; then
        stages[$alias]=1
      fi
    fi
  done <"$file"
}

compose_images_controlled() {
  local image
  while IFS= read -r image; do
    [[ -n $image ]] || continue
    if [[ $image =~ ^[^@[:space:]]+:[^@[:space:]]+@sha256:[0-9a-f]{64}$ ]]; then
      continue
    fi
    case $image in
      happylearn-app:local|happylearn-worker:local|happylearn-update-agent:local|happylearn-backup:phase5|\
      happylearn-offline/postgres:18.4|happylearn-offline/redis:8.8|happylearn-offline/debian:12.12-slim|\
      happylearn-offline/alpine:3.23.3|happylearn-offline/aistor:2026-06-06) ;;
      *) return 1 ;;
    esac
  done
}

[[ $(git -C "$repo_root" check-attr eol -- scripts/deploy-from-github.sh) == 'scripts/deploy-from-github.sh: eol: lf' ]] ||
  fail 'deploy shell scripts must be checked out with LF line endings'
if LC_ALL=C grep -n $'\r' "$deploy_script" >/dev/null; then
  fail 'deploy shell script contains CR bytes despite the LF checkout contract'
fi
bash -n "$deploy_script"

require_literal "$release_workflow" 'release_commit=$(git rev-parse "$GITHUB_REF_NAME^{commit}")'
require_literal "$release_workflow" 'event_commit=$(git rev-parse "$GITHUB_SHA^{commit}")'
require_literal "$release_workflow" 'head_sha=${RELEASE_COMMIT}'
require_literal "$release_workflow" '.workflow_runs[0]'
require_literal "$release_workflow" 'permissions: {}'
require_literal "$release_workflow" 'persist-credentials: false'
require_literal "$release_workflow" 'needs: validate'
require_literal "$release_workflow" '--repo "$GITHUB_REPOSITORY" --verify-tag'
release_package_contract "$release_workflow" || fail 'release package contract is incomplete'
release_package_negative_probes
release_package_workflow_mutation_probes

while IFS= read -r action; do
  [[ $action =~ @[0-9a-f]{40}$ ]] || fail "GitHub Action is not pinned to a full commit SHA: $action"
done < <(sed -nE 's/^[[:space:]]*(-[[:space:]]*)?uses:[[:space:]]*([^[:space:]#]+).*/\2/p' "$release_workflow" "$verify_workflow" "$phase6_workflow")
require_literal "$phase6_workflow" 'aquasecurity/setup-trivy@3fb12ec12f41e471780db15c232d5dd185dcb514 # v0.2.6'

require_literal "$verify_workflow" 'bash -n scripts/deploy-from-github.sh'
require_literal "$verify_workflow" 'shellcheck scripts/deploy-from-github.sh'
require_literal "$verify_workflow" 'docker build --file deploy/Dockerfile.update-agent'
require_literal "$verify_workflow" 'deploy/compose.github.yml'
require_literal "$verify_workflow" 'DOCKER_CONFIG=/tmp/docker-config'
require_literal "$verify_workflow" 'happylearn-update-agent:ci buildx ls'
if ! sed -n '1,/^[[:space:]]*- run: pnpm e2e-contracts$/p' "$verify_workflow" |
  grep -Eq 'apt-get install .*([[:space:]]|^)ripgrep([[:space:]]|$)'; then
  fail 'verify workflow must install ripgrep before running E2E contracts'
fi

require_literal "$compose_override" 'DOCKER_CONFIG: /tmp/docker-config'
require_literal "$compose_override" '- /tmp:rw,noexec,nosuid,size=16m,uid=${HAPPYLEARN_UPDATE_HOST_UID:?set HAPPYLEARN_UPDATE_HOST_UID},gid=${HAPPYLEARN_UPDATE_HOST_GID:?set HAPPYLEARN_UPDATE_HOST_GID},mode=0700'
require_literal "$verify_workflow" '--entrypoint docker'
require_literal "$verify_workflow" '--tmpfs /tmp:rw,noexec,nosuid,size=16m,uid=1000,gid=1000,mode=0700'

require_literal "$deploy_script" 'HAPPYLEARN_UPDATE_HOST_UID=%s'
require_literal "$deploy_script" 'HAPPYLEARN_UPDATE_HOST_GID=%s'
require_literal "$deploy_script" 'HAPPYLEARN_UPDATE_DOCKER_GID=%s'
require_literal "$compose_override" 'update-agent-state-init:'
require_literal "$compose_override" 'chown -R "$$UPDATE_HOST_UID:$$UPDATE_HOST_GID" /state'
require_literal "$compose_override" 'user: "${HAPPYLEARN_UPDATE_HOST_UID:?set HAPPYLEARN_UPDATE_HOST_UID}:${HAPPYLEARN_UPDATE_HOST_GID:?set HAPPYLEARN_UPDATE_HOST_GID}"'
require_literal "$compose_override" 'group_add:'
require_literal "$compose_override" '- "${HAPPYLEARN_UPDATE_DOCKER_GID:?set HAPPYLEARN_UPDATE_DOCKER_GID}"'
require_literal "$deploy_script" "'alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659'"
require_literal "$deploy_script" "readonly OFFLINE_POSTGRES_IMAGE='happylearn-offline/postgres:18.4'"
require_literal "$deploy_script" "readonly OFFLINE_REDIS_IMAGE='happylearn-offline/redis:8.8'"
require_literal "$deploy_script" "readonly OFFLINE_INIT_IMAGE='happylearn-offline/debian:12.12-slim'"
require_literal "$deploy_script" "readonly OFFLINE_STATE_INIT_IMAGE='happylearn-offline/alpine:3.23.3'"
require_literal "$deploy_script" "readonly OFFLINE_AISTOR_IMAGE='happylearn-offline/aistor:2026-06-06'"
require_literal "$deploy_script" 'HAPPYLEARN_LOCAL_POSTGRES_IMAGE=%s'
require_literal "$deploy_script" 'HAPPYLEARN_LOCAL_REDIS_IMAGE=%s'
require_literal "$deploy_script" 'HAPPYLEARN_LOCAL_INIT_IMAGE=%s'
require_literal "$deploy_script" 'HAPPYLEARN_LOCAL_STATE_INIT_IMAGE=%s'
require_literal "$deploy_script" 'up -d --no-build --pull never --wait --wait-timeout 300'
require_literal "$compose_base" 'aistor-license-init:'
require_literal "$compose_base" 'aistor_license_runtime:/license:ro'
require_literal "$compose_base" 'aistor_license_runtime:'

# GitHub Releases are immutable, so every image reference interpreted by the
# local OTA path must be immutable as well. Keep the readable tag and pin the
# multi-architecture index digest so amd64 and arm64 hosts share one contract.
require_literal "$app_dockerfile" '# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e'
require_literal "$app_dockerfile" 'FROM node:24.18.0-bookworm-slim@sha256:6f7b03f7c2c8e2e784dcf9295400527b9b1270fd37b7e9a7285cf83b6951452d AS web-build'
require_literal "$app_dockerfile" 'FROM golang:1.26.5-bookworm@sha256:53eeac89074db483fdf0ab3be1df32bf6e47562263d2d0d6baa7f26acb4957dd AS go-build-base'
require_literal "$app_dockerfile" 'FROM debian:12.12-slim@sha256:d5d3f9c23164ea16f31852f95bd5959aad1c5e854332fe00f7b3a20fcc9f635c AS runtime-base'
require_literal "$worker_dockerfile" '# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e'
require_literal "$worker_dockerfile" 'FROM golang:1.26.5-bookworm@sha256:53eeac89074db483fdf0ab3be1df32bf6e47562263d2d0d6baa7f26acb4957dd AS worker-build'
require_literal "$worker_dockerfile" 'FROM debian:12.12-slim@sha256:d5d3f9c23164ea16f31852f95bd5959aad1c5e854332fe00f7b3a20fcc9f635c AS runtime'
require_literal "$agent_dockerfile" 'FROM golang:1.26.5-bookworm@sha256:53eeac89074db483fdf0ab3be1df32bf6e47562263d2d0d6baa7f26acb4957dd AS build'
require_literal "$agent_dockerfile" 'FROM docker:28.3.3-cli@sha256:0135662b510037ea581d99c2e5929c5e01185139c0b86986a418bd4da0b98a44'
require_literal "$compose_base" '${HAPPYLEARN_LOCAL_POSTGRES_IMAGE:-postgres:18.4@sha256:a02db8cac496f15b094798a38254f14d6e00741f709360e5e00bb6668ea31636}'
require_literal "$compose_base" '${HAPPYLEARN_LOCAL_REDIS_IMAGE:-redis:8.8@sha256:3eafabb4c93fcb8b36b666e07a43f096cb157bc6b07dce4b2492b895c63cf37f}'
require_literal "$compose_base" '${HAPPYLEARN_LOCAL_INIT_IMAGE:-debian:12.12-slim@sha256:d5d3f9c23164ea16f31852f95bd5959aad1c5e854332fe00f7b3a20fcc9f635c}'
require_literal "$compose_override" '${HAPPYLEARN_LOCAL_STATE_INIT_IMAGE:-alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659}'
require_literal "$dockerignore" '/.secrets/'
require_literal "$repo_root/README.md" 'sha256sum vayzra-images.tar.gz > vayzra-images.tar.gz.sha256'
require_literal "$repo_root/README.md" 'sha256sum -c /path/to/vayzra-images.tar.gz.sha256'

for dockerfile in "$app_dockerfile" "$worker_dockerfile" "$agent_dockerfile"; do
  dockerfile_images_pinned "$dockerfile" ||
    fail "external Dockerfile reference is not pinned: ${dockerfile#"$repo_root"/}"
done
negative_dockerfile=$(mktemp)
trap 'rm -f -- "$negative_dockerfile"' EXIT
cp "$app_dockerfile" "$negative_dockerfile"
printf '\nFROM attacker-controlled:latest AS regression-probe\n' >>"$negative_dockerfile"
if dockerfile_images_pinned "$negative_dockerfile"; then
  fail 'Dockerfile digest validator accepted a movable external tag'
fi
rm -f -- "$negative_dockerfile"
trap - EXIT

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  compose_environment=(
    "HAPPYLEARN_AISTOR_LICENSE_FILE=$repo_root/deploy/fixtures/development-github-token-do-not-use-in-production"
    "HAPPYLEARN_UPDATE_REPOSITORY=$repo_root"
    "HAPPYLEARN_UPDATE_AGENT_TOKEN_FILE=$repo_root/deploy/fixtures/development-update-agent-token-do-not-use-in-production"
    "HAPPYLEARN_UPDATE_AGENT_GITHUB_TOKEN_FILE=$repo_root/deploy/fixtures/development-github-token-do-not-use-in-production"
    'HAPPYLEARN_UPDATE_HOST_UID=1000'
    'HAPPYLEARN_UPDATE_HOST_GID=1000'
    'HAPPYLEARN_UPDATE_DOCKER_GID=0'
  )
  compose_images=$(env "${compose_environment[@]}" docker compose --profile '*' \
    --project-directory "$repo_root" -f "$compose_base" -f "$compose_override" config --images)
  compose_images_controlled <<<"$compose_images" || fail 'rendered Compose config contains an uncontrolled image reference'

  negative_images=$(printf 'services:\n  movable-image-regression-probe:\n    image: attacker-controlled:latest\n' | \
    env "${compose_environment[@]}" docker compose --profile '*' --project-directory "$repo_root" \
      -f "$compose_base" -f "$compose_override" -f - config --images)
  if compose_images_controlled <<<"$negative_images"; then
    fail 'Compose image validator accepted a movable external tag'
  fi
fi

if [[ ${HAPPYLEARN_OTA_LIVE_IMAGE_CONTRACT:-0} == 1 ]]; then
  require_literal "$verify_workflow" 'HAPPYLEARN_OTA_LIVE_IMAGE_CONTRACT: '\''1'\'''
  contract_alias="happylearn-contract/offline-alpine:$$"
  contract_archive=$(mktemp)
  cleanup_live_contract() {
    docker image rm "$contract_alias" >/dev/null 2>&1 || true
    rm -f -- "$contract_archive"
  }
  trap cleanup_live_contract EXIT
  docker pull 'alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659' >/dev/null
  docker tag 'alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659' "$contract_alias"
  docker save --output "$contract_archive" "$contract_alias"
  docker image rm "$contract_alias" >/dev/null
  docker load --input "$contract_archive" >/dev/null
  docker image inspect "$contract_alias" >/dev/null
  docker run --rm --pull never "$contract_alias" /bin/true
fi

if command -v shellcheck >/dev/null 2>&1; then
  shellcheck "$deploy_script" "$repo_root/scripts/github-ota_contract_test.sh"
fi

printf 'GitHub OTA contract: PASS\n'
