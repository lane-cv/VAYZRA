-- +goose Up
CREATE TABLE files (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz
);

CREATE TABLE file_versions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  file_id uuid NOT NULL REFERENCES files(id) ON DELETE RESTRICT,
  version bigint NOT NULL CHECK (version > 0),
  object_key text NOT NULL UNIQUE CHECK (char_length(object_key) BETWEEN 1 AND 512),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 255),
  declared_mime text NOT NULL CHECK (char_length(declared_mime) BETWEEN 1 AND 160),
  size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 524288000),
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  processing_state text NOT NULL CHECK (processing_state IN ('pending_scan','processing','ready','rejected','failed')),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  retention_until timestamptz,
  UNIQUE (file_id, version)
);

CREATE TABLE file_previews (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  file_version_id uuid NOT NULL REFERENCES file_versions(id) ON DELETE RESTRICT,
  preview_kind text NOT NULL CHECK (preview_kind IN ('pdf','page','thumbnail','poster')),
  object_key text NOT NULL UNIQUE CHECK (char_length(object_key) BETWEEN 1 AND 512),
  content_type text NOT NULL CHECK (char_length(content_type) BETWEEN 1 AND 160),
  size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 524288000),
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  processing_state text NOT NULL CHECK (processing_state IN ('pending','processing','ready','failed')),
  page_count integer CHECK (page_count IS NULL OR page_count > 0),
  width integer CHECK (width IS NULL OR width > 0),
  height integer CHECK (height IS NULL OR height > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (file_version_id, preview_kind)
);

CREATE TABLE lesson_draft_files (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  lesson_id uuid NOT NULL REFERENCES lesson_drafts(lesson_id) ON DELETE RESTRICT,
  file_version_id uuid NOT NULL REFERENCES file_versions(id) ON DELETE RESTRICT,
  access_policy text NOT NULL CHECK (access_policy IN ('preview','download')),
  sort_position bigint NOT NULL CHECK (sort_position >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (lesson_id, file_version_id),
  UNIQUE (lesson_id, sort_position)
);

CREATE TABLE lesson_revision_files (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  revision_id uuid NOT NULL REFERENCES lesson_revisions(id) ON DELETE RESTRICT,
  file_version_id uuid NOT NULL REFERENCES file_versions(id) ON DELETE RESTRICT,
  access_policy text NOT NULL CHECK (access_policy IN ('preview','download')),
  sort_position bigint NOT NULL CHECK (sort_position >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (revision_id, file_version_id),
  UNIQUE (revision_id, sort_position)
);

CREATE TABLE upload_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  object_key text NOT NULL UNIQUE,
  minio_upload_id text NOT NULL UNIQUE,
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 255),
  declared_mime text NOT NULL CHECK (char_length(declared_mime) BETWEEN 1 AND 160),
  expected_size bigint NOT NULL CHECK (expected_size BETWEEN 1 AND 524288000),
  expected_sha256 text NOT NULL CHECK (expected_sha256 ~ '^[0-9a-f]{64}$'),
  state text NOT NULL CHECK (state IN ('open','completing','completed','cancelled','expired')),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE upload_parts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  upload_session_id uuid NOT NULL REFERENCES upload_sessions(id) ON DELETE RESTRICT,
  part_number integer NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
  size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 524288000),
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  etag text NOT NULL CHECK (char_length(etag) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (upload_session_id, part_number)
);

CREATE TABLE file_access_logs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  file_version_id uuid NOT NULL REFERENCES file_versions(id) ON DELETE RESTRICT,
  lesson_revision_id uuid REFERENCES lesson_revisions(id) ON DELETE RESTRICT,
  access_policy text NOT NULL CHECK (access_policy IN ('preview','download')),
  request_id text NOT NULL CHECK (char_length(request_id) BETWEEN 1 AND 128),
  range_start bigint CHECK (range_start IS NULL OR range_start >= 0),
  range_end bigint CHECK (range_end IS NULL OR range_end >= range_start),
  occurred_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reject_secure_file_history_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'secure file history is immutable' USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER lesson_revision_files_immutable
  BEFORE UPDATE OR DELETE ON lesson_revision_files
  FOR EACH ROW EXECUTE FUNCTION reject_secure_file_history_mutation();

CREATE TRIGGER file_access_logs_immutable
  BEFORE UPDATE OR DELETE ON file_access_logs
  FOR EACH ROW EXECUTE FUNCTION reject_secure_file_history_mutation();

CREATE INDEX file_versions_file_created_idx ON file_versions (file_id, created_at DESC, id);
CREATE INDEX file_versions_processing_idx ON file_versions (processing_state, created_at, id);
CREATE INDEX file_previews_version_idx ON file_previews (file_version_id, preview_kind);
CREATE INDEX lesson_draft_files_sort_idx ON lesson_draft_files (lesson_id, sort_position, id);
CREATE INDEX lesson_revision_files_sort_idx ON lesson_revision_files (revision_id, sort_position, id);
CREATE INDEX upload_sessions_state_expiry_idx ON upload_sessions (state, expires_at, id);
CREATE INDEX file_access_logs_actor_time_idx ON file_access_logs (actor_user_id, occurred_at DESC, id);
CREATE INDEX file_access_logs_version_time_idx ON file_access_logs (file_version_id, occurred_at DESC, id);

-- +goose Down
DROP INDEX file_access_logs_version_time_idx;
DROP INDEX file_access_logs_actor_time_idx;
DROP INDEX upload_sessions_state_expiry_idx;
DROP INDEX lesson_revision_files_sort_idx;
DROP INDEX lesson_draft_files_sort_idx;
DROP INDEX file_previews_version_idx;
DROP INDEX file_versions_processing_idx;
DROP INDEX file_versions_file_created_idx;
DROP TRIGGER file_access_logs_immutable ON file_access_logs;
DROP TRIGGER lesson_revision_files_immutable ON lesson_revision_files;
DROP FUNCTION reject_secure_file_history_mutation();
DROP TABLE file_access_logs;
DROP TABLE upload_parts;
DROP TABLE upload_sessions;
DROP TABLE lesson_revision_files;
DROP TABLE lesson_draft_files;
DROP TABLE file_previews;
DROP TABLE file_versions;
DROP TABLE files;
