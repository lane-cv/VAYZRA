package aiqa

import (
	"context"
	"errors"
	"io"
	"sync"
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
	leased, err := store.LeaseNext(ctx, "dead-runner", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AppendEvents(ctx, run.ID, leased.LeaseOwner, []RunEvent{{Sequence: 1, Kind: "delta", Delta: "partial", CreatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE ai_runs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, run.ID); err != nil {
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

func TestPostgresRunnerReconcileRechecksRenewedLeaseUnderLock(t *testing.T) {
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
	leased, err := store.LeaseNext(ctx, "renewed-runner", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE ai_runs SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	lockKey := "ai-quota:" + fixture.student.String()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1::text,0))`, lockKey); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended($1::text,0))`, lockKey)
		}
	}()

	reconciled := make(chan error, 1)
	go func() { reconciled <- store.ReconcileExpired(ctx, time.Now().UTC(), 10) }()
	waitForDatabaseWait(t, ctx, pool, "advisory", "pg_advisory_xact_lock")
	newExpiry := time.Now().Add(time.Minute).UTC()
	if err = store.Heartbeat(ctx, run.ID, leased.LeaseOwner, newExpiry); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1::text,0))`, lockKey); err != nil {
		t.Fatal(err)
	}
	locked = false
	if err = <-reconciled; err != nil {
		t.Fatal(err)
	}
	var status RunStatus
	var expiry time.Time
	if err = pool.QueryRow(ctx, `SELECT status,lease_expires_at FROM ai_runs WHERE id=$1`, run.ID).Scan(&status, &expiry); err != nil {
		t.Fatal(err)
	}
	if status != RunStreaming || expiry.Sub(newExpiry) > time.Microsecond || newExpiry.Sub(expiry) > time.Microsecond {
		t.Fatalf("renewed run was reconciled: status=%s expiry=%s want=%s", status, expiry, newExpiry)
	}
}

func TestPostgresRunnerSlowPreparationKeepsLeaseAndLossPreventsReturn(t *testing.T) {
	t.Run("slow preparation keeps lease", func(t *testing.T) {
		ctx := context.Background()
		pool := integration.StartPostgres(t)
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		fixture := newRuntimeFixture(t, ctx, pool, 20)
		versionID := insertAttachmentVersion(t, ctx, pool, fixture.student, "ai_attachment", "ready", "text/plain", "slow.txt")
		in := fixture.admission()
		in.Attachments = []AttachmentMetadata{{
			FileVersionID: versionID, DisplayName: "slow.txt", DetectedMIME: "text/plain", Modality: ModalityText, Size: 1,
		}}
		runtimeStore := NewPostgresRuntimeStore(pool)
		_, run, err := runtimeStore.AdmitRun(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		attachments := newBlockingRunnerAttachmentStore()
		store := NewPostgresRunnerStore(pool, runnerTestSecretBox{}, attachments)
		result := make(chan struct {
			leased LeasedRun
			err    error
		}, 1)
		go func() {
			leased, leaseErr := store.LeaseNext(ctx, "slow-prep", time.Now().UTC(), 60*time.Millisecond)
			result <- struct {
				leased LeasedRun
				err    error
			}{leased, leaseErr}
		}()
		<-attachments.started
		time.Sleep(100 * time.Millisecond)
		if err = store.ReconcileExpired(ctx, time.Now().UTC(), 10); err != nil {
			t.Fatal(err)
		}
		close(attachments.release)
		got := <-result
		if got.err != nil || got.leased.Run.ID != run.ID {
			t.Fatalf("lease=%+v err=%v", got.leased, got.err)
		}
		var status RunStatus
		var expires time.Time
		if err = pool.QueryRow(ctx, `SELECT status,lease_expires_at FROM ai_runs WHERE id=$1`, run.ID).Scan(&status, &expires); err != nil {
			t.Fatal(err)
		}
		if status != RunStreaming || !expires.After(time.Now()) {
			t.Fatalf("status=%s expires=%s", status, expires)
		}
	})

	t.Run("lease loss prevents prepared request return", func(t *testing.T) {
		ctx := context.Background()
		pool := integration.StartPostgres(t)
		if err := database.Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		fixture := newRuntimeFixture(t, ctx, pool, 20)
		versionID := insertAttachmentVersion(t, ctx, pool, fixture.student, "ai_attachment", "ready", "text/plain", "lost.txt")
		in := fixture.admission()
		in.Attachments = []AttachmentMetadata{{
			FileVersionID: versionID, DisplayName: "lost.txt", DetectedMIME: "text/plain", Modality: ModalityText, Size: 1,
		}}
		runtimeStore := NewPostgresRuntimeStore(pool)
		_, run, err := runtimeStore.AdmitRun(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		attachments := newBlockingRunnerAttachmentStore()
		store := NewPostgresRunnerStore(pool, runnerTestSecretBox{}, attachments)
		result := make(chan error, 1)
		go func() {
			_, leaseErr := store.LeaseNext(ctx, "losing-prep", time.Now().UTC(), 60*time.Millisecond)
			result <- leaseErr
		}()
		<-attachments.started
		if _, err = pool.Exec(ctx, `UPDATE ai_runs SET lease_owner='stolen-owner',lease_expires_at=now()+interval '1 minute',heartbeat_at=now() WHERE id=$1`, run.ID); err != nil {
			t.Fatal(err)
		}
		select {
		case err = <-result:
		case <-time.After(time.Second):
			t.Fatal("preparation did not stop after lease loss")
		}
		if !errors.Is(err, ErrNoRunnableRun) {
			t.Fatalf("lease loss error=%v", err)
		}
	})
}

func TestPostgresRunnerReconcileExpiredQueuedRemainsClaimableAndTerminalUntouched(t *testing.T) {
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
	if err = store.ReconcileExpired(ctx, time.Now().Add(time.Hour), 10); err != nil {
		t.Fatal(err)
	}
	leased, err := store.LeaseNext(ctx, "queued-reclaim", time.Now().UTC(), time.Minute)
	if err != nil || leased.Run.ID != run.ID {
		t.Fatalf("queued lease=%+v err=%v", leased, err)
	}
	if err = store.Fail(ctx, leased, Failure{Status: RunFailed, ErrorCode: "terminal-test", UsageSource: "unknown"}); err != nil {
		t.Fatal(err)
	}
	var beforeEvents, beforeReleases int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_run_events WHERE run_id=$1`, run.ID).Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_ledger WHERE run_id=$1 AND action='release'`, run.ID).Scan(&beforeReleases); err != nil {
		t.Fatal(err)
	}
	if err = store.ReconcileExpired(ctx, time.Now().Add(24*time.Hour), 10); err != nil {
		t.Fatal(err)
	}
	var status RunStatus
	var afterEvents, afterReleases int
	if err = pool.QueryRow(ctx, `SELECT status FROM ai_runs WHERE id=$1`, run.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_run_events WHERE run_id=$1`, run.ID).Scan(&afterEvents); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_ledger WHERE run_id=$1 AND action='release'`, run.ID).Scan(&afterReleases); err != nil {
		t.Fatal(err)
	}
	if status != RunFailed || beforeEvents != afterEvents || beforeReleases != afterReleases {
		t.Fatalf("terminal changed: status=%s events=%d/%d releases=%d/%d", status, beforeEvents, afterEvents, beforeReleases, afterReleases)
	}
}

func TestPostgresRunnerConcurrentClaimsSkipLockedRows(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	runtimeStore := NewPostgresRuntimeStore(pool)
	_, first, err := runtimeStore.AdmitRun(ctx, fixture.admission())
	if err != nil {
		t.Fatal(err)
	}
	secondStudent := uuid.New()
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash)
VALUES($1,$2,$2,'student','active','x')`, secondStudent, "runner-second-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	secondAdmission := fixture.admission()
	secondAdmission.StudentID = secondStudent
	secondAdmission.IdempotencyKey = "runner-second-admission"
	_, second, err := runtimeStore.AdmitRun(ctx, secondAdmission)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blockerOpen := true
	defer func() {
		if blockerOpen {
			_ = blocker.Rollback(context.Background())
		}
	}()
	var lockedID uuid.UUID
	if err = blocker.QueryRow(ctx, `SELECT id FROM ai_runs WHERE status='queued' ORDER BY created_at,id FOR UPDATE LIMIT 1`).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	if lockedID != first.ID {
		t.Fatalf("locked=%s want oldest=%s", lockedID, first.ID)
	}

	store := NewPostgresRunnerStore(pool, runnerTestSecretBox{}, nil)
	result := make(chan struct {
		leased LeasedRun
		err    error
	}, 1)
	go func() {
		leased, leaseErr := store.LeaseNext(ctx, "skip-second", time.Now().UTC(), time.Minute)
		result <- struct {
			leased LeasedRun
			err    error
		}{leased, leaseErr}
	}()
	select {
	case got := <-result:
		if got.err != nil || got.leased.Run.ID != second.ID {
			t.Fatalf("claim while oldest locked=%s err=%v want=%s", got.leased.Run.ID, got.err, second.ID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("LeaseNext blocked on oldest row instead of using SKIP LOCKED")
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	blockerOpen = false
	oldest, err := store.LeaseNext(ctx, "claim-oldest", time.Now().UTC(), time.Minute)
	if err != nil || oldest.Run.ID != first.ID {
		t.Fatalf("oldest after unlock=%s err=%v want=%s", oldest.Run.ID, err, first.ID)
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

type blockingRunnerAttachmentStore struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRunnerAttachmentStore() *blockingRunnerAttachmentStore {
	return &blockingRunnerAttachmentStore{started: make(chan struct{}), release: make(chan struct{})}
}
func (s *blockingRunnerAttachmentStore) ValidateForAI(context.Context, uuid.UUID, uuid.UUID, []AttachmentInput) ([]AttachmentMetadata, error) {
	return nil, errors.New("unused")
}
func (s *blockingRunnerAttachmentStore) LoadAIText(ctx context.Context, _, _ uuid.UUID) (string, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return "extracted attachment", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (s *blockingRunnerAttachmentStore) OpenAIImage(context.Context, uuid.UUID, uuid.UUID) (io.ReadCloser, string, int64, error) {
	return nil, "", 0, errors.New("unused")
}
