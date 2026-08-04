package operations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPostgresActivityAlertCollectorUsesBoundedExactAggregates(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	db := &alertCollectorDBStub{
		row: dashboardRowStub{values: []any{
			int64(7), int64(3), int64(22), int64(6), int64(21), int64(51),
		}},
	}
	collector := newPostgresActivityAlertCollectorDB(db)
	collection, err := collector.Collect(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(collection.Samples) != 6 {
		t.Fatalf("samples=%d", len(collection.Samples))
	}
	windowStart := now.Add(-15 * time.Minute)
	expected := map[sampleSeries]float64{
		{source: SampleSourceApp, metric: SampleMetricAIRequestsTotal, scope: SampleScopeSucceeded}:               7,
		{source: SampleSourceApp, metric: SampleMetricAIRequestsTotal, scope: SampleScopeFailed}:                  3,
		{source: SampleSourceWorker, metric: SampleMetricQueueItems, scope: SampleScopeProcessing}:                22,
		{source: SampleSourceWorker, metric: SampleMetricQueueFailuresTotal, scope: SampleScopeProcessing}:        6,
		{source: SampleSourceApp, metric: SampleMetricSecurityEventsTotal, scope: SampleScopeLoginFailure}:        21,
		{source: SampleSourceApp, metric: SampleMetricSecurityEventsTotal, scope: SampleScopeAuthorizationDenial}: 51,
	}
	for _, sample := range collection.Samples {
		key := sampleSeries{
			source: sample.Source, metric: sample.Metric, scope: sample.Scope,
		}
		value, ok := expected[key]
		if !ok || sample.Value != value || !sample.ObservedAt.Equal(now) {
			t.Fatalf("sample=%+v", sample)
		}
		if sample.Metric == SampleMetricAIRequestsTotal ||
			sample.Metric == SampleMetricQueueFailuresTotal ||
			sample.Metric == SampleMetricSecurityEventsTotal {
			if sample.WindowStartedAt == nil ||
				!sample.WindowStartedAt.Equal(windowStart) {
				t.Fatalf("windowed sample=%+v", sample)
			}
		} else if sample.WindowStartedAt != nil {
			t.Fatalf("point sample has window=%+v", sample)
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		t.Fatalf("missing samples=%v", expected)
	}
	if !db.deadlineBounded(alertCollectorQueryTimeout) {
		t.Fatalf("collector query deadline=%v", db.deadline)
	}
	for _, fragment := range []string{
		"status IN ('failed','cancelled')",
		"completed_at >= $2", "completed_at <= $1",
		"updated_at >= $2", "updated_at <= $1",
		"occurred_at >= $2", "occurred_at <= $1",
		"FROM login_events", "FROM audit_logs",
	} {
		if !strings.Contains(db.query, fragment) {
			t.Fatalf("query missing %q: %s", fragment, db.query)
		}
	}
	if len(db.args) != 2 || db.args[0] != now || db.args[1] != windowStart {
		t.Fatalf("args=%v", db.args)
	}
}

func TestObjectStoreAlertCollectorCollectsCapacityAndUsage(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	collector := newObjectStoreAlertCollector(objectStoreCapacityStub{
		usedBytes:     30,
		capacityBytes: 100,
	})
	collection, err := collector.Collect(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(collection.Samples) != 2 {
		t.Fatalf("samples=%d", len(collection.Samples))
	}
	for _, sample := range collection.Samples {
		if sample.Source != SampleSourceObjectStore ||
			sample.Scope != SampleScopeObjectStore ||
			sample.Unit != SampleUnitBytes ||
			!sample.ObservedAt.Equal(now) {
			t.Fatalf("sample=%+v", sample)
		}
		switch sample.Metric {
		case SampleMetricObjectUsedBytes:
			if sample.Value != 30 {
				t.Fatalf("used sample=%+v", sample)
			}
		case SampleMetricObjectCapacityBytes:
			if sample.Value != 100 {
				t.Fatalf("capacity sample=%+v", sample)
			}
		default:
			t.Fatalf("unexpected sample=%+v", sample)
		}
	}
}

func TestObjectStoreAlertCollectorRejectsInvalidCapacity(t *testing.T) {
	collector := newObjectStoreAlertCollector(objectStoreCapacityStub{
		usedBytes:     101,
		capacityBytes: 100,
	})
	if _, err := collector.Collect(
		context.Background(),
		time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestPostgresAlertCollectorsIncludeObjectStoreCapacityReader(t *testing.T) {
	collectors := NewPostgresAlertCollectors(nil, objectStoreCapacityStub{
		usedBytes: 1, capacityBytes: 2,
	})
	if len(collectors) != 3 {
		t.Fatalf("collectors=%d", len(collectors))
	}
	collection, err := collectors[2].Collect(
		context.Background(),
		time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	)
	if err != nil || len(collection.Samples) != 2 {
		t.Fatalf("collection=%+v error=%v", collection, err)
	}
}

func TestPostgresActivityAlertCollectorCountsCancelledAtWindowBoundaries(
	t *testing.T,
) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	if _, err := pool.Exec(ctx, `TRUNCATE users CASCADE`); err != nil {
		t.Fatal(err)
	}
	now := alertPostgresClock(t, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	seedDashboardPostgresFixture(t, ctx, tx, now)
	insertDashboardFutureAIRun(t, ctx, tx, now)
	if _, err := tx.Exec(
		ctx,
		`SET LOCAL session_replication_role=replica`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE ai_runs
SET status='cancelled',
    completed_at=$1::timestamptz-interval '15 minutes'
WHERE id='45000000-0000-4000-8000-000000000002'`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE ai_runs
SET status='cancelled'
WHERE id='45000000-0000-4000-8000-000000000006'`); err != nil {
		t.Fatal(err)
	}
	collector := newPostgresActivityAlertCollectorDB(
		dashboardPGXTxDB{tx: tx},
	)
	beforeEnd, err := collector.Collect(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if value := alertCollectionSeriesValue(
		beforeEnd,
		SampleScopeFailed,
	); value != 1 {
		t.Fatalf("cancelled at start plus future=%v want=1", value)
	}
	if _, err := tx.Exec(ctx, `
UPDATE ai_runs
SET completed_at=$1,updated_at=$1
WHERE id='45000000-0000-4000-8000-000000000006'`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	atBothBoundaries, err := collector.Collect(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if value := alertCollectionSeriesValue(
		atBothBoundaries,
		SampleScopeFailed,
	); value != 2 {
		t.Fatalf("cancelled at both boundaries=%v want=2", value)
	}
}

func TestPostgresActivityAlertCollectorQueryUsesPartialIndexes(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	now := alertPostgresClock(t, pool)
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
  1,'alert-main-plan-fixture-'||value,
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
  'alert-main-plan-'||value,
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
	rows, err := tx.Query(
		ctx,
		"EXPLAIN (COSTS OFF) "+postgresActivityAlertCollectorQuery,
		now,
		now.Add(-15*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var planLines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		planLines = append(planLines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(planLines, "\n")
	for _, index := range []string{
		"ai_runs_alert_terminal_idx",
		"login_events_alert_failed_idx",
	} {
		if !strings.Contains(plan, index) {
			t.Fatalf(
				"activity collector plan missing %s:\n%s",
				index,
				plan,
			)
		}
	}
}

func TestPostgresBackupAlertCollectorUsesCurrentRunTruth(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	localFinishedAt := now.Add(-26 * time.Hour)
	configured := true
	remoteUp := false
	db := &alertCollectorDBStub{
		row: dashboardRowStub{values: []any{
			&localFinishedAt, &configured, &remoteUp,
		}},
	}
	collection, err := newPostgresBackupAlertCollectorDB(db).
		Collect(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if collection.RemoteReplicationConfigured == nil ||
		!*collection.RemoteReplicationConfigured ||
		len(collection.Samples) != 2 {
		t.Fatalf("collection=%+v", collection)
	}
	values := make(map[SampleMetric]float64, 2)
	for _, sample := range collection.Samples {
		values[sample.Metric] = sample.Value
	}
	if values[SampleMetricBackupAgeSeconds] != 26*60*60 ||
		values[SampleMetricBackupRemoteUp] != 0 {
		t.Fatalf("values=%v", values)
	}
	if !db.deadlineBounded(alertCollectorQueryTimeout) {
		t.Fatalf("collector query deadline=%v", db.deadline)
	}
	for _, fragment := range []string{
		"finished_at DESC", "id DESC", "local_snapshot_id",
		"state='degraded'", "error_category='remote_unavailable'",
		"remote_snapshot_id",
	} {
		if !strings.Contains(db.query, fragment) {
			t.Fatalf("query missing %q: %s", fragment, db.query)
		}
	}
}

func TestPostgresBackupAlertCollectorDoesNotInferConfigurationFromSamples(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	if _, err := pool.Exec(ctx, `
TRUNCATE backup_runs CASCADE;
TRUNCATE operational_samples`); err != nil {
		t.Fatal(err)
	}
	now := alertPostgresClock(t, pool)
	collector := NewPostgresAlertCollectors(pool)[0]
	empty, err := collector.Collect(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if empty.RemoteReplicationConfigured != nil || len(empty.Samples) != 0 {
		t.Fatalf("empty collection=%+v", empty)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,started_at,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  manifest_sha256,local_expires_at,error_category
) VALUES(
  '71000000-0000-4000-8000-000000000001','first-remote-failure','scheduled',
  'degraded',$1::timestamptz-interval '10 minutes',
  $1::timestamptz-interval '9 minutes',$1::timestamptz-interval '8 minutes',
  22,'alert-key','local-first',decode(repeat('ab',32),'hex'),
  $1::timestamptz+interval '7 days','remote_unavailable'
)`, now); err != nil {
		t.Fatal(err)
	}
	failed, err := collector.Collect(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if failed.RemoteReplicationConfigured == nil ||
		!*failed.RemoteReplicationConfigured ||
		alertCollectionMetricValue(failed, SampleMetricBackupRemoteUp) != 0 {
		t.Fatalf("first remote failure=%+v", failed)
	}
	historicalWindow := now.Add(-15 * time.Minute)
	if err := NewPostgresSampleStore(pool).InsertSamples(ctx, now, []Sample{
		alertSample(
			SampleSourceApp,
			SampleMetricBackupRemoteUp,
			SampleScopeRemote,
			0,
			now,
			nil,
		),
		alertSample(
			SampleSourceApp,
			SampleMetricAIRequestsTotal,
			SampleScopeSucceeded,
			0,
			now,
			&historicalWindow,
		),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_at,started_at,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  remote_snapshot_id,manifest_sha256,local_expires_at
) VALUES
(
  '71000000-0000-4000-8000-000000000002','same-time-remote-success','scheduled',
  'succeeded',$1::timestamptz-interval '5 minutes',
  $1::timestamptz-interval '4 minutes',$1::timestamptz-interval '3 minutes',
  22,'alert-key','local-second','remote-second',
  decode(repeat('bc',32),'hex'),$1::timestamptz+interval '7 days'
),
(
  '71000000-0000-4000-8000-000000000003','same-time-local-only','scheduled',
  'succeeded',$1::timestamptz-interval '5 minutes',
  $1::timestamptz-interval '4 minutes',$1::timestamptz-interval '3 minutes',
  22,'alert-key','local-third',NULL,
  decode(repeat('cd',32),'hex'),$1::timestamptz+interval '7 days'
)`, now); err != nil {
		t.Fatal(err)
	}
	disabled, err := collector.Collect(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.RemoteReplicationConfigured == nil ||
		*disabled.RemoteReplicationConfigured ||
		alertCollectionHasMetric(disabled, SampleMetricBackupRemoteUp) {
		t.Fatalf("disabled with old remote sample=%+v", disabled)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE operational_samples`); err != nil {
		t.Fatal(err)
	}
	afterCleanup, err := collector.Collect(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if afterCleanup.RemoteReplicationConfigured == nil ||
		*afterCleanup.RemoteReplicationConfigured ||
		alertCollectionHasMetric(afterCleanup, SampleMetricBackupRemoteUp) {
		t.Fatalf("after sample cleanup=%+v", afterCleanup)
	}
}

func TestPostgresActivityAlertCollectorReadsExactFifteenMinuteRows(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	if _, err := pool.Exec(ctx, `TRUNCATE users CASCADE`); err != nil {
		t.Fatal(err)
	}
	now := alertPostgresClock(t, pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	seedDashboardPostgresFixture(t, ctx, tx, now)
	if _, err := tx.Exec(ctx, `
INSERT INTO login_events(username,success,reason,occurred_at) VALUES
  ('aggregate-login-1',false,'invalid_credentials',$1::timestamptz-interval '1 minute'),
  ('aggregate-login-2',false,'invalid_credentials',$1::timestamptz-interval '15 minutes'),
  ('aggregate-login-old',false,'invalid_credentials',$1::timestamptz-interval '15 minutes 1 second')`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO audit_logs(action,target_type,target_id,metadata,request_id,occurred_at)
VALUES
  ('authorization.denied','request','aggregate','{"outcome":"denied"}','aggregate-auth-1',$1::timestamptz-interval '1 minute'),
  ('authorization.denied','request','aggregate','{"outcome":"denied"}','aggregate-auth-2',$1::timestamptz-interval '15 minutes'),
  ('authorization.denied','request','aggregate','{"outcome":"denied"}','aggregate-auth-old',$1::timestamptz-interval '15 minutes 1 second')`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	collection, err := newPostgresActivityAlertCollectorDB(
		dashboardPGXTxDB{tx: tx},
	).Collect(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[SampleMetric]float64{
		SampleMetricAIRequestsTotal:     1,
		SampleMetricQueueItems:          2,
		SampleMetricQueueFailuresTotal:  1,
		SampleMetricSecurityEventsTotal: 2,
	}
	bySeries := make(map[sampleSeries]Sample, len(collection.Samples))
	for _, sample := range collection.Samples {
		bySeries[sampleSeries{
			source: sample.Source, metric: sample.Metric, scope: sample.Scope,
		}] = sample
	}
	if bySeries[sampleSeries{
		source: SampleSourceApp, metric: SampleMetricAIRequestsTotal,
		scope: SampleScopeSucceeded,
	}].Value != 0 ||
		bySeries[sampleSeries{
			source: SampleSourceApp, metric: SampleMetricAIRequestsTotal,
			scope: SampleScopeFailed,
		}].Value != expected[SampleMetricAIRequestsTotal] ||
		bySeries[sampleSeries{
			source: SampleSourceWorker, metric: SampleMetricQueueItems,
			scope: SampleScopeProcessing,
		}].Value != expected[SampleMetricQueueItems] ||
		bySeries[sampleSeries{
			source: SampleSourceWorker, metric: SampleMetricQueueFailuresTotal,
			scope: SampleScopeProcessing,
		}].Value != expected[SampleMetricQueueFailuresTotal] ||
		bySeries[sampleSeries{
			source: SampleSourceApp, metric: SampleMetricSecurityEventsTotal,
			scope: SampleScopeLoginFailure,
		}].Value != expected[SampleMetricSecurityEventsTotal] ||
		bySeries[sampleSeries{
			source: SampleSourceApp, metric: SampleMetricSecurityEventsTotal,
			scope: SampleScopeAuthorizationDenial,
		}].Value != expected[SampleMetricSecurityEventsTotal] {
		t.Fatalf("samples=%+v", collection.Samples)
	}
}

func TestPostgresSampleStoreInsertsAndReadsLatestAtomically(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	if _, err := pool.Exec(ctx, `TRUNCATE operational_samples`); err != nil {
		t.Fatal(err)
	}
	now := alertPostgresClock(t, pool)
	window := now.Add(-15 * time.Minute)
	inserted := []Sample{
		alertSample(
			SampleSourceApp,
			SampleMetricAIRequestsTotal,
			SampleScopeSucceeded,
			7,
			now,
			&window,
		),
		alertSample(
			SampleSourceApp,
			SampleMetricAIRequestsTotal,
			SampleScopeFailed,
			3,
			now,
			&window,
		),
	}
	latest, err := NewPostgresSampleStore(pool).
		insertAndLatestMetrics(ctx, now, inserted)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != len(inserted) {
		t.Fatalf("latest=%+v", latest)
	}
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM operational_samples
WHERE observed_at=$1`, now).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(inserted) {
		t.Fatalf("persisted=%d", count)
	}
}

func TestPostgresLoadAlertEvaluationsCollectsCurrentAggregates(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	if _, err := pool.Exec(ctx, `
TRUNCATE login_events;
TRUNCATE operational_samples;
TRUNCATE backup_runs CASCADE;
INSERT INTO login_events(username,success,reason,occurred_at)
SELECT 'alert-login-'||value,false,'invalid_credentials',
       clock_timestamp()-interval '1 minute'
FROM generate_series(1,20) AS value`); err != nil {
		t.Fatal(err)
	}
	now := alertPostgresClock(t, pool)
	if err := NewPostgresSampleStore(pool).InsertSamples(ctx, now, []Sample{
		alertSample(
			SampleSourceHost,
			SampleMetricFilesystemUsedPercent,
			SampleScopeRoot,
			10,
			now,
			nil,
		),
	}); err != nil {
		t.Fatal(err)
	}
	evaluations, err := NewPostgresAlertStore(pool).
		LoadAlertEvaluations(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	byKey := make(map[string]Evaluation, len(evaluations))
	for _, evaluation := range evaluations {
		byKey[evaluation.Rule.DedupeKey] = evaluation
	}
	login := byKey["login_failures"]
	loginDependency := byKey["login_failures_dependency_unavailable"]
	if !login.Available || login.Value != 20 ||
		login.SampleCount != 1 ||
		!loginDependency.Available || loginDependency.Value != 0 {
		t.Fatalf("login=%+v dependency=%+v", login, loginDependency)
	}
	remote, exists := byKey[AlertKeyBackupRemoteSync]
	if !exists || remote.Available {
		t.Fatalf("remote unknown threshold=%+v exists=%t", remote, exists)
	}
	if _, exists := byKey[AlertKeyBackupRemoteSyncDependencyUnavailable]; exists {
		t.Fatal("remote dependency evaluated without current configuration truth")
	}
	var persisted int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM operational_samples
WHERE observed_at=$1
  AND source IN ('app','worker')`, now).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 6 {
		t.Fatalf("persisted aggregate samples=%d want=6", persisted)
	}
}

func TestPostgresLoadAlertEvaluationsDoesNotReusePreviousAggregateOnCollectorFailure(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	if _, err := pool.Exec(ctx, `
TRUNCATE operational_samples;
TRUNCATE backup_runs CASCADE`); err != nil {
		t.Fatal(err)
	}
	now := alertPostgresClock(t, pool)
	if err := NewPostgresSampleStore(pool).InsertSamples(ctx, now, []Sample{
		alertSample(
			SampleSourceWorker,
			SampleMetricQueueItems,
			SampleScopeProcessing,
			101,
			now,
			nil,
		),
	}); err != nil {
		t.Fatal(err)
	}
	store := newPostgresAlertStoreWithCollectors(
		pool,
		[]AlertSampleCollector{
			alertCollectorStub{err: context.DeadlineExceeded},
		},
	)
	evaluations, err := store.LoadAlertEvaluations(ctx, now)
	if !errors.Is(err, ErrAlertCollectorUnavailable) {
		t.Fatalf("error=%v", err)
	}
	byKey := make(map[string]Evaluation, len(evaluations))
	for _, evaluation := range evaluations {
		byKey[evaluation.Rule.DedupeKey] = evaluation
	}
	threshold := byKey["processing_queue_depth"]
	dependency := byKey["processing_queue_depth_dependency_unavailable"]
	if threshold.Available ||
		!dependency.Available || dependency.Value != 1 ||
		dependency.Rule.Summary != "Processing queue metrics are unavailable" {
		t.Fatalf("threshold=%+v dependency=%+v", threshold, dependency)
	}
}

func alertCollectionMetricValue(
	collection AlertCollection,
	metric SampleMetric,
) float64 {
	for _, sample := range collection.Samples {
		if sample.Metric == metric {
			return sample.Value
		}
	}
	return -1
}

func alertCollectionHasMetric(
	collection AlertCollection,
	metric SampleMetric,
) bool {
	for _, sample := range collection.Samples {
		if sample.Metric == metric {
			return true
		}
	}
	return false
}

func alertCollectionSeriesValue(
	collection AlertCollection,
	scope SampleScope,
) float64 {
	for _, sample := range collection.Samples {
		if sample.Metric == SampleMetricAIRequestsTotal &&
			sample.Scope == scope {
			return sample.Value
		}
	}
	return -1
}

type alertCollectorDBStub struct {
	row      dashboardRow
	query    string
	args     []any
	deadline time.Time
}

type alertCollectorStub struct {
	collection AlertCollection
	err        error
}

type objectStoreCapacityStub struct {
	usedBytes     int64
	capacityBytes int64
}

func (stub objectStoreCapacityStub) StorageUsage(
	context.Context,
) (int64, int64, error) {
	return stub.usedBytes, stub.capacityBytes, nil
}

func (stub alertCollectorStub) Collect(
	context.Context,
	time.Time,
) (AlertCollection, error) {
	return stub.collection, stub.err
}

func (db *alertCollectorDBStub) QueryRow(
	ctx context.Context,
	query string,
	args ...any,
) dashboardRow {
	db.query = query
	db.args = append([]any(nil), args...)
	db.deadline, _ = ctx.Deadline()
	return db.row
}

func (db *alertCollectorDBStub) deadlineBounded(maximum time.Duration) bool {
	if db.deadline.IsZero() {
		return false
	}
	remaining := time.Until(db.deadline)
	return remaining > 0 && remaining <= maximum
}
