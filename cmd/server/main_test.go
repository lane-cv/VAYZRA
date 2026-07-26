package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/platform/config"
)

func TestNewServerUsesConfiguredAddressAndTimeouts(t *testing.T) {
	s := newServer(":9010", http.NotFoundHandler())
	if s.Addr != ":9010" {
		t.Fatalf("address = %q", s.Addr)
	}
	if s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.WriteTimeout == 0 || s.IdleTimeout == 0 {
		t.Fatalf("expected explicit timeouts: %#v", s)
	}
	if s.ReadHeaderTimeout != 5*time.Second || s.ReadTimeout != 15*time.Second || s.WriteTimeout != 15*time.Second || s.IdleTimeout != 60*time.Second {
		t.Fatalf("unexpected timeouts: %#v", s)
	}
}

func TestCombinedReadinessRequiresDatabaseAndObjectStoreWithoutLeaking(t *testing.T) {
	if defaultReadinessTimeout != 5*time.Second {
		t.Fatalf("default readiness timeout=%s", defaultReadinessTimeout)
	}
	secret := errors.New("minio endpoint secret")
	checks := 0
	ready := combineReadiness(func(context.Context) error { checks++; return nil }, func(context.Context) error { checks++; return secret })
	err := ready(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") || checks != 2 {
		t.Fatalf("err=%v checks=%d", err, checks)
	}
	checks = 0
	ready = combineReadiness(func(context.Context) error { checks++; return secret }, func(context.Context) error { checks++; return nil })
	if err = ready(context.Background()); err == nil || strings.Contains(err.Error(), "secret") || checks != 1 {
		t.Fatalf("db err=%v checks=%d", err, checks)
	}
}

func TestCombinedReadinessUsesOneDeadlineAndPropagatesCancellation(t *testing.T) {
	objectCancelled := make(chan struct{})
	ready := combineReadinessWithTimeout(func(ctx context.Context) error {
		select {
		case <-time.After(12 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, func(ctx context.Context) error {
		<-ctx.Done()
		close(objectCancelled)
		return ctx.Err()
	}, 20*time.Millisecond)
	started := time.Now()
	err := ready(context.Background())
	if err == nil || strings.Contains(err.Error(), "deadline") || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(started))
	}
	select {
	case <-objectCancelled:
	default:
		t.Fatal("object checker context was not cancelled")
	}

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	seenCancelled := false
	err = combineReadinessWithTimeout(func(ctx context.Context) error { seenCancelled = ctx.Err() != nil; return ctx.Err() }, func(context.Context) error { t.Fatal("object check after database failure"); return nil }, time.Second)(parent)
	if err == nil || !seenCancelled {
		t.Fatalf("parent cancellation err=%v seen=%t", err, seenCancelled)
	}
}

func TestAIUploadFactoryFailureIsSanitizedAndClosesDatabase(t *testing.T) {
	closed := false
	handler, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverStudentAuth{}, nil },
		newAIUploads: func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error) {
			return nil, errors.New("secret object endpoint")
		},
		close: func(*pgxpool.Pool) { closed = true },
	})
	if err == nil || handler != nil || closeResources != nil || !closed {
		t.Fatalf("handler=%v hasClose=%t err=%v closed=%t", handler, closeResources != nil, err, closed)
	}
	if err.Error() != "initialize AI upload service" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error=%q", err)
	}
}
