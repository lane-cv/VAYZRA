#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$repo_root"
mode=${1:---repository}
fail() { printf 'phase6 security: FAIL: %s\n' "$1" >&2; exit 1; }
case $mode in --repository|--live) ;; *) fail 'mode must be --repository or --live' ;; esac
for command in go pnpm govulncheck trivy jq; do command -v "$command" >/dev/null 2>&1 || fail "missing scanner: $command"; done

scan_root=$(mktemp -d "${TMPDIR:-/tmp}/happylearn-phase6-security.XXXXXX")
chmod 0700 "$scan_root"
cleanup() { find "$scan_root" -mindepth 1 -delete 2>/dev/null || true; rmdir "$scan_root" 2>/dev/null || true; }
trap cleanup EXIT HUP INT TERM

timeout --foreground --kill-after=30s 900s govulncheck ./... >"$scan_root/govulncheck.txt"
timeout --foreground --kill-after=30s 900s pnpm audit --prod --audit-level high >"$scan_root/pnpm-audit.txt"
timeout --foreground --kill-after=30s 1200s trivy fs --scanners vuln,secret,misconfig \
  --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 --format json --output "$scan_root/trivy-fs.json" "$repo_root"

if [[ $mode == --live ]]; then
  for command in docker curl openssl; do command -v "$command" >/dev/null 2>&1 || fail "missing live command: $command"; done
  project=${HAPPYLEARN_PHASE6_PROJECT:?}
  env_file=${HAPPYLEARN_PHASE6_ENV_FILE:?}
  https_base=${E2E_PHASE6_HTTPS_BASE_URL:?}
  http_base=${E2E_PHASE6_HTTP_BASE_URL:?}
  hostname=${E2E_PHASE6_HOSTNAME:?}
  ca_file=${E2E_PHASE6_CA_FILE:?}
  [[ $https_base =~ ^https://[^/:]+:([0-9]{1,5})$ ]] || fail 'HTTPS base URL invalid'
  https_port=${BASH_REMATCH[1]}
  [[ $http_base =~ ^http://[^/:]+:([0-9]{1,5})$ ]] || fail 'HTTP base URL invalid'
  http_port=${BASH_REMATCH[1]}
  [[ $project =~ ^happylearn_phase6_[a-f0-9]{12}_prod$ ]] || fail 'project identity invalid'
  [[ -f $env_file && ! -L $env_file && -f $ca_file && ! -L $ca_file ]] || fail 'live input invalid'
  curl --noproxy '*' --resolve "$hostname:$https_port:127.0.0.1" --fail --silent --cacert "$ca_file" "$https_base/" >/dev/null
  [[ $(curl --noproxy '*' --resolve "$hostname:$http_port:127.0.0.1" --silent --output /dev/null --write-out '%{http_code}' "$http_base/") == 308 ]] || fail 'HTTP redirect missing'
  for path in internal/metrics internal/readiness; do
    [[ $(curl --noproxy '*' --resolve "$hostname:$https_port:127.0.0.1" --silent --cacert "$ca_file" --output /dev/null --write-out '%{http_code}' "$https_base/$path") == 404 ]] || fail 'internal route exposed'
  done
  published=$(docker ps --filter "label=com.docker.compose.project=$project" --format '{{.Names}} {{.Ports}}')
  ! grep -Evq "${project}-caddy-1|^$" <<<"$published" || fail 'private service published a port'
  mapfile -t live_ids < <(docker ps --quiet --filter "label=com.docker.compose.project=$project")
  ((${#live_ids[@]} > 0)) || fail 'no live containers'
  while IFS= read -r image; do
    [[ $image =~ @sha256:[a-f0-9]{64}$ ]] || fail 'mutable live image'
    docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$image" | grep -Eq '@sha256:[a-f0-9]{64}$' || fail 'RepoDigests proof missing'
    timeout --foreground --kill-after=30s 1200s trivy image --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 "$image" >/dev/null
    ! docker history --no-trunc "$image" | grep -Eiq '(password|authorization|api[_-]?key|AGE-SECRET-KEY-1)' || fail 'image history contains secret-like material'
  done < <(docker inspect --format '{{.Config.Image}}' "${live_ids[@]}" | LC_ALL=C sort -u)
  printf '{"status":"pass","category":"live_security","hostname":"%s"}\n' "$hostname"
else
  printf '%s\n' '{"status":"pass","category":"repository_security"}'
fi
