package database_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestBackupRestoreMigrationContracts(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	var tables, constraints, indexes int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM information_schema.tables
   WHERE table_schema='public' AND table_name IN
     ('backup_runs','backup_artifacts','restore_verifications')),
  (SELECT count(*) FROM pg_constraint c
   JOIN pg_class r ON r.oid=c.conrelid
   JOIN pg_namespace n ON n.oid=r.relnamespace
   WHERE n.nspname='public' AND (r.relname,c.conname) IN (
     ('backup_runs','backup_runs_state_check'),
     ('backup_runs','backup_runs_terminal_check'),
     ('backup_artifacts','backup_artifacts_kind_check'),
     ('backup_artifacts','backup_artifacts_repository_check'),
     ('restore_verifications','restore_verifications_state_check'))),
  (SELECT count(*) FROM pg_indexes
   WHERE schemaname='public' AND indexname IN (
     'backup_runs_requested_idx',
     'backup_runs_due_idx',
     'restore_verifications_backup_idx'))`).
		Scan(&tables, &constraints, &indexes); err != nil {
		t.Fatal(err)
	}
	if tables != 3 || constraints != 5 || indexes != 3 {
		t.Fatalf("tables=%d constraints=%d indexes=%d", tables, constraints, indexes)
	}

	var idempotencyConstraint bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_constraint c
  JOIN pg_class r ON r.oid=c.conrelid
  JOIN pg_namespace n ON n.oid=r.relnamespace
  WHERE n.nspname='public'
    AND r.relname='backup_runs'
    AND c.conname='backup_runs_idempotency_key'
    AND c.contype='u'
)`).Scan(&idempotencyConstraint); err != nil {
		t.Fatal(err)
	}
	if !idempotencyConstraint {
		t.Fatal("backup_runs_idempotency_key unique constraint is absent")
	}
}

func TestBackupRestoreMigrationInvariants(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	for _, tc := range []struct {
		name, statement, code, constraint string
	}{
		{
			"invalid backup state",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,state)
			 VALUES('invalid-state','manual','restored')`,
			"23514",
			"backup_runs_state_check",
		},
		{
			"terminal state without finish time",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,state)
			 VALUES('terminal-no-finish','manual','succeeded')`,
			"23514",
			"backup_runs_terminal_check",
		},
		{
			"nonterminal state with finish time",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,state,finished_at)
			 VALUES('queued-with-finish','manual','queued',now())`,
			"23514",
			"backup_runs_terminal_check",
		},
		{
			"empty idempotency key",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind)
			 VALUES('','manual')`,
			"23514",
			"backup_runs_idempotency_key_check",
		},
		{
			"idempotency key above maximum",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind)
			 VALUES(repeat('a',129),'manual')`,
			"23514",
			"backup_runs_idempotency_key_check",
		},
		{
			"invalid trigger kind",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind)
			 VALUES('invalid-trigger','automatic')`,
			"23514",
			"backup_runs_trigger_kind_check",
		},
		{
			"invalid restore state",
			`WITH run AS (
			   INSERT INTO backup_runs(idempotency_key,trigger_kind)
			   VALUES('invalid-restore-state','manual') RETURNING id
			 )
			 INSERT INTO restore_verifications(backup_run_id,state)
			 SELECT id,'verified' FROM run`,
			"23514",
			"restore_verifications_state_check",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
				_, err := tx.Exec(ctx, tc.statement)
				assertBackupRestorePostgresError(t, err, tc.code, tc.constraint)
			})
		})
	}

	t.Run("valid nonterminal and terminal shapes", func(t *testing.T) {
		backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `
INSERT INTO backup_runs(idempotency_key,trigger_kind,state)
VALUES('valid-queued','scheduled','queued')`); err != nil {
				t.Fatalf("insert valid nonterminal backup run: %v", err)
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO backup_runs(idempotency_key,trigger_kind,state,finished_at)
VALUES('valid-terminal','pre_release','degraded',now())`); err != nil {
				t.Fatalf("insert valid terminal backup run: %v", err)
			}
		})
	})
}

func TestBackupRestoreMigrationRejectsDuplicateIdempotency(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
INSERT INTO backup_runs(idempotency_key,trigger_kind)
VALUES('same-operation','manual')`); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, `
INSERT INTO backup_runs(idempotency_key,trigger_kind)
VALUES('same-operation','manual')`)
		assertBackupRestorePostgresError(t, err, "23505", "backup_runs_idempotency_key")
	})

	backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
INSERT INTO backup_runs(idempotency_key,trigger_kind)
VALUES('same-key-different-trigger','manual'),
      ('same-key-different-trigger','scheduled')`); err != nil {
			t.Fatalf("idempotency key should be scoped to trigger kind: %v", err)
		}
	})
}

func TestBackupRestoreMigrationRequiresArtifactHashes(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	for _, size := range []int{31, 33} {
		t.Run(strings.Repeat("x", size), func(t *testing.T) {
			backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
				_, err := tx.Exec(ctx, `
WITH run AS (
  INSERT INTO backup_runs(idempotency_key,trigger_kind)
  VALUES($1,'manual') RETURNING id
)
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
)
SELECT id,'manifest','local','opaque-snapshot',decode(repeat('00',$2),'hex'),0,now(),now()
FROM run`, "hash-"+strings.Repeat("x", size), size)
				assertBackupRestorePostgresError(t, err, "23514", "backup_artifacts_sha256_check")
			})
		})
	}

	backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
WITH run AS (
  INSERT INTO backup_runs(idempotency_key,trigger_kind)
  VALUES('valid-artifact-hash','manual') RETURNING id
)
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
)
SELECT id,'manifest','local','opaque-snapshot',decode(repeat('00',32),'hex'),0,now(),now()
FROM run`); err != nil {
			t.Fatalf("insert valid artifact hash: %v", err)
		}
	})
}

func TestBackupRestoreMigrationDownTo19RemovesOnlyBackupSchema(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)

	if _, err := provider.DownTo(ctx, 19); err != nil {
		t.Fatal(err)
	}

	var backupTables, foundationTables int
	var migration19Applied, migration20Applied bool
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM information_schema.tables
   WHERE table_schema='public' AND table_name IN
     ('backup_runs','backup_artifacts','restore_verifications')),
  (SELECT count(*) FROM information_schema.tables
   WHERE table_schema='public' AND table_name IN
     ('system_settings','operational_modes')),
  EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=19 AND is_applied),
  EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=20 AND is_applied)`).
		Scan(&backupTables, &foundationTables, &migration19Applied, &migration20Applied); err != nil {
		t.Fatal(err)
	}
	if backupTables != 0 || foundationTables != 2 || !migration19Applied || migration20Applied {
		t.Fatalf(
			"backup_tables=%d foundation_tables=%d migration19=%t migration20=%t",
			backupTables,
			foundationTables,
			migration19Applied,
			migration20Applied,
		)
	}
}

func migratedBackupRestore(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 19); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	return pool, ctx
}

func backupRestoreTx(t *testing.T, ctx context.Context, pool *pgxpool.Pool, test func(pgx.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback backup restore transaction: %v", err)
		}
	}()
	test(tx)
}

func assertBackupRestorePostgresError(t *testing.T, err error, code, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatal("constraint accepted invalid mutation")
	}
	var postgresErr *pgconn.PgError
	if !errors.As(err, &postgresErr) ||
		postgresErr.Code != code ||
		postgresErr.ConstraintName != constraint {
		t.Fatalf(
			"expected PostgreSQL code=%s constraint=%s, got %v",
			code,
			constraint,
			err,
		)
	}
}
