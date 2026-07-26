package aiqa

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresConfigCreateProviderIdempotencyAndSecretAAD(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAIConfig(t, ctx, pool)
	admin := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,password_hash,must_change_password) VALUES($1,'provider_admin','Provider Admin','admin','x',false)`, admin); err != nil {
		t.Fatal(err)
	}
	actor := Principal{User: auth.User{ID: admin, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "provider-create", IP: net.ParseIP("192.0.2.10")}
	id := uuid.New()
	key := []byte("12345678901234567890123456789012")
	box, err := NewAESGCMSecretBox(key, 1, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store := NewPostgresConfigStoreWithSecurity(pool, box, URLPolicy{})
	secret, err := box.Seal(id, []byte("provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	in := CreateProviderInput{Name: "provider", BaseURL: "https://api.example.test", ProtocolMode: ProtocolResponses, IdempotencyKey: "1234567890abcdef"}
	hash := sha256.Sum256([]byte("provider\x00https://api.example.test\x00responses"))
	first, err := store.CreateProvider(ctx, actor, id, in, secret, hash)
	if err != nil || first.ID != id {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	replayID := uuid.New()
	replaySecret, err := box.Seal(replayID, []byte("provider-secret"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.CreateProvider(ctx, actor, replayID, in, replaySecret, hash)
	if err != nil || again.ID != id {
		t.Fatalf("replay=%#v err=%v", again, err)
	}
	differentID := uuid.New()
	differentSecret, err := box.Seal(differentID, []byte("different-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProvider(ctx, actor, differentID, in, differentSecret, hash); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("different payload=%v", err)
	}
	var persistedHash []byte
	if err := pool.QueryRow(ctx, `SELECT request_hash FROM ai_config_idempotency WHERE key=$1`, in.IdempotencyKey).Scan(&persistedHash); err != nil {
		t.Fatal(err)
	}
	secretDigest := sha256.Sum256([]byte("provider\x00https://api.example.test\x00responses\x00provider-secret"))
	if bytes.Equal(persistedHash, secretDigest[:]) {
		t.Fatal("persisted request hash includes API key")
	}
	var fingerprintColumns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name='ai_config_idempotency' AND column_name ILIKE '%fingerprint%'`).Scan(&fingerprintColumns); err != nil || fingerprintColumns != 0 {
		t.Fatalf("fingerprint columns=%d err=%v", fingerprintColumns, err)
	}
	var blob []byte
	var version int16
	if err := pool.QueryRow(ctx, `SELECT encrypted_api_key,key_version FROM ai_providers WHERE id=$1`, id).Scan(&blob, &version); err != nil {
		t.Fatal(err)
	}
	opened, err := box.Open(id, EncryptedSecret{KeyVersion: version, Blob: blob})
	if err != nil || string(opened) != "provider-secret" {
		t.Fatalf("AAD open=%q err=%v", opened, err)
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='ai.provider_created'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}

func TestPostgresConfigRuntimeDecryptsAndCopiesSecret(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE users CASCADE"); err != nil {
		t.Fatal(err)
	}
	admin := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,password_hash,must_change_password) VALUES($1,'runtime_admin','Runtime Admin','admin','x',false)`, admin); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	box, err := NewAESGCMSecretBox(bytes.Repeat([]byte{4}, 32), 1, bytes.NewReader(bytes.Repeat([]byte{9}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal(id, []byte("runtime-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,active,created_by) VALUES($1,'runtime','http://api.example.test','responses',$2,$3,now(),true,$4)`, id, sealed.Blob, sealed.KeyVersion, admin); err != nil {
		t.Fatal(err)
	}
	seedActivationDependencies(t, ctx, pool, admin, id)
	store := NewPostgresConfigStoreWithSecurity(pool, box, URLPolicy{DevelopmentAllowPrivate: true, Resolver: testResolver{}})
	cfg, err := store.ForRun(ctx, SubjectMath, ModalityText)
	if err != nil || cfg.ProviderID != id || string(cfg.APIKey) != "runtime-secret" || cfg.BaseURL.String() != "http://api.example.test" {
		t.Fatalf("cfg=%#v err=%v", cfg, err)
	}
	cfg.APIKey[0] = 'X'
	again, err := store.ForRun(ctx, SubjectMath, ModalityText)
	if err != nil || string(again.APIKey) != "runtime-secret" {
		t.Fatalf("secret copy=%q err=%v", again.APIKey, err)
	}
	actor := Principal{User: auth.User{ID: admin, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "runtime-model", IP: net.ParseIP("192.0.2.13")}
	var textID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ai_models WHERE provider_id=$1 AND modality='text'`, id).Scan(&textID); err != nil {
		t.Fatal(err)
	}
	disable := PutModelInput{ProviderID: id, ID: textID, UpstreamModelID: "text", Modality: ModalityText, ContextTokens: 8192, MaxOutputTokens: 1024, ImageQuotaTokens: 1000, Enabled: false, ExpectedVersion: 1}
	if _, err := store.PutModel(ctx, actor, disable); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("disable sole active text=%v", err)
	}
	second := disable
	second.ID = uuid.New()
	second.UpstreamModelID = "text-second"
	second.Enabled = true
	second.ExpectedVersion = 0
	if _, err := store.PutModel(ctx, actor, second); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("add ambiguous active text=%v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,enabled,created_by,updated_by) VALUES($1,$2,'ambiguous-text','text',8192,1024,1000,0,0,true,$3,$3)`, uuid.New(), id, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForRun(ctx, SubjectMath, ModalityText); !errors.Is(err, ErrAIDisabled) {
		t.Fatalf("ambiguous runtime=%v", err)
	}
}

func TestPostgresConnectivityLeaseExcludesOtherStoreInstancesAndAudits(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAIConfig(t, ctx, pool)
	admin := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,password_hash,must_change_password) VALUES($1,'connectivity_admin','Connectivity Admin','admin','x',false)`, admin); err != nil {
		t.Fatal(err)
	}
	providerID := uuid.New()
	box, err := NewAESGCMSecretBox(bytes.Repeat([]byte{7}, 32), 1, bytes.NewReader(bytes.Repeat([]byte{3}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal(providerID, []byte("connectivity-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,created_by) VALUES($1,'connectivity','http://api.example.test','responses',$2,$3,now(),$4)`, providerID, sealed.Blob, sealed.KeyVersion, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,created_by,updated_by) VALUES($1,$2,'probe-model','text',8192,32,1000,0,0,$3,$3)`, uuid.New(), providerID, admin); err != nil {
		t.Fatal(err)
	}
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: testResolver{}}
	first := NewPostgresConfigStoreWithSecurity(pool, box, policy)
	second := NewPostgresConfigStoreWithSecurity(pool, box, policy)
	cfg, release, err := first.AcquireProviderTest(ctx, providerID)
	if err != nil || string(cfg.APIKey) != "connectivity-secret" || release == nil {
		t.Fatalf("first cfg=%#v err=%v", cfg, err)
	}
	busyCfg, busyRelease, err := second.AcquireProviderTest(ctx, providerID)
	if !errors.Is(err, ErrProviderTestBusy) || busyRelease != nil || busyCfg.ProtocolMode != ProtocolResponses || len(busyCfg.APIKey) != 0 {
		t.Fatalf("busy cfg=%#v release=%v err=%v", busyCfg, busyRelease != nil, err)
	}
	release()
	cfg, release, err = second.AcquireProviderTest(ctx, providerID)
	if err != nil || release == nil {
		t.Fatalf("after release cfg=%#v err=%v", cfg, err)
	}
	zeroBytes(cfg.APIKey)
	release()

	actor := Principal{User: auth.User{ID: admin, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "provider-test", IP: net.ParseIP("192.0.2.18")}
	if err := first.RecordProviderTest(ctx, actor, providerTestAudit{providerID: providerID, protocol: ProtocolResponses, category: "auth", latencyMS: 12}); err != nil {
		t.Fatal(err)
	}
	var metadata string
	if err := pool.QueryRow(ctx, `SELECT metadata::text FROM audit_logs WHERE action='ai.provider_tested' AND target_id=$1`, providerID.String()).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"connectivity-secret", "authorization", "requestBody", "upstream"} {
		if strings.Contains(strings.ToLower(metadata), strings.ToLower(forbidden)) {
			t.Fatalf("audit leaked %q: %s", forbidden, metadata)
		}
	}
}

func TestConnectivityServiceReleasesLeaseBeforeAuditWithSingleConnection(t *testing.T) {
	ctx, pool, box, policy, actor, providerID := connectivityPostgresFixture(t)
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(single.Close)
	store := NewPostgresConfigStoreWithSecurity(single, box, policy)
	svc := NewAdminConfigServiceWithConnectivity(store, policy, box, &blockingConnectivityTester{
		result: ConnectivityResult{OK: true, Protocol: ProtocolResponses, LatencyMS: 2},
	})
	callCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	result, err := svc.TestProvider(callCtx, actor, providerID)
	if err != nil || !result.OK {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='ai.provider_tested' AND target_id=$1`, providerID.String()).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
}

func TestConnectivityServiceDurablyAuditsCancellationAfterLeaseRelease(t *testing.T) {
	ctx, pool, box, policy, actor, providerID := connectivityPostgresFixture(t)
	store := NewPostgresConfigStoreWithSecurity(pool, box, policy)
	tester := &blockingConnectivityTester{started: make(chan struct{}, 1), release: make(chan struct{})}
	svc := NewAdminConfigServiceWithConnectivity(store, policy, box, tester)
	callCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := svc.TestProvider(callCtx, actor, providerID)
		done <- err
	}()
	select {
	case <-tester.started:
	case <-time.After(time.Second):
		t.Fatal("connectivity probe did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("cancelled call error=%v", err)
	}
	var category string
	if err := pool.QueryRow(ctx, `SELECT metadata->>'errorCategory' FROM audit_logs WHERE action='ai.provider_tested' AND target_id=$1 ORDER BY id DESC LIMIT 1`, providerID.String()).Scan(&category); err != nil || category != "cancelled" {
		t.Fatalf("audit category=%q err=%v", category, err)
	}
}

func connectivityPostgresFixture(t *testing.T) (context.Context, *pgxpool.Pool, SecretBox, URLPolicy, Principal, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAIConfig(t, ctx, pool)
	admin := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,password_hash,must_change_password) VALUES($1,'connectivity_service_admin','Connectivity Service Admin','admin','x',false)`, admin); err != nil {
		t.Fatal(err)
	}
	providerID := uuid.New()
	box, err := NewAESGCMSecretBox(bytes.Repeat([]byte{8}, 32), 1, bytes.NewReader(bytes.Repeat([]byte{4}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal(providerID, []byte("service-connectivity-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,created_by) VALUES($1,'service connectivity','http://api.example.test','responses',$2,$3,now(),$4)`, providerID, sealed.Blob, sealed.KeyVersion, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,created_by,updated_by) VALUES($1,$2,'probe-model','text',8192,32,1000,0,0,$3,$3)`, uuid.New(), providerID, admin); err != nil {
		t.Fatal(err)
	}
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: testResolver{}}
	actor := Principal{User: auth.User{ID: admin, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "provider-test-service", IP: net.ParseIP("192.0.2.28")}
	return ctx, pool, box, policy, actor, providerID
}

func TestPostgresConfigModelPromptAndLimitContracts(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAIConfig(t, ctx, pool)
	admin := uuid.New()
	student := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,password_hash,must_change_password) VALUES($1,'contracts_admin','Contracts Admin','admin','x',false),($2,'contracts_student','Contracts Student','student','x',false)`, admin, student); err != nil {
		t.Fatal(err)
	}
	actor := Principal{User: auth.User{ID: admin, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "contracts", IP: net.ParseIP("192.0.2.11")}
	store := NewPostgresConfigStore(pool)
	one, two := seedProvider(t, ctx, pool, admin, "one"), seedProvider(t, ctx, pool, admin, "two")
	model := uuid.New()
	in := PutModelInput{ProviderID: one, ID: model, UpstreamModelID: "model", Modality: ModalityText, ContextTokens: 100, MaxOutputTokens: 50, ImageQuotaTokens: 10, Enabled: true, ExpectedVersion: 0}
	first, err := store.PutModel(ctx, actor, in)
	if err != nil || first.Version != 1 {
		t.Fatalf("create=%#v err=%v", first, err)
	}
	if _, err := store.PutModel(ctx, actor, in); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("duplicate create=%v", err)
	}
	in.ExpectedVersion = 1
	in.ProviderID = two
	if _, err := store.PutModel(ctx, actor, in); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("cross provider=%v", err)
	}
	in.ProviderID = one
	in.ExpectedVersion = 2
	if _, err := store.PutModel(ctx, actor, in); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("stale=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_models SET quota_blocked_at=now(),quota_block_reason='quota_estimation_anomaly' WHERE id=$1`, model); err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion = 1
	in.ClearQuotaBlock = true
	if _, err := store.PutModel(ctx, actor, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("clear unchanged=%v", err)
	}
	in.ImageQuotaTokens = 11
	updated, err := store.PutModel(ctx, actor, in)
	if err != nil || updated.QuotaBlockedAt != nil {
		t.Fatalf("clear=%#v err=%v", updated, err)
	}
	prompt, err := store.PutPrompt(ctx, actor, PutPromptInput{Subject: SubjectMath, Body: "one", ExpectedVersion: 0})
	if err != nil || prompt.Version != 1 {
		t.Fatalf("prompt=%#v err=%v", prompt, err)
	}
	if _, err := store.PutPrompt(ctx, actor, PutPromptInput{Subject: SubjectMath, Body: "two", ExpectedVersion: 0}); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("prompt stale=%v", err)
	}
	v := int64(9)
	limits := PutLimitsInput{DailyRequests: LimitValue{Mode: "inherit"}, MonthlyRequests: LimitValue{Mode: "disabled"}, DailyTokens: LimitValue{Mode: "limit", Value: &v}, MonthlyTokens: LimitValue{Mode: "inherit"}, ExpectedVersion: 0}
	studentView, err := store.PutStudentLimits(ctx, actor, student, limits)
	if err != nil || studentView.DailyRequests.Mode != "inherit" || studentView.MonthlyRequests.Mode != "disabled" || studentView.DailyTokens.Value == nil || *studentView.DailyTokens.Value != 9 {
		t.Fatalf("student=%#v err=%v", studentView, err)
	}
	limits.ExpectedVersion = 0
	if _, err := store.PutStudentLimits(ctx, actor, student, limits); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("student stale=%v", err)
	}
	all, err := store.GetLimits(ctx)
	if err != nil || all.Students[student].Version != studentView.Version {
		t.Fatalf("limits=%#v err=%v", all, err)
	}
}

func TestPostgresConfigRollsBackMutationWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAIConfig(t, ctx, pool)
	admin := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,password_hash,must_change_password) VALUES($1,'rollback_admin','Rollback Admin','admin','x',false)`, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION aiqa_reject_audit() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'forced ai audit failure'; END; $$ LANGUAGE plpgsql; CREATE TRIGGER aiqa_reject_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION aiqa_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DROP TRIGGER IF EXISTS aiqa_reject_audit ON audit_logs; DROP FUNCTION IF EXISTS aiqa_reject_audit()`)
	})
	actor := Principal{User: auth.User{ID: admin, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "rollback", IP: net.ParseIP("192.0.2.12")}
	zero := int64(0)
	in := PutLimitsInput{DailyRequests: LimitValue{Mode: "limit", Value: &zero}, MonthlyRequests: LimitValue{Mode: "disabled"}, DailyTokens: LimitValue{Mode: "disabled"}, MonthlyTokens: LimitValue{Mode: "disabled"}, ExpectedVersion: 1}
	if _, err := NewPostgresConfigStore(pool).PutGlobalLimits(ctx, actor, in); err == nil {
		t.Fatal("expected audit failure")
	}
	var version, daily int64
	if err := pool.QueryRow(ctx, `SELECT version,daily_request_limit FROM ai_global_limits`).Scan(&version, &daily); err != nil || version != 1 || daily != 0 {
		t.Fatalf("version=%d daily=%d err=%v", version, daily, err)
	}
}

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
func resetAIConfig(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE users CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_global_limits(singleton,daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit) VALUES(true,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
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
