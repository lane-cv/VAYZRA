-- +goose Up
CREATE TABLE alert_webhook_events (
  id uuid PRIMARY KEY,
  alert_id uuid NOT NULL
    REFERENCES operational_alerts(id) ON DELETE CASCADE,
  transition_kind text NOT NULL,
  alert_version bigint NOT NULL,
  category text NOT NULL,
  severity text NOT NULL,
  state text NOT NULL,
  summary text NOT NULL,
  current_value double precision NOT NULL,
  threshold_value double precision NOT NULL,
  first_observed_at timestamptz NOT NULL,
  last_observed_at timestamptz NOT NULL,
  enqueued_at timestamptz NOT NULL,
  CONSTRAINT alert_webhook_events_transition_key
    UNIQUE(alert_id,transition_kind,alert_version),
  CONSTRAINT alert_webhook_events_transition_check CHECK (
    transition_kind IN ('opened','upgraded','resolved')
    AND alert_version >= 1
  ),
  CONSTRAINT alert_webhook_events_shape_check CHECK (
    char_length(category) BETWEEN 1 AND 64
    AND severity IN ('warning','critical')
    AND state IN ('open','acknowledged','resolved')
    AND char_length(summary) BETWEEN 1 AND 240
    AND current_value = current_value
    AND current_value > '-Infinity'::float8
    AND current_value < 'Infinity'::float8
    AND threshold_value = threshold_value
    AND threshold_value > '-Infinity'::float8
    AND threshold_value < 'Infinity'::float8
  ),
  CONSTRAINT alert_webhook_events_timing_check CHECK (
    last_observed_at >= first_observed_at
    AND enqueued_at >= first_observed_at
  )
);

DROP INDEX alert_deliveries_alert_attempt_key;

ALTER TABLE alert_deliveries
  DROP CONSTRAINT alert_deliveries_attempt_check,
  DROP CONSTRAINT alert_deliveries_outcome_check,
  DROP CONSTRAINT alert_deliveries_timing_check,
  ADD COLUMN event_id uuid
    REFERENCES alert_webhook_events(id) ON DELETE CASCADE,
  ADD COLUMN delivery_state text,
  ADD COLUMN scheduled_at timestamptz,
  ADD COLUMN claim_owner text,
  ADD COLUMN claim_token uuid,
  ADD COLUMN claim_expires_at timestamptz,
  ALTER COLUMN outcome DROP NOT NULL,
  ALTER COLUMN started_at DROP NOT NULL,
  ALTER COLUMN finished_at DROP NOT NULL;

UPDATE alert_deliveries SET delivery_state=outcome;

ALTER TABLE alert_deliveries
  ADD CONSTRAINT alert_deliveries_attempt_check CHECK (
    (event_id IS NULL AND attempt BETWEEN 1 AND 4)
    OR (event_id IS NOT NULL AND attempt BETWEEN 1 AND 3)
  ),
  ADD CONSTRAINT alert_deliveries_state_check CHECK (
    (event_id IS NULL AND delivery_state IS NULL)
    OR delivery_state IN ('pending','claimed','succeeded','failed','cancelled')
  ),
  ADD CONSTRAINT alert_deliveries_event_check CHECK (
    (event_id IS NULL
      AND scheduled_at IS NULL
      AND (
        delivery_state IS NULL
        OR delivery_state IN ('succeeded','failed','cancelled')
      ))
    OR (event_id IS NOT NULL AND scheduled_at IS NOT NULL)
  ),
  ADD CONSTRAINT alert_deliveries_claim_check CHECK (
    (event_id IS NULL
      AND delivery_state IS NULL
      AND claim_owner IS NULL
      AND claim_token IS NULL
      AND claim_expires_at IS NULL)
    OR (delivery_state = 'claimed'
      AND claim_owner IS NOT NULL
      AND char_length(claim_owner) BETWEEN 1 AND 128
      AND claim_token IS NOT NULL
      AND claim_expires_at IS NOT NULL)
    OR (delivery_state <> 'claimed'
      AND claim_owner IS NULL
      AND claim_token IS NULL
      AND claim_expires_at IS NULL)
  ),
  ADD CONSTRAINT alert_deliveries_outcome_check CHECK (
    (event_id IS NULL
      AND delivery_state IS NULL
      AND outcome IN ('succeeded','failed','cancelled'))
    OR (delivery_state IN ('pending','claimed')
      AND outcome IS NULL
      AND http_status_class IS NULL
      AND error_category = '')
    OR (delivery_state = 'succeeded' AND outcome = 'succeeded')
    OR (delivery_state = 'failed' AND outcome = 'failed')
    OR (delivery_state = 'cancelled' AND outcome = 'cancelled')
  ),
  ADD CONSTRAINT alert_deliveries_timing_check CHECK (
    (event_id IS NULL
      AND delivery_state IS NULL
      AND started_at IS NOT NULL
      AND finished_at IS NOT NULL
      AND finished_at >= started_at)
    OR (delivery_state = 'pending'
      AND started_at IS NULL
      AND finished_at IS NULL)
    OR (delivery_state = 'claimed'
      AND started_at IS NOT NULL
      AND finished_at IS NULL)
    OR (delivery_state IN ('succeeded','failed')
      AND started_at IS NOT NULL
      AND finished_at IS NOT NULL
      AND finished_at >= started_at)
    OR (delivery_state = 'cancelled'
      AND finished_at IS NOT NULL
      AND (started_at IS NULL OR finished_at >= started_at))
  );

CREATE UNIQUE INDEX alert_deliveries_alert_attempt_key
  ON alert_deliveries(alert_id,attempt,destination)
  WHERE event_id IS NULL;

CREATE UNIQUE INDEX alert_deliveries_event_attempt_key
  ON alert_deliveries(event_id,attempt,destination)
  WHERE event_id IS NOT NULL;

CREATE INDEX alert_deliveries_due_claim_idx
  ON alert_deliveries(scheduled_at,event_id,attempt)
  WHERE delivery_state IN ('pending','claimed');

-- +goose Down
DELETE FROM alert_deliveries WHERE event_id IS NOT NULL;

DROP INDEX alert_deliveries_due_claim_idx;
DROP INDEX alert_deliveries_event_attempt_key;
DROP INDEX alert_deliveries_alert_attempt_key;

ALTER TABLE alert_deliveries
  DROP CONSTRAINT alert_deliveries_attempt_check,
  DROP CONSTRAINT alert_deliveries_state_check,
  DROP CONSTRAINT alert_deliveries_event_check,
  DROP CONSTRAINT alert_deliveries_claim_check,
  DROP CONSTRAINT alert_deliveries_outcome_check,
  DROP CONSTRAINT alert_deliveries_timing_check,
  ALTER COLUMN outcome SET NOT NULL,
  ALTER COLUMN started_at SET NOT NULL,
  ALTER COLUMN finished_at SET NOT NULL,
  DROP COLUMN event_id,
  DROP COLUMN delivery_state,
  DROP COLUMN scheduled_at,
  DROP COLUMN claim_owner,
  DROP COLUMN claim_token,
  DROP COLUMN claim_expires_at,
  ADD CONSTRAINT alert_deliveries_attempt_check CHECK (
    attempt BETWEEN 1 AND 4
  ),
  ADD CONSTRAINT alert_deliveries_outcome_check CHECK (
    outcome IN ('succeeded','failed','cancelled')
  ),
  ADD CONSTRAINT alert_deliveries_timing_check CHECK (
    finished_at >= started_at
  );

CREATE UNIQUE INDEX alert_deliveries_alert_attempt_key
  ON alert_deliveries(alert_id,attempt,destination);

DROP TABLE alert_webhook_events;
