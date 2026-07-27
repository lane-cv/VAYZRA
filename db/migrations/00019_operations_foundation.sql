-- +goose Up
CREATE TABLE system_settings (
  singleton_id boolean PRIMARY KEY DEFAULT true CHECK (singleton_id),
  version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  site_name text NOT NULL DEFAULT 'HappyLearn'
    CHECK (char_length(site_name) BETWEEN 1 AND 80),
  site_announcement text NOT NULL DEFAULT ''
    CHECK (char_length(site_announcement) <= 1000),
  soft_delete_retention_days integer NOT NULL DEFAULT 30,
  audit_retention_days integer NOT NULL DEFAULT 365,
  operational_sample_retention_days integer NOT NULL DEFAULT 7,
  backup_hour integer NOT NULL DEFAULT 3,
  backup_minute integer NOT NULL DEFAULT 0,
  backup_timezone text NOT NULL DEFAULT 'Asia/Shanghai'
    CHECK (backup_timezone='Asia/Shanghai'),
  disk_warning_percent integer NOT NULL DEFAULT 75,
  disk_critical_percent integer NOT NULL DEFAULT 90,
  ai_error_warning_percent integer NOT NULL DEFAULT 10,
  ai_error_critical_percent integer NOT NULL DEFAULT 25,
  processing_queue_warning integer NOT NULL DEFAULT 20,
  processing_queue_critical integer NOT NULL DEFAULT 100,
  updated_by uuid REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT system_settings_retention_check CHECK (
    soft_delete_retention_days BETWEEN 30 AND 365
    AND audit_retention_days BETWEEN 365 AND 2555
    AND operational_sample_retention_days BETWEEN 1 AND 30),
  CONSTRAINT system_settings_backup_clock_check CHECK (
    backup_hour BETWEEN 0 AND 23 AND backup_minute BETWEEN 0 AND 59),
  CONSTRAINT system_settings_threshold_order_check CHECK (
    disk_warning_percent BETWEEN 1 AND 99
    AND disk_critical_percent > disk_warning_percent
    AND disk_critical_percent <= 100
    AND ai_error_warning_percent BETWEEN 1 AND 99
    AND ai_error_critical_percent > ai_error_warning_percent
    AND ai_error_critical_percent <= 100
    AND processing_queue_warning >= 1
    AND processing_queue_critical > processing_queue_warning)
);
INSERT INTO system_settings(singleton_id) VALUES(true);

CREATE TABLE operational_modes (
  singleton_id boolean PRIMARY KEY DEFAULT true CHECK (singleton_id),
  mode text NOT NULL DEFAULT 'normal',
  owner_id uuid,
  lease_token_hash bytea,
  lease_expires_at timestamptz,
  entered_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  CONSTRAINT operational_modes_state_check
    CHECK (mode IN ('normal','draining','backup','release')),
  CONSTRAINT operational_modes_lease_shape_check CHECK (
    (mode='normal' AND owner_id IS NULL AND lease_token_hash IS NULL
      AND lease_expires_at IS NULL AND entered_at IS NULL)
    OR
    (mode<>'normal' AND owner_id IS NOT NULL
      AND lease_token_hash IS NOT NULL AND octet_length(lease_token_hash)=32
      AND lease_expires_at IS NOT NULL AND entered_at IS NOT NULL))
);
INSERT INTO operational_modes(singleton_id) VALUES(true);

-- +goose Down
DROP TABLE operational_modes;
DROP TABLE system_settings;
