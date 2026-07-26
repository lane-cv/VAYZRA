package database_test

import (
	"context"
	"testing"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestAIRuntimeMigrationDownRemovesSchema(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var applied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=16 AND is_applied)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("ai runtime migration was not applied")
	}

	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	t.Cleanup(closeProvider)
	if _, err := provider.DownTo(ctx, 15); err != nil {
		t.Fatal(err)
	}

	var tables, aiAccessColumn int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM information_schema.tables
			 WHERE table_schema='public' AND table_name IN
			 ('ai_threads','ai_messages','ai_message_files','ai_runs','ai_run_events','ai_usage_ledger')),
			(SELECT count(*) FROM information_schema.columns
			 WHERE table_schema='public' AND table_name='file_access_logs' AND column_name='ai_message_id')`).Scan(&tables, &aiAccessColumn); err != nil {
		t.Fatal(err)
	}
	if tables != 0 || aiAccessColumn != 0 {
		t.Fatalf("runtime schema remained after down: tables=%d ai_access_column=%d", tables, aiAccessColumn)
	}
}
