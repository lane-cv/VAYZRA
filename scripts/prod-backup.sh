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

project_dir='' env_file='' release_id=''
while (($#)); do
  case $1 in
    --project-dir|--env-file|--release-id)
      (($# >= 2)) || { hl_fail 'invalid_arguments'; exit 1; }
      case $1 in
        --project-dir) project_dir=$2 ;;
        --env-file) env_file=$2 ;;
        --release-id) release_id=$2 ;;
      esac
      shift 2 ;;
    *) hl_fail 'invalid_arguments'; exit 1 ;;
  esac
done
[[ $release_id =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] || { hl_fail 'invalid_arguments'; exit 1; }
[[ $(id -u) == 0 ]] || { hl_fail 'root_required'; exit 1; }
project_dir=$(hl_canonical_path "$project_dir" directory)
env_file=$(hl_canonical_path "$env_file" file)
[[ $(stat -c '%a' -- "$env_file") == 600 ]] || { hl_fail 'environment_invalid'; exit 1; }

declare -A env=()
while IFS= read -r line || [[ -n $line ]]; do
  [[ -z $line || $line == \#* ]] && continue
  [[ $line =~ ^([A-Z][A-Z0-9_]*)=([^[:cntrl:]]*)$ ]] || { hl_fail 'environment_invalid'; exit 1; }
  env["${BASH_REMATCH[1]}"]=${BASH_REMATCH[2]}
done <"$env_file"
[[ ${env[COMPOSE_PROJECT_NAME]:-} == "$HL_PROD_PROJECT" ]] || { hl_fail 'project_name_invalid'; exit 1; }

secret_dir=${env[HAPPYLEARN_SECRET_DIR]:-}
repository_dir=${env[HAPPYLEARN_BACKUP_HOST_PATH]:-}
release_state=${env[HAPPYLEARN_RELEASE_STATE_PATH]:-}
workflow_state="$release_state/backup-workflows"
for path in "$secret_dir" "$repository_dir" "$release_state" "$workflow_state"; do [[ $path == /* && -d $path && ! -L $path ]] || { hl_fail 'backup_path_invalid'; exit 1; }; done

query() {
  local sql=$1
  hl_compose "$project_dir" "$env_file" exec -T postgres psql --username happylearn --dbname happylearn --no-psqlrc --quiet --tuples-only --no-align --set ON_ERROR_STOP=1 --command "$sql"
}

before=$(query "SELECT COALESCE((SELECT id::text FROM backup_runs WHERE trigger_kind='pre_release' ORDER BY requested_at DESC,id DESC LIMIT 1),'');" | tr -d '[:space:]') || { hl_fail 'backup_precondition_unavailable'; exit 1; }
[[ -z $before || $before =~ ^[0-9a-f-]{36}$ ]] || { hl_fail 'backup_precondition_invalid'; exit 1; }

export HAPPYLEARN_PRODUCTION_ENV_FILE="$env_file"
export HAPPYLEARN_AISTOR_LICENSE_FILE="$secret_dir/aistor-license"
export HAPPYLEARN_BACKUP_SECRET_DIRECTORY="$secret_dir"
export HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY="$repository_dir"
export HAPPYLEARN_BACKUP_STATE_DIRECTORY="$workflow_state"
export HAPPYLEARN_BACKUP_LOCK_DIRECTORY="$release_state/backup.lock"
export HAPPYLEARN_BACKUP_AGE_RECIPIENT="${env[HAPPYLEARN_BACKUP_AGE_RECIPIENT]:-}"
export HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID="${env[HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID]:-}"

timeout --foreground --kill-after=30s 7200s "$project_dir/scripts/phase5-backup.sh" --project happylearn-prod --trigger pre_release >/dev/null || { hl_fail 'pre_release_backup_failed'; exit 1; }

evidence=$(query "
SELECT r.id::text || '|' || to_char(r.finished_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') || '|' || to_char(max(a.verified_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') || '|' || string_agg(DISTINCT a.kind, ',' ORDER BY a.kind)
FROM backup_runs r JOIN backup_artifacts a ON a.backup_run_id=r.id AND a.repository='local'
WHERE r.trigger_kind='pre_release' AND r.state='succeeded' AND r.local_expires_at>clock_timestamp()
GROUP BY r.id,r.finished_at,r.requested_at
ORDER BY r.requested_at DESC,r.id DESC LIMIT 1;" | tr -d '[:space:]') || { hl_fail 'backup_evidence_unavailable'; exit 1; }
IFS='|' read -r evidence_id finished_at verified_at artifact_kinds <<<"$evidence"
[[ $evidence_id =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ && $evidence_id != "$before" && $finished_at =~ ^[0-9T:Z-]+$ && $verified_at =~ ^[0-9T:Z-]+$ && $artifact_kinds == database_dump,manifest,object_snapshot ]] || { hl_fail 'backup_evidence_invalid'; exit 1; }

body=$(jq -cn --arg releaseId "$release_id" --arg evidenceId "$evidence_id" --arg verifiedAt "$verified_at" '{releaseId:$releaseId,evidenceId:$evidenceId,verifiedAt:$verifiedAt,status:"verified"}')
hl_atomic_json_write "$release_state/recovery/pre-release.json" "$body"
printf '{"status":"pass","category":"pre_release_backup_verified","evidenceId":"%s"}\n' "$evidence_id"
