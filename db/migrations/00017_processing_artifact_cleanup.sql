-- +goose Up
CREATE TABLE file_processing_artifacts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  file_version_id uuid NOT NULL REFERENCES file_versions(id) ON DELETE RESTRICT,
  processing_job_id uuid NOT NULL REFERENCES file_processing_jobs(id) ON DELETE RESTRICT,
  attempt_no integer NOT NULL CHECK (attempt_no BETWEEN 1 AND 4),
  artifact_kind text NOT NULL CHECK (artifact_kind IN ('pdf','page','thumbnail','poster','ai_text')),
  object_key text NOT NULL UNIQUE CHECK (char_length(object_key) BETWEEN 1 AND 512),
  content_type text NOT NULL CHECK (char_length(content_type) BETWEEN 1 AND 160),
  size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 524288000),
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  state text NOT NULL CHECK (state IN ('reserved','stored','delete_pending')),
  cleanup_lease_owner text CHECK (cleanup_lease_owner IS NULL OR char_length(cleanup_lease_owner) BETWEEN 1 AND 128),
  cleanup_lease_until timestamptz,
  cleanup_attempts integer NOT NULL DEFAULT 0 CHECK (cleanup_attempts BETWEEN 0 AND 1000),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(processing_job_id,attempt_no,artifact_kind),
  CHECK (
    (cleanup_lease_owner IS NULL AND cleanup_lease_until IS NULL)
    OR (state='delete_pending' AND cleanup_lease_owner IS NOT NULL AND cleanup_lease_until IS NOT NULL)
  )
);
CREATE INDEX file_processing_artifacts_cleanup_idx
  ON file_processing_artifacts(state,updated_at,id);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM file_processing_artifacts) THEN
    RAISE EXCEPTION 'processing artifact cleanup rows cannot be represented before migration 00017'
      USING ERRCODE = '55000';
  END IF;
END;
$$;
-- +goose StatementEnd
DROP INDEX file_processing_artifacts_cleanup_idx;
DROP TABLE file_processing_artifacts;
