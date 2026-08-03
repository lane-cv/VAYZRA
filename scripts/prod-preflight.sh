#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
readonly SCRIPT_DIR
readonly COMMON="$SCRIPT_DIR/prod-common.sh"
[[ $COMMON == /* && -f $COMMON && ! -L $COMMON ]] || { printf '{"status":"fail","category":"common_unavailable"}\n' >&2; exit 1; }
# shellcheck source=prod-common.sh
# shellcheck disable=SC1091
source "$COMMON"

project_dir='' env_file='' manifest='' mode='' expected_host=''
while (($#)); do
  case $1 in
    --project-dir|--env-file|--manifest|--mode|--expected-host-address)
      (($# >= 2)) || { hl_fail 'invalid_arguments'; exit 1; }
      case $1 in
        --project-dir) project_dir=$2 ;;
        --env-file) env_file=$2 ;;
        --manifest) manifest=$2 ;;
        --mode) mode=$2 ;;
        --expected-host-address) expected_host=$2 ;;
      esac
      shift 2 ;;
    *) hl_fail 'invalid_arguments'; exit 1 ;;
  esac
done
[[ $mode == local || $mode == server ]] || { hl_fail 'invalid_arguments'; exit 1; }
[[ $mode == local && -z $expected_host || $mode == server && -n $expected_host ]] || { hl_fail 'invalid_arguments'; exit 1; }

for command in realpath stat jq sha256sum timeout docker flock nproc df; do hl_require_command "$command"; done
project_dir=$(hl_canonical_path "$project_dir" directory)
env_file=$(hl_canonical_path "$env_file" file)
manifest=$(hl_canonical_path "$manifest" file)
hl_secure_file "$env_file"
hl_secure_file "$manifest"
manifest_keys_json='["version","commit","builtAt","images","minSchemaVersion","maxSchemaVersion","composeSha256","caddySha256","backupEvidenceId","createdBy","createdAt"]'
jq -e --argjson keys "$manifest_keys_json" '
  (keys | sort) == ($keys | sort) and
  (.version | type == "string" and test("^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+)(\\.[0-9A-Za-z-]+)*)?(\\+[0-9A-Za-z-]+(\\.[0-9A-Za-z-]+)*)?$")) and
  (.commit | type == "string" and test("^[0-9a-f]{40}([0-9a-f]{24})?$")) and
  (.images | type == "object" and length == 8) and
  (.minSchemaVersion | type == "number" and . >= 0 and . == floor) and
  (.maxSchemaVersion | type == "number" and . == floor and . >= $ARGS.named.minSchemaVersion) and
  (.composeSha256 | test("^[0-9a-f]{64}$")) and
  (.caddySha256 | test("^[0-9a-f]{64}$")) and
  (.backupEvidenceId | test("^[A-Za-z0-9][A-Za-z0-9._:@+-]{0,127}$")) and
  (.createdBy | type == "string" and test("^[A-Za-z0-9][A-Za-z0-9._:@+-]{0,127}$")) and
  (.builtAt | type == "string") and (.createdAt | type == "string") and
  ([.. | strings | select(test("[\\r\\n]") or test("(?i)(password|passwd|secret|token|api[_-]?key|authorization)[[:space:]]*[:=]") or startswith("/"))] | length == 0)
' --argjson minSchemaVersion "$(jq '.minSchemaVersion' "$manifest")" "$manifest" >/dev/null || { hl_fail 'manifest_invalid'; exit 1; }
date -u -d "$(jq -r '.builtAt' "$manifest")" +%s >/dev/null 2>&1 || { hl_fail 'manifest_time_invalid'; exit 1; }
date -u -d "$(jq -r '.createdAt' "$manifest")" +%s >/dev/null 2>&1 || { hl_fail 'manifest_time_invalid'; exit 1; }
[[ -f $project_dir/deploy/compose.prod.yml && ! -L $project_dir/deploy/compose.prod.yml ]] || { hl_fail 'compose_unavailable'; exit 1; }

declare -A env=()
while IFS= read -r line || [[ -n $line ]]; do
  [[ -z $line || $line == \#* ]] && continue
  [[ $line =~ ^([A-Z][A-Z0-9_]*)=([^[:cntrl:]]*)$ ]] || { hl_fail 'environment_invalid'; exit 1; }
  env["${BASH_REMATCH[1]}"]=${BASH_REMATCH[2]}
done <"$env_file"
[[ ${env[COMPOSE_PROJECT_NAME]:-} == "$HL_PROD_PROJECT" ]] || { hl_fail 'project_name_invalid'; exit 1; }
if [[ $mode == server ]]; then
  for key in "${!env[@]}"; do
    [[ $key != HAPPYLEARN_LOCAL_* && $key != *FAILURE_INJECTION* && $key != *FAILURE_MATRIX* ]] || { hl_fail server_test_variable_rejected; exit 1; }
  done
  while IFS= read -r key; do
    [[ $key != HAPPYLEARN_LOCAL_* && $key != *FAILURE_INJECTION* && $key != *FAILURE_MATRIX* ]] || { hl_fail server_test_variable_rejected; exit 1; }
  done < <(compgen -v)
fi

for key in HAPPYLEARN_SECRET_DIR HAPPYLEARN_BACKUP_HOST_PATH HAPPYLEARN_RELEASE_STATE_PATH HAPPYLEARN_CADDYFILE HAPPYLEARN_MAINTENANCE_CADDYFILE HAPPYLEARN_MAINTENANCE_FILE; do
  [[ ${env[$key]:-} == /* ]] || { hl_fail 'environment_path_invalid'; exit 1; }
done
secret_dir=$(realpath -e -- "${env[HAPPYLEARN_SECRET_DIR]}" 2>/dev/null) || { hl_fail 'secret_directory_invalid'; exit 1; }
backup_dir=$(realpath -e -- "${env[HAPPYLEARN_BACKUP_HOST_PATH]}" 2>/dev/null) || { hl_fail 'backup_path_invalid'; exit 1; }
state_dir=$(hl_canonical_path "${env[HAPPYLEARN_RELEASE_STATE_PATH]}" directory)
hl_secret_directory "$secret_dir"
hl_secure_directory "$state_dir"
[[ ! -L $backup_dir && -d $backup_dir && $(stat -c '%u:%g:%a' -- "$backup_dir") == '10003:0:700' ]] || { hl_fail 'backup_path_invalid'; exit 1; }
backup_workflows="$state_dir/backup-workflows"
[[ -d $backup_workflows && ! -L $backup_workflows && $(stat -c '%u:%g:%a' -- "$backup_workflows") == '10003:0:700' ]] || { hl_fail 'backup_state_invalid'; exit 1; }
release_input="$state_dir/release-input"
[[ -d $release_input && ! -L $release_input && $(stat -c '%u:%g:%a' -- "$release_input") == '10001:10001:700' ]] || { hl_fail 'release_input_invalid'; exit 1; }
caddy_file=$(hl_canonical_path "${env[HAPPYLEARN_CADDYFILE]}" file)
hl_canonical_path "${env[HAPPYLEARN_MAINTENANCE_CADDYFILE]}" file >/dev/null
hl_canonical_path "${env[HAPPYLEARN_MAINTENANCE_FILE]}" file >/dev/null

secret_names=(postgres-password redis-password minio-access-key minio-secret-key aistor-license app-database-url app-redis-url app-login-throttle app-minio-access-key app-minio-secret-key app-ai-master-key worker-database-url worker-redis-url worker-login-throttle worker-minio-access-key worker-minio-secret-key worker-ai-master-key metrics-bearer host-metrics-hmac backup-password backup-age-identity backup-database-password backup-local-repository)
secret_uids=(999 999 1000 1000 1000 10001 10001 10001 10001 10001 10001 10002 10002 10002 10002 10002 10002 10001 10001 10003 10003 10003 10003)
secret_limits=(4096 4096 4096 4096 65536 8192 8192 4096 4096 4096 4096 8192 8192 4096 4096 4096 4096 8192 8192 4096 65536 4096 8192)
for index in "${!secret_names[@]}"; do hl_service_secret "$secret_dir/${secret_names[$index]}" "${secret_uids[$index]}" "${secret_limits[$index]}"; done

(( $(nproc) >= 2 )) || { hl_fail 'cpu_insufficient'; exit 1; }
memory_kib=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
(( memory_kib >= 4194304 )) || { hl_fail 'memory_insufficient'; exit 1; }
for path in "$backup_dir" "$state_dir"; do
  available_kib=$(df -Pk -- "$path" | awk 'NR==2 {print $4}')
  (( available_kib >= 5242880 )) || { hl_fail 'disk_insufficient'; exit 1; }
done
[[ ${env[HAPPYLEARN_TIMEZONE]:-} == Asia/Shanghai ]] || { hl_fail 'timezone_invalid'; exit 1; }

image_keys=(APP WORKER MIGRATE BACKUP CADDY POSTGRES REDIS MINIO)
manifest_keys=(app worker migrate backup caddy postgres redis minio)
for index in "${!image_keys[@]}"; do
  value=${env[HAPPYLEARN_${image_keys[$index]}_IMAGE]:-}
  [[ $value =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || { hl_fail 'image_not_immutable'; exit 1; }
  [[ $(jq -r --arg key "${manifest_keys[$index]}" '.images[$key] // empty' "$manifest") == "$value" ]] || { hl_fail 'manifest_image_mismatch'; exit 1; }
  timeout --foreground --kill-after=5s 60s docker manifest inspect "$value" >/dev/null 2>&1 || docker image inspect "$value" >/dev/null 2>&1 || { hl_fail 'image_unavailable'; exit 1; }
done

resolved=$(mktemp)
trap 'rm -f -- "${resolved:-}"' EXIT
chmod 0600 "$resolved"
hl_compose "$project_dir" "$env_file" config >"$resolved" 2>/dev/null || { hl_fail 'compose_invalid'; exit 1; }
compose_hash=$(sha256sum "$resolved" | awk '{print $1}')
caddy_hash=$(sha256sum "$caddy_file" | awk '{print $1}')
[[ $(jq -r '.composeSha256 // empty' "$manifest") == "$compose_hash" && $(jq -r '.caddySha256 // empty' "$manifest") == "$caddy_hash" ]] || { hl_fail 'configuration_hash_mismatch'; exit 1; }
timeout --foreground --kill-after=5s 60s docker run --rm --read-only --network none \
  --tmpfs /data:rw,noexec,nosuid,nodev,size=8m --tmpfs /config:rw,noexec,nosuid,nodev,size=8m \
  -e "HAPPYLEARN_DOMAIN=${env[HAPPYLEARN_DOMAIN]:-}" -v "$caddy_file:/etc/caddy/Caddyfile:ro" \
  "${env[HAPPYLEARN_CADDY_IMAGE]}" caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 || { hl_fail 'caddy_invalid'; exit 1; }

recovery="$state_dir/recovery/latest.json"
hl_secure_file "$recovery"
[[ $(jq -r '.status // empty' "$recovery") == verified ]] || { hl_fail 'recovery_unverified'; exit 1; }
evidence_id=$(jq -r '.evidenceId // empty' "$recovery")
verified_at=$(jq -r '.verifiedAt // empty' "$recovery")
[[ $evidence_id =~ ^[A-Za-z0-9][A-Za-z0-9._:@+-]{0,127}$ && $evidence_id == "$(jq -r '.backupEvidenceId' "$manifest")" ]] || { hl_fail 'recovery_evidence_mismatch'; exit 1; }
verified_epoch=$(date -u -d "$verified_at" +%s 2>/dev/null) || { hl_fail 'recovery_time_invalid'; exit 1; }
now_epoch=$(date -u +%s)
(( verified_epoch <= now_epoch && now_epoch - verified_epoch <= 86400 )) || { hl_fail 'recovery_too_old'; exit 1; }

if [[ -f $state_dir/active-manifest.json ]]; then hl_secure_file "$state_dir/active-manifest.json"; fi
if [[ -f $state_dir/previous-manifest.json ]]; then hl_secure_file "$state_dir/previous-manifest.json"; fi
if [[ -f $state_dir/release-state.json ]]; then
  hl_secure_file "$state_dir/release-state.json"
  previous_hash=$(jq -r '.previousManifestSha256 // empty' "$state_dir/release-state.json")
  if [[ -n $previous_hash ]]; then
    [[ -f $state_dir/previous-manifest.json && $(sha256sum "$state_dir/previous-manifest.json" | awk '{print $1}') == "$previous_hash" ]] || { hl_fail 'previous_manifest_unavailable'; exit 1; }
  fi
fi

if [[ $mode == server ]]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  [[ ${ID:-} == ubuntu && ${VERSION_ID:-} == 24.04 ]] || { hl_fail 'host_os_invalid'; exit 1; }
  [[ $(timedatectl show -p Timezone --value 2>/dev/null) == Asia/Shanghai ]] || { hl_fail 'host_timezone_invalid'; exit 1; }
  domain=${env[HAPPYLEARN_DOMAIN]:-}
  getent ahosts "$domain" | awk '{print $1}' | grep -Fx -- "$expected_host" >/dev/null || { hl_fail 'dns_mismatch'; exit 1; }
  if ss -Hln '( sport = :80 or sport = :443 )' | grep -q .; then
    running_caddy=$(hl_compose "$project_dir" "$env_file" ps --status running --quiet caddy 2>/dev/null || true)
    [[ -n $running_caddy ]] || { hl_fail 'public_port_occupied'; exit 1; }
  fi
fi

rm -f -- "$resolved"
trap - EXIT
hl_pass 'preflight_passed'
