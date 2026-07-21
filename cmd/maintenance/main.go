package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
)

type cleanupFunc func(context.Context, int) error

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Print("maintenance_error")
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return errors.New("maintenance configuration")
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return errors.New("maintenance database")
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		return errors.New("maintenance migration")
	}
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: cfg.MinIOEndpoint, AccessKey: cfg.MinIOAccessKey, SecretKey: cfg.MinIOSecretKey, UseTLS: cfg.MinIOUseTLS,
		OriginalsBucket: cfg.MinIOOriginalsBucket, PreviewsBucket: cfg.MinIOPreviewsBucket,
	})
	if err != nil {
		return errors.New("maintenance object storage")
	}
	service := files.NewFileCleanupService(files.NewPostgresStore(pool), stores.Originals, stores.Previews, time.Now)
	return runCommand(ctx, args, service.Cleanup)
}

func runCommand(ctx context.Context, args []string, cleanup cleanupFunc) error {
	if len(args) == 0 || args[0] != "cleanup-files" || cleanup == nil {
		return errors.New("invalid maintenance command")
	}
	flags := flag.NewFlagSet("cleanup-files", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	limit := flags.Int("limit", 100, "maximum file versions to clean")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *limit < 1 || *limit > 1000 {
		return errors.New("invalid maintenance command")
	}
	return cleanup(ctx, *limit)
}
