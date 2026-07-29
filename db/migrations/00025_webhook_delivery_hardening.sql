-- +goose Up
UPDATE alert_deliveries delivery
SET alert_id=event.alert_id
FROM alert_webhook_events event
WHERE delivery.event_id=event.id
  AND delivery.alert_id<>event.alert_id;

UPDATE alert_webhook_events
SET state='open'
WHERE transition_kind='opened'
  AND state<>'open';

UPDATE alert_webhook_events
SET severity='critical',
    state=CASE
      WHEN state IN ('open','acknowledged') THEN state
      ELSE 'open'
    END
WHERE transition_kind='upgraded'
  AND (
    severity<>'critical'
    OR state NOT IN ('open','acknowledged')
  );

UPDATE alert_webhook_events
SET state='resolved'
WHERE transition_kind='resolved'
  AND state<>'resolved';

ALTER TABLE alert_webhook_events
  ADD CONSTRAINT alert_webhook_events_identity_key
    UNIQUE(id,alert_id),
  ADD CONSTRAINT alert_webhook_events_snapshot_check CHECK (
    (transition_kind='opened' AND state='open')
    OR (
      transition_kind='upgraded'
      AND severity='critical'
      AND state IN ('open','acknowledged')
    )
    OR (transition_kind='resolved' AND state='resolved')
  );

ALTER TABLE alert_deliveries
  DROP CONSTRAINT alert_deliveries_event_id_fkey,
  ADD CONSTRAINT alert_deliveries_event_alert_fkey
    FOREIGN KEY(event_id,alert_id)
    REFERENCES alert_webhook_events(id,alert_id)
    ON DELETE CASCADE;

DROP INDEX alert_deliveries_due_claim_idx;

CREATE INDEX alert_deliveries_effective_due_claim_idx
  ON alert_deliveries(
    (
      CASE
        WHEN delivery_state='pending' THEN scheduled_at
        WHEN delivery_state='claimed' THEN claim_expires_at
      END
    ),
    event_id,
    attempt,
    id
  )
  WHERE event_id IS NOT NULL
    AND delivery_state IN ('pending','claimed');

CREATE INDEX alert_deliveries_active_event_claim_idx
  ON alert_deliveries(event_id,claim_expires_at,id)
  WHERE event_id IS NOT NULL
    AND delivery_state='claimed';

CREATE INDEX alert_deliveries_succeeded_event_idx
  ON alert_deliveries(event_id)
  WHERE event_id IS NOT NULL
    AND delivery_state='succeeded';

-- +goose Down
DROP INDEX alert_deliveries_succeeded_event_idx;
DROP INDEX alert_deliveries_active_event_claim_idx;
DROP INDEX alert_deliveries_effective_due_claim_idx;

CREATE INDEX alert_deliveries_due_claim_idx
  ON alert_deliveries(scheduled_at,event_id,attempt)
  WHERE delivery_state IN ('pending','claimed');

ALTER TABLE alert_deliveries
  DROP CONSTRAINT alert_deliveries_event_alert_fkey,
  ADD CONSTRAINT alert_deliveries_event_id_fkey
    FOREIGN KEY(event_id)
    REFERENCES alert_webhook_events(id)
    ON DELETE CASCADE;

ALTER TABLE alert_webhook_events
  DROP CONSTRAINT alert_webhook_events_snapshot_check,
  DROP CONSTRAINT alert_webhook_events_identity_key;
