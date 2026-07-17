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
