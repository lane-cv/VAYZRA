package aiqa

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresConfigConcurrentActivationAndRedactedReads(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE users CASCADE"); err != nil {
		t.Fatal(err)
	}
	adminID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,password_hash,must_change_password) VALUES($1,'aiadmin','AI Admin','admin','x',false)`, adminID); err != nil {
		t.Fatal(err)
	}
	actor := Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "ai-config-test", IP: net.ParseIP("192.0.2.9")}
	store := NewPostgresConfigStore(pool)
	first, second := seedProvider(t, ctx, pool, adminID, "first"), seedProvider(t, ctx, pool, adminID, "second")
	seedActivationDependencies(t, ctx, pool, adminID, first)
	seedActivationDependencies(t, ctx, pool, adminID, second)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []uuid.UUID{first, second} {
		wg.Add(1)
		go func(id uuid.UUID) { defer wg.Done(); _, e := store.ActivateProvider(ctx, actor, id, 1); errs <- e }(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("activate: %v", err)
		}
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_providers WHERE active`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active=%d err=%v", active, err)
	}
	providers, err := store.ListProviders(ctx)
	if err != nil || len(providers) != 2 || !providers[0].HasKey || !providers[1].HasKey {
		t.Fatalf("providers=%#v err=%v", providers, err)
	}
	if _, err := store.UpdateProvider(ctx, actor, UpdateProviderInput{ID: first, Name: "first", BaseURL: "https://api.example.test", ProtocolMode: ProtocolResponses, ExpectedVersion: 1}, nil); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("stale update=%v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='ai.provider_activated'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
}

func seedProvider(t *testing.T, ctx context.Context, pool *pgxpool.Pool, admin uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,created_by) VALUES($1,$2,'https://api.example.test','responses',decode(repeat('00',29),'hex'),1,now(),$3)`, id, name, admin)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func seedActivationDependencies(t *testing.T, ctx context.Context, pool *pgxpool.Pool, admin, provider uuid.UUID) {
	t.Helper()
	for _, modality := range []string{"text", "vision"} {
		if _, err := pool.Exec(ctx, `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,created_by,updated_by) VALUES($1,$2,$3,$3,8192,1024,1000,0,0,$4,$4)`, uuid.New(), provider, modality, admin); err != nil {
			t.Fatal(err)
		}
	}
	for _, subject := range []string{"math", "physics"} {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM prompt_templates WHERE subject=$1`, subject).Scan(&n)
		if n == 0 {
			if _, err := pool.Exec(ctx, `INSERT INTO prompt_templates(id,subject,version,system_prompt,active,created_by) VALUES($1,$2,1,$2||' prompt',true,$3)`, uuid.New(), subject, admin); err != nil {
				t.Fatal(err)
			}
		}
	}
}
