-- +goose Up
ALTER TABLE lesson_draft_files
  ADD COLUMN display_name text NOT NULL DEFAULT 'attachment' CHECK (char_length(display_name) BETWEEN 1 AND 255),
  ADD COLUMN description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500);
ALTER TABLE lesson_draft_files ALTER COLUMN display_name DROP DEFAULT;

ALTER TABLE lesson_revision_files
  ADD COLUMN display_name text NOT NULL DEFAULT 'attachment' CHECK (char_length(display_name) BETWEEN 1 AND 255),
  ADD COLUMN description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500);
ALTER TABLE lesson_revision_files ALTER COLUMN display_name DROP DEFAULT;

ALTER TABLE file_access_logs DROP CONSTRAINT file_access_logs_file_version_id_fkey;
ALTER TABLE file_access_logs ALTER COLUMN file_version_id DROP NOT NULL;
ALTER TABLE file_access_logs
  ADD COLUMN requested_file_version_id uuid,
  ADD COLUMN result text NOT NULL DEFAULT 'allow' CHECK (result IN ('allow','deny','malformed','fail')),
  ADD COLUMN reason_code text NOT NULL DEFAULT '' CHECK (reason_code IN ('','not_found','policy','not_ready','invalid_range','storage','stream_read','stream_write','stream_close','cancelled')),
  ADD COLUMN ip inet,
  ADD COLUMN playback_session_hash text NOT NULL DEFAULT '' CHECK (char_length(playback_session_hash) IN (0,64));
UPDATE file_access_logs SET requested_file_version_id=file_version_id WHERE requested_file_version_id IS NULL;
UPDATE file_access_logs SET ip='0.0.0.0' WHERE ip IS NULL;
ALTER TABLE file_access_logs ALTER COLUMN requested_file_version_id SET NOT NULL;
ALTER TABLE file_access_logs ALTER COLUMN result DROP DEFAULT;
ALTER TABLE file_access_logs ALTER COLUMN reason_code DROP DEFAULT;
ALTER TABLE file_access_logs ALTER COLUMN ip SET NOT NULL;
ALTER TABLE file_access_logs ADD CONSTRAINT file_access_logs_resolved_version_fkey FOREIGN KEY(file_version_id) REFERENCES file_versions(id) ON DELETE RESTRICT;

CREATE TRIGGER lesson_revision_files_insert_immutable
  BEFORE INSERT ON lesson_revision_files
  FOR EACH ROW EXECUTE FUNCTION reject_finalized_lesson_revision_child_mutation();

CREATE INDEX file_access_logs_requested_time_idx ON file_access_logs(requested_file_version_id,occurred_at DESC,id);
CREATE UNIQUE INDEX file_access_logs_playback_sample_key ON file_access_logs(actor_user_id,requested_file_version_id,lesson_revision_id,access_policy,playback_session_hash) WHERE result='allow' AND playback_session_hash<>'';

-- +goose Down
DROP INDEX IF EXISTS file_access_logs_playback_sample_key;
DROP INDEX file_access_logs_requested_time_idx;
DROP TRIGGER lesson_revision_files_insert_immutable ON lesson_revision_files;
ALTER TABLE file_access_logs DROP CONSTRAINT file_access_logs_resolved_version_fkey;
DROP TRIGGER file_access_logs_immutable ON file_access_logs;
DELETE FROM file_access_logs WHERE file_version_id IS NULL;
CREATE TRIGGER file_access_logs_immutable BEFORE UPDATE OR DELETE ON file_access_logs FOR EACH ROW EXECUTE FUNCTION reject_secure_file_history_mutation();
ALTER TABLE file_access_logs DROP COLUMN playback_session_hash,DROP COLUMN ip,DROP COLUMN reason_code,DROP COLUMN result,DROP COLUMN requested_file_version_id;
ALTER TABLE file_access_logs ALTER COLUMN file_version_id SET NOT NULL;
ALTER TABLE file_access_logs ADD CONSTRAINT file_access_logs_file_version_id_fkey FOREIGN KEY(file_version_id) REFERENCES file_versions(id) ON DELETE RESTRICT;
ALTER TABLE lesson_revision_files DROP COLUMN description,DROP COLUMN display_name;
ALTER TABLE lesson_draft_files DROP COLUMN description,DROP COLUMN display_name;