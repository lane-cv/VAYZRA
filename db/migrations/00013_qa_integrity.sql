-- +goose Up
ALTER TABLE qa_message_files
  ADD CONSTRAINT qa_message_files_file_version_unique UNIQUE (file_version_id);

-- +goose StatementBegin
CREATE FUNCTION enforce_qa_thread_ownership_immutable() RETURNS trigger AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id
     OR NEW.student_id IS DISTINCT FROM OLD.student_id
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'qa thread ownership is immutable' USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER qa_threads_ownership_immutable
  BEFORE UPDATE ON qa_threads
  FOR EACH ROW EXECUTE FUNCTION enforce_qa_thread_ownership_immutable();

-- +goose StatementBegin
CREATE FUNCTION enforce_qa_message_sender() RETURNS trigger AS $$
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

CREATE TRIGGER qa_messages_sender_integrity
  BEFORE INSERT ON qa_messages
  FOR EACH ROW EXECUTE FUNCTION enforce_qa_message_sender();

-- +goose Down
DROP TRIGGER qa_messages_sender_integrity ON qa_messages;
DROP FUNCTION enforce_qa_message_sender();
DROP TRIGGER qa_threads_ownership_immutable ON qa_threads;
DROP FUNCTION enforce_qa_thread_ownership_immutable();
ALTER TABLE qa_message_files DROP CONSTRAINT qa_message_files_file_version_unique;
