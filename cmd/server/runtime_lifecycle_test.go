package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
)

type fakeServerLifecycle struct {
	listen      chan error
	shutdownErr error
	shutdown    chan struct{}
	once        sync.Once
}

func (s *fakeServerLifecycle) ListenAndServe() error {
	return <-s.listen
}

func (s *fakeServerLifecycle) Shutdown(context.Context) error {
	s.once.Do(func() {
		close(s.shutdown)
		select {
		case s.listen <- http.ErrServerClosed:
		default:
		}
	})
	return s.shutdownErr
}

func newFakeServerLifecycle() *fakeServerLifecycle {
	return &fakeServerLifecycle{
		listen:   make(chan error, 1),
		shutdown: make(chan struct{}),
	}
}

func TestServerRuntimeLifecycleAlwaysCleansBeforeReturning(t *testing.T) {
	tests := []struct {
		name        string
		signal      bool
		listenErr   error
		shutdownErr error
		wantErr     string
	}{
		{name: "listen failure", listenErr: errors.New("secret listen detail"), wantErr: "server start"},
		{name: "listen closed", listenErr: http.ErrServerClosed},
		{name: "signal", signal: true},
		{name: "shutdown failure", signal: true, shutdownErr: errors.New("secret shutdown detail"), wantErr: "server shutdown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			signalCtx, signalCancel := context.WithCancel(context.Background())
			defer signalCancel()
			server := newFakeServerLifecycle()
			server.shutdownErr = tc.shutdownErr
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
				cleanupCalls++
			})
			returned = true
			if cleanupCalls != 1 {
				t.Fatalf("cleanup_calls=%d", cleanupCalls)
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
