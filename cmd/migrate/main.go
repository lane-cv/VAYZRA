package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/secretfile"
	"happylearn.local/app/internal/release"
)

const migrationAdvisoryLock int64 = 845103121

type result struct {
	Status        string `json:"status"`
	Category      string `json:"category"`
	SchemaVersion *int64 `json:"schemaVersion,omitempty"`
}

type migrationStore interface {
	CurrentSchema(context.Context) (int64, error)
	Acquire(context.Context, int64) (func(context.Context) error, error)
	Migrate(context.Context) error
	Close()
}

type dependencies struct {
	open   func(context.Context, string) (migrationStore, error)
	latest func() (int64, error)
	getenv func(string) string
}

func main() {
	code := run(context.Background(), os.Args[1:], os.Stdout, productionDependencies())
	if code != 0 {
		os.Exit(code)
	}
}

func run(parent context.Context, args []string, output io.Writer, deps dependencies) int {
	response, code := execute(parent, args, deps)
	if json.NewEncoder(output).Encode(response) != nil {
		return 1
	}
	return code
}

func execute(parent context.Context, args []string, deps dependencies) (result, int) {
	if len(args) == 1 && args[0] == "current-schema" {
		version, err := deps.latest()
		if err != nil {
			return failure("embedded_schema_unavailable"), 1
		}
		return result{Status: "pass", Category: "embedded_schema", SchemaVersion: &version}, 0
	}
	set := flag.NewFlagSet("migrate", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	manifestPath := set.String("manifest", "", "")
	if len(args) == 0 || args[0] != "run" || set.Parse(args[1:]) != nil || set.NArg() != 0 || *manifestPath == "" {
		return failure("invalid_arguments"), 1
	}
	manifest, err := loadManifest(*manifestPath)
	if err != nil {
		return failure("invalid_manifest"), 1
	}
	target, err := deps.latest()
	if err != nil || !manifest.CompatibleWithSchema(target) {
		return failure("target_schema_incompatible"), 1
	}
	databaseURL, err := loadDatabaseURL(deps.getenv)
	if err != nil {
		return failure("database_configuration_unavailable"), 1
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	store, err := deps.open(ctx, databaseURL)
	databaseURL = ""
	if err != nil {
		return failure("database_unavailable"), 1
	}
	defer store.Close()
	starting, err := store.CurrentSchema(ctx)
	if err != nil {
		return failure("schema_unavailable"), 1
	}
	if !manifest.CompatibleWithSchema(starting) {
		return failure("starting_schema_incompatible"), 1
	}
	unlock, err := store.Acquire(ctx, migrationAdvisoryLock)
	if err != nil {
		return failure("migration_lock_unavailable"), 1
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = unlock(unlockCtx)
		unlockCancel()
	}()
	lockedVersion, err := store.CurrentSchema(ctx)
	if err != nil || lockedVersion != starting {
		return failure("schema_changed_during_preflight"), 1
	}
	if err := store.Migrate(ctx); err != nil {
		return failure("migration_failed"), 1
	}
	current, err := store.CurrentSchema(ctx)
	if err != nil || current != target || !manifest.CompatibleWithSchema(current) {
		return failure("resulting_schema_invalid"), 1
	}
	return result{Status: "pass", Category: "migration_complete", SchemaVersion: &current}, 0
}

func loadManifest(path string) (release.Manifest, error) {
	if !filepath.IsAbs(path) {
		return release.Manifest{}, errors.New("manifest unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return release.Manifest{}, errors.New("manifest unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return release.Manifest{}, errors.New("manifest unavailable")
	}
	return release.ParseManifest(data)
}

func loadDatabaseURL(getenv func(string) string) (string, error) {
	if getenv == nil || getenv("HAPPYLEARN_DATABASE_URL") != "" || getenv("HAPPYLEARN_DATABASE_URL_FILE") == "" {
		return "", errors.New("database configuration unavailable")
	}
	value, err := secretfile.Read(getenv("HAPPYLEARN_DATABASE_URL_FILE"))
	if err != nil {
		return "", errors.New("database configuration unavailable")
	}
	return string(value), nil
}

func failure(category string) result { return result{Status: "fail", Category: category} }

type postgresStore struct {
	pool *pgxpool.Pool
	lock *pgxpool.Conn
}

func productionDependencies() dependencies {
	return dependencies{
		latest: database.LatestMigrationVersion,
		getenv: os.Getenv,
		open: func(ctx context.Context, databaseURL string) (migrationStore, error) {
			pool, err := database.Open(ctx, databaseURL)
			if err != nil {
				return nil, err
			}
			return &postgresStore{pool: pool}, nil
		},
	}
}

func (s *postgresStore) CurrentSchema(ctx context.Context) (int64, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('public.goose_db_version') IS NOT NULL`).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var version int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(version_id),0) FROM goose_db_version WHERE is_applied`).Scan(&version)
	return version, err
}

func (s *postgresStore) Acquire(ctx context.Context, key int64) (func(context.Context) error, error) {
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
		connection.Release()
		return nil, err
	}
	s.lock = connection
	return func(unlockCtx context.Context) error {
		if s.lock == nil {
			return nil
		}
		connection := s.lock
		s.lock = nil
		var unlocked bool
		err := connection.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, key).Scan(&unlocked)
		if err != nil || !unlocked {
			connection.Conn().Close(unlockCtx)
			return errors.New("migration unlock failed")
		}
		connection.Release()
		return nil
	}, nil
}

func (s *postgresStore) Migrate(ctx context.Context) error { return database.Migrate(ctx, s.pool) }

func (s *postgresStore) Close() {
	if s.lock != nil {
		_ = s.lock.Conn().Close(context.Background())
		s.lock = nil
	}
	s.pool.Close()
}
