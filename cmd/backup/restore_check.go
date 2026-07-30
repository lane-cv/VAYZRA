package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"happylearn.local/app/db/migrations"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/internal/platform/safelog"
)

const (
	restoreCheckReportFile      = "/work/restore-check.report"
	restoreCheckManifestFile    = "/run/restore/manifest.json"
	restoreCheckPassfile        = "/run/restore-secrets/pgpass"
	restoreCheckOriginalsBucket = "happylearn-originals"
	restoreCheckPreviewsBucket  = "happylearn-previews"
)

var strictMigrationFilename = regexp.MustCompile(
	`^([0-9]{5})_[a-z][a-z0-9_]*\.sql$`,
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

type restoreCheckManifest struct {
	manifest backup.Manifest
	sha256   [sha256.Size]byte
}

type restoreCheckFailure struct {
	category string
}

func (failure *restoreCheckFailure) Error() string {
	return errWorkflowUnavailable.Error()
}

func (failure *restoreCheckFailure) Unwrap() error {
	return errWorkflowUnavailable
}

func newRestoreCheckFailure(category string) error {
	switch category {
	case "input",
		"manifest",
		"migration_catalog",
		"configuration",
		"configuration_database_host",
		"configuration_database_port",
		"configuration_database_user",
		"configuration_database_name",
		"configuration_database_sslmode",
		"configuration_passfile",
		"configuration_minio_endpoint",
		"configuration_minio_access",
		"configuration_minio_secret",
		"configuration_minio_tls",
		"database",
		"object_store",
		"verification",
		"migration_state",
		"report":
	default:
		category = "unavailable"
	}
	return &restoreCheckFailure{category: category}
}

func restoreCheckFailureCategory(err error) string {
	var failure *restoreCheckFailure
	if errors.As(err, &failure) && failure != nil {
		return failure.category
	}
	return "unavailable"
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
	runRestoreHTTPProbe func(context.Context) error
	runRestoreRecord    func(context.Context, func(string) string) error
}

func productionProgramFactories() programFactories {
	return productionProgramFactoriesWithLog(safelog.Logger{})
}

func productionProgramFactoriesWithLog(logger safelog.Logger) programFactories {
	return programFactories{
		newActions: func(
			ctx context.Context,
			getenv func(string) string,
		) (commandActions, func(), error) {
			application, closeApplication, err := newProductionActions(ctx, getenv)
			if application != nil {
				application.logCategory = func(category string) {
					logger.Error("backup.remote_sync", safelog.Field{
						Name:  "category",
						Value: category,
					})
				}
			}
			return application, closeApplication, err
		},
		runRestoreCheck: func(
			ctx context.Context,
			input restoreCheckInput,
			getenv func(string) string,
		) error {
			err := runProductionRestoreCheck(ctx, input, getenv)
			if err != nil {
				logger.Error("backup.restore_check_failure", safelog.Field{
					Name:  "category",
					Value: restoreCheckFailureCategory(err),
				})
			}
			return err
		},
		runRestoreHTTPProbe: runProductionRestoreHTTPProbe,
		runRestoreRecord:    runProductionRestoreRecord,
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
	if args[0] == "restore-record" {
		if len(args) != 1 {
			return errInvalidCommand
		}
		if factories.runRestoreRecord == nil {
			return errWorkflowUnavailable
		}
		if err := factories.runRestoreRecord(ctx, getenv); err != nil {
			return errWorkflowUnavailable
		}
		return nil
	}
	if args[0] == "restore-http-probe" {
		if len(args) != 1 {
			return errInvalidCommand
		}
		if factories.runRestoreHTTPProbe == nil {
			return errRestoreHTTPProbeUnavailable
		}
		if err := factories.runRestoreHTTPProbe(ctx); err != nil {
			return errRestoreHTTPProbeUnavailable
		}
		return nil
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
		return restoreCheckConfig{}, newRestoreCheckFailure("configuration")
	}
	minioTLS := getenv("HAPPYLEARN_MINIO_USE_TLS")
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
	for _, required := range []struct {
		value    string
		category string
	}{
		{config.databaseHost, "configuration_database_host"},
		{config.databasePort, "configuration_database_port"},
		{config.databaseUser, "configuration_database_user"},
		{config.databaseName, "configuration_database_name"},
		{config.databaseSSLMode, "configuration_database_sslmode"},
		{config.passfile, "configuration_passfile"},
		{config.minioEndpoint, "configuration_minio_endpoint"},
		{config.minioAccessKey, "configuration_minio_access"},
		{config.minioSecretKey, "configuration_minio_secret"},
		{minioTLS, "configuration_minio_tls"},
	} {
		if required.value == "" ||
			strings.TrimSpace(required.value) != required.value {
			return restoreCheckConfig{},
				newRestoreCheckFailure(required.category)
		}
	}
	port, err := strconv.ParseUint(config.databasePort, 10, 16)
	if err != nil || port == 0 {
		return restoreCheckConfig{},
			newRestoreCheckFailure("configuration_database_port")
	}
	if config.passfile != restoreCheckPassfile {
		return restoreCheckConfig{},
			newRestoreCheckFailure("configuration_passfile")
	}
	switch config.databaseSSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return restoreCheckConfig{},
			newRestoreCheckFailure("configuration_database_sslmode")
	}
	switch minioTLS {
	case "false":
		if config.minioEndpoint != "minio:9000" {
			return restoreCheckConfig{},
				newRestoreCheckFailure("configuration_minio_endpoint")
		}
	case "true":
		config.useTLS = true
	default:
		return restoreCheckConfig{},
			newRestoreCheckFailure("configuration_minio_tls")
	}
	return config, nil
}

func loadRestoreCheckManifest(
	path string,
	expectedBackupID uuid.UUID,
) (restoreCheckManifest, error) {
	if path == "" ||
		expectedBackupID == uuid.Nil ||
		expectedBackupID.Version() != 4 ||
		expectedBackupID.Variant() != uuid.RFC4122 {
		return restoreCheckManifest{}, errWorkflowUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 ||
		info.Size() > backup.ManifestMaxBytes {
		return restoreCheckManifest{}, errWorkflowUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return restoreCheckManifest{}, errWorkflowUnavailable
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil ||
		!os.SameFile(info, openedInfo) ||
		!openedInfo.Mode().IsRegular() ||
		openedInfo.Mode()&os.ModeSymlink != 0 ||
		openedInfo.Size() != info.Size() {
		_ = file.Close()
		return restoreCheckManifest{}, errWorkflowUnavailable
	}
	encoded, readErr := io.ReadAll(io.LimitReader(
		file,
		backup.ManifestMaxBytes+1,
	))
	closeErr := file.Close()
	if readErr != nil ||
		closeErr != nil ||
		len(encoded) < 1 ||
		len(encoded) > backup.ManifestMaxBytes ||
		int64(len(encoded)) != info.Size() {
		return restoreCheckManifest{}, errWorkflowUnavailable
	}
	manifest, err := backup.DecodeManifest(bytes.NewReader(encoded))
	if err != nil || manifest.BatchID != expectedBackupID.String() {
		return restoreCheckManifest{}, errWorkflowUnavailable
	}
	return restoreCheckManifest{
		manifest: manifest,
		sha256:   sha256.Sum256(encoded),
	}, nil
}

func latestEmbeddedMigrationVersion(migrationFS fs.FS) (int64, error) {
	if migrationFS == nil {
		return 0, errWorkflowUnavailable
	}
	entries, err := fs.ReadDir(migrationFS, ".")
	if err != nil || len(entries) == 0 {
		return 0, errWorkflowUnavailable
	}
	seen := make(map[int64]struct{}, len(entries))
	var latest int64
	for _, entry := range entries {
		match := strictMigrationFilename.FindStringSubmatch(entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil ||
			info == nil ||
			!info.Mode().IsRegular() ||
			len(match) != 2 {
			return 0, errWorkflowUnavailable
		}
		version, parseErr := strconv.ParseInt(match[1], 10, 64)
		if parseErr != nil || version < 1 {
			return 0, errWorkflowUnavailable
		}
		if _, duplicate := seen[version]; duplicate {
			return 0, errWorkflowUnavailable
		}
		seen[version] = struct{}{}
		if version > latest {
			latest = version
		}
	}
	if latest < 1 {
		return 0, errWorkflowUnavailable
	}
	return latest, nil
}

func validateRestoreMigrationVersions(
	manifestVersion int64,
	restoredVersion int64,
	embeddedLatest int64,
) error {
	if manifestVersion < 1 ||
		restoredVersion != embeddedLatest ||
		embeddedLatest < 1 ||
		manifestVersion > restoredVersion {
		return errWorkflowUnavailable
	}
	return nil
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
		return newRestoreCheckFailure("input")
	}
	manifest, err := loadRestoreCheckManifest(
		restoreCheckManifestFile,
		input.backupID,
	)
	if err != nil {
		return newRestoreCheckFailure("manifest")
	}
	latestMigration, err := latestEmbeddedMigrationVersion(migrations.FS)
	if err != nil {
		return newRestoreCheckFailure("migration_catalog")
	}
	if manifest.manifest.DatabaseMigrationVersion > latestMigration {
		return newRestoreCheckFailure("migration_state")
	}
	config, err := loadRestoreCheckConfig(getenv)
	if err != nil {
		return err
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
		return newRestoreCheckFailure("database")
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
		return newRestoreCheckFailure("object_store")
	}
	verifier := backup.NewRestoreVerifier(
		backup.NewPostgresRestoreVerificationDatabase(pool),
		restoreCheckObjects{store: stores.Originals},
		restoreCheckObjects{store: stores.Previews},
	)
	result, err := verifier.Verify(ctx)
	if err != nil {
		return newRestoreCheckFailure("verification")
	}
	if err := validateRestoreMigrationVersions(
		manifest.manifest.DatabaseMigrationVersion,
		result.RestoredMigrationVersion,
		latestMigration,
	); err != nil {
		return newRestoreCheckFailure("migration_state")
	}
	if err := backup.WriteBoundRestoreVerificationReport(
		input.reportFile,
		input.backupID,
		manifest.sha256,
		result,
	); err != nil {
		return newRestoreCheckFailure("report")
	}
	return nil
}

var _ backup.RestoreVerificationObjects = restoreCheckObjects{}
