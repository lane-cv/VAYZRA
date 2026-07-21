-- +goose Up
CREATE TABLE qa_threads (
  id uuid PRIMARY KEY,
  student_id uuid NOT NULL REFERENCES users(id),
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
  status text NOT NULL CHECK (status IN ('pending','in_progress','waiting_student','completed')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  last_message_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CHECK ((status='completed') = (completed_at IS NOT NULL))
);

CREATE TABLE qa_messages (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES qa_threads(id),
  sender_user_id uuid NOT NULL REFERENCES users(id),
  sender_role text NOT NULL CHECK (sender_role IN ('admin','student')),
  body_text text NOT NULL CHECK (char_length(btrim(body_text)) BETWEEN 1 AND 20000),
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 16 AND 128),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(sender_user_id,idempotency_key)
);

CREATE TABLE qa_message_files (
  message_id uuid NOT NULL REFERENCES qa_messages(id),
  file_version_id uuid NOT NULL REFERENCES file_versions(id),
  sort_position smallint NOT NULL CHECK (sort_position BETWEEN 0 AND 19),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 255),
  PRIMARY KEY(message_id,file_version_id),
  UNIQUE(message_id,sort_position)
);

CREATE TABLE teacher_notes (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES qa_threads(id),
  author_user_id uuid NOT NULL REFERENCES users(id),
  body_text text NOT NULL CHECK (char_length(btrim(body_text)) BETWEEN 1 AND 20000),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX qa_threads_student_activity_idx ON qa_threads(student_id,last_message_at DESC,id DESC);
CREATE INDEX qa_threads_teacher_queue_idx ON qa_threads(status,last_message_at DESC,id DESC);
CREATE INDEX qa_messages_thread_time_idx ON qa_messages(thread_id,created_at,id);

-- +goose StatementBegin
CREATE FUNCTION reject_qa_history_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'qa history is immutable' USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER qa_messages_immutable
  BEFORE UPDATE OR DELETE ON qa_messages
  FOR EACH ROW EXECUTE FUNCTION reject_qa_history_mutation();

CREATE TRIGGER qa_message_files_immutable
  BEFORE UPDATE OR DELETE ON qa_message_files
  FOR EACH ROW EXECUTE FUNCTION reject_qa_history_mutation();

CREATE TRIGGER teacher_notes_immutable
  BEFORE UPDATE OR DELETE ON teacher_notes
  FOR EACH ROW EXECUTE FUNCTION reject_qa_history_mutation();

-- +goose Down
DROP INDEX qa_messages_thread_time_idx;
DROP INDEX qa_threads_teacher_queue_idx;
DROP INDEX qa_threads_student_activity_idx;
DROP TRIGGER teacher_notes_immutable ON teacher_notes;
DROP TRIGGER qa_message_files_immutable ON qa_message_files;
DROP TRIGGER qa_messages_immutable ON qa_messages;
DROP FUNCTION reject_qa_history_mutation();
DROP TABLE teacher_notes;
DROP TABLE qa_message_files;
DROP TABLE qa_messages;
DROP TABLE qa_threads;
