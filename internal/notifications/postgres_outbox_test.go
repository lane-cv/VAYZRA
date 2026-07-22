package notifications

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresOutboxClaimIsExclusiveAndLeaseCanBeTakenOver(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,kind,payload,dedupe_key) VALUES($1,'lesson.published','{}',$2)`, id, "claim:"+id.String()); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresOutboxStore(pool)
	first, err := store.Claim(ctx, "one")
	if err != nil || len(first) != 1 || first[0].Attempts != 1 {
		t.Fatalf("first=%v err=%v", first, err)
	}
	second, err := store.Claim(ctx, "two")
	if err != nil || len(second) != 0 {
		t.Fatalf("second=%v err=%v", second, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE outbox_events SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	second, err = store.Claim(ctx, "two")
	if err != nil || len(second) != 1 || second[0].Attempts != 2 {
		t.Fatalf("takeover=%v err=%v", second, err)
	}
	if err = store.Complete(ctx, id, "one"); err == nil {
		t.Fatal("stale owner completed event")
	}
}

func TestPostgresOutboxConcurrentClaimersHaveOneExclusiveOwner(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,kind,payload,dedupe_key) VALUES($1,'lesson.published','{}',$2)`, id, "concurrent:"+id.String()); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresOutboxStore(pool)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan []OutboxEvent, 2)
	errs := make(chan error, 2)
	for _, owner := range []string{"replica-a", "replica-b"} {
		go func(owner string) {
			defer wg.Done()
			<-start
			events, err := store.Claim(ctx, owner)
			results <- events
			errs <- err
		}(owner)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	total := 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for events := range results {
		total += len(events)
	}
	if total != 1 {
		t.Fatalf("claimed total=%d, want 1", total)
	}
}

func TestPostgresOutboxTerminalCleanupSkipsLockedRowsAndClaimsUnrelatedWork(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	terminalID, readyID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,kind,payload,dedupe_key,attempts,next_attempt_at) VALUES
 ($1,'lesson.published','{}',$2,$3,clock_timestamp()),($4,'lesson.published','{}',$5,0,clock_timestamp())`, terminalID, "terminal:"+terminalID.String(), OutboxMaxAttempts, readyID, "ready:"+readyID.String()); err != nil {
		t.Fatal(err)
	}
	locker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = locker.Exec(ctx, `SELECT id FROM outbox_events WHERE id=$1 FOR UPDATE`, terminalID); err != nil {
		t.Fatal(err)
	}
	type claimResult struct {
		events []OutboxEvent
		err    error
	}
	result := make(chan claimResult, 1)
	go func() {
		events, err := NewPostgresOutboxStore(pool).Claim(ctx, "nonblocking-worker")
		result <- claimResult{events, err}
	}()
	select {
	case got := <-result:
		if got.err != nil || len(got.events) != 1 || got.events[0].ID != readyID {
			_ = locker.Rollback(ctx)
			t.Fatalf("claim=%v err=%v", got.events, got.err)
		}
	case <-time.After(500 * time.Millisecond):
		_ = locker.Rollback(ctx)
		<-result
		t.Fatal("claim blocked behind locked terminal row")
	}
	if err = locker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = NewPostgresOutboxStore(pool).Claim(ctx, "cleanup-worker"); err != nil {
		t.Fatal(err)
	}
	var terminal bool
	var category string
	if err = pool.QueryRow(ctx, `SELECT published_at IS NOT NULL,last_error_category FROM outbox_events WHERE id=$1`, terminalID).Scan(&terminal, &category); err != nil || !terminal || category != "max_attempts" {
		t.Fatalf("terminal=%t category=%q err=%v", terminal, category, err)
	}
}

func TestPostgresOutboxDeliversSelectedAudienceOnceAndSkipsDisabled(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	active := insertNotificationUser(t, pool, "student")
	disabled := insertNotificationUser(t, pool, "student")
	lesson, _ := insertPublishedLessonOutboxFixture(t, pool, "selected", []uuid.UUID{active, disabled})
	if _, err := pool.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, disabled); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresOutboxStore(pool)
	events, err := store.Claim(ctx, "worker")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if err = store.DeliverLessonPublication(ctx, events[0], "worker"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, lesson).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	var recipient uuid.UUID
	if err = pool.QueryRow(ctx, `SELECT recipient_user_id FROM notifications WHERE target_id=$1`, lesson).Scan(&recipient); err != nil || recipient != active {
		t.Fatalf("recipient=%s err=%v", recipient, err)
	}
	// A retry after a simulated lost acknowledgement remains exactly once.
	if _, err = pool.Exec(ctx, `UPDATE outbox_events SET published_at=NULL,lease_owner='worker',lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1`, events[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = store.DeliverLessonPublication(ctx, events[0], "worker"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, lesson).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry count=%d err=%v", count, err)
	}
}

func TestPostgresOutboxAllAudienceAndWithdrawnRevisionSafety(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	insertNotificationUser(t, pool, "student")
	insertNotificationUser(t, pool, "student")
	lesson, _ := insertPublishedLessonOutboxFixture(t, pool, "all", nil)
	store := NewPostgresOutboxStore(pool)
	events, err := store.Claim(ctx, "all-worker")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if err = store.DeliverLessonPublication(ctx, events[0], "all-worker"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, lesson).Scan(&count); err != nil || count < 2 {
		t.Fatalf("all count=%d err=%v", count, err)
	}

	withdrawnLesson, _ := insertPublishedLessonOutboxFixture(t, pool, "all", nil)
	if _, err = pool.Exec(ctx, `UPDATE lessons SET published_revision_id=NULL WHERE id=$1`, withdrawnLesson); err != nil {
		t.Fatal(err)
	}
	events, err = store.Claim(ctx, "withdraw-worker")
	if err != nil || len(events) != 1 {
		t.Fatalf("withdraw events=%v err=%v", events, err)
	}
	if err = store.DeliverLessonPublication(ctx, events[0], "withdraw-worker"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, withdrawnLesson).Scan(&count); err != nil || count != 0 {
		t.Fatalf("withdrawn count=%d err=%v", count, err)
	}
}

func TestLessonPublicationDeliveryUsesCompleteActiveCatalogChain(t *testing.T) {
	for _, level := range []string{"lesson", "chapter", "subject", "term", "grade"} {
		t.Run(level, func(t *testing.T) {
			ctx := context.Background()
			pool := integration.StartPostgres(t)
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
				t.Fatal(err)
			}
			insertNotificationUser(t, pool, "student")
			lesson, _ := insertPublishedLessonOutboxFixture(t, pool, "all", nil)
			archiveCatalogLevel(t, pool, lesson, level)
			store := NewPostgresOutboxStore(pool)
			events, err := store.Claim(ctx, "archive-"+level)
			if err != nil || len(events) != 1 {
				t.Fatalf("events=%v err=%v", events, err)
			}
			if err = store.DeliverLessonPublication(ctx, events[0], "archive-"+level); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1 OR target_path LIKE '%'||$1::text||'%'`, lesson).Scan(&count); err != nil || count != 0 {
				t.Fatalf("archived %s leaked lesson notification count=%d err=%v", level, count, err)
			}
		})
	}
}

func TestLessonDeliveryAndWithdrawalSerializeOnLessonLock(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	student := insertNotificationUser(t, pool, "student")
	lesson, _ := insertPublishedLessonOutboxFixture(t, pool, "selected", []uuid.UUID{student})
	store := NewPostgresOutboxStore(pool)
	events, err := store.Claim(ctx, "lock-worker")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	store.afterLessonLock = func() { close(locked); <-release }
	delivered := make(chan error, 1)
	go func() { delivered <- store.DeliverLessonPublication(ctx, events[0], "lock-worker") }()
	<-locked
	withdrawn := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, `UPDATE lessons SET published_revision_id=NULL WHERE id=$1`, lesson)
		withdrawn <- err
	}()
	select {
	case err := <-withdrawn:
		close(release)
		t.Fatalf("withdrawal bypassed delivery lesson lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err = <-delivered; err != nil {
		t.Fatal(err)
	}
	if err = <-withdrawn; err != nil {
		t.Fatal(err)
	}
	var current *uuid.UUID
	var count int
	if err = pool.QueryRow(ctx, `SELECT published_revision_id FROM lessons WHERE id=$1`, lesson).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, lesson).Scan(&count); err != nil || current != nil || count != 1 {
		t.Fatalf("serialized final current=%v notifications=%d err=%v", current, count, err)
	}
}

func TestLessonDeliveryWaitsForEarlierWithdrawalAndSkipsNotification(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	student := insertNotificationUser(t, pool, "student")
	lesson, _ := insertPublishedLessonOutboxFixture(t, pool, "selected", []uuid.UUID{student})
	store := NewPostgresOutboxStore(pool)
	events, err := store.Claim(ctx, "withdraw-first")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	withdrawTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = withdrawTx.Exec(ctx, `UPDATE lessons SET published_revision_id=NULL WHERE id=$1`, lesson); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan error, 1)
	go func() { delivered <- store.DeliverLessonPublication(ctx, events[0], "withdraw-first") }()
	select {
	case err = <-delivered:
		_ = withdrawTx.Rollback(ctx)
		t.Fatalf("delivery bypassed withdrawal lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = withdrawTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-delivered; err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, lesson).Scan(&count); err != nil || count != 0 {
		t.Fatalf("withdraw-first notifications=%d err=%v", count, err)
	}
}

func TestPostgresOutboxFailureBackoffAndPermanentTerminalState(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,kind,payload,dedupe_key) VALUES($1,'lesson.published','{}',$2)`, id, "retry:"+id.String()); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresOutboxStore(pool)
	events, err := store.Claim(ctx, "retry-worker")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	before := time.Now()
	if err = store.Fail(ctx, id, "retry-worker", "delivery_failed", false); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var next time.Time
	var owner *string
	if err = pool.QueryRow(ctx, `SELECT attempts,next_attempt_at,lease_owner FROM outbox_events WHERE id=$1`, id).Scan(&attempts, &next, &owner); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || owner != nil || next.Before(before.Add(500*time.Millisecond)) {
		t.Fatalf("attempts=%d next=%s owner=%v", attempts, next, owner)
	}
	if _, err = pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	events, err = store.Claim(ctx, "retry-worker")
	if err != nil || len(events) != 1 || events[0].Attempts != 2 {
		t.Fatalf("retry events=%v err=%v", events, err)
	}
	if err = store.Fail(ctx, id, "retry-worker", "payload_invalid", true); err != nil {
		t.Fatal(err)
	}
	var terminal bool
	var category string
	if err = pool.QueryRow(ctx, `SELECT published_at IS NOT NULL,last_error_category FROM outbox_events WHERE id=$1`, id).Scan(&terminal, &category); err != nil || !terminal || category != "payload_invalid" {
		t.Fatalf("terminal=%t category=%q err=%v", terminal, category, err)
	}
}

func TestLessonPublicationDeliveryRollsBackInsertsWhenCompletionCrashes(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM outbox_events WHERE published_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	student := insertNotificationUser(t, pool, "student")
	lesson, _ := insertPublishedLessonOutboxFixture(t, pool, "selected", []uuid.UUID{student})
	store := NewPostgresOutboxStore(pool)
	events, err := store.Claim(ctx, "crash-worker")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if _, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION fail_outbox_completion() RETURNS trigger AS $$ BEGIN IF OLD.published_at IS NULL AND NEW.published_at IS NOT NULL THEN RAISE EXCEPTION 'simulated crash'; END IF; RETURN NEW; END; $$ LANGUAGE plpgsql; CREATE TRIGGER fail_outbox_completion BEFORE UPDATE ON outbox_events FOR EACH ROW EXECUTE FUNCTION fail_outbox_completion()`); err != nil {
		t.Fatal(err)
	}
	if err = store.DeliverLessonPublication(ctx, events[0], "crash-worker"); err == nil {
		t.Fatal("expected simulated completion crash")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, lesson).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back notifications=%d err=%v", count, err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_outbox_completion ON outbox_events; DROP FUNCTION fail_outbox_completion()`); err != nil {
		t.Fatal(err)
	}
	if err = store.DeliverLessonPublication(ctx, events[0], "crash-worker"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, lesson).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry notifications=%d err=%v", count, err)
	}
}

func insertPublishedLessonOutboxFixture(t *testing.T, pool *pgxpool.Pool, mode string, students []uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var admin uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL LIMIT 1`).Scan(&admin); err != nil {
		admin = insertNotificationUser(t, pool, "admin")
	}
	grade, term, subject, chapter, lesson, revision := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO grades(id,name) VALUES($1,$2)`, []any{grade, "G " + uuid.NewString()}},
		{`INSERT INTO terms(id,grade_id,name) VALUES($1,$2,$3)`, []any{term, grade, "T " + uuid.NewString()}},
		{`INSERT INTO subjects(id,term_id,name) VALUES($1,$2,$3)`, []any{subject, term, "S " + uuid.NewString()}},
		{`INSERT INTO chapters(id,subject_id,name) VALUES($1,$2,$3)`, []any{chapter, subject, "C " + uuid.NewString()}},
		{`INSERT INTO lessons(id,chapter_id) VALUES($1,$2)`, []any{lesson, chapter}},
		{`INSERT INTO lesson_revisions(id,lesson_id,version,source_draft_version,title,published_by) VALUES($1,$2,1,1,'Notification lesson',$3)`, []any{revision, lesson, admin}},
		{`INSERT INTO lesson_revision_audiences(revision_id,mode) VALUES($1,$2)`, []any{revision, mode}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, student := range students {
		if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audience_users(revision_id,user_id) VALUES($1,$2)`, revision, student); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_finalizations(revision_id,lesson_id) VALUES($1,$2)`, revision, lesson); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lessons SET published_revision_id=$1 WHERE id=$2`, revision, lesson); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"schemaVersion": 1, "lessonId": lesson, "revisionId": revision})
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(kind,payload,dedupe_key) VALUES('lesson.published',$1,$2)`, payload, "lesson.published:"+revision.String()); err != nil {
		t.Fatal(err)
	}
	return lesson, revision
}

func archiveCatalogLevel(t *testing.T, pool *pgxpool.Pool, lesson uuid.UUID, level string) {
	t.Helper()
	ctx := context.Background()
	var query string
	switch level {
	case "lesson":
		query = `UPDATE lessons SET archived_at=clock_timestamp() WHERE id=$1`
	case "chapter":
		query = `UPDATE chapters SET archived_at=clock_timestamp() WHERE id=(SELECT chapter_id FROM lessons WHERE id=$1)`
	case "subject":
		query = `UPDATE subjects SET archived_at=clock_timestamp() WHERE id=(SELECT c.subject_id FROM lessons l JOIN chapters c ON c.id=l.chapter_id WHERE l.id=$1)`
	case "term":
		query = `UPDATE terms SET archived_at=clock_timestamp() WHERE id=(SELECT s.term_id FROM lessons l JOIN chapters c ON c.id=l.chapter_id JOIN subjects s ON s.id=c.subject_id WHERE l.id=$1)`
	case "grade":
		query = `UPDATE grades SET archived_at=clock_timestamp() WHERE id=(SELECT t.grade_id FROM lessons l JOIN chapters c ON c.id=l.chapter_id JOIN subjects s ON s.id=c.subject_id JOIN terms t ON t.id=s.term_id WHERE l.id=$1)`
	default:
		t.Fatalf("unknown level %q", level)
	}
	if _, err := pool.Exec(ctx, query, lesson); err != nil {
		t.Fatal(err)
	}
}

var _ = json.RawMessage{}
var _ = time.Second
