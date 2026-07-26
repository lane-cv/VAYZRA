package aiqa

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/audit"
	"sync"
	"time"
)

type PostgresConfigStore struct {
	pool   *pgxpool.Pool
	box    SecretBox
	policy URLPolicy
}

func NewPostgresConfigStore(p *pgxpool.Pool) *PostgresConfigStore {
	return &PostgresConfigStore{pool: p}
}
func NewPostgresConfigStoreWithSecurity(p *pgxpool.Pool, box SecretBox, policy URLPolicy) *PostgresConfigStore {
	return &PostgresConfigStore{pool: p, box: box, policy: policy}
}
func (s *PostgresConfigStore) ListProviders(ctx context.Context) ([]ProviderView, error) {
	rows, e := s.pool.Query(ctx, `SELECT id,name,base_url,protocol_mode,active,key_updated_at,version FROM ai_providers ORDER BY created_at`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ProviderView{}
	for rows.Next() {
		var v ProviderView
		if e = rows.Scan(&v.ID, &v.Name, &v.BaseURL, &v.ProtocolMode, &v.Active, &v.KeyUpdatedAt, &v.Version); e != nil {
			return nil, e
		}
		v.HasKey = true
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PostgresConfigStore) CreateProvider(ctx context.Context, p Principal, id uuid.UUID, in CreateProviderInput, sec EncryptedSecret, requestHash [32]byte) (out ProviderView, err error) {
	out.ID = id
	err = s.tx(ctx, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `INSERT INTO ai_config_idempotency(key,operation,request_hash,provider_id,created_by) VALUES($1,'create_provider',$2,$3,$4) ON CONFLICT(key) DO NOTHING`, in.IdempotencyKey, requestHash[:], id, p.User.ID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			var storedHash []byte
			if e = tx.QueryRow(ctx, `SELECT request_hash,provider_id FROM ai_config_idempotency WHERE key=$1`, in.IdempotencyKey).Scan(&storedHash, &out.ID); e != nil {
				return e
			}
			if len(storedHash) != len(requestHash) || subtle.ConstantTimeCompare(storedHash, requestHash[:]) != 1 || s.box == nil {
				return ErrConfigConflict
			}
			var storedSecret EncryptedSecret
			e = tx.QueryRow(ctx, `SELECT id,name,base_url,protocol_mode,active,key_updated_at,version,encrypted_api_key,key_version FROM ai_providers WHERE id=$1`, out.ID).Scan(&out.ID, &out.Name, &out.BaseURL, &out.ProtocolMode, &out.Active, &out.KeyUpdatedAt, &out.Version, &storedSecret.Blob, &storedSecret.KeyVersion)
			if e != nil {
				return e
			}
			existingKey, openExistingErr := s.box.Open(out.ID, storedSecret)
			if openExistingErr != nil {
				return ErrConfigConflict
			}
			defer zeroBytes(existingKey)
			incomingKey, openIncomingErr := s.box.Open(id, sec)
			if openIncomingErr != nil {
				return ErrConfigConflict
			}
			defer zeroBytes(incomingKey)
			if len(existingKey) != len(incomingKey) || subtle.ConstantTimeCompare(existingKey, incomingKey) != 1 {
				return ErrConfigConflict
			}
			out.HasKey = e == nil
			return e
		}
		e = tx.QueryRow(ctx, `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,name,base_url,protocol_mode,active,key_updated_at,version`, out.ID, in.Name, in.BaseURL, in.ProtocolMode, sec.Blob, sec.KeyVersion, time.Now().UTC(), p.User.ID).Scan(&out.ID, &out.Name, &out.BaseURL, &out.ProtocolMode, &out.Active, &out.KeyUpdatedAt, &out.Version)
		if e != nil {
			return e
		}
		out.HasKey = true
		return writeAudit(ctx, tx, p, "ai.provider_created", "ai_provider", out.ID.String(), map[string]any{})
	})
	return
}
func (s *PostgresConfigStore) ActiveRuntimeConfig(ctx context.Context) (RuntimeProviderConfig, error) {
	return s.ForRun(ctx, SubjectMath, ModalityText)
}

func (s *PostgresConfigStore) AcquireProviderTest(ctx context.Context, id uuid.UUID) (out RuntimeProviderConfig, release func(), err error) {
	if s.box == nil || id == uuid.Nil {
		return RuntimeProviderConfig{}, nil, ErrProviderUnavailable
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return RuntimeProviderConfig{}, nil, err
	}
	releaseConn := true
	defer func() {
		if releaseConn {
			conn.Release()
		}
	}()

	var rawBaseURL string
	var encrypted []byte
	var keyVersion int16
	err = conn.QueryRow(ctx, `SELECT id,base_url,protocol_mode,encrypted_api_key,key_version FROM ai_providers WHERE id=$1`, id).
		Scan(&out.ProviderID, &rawBaseURL, &out.ProtocolMode, &encrypted, &keyVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeProviderConfig{}, nil, ErrNotFound
	}
	if err != nil {
		return RuntimeProviderConfig{}, nil, err
	}

	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "ai-provider-test:"+id.String()).Scan(&locked); err != nil {
		return RuntimeProviderConfig{}, nil, err
	}
	if !locked {
		return out, nil, ErrProviderTestBusy
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			if unlockErr := conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "ai-provider-test:"+id.String()).Scan(&unlocked); unlockErr != nil || !unlocked {
				raw := conn.Hijack()
				_ = raw.Close(unlockCtx)
				return
			}
			conn.Release()
		})
	}
	releaseConn = false
	fail := func(e error) (RuntimeProviderConfig, func(), error) {
		release()
		return out, nil, e
	}

	var timeoutConnect, timeoutHeaders, timeoutIdle, timeoutTotal int
	err = conn.QueryRow(ctx, `SELECT id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,enabled,quota_blocked_at,coalesce(quota_block_reason,''),version,connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms
		FROM ai_models WHERE provider_id=$1 AND modality='text' AND enabled AND quota_blocked_at IS NULL ORDER BY upstream_model_id,id LIMIT 1`, id).
		Scan(&out.Model.ID, &out.Model.ProviderID, &out.Model.UpstreamModelID, &out.Model.Modality, &out.Model.ContextTokens, &out.Model.MaxOutputTokens, &out.Model.ImageQuotaTokens, &out.Model.InputPriceMicroUSD, &out.Model.OutputPriceMicroUSD, &out.Model.Enabled, &out.Model.QuotaBlockedAt, &out.Model.QuotaBlockReason, &out.Model.Version, &timeoutConnect, &timeoutHeaders, &timeoutIdle, &timeoutTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(ErrProviderUnavailable)
	}
	if err != nil {
		return fail(err)
	}
	baseURL, err := s.policy.NormalizeBaseURL(ctx, rawBaseURL)
	if err != nil {
		return fail(ErrProviderUnavailable)
	}
	out.BaseURL = baseURL
	out.Timeouts = GatewayTimeouts{
		Connect: time.Duration(timeoutConnect) * time.Millisecond, ResponseHeader: time.Duration(timeoutHeaders) * time.Millisecond,
		IdleStream: time.Duration(timeoutIdle) * time.Millisecond, Total: time.Duration(timeoutTotal) * time.Millisecond,
	}
	key, err := s.box.Open(id, EncryptedSecret{KeyVersion: keyVersion, Blob: encrypted})
	if err != nil {
		return fail(ErrProviderUnavailable)
	}
	out.APIKey = key
	return out, release, nil
}

func (s *PostgresConfigStore) RecordProviderTest(ctx context.Context, p Principal, result providerTestAudit) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		return writeAudit(ctx, tx, p, "ai.provider_tested", "ai_provider", result.providerID.String(), map[string]any{
			"providerId":    result.providerID.String(),
			"protocol":      string(result.protocol),
			"ok":            fmt.Sprint(result.ok),
			"errorCategory": result.category,
			"latencyMs":     fmt.Sprint(result.latencyMS),
		})
	})
}

func (s *PostgresConfigStore) ForRun(ctx context.Context, subject Subject, modality Modality) (out RuntimeProviderConfig, err error) {
	if s.box == nil || !subjectOK(subject) || !modalityOK(modality) {
		return RuntimeProviderConfig{}, ErrAIDisabled
	}
	var encrypted []byte
	var keyVersion int16
	var timeoutConnect, timeoutHeaders, timeoutIdle, timeoutTotal int
	var rawBaseURL string
	err = s.pool.QueryRow(ctx, `SELECT p.id,p.base_url,p.protocol_mode,p.encrypted_api_key,p.key_version,m.id,m.provider_id,m.upstream_model_id,m.modality,m.context_window_tokens,m.max_output_tokens,m.image_quota_tokens,m.input_price_micro_usd_per_million_tokens,m.output_price_micro_usd_per_million_tokens,m.enabled,m.quota_blocked_at,coalesce(m.quota_block_reason,''),m.version,m.connect_timeout_ms,m.response_header_timeout_ms,m.idle_stream_timeout_ms,m.total_timeout_ms,t.id,t.subject,t.version,t.system_prompt,t.active FROM ai_providers p JOIN ai_models m ON m.provider_id=p.id AND m.modality=$1 AND m.enabled AND m.quota_blocked_at IS NULL JOIN prompt_templates t ON t.subject=$2 AND t.active WHERE p.active AND (SELECT count(*) FROM ai_models mc WHERE mc.provider_id=p.id AND mc.modality=$1 AND mc.enabled AND mc.quota_blocked_at IS NULL)=1`, modality, subject).Scan(&out.ProviderID, &rawBaseURL, &out.ProtocolMode, &encrypted, &keyVersion, &out.Model.ID, &out.Model.ProviderID, &out.Model.UpstreamModelID, &out.Model.Modality, &out.Model.ContextTokens, &out.Model.MaxOutputTokens, &out.Model.ImageQuotaTokens, &out.Model.InputPriceMicroUSD, &out.Model.OutputPriceMicroUSD, &out.Model.Enabled, &out.Model.QuotaBlockedAt, &out.Model.QuotaBlockReason, &out.Model.Version, &timeoutConnect, &timeoutHeaders, &timeoutIdle, &timeoutTotal, &out.Prompt.ID, &out.Prompt.Subject, &out.Prompt.Version, &out.Prompt.Body, &out.Prompt.Active)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RuntimeProviderConfig{}, ErrAIDisabled
		}
		return RuntimeProviderConfig{}, err
	}
	u, err := s.policy.NormalizeBaseURL(ctx, rawBaseURL)
	if err != nil {
		return RuntimeProviderConfig{}, ErrAIDisabled
	}
	out.BaseURL = u
	out.KeyVersion = keyVersion
	key, err := s.box.Open(out.ProviderID, EncryptedSecret{KeyVersion: keyVersion, Blob: encrypted})
	if err != nil {
		return RuntimeProviderConfig{}, ErrAIDisabled
	}
	out.APIKey = append([]byte(nil), key...)
	out.Timeouts = GatewayTimeouts{Connect: time.Duration(timeoutConnect) * time.Millisecond, ResponseHeader: time.Duration(timeoutHeaders) * time.Millisecond, IdleStream: time.Duration(timeoutIdle) * time.Millisecond, Total: time.Duration(timeoutTotal) * time.Millisecond}
	return out, nil
}
func (s *PostgresConfigStore) UpdateProvider(ctx context.Context, p Principal, in UpdateProviderInput, sec *EncryptedSecret) (out ProviderView, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		q := `UPDATE ai_providers SET name=$2,base_url=$3,protocol_mode=$4,version=version+1,updated_at=now()`
		a := []any{in.ID, in.Name, in.BaseURL, in.ProtocolMode}
		if sec != nil {
			q += `,encrypted_api_key=$5,key_version=$6,key_updated_at=now()`
			a = append(a, sec.Blob, sec.KeyVersion)
		}
		q += ` WHERE id=$1 AND version=$` + fmt.Sprint(len(a)+1) + ` RETURNING id,name,base_url,protocol_mode,active,key_updated_at,version`
		a = append(a, in.ExpectedVersion)
		e := tx.QueryRow(ctx, q, a...).Scan(&out.ID, &out.Name, &out.BaseURL, &out.ProtocolMode, &out.Active, &out.KeyUpdatedAt, &out.Version)
		if e != nil {
			return configErr(e)
		}
		out.HasKey = true
		return writeAudit(ctx, tx, p, "ai.provider_updated", "ai_provider", in.ID.String(), map[string]any{"keyChanged": fmt.Sprint(sec != nil)})
	})
	return
}
func (s *PostgresConfigStore) ActivateProvider(ctx context.Context, p Principal, id uuid.UUID, v int64) (out ProviderView, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT 1 FROM ai_providers FOR UPDATE`); e != nil {
			return e
		}
		var version int64
		e := tx.QueryRow(ctx, `SELECT version FROM ai_providers WHERE id=$1`, id).Scan(&version)
		if e != nil {
			return configErr(e)
		}
		if version != v {
			return ErrConfigConflict
		}
		var textModels, visionModels int
		if e = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE modality='text'),count(*) FILTER (WHERE modality='vision') FROM ai_models WHERE provider_id=$1 AND enabled AND quota_blocked_at IS NULL`, id).Scan(&textModels, &visionModels); e != nil {
			return e
		}
		if textModels != 1 || visionModels != 1 {
			return ErrAIDisabled
		}
		var mathPrompts, physicsPrompts int
		if e = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE subject='math'),count(*) FILTER (WHERE subject='physics') FROM prompt_templates WHERE active`).Scan(&mathPrompts, &physicsPrompts); e != nil {
			return e
		}
		if mathPrompts != 1 || physicsPrompts != 1 {
			return ErrAIDisabled
		}
		if _, e = tx.Exec(ctx, `UPDATE ai_providers SET active=false,updated_at=now() WHERE active`); e != nil {
			return e
		}
		e = tx.QueryRow(ctx, `UPDATE ai_providers SET active=true,version=version+1,updated_at=now() WHERE id=$1 RETURNING id,name,base_url,protocol_mode,active,key_updated_at,version`, id).Scan(&out.ID, &out.Name, &out.BaseURL, &out.ProtocolMode, &out.Active, &out.KeyUpdatedAt, &out.Version)
		if e != nil {
			return configErr(e)
		}
		out.HasKey = true
		return writeAudit(ctx, tx, p, "ai.provider_activated", "ai_provider", id.String(), map[string]any{})
	})
	return
}
func (s *PostgresConfigStore) ListModels(ctx context.Context, id uuid.UUID) (out []ModelView, err error) {
	rows, e := s.pool.Query(ctx, `SELECT id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,enabled,quota_blocked_at,coalesce(quota_block_reason,''),version FROM ai_models WHERE provider_id=$1 ORDER BY upstream_model_id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var v ModelView
		if e = rows.Scan(&v.ID, &v.ProviderID, &v.UpstreamModelID, &v.Modality, &v.ContextTokens, &v.MaxOutputTokens, &v.ImageQuotaTokens, &v.InputPriceMicroUSD, &v.OutputPriceMicroUSD, &v.ConnectTimeoutMS, &v.ResponseHeaderTimeoutMS, &v.IdleStreamTimeoutMS, &v.TotalTimeoutMS, &v.Enabled, &v.QuotaBlockedAt, &v.QuotaBlockReason, &v.Version); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PostgresConfigStore) PutModel(ctx context.Context, p Principal, in PutModelInput) (out ModelView, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var providerActive bool
		if e := tx.QueryRow(ctx, `SELECT active FROM ai_providers WHERE id=$1 FOR UPDATE`, in.ProviderID).Scan(&providerActive); e != nil {
			return configErr(e)
		}
		if in.ExpectedVersion == 0 {
			e := tx.QueryRow(ctx, `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,enabled,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15) RETURNING id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,enabled,quota_blocked_at,coalesce(quota_block_reason,''),version`, in.ID, in.ProviderID, in.UpstreamModelID, in.Modality, in.ContextTokens, in.MaxOutputTokens, in.ImageQuotaTokens, in.InputPriceMicroUSD, in.OutputPriceMicroUSD, in.ConnectTimeoutMS, in.ResponseHeaderTimeoutMS, in.IdleStreamTimeoutMS, in.TotalTimeoutMS, in.Enabled, p.User.ID).Scan(&out.ID, &out.ProviderID, &out.UpstreamModelID, &out.Modality, &out.ContextTokens, &out.MaxOutputTokens, &out.ImageQuotaTokens, &out.InputPriceMicroUSD, &out.OutputPriceMicroUSD, &out.ConnectTimeoutMS, &out.ResponseHeaderTimeoutMS, &out.IdleStreamTimeoutMS, &out.TotalTimeoutMS, &out.Enabled, &out.QuotaBlockedAt, &out.QuotaBlockReason, &out.Version)
			if e != nil {
				return configErr(e)
			}
		} else {
			var oldContext, oldOutput, oldQuota int64
			var blocked *time.Time
			e := tx.QueryRow(ctx, `SELECT context_window_tokens,max_output_tokens,image_quota_tokens,quota_blocked_at FROM ai_models WHERE id=$1 AND provider_id=$2 AND version=$3 FOR UPDATE`, in.ID, in.ProviderID, in.ExpectedVersion).Scan(&oldContext, &oldOutput, &oldQuota, &blocked)
			if e != nil {
				return configErr(e)
			}
			if in.ClearQuotaBlock && blocked != nil && oldContext == in.ContextTokens && oldOutput == in.MaxOutputTokens && oldQuota == in.ImageQuotaTokens {
				return ErrInvalidInput
			}
			clear := in.ClearQuotaBlock
			e = tx.QueryRow(ctx, `UPDATE ai_models SET upstream_model_id=$3,modality=$4,context_window_tokens=$5,max_output_tokens=$6,image_quota_tokens=$7,input_price_micro_usd_per_million_tokens=$8,output_price_micro_usd_per_million_tokens=$9,connect_timeout_ms=$10,response_header_timeout_ms=$11,idle_stream_timeout_ms=$12,total_timeout_ms=$13,enabled=$14,quota_blocked_at=CASE WHEN $15 THEN NULL ELSE quota_blocked_at END,quota_block_reason=CASE WHEN $15 THEN NULL ELSE quota_block_reason END,updated_by=$16,updated_at=now(),version=version+1 WHERE id=$1 AND provider_id=$2 AND version=$17 RETURNING id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,enabled,quota_blocked_at,coalesce(quota_block_reason,''),version`, in.ID, in.ProviderID, in.UpstreamModelID, in.Modality, in.ContextTokens, in.MaxOutputTokens, in.ImageQuotaTokens, in.InputPriceMicroUSD, in.OutputPriceMicroUSD, in.ConnectTimeoutMS, in.ResponseHeaderTimeoutMS, in.IdleStreamTimeoutMS, in.TotalTimeoutMS, in.Enabled, clear, p.User.ID, in.ExpectedVersion).Scan(&out.ID, &out.ProviderID, &out.UpstreamModelID, &out.Modality, &out.ContextTokens, &out.MaxOutputTokens, &out.ImageQuotaTokens, &out.InputPriceMicroUSD, &out.OutputPriceMicroUSD, &out.ConnectTimeoutMS, &out.ResponseHeaderTimeoutMS, &out.IdleStreamTimeoutMS, &out.TotalTimeoutMS, &out.Enabled, &out.QuotaBlockedAt, &out.QuotaBlockReason, &out.Version)
			if e != nil {
				return configErr(e)
			}
		}
		if providerActive {
			var textModels, visionModels int
			if e := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE modality='text'),count(*) FILTER (WHERE modality='vision') FROM ai_models WHERE provider_id=$1 AND enabled AND quota_blocked_at IS NULL`, in.ProviderID).Scan(&textModels, &visionModels); e != nil {
				return e
			}
			if textModels != 1 || visionModels != 1 {
				return ErrInvalidInput
			}
		}
		return writeAudit(ctx, tx, p, "ai.model_put", "ai_model", in.ID.String(), map[string]any{"providerId": in.ProviderID.String(), "modality": string(in.Modality)})
	})
	return
}
func (s *PostgresConfigStore) ListPrompts(ctx context.Context) (out []PromptView, err error) {
	rows, e := s.pool.Query(ctx, `SELECT id,subject,version,system_prompt,active FROM prompt_templates ORDER BY subject,version DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var v PromptView
		if e = rows.Scan(&v.ID, &v.Subject, &v.Version, &v.Body, &v.Active); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PostgresConfigStore) PutPrompt(ctx context.Context, p Principal, in PutPromptInput) (out PromptView, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0::bigint))`, string(in.Subject)); e != nil {
			return e
		}
		var activeVersion int64
		e := tx.QueryRow(ctx, `SELECT version FROM prompt_templates WHERE subject=$1 AND active FOR UPDATE`, in.Subject).Scan(&activeVersion)
		if errors.Is(e, pgx.ErrNoRows) {
			if in.ExpectedVersion != 0 {
				return ErrConfigConflict
			}
			activeVersion = 0
		} else if e != nil {
			return e
		} else if activeVersion != in.ExpectedVersion {
			return ErrConfigConflict
		}
		ver := activeVersion + 1
		if _, e = tx.Exec(ctx, `UPDATE prompt_templates SET active=false WHERE subject=$1 AND active`, in.Subject); e != nil {
			return e
		}
		e = tx.QueryRow(ctx, `INSERT INTO prompt_templates(subject,version,system_prompt,active,created_by) VALUES($1,$2,$3,true,$4) RETURNING id,subject,version,system_prompt,active`, in.Subject, ver, in.Body, p.User.ID).Scan(&out.ID, &out.Subject, &out.Version, &out.Body, &out.Active)
		if e != nil {
			return e
		}
		return writeAudit(ctx, tx, p, "ai.prompt_put", "ai_prompt", out.ID.String(), map[string]any{"subject": string(in.Subject), "version": fmt.Sprint(ver)})
	})
	return
}
func (s *PostgresConfigStore) GetLimits(ctx context.Context) (out LimitViews, err error) {
	var g [4]int64
	err = s.pool.QueryRow(ctx, `SELECT daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit,version FROM ai_global_limits`).Scan(&g[0], &g[1], &g[2], &g[3], &out.Global.Version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LimitViews{}, ErrAIDisabled
		}
		return
	}
	out.Global = globalLimitView(g, out.Global.Version)
	out.Students = map[uuid.UUID]LimitView{}
	rows, e := s.pool.Query(ctx, `SELECT student_id,daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit,version FROM student_ai_limits`)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var a, b, c, d *int64
		var v int64
		if e = rows.Scan(&id, &a, &b, &c, &d, &v); e != nil {
			return out, e
		}
		out.Students[id] = studentLimitView([4]*int64{a, b, c, d}, v)
	}
	err = rows.Err()
	return
}
func (s *PostgresConfigStore) PutGlobalLimits(ctx context.Context, p Principal, in PutLimitsInput) (out LimitView, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		a := limitArgs(in)
		var g [4]int64
		e := tx.QueryRow(ctx, `UPDATE ai_global_limits SET daily_request_limit=$1,monthly_request_limit=$2,daily_token_limit=$3,monthly_token_limit=$4,updated_by=$5,updated_at=now(),version=version+1 WHERE singleton AND version=$6 RETURNING daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit,version`, a[0], a[1], a[2], a[3], p.User.ID, in.ExpectedVersion).Scan(&g[0], &g[1], &g[2], &g[3], &out.Version)
		if e != nil {
			return configErr(e)
		}
		out = globalLimitView(g, out.Version)
		return writeAudit(ctx, tx, p, "ai.limits_global_put", "ai_limits", "global", map[string]any{})
	})
	return
}
func (s *PostgresConfigStore) PutStudentLimits(ctx context.Context, p Principal, id uuid.UUID, in PutLimitsInput) (out LimitView, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		var current int64
		e := tx.QueryRow(ctx, `SELECT version FROM student_ai_limits WHERE student_id=$1 FOR UPDATE`, id).Scan(&current)
		if errors.Is(e, pgx.ErrNoRows) {
			if in.ExpectedVersion != 0 {
				return ErrConfigConflict
			}
			a := limitArgs(in)
			var vals [4]*int64
			e = tx.QueryRow(ctx, `INSERT INTO student_ai_limits(student_id,daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit,updated_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit,version`, id, a[0], a[1], a[2], a[3], p.User.ID).Scan(&vals[0], &vals[1], &vals[2], &vals[3], &out.Version)
			if e != nil {
				return e
			}
			out = studentLimitView(vals, out.Version)
		} else {
			if e != nil {
				return e
			}
			if current != in.ExpectedVersion {
				return ErrConfigConflict
			}
			a := limitArgs(in)
			var vals [4]*int64
			e = tx.QueryRow(ctx, `UPDATE student_ai_limits SET daily_request_limit=$2,monthly_request_limit=$3,daily_token_limit=$4,monthly_token_limit=$5,updated_by=$6,updated_at=now(),version=version+1 WHERE student_id=$1 AND version=$7 RETURNING daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit,version`, id, a[0], a[1], a[2], a[3], p.User.ID, in.ExpectedVersion).Scan(&vals[0], &vals[1], &vals[2], &vals[3], &out.Version)
			if e != nil {
				return configErr(e)
			}
			out = studentLimitView(vals, out.Version)
		}
		return writeAudit(ctx, tx, p, "ai.limits_student_put", "ai_limits", id.String(), map[string]any{"studentId": id.String()})
	})
	return
}
func (s *PostgresConfigStore) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func writeAudit(ctx context.Context, tx pgx.Tx, p Principal, a, t, id string, m map[string]any) error {
	return audit.NewPostgresWriter(tx).Write(ctx, audit.Event{ActorUserID: p.User.ID, Action: a, TargetType: t, TargetID: id, Metadata: m, RequestID: p.RequestID, IP: p.IP})
}
func configErr(e error) error {
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrConfigConflict
	}
	var pgErr *pgconn.PgError
	if errors.As(e, &pgErr) && pgErr.Code == "23505" {
		return ErrConfigConflict
	}
	return e
}
func limitArgs(in PutLimitsInput) [4]any {
	return [4]any{limitNumber(in.DailyRequests), limitNumber(in.MonthlyRequests), limitNumber(in.DailyTokens), limitNumber(in.MonthlyTokens)}
}
func limitNumber(v LimitValue) any {
	if v.Mode == "inherit" {
		return nil
	}
	if v.Mode == "disabled" {
		return int64(0)
	}
	return *v.Value
}
func globalLimitView(v [4]int64, ver int64) LimitView {
	return LimitView{DailyRequests: numberLimit(v[0]), MonthlyRequests: numberLimit(v[1]), DailyTokens: numberLimit(v[2]), MonthlyTokens: numberLimit(v[3]), Version: ver}
}
func studentLimitView(v [4]*int64, ver int64) LimitView {
	return LimitView{DailyRequests: nullableLimit(v[0]), MonthlyRequests: nullableLimit(v[1]), DailyTokens: nullableLimit(v[2]), MonthlyTokens: nullableLimit(v[3]), Version: ver}
}
func numberLimit(v int64) LimitValue {
	if v == 0 {
		return LimitValue{Mode: "disabled"}
	}
	return LimitValue{Mode: "limit", Value: &v}
}
func nullableLimit(v *int64) LimitValue {
	if v == nil {
		return LimitValue{Mode: "inherit"}
	}
	return numberLimit(*v)
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
