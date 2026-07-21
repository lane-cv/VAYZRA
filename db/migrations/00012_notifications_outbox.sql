-- +goose Up
CREATE TABLE notifications (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  recipient_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  kind text NOT NULL CHECK (kind IN ('qa_created','qa_replied','qa_followed_up','qa_status_changed','lesson_published')),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 160),
  summary text NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 240),
  target_type text NOT NULL CHECK (target_type IN ('qa_thread','lesson')),
  target_id uuid NOT NULL,
  target_path text NOT NULL CHECK (target_path LIKE '/%'),
  dedupe_key text NOT NULL CHECK (char_length(dedupe_key) BETWEEN 16 AND 200),
  read_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(recipient_user_id,dedupe_key)
);

CREATE INDEX notifications_recipient_time_idx ON notifications(recipient_user_id,created_at DESC,id DESC);
CREATE INDEX notifications_unread_idx ON notifications(recipient_user_id,created_at DESC,id DESC) WHERE read_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION enforce_notification_mutation() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'notifications are immutable' USING ERRCODE = '23514';
  END IF;
  IF OLD.id IS DISTINCT FROM NEW.id
     OR OLD.recipient_user_id IS DISTINCT FROM NEW.recipient_user_id
     OR OLD.kind IS DISTINCT FROM NEW.kind
     OR OLD.title IS DISTINCT FROM NEW.title
     OR OLD.summary IS DISTINCT FROM NEW.summary
     OR OLD.target_type IS DISTINCT FROM NEW.target_type
     OR OLD.target_id IS DISTINCT FROM NEW.target_id
     OR OLD.target_path IS DISTINCT FROM NEW.target_path
     OR OLD.dedupe_key IS DISTINCT FROM NEW.dedupe_key
     OR OLD.created_at IS DISTINCT FROM NEW.created_at
     OR OLD.read_at IS NOT NULL
     OR NEW.read_at IS NULL THEN
    RAISE EXCEPTION 'notification content and read state are immutable' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER notifications_immutable BEFORE UPDATE OR DELETE ON notifications
FOR EACH ROW EXECUTE FUNCTION enforce_notification_mutation();

ALTER TABLE outbox_events
  ADD COLUMN dedupe_key text,
  ADD COLUMN lease_owner text,
  ADD COLUMN lease_until timestamptz,
  ADD COLUMN attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN last_error_category text,
  ADD CONSTRAINT outbox_events_lease_pair CHECK ((lease_owner IS NULL) = (lease_until IS NULL)),
  ADD CONSTRAINT outbox_events_attempts_bounded CHECK (attempts BETWEEN 0 AND 1000000);
CREATE UNIQUE INDEX outbox_events_dedupe_key ON outbox_events(dedupe_key) WHERE dedupe_key IS NOT NULL;

-- +goose Down
DROP INDEX outbox_events_dedupe_key;
ALTER TABLE outbox_events
  DROP CONSTRAINT outbox_events_attempts_bounded,
  DROP CONSTRAINT outbox_events_lease_pair,
  DROP COLUMN last_error_category,
  DROP COLUMN next_attempt_at,
  DROP COLUMN attempts,
  DROP COLUMN lease_until,
  DROP COLUMN lease_owner,
  DROP COLUMN dedupe_key;
DROP TRIGGER notifications_immutable ON notifications;
DROP FUNCTION enforce_notification_mutation();
DROP TABLE notifications;
