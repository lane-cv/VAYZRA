package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/redisx"
)

type serverOperationsRuntime struct {
	events *[]string
}

func (g *serverOperationsRuntime) AcquireShared(context.Context) (func(), error) {
	*g.events = append(*g.events, "gate_acquire")
	return func() { *g.events = append(*g.events, "gate_release") }, nil
}

func (*serverOperationsRuntime) ClaimsAllowed(context.Context) (bool, error) {
	return true, nil
}

func (g *serverOperationsRuntime) Close(context.Context) error {
	*g.events = append(*g.events, "operations_close")
	return nil
}

func TestProductionApplicationRequiresOperationalGate(t *testing.T) {
	closed := false
	handler, cleanup, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:              func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate:           func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth:           func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		ready:             func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		close:             func(*pgxpool.Pool) { closed = true },
		requireOperations: true,
	})
	if handler != nil || cleanup != nil || err == nil || err.Error() != "initialize operations gate" || !closed {
		t.Fatalf("handler=%v cleanup_present=%t err=%v closed=%t", handler, cleanup != nil, err, closed)
	}
}

func TestProductionApplicationSharesOneOperationalGateAndClosesItBeforePool(t *testing.T) {
	var events []string
	gate := &serverOperationsRuntime{events: &events}
	cleaner := &serverUploadCleaner{}
	var cleanupGate, outboxGate, aiGate operations.ClaimGate
	handler, closeResources, err := buildApplication(context.Background(), config.Config{
		PublicOrigin: "https://learn.example.com",
	}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		newUploads: func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error) {
			return cleaner, nil
		},
		newOperations: func(*pgxpool.Pool) operationsRuntime { return gate },
		startUploadCleanup: func(got files.ExpiredUploadCleaner, claimGate operations.ClaimGate) func() {
			if got != cleaner {
				t.Fatal("wrong cleanup service")
			}
			cleanupGate = claimGate
			return func() { events = append(events, "cleanup_stop") }
		},
		startOutbox: func(_ *pgxpool.Pool, claimGate operations.ClaimGate) func() {
			outboxGate = claimGate
			return func() { events = append(events, "outbox_stop") }
		},
		startAIRunner: func(_ context.Context, _ *pgxpool.Pool, _ config.Config, claimGate operations.ClaimGate) (func(), error) {
			aiGate = claimGate
			return func() { events = append(events, "ai_stop") }, nil
		},
		ready:             func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		close:             func(*pgxpool.Pool) { events = append(events, "pool_close") },
		requireOperations: true,
		openRedis: func(string) (*redis.Client, error) {
			return redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}), nil
		},
		newThrottle: func(*redis.Client, config.Config) (redisx.Limiter, redisx.CaptchaService) {
			return nil, nil
		},
		closeRedis: func(*redis.Client) { events = append(events, "redis_close") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleanupGate != gate || outboxGate != gate || aiGate != gate {
		t.Fatalf("cleanup=%T outbox=%T ai=%T gate=%T", cleanupGate, outboxGate, aiGate, gate)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://learn.example.com")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	closeResources()
	got := strings.Join(events, ",")
	if got != "gate_acquire,gate_release,ai_stop,outbox_stop,cleanup_stop,redis_close,operations_close,pool_close" {
		t.Fatalf("lifecycle=%s", got)
	}
}

func TestOperationalGateClosesOnRedisInitializationFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		openRedis func(string) (*redis.Client, error)
	}{
		{
			name: "open",
			openRedis: func(string) (*redis.Client, error) {
				return nil, errors.New("secret redis detail")
			},
		},
		{
			name: "wiring",
			openRedis: func(string) (*redis.Client, error) {
				return redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}), nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			gate := &serverOperationsRuntime{events: &events}
			handler, closeResources, err := buildApplication(
				context.Background(),
				config.Config{},
				applicationDependencies{
					open:              func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
					migrate:           func(context.Context, *pgxpool.Pool) error { return nil },
					newAuth:           func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
					newOperations:     func(*pgxpool.Pool) operationsRuntime { return gate },
					requireOperations: true,
					ready:             func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
					openRedis:         tc.openRedis,
					close:             func(*pgxpool.Pool) { events = append(events, "pool_close") },
				},
			)
			if handler != nil || closeResources != nil || err == nil ||
				err.Error() != "initialize login throttling" {
				t.Fatalf(
					"handler=%v cleanup_present=%t err=%v",
					handler,
					closeResources != nil,
					err,
				)
			}
			if got := strings.Join(events, ","); got != "operations_close,pool_close" {
				t.Fatalf("lifecycle=%s", got)
			}
		})
	}
}

var _ operationsRuntime = (*serverOperationsRuntime)(nil)
