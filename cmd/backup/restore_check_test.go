package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/platform/objectstore"
)

const canonicalRestoreBackupID = "11111111-1111-4111-8111-111111111111"

func TestRunProgramDispatchesRestoreCheckBeforeBackupConstruction(t *testing.T) {
	var backupConstructions, restoreRuns int
	factories := programFactories{
		newActions: func(
			context.Context,
			func(string) string,
		) (commandActions, func(), error) {
			backupConstructions++
			return nil, func() {}, errors.New("must not construct backup actions")
		},
		runRestoreCheck: func(
			_ context.Context,
			input restoreCheckInput,
			_ func(string) string,
		) error {
			restoreRuns++
			if input.backupID != uuid.MustParse(canonicalRestoreBackupID) ||
				input.reportFile != "/work/restore-check.report" {
				t.Fatalf("input=%+v", input)
			}
			return nil
		},
	}
	err := runProgram(
		context.Background(),
		[]string{
			"restore-check",
			"--backup-id", canonicalRestoreBackupID,
			"--report-file", "/work/restore-check.report",
		},
		func(string) string { return "" },
		factories,
	)
	if err != nil {
		t.Fatal(err)
	}
	if restoreRuns != 1 || backupConstructions != 0 {
		t.Fatalf(
			"restore runs=%d backup constructions=%d",
			restoreRuns,
			backupConstructions,
		)
	}
}

func TestRunProgramKeepsOrdinaryBackupConstructionAndCleanup(t *testing.T) {
	actions := &recordingActions{}
	var backupConstructions, backupCloses, restoreRuns int
	factories := programFactories{
		newActions: func(
			context.Context,
			func(string) string,
		) (commandActions, func(), error) {
			backupConstructions++
			return actions, func() { backupCloses++ }, nil
		},
		runRestoreCheck: func(
			context.Context,
			restoreCheckInput,
			func(string) string,
		) error {
			restoreRuns++
			return errors.New("must not run restore check")
		},
	}
	err := runProgram(
		context.Background(),
		[]string{"prepare", "--run-id", commandRunID},
		func(string) string { return "" },
		factories,
	)
	if err != nil {
		t.Fatal(err)
	}
	if backupConstructions != 1 ||
		backupCloses != 1 ||
		restoreRuns != 0 ||
		len(actions.calls) != 1 ||
		actions.calls[0] != "prepare" {
		t.Fatalf(
			"constructions=%d closes=%d restore=%d calls=%v",
			backupConstructions,
			backupCloses,
			restoreRuns,
			actions.calls,
		)
	}
}

func TestRestoreCheckArgumentsAreExactCanonicalV4(t *testing.T) {
	valid := []string{
		"restore-check",
		"--backup-id", canonicalRestoreBackupID,
		"--report-file", "/work/restore-check.report",
	}
	input, err := parseRestoreCheckInput(valid)
	if err != nil {
		t.Fatal(err)
	}
	if input.backupID.Version() != 4 ||
		input.backupID.Variant() != uuid.RFC4122 ||
		input.backupID.String() != canonicalRestoreBackupID {
		t.Fatalf("backup ID=%s", input.backupID)
	}

	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "missing", args: valid[:4]},
		{
			name: "extra",
			args: append(append([]string{}, valid...), "--unexpected"),
		},
		{
			name: "reordered",
			args: []string{
				"restore-check",
				"--report-file", "/work/restore-check.report",
				"--backup-id", canonicalRestoreBackupID,
			},
		},
		{
			name: "wrong report",
			args: []string{
				"restore-check",
				"--backup-id", canonicalRestoreBackupID,
				"--report-file", "/tmp/restore-check.report",
			},
		},
		{
			name: "version one",
			args: []string{
				"restore-check",
				"--backup-id", "11111111-1111-1111-8111-111111111111",
				"--report-file", "/work/restore-check.report",
			},
		},
		{
			name: "non canonical",
			args: []string{
				"restore-check",
				"--backup-id", "{11111111-1111-4111-8111-111111111111}",
				"--report-file", "/work/restore-check.report",
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := parseRestoreCheckInput(
				testCase.args,
			); !errors.Is(err, errInvalidCommand) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRestoreCheckConfigIsDedicatedAndFailClosed(t *testing.T) {
	values := map[string]string{
		"HAPPYLEARN_DATABASE_HOST":          "postgres",
		"HAPPYLEARN_DATABASE_PORT":          "5432",
		"HAPPYLEARN_DATABASE_USER":          "happylearn",
		"HAPPYLEARN_DATABASE_NAME":          "happylearn",
		"HAPPYLEARN_DATABASE_SSLMODE":       "disable",
		"PGPASSFILE":                        "/run/secrets/pgpass",
		"HAPPYLEARN_MINIO_ENDPOINT":         "minio:9000",
		"HAPPYLEARN_MINIO_ACCESS_KEY":       "restore-access-key",
		"HAPPYLEARN_MINIO_SECRET_KEY":       "restore-secret-key",
		"HAPPYLEARN_MINIO_USE_TLS":          "false",
		"HAPPYLEARN_MINIO_ORIGINALS_BUCKET": "attacker-originals",
		"HAPPYLEARN_MINIO_PREVIEWS_BUCKET":  "attacker-previews",
		"HAPPYLEARN_BACKUP_AGE_RECIPIENT":   "",
	}
	getenv := func(key string) string { return values[key] }
	config, err := loadRestoreCheckConfig(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if config.databaseHost != "postgres" ||
		config.databasePort != "5432" ||
		config.passfile != "/run/secrets/pgpass" ||
		config.originalsBucket != "happylearn-originals" ||
		config.previewsBucket != "happylearn-previews" ||
		config.useTLS {
		t.Fatalf("config=%+v", config)
	}

	for _, key := range []string{
		"HAPPYLEARN_DATABASE_HOST",
		"PGPASSFILE",
		"HAPPYLEARN_MINIO_ENDPOINT",
		"HAPPYLEARN_MINIO_ACCESS_KEY",
		"HAPPYLEARN_MINIO_SECRET_KEY",
	} {
		t.Run(key, func(t *testing.T) {
			broken := make(map[string]string, len(values))
			for name, value := range values {
				broken[name] = value
			}
			broken[key] = ""
			_, err := loadRestoreCheckConfig(
				func(name string) string { return broken[name] },
			)
			if !errors.Is(err, errWorkflowUnavailable) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "restore-secret-key") ||
				strings.Contains(err.Error(), "/run/secrets") {
				t.Fatalf("error leaks secret/path: %v", err)
			}
		})
	}

	values["HAPPYLEARN_MINIO_USE_TLS"] = "true"
	config, err = loadRestoreCheckConfig(getenv)
	if err != nil || !config.useTLS {
		t.Fatalf("TLS config=%+v err=%v", config, err)
	}
	values["HAPPYLEARN_MINIO_USE_TLS"] = "1"
	if _, err := loadRestoreCheckConfig(getenv); !errors.Is(
		err,
		errWorkflowUnavailable,
	) {
		t.Fatalf("non-canonical TLS error=%v", err)
	}
	values["HAPPYLEARN_MINIO_USE_TLS"] = "false"
	values["PGPASSFILE"] = "/tmp/attacker-pgpass"
	if _, err := loadRestoreCheckConfig(getenv); !errors.Is(
		err,
		errWorkflowUnavailable,
	) {
		t.Fatalf("non-fixed passfile error=%v", err)
	}
}

func TestRestoreCheckObjectAdapterMapsSizeNotFoundAndBackendFailure(t *testing.T) {
	const secret = "private-object-key secret-access-key"
	adapter := restoreCheckObjects{store: restoreCheckStatFixture{
		sizes: map[string]int64{"present": 41},
		errors: map[string]error{
			"missing": objectstore.ErrNotFound,
			"failed":  errors.New(secret),
		},
	}}
	size, err := adapter.Stat(context.Background(), "present")
	if err != nil || size != 41 {
		t.Fatalf("size=%d err=%v", size, err)
	}
	if _, err := adapter.Stat(
		context.Background(),
		"missing",
	); !errors.Is(err, backup.ErrRestoreObjectNotFound) {
		t.Fatalf("missing error=%v", err)
	}
	if _, err := adapter.Stat(
		context.Background(),
		"failed",
	); !errors.Is(err, errWorkflowUnavailable) ||
		strings.Contains(err.Error(), secret) {
		t.Fatalf("backend error=%v", err)
	}
}

type restoreCheckStatFixture struct {
	sizes  map[string]int64
	errors map[string]error
}

func (fixture restoreCheckStatFixture) Stat(
	_ context.Context,
	key string,
) (int64, error) {
	return fixture.sizes[key], fixture.errors[key]
}
