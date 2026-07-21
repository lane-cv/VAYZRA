package database_test

import (
	"context"
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
	t.Cleanup(func() { _, _ = provider.UpTo(context.Background(), 12) })
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
