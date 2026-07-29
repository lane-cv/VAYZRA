package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/internal/platform/redisx"
	"happylearn.local/app/internal/qanda"
	"happylearn.local/app/internal/students"
	"happylearn.local/app/internal/teaching"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Print("configuration_error")
		os.Exit(1)
	}

	runtime, closeResources, err := buildProductionApplication(context.Background(), cfg)
	if err != nil {
		log.Printf("startup_error stage=%s", err)
		os.Exit(1)
	}
	publicServer := newServer(cfg.ListenAddress, runtime.Public)
	internalServer := newServer(cfg.InternalListenAddress, runtime.Internal)

	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runServerLifecycles(
		signals,
		[]serverLifecycle{publicServer, internalServer},
		closeResources,
	); err != nil {
		log.Printf("server_error stage=%s", err)
		os.Exit(1)
	}
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
	if len(servers) == 0 || closeResources == nil {
		return errors.New("server configuration")
	}
	for _, server := range servers {
		if server == nil {
			closeResources()
			return errors.New("server configuration")
		}
	}
	errCh := make(chan error, len(servers))
	for _, server := range servers {
		go func(server serverLifecycle) {
			errCh <- server.ListenAndServe()
		}(server)
	}
	var result error
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			result = errors.New("server start")
		}
	case <-signals.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownFailed := false
	forceCloseFailed := false
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			shutdownFailed = true
			if closeErr := server.Close(); closeErr != nil {
				forceCloseFailed = true
			}
		}
	}
	closeResources()
	if forceCloseFailed {
		return errors.New("server force close")
	}
	if shutdownFailed {
		return errors.New("server shutdown")
	}
	return result
}

func newServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

type applicationDependencies struct {
	open                   func(context.Context, string) (*pgxpool.Pool, error)
	migrate                func(context.Context, *pgxpool.Pool) error
	newAuth                func(*pgxpool.Pool) (auth.HTTPService, error)
	newStudents            func(*pgxpool.Pool) students.HTTPService
	newTeaching            func(*pgxpool.Pool) teaching.AdminHTTPService
	newUploads             func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error)
	newQAUploads           func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error)
	newAIUploads           func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error)
	newStudentAI           func(context.Context, *pgxpool.Pool, config.Config) (aiqa.StudentService, aiqa.StudentEventStore, error)
	newFileAccess          func(context.Context, *pgxpool.Pool, config.Config) (files.AccessHTTPService, error)
	newQAFileAccess        func(context.Context, *pgxpool.Pool, config.Config) (files.QAAccessHTTPService, error)
	newAIFileAccess        func(context.Context, *pgxpool.Pool, config.Config) (files.AIAccessHTTPService, error)
	newFileBindings        func(*pgxpool.Pool) files.BindingHTTPService
	newFileCenter          func(*pgxpool.Pool) files.FileCenterHTTPService
	startUploadCleanup     func(files.ExpiredUploadCleaner, operations.WriteGate) func()
	newStudentTeaching     func(*pgxpool.Pool) teaching.StudentHTTPService
	newQuestions           func(*pgxpool.Pool) qanda.HTTPServices
	newAdminAI             func(context.Context, *pgxpool.Pool, config.Config) (aiqa.AdminConfigHTTPService, error)
	newAIReads             func(*pgxpool.Pool) (aiqa.SummaryService, aiqa.AdminUsageService)
	newNotifications       func(*pgxpool.Pool) notifications.HTTPService
	startOutbox            func(*pgxpool.Pool, operations.ClaimGate) func()
	startAIRunner          func(context.Context, *pgxpool.Pool, config.Config, operations.ClaimGate) (func(), error)
	newOperations          func(*pgxpool.Pool) operationsRuntime
	newAdminOperations     func(*pgxpool.Pool, operationsRuntime) operations.HTTPService
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
}

type operationsRuntime interface {
	operations.WriteGate
	operations.ClaimGate
	operations.LeaseSessionCloser
}

type applicationRuntime struct {
	Public   http.Handler
	Internal http.Handler
}

func buildProductionApplication(
	ctx context.Context,
	cfg config.Config,
) (*applicationRuntime, func(), error) {
	return buildApplicationRuntime(ctx, cfg, applicationDependencies{
		open:               database.Open,
		migrate:            database.Migrate,
		newAuth:            newProductionAuthService,
		newStudents:        newProductionStudentService,
		newTeaching:        newProductionTeachingService,
		newUploads:         newProductionUploadService,
		newQAUploads:       newProductionQAUploadService,
		newAIUploads:       newProductionAIUploadService,
		newStudentAI:       newProductionStudentAIService,
		newFileAccess:      newProductionFileAccessService,
		newQAFileAccess:    newProductionQAFileAccessService,
		newAIFileAccess:    newProductionAIFileAccessService,
		newFileBindings:    newProductionFileBindingService,
		newFileCenter:      newProductionFileCenterService,
		startUploadCleanup: files.StartCleanupRunner,
		newStudentTeaching: newProductionStudentTeachingService,
		newQuestions:       newProductionQuestionServices,
		newAdminAI:         newProductionAdminAIService,
		newAIReads:         newProductionAIReadServices,
		newNotifications:   newProductionNotificationService,
		startOutbox:        newProductionOutboxRunner,
		startAIRunner:      newProductionAIRunner,
		newOperations: func(pool *pgxpool.Pool) operationsRuntime {
			return operations.NewPostgresStore(pool)
		},
		newAdminOperations: newProductionAdminOperationsService,
		newAdminBackups:    newProductionAdminBackupService,
		requireOperations:  true,
		ready: func(pool *pgxpool.Pool) func(context.Context) error {
			return pool.Ping
		},
		objectReady: newProductionObjectReadiness,
		close:       func(pool *pgxpool.Pool) { pool.Close() },
		openRedis:   redisx.NewClient,
		newThrottle: func(client *redis.Client, cfg config.Config) (redisx.Limiter, redisx.CaptchaService) {
			policy := redisx.Policy{Secret: []byte(cfg.LoginThrottleSecret), Window: 15 * time.Minute, AccountFailures: 5, IPFailures: 20, Lockout: 15 * time.Minute}
			return redisx.NewLoginLimiter(client, policy), redisx.NewCaptchaStore(client, []byte(cfg.LoginThrottleSecret))
		},
		newProgressLimiter: func(client *redis.Client, cfg config.Config) redisx.ProgressWriteLimiter {
			return redisx.NewProgressWriteLimiter(client, redisx.ProgressLimitPolicy{Secret: []byte(cfg.LoginThrottleSecret), Window: time.Minute, SessionMaxWrites: 60, AccountMaxWrites: 120})
		},
		newSearchLimiter: func(client *redis.Client, cfg config.Config) redisx.SearchRateLimiter {
			return redisx.NewSearchLimiter(client, redisx.ResourceLimitPolicy{Secret: []byte(cfg.LoginThrottleSecret), Window: time.Minute, MaxRequests: 30})
		},
		newProviderTestLimiter: func(client *redis.Client, cfg config.Config) redisx.ProviderTestRateLimiter {
			return redisx.NewProviderTestLimiter(client, redisx.ResourceLimitPolicy{Secret: []byte(cfg.LoginThrottleSecret), Window: time.Minute, MaxRequests: 5})
		},
		closeRedis:  func(client *redis.Client) { _ = client.Close() },
		newInternal: newProductionInternalHandler,
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
	var adminOperations operations.HTTPService
	if deps.requireOperations && deps.newAdminOperations == nil {
		closeResources()
		return nil, nil, errors.New("initialize operations service")
	}
	if deps.newAdminOperations != nil {
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
	var internalHandler http.Handler
	if deps.newInternal != nil {
		internalHandler, err = deps.newInternal(pool, redisClient, cfg)
		if err != nil || internalHandler == nil {
			closeResources()
			return nil, nil, errors.New("initialize internal listener")
		}
	}
	return &applicationRuntime{
		Public:   publicOnlyHandler(handler),
		Internal: internalHandler,
	}, closeResources, nil
}

func publicOnlyHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal" ||
			strings.HasPrefix(r.URL.Path, "/internal/") {
			http.NotFound(w, r)
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
		SkipLifecycleBootstrap: cfg.Environment == "development",
	})
	if err != nil {
		log.Printf("object_store_startup_error stage=%s", err)
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
		SkipLifecycleBootstrap: cfg.Environment == "development",
	})
	if err != nil {
		log.Printf("object_store_startup_error stage=%s", err)
		return nil, err
	}
	return files.NewUploadService(files.NewPostgresStore(pool), stores.Originals, policy, time.Now), nil
}

func newProductionFileAccessService(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) (files.AccessHTTPService, error) {
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
		SkipLifecycleBootstrap: cfg.Environment == "development",
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
		SkipLifecycleBootstrap: cfg.Environment == "development",
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
		SkipLifecycleBootstrap: cfg.Environment == "development",
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
		SkipLifecycleBootstrap: cfg.Environment == "development",
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
	if pool == nil || runtime == nil {
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
			Students: dashboardStore, Questions: dashboardStore,
			AI: dashboardStore, Storage: sampleStore,
			Services: sampleStore, Queues: dashboardStore,
			Backup: dashboardStore, Alerts: dashboardStore,
			Audit: dashboardStore,
		},
	)
	if err != nil {
		return nil
	}
	service, err := operations.NewServiceWithDashboard(
		store,
		audit.NewPostgresWriter(pool),
		dashboard,
	)
	if err != nil {
		return nil
	}
	return service
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
	})
	if err != nil {
		return nil, errors.New("initialize internal handler")
	}
	return handler, nil
}

func newProductionAdminBackupService(pool *pgxpool.Pool) backup.HTTPService {
	return backup.NewService(
		backup.NewPostgresStore(pool),
		time.Now,
	)
}

func newProductionOutboxRunner(pool *pgxpool.Pool, gate operations.ClaimGate) func() {
	return notifications.StartOutboxRunner(notifications.Runner{
		Store: notifications.NewPostgresOutboxStore(pool), Owner: uuid.NewString(),
		PollInterval: time.Second, BatchTimeout: 10 * time.Second, ShutdownTimeout: 2 * time.Second,
		ClaimGate: gate,
	})
}

func newProductionAIRunner(
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg config.Config,
	gate operations.ClaimGate,
) (func(), error) {
	box, err := aiqa.NewAESGCMSecretBox(cfg.AIMasterKey, cfg.AIMasterKeyVersion, rand.Reader)
	if err != nil {
		return nil, err
	}
	policy := aiqa.URLPolicy{DevelopmentAllowPrivate: cfg.AIAllowPrivateProvider}
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
		SkipLifecycleBootstrap: cfg.Environment == "development",
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
		ClaimGate: gate,
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
