-- +goose Up
ALTER TABLE file_versions
  ADD COLUMN detected_mime text CHECK (detected_mime IS NULL OR char_length(detected_mime) BETWEEN 1 AND 160),
  ADD COLUMN scan_result text CHECK (scan_result IS NULL OR scan_result IN ('clean','rejected')),
  ADD COLUMN browser_playable boolean NOT NULL DEFAULT false,
  ADD COLUMN video_container text CHECK (video_container IS NULL OR char_length(video_container) BETWEEN 1 AND 80),
  ADD COLUMN video_codec text CHECK (video_codec IS NULL OR char_length(video_codec) BETWEEN 1 AND 80),
  ADD COLUMN video_duration_ms bigint CHECK (video_duration_ms IS NULL OR video_duration_ms BETWEEN 0 AND 43200000),
  ADD COLUMN video_width integer CHECK (video_width IS NULL OR video_width BETWEEN 1 AND 7680),
  ADD COLUMN video_height integer CHECK (video_height IS NULL OR video_height BETWEEN 1 AND 4320),
  ADD COLUMN failure_category text CHECK (failure_category IS NULL OR failure_category ~ '^[a-z][a-z0-9_]{0,63}$');

CREATE TABLE file_processing_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  file_version_id uuid NOT NULL REFERENCES file_versions(id) ON DELETE RESTRICT,
  kind text NOT NULL CHECK (kind IN ('process_file')),
  state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','completed','failed')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 4),
  available_at timestamptz NOT NULL DEFAULT now(),
  lease_owner text CHECK (lease_owner IS NULL OR char_length(lease_owner) BETWEEN 1 AND 128),
  lease_until timestamptz,
  last_failure_category text CHECK (last_failure_category IS NULL OR last_failure_category ~ '^[a-z][a-z0-9_]{0,63}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((state='running') = (lease_owner IS NOT NULL AND lease_until IS NOT NULL))
);

CREATE UNIQUE INDEX file_processing_jobs_active_key
  ON file_processing_jobs(file_version_id,kind)
  WHERE state IN ('queued','running');
CREATE INDEX file_processing_jobs_claim_idx
  ON file_processing_jobs(available_at,created_at,id)
  WHERE state IN ('queued','running');
CREATE INDEX file_processing_jobs_version_idx
  ON file_processing_jobs(file_version_id,created_at DESC,id);

INSERT INTO file_processing_jobs(file_version_id,kind)
SELECT id,'process_file'
FROM file_versions
WHERE processing_state='pending_scan'
ON CONFLICT (file_version_id,kind) WHERE state IN ('queued','running') DO NOTHING;

-- +goose Down
DROP INDEX file_processing_jobs_version_idx;
DROP INDEX file_processing_jobs_claim_idx;
DROP INDEX file_processing_jobs_active_key;
DROP TABLE file_processing_jobs;
ALTER TABLE file_versions
  DROP COLUMN failure_category,
  DROP COLUMN video_height,
  DROP COLUMN video_width,
  DROP COLUMN video_duration_ms,
  DROP COLUMN video_codec,
  DROP COLUMN video_container,
  DROP COLUMN browser_playable,
  DROP COLUMN scan_result,
  DROP COLUMN detected_mime;