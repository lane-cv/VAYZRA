-- +goose Up
ALTER TABLE qa_messages ADD COLUMN message_kind text;
ALTER TABLE qa_messages DISABLE TRIGGER qa_messages_immutable;

WITH ranked AS (
  SELECT id, sender_role,
         row_number() OVER (PARTITION BY thread_id ORDER BY created_at,id) AS position
  FROM qa_messages
)
UPDATE qa_messages m
SET message_kind = CASE
  WHEN ranked.position=1 THEN 'initial'
  WHEN ranked.sender_role='student' THEN 'student_follow_up'
  ELSE 'admin_reply'
END
FROM ranked
WHERE ranked.id=m.id;

ALTER TABLE qa_messages ENABLE TRIGGER qa_messages_immutable;

ALTER TABLE qa_messages ALTER COLUMN message_kind SET NOT NULL;
ALTER TABLE qa_messages ADD CONSTRAINT qa_messages_message_kind_check
  CHECK (message_kind IN ('initial','student_follow_up','admin_reply'));
ALTER TABLE qa_messages ADD CONSTRAINT qa_messages_message_kind_role_check
  CHECK (
    (message_kind IN ('initial','student_follow_up') AND sender_role='student')
    OR (message_kind='admin_reply' AND sender_role='admin')
  );
CREATE UNIQUE INDEX qa_messages_one_initial_per_thread_idx
  ON qa_messages(thread_id) WHERE message_kind='initial';

-- +goose Down
DROP INDEX qa_messages_one_initial_per_thread_idx;
ALTER TABLE qa_messages DROP CONSTRAINT qa_messages_message_kind_role_check;
ALTER TABLE qa_messages DROP CONSTRAINT qa_messages_message_kind_check;
ALTER TABLE qa_messages DROP COLUMN message_kind;
