package operations

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresAlertLifecycleIsDurableAndThresholdsAreFutureOnly(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	now := alertPostgresClock(t, pool)
	rule := Rule{
		DedupeKey: "ai_error_rate", Category: "ai", Summary: "AI error rate is high",
		Warning: 10, Critical: 25, Direction: DirectionAbove, MinimumSamples: 20,
	}

	first, err := store.EvaluateAlert(ctx, Evaluation{
		Rule: rule, Value: 12, ObservedAt: now, Available: true, SampleCount: 20,
	})
	if err != nil || first.Kind != AlertTransitionNone || first.Alert != nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	page, err := store.ListAlerts(ctx, AlertFilter{Limit: 50})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("first failure page=%+v err=%v", page, err)
	}
	summary, err := NewPostgresDashboardReader(pool).ReadAlertSummary(ctx, now)
	if err != nil ||
		summary.OpenWarning != 0 || summary.OpenCritical != 0 {
		t.Fatalf("first failure dashboard summary=%+v err=%v", summary, err)
	}

	opened, err := store.EvaluateAlert(ctx, Evaluation{
		Rule: rule, Value: 15, ObservedAt: now.Add(time.Minute),
		Available: true, SampleCount: 20,
	})
	if err != nil || opened.Kind != AlertTransitionOpened || opened.Alert == nil {
		t.Fatalf("opened=%+v err=%v", opened, err)
	}
	alertID := opened.Alert.ID
	if opened.Alert.State != AlertStateOpen ||
		opened.Alert.Severity != AlertSeverityWarning ||
		opened.Alert.ConsecutiveFailures != 2 ||
		!opened.Alert.FirstObservedAt.Equal(now) ||
		opened.Alert.ThresholdValue != rule.Warning {
		t.Fatalf("opened alert=%+v", opened.Alert)
	}

	upgraded, err := store.EvaluateAlert(ctx, Evaluation{
		Rule: rule, Value: 30, ObservedAt: now.Add(2 * time.Minute),
		Available: true, SampleCount: 20,
	})
	if err != nil || upgraded.Kind != AlertTransitionUpgraded ||
		upgraded.Alert == nil || upgraded.Alert.ID != alertID ||
		upgraded.Alert.Severity != AlertSeverityCritical ||
		upgraded.Alert.ThresholdValue != rule.Critical {
		t.Fatalf("upgraded=%+v err=%v", upgraded, err)
	}

	admin := alertAdmin(t, ctx, pool)
	acknowledged, err := store.AcknowledgeAlert(ctx, admin, alertID)
	if err != nil {
		t.Fatal(err)
	}
	if acknowledged.State != AlertStateAcknowledged ||
		acknowledged.AcknowledgedBy != admin.User.ID ||
		acknowledged.AcknowledgedAt == nil ||
		acknowledged.ResolvedAt != nil {
		t.Fatalf("acknowledged=%+v", acknowledged)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM audit_logs
WHERE action='operations.alert_acknowledged'
  AND target_type='operational_alert'
  AND target_id=$1`, alertID.String()).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("ack audit count=%d", auditCount)
	}

	ongoing, err := store.EvaluateAlert(ctx, Evaluation{
		Rule: rule, Value: 35, ObservedAt: now.Add(3 * time.Minute),
		Available: true, SampleCount: 20,
	})
	if err != nil || ongoing.Alert == nil ||
		ongoing.Alert.State != AlertStateAcknowledged ||
		ongoing.Alert.ResolvedAt != nil {
		t.Fatalf("ongoing=%+v err=%v", ongoing, err)
	}

	changed := rule
	changed.Warning = 40
	changed.Critical = 60
	for index := 4; index <= 6; index++ {
		transition, evaluateErr := store.EvaluateAlert(ctx, Evaluation{
			Rule: changed, Value: 35,
			ObservedAt: now.Add(time.Duration(index) * time.Minute),
			Available:  true, SampleCount: 20,
		})
		if evaluateErr != nil {
			t.Fatal(evaluateErr)
		}
		if index < 6 {
			if transition.Alert == nil ||
				transition.Alert.State != AlertStateAcknowledged ||
				transition.Alert.ConsecutiveSuccesses != index-3 ||
				transition.Alert.ThresholdValue != changed.Warning {
				t.Fatalf("healthy index=%d transition=%+v", index, transition)
			}
			continue
		}
		if transition.Kind != AlertTransitionResolved ||
			transition.Alert == nil ||
			transition.Alert.State != AlertStateResolved ||
			transition.Alert.ResolvedAt == nil ||
			transition.Alert.ID != alertID ||
			!transition.Alert.FirstObservedAt.Equal(now) {
			t.Fatalf("resolved=%+v", transition)
		}
	}

	if _, err := store.AcknowledgeAlert(ctx, admin, alertID); !errors.Is(err, ErrAlertAlreadyResolved) {
		t.Fatalf("resolved acknowledge error=%v", err)
	}
}

func TestPostgresAlertDependencyUnavailableHysteresisAndUnresolvedDedupe(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	now := alertPostgresClock(t, pool)
	thresholdRule := Rule{
		DedupeKey: "filesystem_root_usage", Category: "storage",
		Summary: "Root filesystem usage is high",
		Warning: 75, Critical: 90, Direction: DirectionAbove, MinimumSamples: 1,
	}
	first, err := alertDependencyEvaluation(Evaluation{
		Rule: thresholdRule, ObservedAt: now, Available: false,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EvaluateAlert(ctx, first); err != nil {
		t.Fatal(err)
	}
	const parallel = 12
	errs := make(chan error, parallel)
	var wait sync.WaitGroup
	for index := 0; index < parallel; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			evaluation := first
			evaluation.ObservedAt = now.Add(time.Minute)
			_, err := store.EvaluateAlert(ctx, Evaluation{
				Rule: evaluation.Rule, Value: evaluation.Value,
				ObservedAt: evaluation.ObservedAt,
				Available:  true, SampleCount: 1,
			})
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows, failures int
	if err := pool.QueryRow(ctx, `
SELECT count(*),coalesce(max(consecutive_failures),0)
FROM operational_alerts
WHERE dedupe_key=$1 AND state<>'resolved'`, first.Rule.DedupeKey).
		Scan(&rows, &failures); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || failures != 2 {
		t.Fatalf("rows=%d failures=%d", rows, failures)
	}
	page, err := store.ListAlerts(ctx, AlertFilter{Limit: 50})
	if err != nil || len(page.Items) != 1 ||
		page.Items[0].Severity != AlertSeverityWarning {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestPostgresAlertInsufficientSamplesDoNotAdvanceOrResolveExistingAlert(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	now := alertPostgresClock(t, pool)
	rule := Rule{
		DedupeKey: "ai_error_rate", Category: "ai",
		Summary: "AI error rate is high",
		Warning: 10, Critical: 25, Direction: DirectionAbove, MinimumSamples: 20,
	}
	for index := 0; index < 2; index++ {
		if _, err := store.EvaluateAlert(ctx, Evaluation{
			Rule: rule, Value: 30,
			ObservedAt: now.Add(time.Duration(index) * time.Minute),
			Available:  true, SampleCount: 20,
		}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := scanAlert(pool.QueryRow(ctx, `
SELECT `+alertSelectColumns+`
FROM operational_alerts
WHERE dedupe_key=$1 AND state<>'resolved'`, rule.DedupeKey))
	if err != nil {
		t.Fatal(err)
	}
	for index := 2; index < 5; index++ {
		transition, err := store.EvaluateAlert(ctx, Evaluation{
			Rule: rule, Value: 100,
			ObservedAt: now.Add(time.Duration(index) * time.Minute),
			Available:  true, SampleCount: 19,
		})
		if err != nil || transition.Kind != AlertTransitionNone ||
			transition.Alert != nil {
			t.Fatalf("index=%d transition=%+v err=%v", index, transition, err)
		}
	}
	after, err := scanAlert(pool.QueryRow(ctx, `
SELECT `+alertSelectColumns+`
FROM operational_alerts
WHERE dedupe_key=$1 AND state<>'resolved'`, rule.DedupeKey))
	if err != nil {
		t.Fatal(err)
	}
	if after.State != AlertStateOpen ||
		after.ConsecutiveFailures != before.ConsecutiveFailures ||
		after.ConsecutiveSuccesses != before.ConsecutiveSuccesses ||
		after.Version != before.Version ||
		!after.LastObservedAt.Equal(before.LastObservedAt) {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}

func TestPostgresDependencyRecoveryResolvesOnlyUnavailableAlert(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	now := alertPostgresClock(t, pool)
	thresholdRule := Rule{
		DedupeKey: "processing_queue_depth", Category: "processing",
		Summary: "Processing queue depth is high",
		Warning: 20, Critical: 100, Direction: DirectionAbove, MinimumSamples: 1,
	}
	for index := 0; index < 2; index++ {
		at := now.Add(time.Duration(index) * time.Minute)
		if _, err := store.EvaluateAlert(ctx, Evaluation{
			Rule: thresholdRule, Value: 21, ObservedAt: at,
			Available: true, SampleCount: 1,
		}); err != nil {
			t.Fatal(err)
		}
		dependency, err := alertDependencyEvaluation(Evaluation{
			Rule: thresholdRule, ObservedAt: at, Available: false,
		}, at)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.EvaluateAlert(ctx, dependency); err != nil {
			t.Fatal(err)
		}
	}
	before, err := scanAlert(pool.QueryRow(ctx, `
SELECT `+alertSelectColumns+`
FROM operational_alerts
WHERE dedupe_key=$1 AND state<>'resolved'`, thresholdRule.DedupeKey))
	if err != nil {
		t.Fatal(err)
	}
	for index := 2; index < 5; index++ {
		at := now.Add(time.Duration(index) * time.Minute)
		dependency, err := alertDependencyEvaluation(Evaluation{
			Rule: thresholdRule, ObservedAt: at, Available: true,
		}, at)
		if err != nil {
			t.Fatal(err)
		}
		transition, err := store.EvaluateAlert(ctx, dependency)
		if err != nil {
			t.Fatal(err)
		}
		if index == 4 &&
			(transition.Kind != AlertTransitionResolved ||
				transition.Alert == nil ||
				transition.Alert.State != AlertStateResolved) {
			t.Fatalf("dependency transition=%+v", transition)
		}
	}
	after, err := scanAlert(pool.QueryRow(ctx, `
SELECT `+alertSelectColumns+`
FROM operational_alerts
WHERE dedupe_key=$1 AND state<>'resolved'`, thresholdRule.DedupeKey))
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID ||
		after.State != before.State ||
		after.ConsecutiveFailures != before.ConsecutiveFailures ||
		after.ConsecutiveSuccesses != before.ConsecutiveSuccesses ||
		after.Version != before.Version ||
		!after.LastObservedAt.Equal(before.LastObservedAt) {
		t.Fatalf("threshold before=%+v after=%+v", before, after)
	}
}

func TestPostgresAlertCandidateClearsBeforeOpening(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	now := alertPostgresClock(t, pool)
	rule := Rule{
		DedupeKey: "processing_queue_depth", Category: "processing",
		Summary: "Processing queue depth is high",
		Warning: 20, Critical: 100, Direction: DirectionAbove, MinimumSamples: 1,
	}
	evaluations := []Evaluation{
		{Rule: rule, Value: 21, ObservedAt: now, Available: true, SampleCount: 1},
		{Rule: rule, Value: 0, ObservedAt: now.Add(time.Minute), Available: true, SampleCount: 1},
		{Rule: rule, Value: 21, ObservedAt: now.Add(2 * time.Minute), Available: true, SampleCount: 1},
	}
	for _, evaluation := range evaluations {
		transition, err := store.EvaluateAlert(ctx, evaluation)
		if err != nil || transition.Kind != AlertTransitionNone {
			t.Fatalf("transition=%+v err=%v", transition, err)
		}
	}
	page, err := store.ListAlerts(ctx, AlertFilter{Limit: 50})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestPostgresAlertDeliveryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	alertID := insertVisibleAlert(t, ctx, pool, "delivery_idempotency")
	now := alertPostgresClock(t, pool).Add(123 * time.Nanosecond)
	delivery := AlertDelivery{
		ID: uuid.New(), AlertID: alertID, Attempt: 1, Destination: "webhook",
		Outcome: "failed", ErrorCategory: "timeout",
		StartedAt: now, FinishedAt: now.Add(time.Second + 198*time.Nanosecond),
	}
	if err := store.RecordAlertDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	replay := delivery
	replay.ID = uuid.New()
	if err := store.RecordAlertDelivery(ctx, replay); err != nil {
		t.Fatalf("idempotent replay error=%v", err)
	}
	conflict := replay
	conflict.Outcome = "succeeded"
	conflict.ErrorCategory = ""
	conflict.HTTPStatusClass = 2
	if err := store.RecordAlertDelivery(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting replay error=%v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM alert_deliveries
WHERE alert_id=$1 AND attempt=1 AND destination='webhook'`, alertID).
		Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("delivery count=%d", count)
	}
}

func TestPostgresAlertRunnerLeaseExcludesAndAllowsTakeover(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	firstStore := NewPostgresAlertStore(pool)
	secondStore := NewPostgresAlertStore(pool)

	first, acquired, err := firstStore.TryAcquireAlertRunnerLease(ctx)
	if err != nil || !acquired || first == nil {
		t.Fatalf("first acquired=%t lease=%v err=%v", acquired, first, err)
	}
	if second, acquired, err := secondStore.TryAcquireAlertRunnerLease(ctx); err != nil || acquired || second != nil {
		t.Fatalf("second acquired=%t lease=%v err=%v", acquired, second, err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	takeover, acquired, err := secondStore.TryAcquireAlertRunnerLease(ctx)
	if err != nil || !acquired || takeover == nil {
		t.Fatalf("takeover acquired=%t lease=%v err=%v", acquired, takeover, err)
	}
	if err := takeover.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := takeover.Release(ctx); err != nil {
		t.Fatalf("idempotent release error=%v", err)
	}
}

func TestPostgresAlertListUsesStableTupleKeysetAndExactFilters(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	now := alertPostgresClock(t, pool)
	ids := []uuid.UUID{
		uuid.MustParse("10000000-0000-4000-8000-000000000001"),
		uuid.MustParse("10000000-0000-4000-8000-000000000002"),
		uuid.MustParse("10000000-0000-4000-8000-000000000003"),
	}
	for _, id := range ids {
		if _, err := pool.Exec(ctx, `
INSERT INTO operational_alerts(
  id,dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary,consecutive_failures,version
) VALUES($1,$2,'backup','critical','open',$3,$3,108001,108000,
         'Verified local backup is overdue',2,2)`,
			id,
			"backup_local_age_"+id.String()[35:],
			now,
		); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListAlerts(ctx, AlertFilter{
		State: AlertStateOpen, Severity: AlertSeverityCritical,
		Category: "backup", Limit: 2,
	})
	if err != nil || len(first.Items) != 2 || first.Next == nil ||
		first.Items[0].ID != ids[2] || first.Items[1].ID != ids[1] ||
		first.Next.ID != ids[1] || !first.Next.LastObservedAt.Equal(now) {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := store.ListAlerts(ctx, AlertFilter{
		State: AlertStateOpen, Severity: AlertSeverityCritical,
		Category: "backup", Before: first.Next, Limit: 2,
	})
	if err != nil || len(second.Items) != 1 || second.Next != nil ||
		second.Items[0].ID != ids[0] {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestPostgresAlertAcknowledgementAndAuditRollbackTogether(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	alertID := insertVisibleAlert(t, ctx, pool, "ack_audit_rollback")
	admin := alertAdmin(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION reject_alert_ack_audit() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.action='operations.alert_acknowledged' THEN
    RAISE EXCEPTION 'forced alert audit rejection';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER reject_alert_ack_audit
BEFORE INSERT ON audit_logs
FOR EACH ROW EXECUTE FUNCTION reject_alert_ack_audit()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS reject_alert_ack_audit ON audit_logs;
DROP FUNCTION IF EXISTS reject_alert_ack_audit()`)
	})
	if _, err := store.AcknowledgeAlert(ctx, admin, alertID); err == nil {
		t.Fatal("forced audit failure was ignored")
	}
	var state string
	var acknowledgedAt *time.Time
	if err := pool.QueryRow(ctx, `
SELECT state,acknowledged_at
FROM operational_alerts
WHERE id=$1`, alertID).Scan(&state, &acknowledgedAt); err != nil {
		t.Fatal(err)
	}
	if state != "open" || acknowledgedAt != nil {
		t.Fatalf("state=%q acknowledgedAt=%v", state, acknowledgedAt)
	}
}

func migratedAlertPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
TRUNCATE alert_deliveries,operational_alerts,audit_logs,users CASCADE;
INSERT INTO system_settings(singleton_id) VALUES(true)
ON CONFLICT(singleton_id) DO NOTHING;
UPDATE system_settings
SET disk_warning_percent=75,disk_critical_percent=90,
    ai_error_warning_percent=10,ai_error_critical_percent=25,
    processing_queue_warning=20,processing_queue_critical=100,
    version=1,updated_by=NULL,updated_at=clock_timestamp()
WHERE singleton_id=true`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func alertAdmin(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) Principal {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users(
  id,username,display_name,role,status,password_hash,must_change_password
) VALUES($1,$2,'Alert Admin','admin','active','hash',false)`,
		id, "alert-admin-"+id.String(),
	); err != nil {
		t.Fatal(err)
	}
	return Principal{
		User:      auth.User{ID: id, Role: auth.RoleAdmin, Status: auth.StatusActive},
		RequestID: "alert-acknowledge-request",
		IP:        net.ParseIP("192.0.2.81"),
	}
}

func insertVisibleAlert(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	dedupeKey string,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary,consecutive_failures
) VALUES($1,'processing','warning','open',clock_timestamp(),clock_timestamp(),
         21,20,'Processing queue depth is high',2)
RETURNING id`, dedupeKey).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func alertPostgresClock(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC()
}
