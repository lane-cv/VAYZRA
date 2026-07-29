package operations

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresAlertTransitionsAtomicallyEnqueueIndependentWebhookEvents(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	now := alertPostgresClock(t, pool)
	clock := now
	ids := deterministicUUIDs(24)
	store, err := NewPostgresAlertStoreWithWebhookOutbox(
		pool,
		func() time.Time { return clock },
		ids.Next,
	)
	if err != nil {
		t.Fatal(err)
	}
	rule := Rule{
		DedupeKey: "webhook_lifecycle", Category: "processing",
		Summary: "Processing queue depth is high",
		Warning: 20, Critical: 100, Direction: DirectionAbove, MinimumSamples: 1,
	}

	evaluate := func(at time.Time, value float64) AlertTransition {
		t.Helper()
		clock = at
		transition, evaluateErr := store.EvaluateAlert(ctx, Evaluation{
			Rule: rule, Value: value, ObservedAt: at,
			Available: true, SampleCount: 1,
		})
		if evaluateErr != nil {
			t.Fatal(evaluateErr)
		}
		return transition
	}
	if got := evaluate(now, 21); got.Kind != AlertTransitionNone {
		t.Fatalf("candidate transition=%+v", got)
	}
	opened := evaluate(now.Add(time.Minute), 22)
	if opened.Kind != AlertTransitionOpened || opened.Alert == nil {
		t.Fatalf("opened=%+v", opened)
	}
	if got := evaluate(now.Add(2*time.Minute), 23); got.Kind != AlertTransitionUpdated {
		t.Fatalf("updated=%+v", got)
	}
	upgraded := evaluate(now.Add(3*time.Minute), 101)
	if upgraded.Kind != AlertTransitionUpgraded || upgraded.Alert == nil {
		t.Fatalf("upgraded=%+v", upgraded)
	}
	for minute := 4; minute <= 6; minute++ {
		transition := evaluate(now.Add(time.Duration(minute)*time.Minute), 0)
		if minute < 6 && transition.Kind != AlertTransitionUpdated {
			t.Fatalf("healthy minute=%d transition=%+v", minute, transition)
		}
		if minute == 6 &&
			(transition.Kind != AlertTransitionResolved ||
				transition.Alert == nil) {
			t.Fatalf("resolved=%+v", transition)
		}
	}

	rows, err := pool.Query(ctx, `
SELECT transition_kind,alert_version,category,severity,state,summary,
       current_value,threshold_value,first_observed_at,last_observed_at,
       enqueued_at
FROM alert_webhook_events
WHERE alert_id=$1
ORDER BY alert_version`, opened.Alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []webhookEventRow
	for rows.Next() {
		var event webhookEventRow
		if err := rows.Scan(
			&event.Kind,
			&event.Version,
			&event.Category,
			&event.Severity,
			&event.State,
			&event.Summary,
			&event.Current,
			&event.Threshold,
			&event.FirstObservedAt,
			&event.LastObservedAt,
			&event.EnqueuedAt,
		); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 ||
		events[0].Kind != AlertTransitionOpened ||
		events[0].Version != opened.Alert.Version ||
		events[1].Kind != AlertTransitionUpgraded ||
		events[1].Version != upgraded.Alert.Version ||
		events[2].Kind != AlertTransitionResolved {
		t.Fatalf("events=%+v", events)
	}
	for _, event := range events {
		if event.Category != rule.Category ||
			event.Summary != rule.Summary ||
			event.FirstObservedAt.IsZero() ||
			event.LastObservedAt.IsZero() ||
			!event.EnqueuedAt.Equal(event.LastObservedAt) {
			t.Fatalf("unsafe or incomplete event snapshot=%+v", event)
		}
	}

	attemptRows, err := pool.Query(ctx, `
SELECT event_id,attempt,delivery_state,
       (extract(epoch FROM scheduled_at - e.enqueued_at) * 1000000000)::bigint
FROM alert_deliveries d
JOIN alert_webhook_events e ON e.id=d.event_id
WHERE e.alert_id=$1
ORDER BY e.alert_version,d.attempt`, opened.Alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer attemptRows.Close()
	offsets := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}
	count := 0
	for attemptRows.Next() {
		var (
			eventID uuid.UUID
			attempt int
			state   string
			offset  int64
		)
		if err := attemptRows.Scan(&eventID, &attempt, &state, &offset); err != nil {
			t.Fatal(err)
		}
		if eventID == uuid.Nil ||
			attempt != count%3+1 ||
			state != "pending" ||
			time.Duration(offset) != offsets[count%3] {
			t.Fatalf(
				"row=%d event=%s attempt=%d state=%q offset=%s",
				count,
				eventID,
				attempt,
				state,
				time.Duration(offset),
			)
		}
		count++
	}
	if err := attemptRows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 9 {
		t.Fatalf("attempt count=%d want=9", count)
	}
}

func TestPostgresWebhookOutboxRollsBackAlertTransitionOnEnqueueFailure(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	now := alertPostgresClock(t, pool)
	clock := now
	ids := deterministicUUIDs(8)
	store, err := NewPostgresAlertStoreWithWebhookOutbox(
		pool,
		func() time.Time { return clock },
		ids.Next,
	)
	if err != nil {
		t.Fatal(err)
	}
	rule := Rule{
		DedupeKey: "webhook_atomicity", Category: "processing",
		Summary: "Processing queue depth is high",
		Warning: 20, Critical: 100, Direction: DirectionAbove, MinimumSamples: 1,
	}
	if _, err := store.EvaluateAlert(ctx, Evaluation{
		Rule: rule, Value: 21, ObservedAt: now,
		Available: true, SampleCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
CREATE OR REPLACE FUNCTION reject_webhook_attempt() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.event_id IS NOT NULL THEN
    RAISE EXCEPTION 'forced webhook outbox failure';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER reject_webhook_attempt
BEFORE INSERT ON alert_deliveries
FOR EACH ROW EXECUTE FUNCTION reject_webhook_attempt()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
DROP TRIGGER IF EXISTS reject_webhook_attempt ON alert_deliveries;
DROP FUNCTION IF EXISTS reject_webhook_attempt()`)
	})

	clock = now.Add(time.Minute)
	if _, err := store.EvaluateAlert(ctx, Evaluation{
		Rule: rule, Value: 22, ObservedAt: clock,
		Available: true, SampleCount: 1,
	}); err == nil {
		t.Fatal("forced outbox failure was ignored")
	}
	var failures int
	var version int64
	if err := pool.QueryRow(ctx, `
SELECT consecutive_failures,version
FROM operational_alerts
WHERE dedupe_key=$1`, rule.DedupeKey).Scan(&failures, &version); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM alert_webhook_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if failures != 1 || version != 1 || events != 0 {
		t.Fatalf("failures=%d version=%d events=%d", failures, version, events)
	}
}

func TestPostgresWebhookOutboxTransitionReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	now := alertPostgresClock(t, pool)
	clock := now
	ids := deterministicUUIDs(16)
	store, err := NewPostgresAlertStoreWithWebhookOutbox(
		pool,
		func() time.Time { return clock },
		ids.Next,
	)
	if err != nil {
		t.Fatal(err)
	}
	alertID := insertVisibleAlert(t, ctx, pool, "webhook_replay")
	clock = alertPostgresClock(t, pool)
	alert, err := scanAlert(pool.QueryRow(ctx, `
SELECT `+alertSelectColumns+` FROM operational_alerts WHERE id=$1`, alertID))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for range 2 {
		if err := store.enqueueWebhookTransition(
			ctx,
			tx,
			AlertTransitionOpened,
			alert,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var events, attempts int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM alert_webhook_events WHERE alert_id=$1),
  (SELECT count(*) FROM alert_deliveries WHERE alert_id=$1 AND event_id IS NOT NULL)`,
		alertID,
	).Scan(&events, &attempts); err != nil {
		t.Fatal(err)
	}
	if events != 1 || attempts != 3 {
		t.Fatalf("events=%d attempts=%d", events, attempts)
	}
}

func TestPostgresAlertStoreWithoutWebhookDoesNotBuildBacklog(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	now := alertPostgresClock(t, pool)
	rule := Rule{
		DedupeKey: "webhook_disabled", Category: "processing",
		Summary: "Processing queue depth is high",
		Warning: 20, Critical: 100, Direction: DirectionAbove, MinimumSamples: 1,
	}
	for minute := range 2 {
		if _, err := store.EvaluateAlert(ctx, Evaluation{
			Rule: rule, Value: 21,
			ObservedAt: now.Add(time.Duration(minute) * time.Minute),
			Available:  true, SampleCount: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var events int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM alert_webhook_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("disabled webhook events=%d", events)
	}
}

type deterministicUUIDSequence struct {
	values []uuid.UUID
	index  int
}

func deterministicUUIDs(count int) *deterministicUUIDSequence {
	values := make([]uuid.UUID, count)
	for index := range values {
		values[index] = uuid.MustParse(
			"10000000-0000-4000-8000-" +
				leftPadInt(index+1, 12),
		)
	}
	return &deterministicUUIDSequence{values: values}
}

func (sequence *deterministicUUIDSequence) Next() uuid.UUID {
	if sequence.index >= len(sequence.values) {
		return uuid.Nil
	}
	value := sequence.values[sequence.index]
	sequence.index++
	return value
}

func leftPadInt(value, width int) string {
	text := strconv.Itoa(value)
	return strings.Repeat("0", width-len(text)) + text
}

type webhookEventRow struct {
	Kind            AlertTransitionKind
	Version         int64
	Category        string
	Severity        AlertSeverity
	State           AlertState
	Summary         string
	Current         float64
	Threshold       float64
	FirstObservedAt time.Time
	LastObservedAt  time.Time
	EnqueuedAt      time.Time
}
