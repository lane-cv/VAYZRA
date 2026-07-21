package processing

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultIdleWait          = 2 * time.Second
	DefaultLeaseDuration     = 30 * time.Second
	DefaultHeartbeatInterval = 10 * time.Second
	DefaultProcessDeadline   = 15 * time.Minute
	MaxAttempts              = 4
)

var retryDelays = [...]time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

type Worker struct {
	Store             Store
	Processor         Processor
	Owner             string
	Now               func() time.Time
	IdleWait          time.Duration
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	Deadlines         map[string]time.Duration
	Wake              <-chan struct{}
}

func (w *Worker) Run(ctx context.Context) error {
	if err := w.validate(); err != nil {
		return err
	}
	for {
		worked, err := w.RunOne(ctx)
		if err != nil && !errors.Is(err, ErrNoJob) && !errors.Is(err, ErrLeaseLost) {
			return err
		}
		if worked {
			continue
		}
		timer := time.NewTimer(w.idleWait())
		select {
		case <-ctx.Done():
			stopAndDrain(timer)
			return nil
		case <-w.Wake:
			stopAndDrain(timer)
		case <-timer.C:
		}
	}
}

func (w *Worker) RunOne(ctx context.Context) (bool, error) {
	if err := w.validate(); err != nil {
		return false, err
	}
	now := w.now()
	job, err := w.Store.LeaseNext(ctx, w.Owner, now, w.leaseDuration())
	if errors.Is(err, ErrNoJob) {
		return false, ErrNoJob
	}
	if err != nil && ctx.Err() != nil {
		return false, nil
	}
	if err != nil {
		return false, errors.New("lease processing job")
	}
	deadline := DefaultProcessDeadline
	if configured := w.Deadlines[job.Kind]; configured > 0 {
		deadline = configured
	}
	jobCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	type outcome struct {
		result Result
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, processErr := w.Processor.Process(jobCtx, job)
		finished <- outcome{result: result, err: processErr}
	}()
	ticker := time.NewTicker(w.heartbeatInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-finished
			return true, nil
		case <-ticker.C:
			if ctx.Err() != nil {
				cancel()
				<-finished
				return true, nil
			}
			if err := w.Store.Heartbeat(ctx, job.ID, w.Owner, w.now().Add(w.leaseDuration())); err != nil {
				cancel()
				<-finished
				return true, ErrLeaseLost
			}
		case got := <-finished:
			if ctx.Err() != nil {
				return true, nil
			}
			if got.err == nil {
				if err := w.Store.Complete(ctx, job, got.result); err != nil {
					if errors.Is(err, ErrLeaseLost) {
						return true, ErrLeaseLost
					}
					return true, errors.New("complete processing job")
				}
				return true, nil
			}
			failure := classifyFailure(got.err, job.Attempts, w.now())
			if err := w.Store.Fail(ctx, job, failure); err != nil {
				if errors.Is(err, ErrLeaseLost) {
					return true, ErrLeaseLost
				}
				return true, errors.New("fail processing job")
			}
			return true, nil
		}
	}
}

func classifyFailure(err error, attempts int, now time.Time) Failure {
	failure := Failure{Category: "processing_error", Permanent: attempts >= MaxAttempts}
	var processingErr *ProcessingError
	if errors.As(err, &processingErr) {
		failure.Category = processingErr.Category
		failure.Permanent = processingErr.Permanent || attempts >= MaxAttempts
		failure.Rejected = processingErr.Rejected
	}
	if !failure.Permanent && attempts > 0 && attempts <= len(retryDelays) {
		failure.RetryAt = now.Add(retryDelays[attempts-1])
	}
	return failure
}

func (w *Worker) validate() error {
	if w.Store == nil || w.Processor == nil || w.Owner == "" {
		return fmt.Errorf("invalid worker configuration")
	}
	return nil
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}
func (w *Worker) idleWait() time.Duration {
	if w.IdleWait > 0 && w.IdleWait <= DefaultIdleWait {
		return w.IdleWait
	}
	return DefaultIdleWait
}
func (w *Worker) leaseDuration() time.Duration {
	if w.LeaseDuration > 0 {
		return w.LeaseDuration
	}
	return DefaultLeaseDuration
}
func (w *Worker) heartbeatInterval() time.Duration {
	if w.HeartbeatInterval > 0 {
		return w.HeartbeatInterval
	}
	return DefaultHeartbeatInterval
}
func stopAndDrain(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
