-- +goose Up
CREATE TABLE backup_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  idempotency_key text NOT NULL,
  trigger_kind text NOT NULL,
  state text NOT NULL DEFAULT 'queued',
  requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
  requested_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz,
  database_migration_version bigint,
  encryption_key_id text,
  local_snapshot_id text,
  remote_snapshot_id text,
  manifest_sha256 bytea,
  logical_bytes bigint,
  stored_bytes bigint,
  local_expires_at timestamptz,
  remote_expires_at timestamptz,
  error_category text NOT NULL DEFAULT '',
  error_trace_id text NOT NULL DEFAULT '',
  owner_id uuid,
  lease_expires_at timestamptz,
  CONSTRAINT backup_runs_idempotency_key
    UNIQUE(trigger_kind, idempotency_key),
  CONSTRAINT backup_runs_idempotency_key_check
    CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
  CONSTRAINT backup_runs_trigger_kind_check
    CHECK (trigger_kind IN ('scheduled', 'manual', 'pre_release')),
  CONSTRAINT backup_runs_state_check
    CHECK (state IN (
      'queued',
      'draining',
      'snapshotting',
      'encrypting',
      'verifying',
      'syncing',
      'succeeded',
      'degraded',
      'failed'
    )),
  CONSTRAINT backup_runs_database_migration_version_check
    CHECK (
      database_migration_version IS NULL
      OR database_migration_version >= 1
    ),
  CONSTRAINT backup_runs_manifest_sha256_check
    CHECK (
      manifest_sha256 IS NULL
      OR octet_length(manifest_sha256) = 32
    ),
  CONSTRAINT backup_runs_logical_bytes_check
    CHECK (logical_bytes IS NULL OR logical_bytes >= 0),
  CONSTRAINT backup_runs_stored_bytes_check
    CHECK (stored_bytes IS NULL OR stored_bytes >= 0),
  CONSTRAINT backup_runs_lease_shape_check CHECK (
    (
      owner_id IS NULL
      AND lease_expires_at IS NULL
    )
    OR
    (
      owner_id IS NOT NULL
      AND lease_expires_at IS NOT NULL
    )
  ),
  CONSTRAINT backup_runs_terminal_check CHECK (
    (
      state IN ('succeeded', 'degraded', 'failed')
      AND finished_at IS NOT NULL
    )
    OR
    (
      state NOT IN ('succeeded', 'degraded', 'failed')
      AND finished_at IS NULL
    )
  ),
  CONSTRAINT backup_runs_terminal_lease_check CHECK (
    state NOT IN ('succeeded', 'degraded', 'failed')
    OR
    (
      owner_id IS NULL
      AND lease_expires_at IS NULL
    )
  ),
  CONSTRAINT backup_runs_recovery_evidence_check CHECK (
    state NOT IN ('succeeded', 'degraded')
    OR
    (
      local_snapshot_id IS NOT NULL
      AND btrim(local_snapshot_id) <> ''
      AND manifest_sha256 IS NOT NULL
      AND octet_length(manifest_sha256) = 32
      AND database_migration_version IS NOT NULL
      AND database_migration_version >= 1
      AND encryption_key_id IS NOT NULL
      AND btrim(encryption_key_id) <> ''
      AND local_expires_at IS NOT NULL
    )
  )
);

CREATE INDEX backup_runs_requested_idx
  ON backup_runs(requested_at DESC, id DESC);

CREATE INDEX backup_runs_due_idx
  ON backup_runs(state, requested_at, id)
  WHERE state NOT IN ('succeeded', 'degraded', 'failed');

CREATE TABLE backup_artifacts (
  backup_run_id uuid NOT NULL
    REFERENCES backup_runs(id) ON DELETE CASCADE,
  kind text NOT NULL,
  repository text NOT NULL,
  snapshot_id text NOT NULL,
  sha256 bytea NOT NULL,
  size_bytes bigint NOT NULL,
  verified_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY(backup_run_id, kind, repository),
  CONSTRAINT backup_artifacts_sha256_check
    CHECK (octet_length(sha256) = 32),
  CONSTRAINT backup_artifacts_size_bytes_check
    CHECK (size_bytes >= 0),
  CONSTRAINT backup_artifacts_kind_check
    CHECK (kind IN (
      'database_dump',
      'object_snapshot',
      'manifest',
      'recovery_report'
    )),
  CONSTRAINT backup_artifacts_repository_check
    CHECK (repository IN ('local', 'remote'))
);

CREATE TABLE restore_verifications (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  backup_run_id uuid NOT NULL REFERENCES backup_runs(id),
  state text NOT NULL DEFAULT 'queued',
  started_at timestamptz,
  finished_at timestamptz,
  restored_migration_version bigint,
  database_row_counts jsonb NOT NULL DEFAULT '{}'::jsonb,
  checked_object_count bigint NOT NULL DEFAULT 0,
  missing_object_count bigint NOT NULL DEFAULT 0,
  unexpected_object_count bigint NOT NULL DEFAULT 0,
  session_revocation_verified boolean NOT NULL DEFAULT false,
  rto_seconds bigint,
  report_sha256 bytea,
  error_category text NOT NULL DEFAULT '',
  error_trace_id text NOT NULL DEFAULT '',
  CONSTRAINT restore_verifications_state_check
    CHECK (state IN ('queued', 'restoring', 'checking', 'succeeded', 'failed')),
  CONSTRAINT restore_verifications_counts_check CHECK (
    checked_object_count >= 0
    AND missing_object_count >= 0
    AND unexpected_object_count >= 0
  ),
  CONSTRAINT restore_verifications_rto_seconds_check
    CHECK (rto_seconds IS NULL OR rto_seconds >= 0),
  CONSTRAINT restore_verifications_migration_version_check CHECK (
    restored_migration_version IS NULL
    OR restored_migration_version >= 1
  ),
  CONSTRAINT restore_verifications_report_sha256_check CHECK (
    report_sha256 IS NULL
    OR octet_length(report_sha256) = 32
  ),
  CONSTRAINT restore_verifications_row_counts_check
    CHECK (jsonb_typeof(database_row_counts) = 'object'),
  CONSTRAINT restore_verifications_terminal_check CHECK (
    (
      state IN ('succeeded', 'failed')
      AND finished_at IS NOT NULL
    )
    OR
    (
      state NOT IN ('succeeded', 'failed')
      AND finished_at IS NULL
    )
  ),
  CONSTRAINT restore_verifications_timing_check CHECK (
    (
      state = 'queued'
      AND started_at IS NULL
      AND finished_at IS NULL
    )
    OR
    (
      state IN ('restoring', 'checking')
      AND started_at IS NOT NULL
      AND finished_at IS NULL
    )
    OR
    (
      state IN ('succeeded', 'failed')
      AND started_at IS NOT NULL
      AND finished_at IS NOT NULL
      AND finished_at >= started_at
    )
  ),
  CONSTRAINT restore_verifications_success_check CHECK (
    state <> 'succeeded'
    OR
    (
      session_revocation_verified
      AND missing_object_count = 0
      AND restored_migration_version IS NOT NULL
      AND restored_migration_version >= 1
      AND report_sha256 IS NOT NULL
      AND octet_length(report_sha256) = 32
      AND rto_seconds IS NOT NULL
      AND database_row_counts <> '{}'::jsonb
    )
  )
);

CREATE INDEX restore_verifications_backup_idx
  ON restore_verifications(backup_run_id, started_at DESC, id DESC);

-- +goose Down
DROP TABLE restore_verifications;
DROP TABLE backup_artifacts;
DROP TABLE backup_runs;
