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
