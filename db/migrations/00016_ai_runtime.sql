-- +goose Up
CREATE TABLE ai_threads (
  id uuid PRIMARY KEY,
  student_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
  subject text NOT NULL CHECK (subject IN ('math','physics')),
  last_message_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ai_messages (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES ai_threads(id) ON DELETE RESTRICT,
  role text NOT NULL CHECK (role IN ('student','assistant')),
  sender_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
  body_text text NOT NULL CHECK (char_length(btrim(body_text)) BETWEEN 1 AND 100000),
  trigger_run_id uuid,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((role='student')=(sender_user_id IS NOT NULL)),
  CHECK ((role='student')=(idempotency_key IS NOT NULL)),
  CHECK (idempotency_key IS NULL OR char_length(idempotency_key) BETWEEN 16 AND 128),
  UNIQUE(sender_user_id,idempotency_key)
);

CREATE TABLE ai_message_files (
  message_id uuid NOT NULL REFERENCES ai_messages(id) ON DELETE RESTRICT,
  file_version_id uuid NOT NULL REFERENCES file_versions(id) ON DELETE RESTRICT,
  sort_position smallint NOT NULL CHECK (sort_position >= 0),
  display_name text NOT NULL CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 255),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(message_id,file_version_id),
  UNIQUE(message_id,sort_position),
  UNIQUE(file_version_id)
);

CREATE TABLE ai_runs (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES ai_threads(id) ON DELETE RESTRICT,
  student_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  trigger_message_id uuid NOT NULL REFERENCES ai_messages(id) ON DELETE RESTRICT,
  attempt_no integer NOT NULL CHECK (attempt_no > 0),
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 16 AND 128),
  status text NOT NULL CHECK (status IN ('queued','streaming','succeeded','failed','cancelled')),

  provider_id uuid NOT NULL REFERENCES ai_providers(id) ON DELETE RESTRICT,
  provider_base_url text NOT NULL CHECK (char_length(provider_base_url) BETWEEN 8 AND 2048 AND position('?' IN provider_base_url)=0),
  protocol_mode text NOT NULL CHECK (protocol_mode IN ('chat_completions','responses')),
  model_id uuid NOT NULL REFERENCES ai_models(id) ON DELETE RESTRICT,
  upstream_model_id text NOT NULL CHECK (char_length(btrim(upstream_model_id)) BETWEEN 1 AND 200),
  modality text NOT NULL CHECK (modality IN ('text','vision')),
  context_window_tokens bigint NOT NULL CHECK (context_window_tokens > 0),
  max_output_tokens bigint NOT NULL CHECK (max_output_tokens > 0 AND max_output_tokens <= context_window_tokens),
  image_quota_tokens bigint NOT NULL CHECK (image_quota_tokens > 0),
  input_price_micro_usd_per_million_tokens bigint NOT NULL CHECK (input_price_micro_usd_per_million_tokens >= 0),
  output_price_micro_usd_per_million_tokens bigint NOT NULL CHECK (output_price_micro_usd_per_million_tokens >= 0),
  prompt_id uuid NOT NULL REFERENCES prompt_templates(id) ON DELETE RESTRICT,
  prompt_subject text NOT NULL CHECK (prompt_subject IN ('math','physics')),
  prompt_version integer NOT NULL CHECK (prompt_version > 0),
  prompt_sha256 text NOT NULL CHECK (prompt_sha256 ~ '^[0-9a-f]{64}$'),
  connect_timeout_ms integer NOT NULL CHECK (connect_timeout_ms BETWEEN 100 AND 30000),
  response_header_timeout_ms integer NOT NULL CHECK (response_header_timeout_ms BETWEEN 1000 AND 120000),
  idle_stream_timeout_ms integer NOT NULL CHECK (idle_stream_timeout_ms BETWEEN 1000 AND 120000),
  total_timeout_ms integer NOT NULL CHECK (total_timeout_ms >= response_header_timeout_ms AND total_timeout_ms >= idle_stream_timeout_ms AND total_timeout_ms <= 600000),

  reserved_request_count bigint NOT NULL CHECK (reserved_request_count > 0),
  reserved_token_count bigint NOT NULL CHECK (reserved_token_count >= 0),
  quota_day_key text NOT NULL CHECK (quota_day_key ~ '^\d{4}-\d{2}-\d{2}$'),
  quota_month_key text NOT NULL CHECK (quota_month_key ~ '^\d{4}-\d{2}$'),
  estimator_version integer NOT NULL CHECK (estimator_version > 0),

  lease_owner text CHECK (lease_owner IS NULL OR char_length(lease_owner) BETWEEN 1 AND 128),
  lease_expires_at timestamptz,
  heartbeat_at timestamptz,
  cancel_requested_at timestamptz,

  input_tokens bigint CHECK (input_tokens IS NULL OR input_tokens >= 0),
  output_tokens bigint CHECK (output_tokens IS NULL OR output_tokens >= 0),
  cost_micro_usd bigint CHECK (cost_micro_usd IS NULL OR cost_micro_usd >= 0),
  usage_source text CHECK (usage_source IS NULL OR usage_source IN ('upstream','estimated','unknown')),
  first_byte_ms bigint CHECK (first_byte_ms IS NULL OR first_byte_ms >= 0),
  total_ms bigint CHECK (total_ms IS NULL OR total_ms >= 0),
  finish_reason text CHECK (finish_reason IS NULL OR char_length(finish_reason) BETWEEN 1 AND 128),
  error_code text CHECK (error_code IS NULL OR char_length(error_code) BETWEEN 1 AND 128),
  last_sequence bigint NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  completed_at timestamptz,
  UNIQUE(student_id,idempotency_key),
  UNIQUE(trigger_message_id,attempt_no),
  CHECK ((lease_owner IS NULL) = (lease_expires_at IS NULL) AND (lease_owner IS NULL) = (heartbeat_at IS NULL)),
  CHECK (
    (status='queued' AND started_at IS NULL AND completed_at IS NULL AND lease_owner IS NULL AND error_code IS NULL)
    OR (status='streaming' AND started_at IS NOT NULL AND completed_at IS NULL AND lease_owner IS NOT NULL AND error_code IS NULL)
    OR (status='succeeded' AND completed_at IS NOT NULL AND lease_owner IS NULL AND usage_source IS NOT NULL AND error_code IS NULL)
    OR (status IN ('failed','cancelled') AND completed_at IS NOT NULL AND lease_owner IS NULL AND usage_source IS NOT NULL AND error_code IS NOT NULL)
  ),
  -- `unknown` records an explicitly unavailable upstream usage report and therefore
  -- stores no synthetic token or cost values; reported/estimated usage is complete.
  CHECK (
    (usage_source IS NULL AND input_tokens IS NULL AND output_tokens IS NULL AND cost_micro_usd IS NULL)
    OR (usage_source='unknown' AND input_tokens IS NULL AND output_tokens IS NULL AND cost_micro_usd IS NULL)
    OR (usage_source IN ('upstream','estimated') AND input_tokens IS NOT NULL AND output_tokens IS NOT NULL AND cost_micro_usd IS NOT NULL)
  )
);
CREATE UNIQUE INDEX ai_runs_one_active_student_idx ON ai_runs(student_id) WHERE status IN ('queued','streaming');
CREATE INDEX ai_runs_claim_idx ON ai_runs(status,created_at,id) WHERE status='queued';
CREATE INDEX ai_runs_thread_time_idx ON ai_runs(thread_id,created_at,id);

ALTER TABLE ai_messages
  ADD CONSTRAINT ai_messages_trigger_run_id_fkey FOREIGN KEY(trigger_run_id) REFERENCES ai_runs(id) ON DELETE RESTRICT;
CREATE UNIQUE INDEX ai_messages_one_final_assistant_per_run_idx
  ON ai_messages(trigger_run_id) WHERE role='assistant';
CREATE INDEX ai_messages_thread_time_idx ON ai_messages(thread_id,created_at,id);

CREATE TABLE ai_run_events (
  id bigserial PRIMARY KEY,
  run_id uuid NOT NULL REFERENCES ai_runs(id) ON DELETE RESTRICT,
  sequence bigint NOT NULL CHECK (sequence > 0),
  kind text NOT NULL CHECK (kind IN ('delta','usage','completed','failed','cancelled')),
  payload_text text NOT NULL CHECK (octet_length(payload_text) <= 16384),
  error_code text CHECK (error_code IS NULL OR char_length(error_code) BETWEEN 1 AND 128),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(run_id,sequence)
);
CREATE INDEX ai_run_events_replay_idx ON ai_run_events(run_id,sequence);

CREATE TABLE ai_usage_ledger (
  id bigserial PRIMARY KEY,
  student_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  run_id uuid NOT NULL REFERENCES ai_runs(id) ON DELETE RESTRICT,
  action text NOT NULL CHECK (action IN ('reserve','settle','release')),
  period_kind text NOT NULL CHECK (period_kind IN ('day','month')),
  period_key text NOT NULL,
  request_delta bigint NOT NULL,
  token_delta bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((period_kind='day' AND period_key ~ '^\d{4}-\d{2}-\d{2}$') OR (period_kind='month' AND period_key ~ '^\d{4}-\d{2}$')),
  CHECK (request_delta <> 0 OR token_delta <> 0),
  UNIQUE(run_id,period_kind,action)
);
CREATE INDEX ai_usage_ledger_student_period_idx ON ai_usage_ledger(student_id,period_kind,period_key,created_at,id);

-- +goose StatementBegin
CREATE FUNCTION reject_ai_history_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'AI history is immutable' USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER ai_messages_immutable BEFORE UPDATE OR DELETE ON ai_messages
FOR EACH ROW EXECUTE FUNCTION reject_ai_history_mutation();
CREATE TRIGGER ai_message_files_immutable BEFORE UPDATE OR DELETE ON ai_message_files
FOR EACH ROW EXECUTE FUNCTION reject_ai_history_mutation();
CREATE TRIGGER ai_run_events_immutable BEFORE UPDATE OR DELETE ON ai_run_events
FOR EACH ROW EXECUTE FUNCTION reject_ai_history_mutation();
CREATE TRIGGER ai_usage_ledger_immutable BEFORE UPDATE OR DELETE ON ai_usage_ledger
FOR EACH ROW EXECUTE FUNCTION reject_ai_history_mutation();

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_thread_immutability() RETURNS trigger AS $$
BEGIN
  IF OLD.id IS DISTINCT FROM NEW.id
     OR OLD.student_id IS DISTINCT FROM NEW.student_id
     OR OLD.subject IS DISTINCT FROM NEW.subject
     OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
    RAISE EXCEPTION 'AI thread owner, subject and creation time are immutable' USING ERRCODE = '55000';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER ai_threads_immutable_identity BEFORE UPDATE ON ai_threads
FOR EACH ROW EXECUTE FUNCTION enforce_ai_thread_immutability();

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_message_integrity() RETURNS trigger AS $$
BEGIN
  IF NEW.role='student' THEN
    IF NEW.trigger_run_id IS NOT NULL OR NOT EXISTS (
      SELECT 1 FROM ai_threads t JOIN users u ON u.id=NEW.sender_user_id
        AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
      WHERE t.id=NEW.thread_id AND t.student_id=NEW.sender_user_id
    ) THEN
      RAISE EXCEPTION 'AI student message sender must be the active thread owner' USING ERRCODE = '23514';
    END IF;
  ELSIF NEW.sender_user_id IS NOT NULL OR NEW.idempotency_key IS NOT NULL OR NOT EXISTS (
    SELECT 1 FROM ai_runs r WHERE r.id=NEW.trigger_run_id
      AND r.thread_id=NEW.thread_id AND r.status='succeeded'
  ) THEN
    RAISE EXCEPTION 'AI assistant messages require a succeeded run in the same thread' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER ai_messages_integrity BEFORE INSERT ON ai_messages
FOR EACH ROW EXECUTE FUNCTION enforce_ai_message_integrity();

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_run_final_assistant_message() RETURNS trigger AS $$
DECLARE
  checked_run_id uuid;
  run_status text;
  final_messages integer;
BEGIN
  IF TG_TABLE_NAME='ai_runs' THEN
    checked_run_id := COALESCE(NEW.id,OLD.id);
  ELSE
    checked_run_id := COALESCE(NEW.trigger_run_id,OLD.trigger_run_id);
  END IF;
  IF checked_run_id IS NULL THEN
    RETURN NULL;
  END IF;
  SELECT status INTO run_status FROM ai_runs WHERE id=checked_run_id;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  SELECT count(*) INTO final_messages
  FROM ai_messages WHERE trigger_run_id=checked_run_id AND role='assistant';
  IF (run_status='succeeded' AND final_messages <> 1)
     OR (run_status <> 'succeeded' AND final_messages <> 0) THEN
    RAISE EXCEPTION 'AI succeeded runs require exactly one final assistant message' USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER ai_runs_final_assistant_message
  AFTER INSERT OR UPDATE ON ai_runs DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION enforce_ai_run_final_assistant_message();
CREATE CONSTRAINT TRIGGER ai_messages_final_assistant_message
  AFTER INSERT OR UPDATE OR DELETE ON ai_messages DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION enforce_ai_run_final_assistant_message();

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_message_file_integrity() RETURNS trigger AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM ai_messages m
    JOIN ai_threads t ON t.id=m.thread_id
    JOIN users u ON u.id=t.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
    JOIN file_versions fv ON fv.id=NEW.file_version_id AND fv.purpose='ai_attachment'
      AND fv.processing_state='ready' AND fv.created_by=t.student_id
    JOIN files f ON f.id=fv.file_id AND f.created_by=t.student_id AND f.deleted_at IS NULL
    WHERE m.id=NEW.message_id AND m.role='student'
  ) THEN
    RAISE EXCEPTION 'AI attachments must be ready AI files owned by the active student sender' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER ai_message_files_integrity BEFORE INSERT ON ai_message_files
FOR EACH ROW EXECUTE FUNCTION enforce_ai_message_file_integrity();

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_run_integrity() RETURNS trigger AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM ai_threads t JOIN ai_messages m ON m.id=NEW.trigger_message_id
    WHERE t.id=NEW.thread_id AND t.student_id=NEW.student_id
      AND m.thread_id=NEW.thread_id AND m.role='student' AND m.sender_user_id=NEW.student_id
  ) THEN
    RAISE EXCEPTION 'AI run must belong to its student thread and student trigger message' USING ERRCODE = '23514';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM ai_providers p WHERE p.id=NEW.provider_id
      AND p.base_url=NEW.provider_base_url AND p.protocol_mode=NEW.protocol_mode
  ) THEN
    RAISE EXCEPTION 'AI run provider snapshot must match the referenced provider' USING ERRCODE = '23514';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM prompt_templates p WHERE p.id=NEW.prompt_id
      AND p.subject=NEW.prompt_subject AND p.version=NEW.prompt_version
      AND encode(digest(p.system_prompt,'sha256'),'hex')=NEW.prompt_sha256
  ) THEN
    RAISE EXCEPTION 'AI run prompt snapshot must match the referenced prompt version' USING ERRCODE = '23514';
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM ai_models m WHERE m.id=NEW.model_id AND m.provider_id=NEW.provider_id
      AND m.upstream_model_id=NEW.upstream_model_id AND m.modality=NEW.modality
      AND m.context_window_tokens=NEW.context_window_tokens
      AND m.max_output_tokens=NEW.max_output_tokens
      AND m.image_quota_tokens=NEW.image_quota_tokens
      AND m.input_price_micro_usd_per_million_tokens=NEW.input_price_micro_usd_per_million_tokens
      AND m.output_price_micro_usd_per_million_tokens=NEW.output_price_micro_usd_per_million_tokens
      AND m.connect_timeout_ms=NEW.connect_timeout_ms
      AND m.response_header_timeout_ms=NEW.response_header_timeout_ms
      AND m.idle_stream_timeout_ms=NEW.idle_stream_timeout_ms
      AND m.total_timeout_ms=NEW.total_timeout_ms
  ) THEN
    RAISE EXCEPTION 'AI run model snapshot must match its provider and model' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER ai_runs_insert_integrity BEFORE INSERT ON ai_runs
FOR EACH ROW EXECUTE FUNCTION enforce_ai_run_integrity();

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_usage_ledger_integrity() RETURNS trigger AS $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM ai_runs r WHERE r.id=NEW.run_id AND r.student_id=NEW.student_id
      AND ((NEW.period_kind='day' AND NEW.period_key=r.quota_day_key)
        OR (NEW.period_kind='month' AND NEW.period_key=r.quota_month_key))
  ) THEN
    RAISE EXCEPTION 'AI usage ledger must use the run student and reserved quota period' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER ai_usage_ledger_integrity BEFORE INSERT ON ai_usage_ledger
FOR EACH ROW EXECUTE FUNCTION enforce_ai_usage_ledger_integrity();

-- +goose StatementBegin
CREATE FUNCTION enforce_ai_run_transition() RETURNS trigger AS $$
BEGIN
  IF OLD.id IS DISTINCT FROM NEW.id
     OR OLD.thread_id IS DISTINCT FROM NEW.thread_id
     OR OLD.student_id IS DISTINCT FROM NEW.student_id
     OR OLD.trigger_message_id IS DISTINCT FROM NEW.trigger_message_id
     OR OLD.attempt_no IS DISTINCT FROM NEW.attempt_no
     OR OLD.idempotency_key IS DISTINCT FROM NEW.idempotency_key
     OR OLD.provider_id IS DISTINCT FROM NEW.provider_id
     OR OLD.provider_base_url IS DISTINCT FROM NEW.provider_base_url
     OR OLD.protocol_mode IS DISTINCT FROM NEW.protocol_mode
     OR OLD.model_id IS DISTINCT FROM NEW.model_id
     OR OLD.upstream_model_id IS DISTINCT FROM NEW.upstream_model_id
     OR OLD.modality IS DISTINCT FROM NEW.modality
     OR OLD.context_window_tokens IS DISTINCT FROM NEW.context_window_tokens
     OR OLD.max_output_tokens IS DISTINCT FROM NEW.max_output_tokens
     OR OLD.image_quota_tokens IS DISTINCT FROM NEW.image_quota_tokens
     OR OLD.input_price_micro_usd_per_million_tokens IS DISTINCT FROM NEW.input_price_micro_usd_per_million_tokens
     OR OLD.output_price_micro_usd_per_million_tokens IS DISTINCT FROM NEW.output_price_micro_usd_per_million_tokens
     OR OLD.prompt_id IS DISTINCT FROM NEW.prompt_id
     OR OLD.prompt_subject IS DISTINCT FROM NEW.prompt_subject
     OR OLD.prompt_version IS DISTINCT FROM NEW.prompt_version
     OR OLD.prompt_sha256 IS DISTINCT FROM NEW.prompt_sha256
     OR OLD.connect_timeout_ms IS DISTINCT FROM NEW.connect_timeout_ms
     OR OLD.response_header_timeout_ms IS DISTINCT FROM NEW.response_header_timeout_ms
     OR OLD.idle_stream_timeout_ms IS DISTINCT FROM NEW.idle_stream_timeout_ms
     OR OLD.total_timeout_ms IS DISTINCT FROM NEW.total_timeout_ms
     OR OLD.reserved_request_count IS DISTINCT FROM NEW.reserved_request_count
     OR OLD.reserved_token_count IS DISTINCT FROM NEW.reserved_token_count
     OR OLD.quota_day_key IS DISTINCT FROM NEW.quota_day_key
     OR OLD.quota_month_key IS DISTINCT FROM NEW.quota_month_key
     OR OLD.estimator_version IS DISTINCT FROM NEW.estimator_version
     OR OLD.created_at IS DISTINCT FROM NEW.created_at THEN
    RAISE EXCEPTION 'AI run identity and snapshot fields are immutable' USING ERRCODE = '55000';
  END IF;

  IF OLD.status IN ('succeeded','failed','cancelled') THEN
    RAISE EXCEPTION 'terminal AI runs cannot transition' USING ERRCODE = '55000';
  END IF;
  IF (OLD.status='queued' AND NEW.status NOT IN ('queued','streaming','failed','cancelled'))
     OR (OLD.status='streaming' AND NEW.status NOT IN ('streaming','succeeded','failed','cancelled')) THEN
    RAISE EXCEPTION 'invalid AI run status transition' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER ai_runs_transition BEFORE UPDATE ON ai_runs
FOR EACH ROW EXECUTE FUNCTION enforce_ai_run_transition();

-- +goose StatementBegin
DO $$
DECLARE constraint_name text;
BEGIN
  FOR constraint_name IN
    SELECT conname FROM pg_constraint
    WHERE conrelid='upload_sessions'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%purpose%'
  LOOP
    EXECUTE format('ALTER TABLE upload_sessions DROP CONSTRAINT %I', constraint_name);
  END LOOP;
  FOR constraint_name IN
    SELECT conname FROM pg_constraint
    WHERE conrelid='file_versions'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%purpose%'
  LOOP
    EXECUTE format('ALTER TABLE file_versions DROP CONSTRAINT %I', constraint_name);
  END LOOP;
  FOR constraint_name IN
    SELECT conname FROM pg_constraint
    WHERE conrelid='file_previews'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%preview_kind%'
  LOOP
    EXECUTE format('ALTER TABLE file_previews DROP CONSTRAINT %I', constraint_name);
  END LOOP;
END;
$$;
-- +goose StatementEnd
ALTER TABLE upload_sessions ADD CONSTRAINT upload_sessions_purpose_check
  CHECK (purpose IN ('teaching','qa_attachment','ai_attachment'));
ALTER TABLE file_versions ADD CONSTRAINT file_versions_purpose_check
  CHECK (purpose IN ('teaching','qa_attachment','ai_attachment'));
ALTER TABLE file_previews ADD CONSTRAINT file_previews_preview_kind_check
  CHECK (preview_kind IN ('pdf','page','thumbnail','poster','ai_text'));

ALTER TABLE file_access_logs
  ADD COLUMN ai_message_id uuid REFERENCES ai_messages(id) ON DELETE RESTRICT;
ALTER TABLE file_access_logs DROP CONSTRAINT file_access_logs_single_business_target;
ALTER TABLE file_access_logs ADD CONSTRAINT file_access_logs_single_business_target CHECK (
  num_nonnulls(lesson_revision_id,qa_message_id,ai_message_id) <= 1
);
CREATE UNIQUE INDEX file_access_logs_ai_playback_sample_key
  ON file_access_logs(actor_user_id,requested_file_version_id,ai_message_id,access_policy,playback_session_hash)
  WHERE result='allow' AND ai_message_id IS NOT NULL AND playback_session_hash<>'';

-- +goose Down
LOCK TABLE file_access_logs IN ACCESS EXCLUSIVE MODE;

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM file_access_logs WHERE ai_message_id IS NOT NULL) THEN
    RAISE EXCEPTION 'AI file access history cannot be represented before migration 00016' USING ERRCODE = '55000';
  END IF;
  IF EXISTS (SELECT 1 FROM upload_sessions WHERE purpose='ai_attachment')
     OR EXISTS (SELECT 1 FROM file_versions WHERE purpose='ai_attachment')
     OR EXISTS (SELECT 1 FROM file_previews WHERE preview_kind='ai_text') THEN
    RAISE EXCEPTION 'AI file artifacts cannot be represented before migration 00016' USING ERRCODE = '55000';
  END IF;
END;
$$;
-- +goose StatementEnd

DROP INDEX file_access_logs_ai_playback_sample_key;
ALTER TABLE file_access_logs DROP CONSTRAINT file_access_logs_single_business_target;
ALTER TABLE file_access_logs DROP COLUMN ai_message_id;
ALTER TABLE file_access_logs ADD CONSTRAINT file_access_logs_single_business_target CHECK (
  NOT (lesson_revision_id IS NOT NULL AND qa_message_id IS NOT NULL)
);

ALTER TABLE file_previews DROP CONSTRAINT file_previews_preview_kind_check;
ALTER TABLE file_previews ADD CONSTRAINT file_previews_preview_kind_check
  CHECK (preview_kind IN ('pdf','page','thumbnail','poster'));
ALTER TABLE file_versions DROP CONSTRAINT file_versions_purpose_check;
ALTER TABLE file_versions ADD CONSTRAINT file_versions_purpose_check
  CHECK (purpose IN ('teaching','qa_attachment'));
ALTER TABLE upload_sessions DROP CONSTRAINT upload_sessions_purpose_check;
ALTER TABLE upload_sessions ADD CONSTRAINT upload_sessions_purpose_check
  CHECK (purpose IN ('teaching','qa_attachment'));

DROP TRIGGER IF EXISTS ai_runs_transition ON ai_runs;
DROP FUNCTION IF EXISTS enforce_ai_run_transition();
DROP TRIGGER IF EXISTS ai_usage_ledger_integrity ON ai_usage_ledger;
DROP FUNCTION IF EXISTS enforce_ai_usage_ledger_integrity();
DROP TRIGGER ai_runs_insert_integrity ON ai_runs;
DROP FUNCTION enforce_ai_run_integrity();
DROP TRIGGER IF EXISTS ai_messages_final_assistant_message ON ai_messages;
DROP TRIGGER IF EXISTS ai_runs_final_assistant_message ON ai_runs;
DROP FUNCTION IF EXISTS enforce_ai_run_final_assistant_message();
DROP TRIGGER ai_message_files_integrity ON ai_message_files;
DROP FUNCTION enforce_ai_message_file_integrity();
DROP TRIGGER ai_messages_integrity ON ai_messages;
DROP FUNCTION enforce_ai_message_integrity();
DROP TRIGGER ai_threads_immutable_identity ON ai_threads;
DROP FUNCTION enforce_ai_thread_immutability();
DROP TRIGGER ai_usage_ledger_immutable ON ai_usage_ledger;
DROP TRIGGER ai_run_events_immutable ON ai_run_events;
DROP TRIGGER ai_message_files_immutable ON ai_message_files;
DROP TRIGGER ai_messages_immutable ON ai_messages;
DROP FUNCTION reject_ai_history_mutation();
DROP INDEX ai_usage_ledger_student_period_idx;
DROP TABLE ai_usage_ledger;
DROP INDEX ai_run_events_replay_idx;
DROP TABLE ai_run_events;
DROP INDEX ai_messages_one_final_assistant_per_run_idx;
DROP INDEX ai_messages_thread_time_idx;
ALTER TABLE ai_messages DROP CONSTRAINT ai_messages_trigger_run_id_fkey;
DROP INDEX ai_runs_thread_time_idx;
DROP INDEX ai_runs_claim_idx;
DROP INDEX ai_runs_one_active_student_idx;
DROP TABLE ai_runs;
DROP TABLE ai_message_files;
DROP TABLE ai_messages;
DROP TABLE ai_threads;
