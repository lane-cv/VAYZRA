package database_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestNotificationMigrationPreservesLegacyOutboxRowsUpAndDown(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	t.Cleanup(closeProvider)
	t.Cleanup(func() { _, _ = provider.UpTo(context.Background(), 13) })
	if _, err := provider.DownTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,kind,payload) VALUES($1,'legacy.test','{}')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 12); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var next bool
	if err := pool.QueryRow(ctx, `SELECT attempts,next_attempt_at IS NOT NULL FROM outbox_events WHERE id=$1`, id).Scan(&attempts, &next); err != nil || attempts != 0 || !next {
		t.Fatalf("attempts=%d next=%v err=%v", attempts, next, err)
	}
	var partial bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname='notifications_unread_idx' AND indexdef LIKE '%WHERE (read_at IS NULL)%')`).Scan(&partial); err != nil || !partial {
		t.Fatalf("partial=%v err=%v", partial, err)
	}
	if _, err := provider.DownTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM outbox_events WHERE id=$1)`, id).Scan(&exists); err != nil || !exists {
		t.Fatalf("row exists=%v err=%v", exists, err)
	}
	var notificationTable bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='notifications')`).Scan(&notificationTable); err != nil || notificationTable {
		t.Fatalf("notification table=%v err=%v", notificationTable, err)
	}
}

func TestNotificationMigrationEnforcesContentRecipientImmutabilityAndOutboxLeases(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	first, second := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{first, second} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'Notify constraints','student','active','hash')`, id, "notify_constraint_"+id.String()); err != nil {
			t.Fatal(err)
		}
	}
	target := uuid.New()
	insert := func(recipient uuid.UUID, kind, title, summary, path, dedupe string) (uuid.UUID, error) {
		id := uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO notifications(id,recipient_user_id,kind,title,summary,target_type,target_id,target_path,dedupe_key) VALUES($1,$2,$3,$4,$5,'qa_thread',$6,$7,$8)`, id, recipient, kind, title, summary, target, path, dedupe)
		return id, err
	}
	validKey := "qa-replied:" + uuid.NewString()
	validID, err := insert(first, "qa_replied", "Teacher reply", "Your teacher replied to a question.", "/student/questions/"+target.String(), validKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                            string
		recipient                       uuid.UUID
		kind, title, summary, path, key string
	}{{"foreign recipient", uuid.New(), "qa_replied", "Teacher reply", "Safe", "/student/questions/x", "foreign-recipient:" + uuid.NewString()}, {"kind", first, "bad", "Teacher reply", "Safe", "/student/questions/x", "invalid-kind:" + uuid.NewString()}, {"empty title", first, "qa_replied", "", "Safe", "/student/questions/x", "empty-title:" + uuid.NewString()}, {"long title", first, "qa_replied", strings.Repeat("t", 161), "Safe", "/student/questions/x", "long-title:" + uuid.NewString()}, {"empty summary", first, "qa_replied", "Teacher reply", "", "/student/questions/x", "empty-summary:" + uuid.NewString()}, {"long summary", first, "qa_replied", "Teacher reply", strings.Repeat("s", 241), "/student/questions/x", "long-summary:" + uuid.NewString()}, {"path", first, "qa_replied", "Teacher reply", "Safe", "relative", "invalid-path:" + uuid.NewString()}, {"short dedupe", first, "qa_replied", "Teacher reply", "Safe", "/student/questions/x", "short"}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := insert(tc.recipient, tc.kind, tc.title, tc.summary, tc.path, tc.key); err == nil {
				t.Fatal("constraint accepted invalid row")
			}
		})
	}
	if _, err := insert(first, "qa_replied", "Teacher reply", "Safe", "/student/questions/x", validKey); err == nil {
		t.Fatal("same-recipient dedupe succeeded")
	}
	if _, err := insert(second, "qa_replied", "Teacher reply", "Safe", "/student/questions/x", validKey); err != nil {
		t.Fatalf("cross-recipient dedupe: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM notifications WHERE id=$1`, validID); err == nil {
		t.Fatal("notification delete succeeded")
	}
	if _, err := pool.Exec(ctx, `UPDATE notifications SET read_at=clock_timestamp() WHERE id=$1`, validID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE notifications SET read_at=NULL WHERE id=$1`, validID); err == nil {
		t.Fatal("read_at reverted")
	}
	if _, err := pool.Exec(ctx, `UPDATE notifications SET read_at=clock_timestamp() WHERE id=$1`, validID); err == nil {
		t.Fatal("read_at changed twice")
	}
	outbox := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,kind,payload) VALUES($1,'constraint.test','{}')`, outbox); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE outbox_events SET lease_owner='worker' WHERE id=$1`, `UPDATE outbox_events SET lease_until=now() WHERE id=$1`, `UPDATE outbox_events SET attempts=-1 WHERE id=$1`, `UPDATE outbox_events SET attempts=1000001 WHERE id=$1`} {
		if _, err := pool.Exec(ctx, query, outbox); err == nil {
			t.Fatalf("constraint accepted %s", query)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET lease_owner='worker',lease_until=now(),attempts=1 WHERE id=$1`, outbox); err != nil {
		t.Fatal(err)
	}
}
