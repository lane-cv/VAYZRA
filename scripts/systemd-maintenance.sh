#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
readonly script_dir
readonly project_name=happylearn-prod
task='' project_dir='' env_file=''

fail() { printf '{"status":"fail","category":"%s"}\n' "$1" >&2; exit 1; }
while (($#)); do
  (($# >= 2)) || fail invalid_arguments
  case $1 in
    --task) task=$2 ;;
    --project-dir) project_dir=$2 ;;
    --env-file) env_file=$2 ;;
    *) fail invalid_arguments ;;
  esac
  shift 2
done
case $task in scheduled|retry-degraded|retention|restore-verify) ;; *) fail invalid_arguments ;; esac

canonical() {
  local path=$1 kind=$2 resolved
  [[ $path == /* && $path != / && ! -L $path ]] || return 1
  resolved=$(realpath -e -- "$path") || return 1
  [[ $resolved == "$path" ]] || return 1
  case $kind in directory) [[ -d $path ]] ;; file) [[ -f $path ]] ;; *) return 1 ;; esac
  printf '%s\n' "$resolved"
}
project_dir=$(canonical "$project_dir" directory) || fail unsafe_path
env_file=$(canonical "$env_file" file) || fail unsafe_path
[[ $project_dir == "$(cd -- "$script_dir/.." && pwd -P)" ]] || fail project_path_mismatch
mode=$(stat -c '%a' -- "$env_file") || fail environment_invalid
(( (8#$mode & 8#077) == 0 )) || fail environment_invalid

declare -A env=()
while IFS= read -r line || [[ -n $line ]]; do
  [[ -z $line || $line == \#* ]] && continue
  [[ $line =~ ^([A-Z][A-Z0-9_]*)=([^[:cntrl:]]*)$ ]] || fail environment_invalid
  env["${BASH_REMATCH[1]}"]=${BASH_REMATCH[2]}
done <"$env_file"
[[ ${env[COMPOSE_PROJECT_NAME]:-} == "$project_name" ]] || fail project_name_invalid

secret_dir=$(canonical "${env[HAPPYLEARN_SECRET_DIR]:-}" directory) || fail secret_directory_invalid
repository_dir=$(canonical "${env[HAPPYLEARN_BACKUP_HOST_PATH]:-}" directory) || fail backup_directory_invalid
state_dir=$(canonical "${env[HAPPYLEARN_RELEASE_STATE_PATH]:-}" directory) || fail state_directory_invalid
workflow_state=$state_dir/backup-workflows
[[ -d $workflow_state && ! -L $workflow_state ]] || fail state_directory_invalid

compose() {
  timeout --foreground --kill-after=10s 120s docker compose \
    --project-name "$project_name" --project-directory "$project_dir" \
    --env-file "$env_file" -f "$project_dir/deploy/compose.prod.yml" "$@"
}
query() {
  local sql=$1
  compose exec -T postgres psql --username happylearn --dbname happylearn \
    --no-psqlrc --quiet --tuples-only --no-align --set ON_ERROR_STOP=1 \
    --command "$sql" | tr -d '[:space:]'
}

export HAPPYLEARN_PRODUCTION_ENV_FILE=$env_file
export HAPPYLEARN_AISTOR_LICENSE_FILE=$secret_dir/aistor-license
export HAPPYLEARN_BACKUP_SECRET_DIRECTORY=$secret_dir
export HAPPYLEARN_BACKUP_REPOSITORY_DIRECTORY=$repository_dir
export HAPPYLEARN_BACKUP_STATE_DIRECTORY=$workflow_state
export HAPPYLEARN_BACKUP_LOCK_DIRECTORY=$state_dir/backup.lock
export HAPPYLEARN_BACKUP_AGE_RECIPIENT=${env[HAPPYLEARN_BACKUP_AGE_RECIPIENT]:-}
export HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID=${env[HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID]:-}

run_scheduled() {
  local due
  due=$(query "SELECT CASE WHEN (clock_timestamp() AT TIME ZONE 'Asia/Shanghai')::time >= time '03:00' THEN 'due' ELSE 'early' END;") || fail schedule_unavailable
  [[ $due == due ]] || { printf '%s\n' '{"status":"pass","category":"backup_not_due"}'; return 0; }
  timeout --foreground --kill-after=30s 7200s "$project_dir/scripts/phase5-backup.sh" \
    --project happylearn-prod --trigger scheduled >/dev/null || fail scheduled_backup_failed
  printf '%s\n' '{"status":"pass","category":"scheduled_backup_reconciled"}'
}

case $task in
  scheduled|retention)
    # The scheduled workflow is local-date idempotent in PostgreSQL and runs
    # local/remote retention after verification. The daily timer reconciles it.
    run_scheduled
    ;;
  retry-degraded)
    pending=$(query "SELECT CASE WHEN EXISTS (SELECT 1 FROM backup_runs WHERE state='degraded' AND error_category='remote_unavailable' AND finished_at IS NOT NULL AND finished_at > COALESCE((SELECT max(finished_at) FROM backup_runs WHERE state='succeeded' AND remote_snapshot_id IS NOT NULL),'-infinity'::timestamptz)) THEN 'retry' ELSE 'clear' END;") || fail retry_state_unavailable
    [[ $pending == retry ]] || { printf '%s\n' '{"status":"pass","category":"no_degraded_backup"}'; exit 0; }
    # A fresh manual run re-verifies local data before replacing degraded
    # remote evidence; it never promotes an unverified partial upload.
    timeout --foreground --kill-after=30s 7200s "$project_dir/scripts/phase5-backup.sh" \
      --project happylearn-prod --trigger manual >/dev/null || fail degraded_retry_failed
    printf '%s\n' '{"status":"pass","category":"degraded_backup_retried"}'
    ;;
  restore-verify)
    backup_id=$(query "SELECT COALESCE((SELECT r.id::text FROM backup_runs r WHERE r.state IN ('succeeded','degraded') AND r.local_snapshot_id IS NOT NULL AND r.local_expires_at > clock_timestamp() ORDER BY r.finished_at DESC,r.id DESC LIMIT 1),'');") || fail restore_candidate_unavailable
    [[ $backup_id =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] || fail restore_candidate_unavailable
    export HAPPYLEARN_RESTORE_CONTROL_DIRECTORY=$state_dir/restore-control
    export HAPPYLEARN_RESTORE_REPORT_DIRECTORY=$state_dir/restore-reports
    export HAPPYLEARN_BACKUP_IMAGE=${env[HAPPYLEARN_BACKUP_IMAGE]:-}
    export HAPPYLEARN_RESTORE_APP_IMAGE=${env[HAPPYLEARN_APP_IMAGE]:-}
    export HAPPYLEARN_RESTORE_POSTGRES_IMAGE=${env[HAPPYLEARN_POSTGRES_IMAGE]:-}
    export HAPPYLEARN_RESTORE_REDIS_IMAGE=${env[HAPPYLEARN_REDIS_IMAGE]:-}
    timeout --foreground --kill-after=30s 14400s "$project_dir/scripts/phase5-restore-verify.sh" \
      --backup-id "$backup_id" >/dev/null || fail restore_verification_failed
    printf '{"status":"pass","category":"restore_verified","backupId":"%s"}\n' "$backup_id"
    ;;
esac
