-- +goose Up
LOCK TABLE ai_runs IN ACCESS EXCLUSIVE MODE;
ALTER TABLE ai_runs DISABLE TRIGGER USER;
ALTER TABLE ai_runs ADD COLUMN provider_key_version smallint;
UPDATE ai_runs r
SET provider_key_version=p.key_version
FROM ai_providers p
WHERE p.id=r.provider_id;
ALTER TABLE ai_runs ENABLE TRIGGER USER;

ALTER TABLE ai_runs
  ALTER COLUMN provider_key_version SET NOT NULL,
  ADD CONSTRAINT ai_runs_provider_key_version_positive CHECK (provider_key_version > 0);

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_run_provider_key_version() RETURNS trigger AS $$
BEGIN
  IF TG_OP='INSERT' THEN
    IF NOT EXISTS (
      SELECT 1 FROM ai_providers p
      WHERE p.id=NEW.provider_id AND p.key_version=NEW.provider_key_version
    ) THEN
      RAISE EXCEPTION 'AI run provider key version must match the provider'
        USING ERRCODE = '23514';
    END IF;
  ELSIF OLD.provider_key_version IS DISTINCT FROM NEW.provider_key_version THEN
    RAISE EXCEPTION 'AI run provider key version is immutable'
      USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER ai_runs_provider_key_version
BEFORE INSERT OR UPDATE ON ai_runs
FOR EACH ROW EXECUTE FUNCTION enforce_ai_run_provider_key_version();

-- +goose Down
DROP TRIGGER ai_runs_provider_key_version ON ai_runs;
DROP FUNCTION enforce_ai_run_provider_key_version();
ALTER TABLE ai_runs
  DROP CONSTRAINT ai_runs_provider_key_version_positive,
  DROP COLUMN provider_key_version;
