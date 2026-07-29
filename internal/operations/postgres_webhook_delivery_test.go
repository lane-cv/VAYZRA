package operations

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresWebhookDeliveryClaimLeaseRetryAndFencing(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	enqueuedAt := alertPostgresClock(t, pool)
	eventID, _ := seedWebhookEvent(t, ctx, pool, enqueuedAt, AlertTransitionOpened)
	store := NewPostgresWebhookDeliveryStore(pool)
	lease := 30 * time.Second

	early, err := store.Claim(
		ctx,
		"worker-a",
		uuid.New(),
		enqueuedAt.Add(time.Minute-time.Microsecond),
		lease,
	)
	if err != nil || early != nil {
		t.Fatalf("early=%+v err=%v", early, err)
	}
	firstToken := uuid.New()
	first, err := store.Claim(
		ctx,
		"worker-a",
		firstToken,
		enqueuedAt.Add(time.Minute),
		lease,
	)
	if err != nil || first == nil ||
		first.EventID != eventID ||
		first.Attempt != 1 ||
		first.ClaimOwner != "worker-a" ||
		first.ClaimToken != firstToken ||
		!first.ClaimExpiresAt.Equal(enqueuedAt.Add(time.Minute+lease)) {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	blocked, err := store.Claim(
		ctx,
		"worker-b",
		uuid.New(),
		enqueuedAt.Add(time.Minute+lease-time.Microsecond),
		lease,
	)
	if err != nil || blocked != nil {
		t.Fatalf("blocked=%+v err=%v", blocked, err)
	}

	reclaimToken := uuid.New()
	reclaimed, err := store.Claim(
		ctx,
		"worker-b",
		reclaimToken,
		enqueuedAt.Add(time.Minute+lease),
		lease,
	)
	if err != nil || reclaimed == nil ||
		reclaimed.ID != first.ID ||
		reclaimed.ClaimToken != reclaimToken ||
		reclaimed.ClaimOwner != "worker-b" {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
	if err := store.Complete(
		ctx,
		*first,
		WebhookDeliveryResult{
			Retryable: true, ErrorCategory: "network",
		},
		enqueuedAt.Add(time.Minute+lease+time.Second),
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale completion error=%v", err)
	}
	finishedAt := enqueuedAt.Add(time.Minute + lease + 2*time.Second)
	if err := store.Complete(
		ctx,
		*reclaimed,
		WebhookDeliveryResult{
			Retryable: true, ErrorCategory: "timeout",
		},
		finishedAt,
	); err != nil {
		t.Fatal(err)
	}
	beforeSecond, err := store.Claim(
		ctx,
		"worker-c",
		uuid.New(),
		enqueuedAt.Add(5*time.Minute-time.Microsecond),
		lease,
	)
	if err != nil || beforeSecond != nil {
		t.Fatalf("before second=%+v err=%v", beforeSecond, err)
	}
	second, err := store.Claim(
		ctx,
		"worker-c",
		uuid.New(),
		enqueuedAt.Add(5*time.Minute),
		lease,
	)
	if err != nil || second == nil || second.Attempt != 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestPostgresWebhookDeliverySuccessAndPermanentFailureCancelLaterAttempts(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		result WebhookDeliveryResult
	}{
		{
			name: "success",
			result: WebhookDeliveryResult{
				Succeeded: true, HTTPStatusClass: 2,
			},
		},
		{
			name: "permanent failure",
			result: WebhookDeliveryResult{
				Retryable: false, HTTPStatusClass: 4,
				ErrorCategory: "client_error",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := migratedAlertPool(t)
			enqueuedAt := alertPostgresClock(t, pool)
			eventID, _ := seedWebhookEvent(
				t,
				ctx,
				pool,
				enqueuedAt,
				AlertTransitionOpened,
			)
			store := NewPostgresWebhookDeliveryStore(pool)
			job, err := store.Claim(
				ctx,
				"worker",
				uuid.New(),
				enqueuedAt.Add(time.Minute),
				time.Minute,
			)
			if err != nil || job == nil {
				t.Fatalf("job=%+v err=%v", job, err)
			}
			if err := store.Complete(
				ctx,
				*job,
				test.result,
				enqueuedAt.Add(time.Minute+time.Second),
			); err != nil {
				t.Fatal(err)
			}
			rows, err := pool.Query(ctx, `
SELECT attempt,delivery_state,outcome,http_status_class,error_category
FROM alert_deliveries
WHERE event_id=$1
ORDER BY attempt`, eventID)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			for attempt := 1; attempt <= 3; attempt++ {
				if !rows.Next() {
					t.Fatalf("missing attempt %d", attempt)
				}
				var (
					gotAttempt int
					state      string
					outcome    *string
					status     *int
					category   string
				)
				if err := rows.Scan(
					&gotAttempt,
					&state,
					&outcome,
					&status,
					&category,
				); err != nil {
					t.Fatal(err)
				}
				if gotAttempt != attempt {
					t.Fatalf("attempt=%d want=%d", gotAttempt, attempt)
				}
				if attempt == 1 {
					wantState := "failed"
					if test.result.Succeeded {
						wantState = "succeeded"
					}
					if state != wantState ||
						outcome == nil ||
						*outcome != wantState {
						t.Fatalf(
							"first state=%q outcome=%v",
							state,
							outcome,
						)
					}
				} else if state != "cancelled" ||
					outcome == nil ||
					*outcome != "cancelled" ||
					status != nil ||
					category != "" {
					t.Fatalf(
						"later attempt=%d state=%q outcome=%v status=%v category=%q",
						attempt,
						state,
						outcome,
						status,
						category,
					)
				}
			}
			if rows.Next() {
				t.Fatal("unexpected extra attempt")
			}
			next, err := store.Claim(
				ctx,
				"worker-next",
				uuid.New(),
				enqueuedAt.Add(time.Hour),
				time.Minute,
			)
			if err != nil || next != nil {
				t.Fatalf("next=%+v err=%v", next, err)
			}
		})
	}
}

func TestPostgresWebhookDeliveryClaimCompetesAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	enqueuedAt := alertPostgresClock(t, pool)
	seedWebhookEvent(t, ctx, pool, enqueuedAt, AlertTransitionOpened)
	stores := []*PostgresWebhookDeliveryStore{
		NewPostgresWebhookDeliveryStore(pool),
		NewPostgresWebhookDeliveryStore(pool),
	}
	start := make(chan struct{})
	type result struct {
		job *WebhookDeliveryJob
		err error
	}
	results := make(chan result, len(stores))
	var wait sync.WaitGroup
	for index, store := range stores {
		wait.Add(1)
		go func(index int, store *PostgresWebhookDeliveryStore) {
			defer wait.Done()
			<-start
			job, err := store.Claim(
				ctx,
				"replica-"+string(rune('a'+index)),
				uuid.New(),
				enqueuedAt.Add(time.Minute),
				time.Minute,
			)
			results <- result{job: job, err: err}
		}(index, store)
	}
	close(start)
	wait.Wait()
	close(results)
	claimed := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.job != nil {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("claimed=%d want=1", claimed)
	}
}

func TestPostgresWebhookDeliveryAbandonMakesClaimImmediatelyAvailable(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	enqueuedAt := alertPostgresClock(t, pool)
	seedWebhookEvent(t, ctx, pool, enqueuedAt, AlertTransitionResolved)
	store := NewPostgresWebhookDeliveryStore(pool)
	now := enqueuedAt.Add(time.Minute)
	job, err := store.Claim(
		ctx,
		"stopping-worker",
		uuid.New(),
		now,
		time.Minute,
	)
	if err != nil || job == nil {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if err := store.Abandon(ctx, *job); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := store.Claim(
		ctx,
		"replacement-worker",
		uuid.New(),
		now,
		time.Minute,
	)
	if err != nil || reclaimed == nil || reclaimed.ID != job.ID {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
}

func seedWebhookEvent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	enqueuedAt time.Time,
	kind AlertTransitionKind,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var alertID uuid.UUID
	if err := pool.QueryRow(ctx, `
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary,consecutive_failures,version
) VALUES(
  $1,'processing','warning','open',$2,$3,21,20,
  'Processing queue depth is high',2,2
)
RETURNING id`,
		"webhook_seed_"+uuid.NewString(),
		enqueuedAt.Add(-time.Minute),
		enqueuedAt,
	).Scan(&alertID); err != nil {
		t.Fatal(err)
	}
	alert, err := scanAlert(pool.QueryRow(ctx, `
SELECT `+alertSelectColumns+` FROM operational_alerts WHERE id=$1`, alertID))
	if err != nil {
		t.Fatal(err)
	}
	if kind == AlertTransitionResolved {
		resolvedAt := enqueuedAt
		alert.State = AlertStateResolved
		alert.ResolvedAt = &resolvedAt
		alert.Version++
		if _, err := pool.Exec(ctx, `
UPDATE operational_alerts
SET state='resolved',resolved_at=$2,version=$3
WHERE id=$1`, alertID, resolvedAt, alert.Version); err != nil {
			t.Fatal(err)
		}
	}
	ids := deterministicUUIDs(8)
	outbox, err := NewPostgresAlertStoreWithWebhookOutbox(
		pool,
		func() time.Time { return enqueuedAt },
		ids.Next,
	)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := outbox.enqueueWebhookTransition(ctx, tx, kind, alert); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var eventID uuid.UUID
	if err := pool.QueryRow(ctx, `
SELECT id FROM alert_webhook_events
WHERE alert_id=$1 AND transition_kind=$2`,
		alertID,
		string(kind),
	).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	return eventID, alertID
}
