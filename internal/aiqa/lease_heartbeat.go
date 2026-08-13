package aiqa

import (
	"context"
	"time"
)

// heartbeatOnTicks renews a run lease until either the caller stops it, its
// context ends, or persistence rejects a heartbeat.
func heartbeatOnTicks(
	ctx context.Context,
	stop <-chan struct{},
	ticks <-chan time.Time,
	now func() time.Time,
	leaseDuration time.Duration,
	heartbeat func(time.Time) error,
) error {
	for {
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
			if err := heartbeat(now().Add(leaseDuration).UTC()); err != nil {
				return err
			}
		}
	}
}
