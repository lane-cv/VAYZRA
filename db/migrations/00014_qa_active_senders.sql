-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_qa_message_sender() RETURNS trigger AS $$
BEGIN
  IF NEW.sender_role='student' AND NOT EXISTS (
    SELECT 1 FROM qa_threads q
    JOIN users u ON u.id=NEW.sender_user_id
      AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
    WHERE q.id=NEW.thread_id AND q.student_id=NEW.sender_user_id
  ) THEN
    RAISE EXCEPTION 'qa student sender must be the active thread owner' USING ERRCODE = '23514';
  END IF;
  IF NEW.sender_role='admin' AND NOT EXISTS (
    SELECT 1 FROM users u
    WHERE u.id=NEW.sender_user_id AND u.role='admin'
      AND u.status='active' AND u.deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'qa admin sender must currently be active' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_qa_message_sender() RETURNS trigger AS $$
BEGIN
  IF NEW.sender_role='student' AND NOT EXISTS (
    SELECT 1 FROM qa_threads q JOIN users u ON u.id=NEW.sender_user_id AND u.role='student'
    WHERE q.id=NEW.thread_id AND q.student_id=NEW.sender_user_id
  ) THEN
    RAISE EXCEPTION 'qa student sender must own thread' USING ERRCODE = '23514';
  END IF;
  IF NEW.sender_role='admin' AND NOT EXISTS (
    SELECT 1 FROM users u WHERE u.id=NEW.sender_user_id AND u.role='admin'
  ) THEN
    RAISE EXCEPTION 'qa admin sender must currently have admin role' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
