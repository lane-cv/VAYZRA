package operations

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWebhookRunnerClaimsSendsAndCompletesOneDelivery(t *testing.T) {
	now := time.Date(2026, 7, 30, 14, 0, 0, 0, time.UTC)
	job := webhookRunnerJob(now)
	store := &webhookRunnerStoreStub{job: &job}
	sender := &webhookRunnerSenderStub{
		result: WebhookDeliveryResult{
			Succeeded: true, HTTPStatusClass: 2,
		},
	}
	runner := webhookTestRunner(store, sender, now)
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.claims != 1 || sender.calls != 1 ||
		store.completes != 1 || store.abandons != 0 {
		t.Fatalf(
			"claims=%d sends=%d completes=%d abandons=%d",
			store.claims,
			sender.calls,
			store.completes,
			store.abandons,
		)
	}
	if store.owner != runner.ClaimOwner ||
		store.token == uuid.Nil ||
		store.leaseDuration != runner.LeaseDuration {
		t.Fatalf(
			"owner=%q token=%s lease=%s",
			store.owner,
			store.token,
			store.leaseDuration,
		)
	}
	if store.completedJob.ClaimToken != job.ClaimToken ||
		store.completedResult != sender.result ||
		!store.finishedAt.Equal(now) {
		t.Fatalf(
			"job=%+v result=%+v finished=%s",
			store.completedJob,
			store.completedResult,
			store.finishedAt,
		)
	}
	if sender.payload.AlertID != job.AlertID.String() ||
		sender.payload.DashboardPath != WebhookDashboardPath {
		t.Fatalf("payload=%+v", sender.payload)
	}
}

func TestWebhookRunnerNoDueDeliveryIsNoop(t *testing.T) {
	now := time.Now().UTC()
	store := &webhookRunnerStoreStub{}
	sender := &webhookRunnerSenderStub{}
	if err := webhookTestRunner(store, sender, now).RunOnce(
		context.Background(),
	); err != nil {
		t.Fatal(err)
	}
	if store.claims != 1 || sender.calls != 0 ||
		store.completes != 0 || store.abandons != 0 {
		t.Fatalf(
			"claims=%d sends=%d completes=%d abandons=%d",
			store.claims,
			sender.calls,
			store.completes,
			store.abandons,
		)
	}
}

func TestWebhookRunnerPersistsBoundedSenderClassification(t *testing.T) {
	now := time.Now().UTC()
	job := webhookRunnerJob(now)
	store := &webhookRunnerStoreStub{job: &job}
	sender := &webhookRunnerSenderStub{result: WebhookDeliveryResult{
		Retryable: true, ErrorCategory: "timeout",
	}}
	if err := webhookTestRunner(store, sender, now).RunOnce(
		context.Background(),
	); err != nil {
		t.Fatal(err)
	}
	if store.completes != 1 ||
		store.completedResult.ErrorCategory != "timeout" ||
		!store.completedResult.Retryable {
		t.Fatalf("completed=%d result=%+v", store.completes, store.completedResult)
	}
}

func TestWebhookRunnerCancellationAbandonsClaim(t *testing.T) {
	now := time.Now().UTC()
	job := webhookRunnerJob(now)
	store := &webhookRunnerStoreStub{job: &job}
	sending := make(chan struct{})
	sender := &webhookRunnerSenderStub{send: func(ctx context.Context) WebhookDeliveryResult {
		close(sending)
		<-ctx.Done()
		return WebhookDeliveryResult{
			Retryable: true, ErrorCategory: "timeout",
		}
	}}
	runner := webhookTestRunner(store, sender, now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runner.RunOnce(ctx)
	}()
	select {
	case <-sending:
	case <-time.After(time.Second):
		t.Fatal("sender was not called")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop after cancellation")
	}
	if store.abandons != 1 || store.completes != 0 {
		t.Fatalf(
			"abandons=%d completes=%d",
			store.abandons,
			store.completes,
		)
	}
}

func TestWebhookRunnerPoisonPayloadIsPermanentSafeFailure(t *testing.T) {
	now := time.Now().UTC()
	job := webhookRunnerJob(now)
	job.Event.Summary = "\nunsafe"
	store := &webhookRunnerStoreStub{job: &job}
	sender := &webhookRunnerSenderStub{}
	if err := webhookTestRunner(store, sender, now).RunOnce(
		context.Background(),
	); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 0 || store.completes != 1 ||
		store.completedResult.Retryable ||
		store.completedResult.ErrorCategory != "protocol_error" {
		t.Fatalf(
			"sends=%d completes=%d result=%+v",
			sender.calls,
			store.completes,
			store.completedResult,
		)
	}
}

func TestStartWebhookDeliveryRunnerRunsImmediatelyAndStopsIdempotently(
	t *testing.T,
) {
	now := time.Now().UTC()
	job := webhookRunnerJob(now)
	store := &webhookRunnerStoreStub{job: &job}
	sent := make(chan struct{}, 1)
	sender := &webhookRunnerSenderStub{send: func(context.Context) WebhookDeliveryResult {
		select {
		case sent <- struct{}{}:
		default:
		}
		return WebhookDeliveryResult{
			Succeeded: true, HTTPStatusClass: 2,
		}
	}}
	runner := webhookTestRunner(store, sender, now)
	runner.PollInterval = time.Hour
	stop, err := StartWebhookDeliveryRunner(runner)
	if err != nil || stop == nil {
		t.Fatalf("nil stop=%t err=%v", stop == nil, err)
	}
	select {
	case <-sent:
	case <-time.After(time.Second):
		stop()
		t.Fatal("runner did not send immediately")
	}
	stop()
	stop()
	calls := sender.callCount()
	time.Sleep(20 * time.Millisecond)
	if sender.callCount() != calls {
		t.Fatalf("send continued after stop: before=%d after=%d", calls, sender.callCount())
	}
}

type webhookRunnerStoreStub struct {
	mu              sync.Mutex
	job             *WebhookDeliveryJob
	claimErr        error
	completeErr     error
	abandonErr      error
	claims          int
	completes       int
	abandons        int
	owner           string
	token           uuid.UUID
	leaseDuration   time.Duration
	completedJob    WebhookDeliveryJob
	completedResult WebhookDeliveryResult
	finishedAt      time.Time
	abandonedJob    WebhookDeliveryJob
}

func (store *webhookRunnerStoreStub) Claim(
	_ context.Context,
	owner string,
	token uuid.UUID,
	_ time.Time,
	leaseDuration time.Duration,
) (*WebhookDeliveryJob, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.claims++
	store.owner = owner
	store.token = token
	store.leaseDuration = leaseDuration
	if store.claimErr != nil {
		return nil, store.claimErr
	}
	if store.job == nil {
		return nil, nil
	}
	job := *store.job
	store.job = nil
	return &job, nil
}

func (store *webhookRunnerStoreStub) Complete(
	_ context.Context,
	job WebhookDeliveryJob,
	result WebhookDeliveryResult,
	finishedAt time.Time,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.completes++
	store.completedJob = job
	store.completedResult = result
	store.finishedAt = finishedAt
	return store.completeErr
}

func (store *webhookRunnerStoreStub) Abandon(
	_ context.Context,
	job WebhookDeliveryJob,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.abandons++
	store.abandonedJob = job
	return store.abandonErr
}

type webhookRunnerSenderStub struct {
	mu      sync.Mutex
	result  WebhookDeliveryResult
	send    func(context.Context) WebhookDeliveryResult
	payload WebhookPayload
	calls   int
}

func (sender *webhookRunnerSenderStub) Send(
	ctx context.Context,
	payload WebhookPayload,
) WebhookDeliveryResult {
	sender.mu.Lock()
	sender.calls++
	sender.payload = payload
	send := sender.send
	result := sender.result
	sender.mu.Unlock()
	if send != nil {
		return send(ctx)
	}
	return result
}

func (sender *webhookRunnerSenderStub) callCount() int {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return sender.calls
}

func webhookTestRunner(
	store WebhookDeliveryStore,
	sender WebhookDeliverySender,
	now time.Time,
) WebhookDeliveryRunner {
	return WebhookDeliveryRunner{
		Store:  store,
		Sender: sender,
		Clock: func() time.Time {
			return now
		},
		NewUUID: func() uuid.UUID {
			return uuid.New()
		},
		ClaimOwner:      "webhook-runner-test",
		PollInterval:    time.Second,
		LeaseDuration:   5 * time.Second,
		DeliveryTimeout: time.Second,
	}
}

func webhookRunnerJob(now time.Time) WebhookDeliveryJob {
	event := WebhookEvent{
		ID:              uuid.New(),
		AlertID:         uuid.New(),
		TransitionKind:  AlertTransitionOpened,
		AlertVersion:    1,
		Category:        "processing",
		Severity:        AlertSeverityWarning,
		State:           AlertStateOpen,
		Summary:         "Processing queue depth is high",
		CurrentValue:    21,
		ThresholdValue:  20,
		FirstObservedAt: now.Add(-time.Minute),
		LastObservedAt:  now,
	}
	return WebhookDeliveryJob{
		ID:             uuid.New(),
		EventID:        event.ID,
		AlertID:        event.AlertID,
		Attempt:        1,
		ScheduledAt:    now,
		StartedAt:      now,
		ClaimOwner:     "webhook-runner-test",
		ClaimToken:     uuid.New(),
		ClaimExpiresAt: now.Add(5 * time.Second),
		Event:          event,
	}
}
