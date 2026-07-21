package database_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"happylearn.local/app/db/migrations"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestTeachingMigrationDownRemovesTaskOneObjects(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := provider.UpTo(context.Background(), 8); err != nil {
			t.Errorf("restore latest migration: %v", err)
		}
	})
	results, err := provider.DownTo(ctx, 3)
	if err != nil {
		t.Fatalf("rollback teaching migration: %v", err)
	}
	if len(results) != 5 || results[len(results)-1].Source.Version != 4 {
		t.Fatalf("rolled back migrations=%v, want versions 8, 7, 6, 5 then 4", results)
	}
	var tables, routines int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('grades','terms','subjects','chapters','lessons','lesson_drafts','lesson_revisions','lesson_revision_finalizations','lesson_draft_audiences','lesson_draft_audience_users','lesson_revision_audiences','lesson_revision_audience_users','lesson_draft_external_videos','lesson_revision_external_videos','outbox_events','lesson_progress')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname IN ('require_student_audience_member','require_empty_all_audience','reject_lesson_revision_mutation','finalize_lesson_revision','reject_finalized_lesson_revision_child_mutation')`).Scan(&routines); err != nil {
		t.Fatal(err)
	}
	if tables != 0 || routines != 0 {
		t.Fatalf("remaining teaching tables=%d routines=%d", tables, routines)
	}
}
