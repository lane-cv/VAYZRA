package aiqa

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresRunnerLeaseRebuildsStoredSnapshotAndTransitionsBeforeIO(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	runtimeStore := NewPostgresRuntimeStore(pool)
	_, admitted, err := runtimeStore.AdmitRun(ctx, fixture.admission())
	if err != nil {
		t.Fatal(err)
	}
	store := NewPostgresRunnerStore(pool, runnerTestSecretBox{}, nil)
	now := time.Now().UTC()
	leased, err := store.LeaseNext(ctx, "runner-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if leased.Run.ID != admitted.ID || leased.Run.Status != RunStreaming || leased.LeaseOwner != "runner-a" {
		t.Fatalf("leased=%+v", leased)
	}
	if leased.Config.ProviderID != fixture.provider || leased.Config.Model.ID != fixture.model ||
		leased.Config.Model.UpstreamModelID != fixture.cfg.Model.UpstreamModelID ||
		leased.Config.Model.ContextTokens != fixture.cfg.Model.ContextTokens ||
		leased.Config.Prompt.ID != fixture.prompt || leased.Config.Prompt.Version != 1 ||
		leased.Config.Prompt.Body != "runtime prompt" || leased.Request.SystemPrompt != "runtime prompt" ||
		leased.Request.Model != fixture.cfg.Model.UpstreamModelID || len(leased.Request.Turns) != 1 ||
		leased.Request.Turns[0].Text != "question" {
		t.Fatalf("snapshot/request mismatch: config=%+v request=%+v", leased.Config, leased.Request)
	}
	var status RunStatus
	var owner string
	if err = pool.QueryRow(ctx, `SELECT status,lease_owner FROM ai_runs WHERE id=$1`, admitted.ID).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != RunStreaming || owner != "runner-a" {
		t.Fatalf("database status=%s owner=%s", status, owner)
	}
	if _, err = store.LeaseNext(ctx, "runner-b", now, time.Minute); !errors.Is(err, ErrNoRunnableRun) {
		t.Fatalf("second lease=%v", err)
	}
}

func TestPostgresRunnerStreamingCancelIsDeferredToLeaseOwner(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	runtimeStore := NewPostgresRuntimeStore(pool)
	_, run, err := runtimeStore.AdmitRun(ctx, fixture.admission())
	if err != nil {
		t.Fatal(err)
	}
	store := NewPostgresRunnerStore(pool, runnerTestSecretBox{}, nil)
	leased, err := store.LeaseNext(ctx, "runner-cancel", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := runtimeStore.CancelRun(ctx, fixture.student, run.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != RunStreaming {
		t.Fatalf("cancel request prematurely terminalized run: %+v", cancelled)
	}
	if err = store.Heartbeat(ctx, run.ID, "runner-cancel", time.Now().UTC()); !errors.Is(err, ErrCancelRequested) {
		t.Fatalf("heartbeat cancel signal=%v", err)
	}
	if err = store.Fail(ctx, leased, Failure{Status: RunCancelled, ErrorCode: "cancelled", UsageSource: "unknown", TotalMS: 9}); err != nil {
		t.Fatal(err)
	}
	got, err := runtimeStore.GetRun(ctx, fixture.student, run.ID)
	if err != nil || got.Status != RunCancelled {
		t.Fatalf("run=%+v err=%v", got, err)
	}
	var releases, assistants int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_ledger WHERE run_id=$1 AND action='release'`, run.ID).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_messages WHERE trigger_run_id=$1`, run.ID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	if releases != 2 || assistants != 0 {
		t.Fatalf("releases=%d assistants=%d", releases, assistants)
	}
}

func TestPostgresRunnerReconcileExpiredStreamingFailsOnceAndPreservesEvents(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	runtimeStore := NewPostgresRuntimeStore(pool)
	_, run, err := runtimeStore.AdmitRun(ctx, fixture.admission())
	if err != nil {
		t.Fatal(err)
	}
	store := NewPostgresRunnerStore(pool, runnerTestSecretBox{}, nil)
	leased, err := store.LeaseNext(ctx, "dead-runner", time.Now().Add(-time.Minute).UTC(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AppendEvents(ctx, run.ID, leased.LeaseOwner, []RunEvent{{Sequence: 1, Kind: "delta", Delta: "partial", CreatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = store.ReconcileExpired(ctx, now, 10); err != nil {
		t.Fatal(err)
	}
	if err = store.ReconcileExpired(ctx, now.Add(time.Second), 10); err != nil {
		t.Fatal(err)
	}
	got, err := runtimeStore.GetRun(ctx, fixture.student, run.ID)
	if err != nil || got.Status != RunFailed || got.ErrorCode != "runner_lost" {
		t.Fatalf("run=%+v err=%v", got, err)
	}
	var deltas, failures, releases int
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind='delta'),count(*) FILTER (WHERE kind='failed') FROM ai_run_events WHERE run_id=$1`, run.ID).Scan(&deltas, &failures); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_ledger WHERE run_id=$1 AND action='release'`, run.ID).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if deltas != 1 || failures != 1 || releases != 2 {
		t.Fatalf("deltas=%d failures=%d releases=%d", deltas, failures, releases)
	}
}

func TestPostgresRunnerCompletionAtomicallyWritesAssistantUsageAndQuota(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	runtimeStore := NewPostgresRuntimeStore(pool)
	in := fixture.admission()
	in.Reservation.TokenCount = 5
	_, run, err := runtimeStore.AdmitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	store := NewPostgresRunnerStore(pool, runnerTestSecretBox{}, nil)
	leased, err := store.LeaseNext(ctx, "complete-runner", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AppendEvents(ctx, run.ID, leased.LeaseOwner, []RunEvent{
		{Sequence: 1, Kind: "delta", Delta: "final ", CreatedAt: time.Now().UTC()},
		{Sequence: 2, Kind: "delta", Delta: "answer", CreatedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	var assistants int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_messages WHERE trigger_run_id=$1`, run.ID).Scan(&assistants); err != nil || assistants != 0 {
		t.Fatalf("partial assistant count=%d err=%v", assistants, err)
	}
	if err = store.Complete(ctx, leased, Completion{
		Answer: "final answer", InputTokens: 7, OutputTokens: 3, CostMicroUSD: 2,
		UsageSource: "upstream", FinishReason: "stop", FirstByteMS: 4, TotalMS: 8,
	}); err != nil {
		t.Fatal(err)
	}
	var status RunStatus
	var input, output, cost, firstByte, total int64
	var completedEvents, settles, releases int
	if err = pool.QueryRow(ctx, `SELECT status,input_tokens,output_tokens,cost_micro_usd,first_byte_ms,total_ms
FROM ai_runs WHERE id=$1`, run.ID).Scan(&status, &input, &output, &cost, &firstByte, &total); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_messages WHERE trigger_run_id=$1`, run.ID).Scan(&assistants); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_run_events WHERE run_id=$1 AND kind='completed'`, run.ID).Scan(&completedEvents); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action='settle'),count(*) FILTER (WHERE action='release')
FROM ai_usage_ledger WHERE run_id=$1`, run.ID).Scan(&settles, &releases); err != nil {
		t.Fatal(err)
	}
	var blocked bool
	if err = pool.QueryRow(ctx, `SELECT quota_blocked_at IS NOT NULL FROM ai_models WHERE id=$1`, fixture.model).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if status != RunSucceeded || input != 7 || output != 3 || cost != 2 || firstByte != 4 || total != 8 ||
		assistants != 1 || completedEvents != 1 || settles != 2 || releases != 2 || !blocked {
		t.Fatalf("status=%s usage=%d/%d/%d latency=%d/%d assistants=%d events=%d ledger=%d/%d blocked=%t",
			status, input, output, cost, firstByte, total, assistants, completedEvents, settles, releases, blocked)
	}
}

func TestPostgresRuntimeQueuedCancelRemainsImmediate(t *testing.T) {
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
	if err != nil || cancelled.Status != RunCancelled {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
}

type runnerTestSecretBox struct{}

func (runnerTestSecretBox) Seal(uuid.UUID, []byte) (EncryptedSecret, error) {
	return EncryptedSecret{}, errors.New("unused")
}
func (runnerTestSecretBox) Open(uuid.UUID, EncryptedSecret) ([]byte, error) {
	return []byte("runner-secret"), nil
}
