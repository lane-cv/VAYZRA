package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"happylearn.local/app/internal/release"
)

type fakeMigrationStore struct {
	versions   []int64
	index      int
	lockKey    int64
	migrations int
	migrateErr error
}

func (s *fakeMigrationStore) CurrentSchema(context.Context) (int64, error) {
	if len(s.versions) == 0 {
		return 0, errors.New("schema")
	}
	index := s.index
	if index >= len(s.versions) {
		index = len(s.versions) - 1
	}
	s.index++
	return s.versions[index], nil
}
func (s *fakeMigrationStore) Acquire(_ context.Context, key int64) (func(context.Context) error, error) {
	s.lockKey = key
	return func(context.Context) error { return nil }, nil
}
func (s *fakeMigrationStore) Migrate(context.Context) error { s.migrations++; return s.migrateErr }
func (s *fakeMigrationStore) Close()                        {}

func migrationManifest(t *testing.T, min, max int64) string {
	t.Helper()
	images := map[string]string{}
	for _, name := range []string{"app", "worker", "migrate", "backup", "caddy", "postgres", "redis", "minio"} {
		images[name] = "registry.example/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	m := release.Manifest{Version: "6.0.0", Commit: strings.Repeat("b", 40), BuiltAt: time.Now().UTC(), Images: images, MinSchemaVersion: min, MaxSchemaVersion: max, ComposeSHA256: strings.Repeat("c", 64), CaddySHA256: strings.Repeat("d", 64), BackupEvidenceID: "backup-1", CreatedBy: "test", CreatedAt: time.Now().UTC()}
	data, err := m.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func migrationDeps(t *testing.T, store migrationStore, latest int64) dependencies {
	t.Helper()
	secret := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(secret, []byte("postgres://user:secret@db/app"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dependencies{latest: func() (int64, error) { return latest, nil }, getenv: func(name string) string {
		if name == "HAPPYLEARN_DATABASE_URL_FILE" {
			return secret
		}
		return ""
	}, open: func(context.Context, string) (migrationStore, error) { return store, nil }}
}

func runMigration(t *testing.T, manifest string, deps dependencies) (int, result) {
	t.Helper()
	var output bytes.Buffer
	code := run(context.Background(), []string{"run", "--manifest", manifest}, &output, deps)
	var got result
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return code, got
}

func TestMigrateRejectsIncompatibleStartingSchema(t *testing.T) {
	store := &fakeMigrationStore{versions: []int64{17}}
	code, got := runMigration(t, migrationManifest(t, 18, 27), migrationDeps(t, store, 27))
	if code == 0 || got.Category != "starting_schema_incompatible" || store.migrations != 0 {
		t.Fatalf("code=%d result=%+v migrations=%d", code, got, store.migrations)
	}
}

func TestMigrateUsesDedicatedAdvisoryLock(t *testing.T) {
	store := &fakeMigrationStore{versions: []int64{26, 26, 27}}
	code, _ := runMigration(t, migrationManifest(t, 26, 27), migrationDeps(t, store, 27))
	if code != 0 || store.lockKey != migrationAdvisoryLock || store.lockKey == 845103120 {
		t.Fatalf("code=%d lock=%d", code, store.lockKey)
	}
}

func TestMigrateAppliesPendingVersionsOnce(t *testing.T) {
	store := &fakeMigrationStore{versions: []int64{27, 27, 27}}
	code, _ := runMigration(t, migrationManifest(t, 27, 27), migrationDeps(t, store, 27))
	if code != 0 || store.migrations != 1 {
		t.Fatalf("code=%d migrations=%d", code, store.migrations)
	}
}

func TestMigrateReportsCurrentSchemaWithoutDSN(t *testing.T) {
	deps := dependencies{latest: func() (int64, error) { return 27, nil }}
	var output bytes.Buffer
	if code := run(context.Background(), []string{"current-schema"}, &output, deps); code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if strings.Contains(output.String(), "postgres") || !strings.Contains(output.String(), `"schemaVersion":27`) {
		t.Fatalf("unsafe output=%s", output.String())
	}
}

func TestMigrateLeavesFailedTransactionalDDLUncommitted(t *testing.T) {
	store := &fakeMigrationStore{versions: []int64{26, 26}, migrateErr: errors.New("secret SQL body")}
	code, got := runMigration(t, migrationManifest(t, 26, 27), migrationDeps(t, store, 27))
	if code == 0 || got.Category != "migration_failed" {
		t.Fatalf("code=%d result=%+v", code, got)
	}
}

func TestMigrateRequiresDatabaseURLFile(t *testing.T) {
	deps := migrationDeps(t, &fakeMigrationStore{versions: []int64{27}}, 27)
	deps.getenv = func(name string) string {
		if name == "HAPPYLEARN_DATABASE_URL" {
			return "postgres://direct"
		}
		return ""
	}
	code, got := runMigration(t, migrationManifest(t, 27, 27), deps)
	if code == 0 || got.Category != "database_configuration_unavailable" {
		t.Fatalf("code=%d result=%+v", code, got)
	}
}
