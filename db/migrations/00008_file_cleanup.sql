-- +goose Up
ALTER TABLE file_versions
  ADD COLUMN purged_at timestamptz,
  ADD COLUMN cleanup_state text CHECK (cleanup_state IS NULL OR cleanup_state IN ('pending','deleting','purged')),
  ADD COLUMN cleanup_lease_owner text CHECK (cleanup_lease_owner IS NULL OR char_length(cleanup_lease_owner) BETWEEN 1 AND 128),
  ADD COLUMN cleanup_lease_until timestamptz,
  ADD COLUMN cleanup_attempts integer NOT NULL DEFAULT 0 CHECK (cleanup_attempts BETWEEN 0 AND 1000),
  ADD CONSTRAINT file_versions_cleanup_lease_check CHECK (
    (cleanup_state='deleting' AND cleanup_lease_owner IS NOT NULL AND cleanup_lease_until IS NOT NULL)
    OR (cleanup_state IS DISTINCT FROM 'deleting' AND cleanup_lease_owner IS NULL AND cleanup_lease_until IS NULL)
  );
CREATE INDEX file_versions_cleanup_idx ON file_versions (retention_until,id) WHERE purged_at IS NULL AND retention_until IS NOT NULL;

-- +goose Down
DROP INDEX file_versions_cleanup_idx;
ALTER TABLE file_versions
  DROP CONSTRAINT file_versions_cleanup_lease_check,
  DROP COLUMN cleanup_attempts,
  DROP COLUMN cleanup_lease_until,
  DROP COLUMN cleanup_lease_owner,
  DROP COLUMN cleanup_state,
  DROP COLUMN purged_at;
