#!/usr/bin/env bash
# shellcheck disable=SC2016
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
deploy_script="$repo_root/scripts/deploy-from-github.sh"
release_workflow="$repo_root/.github/workflows/release.yml"
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

require_literal() {
  local file=$1 literal=$2
  grep -Fq -- "$literal" "$file" || fail "missing contract in ${file#"$repo_root"/}: $literal"
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
require_literal "$release_workflow" 'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2'
require_literal "$release_workflow" 'actions/download-artifact@018cc2cf5baa6db3ef3c5f8a56943fffe632ef53 # v6.0.0'
require_literal "$release_workflow" 'git archive --format=tar --prefix="${archive_root}/" "$RELEASE_COMMIT"'
require_literal "$release_workflow" 'gzip -n > "$archive_file"'
require_literal "$release_workflow" 'sha256sum "$archive_name" > SHA256SUMS'
require_literal "$release_workflow" 'sha256sum --check SHA256SUMS'
require_literal "$release_workflow" '--draft'
require_literal "$release_workflow" 'gh release edit "$GITHUB_REF_NAME" --repo "$GITHUB_REPOSITORY" --draft=false'
require_literal "$release_workflow" '[[ $(jq -r .immutable <<<"$release_json") == true ]]'
require_literal "$release_workflow" 'name: github-release-package-${{ github.run_id }}'
require_literal "$release_workflow" 'path: |'
require_literal "$release_workflow" '${{ runner.temp }}/github-release-package/${{ steps.package.outputs.archive_name }}'
require_literal "$release_workflow" '${{ runner.temp }}/github-release-package/SHA256SUMS'
require_literal "$release_workflow" 'if-no-files-found: error'
require_literal "$release_workflow" 'retention-days: 1'
require_literal "$release_workflow" "find \"\$package_dir\" -mindepth 1 -maxdepth 1 -printf '%f\\n' | LC_ALL=C sort"
require_literal "$release_workflow" "expected_files=\$(printf '%s\\n%s\\n' \"\$archive_name\" SHA256SUMS | LC_ALL=C sort)"
require_literal "$release_workflow" "release_assets=\$(jq -r '[.assets[].name] | sort | .[]' <<<\"\$release_json\")"
require_literal "$release_workflow" ' $relative_entry != */.. && $relative_entry != */../* '

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
