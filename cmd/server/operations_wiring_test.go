package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/redisx"
)

type serverOperationsRuntime struct {
	events *[]string
}

type serverAdminOperationsService struct{}

type serverAdminBackupService struct{}

func (*serverAdminBackupService) RequestManual(
	context.Context,
	operations.Principal,
	string,
) (backup.Run, error) {
	return backup.Run{
		ID:      uuid.MustParse("20000000-0000-4000-8000-000000000001"),
		Trigger: backup.TriggerManual, State: backup.StateQueued,
		RequestedAt: time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC),
	}, nil
}

func (*serverAdminBackupService) List(
	context.Context,
	operations.Principal,
	backup.Filter,
) (backup.Page, error) {
	return backup.Page{Items: []backup.RunSummary{}}, nil
}

func (*serverAdminBackupService) Get(
	context.Context,
	operations.Principal,
	uuid.UUID,
) (backup.RunDetail, error) {
	return backup.RunDetail{}, backup.ErrNotFound
}

func newServerAdminOperations(*pgxpool.Pool, operationsRuntime) operations.HTTPService {
	return &serverAdminOperationsService{}
}

func (*serverAdminOperationsService) GetSettings(context.Context, operations.Principal) (operations.Settings, error) {
	return operations.Settings{
		Version: 1, SiteName: "HappyLearn", SoftDeleteRetentionDays: 30,
		AuditRetentionDays: 365, OperationalSampleRetentionDays: 7,
		BackupHour: 3, BackupTimezone: "Asia/Shanghai",
		DiskWarningPercent: 75, DiskCriticalPercent: 90,
		AIErrorWarningPercent: 10, AIErrorCriticalPercent: 25,
		ProcessingQueueWarning: 20, ProcessingQueueCritical: 100,
		UpdatedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
	}, nil
}

func (*serverAdminOperationsService) UpdateSettings(_ context.Context, _ operations.Principal, settings operations.Settings) (operations.Settings, error) {
	return settings, nil
}

func (*serverAdminOperationsService) ListAudit(context.Context, operations.Principal, audit.AuditFilter) (audit.AuditPage, error) {
	return audit.AuditPage{Items: []audit.Record{}}, nil
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

func TestProductionApplicationWiresAdminOperationsFromSharedRuntime(t *testing.T) {
	var events []string
	gate := &serverOperationsRuntime{events: &events}
	var factoryGate operationsRuntime
	handler, closeResources, err := buildApplication(
		context.Background(),
		config.Config{},
		applicationDependencies{
			open:          func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
			migrate:       func(context.Context, *pgxpool.Pool) error { return nil },
			newAuth:       func(*pgxpool.Pool) (auth.HTTPService, error) { return serverAdminAuth{}, nil },
			newOperations: func(*pgxpool.Pool) operationsRuntime { return gate },
			newAdminOperations: func(_ *pgxpool.Pool, runtime operationsRuntime) operations.HTTPService {
				factoryGate = runtime
				return &serverAdminOperationsService{}
			},
			requireOperations: true,
			ready: func(*pgxpool.Pool) func(context.Context) error {
				return func(context.Context) error { return nil }
			},
			close: func(*pgxpool.Pool) { events = append(events, "pool_close") },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/operations/settings", nil)
	request.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || factoryGate != gate ||
		!strings.Contains(response.Body.String(), `"siteName":"HappyLearn"`) {
		t.Fatalf("status=%d same_gate=%t body=%s", response.Code, factoryGate == gate, response.Body.String())
	}
	closeResources()
	if got := strings.Join(events, ","); got != "operations_close,pool_close" {
		t.Fatalf("lifecycle=%s", got)
	}
}

func TestProductionApplicationWiresAdminBackups(t *testing.T) {
	var events []string
	gate := &serverOperationsRuntime{events: &events}
	factoryCalled := false
	handler, closeResources, err := buildApplication(
		context.Background(),
		config.Config{},
		applicationDependencies{
			open:          func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
			migrate:       func(context.Context, *pgxpool.Pool) error { return nil },
			newAuth:       func(*pgxpool.Pool) (auth.HTTPService, error) { return serverAdminAuth{}, nil },
			newOperations: func(*pgxpool.Pool) operationsRuntime { return gate },
			newAdminOperations: func(*pgxpool.Pool, operationsRuntime) operations.HTTPService {
				return &serverAdminOperationsService{}
			},
			newAdminBackups: func(*pgxpool.Pool) backup.HTTPService {
				factoryCalled = true
				return &serverAdminBackupService{}
			},
			requireOperations: true,
			ready: func(*pgxpool.Pool) func(context.Context) error {
				return func(context.Context) error { return nil }
			},
			close: func(*pgxpool.Pool) { events = append(events, "pool_close") },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/operations/backups", nil)
	request.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !factoryCalled {
		t.Fatalf("status=%d factory_called=%t body=%s", response.Code, factoryCalled, response.Body.String())
	}
	closeResources()
	if got := strings.Join(events, ","); got != "operations_close,pool_close" {
		t.Fatalf("lifecycle=%s", got)
	}
}

func TestAdminOperationsInitializationFailureClosesRuntimeBeforePool(t *testing.T) {
	for _, tc := range []struct {
		name    string
		factory func(*pgxpool.Pool, operationsRuntime) operations.HTTPService
	}{
		{name: "missing factory"},
		{
			name: "nil service",
			factory: func(*pgxpool.Pool, operationsRuntime) operations.HTTPService {
				return nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			gate := &serverOperationsRuntime{events: &events}
			handler, cleanup, err := buildApplication(
				context.Background(),
				config.Config{},
				applicationDependencies{
					open:               func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
					migrate:            func(context.Context, *pgxpool.Pool) error { return nil },
					newAuth:            func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
					newOperations:      func(*pgxpool.Pool) operationsRuntime { return gate },
					newAdminOperations: tc.factory,
					requireOperations:  true,
					ready: func(*pgxpool.Pool) func(context.Context) error {
						return func(context.Context) error { return nil }
					},
					close: func(*pgxpool.Pool) { events = append(events, "pool_close") },
				},
			)
			if handler != nil || cleanup != nil || err == nil ||
				err.Error() != "initialize operations service" {
				t.Fatalf("handler=%v cleanup_present=%t err=%v", handler, cleanup != nil, err)
			}
			if got := strings.Join(events, ","); got != "operations_close,pool_close" {
				t.Fatalf("lifecycle=%s", got)
			}
		})
	}
}

func TestProductionApplicationSharesOneOperationalGateAndClosesItBeforePool(t *testing.T) {
	var events []string
	gate := &serverOperationsRuntime{events: &events}
	cleaner := &serverUploadCleaner{}
	var cleanupGate operations.WriteGate
	var outboxGate, aiGate operations.ClaimGate
	handler, closeResources, err := buildApplication(context.Background(), config.Config{
		PublicOrigin: "https://learn.example.com",
	}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		newUploads: func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error) {
			return cleaner, nil
		},
		newOperations:      func(*pgxpool.Pool) operationsRuntime { return gate },
		newAdminOperations: newServerAdminOperations,
		startUploadCleanup: func(got files.ExpiredUploadCleaner, claimGate operations.WriteGate) func() {
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
					open:               func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
					migrate:            func(context.Context, *pgxpool.Pool) error { return nil },
					newAuth:            func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
					newOperations:      func(*pgxpool.Pool) operationsRuntime { return gate },
					newAdminOperations: newServerAdminOperations,
					requireOperations:  true,
					ready:              func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
					openRedis:          tc.openRedis,
					close:              func(*pgxpool.Pool) { events = append(events, "pool_close") },
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
