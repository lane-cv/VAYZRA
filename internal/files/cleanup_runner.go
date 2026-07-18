package files

import (
	"context"
	"sync"
	"time"
)

const (
	cleanupRunnerInterval = 15 * time.Minute
	cleanupRunTimeout     = 2 * time.Minute
	cleanupBatchLimit     = 100
)

type ExpiredUploadCleaner interface {
	CleanupExpired(context.Context, int) error
}

type cleanupRunner struct {
	cleaner  ExpiredUploadCleaner
	interval time.Duration
	timeout  time.Duration
	limit    int
}

func newCleanupRunner(cleaner ExpiredUploadCleaner, interval, timeout time.Duration, limit int) *cleanupRunner {
	return &cleanupRunner{cleaner: cleaner, interval: interval, timeout: timeout, limit: limit}
}

func (r *cleanupRunner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCtx, cancel := context.WithTimeout(ctx, r.timeout)
			_ = r.cleaner.CleanupExpired(runCtx, r.limit)
			cancel()
		}
	}
}

func StartCleanupRunner(cleaner ExpiredUploadCleaner) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		newCleanupRunner(cleaner, cleanupRunnerInterval, cleanupRunTimeout, cleanupBatchLimit).Run(ctx)
		close(done)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}
