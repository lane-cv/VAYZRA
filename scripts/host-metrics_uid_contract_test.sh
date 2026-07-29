#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image='golang:1.26.5-bookworm'

fail() {
  printf 'host metrics uid contract: FAIL: %s\n' "$1" >&2
  exit 1
}

reject_path() {
  if validate_host_metrics_backup_path "$1"; then
    fail "unsafe path was accepted: $1"
  fi
}

case "${1:-}" in
  --container-root)
    [[ "$EUID" -eq 0 ]] || fail "container setup is not root"
    install -d -o 0 -g 0 -m 0755 /fixture /fixture/parent
    install -d -o 10003 -g 0 -m 0700 \
      /fixture/parent/repository \
      /fixture/parent/other
    install -d -o 10003 -g 0 -m 0750 /fixture/parent/readable
    install -d -o 10005 -g 0 -m 0700 /fixture/parent/wrong-owner
    ln -s /fixture/parent /fixture/symlink-parent
    ln -s /fixture/parent/repository /fixture/leaf-symlink
    exec setpriv --reuid=10004 --regid=0 --clear-groups \
      bash /workspace/scripts/host-metrics_uid_contract_test.sh \
      --deployment-account
    ;;
  --deployment-account)
    [[ "$EUID" -eq 10004 ]] || fail "deployment account UID changed"
    # shellcheck source=scripts/host-metrics-path.sh
    source /workspace/scripts/host-metrics-path.sh

    production_path='/fixture/parent/repository'
    validate_host_metrics_backup_path "$production_path" ||
      fail "UID 10004 rejected UID 10003:0 mode 0700 repository"
    df -Pk "$production_path" >/dev/null ||
      fail "df cannot inspect UID 10003:0 mode 0700 repository"

    reject_path '/fixture/parent/repository/'
    reject_path '/fixture//parent/repository'
    reject_path '/fixture/./parent/repository'
    reject_path '/fixture/parent/../parent/repository'
    reject_path '/fixture/symlink-parent/repository'
    reject_path '/fixture/leaf-symlink'
    reject_path '/fixture/parent/readable'
    reject_path '/fixture/parent/wrong-owner'
    ;;
  '')
    docker run --rm \
      -v "$repo_root:/workspace:ro" \
      "$image" \
      bash /workspace/scripts/host-metrics_uid_contract_test.sh \
      --container-root
    printf '%s\n' 'host metrics uid contract: PASS uid=10004 owner=10003 mode=0700 df=ok'
    ;;
  *)
    fail "unexpected argument"
    ;;
esac
