-- +goose Up
CREATE TABLE ai_providers (
  id uuid PRIMARY KEY,
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  base_url text NOT NULL CHECK (char_length(base_url) BETWEEN 8 AND 2048),
  protocol_mode text NOT NULL CHECK (protocol_mode IN ('chat_completions','responses')),
  encrypted_api_key bytea NOT NULL CHECK (octet_length(encrypted_api_key) BETWEEN 29 AND 8192),
  key_version smallint NOT NULL CHECK (key_version > 0),
  key_updated_at timestamptz NOT NULL,
  active boolean NOT NULL DEFAULT false,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ai_providers_one_active_idx ON ai_providers(active) WHERE active;

CREATE TABLE ai_config_idempotency (
  key text PRIMARY KEY CHECK (char_length(key) BETWEEN 16 AND 128),
  operation text NOT NULL CHECK (operation='create_provider'),
  request_hash bytea NOT NULL CHECK (octet_length(request_hash)=32),
  provider_id uuid NOT NULL REFERENCES ai_providers(id) DEFERRABLE INITIALLY DEFERRED,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ai_models (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid NOT NULL REFERENCES ai_providers(id) ON DELETE CASCADE,
  upstream_model_id text NOT NULL CHECK (char_length(btrim(upstream_model_id)) BETWEEN 1 AND 200),
  modality text NOT NULL CHECK (modality IN ('text','vision')),
  context_window_tokens bigint NOT NULL CHECK (context_window_tokens BETWEEN 1 AND 10000000),
  max_output_tokens bigint NOT NULL CHECK (max_output_tokens BETWEEN 1 AND context_window_tokens),
  connect_timeout_ms integer NOT NULL DEFAULT 5000 CHECK (connect_timeout_ms BETWEEN 100 AND 30000),
  response_header_timeout_ms integer NOT NULL DEFAULT 30000 CHECK (response_header_timeout_ms BETWEEN 1000 AND 120000),
  idle_stream_timeout_ms integer NOT NULL DEFAULT 30000 CHECK (idle_stream_timeout_ms BETWEEN 1000 AND 120000),
  total_timeout_ms integer NOT NULL DEFAULT 120000 CHECK (total_timeout_ms BETWEEN 1000 AND 600000),
  image_quota_tokens bigint NOT NULL CHECK (image_quota_tokens BETWEEN 1 AND 10000000),
  input_price_micro_usd_per_million_tokens bigint NOT NULL CHECK (input_price_micro_usd_per_million_tokens >= 0),
  output_price_micro_usd_per_million_tokens bigint NOT NULL CHECK (output_price_micro_usd_per_million_tokens >= 0),
  enabled boolean NOT NULL DEFAULT true,
  quota_blocked_at timestamptz,
  quota_block_reason text CHECK (quota_block_reason IS NULL OR quota_block_reason='quota_estimation_anomaly'),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider_id, upstream_model_id, modality),
  CHECK (total_timeout_ms >= response_header_timeout_ms AND total_timeout_ms >= idle_stream_timeout_ms),
  CHECK ((quota_blocked_at IS NULL) = (quota_block_reason IS NULL))
);

CREATE TABLE prompt_templates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  subject text NOT NULL CHECK (subject IN ('math','physics')),
  version integer NOT NULL CHECK (version > 0),
  system_prompt text NOT NULL CHECK (char_length(btrim(system_prompt)) BETWEEN 1 AND 100000),
  active boolean NOT NULL DEFAULT false,
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (subject, version)
);
CREATE UNIQUE INDEX prompt_templates_one_active_subject_idx ON prompt_templates(subject) WHERE active;

CREATE TABLE ai_global_limits (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  daily_request_limit bigint NOT NULL CHECK (daily_request_limit >= 0),
  monthly_request_limit bigint NOT NULL CHECK (monthly_request_limit >= 0),
  daily_token_limit bigint NOT NULL CHECK (daily_token_limit >= 0),
  monthly_token_limit bigint NOT NULL CHECK (monthly_token_limit >= 0),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_by uuid REFERENCES users(id) ON DELETE RESTRICT,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO ai_global_limits(singleton,daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit)
VALUES (true,0,0,0,0);

CREATE TABLE student_ai_limits (
  student_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
  daily_request_limit bigint CHECK (daily_request_limit IS NULL OR daily_request_limit >= 0),
  monthly_request_limit bigint CHECK (monthly_request_limit IS NULL OR monthly_request_limit >= 0),
  daily_token_limit bigint CHECK (daily_token_limit IS NULL OR daily_token_limit >= 0),
  monthly_token_limit bigint CHECK (monthly_token_limit IS NULL OR monthly_token_limit >= 0),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_configuration_admin_actors() RETURNS trigger AS $$
BEGIN
  IF TG_TABLE_NAME = 'ai_providers' THEN
    IF NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.created_by AND role='admin' AND status='active' AND deleted_at IS NULL) THEN
      RAISE EXCEPTION 'AI configuration actors must be active admins' USING ERRCODE = '23514';
    END IF;
  ELSIF TG_TABLE_NAME = 'ai_models' THEN
    IF NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.created_by AND role='admin' AND status='active' AND deleted_at IS NULL)
       OR NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.updated_by AND role='admin' AND status='active' AND deleted_at IS NULL) THEN
      RAISE EXCEPTION 'AI configuration actors must be active admins' USING ERRCODE = '23514';
    END IF;
  ELSIF TG_TABLE_NAME = 'prompt_templates' THEN
    IF NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.created_by AND role='admin' AND status='active' AND deleted_at IS NULL) THEN
      RAISE EXCEPTION 'AI configuration actors must be active admins' USING ERRCODE = '23514';
    END IF;
  ELSIF TG_TABLE_NAME = 'ai_global_limits' THEN
    IF NEW.updated_by IS NOT NULL AND NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.updated_by AND role='admin' AND status='active' AND deleted_at IS NULL) THEN
      RAISE EXCEPTION 'AI configuration actors must be active admins' USING ERRCODE = '23514';
    END IF;
  ELSIF TG_TABLE_NAME = 'student_ai_limits' THEN
    IF NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.updated_by AND role='admin' AND status='active' AND deleted_at IS NULL) THEN
      RAISE EXCEPTION 'AI configuration actors must be active admins' USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER ai_providers_active_admin_actor
  BEFORE INSERT OR UPDATE ON ai_providers
  FOR EACH ROW EXECUTE FUNCTION enforce_ai_configuration_admin_actors();
CREATE TRIGGER ai_models_active_admin_actors
  BEFORE INSERT OR UPDATE ON ai_models
  FOR EACH ROW EXECUTE FUNCTION enforce_ai_configuration_admin_actors();
CREATE TRIGGER prompt_templates_active_admin_actor
  BEFORE INSERT ON prompt_templates
  FOR EACH ROW EXECUTE FUNCTION enforce_ai_configuration_admin_actors();
CREATE TRIGGER ai_global_limits_active_admin_actor
  BEFORE INSERT OR UPDATE ON ai_global_limits
  FOR EACH ROW EXECUTE FUNCTION enforce_ai_configuration_admin_actors();
CREATE TRIGGER student_ai_limits_active_admin_actor
  BEFORE INSERT OR UPDATE ON student_ai_limits
  FOR EACH ROW EXECUTE FUNCTION enforce_ai_configuration_admin_actors();

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_global_limits_singleton() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'AI global limits singleton cannot be deleted' USING ERRCODE = '55000';
  END IF;
  IF NEW.updated_by IS NULL THEN
    RAISE EXCEPTION 'AI global limits updates require an active admin' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER ai_global_limits_singleton
  BEFORE UPDATE OR DELETE ON ai_global_limits
  FOR EACH ROW EXECUTE FUNCTION enforce_ai_global_limits_singleton();

-- +goose StatementBegin
CREATE FUNCTION enforce_student_ai_limit_student() RETURNS trigger AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM users WHERE id=NEW.student_id AND role='student' AND status='active' AND deleted_at IS NULL) THEN
    RAISE EXCEPTION 'AI student limits require active students' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER student_ai_limits_active_student
  BEFORE INSERT OR UPDATE ON student_ai_limits
  FOR EACH ROW EXECUTE FUNCTION enforce_student_ai_limit_student();

-- +goose StatementBegin
CREATE FUNCTION enforce_prompt_template_immutability() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'prompt templates cannot be deleted' USING ERRCODE = '55000';
  END IF;
  IF OLD.id IS DISTINCT FROM NEW.id
     OR OLD.subject IS DISTINCT FROM NEW.subject
     OR OLD.version IS DISTINCT FROM NEW.version
     OR OLD.system_prompt IS DISTINCT FROM NEW.system_prompt
     OR OLD.created_by IS DISTINCT FROM NEW.created_by
     OR OLD.created_at IS DISTINCT FROM NEW.created_at
     OR OLD.active IS NOT TRUE
     OR NEW.active IS NOT FALSE THEN
    RAISE EXCEPTION 'prompt template versions are immutable' USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER prompt_templates_immutable
  BEFORE UPDATE OR DELETE ON prompt_templates
  FOR EACH ROW EXECUTE FUNCTION enforce_prompt_template_immutability();

-- +goose Down
DROP TRIGGER prompt_templates_immutable ON prompt_templates;
DROP FUNCTION enforce_prompt_template_immutability();
DROP TRIGGER IF EXISTS ai_global_limits_singleton ON ai_global_limits;
DROP FUNCTION IF EXISTS enforce_ai_global_limits_singleton();
DROP TRIGGER student_ai_limits_active_student ON student_ai_limits;
DROP FUNCTION enforce_student_ai_limit_student();
DROP TRIGGER student_ai_limits_active_admin_actor ON student_ai_limits;
DROP TRIGGER ai_global_limits_active_admin_actor ON ai_global_limits;
DROP TRIGGER prompt_templates_active_admin_actor ON prompt_templates;
DROP TRIGGER ai_models_active_admin_actors ON ai_models;
DROP TRIGGER ai_providers_active_admin_actor ON ai_providers;
DROP FUNCTION enforce_ai_configuration_admin_actors();
DROP TABLE student_ai_limits;
DROP TABLE ai_global_limits;
DROP INDEX prompt_templates_one_active_subject_idx;
DROP TABLE prompt_templates;
DROP TABLE ai_models;
DROP TABLE IF EXISTS ai_config_idempotency;
DROP INDEX ai_providers_one_active_idx;
DROP TABLE ai_providers;
