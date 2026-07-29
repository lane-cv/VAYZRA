package main

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
)

const (
	restoreCheckReportFile      = "/work/restore-check.report"
	restoreCheckPassfile        = "/run/secrets/pgpass"
	restoreCheckOriginalsBucket = "happylearn-originals"
	restoreCheckPreviewsBucket  = "happylearn-previews"
)

type restoreCheckInput struct {
	backupID   uuid.UUID
	reportFile string
}

type restoreCheckConfig struct {
	databaseHost    string
	databasePort    string
	databaseUser    string
	databaseName    string
	databaseSSLMode string
	passfile        string
	minioEndpoint   string
	minioAccessKey  string
	minioSecretKey  string
	originalsBucket string
	previewsBucket  string
	useTLS          bool
}

type programFactories struct {
	newActions func(
		context.Context,
		func(string) string,
	) (commandActions, func(), error)
	runRestoreCheck func(
		context.Context,
		restoreCheckInput,
		func(string) string,
	) error
}

func productionProgramFactories() programFactories {
	return programFactories{
		newActions: func(
			ctx context.Context,
			getenv func(string) string,
		) (commandActions, func(), error) {
			return newProductionActions(ctx, getenv)
		},
		runRestoreCheck: runProductionRestoreCheck,
	}
}

func runProgram(
	ctx context.Context,
	args []string,
	getenv func(string) string,
	factories programFactories,
) error {
	if ctx == nil || getenv == nil || len(args) == 0 {
		return errInvalidCommand
	}
	if args[0] == "restore-check" {
		input, err := parseRestoreCheckInput(args)
		if err != nil {
			return err
		}
		if factories.runRestoreCheck == nil {
			return errWorkflowUnavailable
		}
		if err := factories.runRestoreCheck(ctx, input, getenv); err != nil {
			return errWorkflowUnavailable
		}
		return nil
	}
	if factories.newActions == nil {
		return errWorkflowUnavailable
	}
	actions, closeActions, err := factories.newActions(ctx, getenv)
	if err != nil || actions == nil || closeActions == nil {
		return errWorkflowUnavailable
	}
	defer closeActions()
	return runCommand(ctx, args, actions)
}

func parseRestoreCheckInput(args []string) (restoreCheckInput, error) {
	if len(args) != 5 ||
		args[0] != "restore-check" ||
		args[1] != "--backup-id" ||
		args[3] != "--report-file" ||
		args[4] != restoreCheckReportFile {
		return restoreCheckInput{}, errInvalidCommand
	}
	backupID, err := uuid.Parse(args[2])
	if err != nil ||
		backupID == uuid.Nil ||
		backupID.Version() != 4 ||
		backupID.Variant() != uuid.RFC4122 ||
		backupID.String() != args[2] {
		return restoreCheckInput{}, errInvalidCommand
	}
	return restoreCheckInput{
		backupID:   backupID,
		reportFile: args[4],
	}, nil
}

func loadRestoreCheckConfig(
	getenv func(string) string,
) (restoreCheckConfig, error) {
	if getenv == nil {
		return restoreCheckConfig{}, errWorkflowUnavailable
	}
	config := restoreCheckConfig{
		databaseHost:    getenv("HAPPYLEARN_DATABASE_HOST"),
		databasePort:    getenv("HAPPYLEARN_DATABASE_PORT"),
		databaseUser:    getenv("HAPPYLEARN_DATABASE_USER"),
		databaseName:    getenv("HAPPYLEARN_DATABASE_NAME"),
		databaseSSLMode: getenv("HAPPYLEARN_DATABASE_SSLMODE"),
		passfile:        getenv("PGPASSFILE"),
		minioEndpoint:   getenv("HAPPYLEARN_MINIO_ENDPOINT"),
		minioAccessKey:  getenv("HAPPYLEARN_MINIO_ACCESS_KEY"),
		minioSecretKey:  getenv("HAPPYLEARN_MINIO_SECRET_KEY"),
		originalsBucket: restoreCheckOriginalsBucket,
		previewsBucket:  restoreCheckPreviewsBucket,
	}
	for _, value := range []string{
		config.databaseHost,
		config.databasePort,
		config.databaseUser,
		config.databaseName,
		config.databaseSSLMode,
		config.passfile,
		config.minioEndpoint,
		config.minioAccessKey,
		config.minioSecretKey,
	} {
		if value == "" || strings.TrimSpace(value) != value {
			return restoreCheckConfig{}, errWorkflowUnavailable
		}
	}
	port, err := strconv.ParseUint(config.databasePort, 10, 16)
	if err != nil || port == 0 || config.passfile != restoreCheckPassfile {
		return restoreCheckConfig{}, errWorkflowUnavailable
	}
	switch config.databaseSSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return restoreCheckConfig{}, errWorkflowUnavailable
	}
	switch getenv("HAPPYLEARN_MINIO_USE_TLS") {
	case "", "false":
	case "true":
		config.useTLS = true
	default:
		return restoreCheckConfig{}, errWorkflowUnavailable
	}
	return config, nil
}

type restoreCheckStatStore interface {
	Stat(context.Context, string) (int64, error)
}

type restoreCheckObjects struct {
	store restoreCheckStatStore
}

func (objects restoreCheckObjects) Stat(
	ctx context.Context,
	key string,
) (int64, error) {
	if ctx == nil || objects.store == nil || key == "" {
		return 0, errWorkflowUnavailable
	}
	size, err := objects.store.Stat(ctx, key)
	if errors.Is(err, objectstore.ErrNotFound) {
		return 0, backup.ErrRestoreObjectNotFound
	}
	if err != nil || size < 0 {
		return 0, errWorkflowUnavailable
	}
	return size, nil
}

func runProductionRestoreCheck(
	ctx context.Context,
	input restoreCheckInput,
	getenv func(string) string,
) error {
	if ctx == nil ||
		input.backupID == uuid.Nil ||
		input.backupID.Version() != 4 ||
		input.backupID.Variant() != uuid.RFC4122 ||
		input.reportFile != restoreCheckReportFile {
		return errWorkflowUnavailable
	}
	config, err := loadRestoreCheckConfig(getenv)
	if err != nil {
		return errWorkflowUnavailable
	}
	databaseURL := &url.URL{
		Scheme: "postgres",
		User:   url.User(config.databaseUser),
		Host:   net.JoinHostPort(config.databaseHost, config.databasePort),
		Path:   config.databaseName,
	}
	query := databaseURL.Query()
	query.Set("sslmode", config.databaseSSLMode)
	query.Set("passfile", config.passfile)
	query.Set("connect_timeout", "5")
	query.Set("statement_timeout", "30000")
	databaseURL.RawQuery = query.Encode()

	pool, err := database.Open(ctx, databaseURL.String())
	if err != nil {
		return errWorkflowUnavailable
	}
	defer pool.Close()
	stores, err := objectstore.NewReadOnlyMinIO(ctx, objectstore.MinIOConfig{
		Endpoint:        config.minioEndpoint,
		AccessKey:       config.minioAccessKey,
		SecretKey:       config.minioSecretKey,
		UseTLS:          config.useTLS,
		OriginalsBucket: config.originalsBucket,
		PreviewsBucket:  config.previewsBucket,
	})
	if err != nil {
		return errWorkflowUnavailable
	}
	verifier := backup.NewRestoreVerifier(
		backup.NewPostgresRestoreVerificationDatabase(pool),
		restoreCheckObjects{store: stores.Originals},
		restoreCheckObjects{store: stores.Previews},
	)
	result, err := verifier.Verify(ctx)
	if err != nil {
		return errWorkflowUnavailable
	}
	if err := backup.WriteRestoreVerificationReport(
		input.reportFile,
		result,
	); err != nil {
		return errWorkflowUnavailable
	}
	return nil
}

var _ backup.RestoreVerificationObjects = restoreCheckObjects{}
