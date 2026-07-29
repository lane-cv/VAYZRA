package database_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestAlertCollectorIndexesMigrationContracts(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, `
SELECT indexname,indexdef
FROM pg_indexes
WHERE schemaname='public'
  AND indexname=ANY($1)
ORDER BY indexname`,
		[]string{
			"ai_runs_alert_terminal_idx",
			"login_events_alert_failed_idx",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	definitions := make(map[string]string, 2)
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		definitions[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 {
		t.Fatalf("alert collector indexes=%v", definitions)
	}
	aiDefinition := definitions["ai_runs_alert_terminal_idx"]
	for _, fragment := range []string{
		"(completed_at)",
		"WHERE",
		"succeeded",
		"failed",
		"cancelled",
	} {
		if !strings.Contains(aiDefinition, fragment) {
			t.Fatalf("AI index=%q missing %q", aiDefinition, fragment)
		}
	}
	loginDefinition := definitions["login_events_alert_failed_idx"]
	for _, fragment := range []string{
		"(occurred_at)",
		"WHERE",
		"success = false",
	} {
		if !strings.Contains(loginDefinition, fragment) {
			t.Fatalf("login index=%q missing %q", loginDefinition, fragment)
		}
	}

	var applied bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM goose_db_version
  WHERE version_id=23 AND is_applied
)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("alert collector index migration 23 was not applied")
	}
}

func TestAlertCollectorIndexesMigrationDownTo22(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 22); err != nil {
		t.Fatal(err)
	}

	var indexes int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM pg_indexes
WHERE schemaname='public'
  AND indexname IN (
    'ai_runs_alert_terminal_idx',
    'login_events_alert_failed_idx'
  )`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 0 {
		t.Fatalf("alert collector indexes after down=%d", indexes)
	}
	var applied bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM goose_db_version
  WHERE version_id=23 AND is_applied
)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("migration 23 remains applied after down")
	}
}

func TestAlertCollectorQueriesUsePartialIndexes(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	if _, err := tx.Exec(
		ctx,
		`SET LOCAL session_replication_role=replica`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
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
  quota_day_key,quota_month_key,estimator_version,usage_source,error_code,
  created_at,updated_at,started_at,completed_at
)
SELECT
  gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),
  1,'alert-plan-fixture-'||value,
  CASE value%3
    WHEN 0 THEN 'succeeded'
    WHEN 1 THEN 'failed'
    ELSE 'cancelled'
  END,
  gen_random_uuid(),1,'https://alert-plan.invalid/v1','chat_completions',
  gen_random_uuid(),'alert-plan-model','text',8192,1024,1024,0,0,
  gen_random_uuid(),'math',1,repeat('a',64),
  1000,30000,30000,120000,1,1024,'2026-07-30','2026-07',1,
  'unknown',
  CASE WHEN value%3=0 THEN NULL ELSE 'alert_plan_error' END,
  $1::timestamptz-interval '1 hour',
  $1::timestamptz-interval '1 second',
  $1::timestamptz-interval '30 minutes',
  $1::timestamptz-(value||' seconds')::interval
FROM generate_series(1,12000) AS value`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO login_events(username,success,reason,occurred_at)
SELECT
  'alert-plan-'||value,
  value%20=0,
  CASE WHEN value%20=0 THEN 'authenticated' ELSE 'invalid_credentials' END,
  $1::timestamptz-(value||' seconds')::interval
FROM generate_series(1,12000) AS value`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"ANALYZE ai_runs",
		"ANALYZE login_events",
		"SET LOCAL enable_seqscan=off",
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	aiPlan := explainAlertCollectorQuery(t, ctx, tx, `
SELECT count(*)::bigint
FROM ai_runs
WHERE status IN ('succeeded','failed','cancelled')
  AND completed_at >= $2
  AND completed_at <= $1`,
		now,
		now.Add(-15*time.Minute),
	)
	if !strings.Contains(aiPlan, "ai_runs_alert_terminal_idx") {
		t.Fatalf("AI collector plan does not use partial index:\n%s", aiPlan)
	}
	loginPlan := explainAlertCollectorQuery(t, ctx, tx, `
SELECT count(*)::bigint
FROM login_events
WHERE success=false
  AND occurred_at >= $2
  AND occurred_at <= $1`,
		now,
		now.Add(-15*time.Minute),
	)
	if !strings.Contains(loginPlan, "login_events_alert_failed_idx") {
		t.Fatalf("login collector plan does not use partial index:\n%s", loginPlan)
	}
}

func explainAlertCollectorQuery(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	query string,
	args ...any,
) string {
	t.Helper()
	rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}
