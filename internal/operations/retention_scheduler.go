package operations

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	DefaultRetentionInterval   = 24 * time.Hour
	DefaultRetentionRunTimeout = 2 * time.Minute
)

type retentionWait func(context.Context, time.Duration) bool

type RetentionScheduler struct {
	Settings    SettingsStore
	Runner      RetentionRunner
	Clock       func() time.Time
	Interval    time.Duration
	RunTimeout  time.Duration
	LogCategory func(string)

	wait retentionWait
}

func (scheduler RetentionScheduler) RunOnce(ctx context.Context) error {
	scheduler, err := scheduler.normalized()
	if err != nil || ctx == nil {
		return ErrInvalid
	}
	return scheduler.runOnce(ctx)
}

func StartRetentionScheduler(
	scheduler RetentionScheduler,
) (func(), error) {
	scheduler, err := scheduler.normalized()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if ctx.Err() != nil {
				return
			}
			_ = scheduler.runOnce(ctx)
			if !scheduler.wait(ctx, scheduler.Interval) {
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}, nil
}

func (scheduler RetentionScheduler) normalized() (
	RetentionScheduler,
	error,
) {
	if scheduler.Settings == nil ||
		scheduler.Runner == nil ||
		scheduler.Clock == nil {
		return RetentionScheduler{}, ErrInvalid
	}
	if scheduler.Interval <= 0 {
		scheduler.Interval = DefaultRetentionInterval
	}
	if scheduler.RunTimeout <= 0 {
		scheduler.RunTimeout = DefaultRetentionRunTimeout
	}
	if scheduler.wait == nil {
		scheduler.wait = waitForRetentionInterval
	}
	return scheduler, nil
}

func (scheduler RetentionScheduler) runOnce(ctx context.Context) error {
	runCtx, cancel := context.WithTimeout(ctx, scheduler.RunTimeout)
	defer cancel()
	settings, err := scheduler.Settings.GetSettings(runCtx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			scheduler.logCategory("settings_read_failed")
		}
		return err
	}
	_, err = scheduler.Runner.RunOnce(
		runCtx,
		scheduler.Clock().UTC(),
		settings.OperationalSampleRetentionDays,
	)
	if err != nil && !errors.Is(err, context.Canceled) {
		scheduler.logCategory("cleanup_failed")
	}
	return err
}

func (scheduler RetentionScheduler) logCategory(category string) {
	if scheduler.LogCategory != nil {
		scheduler.LogCategory(category)
	}
}

func waitForRetentionInterval(
	ctx context.Context,
	interval time.Duration,
) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
