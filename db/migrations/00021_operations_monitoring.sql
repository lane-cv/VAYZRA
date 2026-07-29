-- +goose Up
CREATE TABLE operational_samples (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  source text NOT NULL,
  metric_name text NOT NULL,
  scope text NOT NULL,
  value double precision NOT NULL,
  unit text NOT NULL,
  observed_at timestamptz NOT NULL,
  window_started_at timestamptz,
  CONSTRAINT operational_samples_source_check CHECK (
    source IN ('app','postgres','redis','object_store','worker','host')
  ),
  CONSTRAINT operational_samples_value_check CHECK (
    value = value
    AND value > '-Infinity'::float8
    AND value < 'Infinity'::float8
    AND char_length(metric_name) BETWEEN 1 AND 64
    AND char_length(scope) BETWEEN 1 AND 32
    AND char_length(unit) BETWEEN 1 AND 24
  ),
  CONSTRAINT operational_samples_window_check CHECK (
    window_started_at IS NULL OR window_started_at <= observed_at
  )
);

CREATE INDEX operational_samples_metric_time_idx
  ON operational_samples(metric_name, scope, observed_at DESC, id DESC);

CREATE TABLE operational_alerts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dedupe_key text NOT NULL,
  category text NOT NULL,
  severity text NOT NULL,
  state text NOT NULL DEFAULT 'open',
  first_observed_at timestamptz NOT NULL,
  last_observed_at timestamptz NOT NULL,
  acknowledged_by uuid REFERENCES users(id) ON DELETE SET NULL,
  acknowledged_at timestamptz,
  resolved_at timestamptz,
  current_value double precision NOT NULL,
  threshold_value double precision NOT NULL,
  summary text NOT NULL,
  trace_id text NOT NULL DEFAULT '',
  consecutive_failures integer NOT NULL DEFAULT 1,
  consecutive_successes integer NOT NULL DEFAULT 0,
  version bigint NOT NULL DEFAULT 1,
  CONSTRAINT operational_alerts_text_check CHECK (
    char_length(dedupe_key) BETWEEN 1 AND 128
    AND char_length(category) BETWEEN 1 AND 64
    AND char_length(summary) BETWEEN 1 AND 240
    AND char_length(trace_id) <= 128
  ),
  CONSTRAINT operational_alerts_severity_check CHECK (
    severity IN ('warning','critical')
  ),
  CONSTRAINT operational_alerts_state_check CHECK (
    state IN ('open','acknowledged','resolved')
  ),
  CONSTRAINT operational_alerts_value_check CHECK (
    current_value = current_value
    AND current_value > '-Infinity'::float8
    AND current_value < 'Infinity'::float8
    AND threshold_value = threshold_value
    AND threshold_value > '-Infinity'::float8
    AND threshold_value < 'Infinity'::float8
  ),
  CONSTRAINT operational_alerts_acknowledgement_check CHECK (
    (acknowledged_by IS NULL) = (acknowledged_at IS NULL)
    AND (state <> 'open' OR acknowledged_at IS NULL)
    AND (state <> 'acknowledged' OR acknowledged_at IS NOT NULL)
  ),
  CONSTRAINT operational_alerts_resolution_check CHECK (
    (state = 'resolved') = (resolved_at IS NOT NULL)
  ),
  CONSTRAINT operational_alerts_timing_check CHECK (
    last_observed_at >= first_observed_at
    AND (acknowledged_at IS NULL OR acknowledged_at >= first_observed_at)
    AND (resolved_at IS NULL OR resolved_at >= first_observed_at)
  ),
  CONSTRAINT operational_alerts_counters_check CHECK (
    consecutive_failures >= 0 AND consecutive_successes >= 0
  ),
  CONSTRAINT operational_alerts_version_check CHECK (version >= 1)
);

CREATE UNIQUE INDEX operational_alerts_open_dedupe_key
  ON operational_alerts(dedupe_key) WHERE state <> 'resolved';

CREATE INDEX operational_alerts_state_time_idx
  ON operational_alerts(state, last_observed_at DESC, id DESC);

CREATE TABLE alert_deliveries (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  alert_id uuid NOT NULL
    REFERENCES operational_alerts(id) ON DELETE CASCADE,
  attempt integer NOT NULL,
  destination text NOT NULL,
  outcome text NOT NULL,
  http_status_class integer,
  error_category text NOT NULL DEFAULT '',
  started_at timestamptz NOT NULL,
  finished_at timestamptz NOT NULL,
  CONSTRAINT alert_deliveries_attempt_check CHECK (
    attempt BETWEEN 1 AND 4
  ),
  CONSTRAINT alert_deliveries_destination_check CHECK (
    destination = 'webhook'
  ),
  CONSTRAINT alert_deliveries_outcome_check CHECK (
    outcome IN ('succeeded','failed','cancelled')
  ),
  CONSTRAINT alert_deliveries_status_check CHECK (
    http_status_class IS NULL OR http_status_class BETWEEN 1 AND 5
  ),
  CONSTRAINT alert_deliveries_error_category_check CHECK (
    char_length(error_category) <= 64
  ),
  CONSTRAINT alert_deliveries_timing_check CHECK (
    finished_at >= started_at
  )
);

CREATE UNIQUE INDEX alert_deliveries_alert_attempt_key
  ON alert_deliveries(alert_id, attempt, destination);

-- +goose Down
DROP TABLE alert_deliveries;
DROP TABLE operational_alerts;
DROP TABLE operational_samples;
