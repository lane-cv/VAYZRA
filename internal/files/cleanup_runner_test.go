package files

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"happylearn.local/app/internal/operations"
)

func TestCleanupRunnerIsBoundedNonOverlappingAndStops(t *testing.T) {
	cleaner := &blockingCleaner{calls: make(chan context.Context, 2), release: make(chan struct{})}
	runner := newCleanupRunner(
		cleaner,
		&cleanupWriteGate{},
		nil,
		5*time.Millisecond,
		30*time.Millisecond,
		100,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	select {
	case callCtx := <-cleaner.calls:
		if _, ok := callCtx.Deadline(); !ok {
			t.Fatal("cleanup call has no timeout")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("runner did not invoke cleanup")
	}
	time.Sleep(20 * time.Millisecond)
	cleaner.mu.Lock()
	calls := cleaner.count
	cleaner.mu.Unlock()
	if calls != 1 {
		t.Fatalf("overlapping cleanup calls=%d", calls)
	}
	close(cleaner.release)
	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("runner did not stop")
	}
}

type cleanupClaimGate struct {
	err   error
	calls int
}

func (g *cleanupClaimGate) AcquireShared(context.Context) (func(), error) {
	g.calls++
	if g.err != nil {
		return nil, g.err
	}
	return nil, nil
}

func TestOperationalGateBlocksCleanupClaimsAndLogsSafeCategory(t *testing.T) {
	for _, gate := range []*cleanupClaimGate{
		{},
		{err: context.DeadlineExceeded},
	} {
		cleaner := &countingCleaner{}
		var categories []string
		runner := newCleanupRunner(cleaner, gate, func(category string) {
			categories = append(categories, category)
		}, time.Hour, time.Second, 100)
		runner.runOnce(context.Background())
		if cleaner.calls != 0 || gate.calls != 1 {
			t.Fatalf("cleanup_calls=%d gate_calls=%d", cleaner.calls, gate.calls)
		}
		want := "operational_gate_failed"
		if strings.Join(categories, ",") != want {
			t.Fatalf("log=%q want=%q", strings.Join(categories, ","), want)
		}
	}
}

type cleanupWriteGate struct {
	mu             sync.Mutex
	acquireErr     error
	missingRelease bool
	acquireCalls   int
	releaseCalls   int
}

func (g *cleanupWriteGate) AcquireShared(context.Context) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.acquireCalls++
	if g.acquireErr != nil {
		return nil, g.acquireErr
	}
	if g.missingRelease {
		return nil, nil
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.releaseCalls++
	}, nil
}

func (g *cleanupWriteGate) snapshot() (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.acquireCalls, g.releaseCalls
}

func TestCleanupRunnerHoldsWriteGateAcrossEntireCleanupCall(t *testing.T) {
	gate := &cleanupWriteGate{}
	cleaner := &blockingCleaner{
		calls:   make(chan context.Context, 1),
		release: make(chan struct{}),
	}
	runner := newCleanupRunner(
		cleaner,
		gate,
		nil,
		time.Hour,
		time.Second,
		100,
	)
	done := make(chan struct{})
	go func() {
		runner.runOnce(context.Background())
		close(done)
	}()
	select {
	case <-cleaner.calls:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start")
	}
	acquires, releases := gate.snapshot()
	close(cleaner.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not finish")
	}
	finalAcquires, finalReleases := gate.snapshot()
	if acquires != 1 || releases != 0 ||
		finalAcquires != 1 || finalReleases != 1 {
		t.Fatalf(
			"during=%d/%d final=%d/%d",
			acquires,
			releases,
			finalAcquires,
			finalReleases,
		)
	}
}

func TestCleanupRunnerFailsClosedWhenWriteGateIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate *cleanupWriteGate
	}{
		{name: "missing gate"},
		{
			name: "acquire error",
			gate: &cleanupWriteGate{acquireErr: errors.New("secret gate detail")},
		},
		{
			name: "missing release",
			gate: &cleanupWriteGate{missingRelease: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleaner := &countingCleaner{}
			var categories []string
			var gate operations.WriteGate
			if tc.gate != nil {
				gate = tc.gate
			}
			runner := newCleanupRunner(
				cleaner,
				gate,
				func(category string) {
					categories = append(categories, category)
				},
				time.Hour,
				time.Second,
				100,
			)
			runner.runOnce(context.Background())
			if cleaner.calls != 0 {
				t.Fatalf("cleanup_calls=%d", cleaner.calls)
			}
			if got := strings.Join(categories, ","); got != "operational_gate_failed" {
				t.Fatalf("log=%q", got)
			}
		})
	}
}

type countingCleaner struct{ calls int }

func (c *countingCleaner) CleanupExpired(context.Context, int) error {
	c.calls++
	return nil
}

type blockingCleaner struct {
	mu      sync.Mutex
	count   int
	calls   chan context.Context
	release chan struct{}
}

func (c *blockingCleaner) CleanupExpired(ctx context.Context, limit int) error {
	if limit != 100 {
		return ErrInvalid
	}
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	c.calls <- ctx
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
