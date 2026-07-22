package notifications

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

type Runner struct {
	Store           OutboxStore
	Owner           string
	PollInterval    time.Duration
	BatchTimeout    time.Duration
	ShutdownTimeout time.Duration
	LogCategory     func(string)
}

func (r Runner) ProcessBatch(ctx context.Context) error {
	if r.Store == nil || r.Owner == "" {
		return ErrInvalidInput
	}
	events, err := r.Store.Claim(ctx, r.Owner)
	if err != nil {
		return err
	}
	for _, event := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = r.Store.DeliverLessonPublication(ctx, event, r.Owner)
		if err == nil {
			continue
		}
		category := outboxErrorCategory(err)
		permanent := errors.Is(err, ErrPermanentOutbox)
		if errors.Is(err, ErrLeaseLost) {
			category = "lease_lost"
			permanent = false
		}
		if failErr := r.Store.Fail(ctx, event.ID, r.Owner, category, permanent); failErr != nil && !errors.Is(failErr, ErrLeaseLost) {
			return failErr
		}
		r.log(category)
	}
	return nil
}
func (r Runner) log(category string) {
	if r.LogCategory != nil {
		r.LogCategory(category)
	} else {
		log.Printf("outbox_delivery category=%s", category)
	}
}

func StartOutboxRunner(r Runner) func() {
	if r.PollInterval <= 0 {
		r.PollInterval = time.Second
	}
	if r.BatchTimeout <= 0 {
		r.BatchTimeout = 10 * time.Second
	}
	if r.ShutdownTimeout <= 0 {
		r.ShutdownTimeout = 2 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				batchCtx, batchCancel := context.WithTimeout(ctx, r.BatchTimeout)
				err := r.ProcessBatch(batchCtx)
				batchCancel()
				if err != nil && !errors.Is(err, context.Canceled) {
					r.log("batch_failed")
				}
				timer.Reset(r.PollInterval)
			}
		}
	}()
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			cancel()
			timer := time.NewTimer(r.ShutdownTimeout)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				r.log("shutdown_timeout")
			}
		})
	}
}
