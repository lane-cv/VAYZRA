package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"happylearn.local/app/db/migrations"
)

var migrationFilename = regexp.MustCompile(`^([0-9]{5})_[a-z0-9_]+\.sql$`)

// LatestMigrationVersion returns the highest strictly named embedded migration.
func LatestMigrationVersion() (int64, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations")
	}
	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "embed.go" {
			continue
		}
		match := migrationFilename.FindStringSubmatch(entry.Name())
		if match == nil {
			return 0, fmt.Errorf("invalid embedded migration name")
		}
		version, parseErr := strconv.ParseInt(match[1], 10, 64)
		if parseErr != nil || version < 1 {
			return 0, fmt.Errorf("invalid embedded migration version")
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return 0, fmt.Errorf("embedded migrations unavailable")
	}
	return latest, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("apply database migrations: database pool is nil")
	}

	db, err := sql.Open("pgx", pool.Config().ConnString())
	if err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
}
