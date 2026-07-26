package aiqa

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type runnerMemoryStore struct {
	mu        sync.Mutex
	queued    []LeasedRun
	events    []RunEvent
	completed []Completion
	failed    []Failure
	leased    atomic.Int32
	reconcile atomic.Int32
	appendErr error
	heartErr  error
	heartAt   time.Time
}

func (s *runnerMemoryStore) LeaseNext(_ context.Context, owner string, _ time.Time, _ time.Duration) (LeasedRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queued) == 0 {
		return LeasedRun{}, ErrNoRunnableRun
	}
	run := s.queued[0]
	s.queued = s.queued[1:]
	run.LeaseOwner = owner
	s.leased.Add(1)
	return run, nil
}
func (s *runnerMemoryStore) Heartbeat(_ context.Context, _ uuid.UUID, _ string, leaseUntil time.Time) error {
	s.mu.Lock()
	s.heartAt = leaseUntil
	s.mu.Unlock()
	return s.heartErr
}
func (s *runnerMemoryStore) AppendEvents(_ context.Context, _ uuid.UUID, _ string, events []RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	s.events = append(s.events, events...)
	return nil
}
func (s *runnerMemoryStore) Complete(_ context.Context, _ LeasedRun, completion Completion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, completion)
	return nil
}
func (s *runnerMemoryStore) Fail(_ context.Context, _ LeasedRun, failure Failure) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = append(s.failed, failure)
	return nil
}
func (s *runnerMemoryStore) ReconcileExpired(context.Context, time.Time, int) error {
	s.reconcile.Add(1)
	return nil
}

type runnerGatewayFunc func(context.Context, RuntimeProviderConfig, GatewayRequest, func(GatewayEvent) error) error

func (f runnerGatewayFunc) Stream(ctx context.Context, cfg RuntimeProviderConfig, req GatewayRequest, cb func(GatewayEvent) error) error {
	return f(ctx, cfg, req, cb)
}

func TestRunnerCompletesWithMonotonicEventsAndUsage(t *testing.T) {
	store := &runnerMemoryStore{queued: []LeasedRun{{Run: Run{ID: uuid.New()}}}}
	gateway := runnerGatewayFunc(func(_ context.Context, _ RuntimeProviderConfig, _ GatewayRequest, cb func(GatewayEvent) error) error {
		if err := cb(GatewayEvent{Kind: "delta", Delta: "answer "}); err != nil {
			return err
		}
		if err := cb(GatewayEvent{Kind: "delta", Delta: "text"}); err != nil {
			return err
		}
		return cb(GatewayEvent{Kind: "usage", InputTokens: 10, OutputTokens: 4, FinishReason: "stop"})
	})
	stop := StartRunner(Runner{
		Store: store, Gateway: gateway, Owner: "test", GlobalConcurrency: 1,
		PollInterval: time.Millisecond, LeaseDuration: 90 * time.Millisecond,
		FlushInterval: time.Millisecond, FlushBytes: 4096,
	})
	waitRunner(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.completed) == 1
	})
	stop()
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.completed[0]; got.Answer != "answer text" || got.InputTokens != 10 || got.OutputTokens != 4 ||
		got.UsageSource != "upstream" || got.FinishReason != "stop" || got.TotalMS < 0 || got.FirstByteMS < 0 {
		t.Fatalf("completion=%+v", got)
	}
	if len(store.events) != 3 {
		t.Fatalf("events=%+v", store.events)
	}
	for i, event := range store.events {
		if event.Sequence != int64(i+1) {
			t.Fatalf("event[%d].sequence=%d", i, event.Sequence)
		}
	}
}

func TestRunnerUsesExactlyBoundedWorkerPool(t *testing.T) {
	var current, maximum atomic.Int32
	release := make(chan struct{})
	store := &runnerMemoryStore{}
	for i := 0; i < 8; i++ {
		store.queued = append(store.queued, LeasedRun{Run: Run{ID: uuid.New()}})
	}
	gateway := runnerGatewayFunc(func(ctx context.Context, _ RuntimeProviderConfig, _ GatewayRequest, _ func(GatewayEvent) error) error {
		active := current.Add(1)
		defer current.Add(-1)
		for {
			old := maximum.Load()
			if active <= old || maximum.CompareAndSwap(old, active) {
				break
			}
		}
		select {
		case <-release:
			return errors.New("done")
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	stop := StartRunner(Runner{
		Store: store, Gateway: gateway, Owner: "test", GlobalConcurrency: 2,
		PollInterval: time.Millisecond, LeaseDuration: 90 * time.Millisecond,
		FlushInterval: time.Millisecond, FlushBytes: 4096,
	})
	waitRunner(t, func() bool { return maximum.Load() == 2 })
	if got := store.leased.Load(); got != 2 {
		t.Fatalf("leased=%d before workers free", got)
	}
	close(release)
	waitRunner(t, func() bool { return store.leased.Load() >= 8 })
	stop()
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrency=%d", got)
	}
}

func TestRunnerFlushesByTimeAndCancelsPromptly(t *testing.T) {
	store := &runnerMemoryStore{queued: []LeasedRun{{Run: Run{ID: uuid.New()}}}}
	flushed := make(chan struct{})
	gateway := runnerGatewayFunc(func(ctx context.Context, _ RuntimeProviderConfig, _ GatewayRequest, cb func(GatewayEvent) error) error {
		if err := cb(GatewayEvent{Kind: "delta", Delta: "x"}); err != nil {
			return err
		}
		select {
		case <-flushed:
		case <-ctx.Done():
			return ctx.Err()
		}
		<-ctx.Done()
		return ctx.Err()
	})
	originalAppend := store.AppendEvents
	_ = originalAppend
	probe := &appendProbeStore{runnerMemoryStore: store, flushed: flushed}
	stop := StartRunner(Runner{
		Store: probe, Gateway: gateway, Owner: "test", GlobalConcurrency: 1,
		PollInterval: time.Millisecond, LeaseDuration: 90 * time.Millisecond,
		FlushInterval: 10 * time.Millisecond, FlushBytes: 4096,
	})
	select {
	case <-flushed:
	case <-time.After(time.Second):
		t.Fatal("timed flush not observed")
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner shutdown was not prompt")
	}
}

func TestRunnerMapsCallbackFailureCancellationAndLeaseLoss(t *testing.T) {
	tests := []struct {
		name      string
		appendErr error
		heartErr  error
		want      string
		status    RunStatus
	}{
		{name: "callback store", appendErr: errors.New("database unavailable"), want: "callback_store_failure", status: RunFailed},
		{name: "cancel request", heartErr: ErrCancelRequested, want: "cancelled", status: RunCancelled},
		{name: "lease loss", heartErr: ErrRunnerLeaseLost, want: "lease_lost", status: RunFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &runnerMemoryStore{
				queued:    []LeasedRun{{Run: Run{ID: uuid.New()}}},
				appendErr: test.appendErr, heartErr: test.heartErr,
			}
			gateway := runnerGatewayFunc(func(ctx context.Context, _ RuntimeProviderConfig, _ GatewayRequest, cb func(GatewayEvent) error) error {
				if test.appendErr != nil {
					return cb(GatewayEvent{Kind: "delta", Delta: "x"})
				}
				<-ctx.Done()
				return ctx.Err()
			})
			stop := StartRunner(Runner{
				Store: store, Gateway: gateway, Owner: "test", GlobalConcurrency: 1,
				PollInterval: time.Millisecond, LeaseDuration: 60 * time.Millisecond,
				FlushInterval: time.Hour, FlushBytes: 1,
			})
			waitRunner(t, func() bool {
				store.mu.Lock()
				defer store.mu.Unlock()
				return len(store.failed) == 1
			})
			stop()
			store.mu.Lock()
			defer store.mu.Unlock()
			if store.failed[0].ErrorCode != test.want || store.failed[0].Status != test.status {
				t.Fatalf("failure=%+v", store.failed[0])
			}
			if test.heartErr != nil && !store.heartAt.After(time.Now()) {
				t.Fatalf("heartbeat did not extend lease: %s", store.heartAt)
			}
		})
	}
}

type appendProbeStore struct {
	*runnerMemoryStore
	flushed chan struct{}
	once    sync.Once
}

func (s *appendProbeStore) AppendEvents(ctx context.Context, id uuid.UUID, owner string, events []RunEvent) error {
	err := s.runnerMemoryStore.AppendEvents(ctx, id, owner, events)
	s.once.Do(func() { close(s.flushed) })
	return err
}

func waitRunner(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runner condition timed out")
}
