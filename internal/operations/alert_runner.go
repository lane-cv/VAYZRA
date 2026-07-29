package operations

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

const (
	DefaultAlertRunnerInterval = time.Minute
	defaultAlertRunTimeout     = 30 * time.Second
	defaultAlertReleaseTimeout = 2 * time.Second
)

type AlertLease interface {
	Release(context.Context) error
}

type AlertRunnerStore interface {
	TryAcquireAlertRunnerLease(context.Context) (AlertLease, bool, error)
	LoadAlertEvaluations(context.Context, time.Time) ([]Evaluation, error)
	EvaluateAlert(context.Context, Evaluation) (AlertTransition, error)
}

type AlertRunner struct {
	Store             AlertRunnerStore
	Clock             func() time.Time
	PollInterval      time.Duration
	EvaluationTimeout time.Duration
	LogCategory       func(string)
}

func (runner AlertRunner) RunOnce(ctx context.Context) (runErr error) {
	if ctx == nil || runner.Store == nil || runner.Clock == nil {
		return ErrInvalid
	}
	timeout := runner.EvaluationTimeout
	if timeout <= 0 {
		timeout = defaultAlertRunTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lease, acquired, err := runner.Store.TryAcquireAlertRunnerLease(runCtx)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	if lease == nil {
		return ErrInvalid
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(
			context.Background(),
			defaultAlertReleaseTimeout,
		)
		defer releaseCancel()
		if err := lease.Release(releaseCtx); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	now := runner.Clock().UTC()
	if !validSampleTime(now) {
		return ErrInvalid
	}
	evaluations, err := runner.Store.LoadAlertEvaluations(runCtx, now)
	if err != nil && len(evaluations) == 0 {
		return err
	}
	evaluationErrors := make([]error, 0, 1)
	if err != nil {
		evaluationErrors = append(evaluationErrors, err)
	}
	for _, evaluation := range evaluations {
		if runCtx.Err() != nil {
			evaluationErrors = append(evaluationErrors, runCtx.Err())
			break
		}
		if _, err := runner.Store.EvaluateAlert(runCtx, evaluation); err != nil {
			evaluationErrors = append(evaluationErrors, err)
		}
	}
	return errors.Join(evaluationErrors...)
}

func StartAlertRunner(runner AlertRunner) func() {
	if runner.PollInterval <= 0 {
		runner.PollInterval = DefaultAlertRunnerInterval
	}
	if runner.EvaluationTimeout <= 0 {
		runner.EvaluationTimeout = defaultAlertRunTimeout
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
				err := runner.RunOnce(ctx)
				if err != nil && !errors.Is(err, context.Canceled) {
					runner.log("evaluation_failed")
				}
				timer.Reset(runner.PollInterval)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

func (runner AlertRunner) log(category string) {
	if runner.LogCategory != nil {
		runner.LogCategory(category)
		return
	}
	log.Printf("alert_evaluator category=%s", category)
}
