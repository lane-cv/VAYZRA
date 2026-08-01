-- +goose Up
ALTER TABLE system_settings
  ADD COLUMN backup_filesystem_warning_percent integer NOT NULL DEFAULT 75,
  ADD COLUMN backup_filesystem_critical_percent integer NOT NULL DEFAULT 90,
  ADD COLUMN local_backup_age_warning_hours integer NOT NULL DEFAULT 25,
  ADD COLUMN local_backup_age_critical_hours integer NOT NULL DEFAULT 30,
  ADD COLUMN processing_failure_warning_count integer NOT NULL DEFAULT 5,
  ADD COLUMN processing_failure_critical_count integer NOT NULL DEFAULT 20,
  ADD COLUMN login_failure_warning_count integer NOT NULL DEFAULT 20,
  ADD COLUMN login_failure_critical_count integer NOT NULL DEFAULT 100,
  ADD COLUMN authorization_denial_warning_count integer NOT NULL DEFAULT 50,
  ADD COLUMN authorization_denial_critical_count integer NOT NULL DEFAULT 200;

ALTER TABLE system_settings
  DROP CONSTRAINT system_settings_threshold_order_check,
  ADD CONSTRAINT system_settings_threshold_order_check CHECK (
    disk_warning_percent BETWEEN 1 AND 99
    AND disk_critical_percent > disk_warning_percent
    AND disk_critical_percent <= 100
    AND backup_filesystem_warning_percent BETWEEN 1 AND 99
    AND backup_filesystem_critical_percent > backup_filesystem_warning_percent
    AND backup_filesystem_critical_percent <= 100
    AND local_backup_age_warning_hours BETWEEN 1 AND 2147483646
    AND local_backup_age_critical_hours > local_backup_age_warning_hours
    AND local_backup_age_critical_hours <= 2147483647
    AND ai_error_warning_percent BETWEEN 1 AND 99
    AND ai_error_critical_percent > ai_error_warning_percent
    AND ai_error_critical_percent <= 100
    AND processing_queue_warning BETWEEN 1 AND 2147483646
    AND processing_queue_critical > processing_queue_warning
    AND processing_queue_critical <= 2147483647
    AND processing_failure_warning_count BETWEEN 1 AND 2147483646
    AND processing_failure_critical_count > processing_failure_warning_count
    AND processing_failure_critical_count <= 2147483647
    AND login_failure_warning_count BETWEEN 1 AND 2147483646
    AND login_failure_critical_count > login_failure_warning_count
    AND login_failure_critical_count <= 2147483647
    AND authorization_denial_warning_count BETWEEN 1 AND 2147483646
    AND authorization_denial_critical_count > authorization_denial_warning_count
    AND authorization_denial_critical_count <= 2147483647
  );

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM operational_alerts legacy
    JOIN operational_alerts canonical
      ON canonical.dedupe_key = CASE legacy.dedupe_key
        WHEN 'backup_remote_replication' THEN 'backup_remote_sync'
        WHEN 'backup_remote_replication_dependency_unavailable'
          THEN 'backup_remote_sync_dependency_unavailable'
      END
    WHERE legacy.dedupe_key IN (
      'backup_remote_replication',
      'backup_remote_replication_dependency_unavailable'
    )
      AND legacy.state <> 'resolved'
      AND canonical.state <> 'resolved'
  ) THEN
    RAISE EXCEPTION 'unresolved remote alert aliases collide'
      USING ERRCODE = '23505';
  END IF;
END $$;
-- +goose StatementEnd

UPDATE operational_alerts
SET dedupe_key = CASE dedupe_key
  WHEN 'backup_remote_replication' THEN 'backup_remote_sync'
  WHEN 'backup_remote_replication_dependency_unavailable'
    THEN 'backup_remote_sync_dependency_unavailable'
END
WHERE dedupe_key IN (
  'backup_remote_replication',
  'backup_remote_replication_dependency_unavailable'
);

ALTER TABLE operational_alerts
  ADD CONSTRAINT operational_alerts_no_legacy_remote_keys_check CHECK (
    dedupe_key NOT IN (
      'backup_remote_replication',
      'backup_remote_replication_dependency_unavailable'
    )
  );

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM operational_alerts canonical
    JOIN operational_alerts legacy
      ON legacy.dedupe_key = CASE canonical.dedupe_key
        WHEN 'backup_remote_sync' THEN 'backup_remote_replication'
        WHEN 'backup_remote_sync_dependency_unavailable'
          THEN 'backup_remote_replication_dependency_unavailable'
      END
    WHERE canonical.dedupe_key IN (
      'backup_remote_sync',
      'backup_remote_sync_dependency_unavailable'
    )
      AND canonical.state <> 'resolved'
      AND legacy.state <> 'resolved'
  ) THEN
    RAISE EXCEPTION 'unresolved remote alert aliases collide'
      USING ERRCODE = '23505';
  END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE operational_alerts
  DROP CONSTRAINT operational_alerts_no_legacy_remote_keys_check;

UPDATE operational_alerts
SET dedupe_key = CASE dedupe_key
  WHEN 'backup_remote_sync' THEN 'backup_remote_replication'
  WHEN 'backup_remote_sync_dependency_unavailable'
    THEN 'backup_remote_replication_dependency_unavailable'
END
WHERE dedupe_key IN (
  'backup_remote_sync',
  'backup_remote_sync_dependency_unavailable'
);

ALTER TABLE system_settings
  DROP CONSTRAINT system_settings_threshold_order_check,
  DROP COLUMN backup_filesystem_warning_percent,
  DROP COLUMN backup_filesystem_critical_percent,
  DROP COLUMN local_backup_age_warning_hours,
  DROP COLUMN local_backup_age_critical_hours,
  DROP COLUMN processing_failure_warning_count,
  DROP COLUMN processing_failure_critical_count,
  DROP COLUMN login_failure_warning_count,
  DROP COLUMN login_failure_critical_count,
  DROP COLUMN authorization_denial_warning_count,
  DROP COLUMN authorization_denial_critical_count,
  ADD CONSTRAINT system_settings_threshold_order_check CHECK (
    disk_warning_percent BETWEEN 1 AND 99
    AND disk_critical_percent > disk_warning_percent
    AND disk_critical_percent <= 100
    AND ai_error_warning_percent BETWEEN 1 AND 99
    AND ai_error_critical_percent > ai_error_warning_percent
    AND ai_error_critical_percent <= 100
    AND processing_queue_warning >= 1
    AND processing_queue_critical > processing_queue_warning
  );
