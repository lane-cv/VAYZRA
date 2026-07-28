package files

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCleanupRunnerIsBoundedNonOverlappingAndStops(t *testing.T) {
	cleaner := &blockingCleaner{calls: make(chan context.Context, 2), release: make(chan struct{})}
	runner := newCleanupRunner(cleaner, nil, nil, 5*time.Millisecond, 30*time.Millisecond, 100)
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
	allowed bool
	err     error
	calls   int
}

func (g *cleanupClaimGate) ClaimsAllowed(context.Context) (bool, error) {
	g.calls++
	return g.allowed, g.err
}

func TestOperationalGateBlocksCleanupClaimsAndLogsSafeCategory(t *testing.T) {
	for _, gate := range []*cleanupClaimGate{
		{allowed: false},
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
		want := ""
		if gate.err != nil {
			want = "operational_gate_failed"
		}
		if strings.Join(categories, ",") != want {
			t.Fatalf("log=%q want=%q", strings.Join(categories, ","), want)
		}
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
