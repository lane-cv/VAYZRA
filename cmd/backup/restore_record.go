package main

import (
	"context"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/platform/database"
)

type restoreSuccessRecorder interface {
	RecordRestoreSuccess(
		context.Context,
		backup.RestoreSuccessInput,
	) (backup.RestoreVerification, error)
}

func recordRestoreExercise(
	ctx context.Context,
	reader io.Reader,
	recorder restoreSuccessRecorder,
	now func() time.Time,
) error {
	if ctx == nil || reader == nil || recorder == nil || now == nil {
		return errWorkflowUnavailable
	}
	report, err := backup.ParseRestoreExerciseReport(reader)
	if err != nil {
		return errWorkflowUnavailable
	}
	finishedAt := now().UTC().Truncate(time.Microsecond)
	if finishedAt.IsZero() {
		return errWorkflowUnavailable
	}
	startedAt := finishedAt.Add(
		-time.Duration(report.DurationSeconds) * time.Second,
	)
	recorded, err := recorder.RecordRestoreSuccess(
		ctx,
		backup.RestoreSuccessInput{
			VerificationID:            report.VerificationID,
			BackupRunID:               report.BackupID,
			ManifestSHA256:            append([]byte(nil), report.ManifestSHA256...),
			StartedAt:                 startedAt,
			FinishedAt:                finishedAt,
			RestoredMigrationVersion:  report.RestoredMigrationVersion,
			DatabaseRowCounts:         report.DatabaseRowCounts,
			CheckedObjectCount:        report.CheckedObjectCount,
			SessionRevocationVerified: true,
			RTOSeconds:                report.DurationSeconds,
			ReportSHA256:              append([]byte(nil), report.ReportSHA256...),
		},
	)
	if err != nil ||
		recorded.ID != report.VerificationID ||
		recorded.BackupRunID != report.BackupID ||
		recorded.State != backup.RestoreSucceeded {
		return errWorkflowUnavailable
	}
	return nil
}

type restoreRecordDatabaseConfig struct {
	host    string
	port    string
	user    string
	name    string
	sslMode string
}

func loadRestoreRecordDatabaseConfig(
	getenv func(string) string,
) (restoreRecordDatabaseConfig, error) {
	if getenv == nil {
		return restoreRecordDatabaseConfig{}, errWorkflowUnavailable
	}
	config := restoreRecordDatabaseConfig{
		host:    getenv("HAPPYLEARN_DATABASE_HOST"),
		port:    getenv("HAPPYLEARN_DATABASE_PORT"),
		user:    getenv("HAPPYLEARN_DATABASE_USER"),
		name:    getenv("HAPPYLEARN_DATABASE_NAME"),
		sslMode: getenv("HAPPYLEARN_DATABASE_SSLMODE"),
	}
	for _, value := range []string{
		config.host,
		config.port,
		config.user,
		config.name,
		config.sslMode,
	} {
		if value == "" || strings.TrimSpace(value) != value {
			return restoreRecordDatabaseConfig{}, errWorkflowUnavailable
		}
	}
	port, err := strconv.ParseUint(config.port, 10, 16)
	if err != nil || port == 0 {
		return restoreRecordDatabaseConfig{}, errWorkflowUnavailable
	}
	return config, nil
}

func runProductionRestoreRecord(
	ctx context.Context,
	getenv func(string) string,
) error {
	config, err := loadRestoreRecordDatabaseConfig(getenv)
	if err != nil {
		return errWorkflowUnavailable
	}
	secrets := backup.NewFileSecrets()
	password, err := secrets.Read(backup.SecretDatabasePassword)
	if err != nil {
		return errWorkflowUnavailable
	}
	databaseURL, err := restoreRecordDatabaseURL(config, password)
	if err != nil {
		return errWorkflowUnavailable
	}
	pool, err := database.Open(ctx, databaseURL.String())
	if err != nil {
		return errWorkflowUnavailable
	}
	defer pool.Close()
	return recordRestoreExercise(
		ctx,
		os.Stdin,
		backup.NewPostgresStore(pool),
		time.Now,
	)
}

func restoreRecordDatabaseURL(
	config restoreRecordDatabaseConfig,
	password string,
) (*url.URL, error) {
	if password == "" {
		return nil, errWorkflowUnavailable
	}
	databaseURL := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(config.user, password),
		Host:   net.JoinHostPort(config.host, config.port),
		Path:   config.name,
	}
	query := databaseURL.Query()
	query.Set("sslmode", config.sslMode)
	query.Set("connect_timeout", "5")
	query.Set("statement_timeout", "30000")
	query.Set("lock_timeout", "10000")
	databaseURL.RawQuery = query.Encode()
	return databaseURL, nil
}

var _ restoreSuccessRecorder = (*backup.PostgresStore)(nil)
