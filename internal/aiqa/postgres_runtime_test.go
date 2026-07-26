package aiqa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresRuntimeConcurrentQuotaAndIdempotency(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 1)
	store := NewPostgresRuntimeStore(pool)

	start := make(chan struct{})
	type result struct {
		run Run
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			<-start
			in := fixture.admission()
			in.IdempotencyKey = "quota-admission-" + string(rune('a'+i)) + "-0001"
			_, run, err := store.AdmitRun(ctx, in)
			results <- result{run, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if (first.err == nil) == (second.err == nil) {
		t.Fatalf("wanted exactly one admission: first=%v second=%v", first.err, second.err)
	}
	rejected := first.err
	accepted := second
	if first.err == nil {
		rejected, accepted = second.err, first
	}
	if !errors.Is(rejected, ErrQuotaExceeded) {
		t.Fatalf("second admission error=%v", rejected)
	}

	// Repeating the accepted key returns the exact run without another reserve.
	var acceptedKey string
	if err := pool.QueryRow(ctx, `SELECT idempotency_key FROM ai_runs WHERE id=$1`, accepted.run.ID).Scan(&acceptedKey); err != nil {
		t.Fatal(err)
	}
	duplicate := fixture.admission()
	duplicate.IdempotencyKey = acceptedKey
	_, duplicateRun, err := store.AdmitRun(ctx, duplicate)
	if err != nil || duplicateRun.ID != accepted.run.ID {
		t.Fatalf("duplicate run=%s want=%s err=%v", duplicateRun.ID, accepted.run.ID, err)
	}
	var reserves int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_ledger WHERE run_id=$1 AND action='reserve'`, accepted.run.ID).Scan(&reserves); err != nil || reserves != 2 {
		t.Fatalf("reserve rows=%d err=%v", reserves, err)
	}
}

func TestPostgresRuntimeOneActiveOwnerAndTerminalIdempotency(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	store := NewPostgresRuntimeStore(pool)

	in := fixture.admission()
	_, run, err := store.AdmitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	second := fixture.admission()
	second.IdempotencyKey = "second-active-run-0001"
	if _, _, err = store.AdmitRun(ctx, second); !errors.Is(err, ErrAIBusy) {
		t.Fatalf("second active error=%v", err)
	}
	if _, err = store.GetRun(ctx, uuid.New(), run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner get=%v", err)
	}
	if _, err = store.CancelRun(ctx, uuid.New(), run.ID, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner cancel=%v", err)
	}
	if _, err = store.CancelRun(ctx, fixture.student, run.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CancelRun(ctx, fixture.student, run.ID, time.Now()); err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	var releases int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_ledger WHERE run_id=$1 AND action='release'`, run.ID).Scan(&releases); err != nil || releases != 2 {
		t.Fatalf("release rows=%d err=%v", releases, err)
	}
	digest := sha256.Sum256([]byte(fixture.cfg.Prompt.Body))
	retryInput := RuntimeRetryAdmission{
		StudentID: fixture.student, SourceRunID: run.ID, RunID: uuid.New(), IdempotencyKey: "retry-cancelled-0001",
		Snapshot:    RuntimeSnapshot{Provider: fixture.cfg, PromptSHA256: hex.EncodeToString(digest[:])},
		Reservation: QuotaReservation{RequestCount: 1, TokenCount: 2000, DayKey: "2026-07-26", MonthKey: "2026-07", EstimatorVersion: CurrentEstimatorVersion},
		Now:         time.Now().UTC(),
	}
	_, retry, err := store.RetryRun(ctx, retryInput)
	if err != nil || retry.AttemptNo != 2 || retry.TriggerMessageID != run.TriggerMessageID {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	if _, _, err = store.RetryRun(ctx, RuntimeRetryAdmission{
		StudentID: fixture.student, SourceRunID: retry.ID, RunID: uuid.New(), IdempotencyKey: "retry-queued-run-0001",
		Snapshot: retryInput.Snapshot, Reservation: retryInput.Reservation, Now: time.Now().UTC(),
	}); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("queued retry source error=%v", err)
	}
	var assistants int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_messages WHERE trigger_run_id=$1`, retry.ID).Scan(&assistants); err != nil || assistants != 0 {
		t.Fatalf("queued assistant count=%d err=%v", assistants, err)
	}
}

func TestPostgresRuntimeConcurrentSettleWritesOneTerminalSet(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	store := NewPostgresRuntimeStore(pool)
	_, run, err := store.AdmitRun(ctx, fixture.admission())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err = pool.Exec(ctx, `UPDATE ai_runs SET status='streaming',started_at=$2::timestamptz,updated_at=$2::timestamptz,lease_owner='test',lease_expires_at=$2::timestamptz+interval '1 minute',heartbeat_at=$2::timestamptz WHERE id=$1`, run.ID, now); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, e := store.SucceedRun(ctx, fixture.student, run.ID, "final answer", TerminalUsage{InputTokens: 3, OutputTokens: 2, CostMicroUSD: 7, UsageSource: "upstream"}, time.Now().UTC())
			errs <- e
		}()
	}
	close(start)
	if err = <-errs; err != nil {
		t.Fatal(err)
	}
	if err = <-errs; err != nil {
		t.Fatal(err)
	}
	var settles, releases, assistants int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE action='settle'),count(*) FILTER(WHERE action='release') FROM ai_usage_ledger WHERE run_id=$1`, run.ID).Scan(&settles, &releases); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_messages WHERE trigger_run_id=$1`, run.ID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	if settles != 2 || releases != 2 || assistants != 1 {
		t.Fatalf("settles=%d releases=%d assistants=%d", settles, releases, assistants)
	}
}

type runtimeFixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	student  uuid.UUID
	provider uuid.UUID
	model    uuid.UUID
	prompt   uuid.UUID
	cfg      RuntimeProviderConfig
}

func newRuntimeFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, dailyRequests int64) runtimeFixture {
	t.Helper()
	if _, err := pool.Exec(ctx, `TRUNCATE TABLE users CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_global_limits(singleton,daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit)
VALUES(true,$1,100,1000000,1000000)`, dailyRequests); err != nil {
		t.Fatal(err)
	}
	f := runtimeFixture{t: t, ctx: ctx, pool: pool, student: uuid.New(), provider: uuid.New(), model: uuid.New(), prompt: uuid.New()}
	admin := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES
($1,$2,$2,'admin','active','x'),($3,$4,$4,'student','active','x')`, admin, "runtime-admin", f.student, "runtime-student"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,active,created_by)
VALUES($1,'runtime','https://api.example.test/v1','responses',decode(repeat('00',29),'hex'),1,now(),true,$2)`, f.provider, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,enabled,connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,created_by,updated_by)
VALUES($1,$2,'runtime-model','text',8192,1024,1000,2,3,true,1000,5000,5000,30000,$3,$3)`, f.model, f.provider, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO prompt_templates(id,subject,version,system_prompt,active,created_by) VALUES($1,'math',1,'runtime prompt',true,$2)`, f.prompt, admin); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://api.example.test/v1")
	f.cfg = RuntimeProviderConfig{
		ProviderID: f.provider, BaseURL: u, ProtocolMode: ProtocolResponses,
		Model:    ModelView{ID: f.model, ProviderID: f.provider, UpstreamModelID: "runtime-model", Modality: ModalityText, ContextTokens: 8192, MaxOutputTokens: 1024, ImageQuotaTokens: 1000, InputPriceMicroUSD: 2, OutputPriceMicroUSD: 3, Enabled: true},
		Prompt:   PromptView{ID: f.prompt, Subject: SubjectMath, Version: 1, Body: "runtime prompt", Active: true},
		Timeouts: GatewayTimeouts{Connect: time.Second, ResponseHeader: 5 * time.Second, IdleStream: 5 * time.Second, Total: 30 * time.Second},
	}
	return f
}

func (f runtimeFixture) admission() RuntimeAdmission {
	now := time.Now().UTC()
	sum := sha256.Sum256([]byte(f.cfg.Prompt.Body))
	return RuntimeAdmission{
		StudentID: f.student, ThreadID: uuid.New(), CreateThread: true, ThreadTitle: "runtime", Subject: SubjectMath,
		MessageID: uuid.New(), MessageBody: "question", IdempotencyKey: "runtime-admit-0001", AttemptNo: 1,
		Snapshot:    RuntimeSnapshot{Provider: f.cfg, PromptSHA256: hex.EncodeToString(sum[:])},
		Reservation: QuotaReservation{RequestCount: 1, TokenCount: 2000, DayKey: "2026-07-26", MonthKey: "2026-07", EstimatorVersion: CurrentEstimatorVersion},
		Now:         now,
	}
}

func TestPostgresRuntimeAnomalyStoresActualAndBlocksModel(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	store := NewPostgresRuntimeStore(pool)
	in := fixture.admission()
	in.Reservation.TokenCount = 2
	_, run, err := store.AdmitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err = pool.Exec(ctx, `UPDATE ai_runs SET status='streaming',started_at=$2::timestamptz,updated_at=$2::timestamptz,lease_owner='test',lease_expires_at=$2::timestamptz+interval '1 minute',heartbeat_at=$2::timestamptz WHERE id=$1`, run.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SucceedRun(ctx, fixture.student, run.ID, "answer", TerminalUsage{InputTokens: 10, OutputTokens: 5, CostMicroUSD: 99, UsageSource: "upstream"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var input, output, cost, charged int64
	var reason string
	if err = pool.QueryRow(ctx, `SELECT input_tokens,output_tokens,cost_micro_usd FROM ai_runs WHERE id=$1`, run.ID).Scan(&input, &output, &cost); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT quota_block_reason FROM ai_models WHERE id=$1`, fixture.model).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT sum(token_delta) FROM ai_usage_ledger WHERE run_id=$1`, run.ID).Scan(&charged); err != nil {
		t.Fatal(err)
	}
	if input != 10 || output != 5 || cost != 99 || charged != 4 || reason != "quota_estimation_anomaly" {
		t.Fatalf("actual=%d/%d cost=%d charged(two periods)=%d reason=%s", input, output, cost, charged, reason)
	}
}
