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
		"ai_runs_dashboard_queue_idx":                  "(status, lease_expires_at, completed_at)",
		"file_processing_jobs_dashboard_queue_idx":     "(state, lease_until, available_at, updated_at)",
		"outbox_events_dashboard_pending_idx":          "(next_attempt_at, lease_until, created_at)",
		"outbox_events_dashboard_terminal_failure_idx": "(published_at)",
		"backup_runs_dashboard_finished_idx":           "(finished_at DESC, id DESC)",
		"restore_verifications_dashboard_finished_idx": "(finished_at DESC, id DESC)",
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

func TestDashboardIndexesSupportFocusedPlans(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		index string
		query string
	}{
		{
			"audit latest",
			"audit_logs_dashboard_latest_idx",
			`SELECT occurred_at FROM audit_logs
			 ORDER BY occurred_at DESC,id DESC LIMIT 10`,
		},
		{
			"AI daily",
			"ai_runs_dashboard_daily_idx",
			`SELECT count(*) FROM ai_runs
			 WHERE created_at >= clock_timestamp()-interval '1 day'`,
		},
		{
			"AI queue",
			"ai_runs_dashboard_queue_idx",
			`SELECT count(*) FROM ai_runs
			 WHERE status='streaming' AND lease_expires_at<clock_timestamp()`,
		},
		{
			"processing queue",
			"file_processing_jobs_dashboard_queue_idx",
			`SELECT count(*) FROM file_processing_jobs
			 WHERE state='running' AND lease_until<clock_timestamp()`,
		},
		{
			"outbox pending",
			"outbox_events_dashboard_pending_idx",
			`SELECT count(*) FROM outbox_events
			 WHERE published_at IS NULL
			   AND next_attempt_at<=clock_timestamp()
			   AND lease_until<=clock_timestamp()`,
		},
		{
			"outbox terminal failure",
			"outbox_events_dashboard_terminal_failure_idx",
			`SELECT count(*) FROM outbox_events
			 WHERE published_at>=clock_timestamp()-interval '15 minutes'
			   AND last_error_category IS NOT NULL`,
		},
		{
			"backup latest",
			"backup_runs_dashboard_finished_idx",
			`SELECT finished_at FROM backup_runs
			 WHERE finished_at IS NOT NULL
			 ORDER BY finished_at DESC,id DESC LIMIT 1`,
		},
		{
			"restore latest",
			"restore_verifications_dashboard_finished_idx",
			`SELECT finished_at FROM restore_verifications
			 WHERE finished_at IS NOT NULL
			 ORDER BY finished_at DESC,id DESC LIMIT 1`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(ctx, `EXPLAIN (COSTS OFF) `+test.query)
			if err != nil {
				t.Fatal(err)
			}
			var plan strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				plan.WriteString(line)
				plan.WriteByte('\n')
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan.String(), test.index) {
				t.Fatalf("plan missing %s:\n%s", test.index, plan.String())
			}
		})
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
