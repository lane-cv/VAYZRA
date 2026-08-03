#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
readonly SCRIPT_DIR
readonly COMMON="$SCRIPT_DIR/prod-common.sh"
[[ -f $COMMON && ! -L $COMMON ]] || exit 1
# shellcheck source=prod-common.sh
# shellcheck disable=SC1091
source "$COMMON"

project_dir='' env_file='' mode='' expected_host='' target_project='' backup_id='' confirmation='' destructive=false
while (($#)); do
  case $1 in
    --project-dir|--env-file|--mode|--expected-host-address|--target-project|--backup-id|--confirmation)
      (($# >= 2)) || { hl_fail invalid_arguments; exit 1; }
      case $1 in
        --project-dir) project_dir=$2 ;;
        --env-file) env_file=$2 ;;
        --mode) mode=$2 ;;
        --expected-host-address) expected_host=$2 ;;
        --target-project) target_project=$2 ;;
        --backup-id) backup_id=$2 ;;
        --confirmation) confirmation=$2 ;;
      esac
      shift 2 ;;
    --destructive) destructive=true; shift ;;
    *) hl_fail invalid_arguments; exit 1 ;;
  esac
done

[[ $(id -u) == 0 ]] || { hl_fail root_required; exit 1; }
case $mode in local|server) ;; *) hl_fail invalid_arguments; exit 1 ;; esac
[[ $mode == local && -z $expected_host || $mode == server && -n $expected_host ]] || { hl_fail invalid_arguments; exit 1; }
[[ $target_project =~ ^happylearn-phase5-restore-[a-f0-9]{12}$ && $target_project != happylearn-prod ]] || { hl_fail target_project_invalid; exit 1; }
[[ $backup_id =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] || { hl_fail backup_id_invalid; exit 1; }
[[ $destructive == true && $confirmation == "$target_project:$backup_id" ]] || { hl_fail confirmation_invalid; exit 1; }

project_dir=$(hl_canonical_path "$project_dir" directory)
env_file=$(hl_canonical_path "$env_file" file)
hl_secure_file "$env_file"

declare -A env=()
while IFS= read -r line || [[ -n $line ]]; do
  [[ -z $line || $line == \#* ]] && continue
  [[ $line =~ ^([A-Z][A-Z0-9_]*)=([^[:cntrl:]]*)$ ]] || { hl_fail environment_invalid; exit 1; }
  env["${BASH_REMATCH[1]}"]=${BASH_REMATCH[2]}
done <"$env_file"
[[ ${env[COMPOSE_PROJECT_NAME]:-} == "$HL_PROD_PROJECT" ]] || { hl_fail project_name_invalid; exit 1; }
if [[ $mode == server ]]; then
  for key in "${!env[@]}"; do
    [[ $key != HAPPYLEARN_LOCAL_* && $key != *FAILURE_INJECTION* && $key != *FAILURE_MATRIX* ]] || { hl_fail server_test_variable_rejected; exit 1; }
  done
  while IFS= read -r key; do
    [[ $key != HAPPYLEARN_LOCAL_* && $key != *FAILURE_INJECTION* && $key != *FAILURE_MATRIX* ]] || { hl_fail server_test_variable_rejected; exit 1; }
  done < <(compgen -v)
fi
local_restore_failure_injection=''
if [[ $mode == local ]]; then
  local_restore_failure_injection=${env[HAPPYLEARN_LOCAL_RESTORE_FAILURE_INJECTION]:-}
  [[ -z $local_restore_failure_injection ||
    $local_restore_failure_injection == missing_object ]] || {
    hl_fail local_failure_injection_invalid
    exit 1
  }
fi

state_dir=$(hl_canonical_path "${env[HAPPYLEARN_RELEASE_STATE_PATH]:-}" directory)
repository_dir=${env[HAPPYLEARN_BACKUP_HOST_PATH]:-}
[[ -n $repository_dir && $repository_dir == /* && $repository_dir != / && ! -L $repository_dir && -d $repository_dir ]] || { hl_fail backup_path_invalid; exit 1; }
repository_resolved=$(realpath -e -- "$repository_dir" 2>/dev/null) || { hl_fail backup_path_invalid; exit 1; }
[[ $repository_resolved == "$repository_dir" && $(stat -c '%u:%g:%a' -- "$repository_dir") == '10003:0:700' ]] || { hl_fail backup_path_invalid; exit 1; }
repository_dir=$repository_resolved
secret_dir=${env[HAPPYLEARN_SECRET_DIR]:-}
[[ $secret_dir == /* && -d $secret_dir && ! -L $secret_dir ]] || { hl_fail restore_path_invalid; exit 1; }
hl_secure_directory "$state_dir"
hl_acquire_release_lock "$state_dir"

restore_control="$state_dir/restore-control"
restore_reports="$state_dir/restore-reports"
for path in "$restore_control" "$restore_reports"; do
  [[ -d $path && ! -L $path && $(stat -c '%u:%a' -- "$path") == 0:700 ]] || { hl_fail restore_path_invalid; exit 1; }
done
teacher_credential="$restore_control/teacher-credential"
[[ -f $teacher_credential && ! -L $teacher_credential && $(stat -c '%u:%a' -- "$teacher_credential") == 0:400 ]] || { hl_fail restore_teacher_credential_invalid; exit 1; }

validate_recovery() {
  local path=$1 now verified epoch evidence
  hl_secure_file "$path" || return 1
  [[ $(jq -r '.status // empty' "$path") == verified ]] || return 1
  evidence=$(jq -r '.evidenceId // empty' "$path")
  verified=$(jq -r '.verifiedAt // empty' "$path")
  [[ $evidence =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] || return 1
  epoch=$(date -u -d "$verified" +%s 2>/dev/null) || return 1
  now=$(date -u +%s)
  (( epoch <= now && now - epoch <= 86400 )) || return 1
  printf '%s\n' "$evidence"
}
latest_id=$(validate_recovery "$state_dir/recovery/latest.json") || { hl_fail second_recovery_point_missing; exit 1; }
second_id=$(validate_recovery "$state_dir/recovery/pre-release.json") || { hl_fail second_recovery_point_missing; exit 1; }
[[ $latest_id != "$second_id" && ( $backup_id == "$latest_id" || $backup_id == "$second_id" ) ]] || { hl_fail second_recovery_point_invalid; exit 1; }

operational_mode=$(hl_compose "$project_dir" "$env_file" exec -T postgres psql --username happylearn --dbname happylearn --no-psqlrc --quiet --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT mode FROM operational_modes WHERE singleton_id=true;" | tr -d '[:space:]')
[[ $operational_mode == release ]] || { hl_fail maintenance_required; exit 1; }
# Connect to loopback while preserving both HTTP Host and TLS SNI. A Host header
# on an https://127.0.0.1 request is insufficient because Caddy selects the TLS
# site before it receives that header.
# The positional argument is intentionally expanded by the shell inside Caddy.
# shellcheck disable=SC2016
maintenance_status=$(hl_compose "$project_dir" "$env_file" exec -T caddy sh -eu -c '
  curl --insecure --silent --output /dev/null --write-out "%{http_code}" \
    --connect-timeout 3 --max-time 10 --resolve "$1:443:127.0.0.1" \
    "https://$1/api/v1/health/live"
' _ "${env[HAPPYLEARN_DOMAIN]:-}") || { hl_fail maintenance_probe_failed; exit 1; }
[[ $maintenance_status == 503 ]] || { hl_fail active_public_traffic; exit 1; }
unset maintenance_status

readonly -a volume_kinds=(postgres aistor aistor-license secrets)
for kind in "${volume_kinds[@]}"; do
  volume="$target_project-$kind"
  if docker volume inspect "$volume" >/dev/null 2>&1; then hl_fail replacement_volume_not_empty; exit 1; fi
done

secret_copy=$(mktemp -d --tmpdir="$state_dir" '.restore-secrets.XXXXXX')
chmod 0700 "$secret_copy"
cleanup_secret_copy() {
  [[ -n ${secret_copy:-} && $secret_copy == "$state_dir"/.restore-secrets.* && -d $secret_copy && ! -L $secret_copy ]] || return 0
  find "$secret_copy" -type f -exec chmod u+w {} + 2>/dev/null || true
  rm -rf -- "$secret_copy"
}
trap cleanup_secret_copy EXIT
install -m 0400 -o 0 -g 0 "$secret_dir/backup-password" "$secret_copy/local_password"
install -m 0400 -o 0 -g 0 "$secret_dir/aistor-license" "$secret_copy/aistor-license"

handoff="$restore_reports/restore-${backup_id}-handoff.json"
report="$restore_reports/restore-${backup_id}.json"
[[ ! -e $handoff && ! -L $handoff && ! -e $report && ! -L $report ]] || { hl_fail restore_evidence_exists; exit 1; }

# Phase 5 performs repository_check, revoke_restored_sessions, authorization,
# csrf, and object integrity checks before preserve mode can publish a handoff.
HAPPYLEARN_RESTORE_PRESERVE_VERIFIED_VOLUMES=true \
HAPPYLEARN_RESTORE_TARGET_PROJECT="$target_project" \
HAPPYLEARN_RESTORE_HANDOFF_FILE="$handoff" \
HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$repository_dir" \
HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$secret_copy" \
HAPPYLEARN_RESTORE_CONTROL_DIRECTORY="$restore_control" \
HAPPYLEARN_RESTORE_REPORT_DIRECTORY="$restore_reports" \
HAPPYLEARN_AISTOR_LICENSE_FILE="$secret_copy/aistor-license" \
HAPPYLEARN_RESTORE_TEACHER_CREDENTIAL_FILE="$teacher_credential" \
HAPPYLEARN_BACKUP_IMAGE="${env[HAPPYLEARN_BACKUP_IMAGE]:-}" \
HAPPYLEARN_RESTORE_APP_IMAGE="${env[HAPPYLEARN_APP_IMAGE]:-}" \
HAPPYLEARN_RESTORE_POSTGRES_IMAGE="${env[HAPPYLEARN_POSTGRES_IMAGE]:-}" \
HAPPYLEARN_RESTORE_REDIS_IMAGE="${env[HAPPYLEARN_REDIS_IMAGE]:-}" \
HAPPYLEARN_LOCAL_RESTORE_FAILURE_INJECTION="$local_restore_failure_injection" \
timeout --foreground --kill-after=30s 14400s "$SCRIPT_DIR/phase5-restore-verify.sh" --backup-id "$backup_id" >/dev/null

jq -e --arg backup "$backup_id" --arg project "$target_project" '
  .status == "verified_detached" and .backupId == $backup and
  .targetProject == $project and .sessionsRevoked == true and
  .authorizationVerified == true and .csrfVerified == true and
  .objectIntegrityVerified == true and .switchAutomatic == false
' "$handoff" >/dev/null || { hl_fail restore_handoff_invalid; exit 1; }

for kind in "${volume_kinds[@]}"; do
  volume="$target_project-$kind"
  [[ $(docker volume inspect --format '{{index .Labels "com.docker.compose.project"}}' "$volume") == "$target_project" ]] || { hl_fail restore_volume_identity_invalid; exit 1; }
  [[ -z $(docker container ls --all --quiet --filter "volume=$volume") ]] || { hl_fail restore_volume_attached; exit 1; }
done

proposal="$state_dir/switch-proposal-${backup_id}.json"
[[ ! -e $proposal && ! -L $proposal ]] || { hl_fail switch_proposal_exists; exit 1; }
handoff_hash=$(sha256sum "$handoff" | awk '{print $1}')
proposal_body=$(jq -cn --arg backupId "$backup_id" --arg targetProject "$target_project" --arg handoff "$handoff" --arg handoffSha256 "$handoff_hash" --arg createdAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '{status:"verified",backupId:$backupId,targetProject:$targetProject,handoff:$handoff,handoffSha256:$handoffSha256,createdAt:$createdAt,switchAutomatic:false,category:"switch_not_automatic"}')
hl_atomic_json_write "$proposal" "$proposal_body"
trap - EXIT
cleanup_secret_copy
printf '{"status":"pass","category":"restore_verified_switch_proposal","backupId":"%s","targetProject":"%s"}\n' "$backup_id" "$target_project"
