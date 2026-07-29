package database_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
	hotColumns := []string{
		"updated_at",
		"started_at",
		"heartbeat_at",
		"lease_expires_at",
	}
	hotRows, err := pool.Query(ctx, `
SELECT index_class.relname,table_attribute.attname
FROM pg_index AS index_metadata
JOIN pg_class AS index_class
  ON index_class.oid=index_metadata.indexrelid
JOIN pg_class AS table_class
  ON table_class.oid=index_metadata.indrelid
JOIN pg_namespace AS table_namespace
  ON table_namespace.oid=table_class.relnamespace
CROSS JOIN LATERAL unnest(index_metadata.indkey)
  WITH ORDINALITY AS indexed_column(attribute_number,position)
JOIN pg_attribute AS table_attribute
  ON table_attribute.attrelid=table_class.oid
 AND table_attribute.attnum=indexed_column.attribute_number
WHERE table_namespace.nspname='public'
  AND table_class.relname='ai_runs'
  AND index_class.relname LIKE 'ai_runs_dashboard_%'
  AND table_attribute.attname=ANY($1)
ORDER BY index_class.relname,indexed_column.position`,
		hotColumns,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer hotRows.Close()
	var hotIndexColumns []string
	for hotRows.Next() {
		var index, column string
		if err := hotRows.Scan(&index, &column); err != nil {
			t.Fatal(err)
		}
		hotIndexColumns = append(hotIndexColumns, index+"."+column)
	}
	if err := hotRows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(hotIndexColumns) != 0 {
		t.Fatalf("AI dashboard indexes block hot updates: %v", hotIndexColumns)
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

func TestDashboardAIIndexesPreserveQueuedHeartbeatHOTUpdates(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var runID uuid.UUID
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	err = tx.QueryRow(ctx, `
INSERT INTO ai_runs(
  id,thread_id,student_id,trigger_message_id,attempt_no,idempotency_key,
  status,provider_id,provider_key_version,provider_base_url,protocol_mode,
  model_id,upstream_model_id,modality,context_window_tokens,
  max_output_tokens,image_quota_tokens,
  input_price_micro_usd_per_million_tokens,
  output_price_micro_usd_per_million_tokens,
  prompt_id,prompt_subject,prompt_version,prompt_sha256,
  connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,
  total_timeout_ms,reserved_request_count,reserved_token_count,
  quota_day_key,quota_month_key,estimator_version,created_at,updated_at
)
VALUES(
  gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),
  1,'dashboard-hot-'||gen_random_uuid()::text,
  'queued',gen_random_uuid(),1,'https://dashboard.invalid/v1',
  'chat_completions',gen_random_uuid(),'dashboard-model','text',
  8192,1024,1024,0,0,gen_random_uuid(),'math',1,repeat('a',64),
  1000,30000,30000,120000,1,1024,'2026-07-30','2026-07',1,
  clock_timestamp()-interval '1 second',
  clock_timestamp()-interval '1 second'
)
RETURNING id`).Scan(&runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM ai_runs WHERE id=$1`, runID); err != nil {
			t.Errorf("clean HOT probe run: %v", err)
		}
	})

	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(
		ctx,
		`SELECT pg_stat_reset_single_table_counters('public.ai_runs'::regclass)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `SELECT pg_stat_force_next_flush()`); err != nil {
		t.Fatal(err)
	}
	baselineUpdates, baselineHOT := dashboardAIUpdateStats(t, ctx, connection)
	tag, err := connection.Exec(ctx, `
UPDATE ai_runs
SET updated_at=updated_at+interval '1 microsecond'
WHERE id=$1 AND status='queued'`,
		runID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("queued HOT probe updates=%d want=1", tag.RowsAffected())
	}
	if _, err := connection.Exec(ctx, `SELECT pg_stat_force_next_flush()`); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		updates, hot := dashboardAIUpdateStats(t, ctx, connection)
		if updates > baselineUpdates {
			if updates-baselineUpdates != 1 || hot-baselineHOT != 1 {
				t.Fatalf(
					"queued heartbeat updates=%d hot=%d want updates=1 hot=1",
					updates-baselineUpdates,
					hot-baselineHOT,
				)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"queued heartbeat stats did not flush: updates=%d hot=%d baseline=%d/%d",
				updates,
				hot,
				baselineUpdates,
				baselineHOT,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func dashboardAIUpdateStats(
	t *testing.T,
	ctx context.Context,
	connection *pgxpool.Conn,
) (int64, int64) {
	t.Helper()
	if _, err := connection.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
		t.Fatal(err)
	}
	var updates, hot int64
	if err := connection.QueryRow(ctx, `
SELECT n_tup_upd,n_tup_hot_upd
FROM pg_stat_user_tables
WHERE schemaname='public' AND relname='ai_runs'`).
		Scan(&updates, &hot); err != nil {
		t.Fatal(err)
	}
	return updates, hot
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
