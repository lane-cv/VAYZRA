package database_test

import (
	"context"
	"testing"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestQAMigrationDownRemovesSchema(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var applied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=9 AND is_applied)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("qa migration was not applied")
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	t.Cleanup(closeProvider)
	t.Cleanup(func() {
		if _, err := provider.UpTo(context.Background(), 9); err != nil {
			t.Errorf("restore latest migration: %v", err)
		}
	})

	if _, err := provider.DownTo(ctx, 8); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name IN
		('qa_threads','qa_messages','qa_message_files','teacher_notes')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("remaining qa tables=%d", tables)
	}
}
