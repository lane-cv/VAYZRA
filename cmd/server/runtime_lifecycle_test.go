package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type fakeServerLifecycle struct {
	listen       chan error
	shutdownErr  error
	closeErr     error
	shutdown     chan struct{}
	closeCalled  chan struct{}
	listenCalled chan struct{}
	shutdownOne  sync.Once
	closeOnce    sync.Once
	listenOnce   sync.Once
	mu           sync.Mutex
	stopped      bool
}

func (s *fakeServerLifecycle) ListenAndServe() error {
	s.listenOnce.Do(func() { close(s.listenCalled) })
	return <-s.listen
}

func (s *fakeServerLifecycle) Shutdown(context.Context) error {
	s.shutdownOne.Do(func() {
		close(s.shutdown)
		if s.shutdownErr == nil {
			s.markStopped()
			select {
			case s.listen <- http.ErrServerClosed:
			default:
			}
		}
	})
	return s.shutdownErr
}

func (s *fakeServerLifecycle) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeCalled)
		if s.closeErr == nil {
			s.markStopped()
			select {
			case s.listen <- http.ErrServerClosed:
			default:
			}
		}
	})
	return s.closeErr
}

func (s *fakeServerLifecycle) markStopped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
}

func (s *fakeServerLifecycle) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func newFakeServerLifecycle() *fakeServerLifecycle {
	return &fakeServerLifecycle{
		listen:       make(chan error, 1),
		shutdown:     make(chan struct{}),
		closeCalled:  make(chan struct{}),
		listenCalled: make(chan struct{}),
	}
}

func TestServerRuntimeLifecycleCleansOnlyAfterRuntimeStops(t *testing.T) {
	tests := []struct {
		name        string
		signal      bool
		listenErr   error
		shutdownErr error
		closeErr    error
		wantErr     string
		wantClose   bool
		wantCleanup int
	}{
		{
			name: "listen failure", listenErr: errors.New("secret listen detail"),
			wantErr: "server start", wantCleanup: 1,
		},
		{name: "listen closed", listenErr: http.ErrServerClosed, wantCleanup: 1},
		{name: "signal", signal: true, wantCleanup: 1},
		{
			name: "shutdown failure force closes and cleans resources", signal: true,
			shutdownErr: errors.New("secret shutdown detail"),
			wantErr:     "server shutdown", wantClose: true, wantCleanup: 1,
		},
		{
			name: "failed force close still cleans resources", signal: true,
			shutdownErr: errors.New("secret shutdown detail"),
			closeErr:    errors.New("secret close detail"),
			wantErr:     "server force close", wantClose: true, wantCleanup: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			signalCtx, signalCancel := context.WithCancel(context.Background())
			defer signalCancel()
			server := newFakeServerLifecycle()
			server.shutdownErr = tc.shutdownErr
			server.closeErr = tc.closeErr
			if tc.listenErr != nil {
				server.listen <- tc.listenErr
			}
			if tc.signal {
				signalCancel()
			}
			cleanupCalls := 0
			returned := false
			err := runServerLifecycle(signalCtx, server, func() {
				if returned {
					t.Fatal("cleanup ran after lifecycle returned")
				}
				if tc.closeErr == nil && !server.isStopped() {
					t.Fatal("cleanup ran while server runtime was active")
				}
				cleanupCalls++
			})
			returned = true
			if cleanupCalls != tc.wantCleanup {
				t.Fatalf(
					"cleanup_calls=%d want=%d",
					cleanupCalls,
					tc.wantCleanup,
				)
			}
			select {
			case <-server.shutdown:
			default:
				t.Fatal("server shutdown was not attempted")
			}
			select {
			case <-server.closeCalled:
				if !tc.wantClose {
					t.Fatal("unexpected force close")
				}
			default:
				if tc.wantClose {
					t.Fatal("force close was not attempted")
				}
			}
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

func TestInternalServerRuntimeLifecycleStopsBothWhenEitherExits(t *testing.T) {
	signals, cancel := context.WithCancel(context.Background())
	defer cancel()
	public := newFakeServerLifecycle()
	internal := newFakeServerLifecycle()
	internal.listen <- errors.New("secret internal listen detail")
	cleanupCalls := 0

	err := runServerLifecycles(
		signals,
		[]serverLifecycle{public, internal},
		func() {
			if !public.isStopped() || !internal.isStopped() {
				t.Fatal("cleanup ran before both listeners stopped")
			}
			cleanupCalls++
		},
	)
	if err == nil || err.Error() != "server start" || cleanupCalls != 1 {
		t.Fatalf("err=%v cleanupCalls=%d", err, cleanupCalls)
	}
	for name, server := range map[string]*fakeServerLifecycle{
		"public": public, "internal": internal,
	} {
		select {
		case <-server.shutdown:
		default:
			t.Fatalf("%s shutdown was not attempted", name)
		}
	}
}

func TestInternalServerRuntimeLifecycleSignalStopsBoth(t *testing.T) {
	signals, cancel := context.WithCancel(context.Background())
	public := newFakeServerLifecycle()
	internal := newFakeServerLifecycle()
	cancel()
	cleanupCalls := 0

	err := runServerLifecycles(
		signals,
		[]serverLifecycle{public, internal},
		func() { cleanupCalls++ },
	)
	if err != nil || cleanupCalls != 1 ||
		!public.isStopped() || !internal.isStopped() {
		t.Fatalf(
			"err=%v cleanupCalls=%d publicStopped=%t internalStopped=%t",
			err,
			cleanupCalls,
			public.isStopped(),
			internal.isStopped(),
		)
	}
}

func TestInternalServerRuntimeLifecycleValidatesAllServersBeforeStarting(t *testing.T) {
	signals, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := newFakeServerLifecycle()
	cleanupCalls := 0
	err := runServerLifecycles(
		signals,
		[]serverLifecycle{server, nil},
		func() { cleanupCalls++ },
	)
	if err == nil || err.Error() != "server configuration" || cleanupCalls != 1 {
		t.Fatalf("err=%v cleanupCalls=%d", err, cleanupCalls)
	}
	select {
	case <-server.listenCalled:
		t.Fatal("valid server started before the whole list was validated")
	case <-time.After(100 * time.Millisecond):
	}
}
