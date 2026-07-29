-- +goose Up
CREATE INDEX audit_logs_dashboard_latest_idx
  ON audit_logs(occurred_at DESC,id DESC);

CREATE INDEX ai_runs_dashboard_daily_idx
  ON ai_runs(created_at)
  INCLUDE (
    status,
    usage_source,
    first_byte_ms,
    total_ms,
    cost_micro_usd,
    updated_at,
    started_at,
    completed_at
  );

CREATE INDEX ai_runs_dashboard_queue_idx
  ON ai_runs(status,lease_expires_at,completed_at)
  INCLUDE (created_at,updated_at)
  WHERE status IN ('queued','streaming','failed');

CREATE INDEX file_processing_jobs_dashboard_queue_idx
  ON file_processing_jobs(state,lease_until,available_at,updated_at)
  INCLUDE (attempts,created_at)
  WHERE state IN ('queued','running','failed');

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

CREATE INDEX restore_verifications_dashboard_finished_idx
  ON restore_verifications(finished_at DESC,id DESC)
  INCLUDE (state,rto_seconds)
  WHERE finished_at IS NOT NULL;

-- +goose Down
DROP INDEX restore_verifications_dashboard_finished_idx;
DROP INDEX backup_runs_dashboard_finished_idx;
DROP INDEX outbox_events_dashboard_terminal_failure_idx;
DROP INDEX outbox_events_dashboard_pending_idx;
DROP INDEX file_processing_jobs_dashboard_queue_idx;
DROP INDEX ai_runs_dashboard_queue_idx;
DROP INDEX ai_runs_dashboard_daily_idx;
DROP INDEX audit_logs_dashboard_latest_idx;
