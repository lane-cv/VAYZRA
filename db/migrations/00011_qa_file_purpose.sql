-- +goose Up
ALTER TABLE upload_sessions ADD COLUMN purpose text NOT NULL DEFAULT 'teaching'
  CHECK (purpose IN ('teaching','qa_attachment'));
ALTER TABLE file_versions ADD COLUMN purpose text NOT NULL DEFAULT 'teaching'
  CHECK (purpose IN ('teaching','qa_attachment'));
ALTER TABLE upload_sessions ALTER COLUMN purpose DROP DEFAULT;
ALTER TABLE file_versions ALTER COLUMN purpose DROP DEFAULT;

CREATE INDEX file_versions_owner_purpose_state_idx
  ON file_versions(file_id,purpose,processing_state,id);

ALTER TABLE file_access_logs
  ADD COLUMN qa_message_id uuid REFERENCES qa_messages(id) ON DELETE RESTRICT,
  ADD CONSTRAINT file_access_logs_single_business_target CHECK (
    NOT (lesson_revision_id IS NOT NULL AND qa_message_id IS NOT NULL)
  );
CREATE UNIQUE INDEX file_access_logs_qa_playback_sample_key
  ON file_access_logs(actor_user_id,requested_file_version_id,qa_message_id,access_policy,playback_session_hash)
  WHERE result='allow' AND qa_message_id IS NOT NULL AND playback_session_hash<>'';

-- +goose Down
LOCK TABLE file_access_logs IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM file_access_logs WHERE qa_message_id IS NOT NULL) THEN
    RAISE EXCEPTION 'Q&A file access history cannot be represented before migration 00011'
      USING ERRCODE = '55000';
  END IF;
END;
$$;
-- +goose StatementEnd

DROP INDEX file_access_logs_qa_playback_sample_key;
ALTER TABLE file_access_logs DROP CONSTRAINT file_access_logs_single_business_target;
ALTER TABLE file_access_logs DROP COLUMN qa_message_id;
DROP INDEX file_versions_owner_purpose_state_idx;
ALTER TABLE file_versions DROP COLUMN purpose;
ALTER TABLE upload_sessions DROP COLUMN purpose;
