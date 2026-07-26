package database_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestAIConfigurationSchemaEnforcesSecretsAndOneActiveProvider(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	replayAIConfigurationMigration(t, pool)
	adminID := seedActiveAdmin(t, pool)
	first := uuid.New()
	second := uuid.New()
	insertProvider(t, pool, first, adminID, false)
	insertProvider(t, pool, second, adminID, false)
	mustExec(t, pool, `UPDATE ai_providers SET active=true WHERE id=$1`, first)
	if _, err := pool.Exec(ctx, `UPDATE ai_providers SET active=true WHERE id=$1`, second); err == nil {
		t.Fatal("expected one-active-provider constraint")
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_providers SET encrypted_api_key='' WHERE id=$1`, first); err == nil {
		t.Fatal("expected ciphertext constraint")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,created_by) VALUES($1,'Bad protocol','https://api.example.test','unexpected',decode(repeat('00',29),'hex'),1,now(),$2)`, uuid.New(), adminID); err == nil {
		t.Fatal("expected protocol constraint")
	}
}

func TestAIConfigurationIdempotencySchemaRejectsUnsafeRows(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	replayAIConfigurationMigration(t, pool)
	admin := seedActiveAdmin(t, pool)
	provider := uuid.New()
	insertProvider(t, pool, provider, admin, false)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_config_idempotency(key,operation,request_hash,provider_id,created_by) VALUES('short','create_provider',decode(repeat('00',32),'hex'),$1,$2)`, provider, admin); err == nil {
		t.Fatal("short idempotency key accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_config_idempotency(key,operation,request_hash,provider_id,created_by) VALUES('1234567890abcdef','other',decode(repeat('00',32),'hex'),$1,$2)`, provider, admin); err == nil {
		t.Fatal("unknown operation accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_config_idempotency(key,operation,request_hash,provider_id,created_by) VALUES('abcdef1234567890','create_provider',decode(repeat('00',31),'hex'),$1,$2)`, provider, admin); err == nil {
		t.Fatal("short request hash accepted")
	}
}

func TestAIConfigurationSchemaEnforcesModelPromptAndLimitInvariants(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	replayAIConfigurationMigration(t, pool)
	adminID := seedActiveAdmin(t, pool)
	providerID := uuid.New()
	insertProvider(t, pool, providerID, adminID, false)
	modelID := uuid.New()
	insertModel(t, pool, modelID, providerID, adminID, "text")
	assertGlobalLimitBootstrap(t, pool)

	queries := []struct {
		name  string
		query string
		args  []any
	}{
		{"duplicate provider model modality", `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,created_by,updated_by) VALUES($1,$2,'model-text','text',8192,1024,1000,0,0,$3,$3)`, []any{uuid.New(), providerID, adminID}},
		{"negative input price", `UPDATE ai_models SET input_price_micro_usd_per_million_tokens=-1 WHERE id=$1`, []any{modelID}},
		{"negative output price", `UPDATE ai_models SET output_price_micro_usd_per_million_tokens=-1 WHERE id=$1`, []any{modelID}},
		{"inconsistent quota block time only", `UPDATE ai_models SET quota_blocked_at=now() WHERE id=$1`, []any{modelID}},
		{"inconsistent quota block reason only", `UPDATE ai_models SET quota_block_reason='quota_estimation_anomaly' WHERE id=$1`, []any{modelID}},
		{"invalid quota block reason", `UPDATE ai_models SET quota_blocked_at=now(),quota_block_reason='other' WHERE id=$1`, []any{modelID}},
		{"invalid model modality", `UPDATE ai_models SET modality='audio' WHERE id=$1`, []any{modelID}},
		{"invalid model timeout relationship", `UPDATE ai_models SET total_timeout_ms=1000,response_header_timeout_ms=2000 WHERE id=$1`, []any{modelID}},
		{"negative global limit", `UPDATE ai_global_limits SET daily_request_limit=-1,updated_by=$1`, []any{adminID}},
	}
	for _, tc := range queries {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tc.query, tc.args...); err == nil {
				t.Fatal("constraint accepted invalid value")
			}
		})
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_global_limits SET daily_request_limit=1,updated_by=NULL`); err == nil {
		t.Fatal("expected global limit actor constraint after bootstrap")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ai_global_limits`); err == nil {
		t.Fatal("expected global limit singleton deletion rejection")
	}

	firstPrompt := uuid.New()
	mustExec(t, pool, `INSERT INTO prompt_templates(id,subject,version,system_prompt,active,created_by) VALUES($1,'math',1,'Math system prompt',true,$2)`, firstPrompt, adminID)
	if _, err := pool.Exec(ctx, `INSERT INTO prompt_templates(id,subject,version,system_prompt,active,created_by) VALUES($1,'math',2,'New math system prompt',true,$2)`, uuid.New(), adminID); err == nil {
		t.Fatal("expected one-active-prompt constraint")
	}
	if _, err := pool.Exec(ctx, `UPDATE prompt_templates SET system_prompt='mutated' WHERE id=$1`, firstPrompt); err == nil {
		t.Fatal("expected prompt immutability")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM prompt_templates WHERE id=$1`, firstPrompt); err == nil {
		t.Fatal("expected prompt delete rejection")
	}

	studentID := seedActiveStudent(t, pool)
	mustExec(t, pool, `INSERT INTO student_ai_limits(student_id,daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit,updated_by) VALUES($1,NULL,0,10,NULL,$2)`, studentID, adminID)
	if _, err := pool.Exec(ctx, `UPDATE student_ai_limits SET daily_token_limit=-1 WHERE student_id=$1`, studentID); err == nil {
		t.Fatal("expected student limit constraint")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO student_ai_limits(student_id,updated_by) VALUES($1,$2)`, adminID, adminID); err == nil {
		t.Fatal("expected active-student limit ownership constraint")
	}
}

func TestAIConfigurationSchemaRequiresActiveAdminActors(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	replayAIConfigurationMigration(t, pool)
	adminID := seedActiveAdmin(t, pool)
	studentActorID := seedActiveStudent(t, pool)
	providerID := uuid.New()
	insertProvider(t, pool, providerID, adminID, false)
	modelID := uuid.New()
	insertModel(t, pool, modelID, providerID, adminID, "text")

	for _, tc := range []struct {
		name  string
		query string
		args  []any
	}{
		{"provider created by student", `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,created_by) VALUES($1,'Student provider','https://api.example.test','responses',decode(repeat('00',29),'hex'),1,now(),$2)`, []any{uuid.New(), studentActorID}},
		{"model created by student", `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,created_by,updated_by) VALUES($1,$2,'student-created','vision',8192,1024,1000,0,0,$3,$4)`, []any{uuid.New(), providerID, studentActorID, adminID}},
		{"model updated by student", `UPDATE ai_models SET updated_by=$2 WHERE id=$1`, []any{modelID, studentActorID}},
		{"prompt created by student", `INSERT INTO prompt_templates(id,subject,version,system_prompt,created_by) VALUES($1,'physics',1,'Physics system prompt',$2)`, []any{uuid.New(), studentActorID}},
		{"global limits updated by student", `UPDATE ai_global_limits SET daily_request_limit=1,updated_by=$1`, []any{studentActorID}},
		{"student limits updated by student", `INSERT INTO student_ai_limits(student_id,updated_by) VALUES($1,$2)`, []any{studentActorID, studentActorID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tc.query, tc.args...); err == nil {
				t.Fatal("admin actor constraint accepted non-admin")
			}
		})
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE users SET status='active',deleted_at=NULL WHERE id=$1`, adminID)
	})
	mustExec(t, pool, `UPDATE users SET status='disabled' WHERE id=$1`, adminID)
	if _, err := pool.Exec(ctx, `UPDATE ai_models SET updated_by=$2 WHERE id=$1`, modelID, adminID); err == nil {
		t.Fatal("inactive admin updated model")
	}
	mustExec(t, pool, `UPDATE users SET status='active' WHERE id=$1`, adminID)
	mustExec(t, pool, `UPDATE users SET deleted_at=now() WHERE id=$1`, adminID)
	if _, err := pool.Exec(ctx, `INSERT INTO prompt_templates(id,subject,version,system_prompt,created_by) VALUES($1,'physics',1,'Deleted admin prompt',$2)`, uuid.New(), adminID); err == nil {
		t.Fatal("deleted admin created prompt")
	}
}

func TestAIConfigurationMigrationDownUpRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	t.Cleanup(closeProvider)
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore latest migration: %v", err)
		}
	})
	if _, err := provider.DownTo(ctx, 14); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('ai_providers','ai_models','prompt_templates','ai_global_limits','student_ai_limits')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("remaining ai configuration tables=%d", tables)
	}
	if _, err := provider.UpTo(ctx, 15); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('ai_providers','ai_models','prompt_templates','ai_global_limits','student_ai_limits')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 5 {
		t.Fatalf("ai configuration tables after re-up=%d", tables)
	}
}

func replayAIConfigurationMigration(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	t.Cleanup(closeProvider)
	t.Cleanup(func() {
		if _, err := provider.Up(context.Background()); err != nil {
			t.Errorf("restore latest migration: %v", err)
		}
	})
	if _, err := provider.DownTo(context.Background(), 14); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 15); err != nil {
		t.Fatal(err)
	}
}

func seedActiveAdmin(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		WITH existing AS (
			SELECT id FROM users
			WHERE role='admin' AND status='active' AND deleted_at IS NULL
			LIMIT 1
		), inserted AS (
			INSERT INTO users(id,username,display_name,role,status,password_hash)
			SELECT $1,$2,'AI configuration admin','admin','active','hash'
			WHERE NOT EXISTS (SELECT 1 FROM existing)
			RETURNING id
		)
		SELECT id FROM existing UNION ALL SELECT id FROM inserted LIMIT 1`, uuid.New(), "ai_config_admin_"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertGlobalLimitBootstrap(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_global_limits`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("global limit rows=%d, want 1", count)
	}
	var dailyRequests, monthlyRequests, dailyTokens, monthlyTokens int64
	var updatedByNull bool
	if err := pool.QueryRow(context.Background(), `SELECT daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit,updated_by IS NULL FROM ai_global_limits`).Scan(&dailyRequests, &monthlyRequests, &dailyTokens, &monthlyTokens, &updatedByNull); err != nil {
		t.Fatal(err)
	}
	if dailyRequests != 0 || monthlyRequests != 0 || dailyTokens != 0 || monthlyTokens != 0 || !updatedByNull {
		t.Fatalf("global bootstrap limits=(%d,%d,%d,%d) updated_by_null=%t", dailyRequests, monthlyRequests, dailyTokens, monthlyTokens, updatedByNull)
	}
}

func seedActiveStudent(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'AI configuration student','student','active','hash')`, id, "ai_config_student_"+id.String()); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertProvider(t *testing.T, pool *pgxpool.Pool, id, adminID uuid.UUID, active bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,active,created_by) VALUES($1,$2,'https://api.example.test','chat_completions',decode(repeat('00',29),'hex'),1,now(),$3,$4)`, id, "AI provider "+id.String(), active, adminID); err != nil {
		t.Fatal(err)
	}
}

func insertModel(t *testing.T, pool *pgxpool.Pool, id, providerID, adminID uuid.UUID, modality string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,created_by,updated_by) VALUES($1,$2,$3,$4,8192,1024,1000,0,0,$5,$5)`, id, providerID, "model-"+modality, modality, adminID); err != nil {
		t.Fatal(err)
	}
}

func mustExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
