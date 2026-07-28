package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type fakeWorkerHealthLifecycle struct {
	err      error
	called   chan struct{}
	callOnce sync.Once
}

func (s *fakeWorkerHealthLifecycle) Shutdown(context.Context) error {
	s.callOnce.Do(func() { close(s.called) })
	return s.err
}

func TestWorkerRuntimeHealthFailureDrainsWorkerBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error)
	healthDone := make(chan error, 1)
	healthDone <- errors.New("secret listener detail")
	health := &fakeWorkerHealthLifecycle{called: make(chan struct{})}
	result := make(chan error, 1)
	resourcesClosed := make(chan struct{})
	go func() {
		err := coordinateWorkerRuntime(
			ctx,
			cancel,
			health,
			workerDone,
			healthDone,
			time.Second,
		)
		close(resourcesClosed)
		result <- err
	}()

	<-health.called
	select {
	case err := <-result:
		t.Fatalf("returned before worker drained: %v", err)
	case <-resourcesClosed:
		t.Fatal("resources closed before worker drained")
	default:
	}
	workerDone <- nil
	if err := <-result; err == nil || err.Error() != "worker health" {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("worker context was not cancelled")
	}
}

func TestWorkerRuntimeCoordinatesEveryExitThroughSharedShutdown(t *testing.T) {
	tests := []struct {
		name          string
		triggerSignal bool
		workerErr     error
		healthErr     error
		shutdownErr   error
		wantErr       string
	}{
		{name: "signal", triggerSignal: true},
		{name: "worker closed", workerErr: nil},
		{name: "worker failure", workerErr: errors.New("secret worker detail"), wantErr: "worker lifecycle"},
		{name: "health closed", healthErr: http.ErrServerClosed},
		{name: "shutdown failure", triggerSignal: true, shutdownErr: errors.New("secret shutdown detail"), wantErr: "worker health shutdown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cancel := context.WithCancel(context.Background())
			defer cancel()
			signalCtx, signalCancel := context.WithCancel(context.Background())
			defer signalCancel()
			workerDone := make(chan error, 1)
			healthDone := make(chan error, 1)
			health := &fakeWorkerHealthLifecycle{
				err: tc.shutdownErr, called: make(chan struct{}),
			}
			if tc.triggerSignal {
				signalCancel()
			} else if tc.name == "worker closed" || tc.workerErr != nil {
				workerDone <- tc.workerErr
			} else {
				healthDone <- tc.healthErr
			}

			result := make(chan error, 1)
			go func() {
				result <- coordinateWorkerRuntime(
					signalCtx,
					cancel,
					health,
					workerDone,
					healthDone,
					time.Second,
				)
			}()
			<-health.called
			if !tc.triggerSignal && tc.name != "worker closed" && tc.workerErr == nil {
				workerDone <- nil
			} else if tc.triggerSignal {
				workerDone <- nil
			}
			err := <-result
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err=%v", err)
				}
			} else if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err=%v want=%q", err, tc.wantErr)
			}
		})
	}
}
