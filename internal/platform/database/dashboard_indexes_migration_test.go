package database_test

import (
	"context"
	"strings"
	"testing"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestDashboardIndexesMigrationContracts(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	expected := map[string]string{
		"audit_logs_dashboard_latest_idx":              "(occurred_at DESC, id DESC)",
		"ai_runs_dashboard_daily_idx":                  "(created_at)",
		"ai_runs_dashboard_failed_idx":                 "(completed_at)",
		"ai_runs_dashboard_unknown_idx":                "(status)",
		"file_processing_jobs_dashboard_failed_idx":    "(updated_at)",
		"file_processing_jobs_dashboard_unknown_idx":   "(state)",
		"outbox_events_dashboard_pending_idx":          "(next_attempt_at, lease_until, created_at)",
		"outbox_events_dashboard_terminal_failure_idx": "(published_at)",
		"backup_runs_dashboard_finished_idx":           "(finished_at DESC, id DESC)",
		"backup_runs_dashboard_remote_finished_idx":    "(finished_at DESC, id DESC)",
		"restore_verifications_dashboard_finished_idx": "(finished_at DESC, id DESC)",
		"restore_verifications_dashboard_unknown_idx":  "(finished_at)",
	}
	rows, err := pool.Query(ctx, `
SELECT indexname,indexdef
FROM pg_indexes
WHERE schemaname='public' AND indexname=ANY($1)
ORDER BY indexname`,
		mapKeys(expected),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := make(map[string]string, len(expected))
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		found[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(found) != len(expected) {
		t.Fatalf("dashboard indexes=%v want=%v", found, expected)
	}
	for name, fragment := range expected {
		if !strings.Contains(found[name], fragment) {
			t.Fatalf("index %s definition=%q missing %q", name, found[name], fragment)
		}
	}
	aiDailyDefinition := found["ai_runs_dashboard_daily_idx"]
	if strings.Contains(strings.ToUpper(aiDailyDefinition), " INCLUDE ") ||
		strings.Contains(aiDailyDefinition, "updated_at") ||
		strings.Contains(aiDailyDefinition, "started_at") ||
		strings.Contains(aiDailyDefinition, "completed_at") {
		t.Fatalf(
			"AI daily index must preserve HOT updates, definition=%q",
			aiDailyDefinition,
		)
	}

	var applied bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM goose_db_version
  WHERE version_id=22 AND is_applied
)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("dashboard index migration 22 was not applied")
	}
}

func TestDashboardIndexesMigrationDownTo21(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 21); err != nil {
		t.Fatal(err)
	}

	var indexes int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM pg_indexes
WHERE schemaname='public'
  AND indexname LIKE '%_dashboard_%_idx'`).
		Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 0 {
		t.Fatalf("dashboard indexes after down=%d", indexes)
	}
}

func mapKeys(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}
