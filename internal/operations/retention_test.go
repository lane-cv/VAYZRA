package operations

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresRetentionRunnerRejectsInvalidInputsBeforeDatabaseUse(
	t *testing.T,
) {
	runner := NewPostgresRetentionRunner(nil)
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)

	for _, retentionDays := range []int{-1, 31} {
		result, err := runner.RunOnce(context.Background(), now, retentionDays)
		if !errors.Is(err, ErrInvalid) || result != (RetentionResult{}) {
			t.Fatalf(
				"retentionDays=%d result=%+v error=%v want zero,ErrInvalid",
				retentionDays,
				result,
				err,
			)
		}
	}
	for name, run := range map[string]func() (RetentionResult, error){
		"nil context": func() (RetentionResult, error) {
			return runner.RunOnce(nil, now, 7)
		},
		"year zero": func() (RetentionResult, error) {
			return runner.RunOnce(
				context.Background(),
				time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
				7,
			)
		},
		"year ten thousand": func() (RetentionResult, error) {
			return runner.RunOnce(
				context.Background(),
				time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
				7,
			)
		},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := run()
			if !errors.Is(err, ErrInvalid) || result != (RetentionResult{}) {
				t.Fatalf("result=%+v error=%v want zero,ErrInvalid", result, err)
			}
		})
	}
	for name, closed := range map[string]*PostgresRetentionRunner{
		"nil runner": nil,
		"nil pool":   runner,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := closed.RunOnce(context.Background(), now, 0)
			if !errors.Is(err, errStoreClosed) ||
				result != (RetentionResult{}) {
				t.Fatalf(
					"result=%+v error=%v want zero,errStoreClosed",
					result,
					err,
				)
			}
		})
	}
}

func TestPostgresRetentionRunnerDeletesOnlyExpiredTerminalMetadataAndAuditsCounts(
	t *testing.T,
) {
	ctx := context.Background()
	pool, runner := migratedRetentionRunner(t)
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	sampleCutoff := now.AddDate(0, 0, -defaultOperationalSampleRetentionDays)
	metadataCutoff := now.AddDate(0, 0, -365)
	old := metadataCutoff.Add(-time.Second)

	if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at
) VALUES
  ('app','backup_age_seconds','local',1,'seconds',$1),
  ('app','backup_age_seconds','local',2,'seconds',$2);

WITH deletable_alert AS (
  INSERT INTO operational_alerts(
    dedupe_key,category,severity,state,first_observed_at,last_observed_at,
    resolved_at,current_value,threshold_value,summary
  ) VALUES(
    'retention-deletable','backup','warning','resolved',$3,$3,$3,1,2,
    'deletable retention alert'
  )
  RETURNING id
), deletable_event AS (
  INSERT INTO alert_webhook_events(
    id,alert_id,transition_kind,alert_version,category,severity,state,summary,
    current_value,threshold_value,first_observed_at,last_observed_at,enqueued_at
  )
  SELECT gen_random_uuid(),id,'resolved',1,'backup','warning','resolved',
    'deletable retention alert',1,2,$3,$3,$3
  FROM deletable_alert
  RETURNING id,alert_id
)
INSERT INTO alert_deliveries(
  event_id,alert_id,attempt,destination,outcome,delivery_state,scheduled_at,
  started_at,finished_at
)
SELECT id,alert_id,1,'webhook','succeeded','succeeded',$3,$3,$3
FROM deletable_event;

WITH boundary_alert AS (
  INSERT INTO operational_alerts(
    dedupe_key,category,severity,state,first_observed_at,last_observed_at,
    resolved_at,current_value,threshold_value,summary
  ) VALUES(
    'retention-boundary','backup','warning','resolved',$4,$4,$4,1,2,
    'boundary retention alert'
  )
  RETURNING id
)
INSERT INTO alert_deliveries(
  alert_id,attempt,destination,outcome,started_at,finished_at
)
SELECT id,1,'webhook','failed',$4,$4
FROM boundary_alert;

INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  resolved_at,current_value,threshold_value,summary
) VALUES(
  'retention-parent-boundary','backup','warning','resolved',$4,$4,$4,1,2,
  'parent boundary retention alert'
);

WITH pending_alert AS (
  INSERT INTO operational_alerts(
    dedupe_key,category,severity,state,first_observed_at,last_observed_at,
    resolved_at,current_value,threshold_value,summary
  ) VALUES(
    'retention-pending-delivery','backup','warning','resolved',$3,$3,$3,1,2,
    'pending delivery retention alert'
  )
  RETURNING id
), pending_event AS (
  INSERT INTO alert_webhook_events(
    id,alert_id,transition_kind,alert_version,category,severity,state,summary,
    current_value,threshold_value,first_observed_at,last_observed_at,enqueued_at
  )
  SELECT gen_random_uuid(),id,'resolved',1,'backup','warning','resolved',
    'pending delivery retention alert',1,2,$3,$3,$3
  FROM pending_alert
  RETURNING id,alert_id
)
INSERT INTO alert_deliveries(
  event_id,alert_id,attempt,destination,delivery_state,scheduled_at
)
SELECT id,alert_id,1,'webhook','pending',$3
FROM pending_event;

WITH claimed_alert AS (
  INSERT INTO operational_alerts(
    dedupe_key,category,severity,state,first_observed_at,last_observed_at,
    resolved_at,current_value,threshold_value,summary
  ) VALUES(
    'retention-claimed-delivery','backup','warning','resolved',$3,$3,$3,1,2,
    'claimed delivery retention alert'
  )
  RETURNING id
), claimed_event AS (
  INSERT INTO alert_webhook_events(
    id,alert_id,transition_kind,alert_version,category,severity,state,summary,
    current_value,threshold_value,first_observed_at,last_observed_at,enqueued_at
  )
  SELECT gen_random_uuid(),id,'resolved',1,'backup','warning','resolved',
    'claimed delivery retention alert',1,2,$3,$3,$3
  FROM claimed_alert
  RETURNING id,alert_id
)
INSERT INTO alert_deliveries(
  event_id,alert_id,attempt,destination,delivery_state,scheduled_at,started_at,
  claim_owner,claim_token,claim_expires_at
)
SELECT
  id,alert_id,1,'webhook','claimed',$3,$3,'retention-test',
  gen_random_uuid(),$4
FROM claimed_event;

INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary
) VALUES(
  'retention-open','backup','warning','open',$3,$3,1,2,
  'open retention alert'
);

WITH identity AS (
  SELECT gen_random_uuid() AS id
), acknowledged_user AS (
  INSERT INTO users(
    id,username,display_name,role,status,password_hash,must_change_password
  )
  SELECT
    id,'retention_ack_'||replace(id::text,'-',''),'Retention Ack Student',
    'student','active','hash',false
  FROM identity
  RETURNING id
)
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  acknowledged_by,acknowledged_at,current_value,threshold_value,summary
)
SELECT
  'retention-acknowledged','backup','warning','acknowledged',$3,$3,id,$3,
  1,2,'acknowledged retention alert'
FROM acknowledged_user;

WITH deletable_backup AS (
  INSERT INTO backup_runs(
    idempotency_key,trigger_kind,state,requested_at,started_at,finished_at
  ) VALUES('retention-deletable','scheduled','failed',$3,$3,$3)
  RETURNING id
), artifact AS (
  INSERT INTO backup_artifacts(
    backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,
    expires_at
  )
  SELECT id,'database_dump','local','retention-snapshot',
    decode(repeat('11',32),'hex'),1,$3,$3
  FROM deletable_backup
)
INSERT INTO restore_verifications(
  backup_run_id,state,started_at,finished_at
)
SELECT id,'failed',$3,$3
FROM deletable_backup;

WITH boundary_backup AS (
  INSERT INTO backup_runs(
    idempotency_key,trigger_kind,state,requested_at,started_at,finished_at
  ) VALUES('retention-boundary','scheduled','failed',$4,$4,$4)
  RETURNING id
)
INSERT INTO restore_verifications(
  backup_run_id,state,started_at,finished_at
)
SELECT id,'failed',$4,$4
FROM boundary_backup;

INSERT INTO backup_runs(
  idempotency_key,trigger_kind,state,requested_at,started_at,finished_at
) VALUES(
  'retention-parent-boundary','scheduled','failed',$4,$4,$4
);

WITH blocked_backup AS (
  INSERT INTO backup_runs(
    idempotency_key,trigger_kind,state,requested_at,started_at,finished_at
  ) VALUES('retention-blocked','scheduled','failed',$3,$3,$3)
  RETURNING id
)
INSERT INTO restore_verifications(backup_run_id,state)
SELECT id,'queued'
FROM blocked_backup;

INSERT INTO backup_runs(
  idempotency_key,trigger_kind,state,requested_at
) VALUES('retention-nonterminal','scheduled','queued',$3);

INSERT INTO audit_logs(
  action,target_type,target_id,metadata,request_id,ip,occurred_at
) VALUES(
  'operations.lease_taken_over','operational_mode','global','{}',
  'retention-existing-audit','127.0.0.1',$3
)`,
		pgx.QueryExecModeSimpleProtocol,
		sampleCutoff.Add(-time.Nanosecond),
		sampleCutoff,
		old,
		metadataCutoff,
	); err != nil {
		t.Fatal(err)
	}

	result, err := runner.RunOnce(ctx, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := RetentionResult{
		Samples:              1,
		AlertDeliveries:      1,
		Alerts:               1,
		RestoreVerifications: 1,
		BackupRuns:           1,
	}
	if result != want {
		t.Fatalf("result=%+v want=%+v", result, want)
	}

	for table, wantCount := range map[string]int{
		"operational_samples":   1,
		"alert_deliveries":      3,
		"operational_alerts":    6,
		"restore_verifications": 2,
		"backup_runs":           4,
		"backup_artifacts":      0,
		"alert_webhook_events":  2,
		"audit_logs":            2,
	} {
		var got int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != wantCount {
			t.Errorf("%s count=%d want=%d", table, got, wantCount)
		}
	}

	var (
		auditID       int64
		actorIsSystem bool
		targetType    string
		targetID      string
		requestID     string
		ip            net.IP
		metadata      []byte
	)
	if err := pool.QueryRow(ctx, `
SELECT id,actor_user_id IS NULL,target_type,target_id,request_id,ip,metadata
FROM audit_logs
WHERE action='operations.retention_completed'`).
		Scan(
			&auditID,
			&actorIsSystem,
			&targetType,
			&targetID,
			&requestID,
			&ip,
			&metadata,
		); err != nil {
		t.Fatal(err)
	}
	var gotMetadata map[string]any
	if err := json.Unmarshal(metadata, &gotMetadata); err != nil {
		t.Fatal(err)
	}
	wantMetadata := map[string]any{
		"samples":              "1",
		"alertDeliveries":      "1",
		"alerts":               "1",
		"restoreVerifications": "1",
		"backupRuns":           "1",
		"outcome":              "succeeded",
	}
	if !actorIsSystem ||
		targetType != "metadata_retention" ||
		targetID != "global" ||
		requestID != "operations-retention" ||
		!ip.Equal(net.ParseIP("127.0.0.1")) ||
		!equalJSONMaps(gotMetadata, wantMetadata) {
		t.Fatalf(
			"audit system=%t target=%s/%s request=%q ip=%s metadata=%v",
			actorIsSystem,
			targetType,
			targetID,
			requestID,
			ip,
			gotMetadata,
		)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE audit_logs SET action='changed' WHERE id=$1`,
		auditID,
	); err == nil {
		t.Fatal("retention audit update unexpectedly succeeded")
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM audit_logs WHERE id=$1`,
		auditID,
	); err == nil {
		t.Fatal("retention audit delete unexpectedly succeeded")
	}
}

func TestPostgresRetentionRunnerAcceptsOneAndThirtyDayEndpoints(
	t *testing.T,
) {
	for _, retentionDays := range []int{1, 30} {
		t.Run(strconv.Itoa(retentionDays), func(t *testing.T) {
			ctx := context.Background()
			pool, runner := migratedRetentionRunner(t)
			now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
			cutoff := now.AddDate(0, 0, -retentionDays)
			if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at
) VALUES
  ('app','backup_age_seconds','local',1,'seconds',$1),
  ('app','backup_age_seconds','local',2,'seconds',$2)`,
				cutoff.Add(-time.Nanosecond),
				cutoff,
			); err != nil {
				t.Fatal(err)
			}
			result, err := runner.RunOnce(ctx, now, retentionDays)
			if err != nil {
				t.Fatal(err)
			}
			if result != (RetentionResult{Samples: 1}) {
				t.Fatalf("result=%+v want one sample", result)
			}
			var remaining int
			if err := pool.QueryRow(
				ctx,
				`SELECT count(*) FROM operational_samples`,
			).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining != 1 {
				t.Fatalf("remaining samples=%d want=1", remaining)
			}
		})
	}
}

func TestPostgresRetentionRunnerDeletesSucceededAndDegradedBackupMetadata(
	t *testing.T,
) {
	ctx := context.Background()
	pool, runner := migratedRetentionRunner(t)
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -metadataRetentionDays).Add(-time.Second)
	for _, state := range []string{"succeeded", "degraded"} {
		if _, err := pool.Exec(ctx, `
WITH backup AS (
  INSERT INTO backup_runs(
    idempotency_key,trigger_kind,state,requested_at,started_at,finished_at,
    database_migration_version,encryption_key_id,local_snapshot_id,
    manifest_sha256,local_expires_at
  ) VALUES(
    $1,'scheduled',$2,$3,$3,$3,1,'retention-key','retention-snapshot',
    decode(repeat('22',32),'hex'),$3
  )
  RETURNING id
)
INSERT INTO restore_verifications(
  backup_run_id,state,started_at,finished_at,restored_migration_version,
  database_row_counts,session_revocation_verified,rto_seconds,report_sha256
)
SELECT
  id,'succeeded',$3,$3,1,'{"users":0}'::jsonb,true,1,
  decode(repeat('33',32),'hex')
FROM backup`,
			"retention-"+state,
			state,
			old,
		); err != nil {
			t.Fatal(err)
		}
	}

	result, err := runner.RunOnce(ctx, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := RetentionResult{
		RestoreVerifications: 2,
		BackupRuns:           2,
	}
	if result != want {
		t.Fatalf("result=%+v want=%+v", result, want)
	}
	assertRetentionTableCounts(t, pool, 0)
}

func TestPostgresRetentionRunnerKeepsDeliveriesForOpenAndAcknowledgedAlerts(
	t *testing.T,
) {
	ctx := context.Background()
	pool, runner := migratedRetentionRunner(t)
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -metadataRetentionDays).Add(-time.Second)
	if _, err := pool.Exec(ctx, `
WITH identity AS (
  SELECT gen_random_uuid() AS id
), acknowledged_user AS (
  INSERT INTO users(
    id,username,display_name,role,status,password_hash,must_change_password
  )
  SELECT
    id,'retention_history_'||replace(id::text,'-',''),
    'Retention History Student','student','active','hash',false
  FROM identity
  RETURNING id
), open_alert AS (
  INSERT INTO operational_alerts(
    dedupe_key,category,severity,state,first_observed_at,last_observed_at,
    current_value,threshold_value,summary
  )
  VALUES(
    'retention-history-open','backup','warning','open',$1,$1,
    1,2,'open retention history'
  )
  RETURNING id
), acknowledged_alert AS (
  INSERT INTO operational_alerts(
    dedupe_key,category,severity,state,first_observed_at,last_observed_at,
    acknowledged_by,acknowledged_at,current_value,threshold_value,summary
  )
  SELECT
    'retention-history-ack','backup','warning','acknowledged',
    $1,$1,id,$1,1,2,'acknowledged retention history'
  FROM acknowledged_user
  RETURNING id,state
), resolved_alert AS (
  INSERT INTO operational_alerts(
    dedupe_key,category,severity,state,first_observed_at,last_observed_at,
    resolved_at,current_value,threshold_value,summary
  )
  VALUES(
    'retention-history-resolved','backup','warning','resolved',$1,$1,$1,
    1,2,'resolved retention history'
  )
  RETURNING id
), alerts AS (
  SELECT id FROM open_alert
  UNION ALL
  SELECT id FROM acknowledged_alert
  UNION ALL
  SELECT id FROM resolved_alert
)
INSERT INTO alert_deliveries(
  alert_id,attempt,destination,outcome,started_at,finished_at
)
SELECT id,1,'webhook','failed',$1,$1
FROM alerts`,
		old,
	); err != nil {
		t.Fatal(err)
	}

	result, err := runner.RunOnce(ctx, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if result.AlertDeliveries != 1 || result.Alerts != 1 {
		t.Fatalf("result=%+v want one resolved alert and delivery", result)
	}
	var states []string
	rows, err := pool.Query(ctx, `
SELECT alert.state
FROM alert_deliveries AS delivery
JOIN operational_alerts AS alert ON alert.id=delivery.alert_id
ORDER BY alert.state`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		if err := rows.Scan(&state); err != nil {
			t.Fatal(err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(states, ","); got != "acknowledged,open" {
		t.Fatalf("remaining delivery parent states=%s", got)
	}
}

func TestPostgresRetentionRunnerReturnsZeroForClosedPool(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	closed, err := pgxpool.New(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()

	result, err := NewPostgresRetentionRunner(closed).RunOnce(
		ctx,
		time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC),
		7,
	)
	if err == nil || result != (RetentionResult{}) {
		t.Fatalf(
			"result=%+v error=%v want zero,closed-pool error",
			result,
			err,
		)
	}
}

func TestPostgresRetentionRunnerSkipsWithoutAuditWhenAdvisoryLockIsHeld(
	t *testing.T,
) {
	ctx := context.Background()
	pool, runner := migratedRetentionRunner(t)
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	canceledCtx, cancelBeforeRun := context.WithCancel(ctx)
	cancelBeforeRun()
	canceledResult, canceledErr := runner.RunOnce(canceledCtx, now, 7)
	if !errors.Is(canceledErr, context.Canceled) ||
		canceledResult != (RetentionResult{}) {
		t.Fatalf(
			"pre-canceled result=%+v error=%v want zero,context.Canceled",
			canceledResult,
			canceledErr,
		)
	}
	seedRetentionBatch(t, pool, now, 1)

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(
		ctx,
		`SELECT pg_advisory_lock($1)`,
		retentionAdvisoryLockKey,
	); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = blocker.Exec(
				context.Background(),
				`SELECT pg_advisory_unlock($1)`,
				retentionAdvisoryLockKey,
			)
		}
	}()

	result, err := runner.RunOnce(ctx, now, 7)
	if err != nil || result != (RetentionResult{}) {
		t.Fatalf("locked result=%+v error=%v want zero,nil", result, err)
	}
	assertRetentionTableCounts(t, pool, 1)
	assertRetentionAuditCount(t, pool, 0)

	if _, err := blocker.Exec(
		ctx,
		`SELECT pg_advisory_unlock($1)`,
		retentionAdvisoryLockKey,
	); err != nil {
		t.Fatal(err)
	}
	locked = false

	result, err = runner.RunOnce(ctx, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := RetentionResult{
		Samples:              1,
		AlertDeliveries:      1,
		Alerts:               1,
		RestoreVerifications: 1,
		BackupRuns:           1,
	}
	if result != want {
		t.Fatalf("unlocked result=%+v want=%+v", result, want)
	}
	assertRetentionTableCounts(t, pool, 0)
	assertRetentionAuditCount(t, pool, 1)
}

func TestPostgresRetentionRunnerBoundsEveryTableToOneThousandRows(
	t *testing.T,
) {
	ctx := context.Background()
	pool, runner := migratedRetentionRunner(t)
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	seedRetentionBatch(t, pool, now, metadataRetentionBatch+1)

	result, err := runner.RunOnce(ctx, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := RetentionResult{
		Samples:              metadataRetentionBatch,
		AlertDeliveries:      metadataRetentionBatch,
		Alerts:               metadataRetentionBatch,
		RestoreVerifications: metadataRetentionBatch,
		BackupRuns:           metadataRetentionBatch,
	}
	if result != want {
		t.Fatalf("result=%+v want=%+v", result, want)
	}
	assertRetentionTableCounts(t, pool, 1)
	assertRetentionAuditCount(t, pool, 1)
}

func TestPostgresRetentionRunnerRollsBackEveryDeleteWhenAuditFails(
	t *testing.T,
) {
	ctx := context.Background()
	pool, runner := migratedRetentionRunner(t)
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	seedRetentionBatch(t, pool, now, 1)
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION operations_test_reject_retention_audit() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.action='operations.retention_completed' THEN
    RAISE EXCEPTION 'retention audit rejected by test fixture';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER operations_test_reject_retention_audit
BEFORE INSERT ON audit_logs
FOR EACH ROW EXECUTE FUNCTION operations_test_reject_retention_audit()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `
DROP TRIGGER IF EXISTS operations_test_reject_retention_audit ON audit_logs;
DROP FUNCTION IF EXISTS operations_test_reject_retention_audit()`)
	})

	result, err := runner.RunOnce(ctx, now, 7)
	if err == nil || result != (RetentionResult{}) {
		t.Fatalf("result=%+v error=%v want zero,database error", result, err)
	}
	assertRetentionTableCounts(t, pool, 1)
	assertRetentionAuditCount(t, pool, 0)
}

func TestPostgresRetentionRunnerUnlocksAndRollsBackAfterCallerCancellation(
	t *testing.T,
) {
	const pauseLock int64 = 845103123

	ctx := context.Background()
	pool, runner := migratedRetentionRunner(t)
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	seedRetentionBatch(t, pool, now, 1)
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION operations_test_pause_retention_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_lock(845103123);
  PERFORM pg_advisory_unlock(845103123);
  RETURN OLD;
END
$$;
CREATE TRIGGER operations_test_pause_retention_delete
BEFORE DELETE ON operational_samples
FOR EACH ROW EXECUTE FUNCTION operations_test_pause_retention_delete()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `
DROP TRIGGER IF EXISTS operations_test_pause_retention_delete
  ON operational_samples;
DROP FUNCTION IF EXISTS operations_test_pause_retention_delete()`)
	})

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(
		ctx,
		`SELECT pg_advisory_lock($1)`,
		pauseLock,
	); err != nil {
		t.Fatal(err)
	}
	pauseLocked := true
	defer func() {
		if pauseLocked {
			_, _ = blocker.Exec(
				context.Background(),
				`SELECT pg_advisory_unlock($1)`,
				pauseLock,
			)
		}
	}()

	runCtx, cancelRun := context.WithCancel(ctx)
	type runResult struct {
		result RetentionResult
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, runErr := runner.RunOnce(runCtx, now, 7)
		done <- runResult{result: result, err: runErr}
	}()
	waitForRetentionDeletePause(t, pool)
	cancelRun()

	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) ||
			got.result != (RetentionResult{}) {
			t.Fatalf(
				"canceled result=%+v error=%v want zero,context.Canceled",
				got.result,
				got.err,
			)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retention runner did not return after caller cancellation")
	}

	probe, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Release()
	var acquired bool
	if err := probe.QueryRow(
		ctx,
		`SELECT pg_try_advisory_lock($1)`,
		retentionAdvisoryLockKey,
	).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("canceled retention run retained the advisory lock")
	}
	if _, err := probe.Exec(
		ctx,
		`SELECT pg_advisory_unlock($1)`,
		retentionAdvisoryLockKey,
	); err != nil {
		t.Fatal(err)
	}
	assertRetentionTableCounts(t, pool, 1)
	assertRetentionAuditCount(t, pool, 0)

	if _, err := blocker.Exec(
		ctx,
		`SELECT pg_advisory_unlock($1)`,
		pauseLock,
	); err != nil {
		t.Fatal(err)
	}
	pauseLocked = false
}

func migratedRetentionRunner(
	t *testing.T,
) (*pgxpool.Pool, *PostgresRetentionRunner) {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
TRUNCATE
  operational_samples,
  alert_deliveries,
  alert_webhook_events,
  operational_alerts,
  restore_verifications,
  backup_artifacts,
  backup_runs,
  audit_logs
CASCADE`); err != nil {
		t.Fatal(err)
	}
	return pool, NewPostgresRetentionRunner(pool)
}

func seedRetentionBatch(
	t *testing.T,
	pool *pgxpool.Pool,
	now time.Time,
	count int,
) {
	t.Helper()
	old := now.AddDate(0, 0, -metadataRetentionDays).Add(-time.Second)
	sampleOld := now.AddDate(
		0,
		0,
		-defaultOperationalSampleRetentionDays,
	).Add(-time.Second)
	if _, err := pool.Exec(context.Background(), `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at
)
SELECT 'app','backup_age_seconds','local',series,'seconds',$1
FROM generate_series(1,$3::int) AS series;

INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  resolved_at,current_value,threshold_value,summary
)
SELECT
  'retention-batch-'||series,'backup','warning','resolved',$2,$2,$2,
  series,series+1,'batch retention alert'
FROM generate_series(1,$3::int) AS series;

INSERT INTO alert_deliveries(
  alert_id,attempt,destination,outcome,started_at,finished_at
)
SELECT id,1,'webhook','failed',$2,$2
FROM operational_alerts
WHERE dedupe_key LIKE 'retention-batch-%';

INSERT INTO backup_runs(
  idempotency_key,trigger_kind,state,requested_at,started_at,finished_at
)
SELECT 'retention-batch-'||series,'scheduled','failed',$2,$2,$2
FROM generate_series(1,$3::int) AS series;

INSERT INTO restore_verifications(
  backup_run_id,state,started_at,finished_at
)
SELECT id,'failed',$2,$2
FROM backup_runs
WHERE idempotency_key LIKE 'retention-batch-%'`,
		pgx.QueryExecModeSimpleProtocol,
		sampleOld,
		old,
		count,
	); err != nil {
		t.Fatal(err)
	}
}

func waitForRetentionDeletePause(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(context.Background(), `
SELECT EXISTS(
  SELECT 1
  FROM pg_stat_activity
  WHERE wait_event_type='Lock'
    AND wait_event='advisory'
    AND query LIKE '%DELETE FROM operational_samples%'
)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("retention delete did not pause on the test advisory lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertRetentionTableCounts(
	t *testing.T,
	pool *pgxpool.Pool,
	want int,
) {
	t.Helper()
	for _, table := range []string{
		"operational_samples",
		"alert_deliveries",
		"operational_alerts",
		"restore_verifications",
		"backup_runs",
	} {
		var got int
		if err := pool.QueryRow(
			context.Background(),
			"SELECT count(*) FROM "+table,
		).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s count=%d want=%d", table, got, want)
		}
	}
}

func assertRetentionAuditCount(
	t *testing.T,
	pool *pgxpool.Pool,
	want int,
) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), `
SELECT count(*)
FROM audit_logs
WHERE action='operations.retention_completed'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("retention audit count=%d want=%d", got, want)
	}
}

func equalJSONMaps(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
