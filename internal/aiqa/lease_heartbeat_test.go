package aiqa

import (
	"context"
	"testing"
	"time"
)

func TestHeartbeatOnTicksRenewsFromProcessingTime(t *testing.T) {
	tick := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC)
	processingNow := tick.Add(5 * time.Minute)
	leaseDuration := 60 * time.Millisecond
	ticks := make(chan time.Time, 1)
	stop := make(chan struct{})
	heartbeaten := make(chan time.Time, 1)
	done := make(chan error, 1)

	go func() {
		done <- heartbeatOnTicks(context.Background(), stop, ticks, func() time.Time {
			return processingNow
		}, leaseDuration, func(leaseUntil time.Time) error {
			heartbeaten <- leaseUntil
			return nil
		})
	}()
	ticks <- tick

	select {
	case got := <-heartbeaten:
		want := processingNow.Add(leaseDuration)
		if !got.Equal(want) {
			t.Fatalf("leaseUntil=%s want=%s", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat was not called")
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("heartbeat loop error=%v", err)
	}
}
