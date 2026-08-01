-- +goose Up
CREATE TABLE infrastructure_configuration_status (
  configuration_key text PRIMARY KEY,
  configured boolean NOT NULL,
  last_validated_at timestamptz NOT NULL,
  CONSTRAINT infrastructure_configuration_status_key_check CHECK (
    configuration_key IN (
      'application_database',
      'redis_security',
      'object_store',
      'ai_encryption',
      'internal_metrics',
      'host_metrics_ingestion',
      'alert_webhook',
      'local_backup',
      'remote_backup'
    )
  )
);

-- +goose Down
DROP TABLE infrastructure_configuration_status;
