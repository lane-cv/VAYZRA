package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/app"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/database"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Print("configuration_error")
		os.Exit(1)
	}

	handler, closeResources, err := buildProductionApplication(context.Background(), cfg)
	if err != nil {
		log.Print("startup_error")
		os.Exit(1)
	}
	defer closeResources()
	server := newServer(cfg.ListenAddress, handler)

	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Print("server_start_error")
			os.Exit(1)
		}
	case <-signals.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Print("server_shutdown_error")
			os.Exit(1)
		}
	}
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
	open    func(context.Context, string) (*pgxpool.Pool, error)
	migrate func(context.Context, *pgxpool.Pool) error
	newAuth func(*pgxpool.Pool) (auth.HTTPService, error)
	ready   func(*pgxpool.Pool) func(context.Context) error
	close   func(*pgxpool.Pool)
}

func buildProductionApplication(ctx context.Context, cfg config.Config) (http.Handler, func(), error) {
	return buildApplication(ctx, cfg, applicationDependencies{
		open:    database.Open,
		migrate: database.Migrate,
		newAuth: newProductionAuthService,
		ready: func(pool *pgxpool.Pool) func(context.Context) error {
			return pool.Ping
		},
		close: func(pool *pgxpool.Pool) { pool.Close() },
	})
}

func buildApplication(ctx context.Context, cfg config.Config, deps applicationDependencies) (http.Handler, func(), error) {
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
	ready := deps.ready(pool)
	if ready == nil {
		closePool()
		return nil, nil, errors.New("initialize readiness check")
	}
	return app.New(app.Dependencies{
		Ready:        ready,
		Auth:         service,
		PublicOrigin: cfg.PublicOrigin,
		CookieSecure: cfg.CookieSecure,
	}), closePool, nil
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
