package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type workerLifecycleEvents struct {
	mu     sync.Mutex
	events []string
}

func (e *workerLifecycleEvents) add(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *workerLifecycleEvents) joined() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.events, ",")
}

type fakeWorkerHealthLifecycle struct {
	shutdownErr error
	closeErr    error
	events      *workerLifecycleEvents
}

func (s *fakeWorkerHealthLifecycle) Shutdown(context.Context) error {
	s.events.add("health_shutdown")
	return s.shutdownErr
}

func (s *fakeWorkerHealthLifecycle) Close() error {
	s.events.add("health_close")
	return s.closeErr
}

type workerRuntimeResult struct {
	safeToClean bool
	err         error
}

func TestWorkerRuntimeHealthFailureDrainsWorkerBeforeHealthAndCleanup(t *testing.T) {
	workerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error)
	healthDone := make(chan error, 1)
	healthDone <- errors.New("secret listener detail")
	events := &workerLifecycleEvents{}
	health := &fakeWorkerHealthLifecycle{events: events}
	result := make(chan workerRuntimeResult, 1)
	go func() {
		safeToClean, err := coordinateWorkerRuntime(
			workerCtx,
			cancel,
			health,
			workerDone,
			healthDone,
			time.Second,
		)
		result <- workerRuntimeResult{safeToClean: safeToClean, err: err}
	}()

	select {
	case <-workerCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("worker context was not cancelled after health failure")
	}
	select {
	case got := <-result:
		t.Fatalf("returned before worker drained: %#v", got)
	default:
	}
	events.add("worker_stopped")
	workerDone <- nil
	got := <-result
	if !got.safeToClean || got.err == nil || got.err.Error() != "worker health" {
		t.Fatalf("result=%#v", got)
	}
	events.add("operations_close")
	events.add("pool_close")
	if order := events.joined(); order !=
		"worker_stopped,health_shutdown,operations_close,pool_close" {
		t.Fatalf("lifecycle=%s", order)
	}
}

func TestWorkerRuntimeTimeoutLeavesDatabaseResourcesOpen(t *testing.T) {
	workerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signalCtx, signalCancel := context.WithCancel(workerCtx)
	signalCancel()
	events := &workerLifecycleEvents{}
	safeToClean, err := coordinateWorkerRuntime(
		signalCtx,
		cancel,
		&fakeWorkerHealthLifecycle{events: events},
		make(chan error),
		make(chan error),
		20*time.Millisecond,
	)
	if safeToClean || err == nil || err.Error() != "worker shutdown timeout" {
		t.Fatalf("safe=%t err=%v", safeToClean, err)
	}
	if safeToClean {
		events.add("operations_close")
		events.add("pool_close")
	}
	if order := events.joined(); order != "health_shutdown" {
		t.Fatalf("database resources closed after worker timeout: %s", order)
	}
}

func TestWorkerRuntimeCoordinatesEveryExitThroughSafeShutdown(t *testing.T) {
	tests := []struct {
		name          string
		triggerSignal bool
		triggerWorker bool
		workerErr     error
		healthErr     error
		shutdownErr   error
		closeErr      error
		wantSafe      bool
		wantErr       string
		wantOrder     string
	}{
		{
			name: "signal", triggerSignal: true, wantSafe: true,
			wantOrder: "worker_stopped,health_shutdown,operations_close,pool_close",
		},
		{
			name: "worker closed", triggerWorker: true, wantSafe: true,
			wantOrder: "worker_stopped,health_shutdown,operations_close,pool_close",
		},
		{
			name: "worker failure", triggerWorker: true,
			workerErr: errors.New("secret worker detail"),
			wantSafe:  true, wantErr: "worker lifecycle",
			wantOrder: "worker_stopped,health_shutdown,operations_close,pool_close",
		},
		{
			name: "health closed", healthErr: http.ErrServerClosed, wantSafe: true,
			wantOrder: "worker_stopped,health_shutdown,operations_close,pool_close",
		},
		{
			name: "shutdown failure is force closed", triggerSignal: true,
			shutdownErr: errors.New("secret shutdown detail"),
			wantErr:     "worker health shutdown",
			wantOrder:   "worker_stopped,health_shutdown,health_close",
		},
		{
			name:          "failed force close disarms database cleanup",
			triggerSignal: true,
			shutdownErr:   errors.New("secret shutdown detail"),
			closeErr:      errors.New("secret close detail"),
			wantErr:       "worker health force close",
			wantOrder:     "worker_stopped,health_shutdown,health_close",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workerCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			signalCtx, signalCancel := context.WithCancel(workerCtx)
			defer signalCancel()
			workerDone := make(chan error, 1)
			healthDone := make(chan error, 1)
			events := &workerLifecycleEvents{}
			health := &fakeWorkerHealthLifecycle{
				shutdownErr: tc.shutdownErr,
				closeErr:    tc.closeErr,
				events:      events,
			}
			if tc.triggerSignal {
				signalCancel()
				go func() {
					<-workerCtx.Done()
					events.add("worker_stopped")
					workerDone <- tc.workerErr
				}()
			} else if tc.triggerWorker {
				events.add("worker_stopped")
				workerDone <- tc.workerErr
			} else {
				healthDone <- tc.healthErr
				go func() {
					<-workerCtx.Done()
					events.add("worker_stopped")
					workerDone <- tc.workerErr
				}()
			}

			safeToClean, err := coordinateWorkerRuntime(
				signalCtx,
				cancel,
				health,
				workerDone,
				healthDone,
				time.Second,
			)
			if safeToClean != tc.wantSafe {
				t.Fatalf("safe=%t want=%t err=%v", safeToClean, tc.wantSafe, err)
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err=%v", err)
				}
			} else if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err=%v want=%q", err, tc.wantErr)
			}
			if safeToClean {
				events.add("operations_close")
				events.add("pool_close")
			}
			if order := events.joined(); order != tc.wantOrder {
				t.Fatalf("lifecycle=%s want=%s", order, tc.wantOrder)
			}
		})
	}
}
