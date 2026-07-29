package operations

import (
	"context"
	"errors"
	"strings"
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

func TestAlertRunnerContinuesAfterRuleErrorsAndReturnsAllFailures(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	firstErr := errors.New("first evaluation failed")
	thirdErr := errors.New("third evaluation failed")
	store := &alertRunnerStoreStub{
		lease:    &alertRunnerLeaseStub{},
		acquired: true,
		evaluations: []Evaluation{
			alertRunnerEvaluation("filesystem_root_usage", now),
			alertRunnerEvaluation("processing_queue_depth", now),
			alertRunnerEvaluation("filesystem_backup_usage", now),
		},
		evaluateErrors: map[string]error{
			"filesystem_root_usage":   firstErr,
			"filesystem_backup_usage": thirdErr,
		},
	}
	runner := AlertRunner{
		Store: store, Clock: func() time.Time { return now },
		EvaluationTimeout: time.Second,
	}
	err := runner.RunOnce(context.Background())
	if !errors.Is(err, firstErr) || !errors.Is(err, thirdErr) {
		t.Fatalf("aggregate error=%v", err)
	}
	if got := store.evaluateCalls(); got != 3 {
		t.Fatalf("evaluate calls=%d want=3", got)
	}
}

func TestAlertRunnerEvaluatesUsableRulesWhenCollectionIsPartiallyUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	store := &alertRunnerStoreStub{
		lease:    &alertRunnerLeaseStub{},
		acquired: true,
		evaluations: []Evaluation{
			alertRunnerEvaluation("filesystem_root_usage", now),
			alertRunnerEvaluation("processing_queue_depth", now),
		},
		loadErr: ErrAlertCollectorUnavailable,
	}
	err := (AlertRunner{
		Store: store, Clock: func() time.Time { return now },
		EvaluationTimeout: time.Second,
	}).RunOnce(context.Background())
	if !errors.Is(err, ErrAlertCollectorUnavailable) {
		t.Fatalf("error=%v", err)
	}
	if got := store.evaluateCalls(); got != 2 {
		t.Fatalf("evaluate calls=%d want=2", got)
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
	samples := []Sample{
		alertSample(SampleSourceHost, SampleMetricFilesystemUsedPercent, SampleScopeRoot, 80, now, nil),
		alertSample(SampleSourceHost, SampleMetricFilesystemUsedPercent, SampleScopeBackup, 81, now, nil),
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
	if len(evaluations) != 16 {
		t.Fatalf("evaluations=%d", len(evaluations))
	}
	var root Evaluation
	for _, evaluation := range evaluations {
		switch evaluation.Rule.DedupeKey {
		case "filesystem_root_usage":
			root = evaluation
		}
	}
	if root.Rule.Warning != 80 || root.Rule.Critical != 95 ||
		!root.Available || root.Value != 80 {
		t.Fatalf("root=%+v", root)
	}
}

func TestPostgresAlertRunnerEmptyDatabaseNeverOpensBusinessThresholdAlerts(t *testing.T) {
	ctx := context.Background()
	pool := migratedAlertPool(t)
	if _, err := pool.Exec(ctx, `
TRUNCATE operational_samples;
TRUNCATE operational_alerts CASCADE;
TRUNCATE backup_runs CASCADE;
TRUNCATE login_events`); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresAlertStore(pool)
	now := alertPostgresClock(t, pool)
	clock := now
	runner := AlertRunner{
		Store: store, Clock: func() time.Time { return clock },
		EvaluationTimeout: 10 * time.Second,
	}
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(time.Minute)
	if err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListAlerts(ctx, AlertFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 0 {
		t.Fatal("missing explicit dependency-unavailable alerts")
	}
	for _, alert := range page.Items {
		if !strings.HasSuffix(
			alert.DedupeKey,
			"_dependency_unavailable",
		) ||
			!strings.Contains(alert.Summary, "unavailable") ||
			alert.CurrentValue != 1 ||
			alert.ThresholdValue != 1 {
			t.Fatalf("non-dependency or contradictory alert=%+v", alert)
		}
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
	mu             sync.Mutex
	lease          AlertLease
	leaseFactory   func() AlertLease
	acquired       bool
	acquireErr     error
	evaluations    []Evaluation
	loadErr        error
	evaluateErr    error
	evaluateErrors map[string]error
	onEvaluate     func()
	acquireCalls   int
	loadCalls      int
	evaluated      []Evaluation
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
	if evaluationErr := store.evaluateErrors[evaluation.Rule.DedupeKey]; evaluationErr != nil {
		err = evaluationErr
	}
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
