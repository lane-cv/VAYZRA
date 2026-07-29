-- +goose Up
CREATE INDEX audit_logs_dashboard_latest_idx
  ON audit_logs(occurred_at DESC,id DESC);

CREATE INDEX ai_runs_dashboard_daily_idx
  ON ai_runs(created_at);

CREATE INDEX ai_runs_dashboard_failed_idx
  ON ai_runs(completed_at)
  INCLUDE (created_at,updated_at,started_at)
  WHERE status='failed';

CREATE INDEX ai_runs_dashboard_unknown_idx
  ON ai_runs(status)
  WHERE status NOT IN ('queued','streaming','succeeded','failed','cancelled');

CREATE INDEX file_processing_jobs_dashboard_failed_idx
  ON file_processing_jobs(updated_at)
  WHERE state='failed';

CREATE INDEX file_processing_jobs_dashboard_unknown_idx
  ON file_processing_jobs(state)
  WHERE state NOT IN ('queued','running','completed','failed');

CREATE INDEX outbox_events_dashboard_pending_idx
  ON outbox_events(next_attempt_at,lease_until,created_at)
  INCLUDE (lease_owner,attempts)
  WHERE published_at IS NULL;

CREATE INDEX outbox_events_dashboard_terminal_failure_idx
  ON outbox_events(published_at)
  WHERE published_at IS NOT NULL
    AND last_error_category IS NOT NULL;

CREATE INDEX backup_runs_dashboard_finished_idx
  ON backup_runs(finished_at DESC,id DESC)
  INCLUDE (state,local_snapshot_id,remote_snapshot_id)
  WHERE finished_at IS NOT NULL;

CREATE INDEX backup_runs_dashboard_remote_finished_idx
  ON backup_runs(finished_at DESC,id DESC)
  INCLUDE (state,remote_snapshot_id)
  WHERE finished_at IS NOT NULL
    AND (
      state='degraded'
      OR (remote_snapshot_id IS NOT NULL AND btrim(remote_snapshot_id)<>'')
    );

CREATE INDEX restore_verifications_dashboard_finished_idx
  ON restore_verifications(finished_at DESC,id DESC)
  INCLUDE (state,rto_seconds)
  WHERE finished_at IS NOT NULL;

CREATE INDEX restore_verifications_dashboard_unknown_idx
  ON restore_verifications(finished_at)
  WHERE finished_at IS NOT NULL
    AND state NOT IN ('succeeded','failed');

-- +goose Down
DROP INDEX restore_verifications_dashboard_unknown_idx;
DROP INDEX restore_verifications_dashboard_finished_idx;
DROP INDEX backup_runs_dashboard_remote_finished_idx;
DROP INDEX backup_runs_dashboard_finished_idx;
DROP INDEX outbox_events_dashboard_terminal_failure_idx;
DROP INDEX outbox_events_dashboard_pending_idx;
DROP INDEX file_processing_jobs_dashboard_unknown_idx;
DROP INDEX file_processing_jobs_dashboard_failed_idx;
DROP INDEX ai_runs_dashboard_unknown_idx;
DROP INDEX ai_runs_dashboard_failed_idx;
DROP INDEX ai_runs_dashboard_daily_idx;
DROP INDEX audit_logs_dashboard_latest_idx;
