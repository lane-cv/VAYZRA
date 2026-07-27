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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/auth"
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

func TestPostgresRuntimeThreadDetailReturnsLatestTerminalRun(t *testing.T) {
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
	cancelled, err := store.CancelRun(ctx, fixture.student, run.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.GetThread(ctx, fixture.student, run.ThreadID, MessageCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if detail.ActiveRun == nil || detail.ActiveRun.ID != cancelled.ID || detail.ActiveRun.Status != RunCancelled {
		t.Fatalf("latest run=%+v want cancelled=%s", detail.ActiveRun, cancelled.ID)
	}
}

func TestPostgresRuntimeSynchronizedSameKeyReturnsOneRun(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	store := NewPostgresRuntimeStore(pool)
	start := make(chan struct{})
	type result struct {
		run Run
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			in := fixture.admission()
			in.IdempotencyKey = "same-key-race-00001"
			<-start
			_, run, err := store.AdmitRun(ctx, in)
			results <- result{run, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.run.ID != second.run.ID {
		t.Fatalf("first=%+v/%v second=%+v/%v", first.run, first.err, second.run, second.err)
	}
	var runs, reserves int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_runs WHERE student_id=$1 AND idempotency_key='same-key-race-00001'`, fixture.student).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_ledger WHERE run_id=$1 AND action='reserve'`, first.run.ID).Scan(&reserves); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || reserves != 2 {
		t.Fatalf("runs=%d reserve rows=%d", runs, reserves)
	}
}

func TestPostgresRuntimeSynchronizedDistinctKeysHitActiveIndex(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	store := NewPostgresRuntimeStore(pool)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			in := fixture.admission()
			in.IdempotencyKey = "active-index-race-" + string(rune('a'+i))
			<-start
			_, _, err := store.AdmitRun(ctx, in)
			errs <- err
		}()
	}
	close(start)
	first, second := <-errs, <-errs
	if (first == nil) == (second == nil) {
		t.Fatalf("wanted exactly one success: %v / %v", first, second)
	}
	if first != nil && !errors.Is(first, ErrAIBusy) || second != nil && !errors.Is(second, ErrAIBusy) {
		t.Fatalf("wanted ErrAIBusy: %v / %v", first, second)
	}
}

func TestPostgresRunEventReplayIsBoundedByCapturedSequence(t *testing.T) {
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
	sessionID := uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO sessions(id,user_id,token_hash,idle_expires_at,absolute_expires_at)
VALUES($1,$2,$3,now()+interval '1 hour',now()+interval '1 day')`, sessionID, fixture.student, []byte(uuid.NewString())); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO ai_run_events(run_id,sequence,kind,payload_text,created_at) VALUES
($1,1,'delta','one',now()),($1,2,'delta','two',now())`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE ai_runs SET last_sequence=2 WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	actor := Principal{User: auth.User{ID: fixture.student, Role: auth.RoleStudent, Status: auth.StatusActive}, SessionID: sessionID}
	events, err := store.ListRunEvents(ctx, actor, run.ID, 0, 1, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != 1 || events[0].Delta != "one" {
		t.Fatalf("bounded events=%+v", events)
	}
	events, err = store.ListRunEvents(ctx, actor, run.ID, 1, 2, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != 2 || events[0].Delta != "two" {
		t.Fatalf("next events=%+v", events)
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
	streamAt := time.Now().UTC()
	if _, err = pool.Exec(ctx, `UPDATE ai_runs SET status='streaming',started_at=$2::timestamptz,updated_at=$2::timestamptz,lease_owner='test',lease_expires_at=$2::timestamptz+interval '1 minute',heartbeat_at=$2::timestamptz WHERE id=$1`, retry.ID, streamAt); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SucceedRun(ctx, fixture.student, retry.ID, "retry answer", TerminalUsage{
		InputTokens: 500, OutputTokens: 100, CostMicroUSD: 9, UsageSource: "upstream",
	}, streamAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var blocked bool
	if err = pool.QueryRow(ctx, `SELECT quota_blocked_at IS NOT NULL FROM ai_models WHERE id=$1`, fixture.model).Scan(&blocked); err != nil || blocked {
		t.Fatalf("retry falsely blocked model=%v err=%v", blocked, err)
	}
}

func TestPostgresRuntimeServiceRetryRevalidatesPersistedVision(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	var adminID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT created_by FROM ai_providers WHERE id=$1`, fixture.provider).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	visionModelID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,enabled,connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,created_by,updated_by)
VALUES($1,$2,'runtime-vision','vision',8192,1024,1000,2,3,true,1000,5000,5000,30000,$3,$3)`, visionModelID, fixture.provider, adminID); err != nil {
		t.Fatal(err)
	}
	visionConfig := fixture.cfg
	visionConfig.Model.ID = visionModelID
	visionConfig.Model.UpstreamModelID = "runtime-vision"
	visionConfig.Model.Modality = ModalityVision
	imageID := insertAttachmentVersion(t, ctx, pool, fixture.student, "ai_attachment", "ready", "image/png", "retry.png")
	store := NewPostgresRuntimeStore(pool)
	in := fixture.admission()
	in.Snapshot.Provider = visionConfig
	in.Attachments = []AttachmentMetadata{{
		FileVersionID: imageID, DisplayName: "retry.png", DetectedMIME: "image/png", Modality: ModalityVision, Size: 1,
	}}
	_, source, err := store.AdmitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CancelRun(ctx, fixture.student, source.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	attachments := NewPostgresAttachmentStore(pool, nil, nil)
	service := NewStudentService(store, fixedRuntimeConfigSource{text: fixture.cfg, vision: visionConfig}, attachments, time.Now)
	principal := Principal{User: auth.User{ID: fixture.student, Role: auth.RoleStudent, Status: auth.StatusActive}}
	const readyKey = "retry-ready-vision-01"
	readyRetry, err := service.RetryRun(ctx, principal, source.ID, readyKey)
	if err != nil || readyRetry.AttemptNo != 2 {
		t.Fatalf("ready retry=%+v err=%v", readyRetry, err)
	}
	if _, err = store.CancelRun(ctx, fixture.student, readyRetry.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE file_versions SET processing_state='failed' WHERE id=$1`, imageID); err != nil {
		t.Fatal(err)
	}
	// Exact replay remains fast and does not revalidate a now-stale attachment.
	replayed, err := service.RetryRun(ctx, principal, source.ID, readyKey)
	if err != nil || replayed.ID != readyRetry.ID {
		t.Fatalf("stale idempotent replay=%+v err=%v", replayed, err)
	}
	var beforeRuns, beforeLedger int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_runs WHERE student_id=$1`, fixture.student).Scan(&beforeRuns); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_ledger WHERE student_id=$1`, fixture.student).Scan(&beforeLedger); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RetryRun(ctx, principal, source.ID, "retry-unready-vis-01"); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("unready vision retry=%v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE file_versions SET processing_state='ready',purged_at=now() WHERE id=$1`, imageID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RetryRun(ctx, principal, source.ID, "retry-purged-vis-001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purged vision retry=%v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE file_versions SET purged_at=NULL WHERE id=$1`, imageID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, fixture.student); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RetryRun(ctx, principal, source.ID, "retry-disabled-owner1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inactive owner vision retry=%v", err)
	}
	var afterRuns, afterLedger int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_runs WHERE student_id=$1`, fixture.student).Scan(&afterRuns); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_ledger WHERE student_id=$1`, fixture.student).Scan(&afterLedger); err != nil {
		t.Fatal(err)
	}
	if afterRuns != beforeRuns || afterLedger != beforeLedger {
		t.Fatalf("rejected retry mutated state: runs %d->%d ledger %d->%d", beforeRuns, afterRuns, beforeLedger, afterLedger)
	}
}

type fixedRuntimeConfigSource struct {
	text, vision RuntimeProviderConfig
}

func (f fixedRuntimeConfigSource) ForRun(_ context.Context, _ Subject, modality Modality) (RuntimeProviderConfig, error) {
	if modality == ModalityVision {
		return f.vision, nil
	}
	return f.text, nil
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
	rows, err := pool.Query(ctx, `SELECT period_kind,sum(request_delta),sum(token_delta) FROM ai_usage_ledger WHERE run_id=$1 GROUP BY period_kind ORDER BY period_kind`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var kind string
		var requests, tokens int64
		if err = rows.Scan(&kind, &requests, &tokens); err != nil {
			t.Fatal(err)
		}
		if requests != 1 || tokens != 5 {
			t.Fatalf("%s net request=%d token=%d", kind, requests, tokens)
		}
		count++
	}
	if err = rows.Err(); err != nil || count != 2 {
		t.Fatalf("period count=%d err=%v", count, err)
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
		ProviderID: f.provider, KeyVersion: 1, BaseURL: u, ProtocolMode: ProtocolResponses,
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

func TestPostgresRuntimeAnomalySettlementBlocksCrossStudentAdmission(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgresWithMaxConns(t, 4)
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
	studentB := uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,'runtime-student-b','runtime-student-b','student','active','x')`, studentB); err != nil {
		t.Fatal(err)
	}
	const pauseLock int64 = 77199213
	if _, err = pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION aiqa_test_pause_anomaly() RETURNS trigger AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(77199213);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER aiqa_test_pause_anomaly_trigger AFTER UPDATE OF quota_blocked_at ON ai_models
FOR EACH ROW WHEN (OLD.quota_blocked_at IS NULL AND NEW.quota_blocked_at IS NOT NULL)
EXECUTE FUNCTION aiqa_test_pause_anomaly()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS aiqa_test_pause_anomaly_trigger ON ai_models; DROP FUNCTION IF EXISTS aiqa_test_pause_anomaly()`)
	})
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_lock($1)`, pauseLock); err != nil {
		t.Fatal(err)
	}
	unlocked := false
	defer func() {
		if !unlocked {
			_, _ = blocker.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, pauseLock)
		}
	}()

	settleDone := make(chan error, 1)
	go func() {
		_, settleErr := store.SucceedRun(ctx, fixture.student, run.ID, "answer", TerminalUsage{
			InputTokens: 10, OutputTokens: 5, CostMicroUSD: 99, UsageSource: "upstream",
		}, now.Add(time.Second))
		settleDone <- settleErr
	}()
	waitForDatabaseWait(t, ctx, blocker, "advisory", "UPDATE ai_models SET quota_blocked_at")

	admissionDone := make(chan error, 1)
	go func() {
		other := fixture.admission()
		other.StudentID = studentB
		other.IdempotencyKey = "cross-student-admit-1"
		_, _, admissionErr := store.AdmitRun(ctx, other)
		admissionDone <- admissionErr
	}()
	waitForDatabaseWait(t, ctx, blocker, "transactionid", "FROM ai_models WHERE id")

	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_unlock($1)`, pauseLock); err != nil {
		t.Fatal(err)
	}
	unlocked = true
	if err = <-settleDone; err != nil {
		t.Fatal(err)
	}
	if err = <-admissionDone; !errors.Is(err, ErrAIDisabled) {
		t.Fatalf("cross-student admission after anomaly=%v", err)
	}
}

func waitForDatabaseWait(t *testing.T, ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, event, queryFragment string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var found bool
		err := querier.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event=$1 AND query LIKE '%'||$2||'%'
)`, event, queryFragment).Scan(&found)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("database wait not observed: event=%s query=%s", event, queryFragment)
}
