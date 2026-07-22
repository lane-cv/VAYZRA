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

var _ = json.RawMessage{}
var _ = time.Second
