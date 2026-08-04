package main

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"happylearn.local/app/internal/aiqa"
	"happylearn.local/app/internal/app"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/notifications"
	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/buildinfo"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/httpx"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/internal/platform/redisx"
	"happylearn.local/app/internal/platform/safelog"
	"happylearn.local/app/internal/qanda"
	"happylearn.local/app/internal/students"
	"happylearn.local/app/internal/teaching"
)

var (
	buildVersion     string
	buildCommit      string
	buildTime        string
	buildMinSchema   string
	buildMaxSchema   string
	runtimeBuildInfo = buildinfo.Development()
)

func main() {
	bootstrapLogger, err := safelog.New(os.Stderr, time.Now)
	if err != nil {
		os.Exit(1)
	}
	bootstrapLogger.Info("server.configuration", safelog.Field{
		Name:  "stage",
		Value: "start",
	})
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		bootstrapLogger.Error("server.error", safelog.Field{
			Name:  "stage",
			Value: "configuration",
		})
		os.Exit(1)
	}
	release, err := loadBuildInfo(cfg.Environment)
	if err != nil {
		bootstrapLogger.Error("server.error", safelog.Field{Name: "stage", Value: "build_info"})
		os.Exit(1)
	}
	runtimeBuildInfo = release
	logger, err := safelog.NewFromConfig(os.Stderr, time.Now, cfg)
	if err != nil {
		bootstrapLogger.Error("server.error", safelog.Field{
			Name:  "stage",
			Value: "logging",
		})
		os.Exit(1)
	}
	logger.Info("server.configuration", safelog.Field{
		Name:  "stage",
		Value: "success",
	})
	logger.Info("server.startup", safelog.Field{
		Name:  "stage",
		Value: "start",
	})

	runtime, closeResources, err := buildProductionApplicationWithLog(
		context.Background(),
		cfg,
		logger,
	)
	if err != nil {
		logger.Error("server.error", safelog.Field{
			Name:  "stage",
			Value: "startup",
		}, safelog.Field{
			Name:  "category",
			Value: applicationStartupComponent(err),
		})
		os.Exit(1)
	}
	logger.Info("server.startup", safelog.Field{
		Name:  "stage",
		Value: "success",
	})
	publicServer := newServerWithLog(
		cfg.ListenAddress,
		runtime.Public,
		logger,
		"public",
	)
	internalServer := newServerWithLog(
		cfg.InternalListenAddress,
		runtime.Internal,
		logger,
		"internal",
	)

	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runServerLifecyclesWithLog(
		signals,
		[]serverLifecycle{publicServer, internalServer},
		closeResources,
		logger,
	); err != nil {
		os.Exit(1)
	}
}

func applicationStartupComponent(err error) string {
	if err == nil {
		return "unknown"
	}
	components := map[string]string{
		"open authentication storage":             "database",
		"migrate authentication storage":          "migration",
		"initialize authentication service":       "authentication",
		"initialize upload service":               "uploads",
		"initialize question upload service":      "question_uploads",
		"initialize AI upload service":            "ai_uploads",
		"initialize student AI service":           "student_ai",
		"initialize AI configuration service":     "ai_configuration",
		"initialize AI read services":             "ai_reads",
		"initialize file access service":          "file_access",
		"initialize question file access service": "question_file_access",
		"initialize AI file access service":       "ai_file_access",
		"initialize readiness check":              "readiness",
		"initialize operations gate":              "operations_gate",
		"initialize alert webhook":                "alert_webhook",
		"initialize operations service":           "operations",
		"initialize backup service":               "backup",
		"initialize login throttling":             "login_throttling",
		"initialize upload cleanup":               "upload_cleanup",
		"initialize notification outbox":          "notification_outbox",
		"initialize AI runner":                    "ai_runner",
		"initialize alert runner":                 "alert_runner",
		"initialize webhook runner":               "webhook_runner",
		"initialize retention scheduler":          "retention",
		"initialize internal handler":             "internal_handler",
		"record infrastructure status":            "infrastructure_status",
	}
	if component, ok := components[err.Error()]; ok {
		return component
	}
	return "unknown"
}

type serverLifecycle interface {
	ListenAndServe() error
	Shutdown(context.Context) error
	Close() error
}

func runServerLifecycle(
	signals context.Context,
	server serverLifecycle,
	closeResources func(),
) error {
	return runServerLifecycles(
		signals,
		[]serverLifecycle{server},
		closeResources,
	)
}

func runServerLifecycles(
	signals context.Context,
	servers []serverLifecycle,
	closeResources func(),
) error {
	return runServerLifecyclesWithLog(
		signals,
		servers,
		closeResources,
		safelog.Logger{},
	)
}

func runServerLifecyclesWithLog(
	signals context.Context,
	servers []serverLifecycle,
	closeResources func(),
	logger safelog.Logger,
) error {
	if len(servers) == 0 || closeResources == nil {
		logServerError(logger, "configuration")
		return errors.New("server configuration")
	}
	for _, server := range servers {
		if server == nil {
			closeResources()
			logServerError(logger, "configuration")
			return errors.New("server configuration")
		}
	}
	errCh := make(chan error, len(servers))
	for _, server := range servers {
		go func(server serverLifecycle) {
			errCh <- server.ListenAndServe()
		}(server)
	}
	logger.Info("server.started")
	var result error
	resultStage := ""
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			result = errors.New("server start")
			resultStage = "listen"
		}
	case <-signals.Done():
	}
	logger.Info("server.shutdown", safelog.Field{
		Name:  "stage",
		Value: "start",
	})
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type shutdownResult struct {
		shutdownErr error
		closeErr    error
	}
	results := make(chan shutdownResult, len(servers))
	var shutdowns sync.WaitGroup
	shutdowns.Add(len(servers))
	for _, server := range servers {
		go func(server serverLifecycle) {
			defer shutdowns.Done()
			outcome := shutdownResult{}
			outcome.shutdownErr = server.Shutdown(shutdownCtx)
			if outcome.shutdownErr != nil {
				outcome.closeErr = server.Close()
			}
			results <- outcome
		}(server)
	}
	shutdowns.Wait()
	close(results)
	shutdownFailed := false
	forceCloseFailed := false
	for outcome := range results {
		shutdownFailed = shutdownFailed || outcome.shutdownErr != nil
		forceCloseFailed = forceCloseFailed || outcome.closeErr != nil
	}
	if forceCloseFailed {
		logServerError(logger, "force_close")
		return errors.New("server force close")
	}
	closeResources()
	if shutdownFailed {
		logServerError(logger, "shutdown")
		logger.Info("server.stopped")
		return errors.New("server shutdown")
	}
	if result != nil {
		logServerError(logger, resultStage)
		logger.Info("server.stopped")
		return result
	}
	logger.Info("server.stopped")
	return result
}

func logServerError(logger safelog.Logger, stage string) {
	logger.Error("server.error", safelog.Field{
		Name:  "stage",
		Value: stage,
	})
}

func newServer(address string, handler http.Handler) *http.Server {
	return newServerWithLog(
		address,
		handler,
		safelog.Logger{},
		"server",
	)
}

func newServerWithLog(
	address string,
	handler http.Handler,
	logger safelog.Logger,
	service string,
) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ErrorLog:          httpx.SafeServerErrorLog(logger, service),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

type applicationDependencies struct {
	logger                        safelog.Logger
	open                          func(context.Context, string) (*pgxpool.Pool, error)
	migrate                       func(context.Context, *pgxpool.Pool) error
	newAuth                       func(*pgxpool.Pool) (auth.HTTPService, error)
	newStudents                   func(*pgxpool.Pool) students.HTTPService
	newTeaching                   func(*pgxpool.Pool) teaching.AdminHTTPService
	newUploads                    func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error)
	newQAUploads                  func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error)
	newAIUploads                  func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error)
	newStudentAI                  func(context.Context, *pgxpool.Pool, config.Config) (aiqa.StudentService, aiqa.StudentEventStore, error)
	newFileAccess                 func(context.Context, *pgxpool.Pool, config.Config) (files.AccessHTTPService, error)
	newQAFileAccess               func(context.Context, *pgxpool.Pool, config.Config) (files.QAAccessHTTPService, error)
	newAIFileAccess               func(context.Context, *pgxpool.Pool, config.Config) (files.AIAccessHTTPService, error)
	newFileBindings               func(*pgxpool.Pool) files.BindingHTTPService
	newFileCenter                 func(*pgxpool.Pool) files.FileCenterHTTPService
	startUploadCleanup            func(files.ExpiredUploadCleaner, operations.WriteGate) func()
	newStudentTeaching            func(*pgxpool.Pool) teaching.StudentHTTPService
	newQuestions                  func(*pgxpool.Pool) qanda.HTTPServices
	newAdminAI                    func(context.Context, *pgxpool.Pool, config.Config) (aiqa.AdminConfigHTTPService, error)
	newAIReads                    func(*pgxpool.Pool) (aiqa.SummaryService, aiqa.AdminUsageService)
	newNotifications              func(*pgxpool.Pool) notifications.HTTPService
	startOutbox                   func(*pgxpool.Pool, operations.ClaimGate) func()
	startAIRunner                 func(context.Context, *pgxpool.Pool, config.Config, operations.ClaimGate) (func(), error)
	startAlertRunner              func(*pgxpool.Pool) func()
	startAlertRunnerWithWebhook   func(*pgxpool.Pool, bool) func()
	startWebhookRunner            func(*pgxpool.Pool, operations.WebhookTestSender) (func(), error)
	startRetention                func(*pgxpool.Pool) (func(), error)
	newOperations                 func(*pgxpool.Pool) operationsRuntime
	newAdminOperations            func(*pgxpool.Pool, operationsRuntime) operations.HTTPService
	newAdminOperationsWithWebhook func(
		*pgxpool.Pool,
		operationsRuntime,
		operations.WebhookTestSender,
	) operations.HTTPService
	newWebhookSender func(
		context.Context,
		config.Config,
	) (operations.WebhookTestSender, error)
	newAdminBackups        func(*pgxpool.Pool) backup.HTTPService
	requireOperations      bool
	ready                  func(*pgxpool.Pool) func(context.Context) error
	objectReady            func(context.Context, config.Config) (func(context.Context) error, error)
	readinessTimeout       time.Duration
	close                  func(*pgxpool.Pool)
	openRedis              func(string) (*redis.Client, error)
	newThrottle            func(*redis.Client, config.Config) (redisx.Limiter, redisx.CaptchaService)
	newProgressLimiter     func(*redis.Client, config.Config) redisx.ProgressWriteLimiter
	newSearchLimiter       func(*redis.Client, config.Config) redisx.SearchRateLimiter
	newProviderTestLimiter func(*redis.Client, config.Config) redisx.ProviderTestRateLimiter
	closeRedis             func(*redis.Client)
	newInternal            func(*pgxpool.Pool, *redis.Client, config.Config) (http.Handler, error)
	recordInfrastructure   func(
		context.Context,
		operations.InfrastructureStatusWriter,
		config.Config,
		bool,
	) error
}

type operationsRuntime interface {
	operations.WriteGate
	operations.ClaimGate
	operations.LeaseSessionCloser
}

type productionRunnerLogs struct {
	uploadCleanup       func(string)
	outbox              func(string)
	ai                  func(string)
	alert               func(string)
	webhook             func(string)
	retention           func(string)
	loginLimiter        func(string)
	progressLimiter     func(string)
	searchLimiter       func(string)
	providerTestLimiter func(string)
}

func newProductionRunnerLogs(logger safelog.Logger) productionRunnerLogs {
	return productionRunnerLogs{
		uploadCleanup:   safeCategoryLogger(logger, "upload.cleanup"),
		outbox:          safeCategoryLogger(logger, "notifications.outbox"),
		ai:              safeCategoryLogger(logger, "ai.runner"),
		alert:           safeCategoryLogger(logger, "operations.alert"),
		webhook:         safeCategoryLogger(logger, "operations.webhook"),
		retention:       safeCategoryLogger(logger, "operations.retention"),
		loginLimiter:    safeCategoryLogger(logger, "redis.login"),
		progressLimiter: safeCategoryLogger(logger, "redis.progress"),
		searchLimiter:   safeCategoryLogger(logger, "redis.search"),
		providerTestLimiter: safeCategoryLogger(
			logger,
			"redis.provider_test",
		),
	}
}

func safeCategoryLogger(logger safelog.Logger, event string) func(string) {
	return func(category string) {
		logger.Error(event, safelog.Field{
			Name:  "category",
			Value: category,
		})
	}
}

type applicationRuntime struct {
	Public   http.Handler
	Internal http.Handler
}

func buildProductionApplication(
	ctx context.Context,
	cfg config.Config,
) (*applicationRuntime, func(), error) {
	return buildProductionApplicationWithLog(ctx, cfg, safelog.Logger{})
}

func buildProductionApplicationWithLog(
	ctx context.Context,
	cfg config.Config,
	logger safelog.Logger,
) (*applicationRuntime, func(), error) {
	runnerLogs := newProductionRunnerLogs(logger)
	return buildApplicationRuntime(ctx, cfg, applicationDependencies{
		logger:          logger,
		open:            database.Open,
		migrate:         database.Migrate,
		newAuth:         newProductionAuthService,
		newStudents:     newProductionStudentService,
		newTeaching:     newProductionTeachingService,
		newUploads:      newProductionUploadService,
		newQAUploads:    newProductionQAUploadService,
		newAIUploads:    newProductionAIUploadService,
		newStudentAI:    newProductionStudentAIService,
		newFileAccess:   newProductionFileAccessService,
		newQAFileAccess: newProductionQAFileAccessService,
		newAIFileAccess: newProductionAIFileAccessService,
		newFileBindings: newProductionFileBindingService,
		newFileCenter:   newProductionFileCenterService,
		startUploadCleanup: func(
			cleaner files.ExpiredUploadCleaner,
			gate operations.WriteGate,
		) func() {
			return files.StartCleanupRunnerWithLog(
				cleaner,
				gate,
				runnerLogs.uploadCleanup,
			)
		},
		newStudentTeaching: newProductionStudentTeachingService,
		newQuestions:       newProductionQuestionServices,
		newAdminAI:         newProductionAdminAIService,
		newAIReads:         newProductionAIReadServices,
		newNotifications:   newProductionNotificationService,
		startOutbox: func(
			pool *pgxpool.Pool,
			gate operations.ClaimGate,
		) func() {
			return newProductionOutboxRunnerWithLog(
				pool,
				gate,
				runnerLogs.outbox,
			)
		},
		startAIRunner: func(
			ctx context.Context,
			pool *pgxpool.Pool,
			cfg config.Config,
			gate operations.ClaimGate,
		) (func(), error) {
			return newProductionAIRunnerWithLog(
				ctx,
				pool,
				cfg,
				gate,
				runnerLogs.ai,
			)
		},
		startAlertRunnerWithWebhook: func(
			pool *pgxpool.Pool,
			webhookEnabled bool,
		) func() {
			objectStoreMetrics, metricsErr := objectstore.NewMinIOStorageMetrics(
				objectstore.MinIOConfig{
					Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey,
					SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
				},
			)
			if metricsErr != nil {
				runnerLogs.alert("object_store_metrics_unavailable")
				return newProductionAlertRunnerWithWebhookAndLog(
					pool, webhookEnabled, runnerLogs.alert,
				)
			}
			return newProductionAlertRunnerWithWebhookAndLog(
				pool,
				webhookEnabled,
				runnerLogs.alert,
				objectStoreMetrics,
			)
		},
		startWebhookRunner: func(
			pool *pgxpool.Pool,
			sender operations.WebhookTestSender,
		) (func(), error) {
			return newProductionWebhookRunnerWithLog(
				pool,
				sender,
				runnerLogs.webhook,
			)
		},
		startRetention: func(pool *pgxpool.Pool) (func(), error) {
			return newProductionRetentionSchedulerWithLog(
				pool,
				runnerLogs.retention,
			)
		},
		newOperations: func(pool *pgxpool.Pool) operationsRuntime {
			return operations.NewPostgresStore(pool)
		},
		newAdminOperationsWithWebhook: newProductionAdminOperationsServiceWithWebhook,
		newWebhookSender:              newProductionWebhookSender,
		newAdminBackups:               newProductionAdminBackupService,
		requireOperations:             true,
		ready: func(pool *pgxpool.Pool) func(context.Context) error {
			return pool.Ping
		},
		objectReady: newProductionObjectReadiness,
		close:       func(pool *pgxpool.Pool) { pool.Close() },
		openRedis:   redisx.NewClient,
		newThrottle: func(client *redis.Client, cfg config.Config) (redisx.Limiter, redisx.CaptchaService) {
			policy := redisx.Policy{Secret: []byte(cfg.LoginThrottleSecret), Window: 15 * time.Minute, AccountFailures: 5, IPFailures: 20, Lockout: 15 * time.Minute}
			return redisx.NewLoginLimiterWithLog(
					client,
					policy,
					runnerLogs.loginLimiter,
				),
				redisx.NewCaptchaStore(client, []byte(cfg.LoginThrottleSecret))
		},
		newProgressLimiter: func(client *redis.Client, cfg config.Config) redisx.ProgressWriteLimiter {
			return redisx.NewProgressWriteLimiterWithLog(
				client,
				redisx.ProgressLimitPolicy{Secret: []byte(cfg.LoginThrottleSecret), Window: time.Minute, SessionMaxWrites: 60, AccountMaxWrites: 120},
				runnerLogs.progressLimiter,
			)
		},
		newSearchLimiter: func(client *redis.Client, cfg config.Config) redisx.SearchRateLimiter {
			return redisx.NewSearchLimiterWithLog(
				client,
				redisx.ResourceLimitPolicy{Secret: []byte(cfg.LoginThrottleSecret), Window: time.Minute, MaxRequests: 30},
				runnerLogs.searchLimiter,
			)
		},
		newProviderTestLimiter: func(client *redis.Client, cfg config.Config) redisx.ProviderTestRateLimiter {
			return redisx.NewProviderTestLimiterWithLog(
				client,
				redisx.ResourceLimitPolicy{Secret: []byte(cfg.LoginThrottleSecret), Window: time.Minute, MaxRequests: 5},
				runnerLogs.providerTestLimiter,
			)
		},
		closeRedis:  func(client *redis.Client) { _ = client.Close() },
		newInternal: newProductionInternalHandler,
		recordInfrastructure: func(
			ctx context.Context,
			writer operations.InfrastructureStatusWriter,
			cfg config.Config,
			webhookEnabled bool,
		) error {
			return recordOwnedInfrastructureStatuses(
				ctx,
				writer,
				cfg,
				webhookEnabled,
				time.Now(),
			)
		},
	})
}

func buildApplication(ctx context.Context, cfg config.Config, deps applicationDependencies) (http.Handler, func(), error) {
	runtime, closeResources, err := buildApplicationRuntime(ctx, cfg, deps)
	if err != nil {
		return nil, nil, err
	}
	return runtime.Public, closeResources, nil
}

func buildApplicationRuntime(
	ctx context.Context,
	cfg config.Config,
	deps applicationDependencies,
) (*applicationRuntime, func(), error) {
	pool, err := deps.open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, errors.New("open authentication storage")
	}
	closePool := func() { deps.close(pool) }
	if err := deps.migrate(ctx, pool); err != nil {
		closePool()
		return nil, nil, errors.New("migrate authentication storage")
	}
	service, err := deps.newAuth(pool)
	if err != nil {
		closePool()
		return nil, nil, errors.New("initialize authentication service")
	}
	var studentService students.HTTPService
	if deps.newStudents != nil {
		studentService = deps.newStudents(pool)
	}
	var teachingService teaching.AdminHTTPService
	if deps.newTeaching != nil {
		teachingService = deps.newTeaching(pool)
	}
	var uploadService files.UploadHTTPService
	if deps.newUploads != nil {
		uploadService, err = deps.newUploads(ctx, pool, cfg)
		if err != nil {
			closePool()
			return nil, nil, errors.New("initialize upload service")
		}
	}
	var qaUploadService files.UploadHTTPService
	if deps.newQAUploads != nil {
		qaUploadService, err = deps.newQAUploads(ctx, pool, cfg)
		if err != nil {
			closePool()
			return nil, nil, errors.New("initialize question upload service")
		}
	}
	var aiUploadService files.UploadHTTPService
	if deps.newAIUploads != nil {
		aiUploadService, err = deps.newAIUploads(ctx, pool, cfg)
		if err != nil {
			closePool()
			return nil, nil, errors.New("initialize AI upload service")
		}
	}
	var studentAI aiqa.StudentService
	var studentAIEvents aiqa.StudentEventStore
	if deps.newStudentAI != nil {
		studentAI, studentAIEvents, err = deps.newStudentAI(ctx, pool, cfg)
		if err != nil || studentAI == nil || studentAIEvents == nil {
			closePool()
			return nil, nil, errors.New("initialize student AI service")
		}
	}
	var studentTeachingService teaching.StudentHTTPService
	if deps.newStudentTeaching != nil {
		studentTeachingService = deps.newStudentTeaching(pool)
	}
	var questionServices qanda.HTTPServices
	if deps.newQuestions != nil {
		questionServices = deps.newQuestions(pool)
	}
	var adminAI aiqa.AdminConfigHTTPService
	if deps.newAdminAI != nil {
		adminAI, err = deps.newAdminAI(ctx, pool, cfg)
		if err != nil {
			closePool()
			return nil, nil, errors.New("initialize AI configuration service")
		}
	}
	var studentAISummaries aiqa.SummaryService
	var adminAIUsage aiqa.AdminUsageService
	if deps.newAIReads != nil {
		studentAISummaries, adminAIUsage = deps.newAIReads(pool)
		if studentAISummaries == nil || adminAIUsage == nil {
			closePool()
			return nil, nil, errors.New("initialize AI read services")
		}
	}
	var notificationService notifications.HTTPService
	if deps.newNotifications != nil {
		notificationService = deps.newNotifications(pool)
	}
	var fileAccessService files.AccessHTTPService
	if deps.newFileAccess != nil {
		fileAccessService, err = deps.newFileAccess(ctx, pool, cfg)
		if err != nil {
			closePool()
			return nil, nil, errors.New("initialize file access service")
		}
	}
	var qaFileAccessService files.QAAccessHTTPService
	if deps.newQAFileAccess != nil {
		qaFileAccessService, err = deps.newQAFileAccess(ctx, pool, cfg)
		if err != nil {
			closePool()
			return nil, nil, errors.New("initialize question file access service")
		}
	}
	var aiFileAccessService files.AIAccessHTTPService
	if deps.newAIFileAccess != nil {
		aiFileAccessService, err = deps.newAIFileAccess(ctx, pool, cfg)
		if err != nil || aiFileAccessService == nil {
			closePool()
			return nil, nil, errors.New("initialize AI file access service")
		}
	}
	var fileBindingService files.BindingHTTPService
	if deps.newFileBindings != nil {
		fileBindingService = deps.newFileBindings(pool)
	}
	var fileCenterService files.FileCenterHTTPService
	if deps.newFileCenter != nil {
		fileCenterService = deps.newFileCenter(pool)
	}
	databaseReady := deps.ready(pool)
	if databaseReady == nil {
		closePool()
		return nil, nil, errors.New("initialize readiness check")
	}
	var objectReady func(context.Context) error
	if deps.objectReady != nil {
		objectReady, err = deps.objectReady(ctx, cfg)
		if err != nil || objectReady == nil {
			closePool()
			return nil, nil, errors.New("initialize readiness check")
		}
	}
	ready := combineReadinessWithTimeout(databaseReady, objectReady, deps.readinessTimeout)
	var operationalGate operationsRuntime
	if deps.newOperations != nil {
		operationalGate = deps.newOperations(pool)
	}
	if deps.requireOperations && operationalGate == nil {
		closePool()
		return nil, nil, errors.New("initialize operations gate")
	}
	var limiter redisx.Limiter
	var captchas redisx.CaptchaService
	var progressLimiter redisx.ProgressWriteLimiter
	var searchLimiter redisx.SearchRateLimiter
	var providerTestLimiter redisx.ProviderTestRateLimiter
	var redisClient *redis.Client
	closeResources := closePool
	if operationalGate != nil {
		closeResources = func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = operationalGate.Close(closeCtx)
			cancel()
			closePool()
		}
	}
	var webhookSender operations.WebhookTestSender
	if deps.newWebhookSender != nil {
		webhookSender, err = deps.newWebhookSender(ctx, cfg)
		if err != nil || webhookSender == nil {
			closeResources()
			return nil, nil, errors.New("initialize alert webhook")
		}
	}
	var adminOperations operations.HTTPService
	if deps.requireOperations &&
		deps.newAdminOperations == nil &&
		deps.newAdminOperationsWithWebhook == nil {
		closeResources()
		return nil, nil, errors.New("initialize operations service")
	}
	if deps.newAdminOperationsWithWebhook != nil {
		if webhookSender == nil {
			closeResources()
			return nil, nil, errors.New("initialize alert webhook")
		}
		adminOperations = deps.newAdminOperationsWithWebhook(
			pool,
			operationalGate,
			webhookSender,
		)
		if adminOperations == nil {
			closeResources()
			return nil, nil, errors.New("initialize operations service")
		}
	} else if deps.newAdminOperations != nil {
		adminOperations = deps.newAdminOperations(pool, operationalGate)
		if adminOperations == nil {
			closeResources()
			return nil, nil, errors.New("initialize operations service")
		}
	}
	var adminBackups backup.HTTPService
	if deps.newAdminBackups != nil {
		adminBackups = deps.newAdminBackups(pool)
		if adminBackups == nil {
			closeResources()
			return nil, nil, errors.New("initialize backup service")
		}
	}
	if deps.openRedis != nil {
		client, err := deps.openRedis(cfg.RedisURL)
		if err != nil {
			closeResources()
			return nil, nil, errors.New("initialize login throttling")
		}
		redisClient = client
		if deps.newThrottle == nil || deps.closeRedis == nil {
			_ = client.Close()
			closeResources()
			return nil, nil, errors.New("initialize login throttling")
		}
		limiter, captchas = deps.newThrottle(client, cfg)
		if deps.newProgressLimiter != nil {
			progressLimiter = deps.newProgressLimiter(client, cfg)
		}
		if deps.newSearchLimiter != nil {
			searchLimiter = deps.newSearchLimiter(client, cfg)
		}
		if deps.newProviderTestLimiter != nil {
			providerTestLimiter = deps.newProviderTestLimiter(client, cfg)
		}
		closeOtherResources := closeResources
		closeResources = func() {
			deps.closeRedis(client)
			closeOtherResources()
		}
	}
	if cleaner, ok := uploadService.(files.ExpiredUploadCleaner); ok && deps.startUploadCleanup != nil {
		stopCleanup := deps.startUploadCleanup(cleaner, operationalGate)
		if stopCleanup == nil {
			closeResources()
			return nil, nil, errors.New("initialize upload cleanup")
		}
		closeOtherResources := closeResources
		closeResources = func() {
			stopCleanup()
			closeOtherResources()
		}
	}
	handler := app.New(app.Dependencies{
		Logger:              deps.logger,
		Ready:               ready,
		Auth:                service,
		Students:            studentService,
		Teaching:            teachingService,
		Uploads:             uploadService,
		QAUploads:           qaUploadService,
		AIUploads:           aiUploadService,
		FileAccess:          fileAccessService,
		QAFileAccess:        qaFileAccessService,
		FileBindings:        fileBindingService,
		FileCenter:          fileCenterService,
		StudentTeaching:     studentTeachingService,
		StudentQuestions:    questionServices.Student,
		AdminQuestions:      questionServices.Admin,
		AdminAI:             adminAI,
		AdminAIUsage:        adminAIUsage,
		StudentAI:           studentAI,
		StudentAIEvents:     studentAIEvents,
		StudentAISummaries:  studentAISummaries,
		Notifications:       notificationService,
		OperationsWriteGate: operationalGate,
		AdminOperations:     adminOperations,
		AdminBackups:        adminBackups,
		AIFileAccess:        aiFileAccessService,
		PublicOrigin:        cfg.PublicOrigin,
		CookieSecure:        cfg.CookieSecure,
		TrustedProxyCIDRs:   cfg.TrustedProxyCIDRs,
		Limiter:             limiter,
		ProgressLimiter:     progressLimiter,
		SearchLimiter:       searchLimiter,
		ProviderTestLimiter: providerTestLimiter,
		Captchas:            captchas,
		StaticFiles:         os.DirFS("web/dist"),
	})
	if deps.startOutbox != nil {
		stopOutbox := deps.startOutbox(pool, operationalGate)
		if stopOutbox == nil {
			closeResources()
			return nil, nil, errors.New("initialize notification delivery")
		}
		closeOtherResources := closeResources
		closeResources = func() {
			stopOutbox()
			closeOtherResources()
		}
	}
	if deps.startAIRunner != nil {
		stopAIRunner, startErr := deps.startAIRunner(ctx, pool, cfg, operationalGate)
		if startErr != nil || stopAIRunner == nil {
			closeResources()
			return nil, nil, errors.New("initialize AI runner")
		}
		closeOtherResources := closeResources
		closeResources = func() {
			stopAIRunner()
			closeOtherResources()
		}
	}
	if deps.startWebhookRunner != nil {
		if webhookSender == nil {
			closeResources()
			return nil, nil, errors.New("initialize alert webhook")
		}
		stopWebhookRunner, startErr := deps.startWebhookRunner(
			pool,
			webhookSender,
		)
		if startErr != nil || stopWebhookRunner == nil {
			closeResources()
			return nil, nil, errors.New("initialize alert webhook")
		}
		closeOtherResources := closeResources
		closeResources = func() {
			stopWebhookRunner()
			closeOtherResources()
		}
	}
	if deps.startAlertRunnerWithWebhook != nil ||
		deps.startAlertRunner != nil {
		var stopAlertRunner func()
		if deps.startAlertRunnerWithWebhook != nil {
			stopAlertRunner = deps.startAlertRunnerWithWebhook(
				pool,
				webhookSender != nil && webhookSender.Enabled(),
			)
		} else {
			stopAlertRunner = deps.startAlertRunner(pool)
		}
		if stopAlertRunner == nil {
			closeResources()
			return nil, nil, errors.New("initialize alert evaluator")
		}
		closeOtherResources := closeResources
		closeResources = func() {
			stopAlertRunner()
			closeOtherResources()
		}
	}
	if deps.startRetention != nil {
		stopRetention, startErr := deps.startRetention(pool)
		if startErr != nil || stopRetention == nil {
			closeResources()
			return nil, nil, errors.New("initialize operations retention")
		}
		closeOtherResources := closeResources
		closeResources = func() {
			stopRetention()
			closeOtherResources()
		}
	}
	var internalHandler http.Handler
	if deps.newInternal != nil {
		internalHandler, err = deps.newInternal(pool, redisClient, cfg)
		if err != nil || internalHandler == nil {
			closeResources()
			return nil, nil, errors.New("initialize internal listener")
		}
		internalHandler = httpx.RequestID(
			httpx.SafeRequestLog(deps.logger, time.Now)(
				httpx.SafeRecoverer(deps.logger)(internalHandler),
			),
		)
	}
	if deps.recordInfrastructure != nil {
		writer, ok := operationalGate.(operations.InfrastructureStatusWriter)
		if !ok {
			closeResources()
			return nil, nil, errors.New("initialize infrastructure status")
		}
		webhookEnabled := webhookSender != nil && webhookSender.Enabled()
		if err := deps.recordInfrastructure(
			ctx,
			writer,
			cfg,
			webhookEnabled,
		); err != nil {
			closeResources()
			return nil, nil, errors.New("initialize infrastructure status")
		}
	}
	return &applicationRuntime{
		Public:   publicOnlyHandlerWithLog(handler, deps.logger),
		Internal: internalHandler,
	}, closeResources, nil
}

func publicOnlyHandler(next http.Handler) http.Handler {
	return publicOnlyHandlerWithLog(next, safelog.Logger{})
}

func publicOnlyHandlerWithLog(
	next http.Handler,
	logger safelog.Logger,
) http.Handler {
	denied := httpx.RequestID(
		httpx.SafeRequestLog(logger, time.Now)(
			httpx.SafeRecoverer(logger)(http.NotFoundHandler()),
		),
	)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal" ||
			strings.HasPrefix(r.URL.Path, "/internal/") {
			denied.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const defaultReadinessTimeout = 5 * time.Second

func combineReadiness(databaseReady, objectReady func(context.Context) error) func(context.Context) error {
	return combineReadinessWithTimeout(databaseReady, objectReady, defaultReadinessTimeout)
}

func combineReadinessWithTimeout(databaseReady, objectReady func(context.Context) error, timeout time.Duration) func(context.Context) error {
	if timeout <= 0 {
		timeout = defaultReadinessTimeout
	}
	return func(ctx context.Context) error {
		checkCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if databaseReady == nil {
			return errors.New("dependency not ready")
		}
		if err := databaseReady(checkCtx); err != nil {
			return errors.New("dependency not ready")
		}
		if objectReady != nil {
			if err := objectReady(checkCtx); err != nil {
				return errors.New("dependency not ready")
			}
		}
		return nil
	}
}

func newProductionObjectReadiness(ctx context.Context, cfg config.Config) (func(context.Context) error, error) {
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
		SkipLifecycleBootstrap: cfg.Environment == "development" || cfg.SkipObjectStoreLifecycleBootstrap,
	})
	if err != nil {
		return nil, err
	}
	return stores.Ready, nil
}

func newProductionAuthService(pool *pgxpool.Pool) (auth.HTTPService, error) {
	users := auth.NewPostgresUserStore(pool)
	sessions := auth.NewPostgresSessionStore(pool)
	hasher := auth.NewPasswordHasher(auth.Argon2Params{
		MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32,
	})
	service, err := auth.NewService(auth.ServiceConfig{
		Users: users, Sessions: sessions, LoginEvents: sessions, PasswordRotations: sessions, Hasher: hasher,
	})
	if err != nil {
		return nil, errors.New("initialize authentication service")
	}
	return service, nil
}

func newProductionTeachingService(pool *pgxpool.Pool) teaching.AdminHTTPService {
	return teaching.NewService(teaching.NewPostgresStore(pool), files.NewReadinessChecker(), time.Now)
}

func newProductionAdminAIService(_ context.Context, pool *pgxpool.Pool, cfg config.Config) (aiqa.AdminConfigHTTPService, error) {
	box, err := aiqa.NewAESGCMSecretBox(cfg.AIMasterKey, cfg.AIMasterKeyVersion, rand.Reader)
	if err != nil {
		return nil, errors.New("initialize AI secret box")
	}
	policy := aiqa.URLPolicy{DevelopmentAllowPrivate: cfg.AIAllowPrivateProvider}
	store := aiqa.NewPostgresConfigStoreWithSecurity(pool, box, policy)
	return aiqa.NewAdminConfigServiceWithConnectivity(store, policy, box, aiqa.NewProviderConnectivityTester(policy)), nil
}

func newProductionAIReadServices(pool *pgxpool.Pool) (aiqa.SummaryService, aiqa.AdminUsageService) {
	return aiqa.NewSummaryService(aiqa.NewPostgresSummaryStore(pool)), aiqa.NewPostgresAdminUsageService(pool)
}

func newProductionUploadService(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) (files.UploadHTTPService, error) {
	return newProductionUploadServiceWithPolicy(ctx, pool, cfg, files.TeachingUploadPolicy{})
}

func newProductionQAUploadService(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) (files.UploadHTTPService, error) {
	return newProductionUploadServiceWithPolicy(ctx, pool, cfg, files.QAUploadPolicy{})
}

func newProductionAIUploadService(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) (files.UploadHTTPService, error) {
	return newProductionUploadServiceWithPolicy(ctx, pool, cfg, files.AIUploadPolicy{})
}

func newProductionUploadServiceWithPolicy(ctx context.Context, pool *pgxpool.Pool, cfg config.Config, policy files.UploadPolicy) (files.UploadHTTPService, error) {
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
		SkipLifecycleBootstrap: cfg.Environment == "development" || cfg.SkipObjectStoreLifecycleBootstrap,
	})
	if err != nil {
		return nil, err
	}
	return files.NewUploadService(files.NewPostgresStore(pool), stores.Originals, policy, time.Now), nil
}

func newProductionFileAccessService(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) (files.AccessHTTPService, error) {
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
		SkipLifecycleBootstrap: cfg.Environment == "development" || cfg.SkipObjectStoreLifecycleBootstrap,
	})
	if err != nil {
		return nil, err
	}
	store := files.NewPostgresStore(pool)
	return files.NewAccessService(store, stores.Originals, stores.Previews), nil
}

func newProductionQAFileAccessService(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) (files.QAAccessHTTPService, error) {
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
		SkipLifecycleBootstrap: cfg.Environment == "development" || cfg.SkipObjectStoreLifecycleBootstrap,
	})
	if err != nil {
		return nil, err
	}
	return files.NewQAAccessService(files.NewPostgresStore(pool), stores.Originals, stores.Previews), nil
}

func newProductionAIFileAccessService(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) (files.AIAccessHTTPService, error) {
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
		SkipLifecycleBootstrap: cfg.Environment == "development" || cfg.SkipObjectStoreLifecycleBootstrap,
	})
	if err != nil {
		return nil, err
	}
	return files.NewAIAccessService(files.NewPostgresStore(pool), stores.Originals, stores.Previews, audit.NewPostgresWriter(pool)), nil
}

func newProductionStudentAIService(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) (aiqa.StudentService, aiqa.StudentEventStore, error) {
	box, err := aiqa.NewAESGCMSecretBox(cfg.AIMasterKey, cfg.AIMasterKeyVersion, rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
		SkipLifecycleBootstrap: cfg.Environment == "development" || cfg.SkipObjectStoreLifecycleBootstrap,
	})
	if err != nil {
		return nil, nil, err
	}
	policy := aiqa.URLPolicy{DevelopmentAllowPrivate: cfg.AIAllowPrivateProvider}
	runtimeStore := aiqa.NewPostgresRuntimeStore(pool)
	configStore := aiqa.NewPostgresConfigStoreWithSecurity(pool, box, policy)
	attachments := aiqa.NewPostgresAttachmentStore(pool, stores.Originals, stores.Previews)
	return aiqa.NewStudentService(runtimeStore, configStore, attachments, time.Now), runtimeStore, nil
}

func newProductionFileBindingService(pool *pgxpool.Pool) files.BindingHTTPService {
	return files.NewBindingService(files.NewPostgresStore(pool))
}
func newProductionFileCenterService(pool *pgxpool.Pool) files.FileCenterHTTPService {
	return files.NewFileCenterService(files.NewPostgresStore(pool), time.Now)
}
func newProductionStudentTeachingService(pool *pgxpool.Pool) teaching.StudentHTTPService {
	return teaching.NewStudentService(teaching.NewPostgresStore(pool), time.Now)
}

func newProductionQuestionServices(pool *pgxpool.Pool) qanda.HTTPServices {
	store := qanda.NewPostgresStore(pool)
	uow := qanda.NewPostgresUnitOfWork(pool, func(tx pgx.Tx) qanda.NotificationWriter {
		return notifications.NewWriter(tx)
	})
	service := qanda.NewService(store, uow, time.Now)
	return qanda.HTTPServices{Student: service, Admin: service}
}

func newProductionNotificationService(pool *pgxpool.Pool) notifications.HTTPService {
	return notifications.NewService(notifications.NewPostgresStore(pool))
}

func newProductionAdminOperationsService(
	pool *pgxpool.Pool,
	runtime operationsRuntime,
) operations.HTTPService {
	webhook, err := operations.NewWebhookSender(
		context.Background(),
		operations.WebhookSenderConfig{},
	)
	if err != nil {
		return nil
	}
	return newProductionAdminOperationsServiceWithWebhook(
		pool,
		runtime,
		webhook,
	)
}

func newProductionAdminOperationsServiceWithWebhook(
	pool *pgxpool.Pool,
	runtime operationsRuntime,
	webhook operations.WebhookTestSender,
) operations.HTTPService {
	if pool == nil || runtime == nil {
		return nil
	}
	if webhook == nil {
		return nil
	}
	store, ok := runtime.(operations.ServiceStore)
	if !ok {
		return nil
	}
	dashboardStore := operations.NewPostgresDashboardReader(pool)
	sampleStore, err := operations.NewPostgresSampleDashboardReader(
		pool,
		operations.DashboardSampleFreshFor,
	)
	if err != nil {
		return nil
	}
	dashboard, err := operations.NewDashboardAssembler(
		time.Now,
		operations.DashboardSampleFreshFor,
		operations.DashboardDependencies{
			ReleaseVersion: runtimeBuildInfo.Version,
			Students:       dashboardStore, Questions: dashboardStore,
			AI: dashboardStore, Storage: sampleStore,
			Services: sampleStore, Queues: dashboardStore,
			Backup: dashboardStore, Alerts: dashboardStore,
			Audit: dashboardStore,
		},
	)
	if err != nil {
		return nil
	}
	service, err := operations.NewServiceWithDashboardAlertsAndWebhook(
		store,
		audit.NewPostgresWriter(pool),
		dashboard,
		operations.NewPostgresAlertStore(pool),
		webhook,
	)
	if err != nil {
		return nil
	}
	return service
}

func newProductionAlertRunner(pool *pgxpool.Pool) func() {
	return newProductionAlertRunnerWithWebhook(pool, false)
}

func newProductionAlertRunnerWithWebhook(
	pool *pgxpool.Pool,
	webhookEnabled bool,
) func() {
	return newProductionAlertRunnerWithWebhookAndLog(
		pool,
		webhookEnabled,
		nil,
	)
}

func newProductionAlertRunnerWithWebhookAndLog(
	pool *pgxpool.Pool,
	webhookEnabled bool,
	logCategory func(string),
	objectStore ...operations.ObjectStoreCapacityReader,
) func() {
	store := operations.NewPostgresAlertStore(pool, objectStore...)
	if webhookEnabled {
		var err error
		store, err = operations.NewPostgresAlertStoreWithWebhookOutbox(
			pool,
			time.Now,
			uuid.New,
			objectStore...,
		)
		if err != nil {
			return nil
		}
	}
	return operations.StartAlertRunner(operations.AlertRunner{
		Store:             store,
		Clock:             time.Now,
		PollInterval:      operations.DefaultAlertRunnerInterval,
		EvaluationTimeout: 30 * time.Second,
		LogCategory:       logCategory,
	})
}

func newProductionWebhookSender(
	ctx context.Context,
	cfg config.Config,
) (operations.WebhookTestSender, error) {
	return operations.NewWebhookSender(ctx, operations.WebhookSenderConfig{
		URL:                     cfg.WebhookURL,
		Authorization:           cfg.WebhookAuthorization,
		DevelopmentAllowPrivate: cfg.Environment == "development",
	})
}

func newProductionWebhookRunner(
	pool *pgxpool.Pool,
	sender operations.WebhookTestSender,
) (func(), error) {
	return newProductionWebhookRunnerWithLog(pool, sender, nil)
}

func newProductionWebhookRunnerWithLog(
	pool *pgxpool.Pool,
	sender operations.WebhookTestSender,
	logCategory func(string),
) (func(), error) {
	if pool == nil || sender == nil {
		return nil, operations.ErrInvalid
	}
	if !sender.Enabled() {
		return func() {}, nil
	}
	return operations.StartWebhookDeliveryRunner(
		operations.WebhookDeliveryRunner{
			Store:           operations.NewPostgresWebhookDeliveryStore(pool),
			Sender:          sender,
			Clock:           time.Now,
			NewUUID:         uuid.New,
			ClaimOwner:      "server:" + uuid.NewString(),
			PollInterval:    operations.DefaultWebhookPollInterval,
			LeaseDuration:   operations.DefaultWebhookLeaseDuration,
			DeliveryTimeout: 15 * time.Second,
			LogCategory:     logCategory,
		},
	)
}

func newProductionRetentionSchedulerWithLog(
	pool *pgxpool.Pool,
	logCategory func(string),
) (func(), error) {
	if pool == nil {
		return nil, operations.ErrInvalid
	}
	settings := operations.NewPostgresStore(pool)
	stop, err := startProductionRetentionScheduler(
		settings,
		operations.NewPostgresRetentionRunner(pool),
		time.Now,
		logCategory,
	)
	if err != nil {
		closeCtx, cancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		_ = settings.Close(closeCtx)
		cancel()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			stop()
			closeCtx, cancel := context.WithTimeout(
				context.Background(),
				2*time.Second,
			)
			_ = settings.Close(closeCtx)
			cancel()
		})
	}, nil
}

func startProductionRetentionScheduler(
	settings operations.SettingsStore,
	runner operations.RetentionRunner,
	clock func() time.Time,
	logCategory func(string),
) (func(), error) {
	return operations.StartRetentionScheduler(operations.RetentionScheduler{
		Settings:    settings,
		Runner:      runner,
		Clock:       clock,
		Interval:    operations.DefaultRetentionInterval,
		RunTimeout:  operations.DefaultRetentionRunTimeout,
		LogCategory: logCategory,
	})
}

func newProductionInternalHandler(
	pool *pgxpool.Pool,
	client *redis.Client,
	cfg config.Config,
) (http.Handler, error) {
	if cfg.Environment == "development" &&
		cfg.MetricsBearerSecret == "" &&
		len(cfg.HostMetricsHMACSecret) == 0 {
		return http.NotFoundHandler(), nil
	}
	if cfg.MetricsBearerSecret == "" || len(cfg.HostMetricsHMACSecret) == 0 {
		return nil, errors.New("internal secrets unavailable")
	}
	if pool == nil || client == nil {
		return nil, errors.New("internal dependencies unavailable")
	}
	nonces, err := operations.NewRedisHostNonceStore(client)
	if err != nil {
		return nil, errors.New("initialize internal nonce store")
	}
	samples := operations.NewPostgresSampleStore(pool)
	handler, err := operations.NewInternalHandler(operations.InternalHTTPConfig{
		MetricsBearerSecret:   cfg.MetricsBearerSecret,
		HostMetricsHMACSecret: cfg.HostMetricsHMACSecret,
		Clock:                 time.Now,
		Metrics:               samples,
		Samples:               samples,
		Nonces:                nonces,
		Release:               runtimeBuildInfo,
		SchemaVersion: func(ctx context.Context) (int64, error) {
			var version int64
			err := pool.QueryRow(ctx, `SELECT COALESCE(max(version_id),0) FROM goose_db_version WHERE is_applied`).Scan(&version)
			return version, err
		},
	})
	if err != nil {
		return nil, errors.New("initialize internal handler")
	}
	return handler, nil
}

func loadBuildInfo(environment string) (buildinfo.Info, error) {
	if environment != "production" {
		return buildinfo.Development(), nil
	}
	return buildinfo.Parse(buildVersion, buildCommit, buildTime, buildMinSchema, buildMaxSchema)
}

func newProductionAdminBackupService(pool *pgxpool.Pool) backup.HTTPService {
	return backup.NewService(
		backup.NewPostgresStore(pool),
		time.Now,
	)
}

func newProductionOutboxRunner(pool *pgxpool.Pool, gate operations.ClaimGate) func() {
	return newProductionOutboxRunnerWithLog(pool, gate, nil)
}

func newProductionOutboxRunnerWithLog(
	pool *pgxpool.Pool,
	gate operations.ClaimGate,
	logCategory func(string),
) func() {
	return notifications.StartOutboxRunner(notifications.Runner{
		Store: notifications.NewPostgresOutboxStore(pool), Owner: uuid.NewString(),
		PollInterval: time.Second, BatchTimeout: 10 * time.Second, ShutdownTimeout: 2 * time.Second,
		ClaimGate: gate, LogCategory: logCategory,
	})
}

func newProductionAIRunner(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Config,
	gate operations.ClaimGate,
) (func(), error) {
	return newProductionAIRunnerWithLog(ctx, pool, cfg, gate, nil)
}

func newProductionAIRunnerWithLog(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Config,
	gate operations.ClaimGate,
	logCategory func(string),
) (func(), error) {
	box, err := aiqa.NewAESGCMSecretBox(cfg.AIMasterKey, cfg.AIMasterKeyVersion, rand.Reader)
	if err != nil {
		return nil, err
	}
	policy := aiqa.URLPolicy{DevelopmentAllowPrivate: cfg.AIAllowPrivateProvider}
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
		SkipLifecycleBootstrap: cfg.Environment == "development" || cfg.SkipObjectStoreLifecycleBootstrap,
	})
	if err != nil {
		return nil, err
	}
	attachments := aiqa.NewPostgresAttachmentStore(pool, stores.Originals, stores.Previews)
	return aiqa.StartRunner(aiqa.Runner{
		Store: aiqa.NewPostgresRunnerStore(pool, box, attachments), Gateway: newProductionAIGateway(policy),
		Owner: uuid.NewString(), GlobalConcurrency: cfg.AIGlobalConcurrency,
		PollInterval: time.Second, LeaseDuration: 30 * time.Second,
		FlushInterval: 250 * time.Millisecond, FlushBytes: 4 << 10,
		ClaimGate: gate, LogCategory: logCategory,
	}), nil
}

func newProductionAIGateway(policy aiqa.URLPolicy) aiqa.Gateway {
	return aiqa.NewSafeGateway(policy)
}

func newProductionStudentService(pool *pgxpool.Pool) students.HTTPService {
	users := auth.NewPostgresUserStore(pool)
	hasher := auth.NewPasswordHasher(auth.Argon2Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32})
	return students.NewService(users, students.NewPostgresUnitOfWork(pool), hasher, nil)
}
