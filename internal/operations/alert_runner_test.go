package operations

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAlertRunnerEvaluatesUnderLeaseAndAlwaysReleases(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	evaluations := []Evaluation{
		alertRunnerEvaluation("filesystem_root_usage", now),
		alertRunnerEvaluation("processing_queue_depth", now),
	}
	lease := &alertRunnerLeaseStub{}
	store := &alertRunnerStoreStub{
		lease: lease, acquired: true, evaluations: evaluations,
	}
	runner := AlertRunner{
		Store: store, Clock: func() time.Time { return now },
		EvaluationTimeout: time.Second,
	}
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.acquireCalls != 1 || store.loadCalls != 1 ||
		len(store.evaluated) != len(evaluations) || lease.releases != 1 {
		t.Fatalf(
			"acquire=%d load=%d evaluated=%d releases=%d",
			store.acquireCalls,
			store.loadCalls,
			len(store.evaluated),
			lease.releases,
		)
	}
	for index := range evaluations {
		if store.evaluated[index].Rule.DedupeKey != evaluations[index].Rule.DedupeKey {
			t.Fatalf("evaluated[%d]=%+v", index, store.evaluated[index])
		}
	}
}

func TestAlertRunnerSkipsWhenLeaseIsHeld(t *testing.T) {
	store := &alertRunnerStoreStub{acquired: false}
	runner := AlertRunner{
		Store: store, Clock: time.Now, EvaluationTimeout: time.Second,
	}
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.acquireCalls != 1 || store.loadCalls != 0 || len(store.evaluated) != 0 {
		t.Fatalf(
			"acquire=%d load=%d evaluated=%d",
			store.acquireCalls,
			store.loadCalls,
			len(store.evaluated),
		)
	}
}

func TestAlertRunnerReturnsLoadAndEvaluationErrorsAfterLeaseRelease(t *testing.T) {
	sentinel := errors.New("alert runner sentinel")
	for name, configure := range map[string]func(*alertRunnerStoreStub){
		"load": func(store *alertRunnerStoreStub) {
			store.loadErr = sentinel
		},
		"evaluate": func(store *alertRunnerStoreStub) {
			store.evaluations = []Evaluation{
				alertRunnerEvaluation("filesystem_root_usage", time.Now().UTC()),
			}
			store.evaluateErr = sentinel
		},
	} {
		t.Run(name, func(t *testing.T) {
			lease := &alertRunnerLeaseStub{}
			store := &alertRunnerStoreStub{lease: lease, acquired: true}
			configure(store)
			runner := AlertRunner{
				Store: store, Clock: time.Now, EvaluationTimeout: time.Second,
			}
			if err := runner.RunOnce(context.Background()); !errors.Is(err, sentinel) {
				t.Fatalf("error=%v", err)
			}
			if lease.releases != 1 {
				t.Fatalf("releases=%d", lease.releases)
			}
		})
	}
}

func TestStartAlertRunnerRunsImmediatelyAndStopsIdempotently(t *testing.T) {
	evaluated := make(chan struct{}, 1)
	store := &alertRunnerStoreStub{
		leaseFactory: func() AlertLease {
			return &alertRunnerLeaseStub{}
		},
		acquired: true,
		evaluations: []Evaluation{
			alertRunnerEvaluation("filesystem_root_usage", time.Now().UTC()),
		},
		onEvaluate: func() {
			select {
			case evaluated <- struct{}{}:
			default:
			}
		},
	}
	stop := StartAlertRunner(AlertRunner{
		Store: store, Clock: time.Now,
		PollInterval: time.Hour, EvaluationTimeout: time.Second,
	})
	if stop == nil {
		t.Fatal("nil stop")
	}
	select {
	case <-evaluated:
	case <-time.After(time.Second):
		stop()
		t.Fatal("runner did not evaluate immediately")
	}
	stop()
	stop()
	calls := store.evaluateCalls()
	time.Sleep(20 * time.Millisecond)
	if store.evaluateCalls() != calls {
		t.Fatalf("evaluation continued after stop: before=%d after=%d", calls, store.evaluateCalls())
	}
}

func TestPostgresLoadAlertEvaluationsUsesLatestSettingsAndSamples(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	store := NewPostgresAlertStore(pool)
	now := alertPostgresClock(t, pool)
	window := now.Add(-15 * time.Minute)
	samples := []Sample{
		alertSample(SampleSourceHost, SampleMetricFilesystemUsedPercent, SampleScopeRoot, 80, now, nil),
		alertSample(SampleSourceHost, SampleMetricFilesystemUsedPercent, SampleScopeBackup, 81, now, nil),
		alertSample(SampleSourceApp, SampleMetricBackupAgeSeconds, SampleScopeLocal, 26*60*60, now, nil),
		alertSample(SampleSourceApp, SampleMetricBackupRemoteUp, SampleScopeRemote, 0, now, nil),
		alertSample(SampleSourceApp, SampleMetricAIRequestsTotal, SampleScopeSucceeded, 75, now, &window),
		alertSample(SampleSourceApp, SampleMetricAIRequestsTotal, SampleScopeFailed, 25, now, &window),
		alertSample(SampleSourceWorker, SampleMetricQueueItems, SampleScopeProcessing, 21, now, nil),
		alertSample(SampleSourceWorker, SampleMetricQueueFailuresTotal, SampleScopeProcessing, 5, now, &window),
		alertSample(SampleSourceApp, SampleMetricSecurityEventsTotal, SampleScopeLoginFailure, 20, now, &window),
		alertSample(SampleSourceApp, SampleMetricSecurityEventsTotal, SampleScopeAuthorizationDenial, 50, now, &window),
	}
	if err := NewPostgresSampleStore(pool).InsertSamples(ctx, now, samples); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE system_settings
SET disk_warning_percent=80,disk_critical_percent=95,
    version=version+1`); err != nil {
		t.Fatal(err)
	}
	evaluations, err := store.LoadAlertEvaluations(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != 9 {
		t.Fatalf("evaluations=%d", len(evaluations))
	}
	var root, remote Evaluation
	for _, evaluation := range evaluations {
		switch evaluation.Rule.DedupeKey {
		case "filesystem_root_usage":
			root = evaluation
		case "backup_remote_replication":
			remote = evaluation
		}
	}
	if root.Rule.Warning != 80 || root.Rule.Critical != 95 ||
		!root.Available || root.Value != 80 {
		t.Fatalf("root=%+v", root)
	}
	if !remote.Available || remote.Value != 0 {
		t.Fatalf("remote=%+v", remote)
	}
}

type alertRunnerLeaseStub struct {
	releases int
	err      error
}

func (lease *alertRunnerLeaseStub) Release(context.Context) error {
	lease.releases++
	return lease.err
}

type alertRunnerStoreStub struct {
	mu           sync.Mutex
	lease        AlertLease
	leaseFactory func() AlertLease
	acquired     bool
	acquireErr   error
	evaluations  []Evaluation
	loadErr      error
	evaluateErr  error
	onEvaluate   func()
	acquireCalls int
	loadCalls    int
	evaluated    []Evaluation
}

func (store *alertRunnerStoreStub) TryAcquireAlertRunnerLease(
	context.Context,
) (AlertLease, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.acquireCalls++
	if store.acquireErr != nil {
		return nil, false, store.acquireErr
	}
	lease := store.lease
	if store.leaseFactory != nil {
		lease = store.leaseFactory()
	}
	return lease, store.acquired, nil
}

func (store *alertRunnerStoreStub) LoadAlertEvaluations(
	context.Context,
	time.Time,
) ([]Evaluation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.loadCalls++
	return append([]Evaluation(nil), store.evaluations...), store.loadErr
}

func (store *alertRunnerStoreStub) EvaluateAlert(
	_ context.Context,
	evaluation Evaluation,
) (AlertTransition, error) {
	store.mu.Lock()
	store.evaluated = append(store.evaluated, evaluation)
	hook := store.onEvaluate
	err := store.evaluateErr
	store.mu.Unlock()
	if hook != nil {
		hook()
	}
	return AlertTransition{Kind: AlertTransitionUpdated}, err
}

func (store *alertRunnerStoreStub) evaluateCalls() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.evaluated)
}

func alertRunnerEvaluation(dedupeKey string, observedAt time.Time) Evaluation {
	rule := Rule{
		DedupeKey: dedupeKey, Category: "storage",
		Summary: "Root filesystem usage is high",
		Warning: 75, Critical: 90, Direction: DirectionAbove, MinimumSamples: 1,
	}
	if dedupeKey == "processing_queue_depth" {
		rule.Category = "processing"
		rule.Summary = "Processing queue depth is high"
		rule.Warning = 20
		rule.Critical = 100
	}
	return Evaluation{
		Rule: rule, Value: rule.Warning,
		ObservedAt: observedAt, Available: true, SampleCount: 1,
	}
}
