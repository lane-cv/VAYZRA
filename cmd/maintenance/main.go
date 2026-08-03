package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/internal/platform/safelog"
)

type cleanupFunc func(context.Context, int) error

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := runMaintenanceWithLog(
		ctx,
		os.Args[1:],
		os.Getenv,
		os.Stderr,
		time.Now,
	); err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	return runMaintenanceWithLog(
		ctx,
		args,
		os.Getenv,
		io.Discard,
		time.Now,
	)
}

func runMaintenanceWithLog(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	output io.Writer,
	clock func() time.Time,
) error {
	bootstrapLogger, err := safelog.New(output, clock)
	if err != nil {
		return errors.New("maintenance logging")
	}
	bootstrapLogger.Info("maintenance.configuration", safelog.Field{
		Name:  "stage",
		Value: "start",
	})
	cfg, err := config.Load(getenv)
	if err != nil {
		logMaintenanceResult(bootstrapLogger, "configuration", err)
		return errors.New("maintenance configuration")
	}
	logger, err := safelog.NewFromConfig(output, clock, cfg)
	if err != nil {
		logMaintenanceResult(bootstrapLogger, "logging", err)
		return errors.New("maintenance logging")
	}
	logger.Info("maintenance.configuration", safelog.Field{
		Name:  "stage",
		Value: "success",
	})
	stage := maintenanceCommandStage(args)
	logger.Info("maintenance.start", safelog.Field{
		Name:  "stage",
		Value: stage,
	})
	err = runConfiguredMaintenance(ctx, args, cfg)
	logMaintenanceResult(logger, stage, err)
	return err
}

func runConfiguredMaintenance(
	ctx context.Context,
	args []string,
	cfg config.Config,
) error {
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
		SkipLifecycleBootstrap: cfg.Environment == "development" || cfg.SkipObjectStoreLifecycleBootstrap,
	})
	if err != nil {
		return errors.New("maintenance object storage")
	}
	service := files.NewFileCleanupService(files.NewPostgresStore(pool), stores.Originals, stores.Previews, time.Now)
	return runCommand(ctx, args, service.Cleanup)
}

func maintenanceCommandStage(args []string) string {
	if len(args) > 0 && args[0] == "cleanup-files" {
		return "cleanup_files"
	}
	return "arguments"
}

func logMaintenanceResult(logger safelog.Logger, stage string, err error) {
	event := "maintenance.success"
	if err != nil {
		event = "maintenance.failure"
	}
	fields := []safelog.Field{{
		Name:  "stage",
		Value: stage,
	}}
	if err != nil {
		logger.Error(event, fields...)
		return
	}
	logger.Info(event, fields...)
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
