-- +goose Up
CREATE INDEX ai_runs_alert_terminal_idx
  ON ai_runs(completed_at)
  WHERE status IN ('succeeded','failed','cancelled');

CREATE INDEX login_events_alert_failed_idx
  ON login_events(occurred_at)
  WHERE success=false;

-- +goose Down
DROP INDEX login_events_alert_failed_idx;
DROP INDEX ai_runs_alert_terminal_idx;
