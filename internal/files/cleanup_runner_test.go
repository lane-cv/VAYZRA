package files

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestCleanupRunnerIsBoundedNonOverlappingAndStops(t *testing.T) {
	cleaner := &blockingCleaner{calls: make(chan context.Context, 2), release: make(chan struct{})}
	runner := newCleanupRunner(cleaner, 5*time.Millisecond, 30*time.Millisecond, 100)
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
