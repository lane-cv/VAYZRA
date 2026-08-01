package main

import (
	"context"
	"errors"
	"net"
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
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/redisx"
	"happylearn.local/app/tests/integration"
)

type serverOperationsRuntime struct {
	events   *[]string
	statuses []operations.InfrastructureStatus
}

func (g *serverOperationsRuntime) RecordInfrastructureStatus(
	_ context.Context,
	key operations.InfrastructureKey,
	configured bool,
	validatedAt time.Time,
) error {
	g.statuses = append(g.statuses, operations.InfrastructureStatus{
		Key: key, Configured: configured, LastValidatedAt: &validatedAt,
	})
	return nil
}

type serverAdminOperationsService struct{}

type serverAdminBackupService struct{}

type serverWebhookSender struct {
	enabled bool
}

func (sender *serverWebhookSender) Enabled() bool {
	return sender.enabled
}

func (*serverWebhookSender) Send(
	context.Context,
	operations.WebhookPayload,
) operations.WebhookDeliveryResult {
	return operations.WebhookDeliveryResult{
		Succeeded: true, HTTPStatusClass: 2,
	}
}

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

func (*serverAdminOperationsService) GetDashboard(
	context.Context,
	operations.Principal,
) (operations.Dashboard, error) {
	return operations.Dashboard{
		ObservedAt:       time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC),
		Services:         []operations.ServiceHealth{},
		Queues:           []operations.QueueSummary{},
		RecentAuditState: operations.DataStateEmpty,
		RecentAudit:      []operations.AuditSummary{},
	}, nil
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

func TestProductionApplicationRecordsOwnedInfrastructureWithoutMetadata(t *testing.T) {
	var events []string
	runtime := &serverOperationsRuntime{events: &events}
	called := 0
	cfg := config.Config{
		DatabaseURL: "postgres://private-database",
		RedisURL: "redis://private-redis",
		LoginThrottleSecret: "private-login-secret",
		MinIOEndpoint: "private-object-store",
		MinIOAccessKey: "private-access-key",
		MinIOSecretKey: "private-secret-key",
		MinIOOriginalsBucket: "private-originals",
		MinIOPreviewsBucket: "private-previews",
		AIMasterKey: make([]byte, 32),
		AIMasterKeyVersion: 1,
		MetricsBearerSecret: "private-metrics-secret",
		HostMetricsHMACSecret: []byte("private-host-secret"),
	}
	_, closeResources, err := buildApplication(
		context.Background(),
		cfg,
		applicationDependencies{
			open: func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
			migrate: func(context.Context, *pgxpool.Pool) error { return nil },
			newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverAdminAuth{}, nil },
			newOperations: func(*pgxpool.Pool) operationsRuntime { return runtime },
			newAdminOperations: newServerAdminOperations,
			requireOperations: true,
			ready: func(*pgxpool.Pool) func(context.Context) error {
				return func(context.Context) error { return nil }
			},
			recordInfrastructure: func(
				ctx context.Context,
				writer operations.InfrastructureStatusWriter,
				got config.Config,
				webhookEnabled bool,
			) error {
				called++
				if got.DatabaseURL != cfg.DatabaseURL || webhookEnabled {
					t.Fatalf("config was not forwarded as status-only validation input")
				}
				return recordOwnedInfrastructureStatuses(
					ctx,
					writer,
					got,
					webhookEnabled,
					time.Date(2026, 7, 28, 4, 5, 6, 0, time.UTC),
				)
			},
			close: func(*pgxpool.Pool) {},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer closeResources()
	if called != 1 || len(runtime.statuses) != 7 {
		t.Fatalf("called=%d statuses=%+v", called, runtime.statuses)
	}
	for index, status := range runtime.statuses {
		if index < 6 && !status.Configured {
			t.Fatalf("owned status[%d]=%+v", index, status)
		}
		if index == 6 && status.Configured {
			t.Fatalf("disabled webhook status=%+v", status)
		}
	}
}

func TestProductionAdminOperationsServiceWiresDashboardReaders(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	runtime := operations.NewPostgresStore(pool)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := runtime.Close(closeCtx); err != nil {
			t.Errorf("close operations runtime: %v", err)
		}
	})
	service := newProductionAdminOperationsService(pool, runtime)
	if service == nil {
		t.Fatal("production operations service is nil")
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	dashboard, err := service.GetDashboard(readCtx, operations.Principal{
		User: auth.User{
			ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive,
		},
		RequestID: "server-dashboard-wiring",
		IP:        net.ParseIP("192.0.2.90"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.ObservedAt.IsZero() ||
		len(dashboard.Services) != len(operations.DashboardServiceOrder()) ||
		len(dashboard.Queues) != len(operations.DashboardQueueOrder()) {
		t.Fatalf("dashboard=%+v", dashboard)
	}
	alertService, ok := service.(operations.AlertHTTPService)
	if !ok {
		t.Fatalf("production operations service lacks alert API: %T", service)
	}
	alerts, err := alertService.ListAlerts(readCtx, operations.Principal{
		User: auth.User{
			ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive,
		},
		RequestID: "server-alert-wiring",
		IP:        net.ParseIP("192.0.2.91"),
	}, operations.AlertFilter{Limit: 50})
	if err != nil {
		t.Fatalf("alerts=%+v err=%v", alerts, err)
	}
	webhookService, ok := service.(operations.WebhookTestHTTPService)
	if !ok {
		t.Fatalf("production operations service lacks webhook test API: %T", service)
	}
	err = webhookService.TestWebhook(readCtx, operations.Principal{
		User: auth.User{
			ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive,
		},
		RequestID: "server-webhook-wiring",
		IP:        net.ParseIP("192.0.2.92"),
	})
	if !errors.Is(err, operations.ErrWebhookNotConfigured) {
		t.Fatalf("disabled webhook test error=%v", err)
	}

	nilPoolRuntime := operations.NewPostgresStore(nil)
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		_ = nilPoolRuntime.Close(closeCtx)
	})
	if got := newProductionAdminOperationsService(nil, nilPoolRuntime); got != nil {
		t.Fatalf("nil-pool production service=%T want nil", got)
	}
}

func TestProductionApplicationStopsAlertRunnerBeforeOperationsAndPool(t *testing.T) {
	var events []string
	gate := &serverOperationsRuntime{events: &events}
	handler, closeResources, err := buildApplication(
		context.Background(),
		config.Config{},
		applicationDependencies{
			open: func(context.Context, string) (*pgxpool.Pool, error) {
				return nil, nil
			},
			migrate: func(context.Context, *pgxpool.Pool) error { return nil },
			newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) {
				return serverAdminAuth{}, nil
			},
			newOperations:      func(*pgxpool.Pool) operationsRuntime { return gate },
			newAdminOperations: newServerAdminOperations,
			startAlertRunner: func(*pgxpool.Pool) func() {
				events = append(events, "alert_start")
				return func() { events = append(events, "alert_stop") }
			},
			requireOperations: true,
			ready: func(*pgxpool.Pool) func(context.Context) error {
				return func(context.Context) error { return nil }
			},
			close: func(*pgxpool.Pool) { events = append(events, "pool_close") },
		},
	)
	if err != nil || handler == nil || closeResources == nil {
		t.Fatalf(
			"handler=%v cleanup_present=%t err=%v",
			handler,
			closeResources != nil,
			err,
		)
	}
	closeResources()
	if got := strings.Join(events, ","); got != "alert_start,alert_stop,operations_close,pool_close" {
		t.Fatalf("lifecycle=%s", got)
	}
}

func TestProductionApplicationStartsWebhookBeforeAlertAndStopsInReverse(
	t *testing.T,
) {
	var events []string
	gate := &serverOperationsRuntime{events: &events}
	sender := &serverWebhookSender{enabled: true}
	var adminSender, runnerSender operations.WebhookTestSender
	var alertWebhookEnabled bool
	handler, closeResources, err := buildApplication(
		context.Background(),
		config.Config{},
		applicationDependencies{
			open: func(context.Context, string) (*pgxpool.Pool, error) {
				return nil, nil
			},
			migrate: func(context.Context, *pgxpool.Pool) error { return nil },
			newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) {
				return serverAdminAuth{}, nil
			},
			newOperations: func(*pgxpool.Pool) operationsRuntime {
				return gate
			},
			newWebhookSender: func(
				context.Context,
				config.Config,
			) (operations.WebhookTestSender, error) {
				return sender, nil
			},
			newAdminOperationsWithWebhook: func(
				_ *pgxpool.Pool,
				_ operationsRuntime,
				got operations.WebhookTestSender,
			) operations.HTTPService {
				adminSender = got
				return &serverAdminOperationsService{}
			},
			startWebhookRunner: func(
				_ *pgxpool.Pool,
				got operations.WebhookTestSender,
			) (func(), error) {
				runnerSender = got
				events = append(events, "webhook_start")
				return func() {
					events = append(events, "webhook_stop")
				}, nil
			},
			startAlertRunnerWithWebhook: func(
				_ *pgxpool.Pool,
				enabled bool,
			) func() {
				alertWebhookEnabled = enabled
				events = append(events, "alert_start")
				return func() {
					events = append(events, "alert_stop")
				}
			},
			requireOperations: true,
			ready: func(*pgxpool.Pool) func(context.Context) error {
				return func(context.Context) error { return nil }
			},
			close: func(*pgxpool.Pool) {
				events = append(events, "pool_close")
			},
		},
	)
	if err != nil || handler == nil || closeResources == nil {
		t.Fatalf(
			"handler=%v cleanup_present=%t err=%v",
			handler,
			closeResources != nil,
			err,
		)
	}
	if adminSender != sender || runnerSender != sender ||
		!alertWebhookEnabled {
		t.Fatalf(
			"admin_same=%t runner_same=%t alert_enabled=%t",
			adminSender == sender,
			runnerSender == sender,
			alertWebhookEnabled,
		)
	}
	closeResources()
	if got := strings.Join(events, ","); got !=
		"webhook_start,alert_start,alert_stop,webhook_stop,operations_close,pool_close" {
		t.Fatalf("lifecycle=%s", got)
	}
}

func TestWebhookInitializationFailureClosesOperationsAndPool(t *testing.T) {
	for name, configure := range map[string]func(*applicationDependencies){
		"sender": func(deps *applicationDependencies) {
			deps.newWebhookSender = func(
				context.Context,
				config.Config,
			) (operations.WebhookTestSender, error) {
				return nil, errors.New("private sender detail")
			}
		},
		"runner": func(deps *applicationDependencies) {
			deps.newWebhookSender = func(
				context.Context,
				config.Config,
			) (operations.WebhookTestSender, error) {
				return &serverWebhookSender{enabled: true}, nil
			}
			deps.startWebhookRunner = func(
				*pgxpool.Pool,
				operations.WebhookTestSender,
			) (func(), error) {
				return nil, errors.New("private runner detail")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var events []string
			gate := &serverOperationsRuntime{events: &events}
			deps := applicationDependencies{
				open: func(
					context.Context,
					string,
				) (*pgxpool.Pool, error) {
					return nil, nil
				},
				migrate: func(
					context.Context,
					*pgxpool.Pool,
				) error {
					return nil
				},
				newAuth: func(
					*pgxpool.Pool,
				) (auth.HTTPService, error) {
					return serverAdminAuth{}, nil
				},
				newOperations: func(*pgxpool.Pool) operationsRuntime {
					return gate
				},
				newAdminOperationsWithWebhook: func(
					*pgxpool.Pool,
					operationsRuntime,
					operations.WebhookTestSender,
				) operations.HTTPService {
					return &serverAdminOperationsService{}
				},
				requireOperations: true,
				ready: func(
					*pgxpool.Pool,
				) func(context.Context) error {
					return func(context.Context) error { return nil }
				},
				close: func(*pgxpool.Pool) {
					events = append(events, "pool_close")
				},
			}
			configure(&deps)
			handler, cleanup, err := buildApplication(
				context.Background(),
				config.Config{},
				deps,
			)
			if handler != nil || cleanup != nil || err == nil ||
				err.Error() != "initialize alert webhook" {
				t.Fatalf(
					"handler=%v cleanup_present=%t err=%v",
					handler,
					cleanup != nil,
					err,
				)
			}
			if got := strings.Join(events, ","); got !=
				"operations_close,pool_close" {
				t.Fatalf("lifecycle=%s", got)
			}
		})
	}
}

func TestProductionAlertRunnerCollectsApplicationAggregates(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
TRUNCATE operational_samples;
TRUNCATE operational_alerts CASCADE;
TRUNCATE backup_runs CASCADE;
TRUNCATE login_events;
INSERT INTO system_settings(singleton_id) VALUES(true)
ON CONFLICT(singleton_id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	stop := newProductionAlertRunner(pool)
	if stop == nil {
		t.Fatal("production alert runner returned nil stop")
	}
	defer stop()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		if err := pool.QueryRow(ctx, `
SELECT count(*) FROM operational_samples
WHERE source IN ('app','worker')`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("production runner aggregate samples=%d want=6", count)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
}

func TestAlertRunnerInitializationFailureClosesOperationsAndPool(t *testing.T) {
	var events []string
	gate := &serverOperationsRuntime{events: &events}
	handler, closeResources, err := buildApplication(
		context.Background(),
		config.Config{},
		applicationDependencies{
			open: func(context.Context, string) (*pgxpool.Pool, error) {
				return nil, nil
			},
			migrate: func(context.Context, *pgxpool.Pool) error { return nil },
			newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) {
				return serverAdminAuth{}, nil
			},
			newOperations:      func(*pgxpool.Pool) operationsRuntime { return gate },
			newAdminOperations: newServerAdminOperations,
			startAlertRunner:   func(*pgxpool.Pool) func() { return nil },
			requireOperations:  true,
			ready: func(*pgxpool.Pool) func(context.Context) error {
				return func(context.Context) error { return nil }
			},
			close: func(*pgxpool.Pool) { events = append(events, "pool_close") },
		},
	)
	if handler != nil || closeResources != nil || err == nil ||
		err.Error() != "initialize alert evaluator" {
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
}

func TestProductionApplicationStartsAndStopsRetentionBeforeOperationsAndPool(
	t *testing.T,
) {
	var events []string
	gate := &serverOperationsRuntime{events: &events}
	handler, closeResources, err := buildApplication(
		context.Background(),
		config.Config{},
		applicationDependencies{
			open: func(context.Context, string) (*pgxpool.Pool, error) {
				return nil, nil
			},
			migrate: func(context.Context, *pgxpool.Pool) error { return nil },
			newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) {
				return serverAdminAuth{}, nil
			},
			newOperations:      func(*pgxpool.Pool) operationsRuntime { return gate },
			newAdminOperations: newServerAdminOperations,
			startRetention: func(*pgxpool.Pool) (func(), error) {
				events = append(events, "retention_start")
				return func() {
					events = append(events, "retention_stop")
				}, nil
			},
			requireOperations: true,
			ready: func(*pgxpool.Pool) func(context.Context) error {
				return func(context.Context) error { return nil }
			},
			close: func(*pgxpool.Pool) {
				events = append(events, "pool_close")
			},
		},
	)
	if err != nil || handler == nil || closeResources == nil {
		t.Fatalf(
			"handler=%v cleanup_present=%t err=%v",
			handler,
			closeResources != nil,
			err,
		)
	}
	closeResources()
	if got := strings.Join(events, ","); got !=
		"retention_start,retention_stop,operations_close,pool_close" {
		t.Fatalf("lifecycle=%s", got)
	}
}

func TestRetentionInitializationFailureClosesOperationsAndPool(t *testing.T) {
	for name, startRetention := range map[string]func(
		*pgxpool.Pool,
	) (func(), error){
		"error": func(*pgxpool.Pool) (func(), error) {
			return nil, errors.New("private retention detail")
		},
		"nil stopper": func(*pgxpool.Pool) (func(), error) {
			return nil, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			var events []string
			gate := &serverOperationsRuntime{events: &events}
			handler, closeResources, err := buildApplication(
				context.Background(),
				config.Config{},
				applicationDependencies{
					open: func(
						context.Context,
						string,
					) (*pgxpool.Pool, error) {
						return nil, nil
					},
					migrate: func(
						context.Context,
						*pgxpool.Pool,
					) error {
						return nil
					},
					newAuth: func(
						*pgxpool.Pool,
					) (auth.HTTPService, error) {
						return serverAdminAuth{}, nil
					},
					newOperations: func(*pgxpool.Pool) operationsRuntime {
						return gate
					},
					newAdminOperations: newServerAdminOperations,
					startRetention:     startRetention,
					requireOperations:  true,
					ready: func(
						*pgxpool.Pool,
					) func(context.Context) error {
						return func(context.Context) error { return nil }
					},
					close: func(*pgxpool.Pool) {
						events = append(events, "pool_close")
					},
				},
			)
			if handler != nil || closeResources != nil || err == nil ||
				err.Error() != "initialize operations retention" ||
				strings.Contains(err.Error(), "private") {
				t.Fatalf(
					"handler=%v cleanup_present=%t err=%v",
					handler,
					closeResources != nil,
					err,
				)
			}
			if got := strings.Join(events, ","); got !=
				"operations_close,pool_close" {
				t.Fatalf("lifecycle=%s", got)
			}
		})
	}
}

func TestProductionRetentionHelperWiresFixedCategoryLogger(t *testing.T) {
	secret := "postgres://private-user:private-password@private-host/database"
	logged := make(chan string, 1)
	stop, err := startProductionRetentionScheduler(
		&serverRetentionSettingsStore{err: errors.New(secret)},
		&serverRetentionRunner{},
		time.Now,
		func(category string) {
			logged <- category
		},
	)
	if err != nil || stop == nil {
		t.Fatalf("stop nil=%t err=%v", stop == nil, err)
	}
	select {
	case category := <-logged:
		if category != "settings_read_failed" ||
			strings.Contains(category, secret) {
			t.Fatalf("category=%q", category)
		}
	case <-time.After(time.Second):
		t.Fatal("retention scheduler did not report startup failure")
	}
	stop()

	if got, err := newProductionRetentionSchedulerWithLog(nil, nil); got != nil || !errors.Is(err, operations.ErrInvalid) {
		t.Fatalf("nil-pool stop nil=%t err=%v", got == nil, err)
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

type serverRetentionSettingsStore struct {
	err error
}

func (store *serverRetentionSettingsStore) GetSettings(
	context.Context,
) (operations.Settings, error) {
	return operations.Settings{
		OperationalSampleRetentionDays: 7,
	}, store.err
}

func (*serverRetentionSettingsStore) UpdateSettings(
	context.Context,
	operations.Principal,
	operations.Settings,
) (operations.Settings, error) {
	return operations.Settings{}, nil
}

type serverRetentionRunner struct{}

func (*serverRetentionRunner) RunOnce(
	context.Context,
	time.Time,
	int,
) (operations.RetentionResult, error) {
	return operations.RetentionResult{}, nil
}
