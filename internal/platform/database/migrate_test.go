package database_test

import (
	"context"
	"testing"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestAuthMigrationCreatesConstraints(t *testing.T) {
	pool := integration.StartPostgres(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}

	var count int
	err := pool.QueryRow(context.Background(), `
		select count(*) from information_schema.tables
		where table_schema = 'public' and table_name in
		('users', 'sessions', 'login_events', 'audit_logs')`).Scan(&count)
	if err != nil || count != 4 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestAuthMigrationCreatesStudentPagingIndex(t *testing.T) {
	pool := integration.StartPostgres(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}

	var exists bool
	err := pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1
			FROM pg_indexes
			WHERE schemaname = 'public'
				AND tablename = 'users'
				AND indexname = 'users_students_active_id_idx'
		)`).Scan(&exists)
	if err != nil || !exists {
		t.Fatalf("student paging index exists=%t err=%v", exists, err)
	}
}

func TestAuthMigrationHasUserUniquenessIndexes(t *testing.T) {
	pool := integration.StartPostgres(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}

	var count int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM pg_indexes
		WHERE schemaname = 'public'
			AND tablename = 'users'
			AND indexname IN ('users_username_active_key', 'users_single_admin_key')`).Scan(&count)
	if err != nil || count != 2 {
		t.Fatalf("user uniqueness index count=%d err=%v", count, err)
	}
}

func TestTeachingMigrationCreatesCatalogSchema(t *testing.T) {
	pool := integration.StartPostgres(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}

	var version int
	if err := pool.QueryRow(context.Background(), `
		SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 4 {
		t.Fatalf("migration version=%d err=%v", version, err)
	}

	var count int
	err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name IN
		('grades', 'terms', 'subjects', 'chapters', 'lessons', 'lesson_drafts',
		 'lesson_revisions', 'lesson_draft_audiences', 'lesson_draft_audience_users',
		 'lesson_revision_audiences', 'lesson_revision_audience_users',
		 'lesson_draft_external_videos', 'lesson_revision_external_videos',
		 'outbox_events', 'lesson_progress')`).Scan(&count)
	if err != nil || count != 15 {
		t.Fatalf("teaching table count=%d err=%v", count, err)
	}
}
