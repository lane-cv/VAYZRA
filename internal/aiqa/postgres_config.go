package aiqa

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/audit"
	"time"
)

type PostgresConfigStore struct{ pool *pgxpool.Pool }

func NewPostgresConfigStore(p *pgxpool.Pool) *PostgresConfigStore { return &PostgresConfigStore{p} }
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
func (s *PostgresConfigStore) CreateProvider(ctx context.Context, p Principal, in CreateProviderInput, sec EncryptedSecret) (out ProviderView, err error) {
	out.ID = uuid.New()
	err = s.tx(ctx, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,name,base_url,protocol_mode,active,key_updated_at,version`, out.ID, in.Name, in.BaseURL, in.ProtocolMode, sec.Blob, sec.KeyVersion, time.Now().UTC(), p.User.ID).Scan(&out.ID, &out.Name, &out.BaseURL, &out.ProtocolMode, &out.Active, &out.KeyUpdatedAt, &out.Version)
		if e != nil {
			return e
		}
		out.HasKey = true
		return writeAudit(ctx, tx, p, "ai.provider_created", "ai_provider", out.ID.String(), map[string]any{})
	})
	return
}
func (s *PostgresConfigStore) ActiveRuntimeConfig(context.Context) (RuntimeProviderConfig, error) {
	return RuntimeProviderConfig{}, ErrAIDisabled
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
		var n int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM ai_models WHERE provider_id=$1 AND enabled AND modality IN ('text','vision')`, id).Scan(&n); e != nil {
			return e
		}
		if n != 2 {
			return ErrAIDisabled
		}
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM prompt_templates WHERE active AND subject IN ('math','physics')`).Scan(&n); e != nil {
			return e
		}
		if n != 2 {
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
	rows, e := s.pool.Query(ctx, `SELECT id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,enabled,quota_blocked_at,coalesce(quota_block_reason,''),version FROM ai_models WHERE provider_id=$1 ORDER BY upstream_model_id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var v ModelView
		if e = rows.Scan(&v.ID, &v.ProviderID, &v.UpstreamModelID, &v.Modality, &v.ContextTokens, &v.MaxOutputTokens, &v.ImageQuotaTokens, &v.InputPriceMicroUSD, &v.OutputPriceMicroUSD, &v.Enabled, &v.QuotaBlockedAt, &v.QuotaBlockReason, &v.Version); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PostgresConfigStore) PutModel(ctx context.Context, p Principal, in PutModelInput) (out ModelView, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		q := `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,enabled,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11) ON CONFLICT(id) DO UPDATE SET upstream_model_id=excluded.upstream_model_id,modality=excluded.modality,context_window_tokens=excluded.context_window_tokens,max_output_tokens=excluded.max_output_tokens,image_quota_tokens=excluded.image_quota_tokens,input_price_micro_usd_per_million_tokens=excluded.input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens=excluded.output_price_micro_usd_per_million_tokens,enabled=excluded.enabled,updated_by=excluded.updated_by,updated_at=now(),version=ai_models.version+1 RETURNING id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,enabled,quota_blocked_at,coalesce(quota_block_reason,''),version`
		e := tx.QueryRow(ctx, q, in.ID, in.ProviderID, in.UpstreamModelID, in.Modality, in.ContextTokens, in.MaxOutputTokens, in.ImageQuotaTokens, in.InputPriceMicroUSD, in.OutputPriceMicroUSD, in.Enabled, p.User.ID).Scan(&out.ID, &out.ProviderID, &out.UpstreamModelID, &out.Modality, &out.ContextTokens, &out.MaxOutputTokens, &out.ImageQuotaTokens, &out.InputPriceMicroUSD, &out.OutputPriceMicroUSD, &out.Enabled, &out.QuotaBlockedAt, &out.QuotaBlockReason, &out.Version)
		if e != nil {
			return e
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
		var ver int64
		e := tx.QueryRow(ctx, `SELECT coalesce(max(version),0)+1 FROM prompt_templates WHERE subject=$1`, in.Subject).Scan(&ver)
		if e != nil {
			return e
		}
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
		return
	}
	out.Global = limitView(g, out.Global.Version, false)
	out.Students = map[uuid.UUID]LimitView{}
	return
}
func (s *PostgresConfigStore) PutGlobalLimits(ctx context.Context, p Principal, in PutLimitsInput) (out LimitView, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		a := limitArgs(in)
		e := tx.QueryRow(ctx, `UPDATE ai_global_limits SET daily_request_limit=$1,monthly_request_limit=$2,daily_token_limit=$3,monthly_token_limit=$4,updated_by=$5,updated_at=now(),version=version+1 WHERE singleton AND version=$6 RETURNING version`, a[0], a[1], a[2], a[3], p.User.ID, in.ExpectedVersion).Scan(&out.Version)
		if e != nil {
			return configErr(e)
		}
		return writeAudit(ctx, tx, p, "ai.limits_global_put", "ai_limits", "global", map[string]any{})
	})
	return
}
func (s *PostgresConfigStore) PutStudentLimits(ctx context.Context, p Principal, id uuid.UUID, in PutLimitsInput) (out LimitView, err error) {
	return out, ErrNotFound
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
func limitView(v [4]int64, ver int64, _ bool) LimitView {
	return LimitView{DailyRequests: LimitValue{Mode: "limit", Value: &v[0]}, MonthlyRequests: LimitValue{Mode: "limit", Value: &v[1]}, DailyTokens: LimitValue{Mode: "limit", Value: &v[2]}, MonthlyTokens: LimitValue{Mode: "limit", Value: &v[3]}, Version: ver}
}
