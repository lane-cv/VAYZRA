package operations

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRetentionSchedulerReadsLatestSampleRetentionForEveryRun(t *testing.T) {
	settings := &retentionSettingsStoreStub{retentionDays: []int{7, 21}}
	runner := &retentionRunnerStub{}
	now := time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)
	scheduler := RetentionScheduler{
		Settings: settings,
		Runner:   runner,
		Clock:    func() time.Time { return now },
	}

	for range 2 {
		if err := scheduler.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	if settings.calls != 2 {
		t.Fatalf("settings calls=%d want=2", settings.calls)
	}
	if got := runner.retentionDays(); len(got) != 2 ||
		got[0] != 7 || got[1] != 21 {
		t.Fatalf("retention days=%v", got)
	}
	if got := runner.runTimes(); len(got) != 2 ||
		!got[0].Equal(now) || !got[1].Equal(now) {
		t.Fatalf("run times=%v", got)
	}
}

func TestRetentionSchedulerBoundsEachRun(t *testing.T) {
	deadlineSeen := make(chan time.Time, 1)
	runner := &retentionRunnerStub{run: func(
		ctx context.Context,
		_ time.Time,
		_ int,
	) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("run context has no deadline")
		}
		deadlineSeen <- deadline
		return nil
	}}
	scheduler := RetentionScheduler{
		Settings:   &retentionSettingsStoreStub{retentionDays: []int{7}},
		Runner:     runner,
		Clock:      time.Now,
		RunTimeout: 5 * time.Second,
	}

	if err := scheduler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	deadline := <-deadlineSeen
	if remaining := time.Until(deadline); remaining <= 0 ||
		remaining > 5*time.Second {
		t.Fatalf("deadline remaining=%s", remaining)
	}
}

func TestStartRetentionSchedulerRunsImmediatelyUsesDailyDefaultAndStopsIdempotently(
	t *testing.T,
) {
	started := make(chan struct{}, 1)
	waiting := make(chan time.Duration, 1)
	runner := &retentionRunnerStub{run: func(
		context.Context,
		time.Time,
		int,
	) error {
		started <- struct{}{}
		return nil
	}}
	scheduler := RetentionScheduler{
		Settings: &retentionSettingsStoreStub{retentionDays: []int{7}},
		Runner:   runner,
		Clock:    time.Now,
		wait: func(ctx context.Context, interval time.Duration) bool {
			waiting <- interval
			<-ctx.Done()
			return false
		},
	}

	stop, err := StartRetentionScheduler(scheduler)
	if err != nil || stop == nil {
		t.Fatalf("stop nil=%t err=%v", stop == nil, err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not run immediately")
	}
	select {
	case interval := <-waiting:
		if interval != DefaultRetentionInterval {
			t.Fatalf("interval=%s want=%s", interval, DefaultRetentionInterval)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not enter daily wait")
	}

	stopped := make(chan struct{})
	go func() {
		stop()
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("idempotent stop did not wait for scheduler exit")
	}
}

func TestRetentionSchedulerNeverOverlapsRuns(t *testing.T) {
	var mu sync.Mutex
	active := 0
	maxActive := 0
	runs := 0
	runner := &retentionRunnerStub{run: func(
		context.Context,
		time.Time,
		int,
	) error {
		mu.Lock()
		active++
		runs++
		if active > maxActive {
			maxActive = active
		}
		active--
		mu.Unlock()
		return nil
	}}
	waits := 0
	secondWait := make(chan struct{})
	exited := make(chan struct{})
	scheduler := RetentionScheduler{
		Settings: &retentionSettingsStoreStub{retentionDays: []int{7, 7}},
		Runner:   runner,
		Clock:    time.Now,
		wait: func(context.Context, time.Duration) bool {
			waits++
			if waits == 1 {
				return true
			}
			close(secondWait)
			return false
		},
	}

	stop, err := StartRetentionScheduler(scheduler)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondWait:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not complete two serial runs")
	}
	go func() {
		stop()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not exit")
	}
	mu.Lock()
	defer mu.Unlock()
	if runs != 2 || maxActive != 1 {
		t.Fatalf("runs=%d max_active=%d", runs, maxActive)
	}
}

func TestRetentionSchedulerLogsOnlyFixedFailureCategories(t *testing.T) {
	secret := "postgres://private-user:private-password@private-host/database"
	var categories []string
	logCategory := func(category string) {
		categories = append(categories, category)
	}
	settingsFailure := RetentionScheduler{
		Settings:    &retentionSettingsStoreStub{err: errors.New(secret)},
		Runner:      &retentionRunnerStub{},
		Clock:       time.Now,
		LogCategory: logCategory,
	}
	if err := settingsFailure.RunOnce(context.Background()); err == nil {
		t.Fatal("settings failure was not returned")
	}
	retentionFailure := RetentionScheduler{
		Settings: &retentionSettingsStoreStub{retentionDays: []int{7}},
		Runner: &retentionRunnerStub{run: func(
			context.Context,
			time.Time,
			int,
		) error {
			return errors.New(secret)
		}},
		Clock:       time.Now,
		LogCategory: logCategory,
	}
	if err := retentionFailure.RunOnce(context.Background()); err == nil {
		t.Fatal("retention failure was not returned")
	}

	if got := categories; len(got) != 2 ||
		got[0] != "settings_read_failed" ||
		got[1] != "cleanup_failed" {
		t.Fatalf("categories=%v", got)
	}
	for _, category := range categories {
		if category == secret {
			t.Fatal("raw error detail reached logger")
		}
	}
}

func TestStartRetentionSchedulerRejectsInvalidDependencies(t *testing.T) {
	for name, scheduler := range map[string]RetentionScheduler{
		"settings": {Runner: &retentionRunnerStub{}, Clock: time.Now},
		"runner": {
			Settings: &retentionSettingsStoreStub{retentionDays: []int{7}},
			Clock:    time.Now,
		},
		"clock": {
			Settings: &retentionSettingsStoreStub{retentionDays: []int{7}},
			Runner:   &retentionRunnerStub{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			stop, err := StartRetentionScheduler(scheduler)
			if stop != nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("stop nil=%t err=%v", stop == nil, err)
			}
		})
	}
}

type retentionSettingsStoreStub struct {
	mu            sync.Mutex
	retentionDays []int
	calls         int
	err           error
}

func (store *retentionSettingsStoreStub) GetSettings(
	context.Context,
) (Settings, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls++
	if store.err != nil {
		return Settings{}, store.err
	}
	index := store.calls - 1
	if index >= len(store.retentionDays) {
		index = len(store.retentionDays) - 1
	}
	return Settings{
		OperationalSampleRetentionDays: store.retentionDays[index],
	}, nil
}

func (*retentionSettingsStoreStub) UpdateSettings(
	context.Context,
	Principal,
	Settings,
) (Settings, error) {
	return Settings{}, nil
}

type retentionRunnerStub struct {
	mu    sync.Mutex
	days  []int
	times []time.Time
	run   func(context.Context, time.Time, int) error
}

func (runner *retentionRunnerStub) RunOnce(
	ctx context.Context,
	now time.Time,
	retentionDays int,
) (RetentionResult, error) {
	runner.mu.Lock()
	runner.days = append(runner.days, retentionDays)
	runner.times = append(runner.times, now)
	runner.mu.Unlock()
	if runner.run != nil {
		return RetentionResult{}, runner.run(ctx, now, retentionDays)
	}
	return RetentionResult{}, nil
}

func (runner *retentionRunnerStub) retentionDays() []int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]int(nil), runner.days...)
}

func (runner *retentionRunnerStub) runTimes() []time.Time {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]time.Time(nil), runner.times...)
}
