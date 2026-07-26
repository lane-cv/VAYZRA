package database_test

import (
	"context"
	"testing"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestAIRunProviderKeyVersionMigrationApplied(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var applied, present bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=18 AND is_applied),
       EXISTS(
         SELECT 1 FROM information_schema.columns
         WHERE table_schema='public' AND table_name='ai_runs'
           AND column_name='provider_key_version' AND is_nullable='NO'
       )`).Scan(&applied, &present); err != nil {
		t.Fatal(err)
	}
	if !applied || !present {
		t.Fatalf("migration applied=%t provider_key_version_present=%t", applied, present)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 17); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT NOT EXISTS(
  SELECT 1 FROM information_schema.columns
  WHERE table_schema='public' AND table_name='ai_runs' AND column_name='provider_key_version'
)`).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("down migration retained provider_key_version")
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var allBackfilled bool
	if err := pool.QueryRow(ctx, `
SELECT COALESCE(bool_and(r.provider_key_version=p.key_version),true)
FROM ai_runs r JOIN ai_providers p ON p.id=r.provider_id`).Scan(&allBackfilled); err != nil {
		t.Fatal(err)
	}
	if !allBackfilled {
		t.Fatal("up migration did not backfill provider key versions")
	}
}
