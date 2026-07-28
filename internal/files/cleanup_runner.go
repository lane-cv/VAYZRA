package files

import (
	"context"
	"log"
	"sync"
	"time"

	"happylearn.local/app/internal/operations"
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
	gate     operations.WriteGate
	log      func(string)
}

func newCleanupRunner(
	cleaner ExpiredUploadCleaner,
	gate operations.WriteGate,
	logCategory func(string),
	interval, timeout time.Duration,
	limit int,
) *cleanupRunner {
	return &cleanupRunner{
		cleaner: cleaner, gate: gate, log: logCategory,
		interval: interval, timeout: timeout, limit: limit,
	}
}

func (r *cleanupRunner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

func (r *cleanupRunner) runOnce(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if r.gate == nil {
		r.logCategory("operational_gate_failed")
		return
	}
	release, err := r.gate.AcquireShared(runCtx)
	if err != nil || release == nil {
		if release != nil {
			release()
		}
		r.logCategory("operational_gate_failed")
		return
	}
	defer release()
	_ = r.cleaner.CleanupExpired(
		withCleanupAdmission(runCtx),
		r.limit,
	)
}

func (r *cleanupRunner) logCategory(category string) {
	if r.log != nil {
		r.log(category)
		return
	}
	log.Printf("upload_cleanup category=%s", category)
}

func StartCleanupRunner(cleaner ExpiredUploadCleaner, gate operations.WriteGate) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		newCleanupRunner(
			cleaner, gate, nil,
			cleanupRunnerInterval, cleanupRunTimeout, cleanupBatchLimit,
		).Run(ctx)
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
