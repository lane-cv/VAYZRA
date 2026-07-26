package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

const migrationFixtureCleanupTimeout = 5 * time.Second

func TestFileProcessingMigrationUsesVersionSevenAndBackfillsPendingFiles(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	actor := uuid.New()
	username := "procmig_" + uuid.NewString()
	objectKey := "test-processing/migration/" + uuid.NewString()
	// Register fixture cleanup before creating the provider. Cleanup is LIFO:
	// registerMigrationProviderCleanup is added next, so the latest schema is restored
	// before this callback removes any partially-created fixture rows.
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), migrationFixtureCleanupTimeout)
		defer cancel()
		if err := cleanupFileProcessingMigrationFixture(cleanupCtx, pool, username, objectKey); err != nil {
			t.Errorf("cleanup file processing migration fixture: %v", err)
		}
	})

	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'Migration fixture','student','active','hash')`, actor, username); err != nil {
		t.Fatal(err)
	}
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actor).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,'backfill.pdf','application/pdf',1,$3,'pending_scan',$4) RETURNING id`, fileID, objectKey, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", actor).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	var versionSevenApplied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=7 AND is_applied)`).Scan(&versionSevenApplied); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_processing_jobs WHERE file_version_id=$1 AND kind='process_file' AND state='queued'`, versionID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if !versionSevenApplied || jobs != 1 {
		t.Fatalf("version_seven_applied=%t jobs=%d", versionSevenApplied, jobs)
	}
}

func cleanupFileProcessingMigrationFixture(ctx context.Context, pool *pgxpool.Pool, username, objectKey string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var jobsTable, versionsTable, filesTable, usersTable bool
	if err := tx.QueryRow(ctx, `SELECT
		to_regclass('public.file_processing_jobs') IS NOT NULL,
		to_regclass('public.file_versions') IS NOT NULL,
		to_regclass('public.files') IS NOT NULL,
		to_regclass('public.users') IS NOT NULL`).Scan(&jobsTable, &versionsTable, &filesTable, &usersTable); err != nil {
		return err
	}
	if jobsTable && versionsTable {
		if _, err := tx.Exec(ctx, `DELETE FROM file_processing_jobs j USING file_versions fv WHERE j.file_version_id=fv.id AND fv.object_key=$1`, objectKey); err != nil {
			return err
		}
	}
	if versionsTable {
		if _, err := tx.Exec(ctx, `DELETE FROM file_versions WHERE object_key=$1`, objectKey); err != nil {
			return err
		}
	}
	if filesTable && usersTable {
		query := `DELETE FROM files f USING users u WHERE f.created_by=u.id AND u.username=$1`
		if versionsTable {
			query += ` AND NOT EXISTS(SELECT 1 FROM file_versions fv WHERE fv.file_id=f.id)`
		}
		if _, err := tx.Exec(ctx, query, username); err != nil {
			return err
		}
	}
	if usersTable {
		query := `DELETE FROM users u WHERE u.username=$1`
		if filesTable {
			query += ` AND NOT EXISTS(SELECT 1 FROM files f WHERE f.created_by=u.id)`
		}
		if _, err := tx.Exec(ctx, query, username); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
