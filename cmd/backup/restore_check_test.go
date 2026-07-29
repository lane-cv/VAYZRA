package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/db/migrations"
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
		"HAPPYLEARN_MINIO_USE_TLS",
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
	values["PGPASSFILE"] = restoreCheckPassfile
	values["HAPPYLEARN_MINIO_ENDPOINT"] = "restore-aistor.internal:9000"
	if _, err := loadRestoreCheckConfig(getenv); !errors.Is(
		err,
		errWorkflowUnavailable,
	) {
		t.Fatalf("non-isolated plaintext endpoint error=%v", err)
	}
	values["HAPPYLEARN_MINIO_USE_TLS"] = "true"
	config, err = loadRestoreCheckConfig(getenv)
	if err != nil || !config.useTLS {
		t.Fatalf("explicit TLS endpoint config=%+v err=%v", config, err)
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

func TestLoadRestoreCheckManifestUsesActualCanonicalBytesAndMatchingBackupID(
	t *testing.T,
) {
	backupID := uuid.MustParse(canonicalRestoreBackupID)
	encoded := restoreCheckManifestBytes(t, backupID, 19)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadRestoreCheckManifest(path, backupID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.manifest.BatchID != canonicalRestoreBackupID ||
		loaded.manifest.DatabaseMigrationVersion != 19 ||
		loaded.sha256 != sha256.Sum256(encoded) {
		t.Fatalf("loaded=%+v", loaded)
	}
}

func TestLoadRestoreCheckManifestRejectsMissingTamperedWrongAndUnsafeFiles(
	t *testing.T,
) {
	backupID := uuid.MustParse(canonicalRestoreBackupID)
	valid := restoreCheckManifestBytes(t, backupID, 19)
	for _, testCase := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name:  "missing",
			setup: func(*testing.T, string) {},
		},
		{
			name: "tampered noncanonical",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(
					path,
					append(append([]byte(nil), valid...), '\n'),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wrong backup ID",
			setup: func(t *testing.T, path string) {
				t.Helper()
				wrongID := uuid.MustParse(
					"22222222-2222-4222-8222-222222222222",
				)
				if err := os.WriteFile(
					path,
					restoreCheckManifestBytes(t, wrongID, 19),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := path + ".target"
				if err := os.WriteFile(target, valid, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "too large",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(
					path,
					bytes.Repeat(
						[]byte{'x'},
						backup.ManifestMaxBytes+1,
					),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			testCase.setup(t, path)
			_, err := loadRestoreCheckManifest(path, backupID)
			if !errors.Is(err, errWorkflowUnavailable) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), path) ||
				strings.Contains(err.Error(), canonicalRestoreBackupID) {
				t.Fatalf("error leaks manifest detail: %v", err)
			}
		})
	}
}

func restoreCheckManifestBytes(
	t *testing.T,
	backupID uuid.UUID,
	migrationVersion int64,
) []byte {
	t.Helper()
	encoded, err := backup.MarshalManifest(backup.Manifest{
		SchemaVersion:            1,
		BatchID:                  backupID.String(),
		CreatedAt:                time.Date(2026, 7, 29, 1, 2, 3, 4, time.UTC),
		DatabaseMigrationVersion: migrationVersion,
		DatabaseDumpSHA256: strings.Repeat(
			"1",
			sha256.Size*2,
		),
		ObjectSnapshotID: strings.Repeat(
			"2",
			sha256.Size*2,
		),
		ObjectCount:     1,
		ReferencedBytes: 41,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestLatestEmbeddedMigrationVersionIsStrictAndCurrent(t *testing.T) {
	latest, err := latestEmbeddedMigrationVersion(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if latest != 20 {
		t.Fatalf("latest migration=%d", latest)
	}

	for _, testCase := range []struct {
		name       string
		migrations fstest.MapFS
	}{
		{
			name: "noncanonical name",
			migrations: fstest.MapFS{
				"20_bad.sql": {Data: []byte("SELECT 1;")},
			},
		},
		{
			name: "duplicate version",
			migrations: fstest.MapFS{
				"00001_first.sql":  {Data: []byte("SELECT 1;")},
				"00001_second.sql": {Data: []byte("SELECT 1;")},
			},
		},
		{
			name: "zero version",
			migrations: fstest.MapFS{
				"00000_zero.sql": {Data: []byte("SELECT 1;")},
			},
		},
		{
			name:       "empty",
			migrations: fstest.MapFS{},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := latestEmbeddedMigrationVersion(
				testCase.migrations,
			); !errors.Is(err, errWorkflowUnavailable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestValidateRestoreMigrationVersionsAllowsOldBackupAtLatestSchema(
	t *testing.T,
) {
	for _, testCase := range []struct {
		name            string
		manifestVersion int64
		restoredVersion int64
		embeddedLatest  int64
		wantError       bool
	}{
		{
			name:            "current manifest",
			manifestVersion: 20, restoredVersion: 20, embeddedLatest: 20,
		},
		{
			name:            "old manifest migrated forward",
			manifestVersion: 19, restoredVersion: 20, embeddedLatest: 20,
		},
		{
			name:            "future manifest",
			manifestVersion: 21, restoredVersion: 20, embeddedLatest: 20,
			wantError: true,
		},
		{
			name:            "old restored database",
			manifestVersion: 19, restoredVersion: 19, embeddedLatest: 20,
			wantError: true,
		},
		{
			name:            "future restored database",
			manifestVersion: 20, restoredVersion: 21, embeddedLatest: 20,
			wantError: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateRestoreMigrationVersions(
				testCase.manifestVersion,
				testCase.restoredVersion,
				testCase.embeddedLatest,
			)
			if testCase.wantError &&
				!errors.Is(err, errWorkflowUnavailable) {
				t.Fatalf("error=%v", err)
			}
			if !testCase.wantError && err != nil {
				t.Fatal(err)
			}
		})
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
