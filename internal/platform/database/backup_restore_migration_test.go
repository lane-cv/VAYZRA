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

	var tables, constraints int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM information_schema.tables
   WHERE table_schema='public' AND table_name IN
     ('backup_runs','backup_artifacts','restore_verifications')),
  (SELECT count(*) FROM pg_constraint c
   JOIN pg_class r ON r.oid=c.conrelid
   JOIN pg_namespace n ON n.oid=r.relnamespace
   WHERE n.nspname='public'
     AND c.contype='c'
     AND (r.relname,c.conname) IN (
     ('backup_runs','backup_runs_state_check'),
     ('backup_runs','backup_runs_terminal_check'),
     ('backup_artifacts','backup_artifacts_kind_check'),
     ('backup_artifacts','backup_artifacts_repository_check'),
     ('restore_verifications','restore_verifications_state_check')))`).
		Scan(&tables, &constraints); err != nil {
		t.Fatal(err)
	}
	if tables != 3 || constraints != 5 {
		t.Fatalf("tables=%d constraints=%d", tables, constraints)
	}

	for _, expected := range []struct {
		name, definition string
	}{
		{
			"backup_runs_state_check",
			"CHECK (state = ANY (ARRAY['queued'::text, 'draining'::text, 'snapshotting'::text, 'encrypting'::text, 'verifying'::text, 'syncing'::text, 'succeeded'::text, 'degraded'::text, 'failed'::text]))",
		},
		{
			"backup_runs_terminal_check",
			"CHECK ((state = ANY (ARRAY['succeeded'::text, 'degraded'::text, 'failed'::text])) AND finished_at IS NOT NULL OR (state <> ALL (ARRAY['succeeded'::text, 'degraded'::text, 'failed'::text])) AND finished_at IS NULL)",
		},
		{
			"backup_artifacts_kind_check",
			"CHECK (kind = ANY (ARRAY['database_dump'::text, 'object_snapshot'::text, 'manifest'::text, 'recovery_report'::text]))",
		},
		{
			"backup_artifacts_repository_check",
			"CHECK (repository = ANY (ARRAY['local'::text, 'remote'::text]))",
		},
		{
			"restore_verifications_state_check",
			"CHECK (state = ANY (ARRAY['queued'::text, 'restoring'::text, 'checking'::text, 'succeeded'::text, 'failed'::text]))",
		},
	} {
		assertBackupRestoreCheckDefinition(t, ctx, pool, expected.name, expected.definition)
	}

	var idempotencyTable, idempotencyType, idempotencyColumns string
	if err := pool.QueryRow(ctx, `
SELECT
  r.relname,
  c.contype::text,
  (
    SELECT string_agg(a.attname, ',' ORDER BY key.ordinality)
    FROM unnest(c.conkey) WITH ORDINALITY AS key(attnum, ordinality)
    JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=key.attnum
  )
FROM pg_constraint c
JOIN pg_class r ON r.oid=c.conrelid
JOIN pg_namespace n ON n.oid=r.relnamespace
WHERE n.nspname='public'
  AND c.conname='backup_runs_idempotency_key'`).
		Scan(&idempotencyTable, &idempotencyType, &idempotencyColumns); err != nil {
		t.Fatal(err)
	}
	if idempotencyTable != "backup_runs" ||
		idempotencyType != "u" ||
		idempotencyColumns != "trigger_kind,idempotency_key" {
		t.Fatalf(
			"idempotency table=%q type=%q columns=%q",
			idempotencyTable,
			idempotencyType,
			idempotencyColumns,
		)
	}

	assertBackupRestoreIndex(
		t,
		ctx,
		pool,
		"backup_runs_requested_idx",
		"backup_runs",
		"requested_at,id",
		"DESC,DESC",
		"",
	)
	assertBackupRestoreIndex(
		t,
		ctx,
		pool,
		"backup_runs_due_idx",
		"backup_runs",
		"state,requested_at,id",
		"ASC,ASC,ASC",
		"(state <> ALL (ARRAY['succeeded'::text, 'degraded'::text, 'failed'::text]))",
	)
	assertBackupRestoreIndex(
		t,
		ctx,
		pool,
		"restore_verifications_backup_idx",
		"restore_verifications",
		"backup_run_id,started_at,id",
		"ASC,DESC,DESC",
		"",
	)
}

func TestBackupRestoreMigrationNamedCheckCatalog(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	var matched int
	if err := pool.QueryRow(ctx, `
WITH expected(table_name,constraint_name) AS (
  VALUES
    ('backup_runs','backup_runs_idempotency_key_check'),
    ('backup_runs','backup_runs_trigger_kind_check'),
    ('backup_runs','backup_runs_state_check'),
    ('backup_runs','backup_runs_terminal_check'),
    ('backup_runs','backup_runs_database_migration_version_check'),
    ('backup_runs','backup_runs_manifest_sha256_check'),
    ('backup_runs','backup_runs_logical_bytes_check'),
    ('backup_runs','backup_runs_stored_bytes_check'),
    ('backup_runs','backup_runs_lease_generation_check'),
    ('backup_runs','backup_runs_lease_shape_check'),
    ('backup_runs','backup_runs_terminal_lease_check'),
    ('backup_runs','backup_runs_recovery_evidence_check'),
    ('backup_artifacts','backup_artifacts_sha256_check'),
    ('backup_artifacts','backup_artifacts_size_bytes_check'),
    ('backup_artifacts','backup_artifacts_kind_check'),
    ('backup_artifacts','backup_artifacts_repository_check'),
    ('restore_verifications','restore_verifications_state_check'),
    ('restore_verifications','restore_verifications_counts_check'),
    ('restore_verifications','restore_verifications_rto_seconds_check'),
    ('restore_verifications','restore_verifications_migration_version_check'),
    ('restore_verifications','restore_verifications_report_sha256_check'),
    ('restore_verifications','restore_verifications_row_counts_check'),
    ('restore_verifications','restore_verifications_terminal_check'),
    ('restore_verifications','restore_verifications_timing_check'),
    ('restore_verifications','restore_verifications_success_check')
)
SELECT count(*)
FROM expected e
JOIN pg_class r ON r.relname=e.table_name
JOIN pg_namespace n ON n.oid=r.relnamespace AND n.nspname='public'
JOIN pg_constraint c
  ON c.conrelid=r.oid
  AND c.conname=e.constraint_name
  AND c.contype='c'`).Scan(&matched); err != nil {
		t.Fatal(err)
	}
	if matched != 25 {
		t.Fatalf("matched named check constraints=%d want=25", matched)
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
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,database_migration_version,
			   encryption_key_id,local_snapshot_id,manifest_sha256,local_expires_at
			 )
			 VALUES(
			   'terminal-no-finish','manual','succeeded',1,
			   'key-1','local-1',decode(repeat('00',32),'hex'),now()
			 )`,
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
INSERT INTO backup_runs(
  idempotency_key,
  trigger_kind,
  state,
  finished_at,
  database_migration_version,
  encryption_key_id,
  local_snapshot_id,
  manifest_sha256,
  local_expires_at
)
VALUES(
  'valid-terminal',
  'pre_release',
  'degraded',
  now(),
  1,
  'owner-key-1',
  'local-snapshot',
  decode(repeat('00',32),'hex'),
  now()
)`); err != nil {
				t.Fatalf("insert valid terminal backup run: %v", err)
			}
		})
	})
}

func TestBackupRestoreMigrationBackupRunEvidenceInvariants(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	for _, tc := range []struct {
		name, statement, constraint string
	}{
		{
			"zero database migration version",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,database_migration_version)
			 VALUES('zero-migration','manual',0)`,
			"backup_runs_database_migration_version_check",
		},
		{
			"negative database migration version",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,database_migration_version)
			 VALUES('negative-migration','manual',-1)`,
			"backup_runs_database_migration_version_check",
		},
		{
			"short manifest hash",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,manifest_sha256)
			 VALUES('short-manifest','manual',decode(repeat('00',31),'hex'))`,
			"backup_runs_manifest_sha256_check",
		},
		{
			"long manifest hash",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,manifest_sha256)
			 VALUES('long-manifest','manual',decode(repeat('00',33),'hex'))`,
			"backup_runs_manifest_sha256_check",
		},
		{
			"lease owner without expiry",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,owner_id)
			 VALUES('owner-no-expiry','manual',gen_random_uuid())`,
			"backup_runs_lease_shape_check",
		},
		{
			"lease expiry without owner",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,lease_expires_at)
			 VALUES('expiry-no-owner','manual',now())`,
			"backup_runs_lease_shape_check",
		},
		{
			"terminal run retains lease",
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,finished_at,
			   database_migration_version,encryption_key_id,local_snapshot_id,
			   manifest_sha256,local_expires_at,owner_id,lease_expires_at
			 )
			 VALUES(
			   'terminal-with-lease','manual','succeeded',now(),
			   1,'key-1','local-1',decode(repeat('00',32),'hex'),now(),
			   gen_random_uuid(),now()
			 )`,
			"backup_runs_terminal_lease_check",
		},
		{
			"degraded run retains lease",
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,finished_at,
			   database_migration_version,encryption_key_id,local_snapshot_id,
			   manifest_sha256,local_expires_at,owner_id,lease_expires_at
			 )
			 VALUES(
			   'degraded-with-lease','manual','degraded',now(),
			   1,'key-1','local-1',decode(repeat('00',32),'hex'),now(),
			   gen_random_uuid(),now()
			 )`,
			"backup_runs_terminal_lease_check",
		},
		{
			"failed run retains lease",
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,finished_at,owner_id,lease_expires_at
			 )
			 VALUES(
			   'failed-with-lease','manual','failed',now(),gen_random_uuid(),now()
			 )`,
			"backup_runs_terminal_lease_check",
		},
		{
			"succeeded without local snapshot",
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,finished_at,
			   database_migration_version,encryption_key_id,manifest_sha256,local_expires_at
			 )
			 VALUES(
			   'success-no-local','manual','succeeded',now(),
			   1,'key-1',decode(repeat('00',32),'hex'),now()
			 )`,
			"backup_runs_recovery_evidence_check",
		},
		{
			"degraded without local recovery evidence",
			`INSERT INTO backup_runs(idempotency_key,trigger_kind,state,finished_at)
			 VALUES('degraded-no-local','manual','degraded',now())`,
			"backup_runs_recovery_evidence_check",
		},
		{
			"succeeded with blank local snapshot",
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,finished_at,
			   database_migration_version,encryption_key_id,local_snapshot_id,
			   manifest_sha256,local_expires_at
			 )
			 VALUES(
			   'success-blank-local','manual','succeeded',now(),
			   1,'key-1','   ',decode(repeat('00',32),'hex'),now()
			 )`,
			"backup_runs_recovery_evidence_check",
		},
		{
			"succeeded without manifest hash",
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,finished_at,
			   database_migration_version,encryption_key_id,local_snapshot_id,local_expires_at
			 )
			 VALUES(
			   'success-no-manifest','manual','succeeded',now(),
			   1,'key-1','local-1',now()
			 )`,
			"backup_runs_recovery_evidence_check",
		},
		{
			"succeeded without migration version",
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,finished_at,
			   encryption_key_id,local_snapshot_id,manifest_sha256,local_expires_at
			 )
			 VALUES(
			   'success-no-migration','manual','succeeded',now(),
			   'key-1','local-1',decode(repeat('00',32),'hex'),now()
			 )`,
			"backup_runs_recovery_evidence_check",
		},
		{
			"succeeded with blank encryption key id",
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,finished_at,
			   database_migration_version,encryption_key_id,local_snapshot_id,
			   manifest_sha256,local_expires_at
			 )
			 VALUES(
			   'success-blank-key','manual','succeeded',now(),
			   1,'   ','local-1',decode(repeat('00',32),'hex'),now()
			 )`,
			"backup_runs_recovery_evidence_check",
		},
		{
			"succeeded without local expiry",
			`INSERT INTO backup_runs(
			   idempotency_key,trigger_kind,state,finished_at,
			   database_migration_version,encryption_key_id,local_snapshot_id,manifest_sha256
			 )
			 VALUES(
			   'success-no-expiry','manual','succeeded',now(),
			   1,'key-1','local-1',decode(repeat('00',32),'hex')
			 )`,
			"backup_runs_recovery_evidence_check",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
				_, err := tx.Exec(ctx, tc.statement)
				assertBackupRestorePostgresError(t, err, "23514", tc.constraint)
			})
		})
	}

	t.Run("valid evidence and lease boundaries", func(t *testing.T) {
		backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `
INSERT INTO backup_runs(
  idempotency_key,trigger_kind,state,database_migration_version,
  manifest_sha256,owner_id,lease_expires_at
)
VALUES(
  'valid-leased-queued','scheduled','queued',1,
  decode(repeat('00',32),'hex'),gen_random_uuid(),now()
)`); err != nil {
				t.Fatalf("insert valid leased queued run: %v", err)
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO backup_runs(
  idempotency_key,trigger_kind,state,finished_at,
  database_migration_version,encryption_key_id,local_snapshot_id,
  manifest_sha256,local_expires_at
)
VALUES
  (
    'valid-success','manual','succeeded',now(),
    1,'key-1','local-1',decode(repeat('00',32),'hex'),now()
  ),
  (
    'valid-degraded','manual','degraded',now(),
    1,'key-1','local-2',decode(repeat('00',32),'hex'),now()
  ),
  (
    'valid-failed','manual','failed',now(),
    NULL,NULL,NULL,NULL,NULL
  )`); err != nil {
				t.Fatalf("insert valid terminal evidence boundaries: %v", err)
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

func TestBackupRestoreMigrationFencesLeaseGenerations(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	var nullable, dataType, defaultValue string
	if err := pool.QueryRow(ctx, `
SELECT is_nullable,data_type,column_default
FROM information_schema.columns
WHERE table_schema='public'
  AND table_name='backup_runs'
  AND column_name='lease_generation'`).
		Scan(&nullable, &dataType, &defaultValue); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" || dataType != "bigint" || defaultValue != "0" {
		t.Fatalf(
			"lease_generation nullable=%q type=%q default=%q",
			nullable,
			dataType,
			defaultValue,
		)
	}
	assertBackupRestoreCheckDefinition(
		t,
		ctx,
		pool,
		"backup_runs_lease_generation_check",
		"CHECK (lease_generation >= 0)",
	)

	backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
		_, err := tx.Exec(ctx, `
INSERT INTO backup_runs(idempotency_key,trigger_kind,lease_generation)
VALUES('negative-lease-generation','manual',-1)`)
		assertBackupRestorePostgresError(
			t,
			err,
			"23514",
			"backup_runs_lease_generation_check",
		)
	})
}

func TestBackupRestoreMigrationRequiresValidArtifacts(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	for _, size := range []int{31, 33} {
		t.Run("hash-"+strings.Repeat("x", size), func(t *testing.T) {
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

	for _, tc := range []struct {
		name, kind, repository string
		size                   int
		constraint             string
	}{
		{"invalid kind", "database", "local", 0, "backup_artifacts_kind_check"},
		{"invalid repository", "manifest", "archive", 0, "backup_artifacts_repository_check"},
		{"negative size", "manifest", "local", -1, "backup_artifacts_size_bytes_check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
				_, err := tx.Exec(ctx, `
WITH run AS (
  INSERT INTO backup_runs(idempotency_key,trigger_kind)
  VALUES($1,'manual') RETURNING id
)
INSERT INTO backup_artifacts(
  backup_run_id,kind,repository,snapshot_id,sha256,size_bytes,verified_at,expires_at
)
SELECT id,$2,$3,'opaque-snapshot',decode(repeat('00',32),'hex'),$4,now(),now()
FROM run`, "artifact-"+strings.ReplaceAll(tc.name, " ", "-"), tc.kind, tc.repository, tc.size)
				assertBackupRestorePostgresError(t, err, "23514", tc.constraint)
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

func TestBackupRestoreMigrationRequiresAllowlistedIntegerRowCounts(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	for _, tc := range []struct {
		name, counts string
	}{
		{name: "unknown key", counts: `{"secret_table":1}`},
		{name: "negative", counts: `{"users":-1}`},
		{name: "fraction", counts: `{"users":1.5}`},
		{name: "string", counts: `{"users":"1"}`},
		{name: "bigint overflow", counts: `{"users":9223372036854775808}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
				_, err := tx.Exec(ctx, `
WITH run AS (
  INSERT INTO backup_runs(idempotency_key,trigger_kind)
  VALUES($1,'manual') RETURNING id
)
INSERT INTO restore_verifications(backup_run_id,database_row_counts)
SELECT id,$2::jsonb FROM run`,
					"row-counts-"+strings.ReplaceAll(tc.name, " ", "-"),
					tc.counts,
				)
				assertBackupRestorePostgresError(
					t,
					err,
					"23514",
					"restore_verifications_row_counts_check",
				)
			})
		})
	}

	backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
WITH runs AS (
  INSERT INTO backup_runs(idempotency_key,trigger_kind)
  VALUES('row-counts-empty','manual'),('row-counts-boundaries','manual')
  RETURNING id,idempotency_key
)
INSERT INTO restore_verifications(backup_run_id,database_row_counts)
SELECT
  id,
  CASE idempotency_key
    WHEN 'row-counts-empty' THEN '{}'::jsonb
    ELSE '{
      "users":0,
      "sessions":9223372036854775807,
      "subjects":0,
      "grades":0,
      "terms":0,
      "chapters":0,
      "lessons":0,
      "lesson_revisions":0,
      "files":0,
      "file_versions":0,
      "file_previews":0,
      "qa_threads":0,
      "qa_messages":0,
      "ai_threads":0,
      "ai_messages":0,
      "ai_runs":0
    }'::jsonb
  END
FROM runs`); err != nil {
			t.Fatalf("insert valid row-count boundaries: %v", err)
		}
	})
}

func TestBackupRestoreMigrationRestoreEvidenceInvariants(t *testing.T) {
	pool, ctx := migratedBackupRestore(t)

	for _, tc := range []struct {
		name, values, constraint string
	}{
		{
			"negative checked count",
			`'queued',NULL,NULL,NULL,'{}'::jsonb,-1,0,0,false,NULL,NULL`,
			"restore_verifications_counts_check",
		},
		{
			"negative missing count",
			`'queued',NULL,NULL,NULL,'{}'::jsonb,0,-1,0,false,NULL,NULL`,
			"restore_verifications_counts_check",
		},
		{
			"negative unexpected count",
			`'queued',NULL,NULL,NULL,'{}'::jsonb,0,0,-1,false,NULL,NULL`,
			"restore_verifications_counts_check",
		},
		{
			"negative rto",
			`'queued',NULL,NULL,NULL,'{}'::jsonb,0,0,0,false,-1,NULL`,
			"restore_verifications_rto_seconds_check",
		},
		{
			"zero restored migration",
			`'queued',NULL,NULL,0,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_migration_version_check",
		},
		{
			"short report hash",
			`'queued',NULL,NULL,NULL,'{}'::jsonb,0,0,0,false,NULL,decode(repeat('00',31),'hex')`,
			"restore_verifications_report_sha256_check",
		},
		{
			"long report hash",
			`'queued',NULL,NULL,NULL,'{}'::jsonb,0,0,0,false,NULL,decode(repeat('00',33),'hex')`,
			"restore_verifications_report_sha256_check",
		},
		{
			"row counts array",
			`'queued',NULL,NULL,NULL,'[]'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_row_counts_check",
		},
		{
			"row counts scalar",
			`'queued',NULL,NULL,NULL,'1'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_row_counts_check",
		},
		{
			"row counts null",
			`'queued',NULL,NULL,NULL,'null'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_row_counts_check",
		},
		{
			"succeeded without finish time",
			`'succeeded',now(),NULL,1,'{"users":1}'::jsonb,0,0,0,true,0,decode(repeat('00',32),'hex')`,
			"restore_verifications_terminal_check",
		},
		{
			"failed without finish time",
			`'failed',now(),NULL,NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_terminal_check",
		},
		{
			"nonterminal with finish time",
			`'checking',now(),now(),NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_terminal_check",
		},
		{
			"succeeded without session revocation",
			`'succeeded',now(),now(),1,'{"users":1}'::jsonb,0,0,0,false,0,decode(repeat('00',32),'hex')`,
			"restore_verifications_success_check",
		},
		{
			"succeeded with missing object",
			`'succeeded',now(),now(),1,'{"users":1}'::jsonb,1,1,0,true,0,decode(repeat('00',32),'hex')`,
			"restore_verifications_success_check",
		},
		{
			"succeeded without restored migration",
			`'succeeded',now(),now(),NULL,'{"users":1}'::jsonb,0,0,0,true,0,decode(repeat('00',32),'hex')`,
			"restore_verifications_success_check",
		},
		{
			"succeeded without report hash",
			`'succeeded',now(),now(),1,'{"users":1}'::jsonb,0,0,0,true,0,NULL`,
			"restore_verifications_success_check",
		},
		{
			"queued with start time",
			`'queued',now(),NULL,NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_timing_check",
		},
		{
			"queued with finish time",
			`'queued',NULL,now(),NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_terminal_check",
		},
		{
			"restoring without start time",
			`'restoring',NULL,NULL,NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_timing_check",
		},
		{
			"restoring with finish time",
			`'restoring',now(),now(),NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_terminal_check",
		},
		{
			"checking without start time",
			`'checking',NULL,NULL,NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_timing_check",
		},
		{
			"checking with finish time",
			`'checking',now(),now(),NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_terminal_check",
		},
		{
			"failed without start time",
			`'failed',NULL,now(),NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_timing_check",
		},
		{
			"failed with reversed times",
			`'failed',now(),now()-interval '1 second',NULL,'{}'::jsonb,0,0,0,false,NULL,NULL`,
			"restore_verifications_timing_check",
		},
		{
			"succeeded without start time",
			`'succeeded',NULL,now(),1,'{"users":1}'::jsonb,0,0,0,true,0,decode(repeat('00',32),'hex')`,
			"restore_verifications_timing_check",
		},
		{
			"succeeded without rto",
			`'succeeded',now(),now(),1,'{"users":1}'::jsonb,0,0,0,true,NULL,decode(repeat('00',32),'hex')`,
			"restore_verifications_success_check",
		},
		{
			"succeeded with empty row counts",
			`'succeeded',now(),now(),1,'{}'::jsonb,0,0,0,true,0,decode(repeat('00',32),'hex')`,
			"restore_verifications_success_check",
		},
		{
			"succeeded with reversed times",
			`'succeeded',now(),now()-interval '1 second',1,'{"users":1}'::jsonb,0,0,0,true,0,decode(repeat('00',32),'hex')`,
			"restore_verifications_timing_check",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
				_, err := tx.Exec(ctx, `
WITH run AS (
  INSERT INTO backup_runs(idempotency_key,trigger_kind)
  VALUES($1,'manual') RETURNING id
)
INSERT INTO restore_verifications(
  backup_run_id,state,started_at,finished_at,restored_migration_version,
  database_row_counts,checked_object_count,missing_object_count,
  unexpected_object_count,session_revocation_verified,rto_seconds,report_sha256
)
SELECT id,`+tc.values+`
FROM run`, "restore-"+strings.ReplaceAll(tc.name, " ", "-"))
				assertBackupRestorePostgresError(t, err, "23514", tc.constraint)
			})
		})
	}

	t.Run("valid succeeded failed and nonterminal shapes", func(t *testing.T) {
		backupRestoreTx(t, ctx, pool, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `
WITH runs AS (
  INSERT INTO backup_runs(idempotency_key,trigger_kind)
  VALUES
    ('restore-valid-queued','manual'),
    ('restore-valid-restoring','manual'),
    ('restore-valid-checking','manual'),
    ('restore-valid-succeeded','manual'),
    ('restore-valid-failed','manual')
  RETURNING id,idempotency_key
)
INSERT INTO restore_verifications(
  backup_run_id,state,started_at,finished_at,restored_migration_version,
  database_row_counts,checked_object_count,missing_object_count,
  unexpected_object_count,session_revocation_verified,rto_seconds,report_sha256
)
SELECT
  id,
  CASE idempotency_key
    WHEN 'restore-valid-queued' THEN 'queued'
    WHEN 'restore-valid-restoring' THEN 'restoring'
    WHEN 'restore-valid-checking' THEN 'checking'
    WHEN 'restore-valid-succeeded' THEN 'succeeded'
    ELSE 'failed'
  END,
  CASE WHEN idempotency_key='restore-valid-queued' THEN NULL ELSE now() END,
  CASE
    WHEN idempotency_key IN (
      'restore-valid-queued',
      'restore-valid-restoring',
      'restore-valid-checking'
    )
    THEN NULL
    ELSE now()
  END,
  CASE WHEN idempotency_key='restore-valid-succeeded' THEN 1 ELSE NULL END,
  '{"users":1}'::jsonb,
  CASE WHEN idempotency_key='restore-valid-succeeded' THEN 1 ELSE 0 END,
  0,
  CASE WHEN idempotency_key='restore-valid-succeeded' THEN 1 ELSE 0 END,
  idempotency_key='restore-valid-succeeded',
  CASE WHEN idempotency_key='restore-valid-succeeded' THEN 0 ELSE NULL END,
  CASE
    WHEN idempotency_key='restore-valid-succeeded'
    THEN decode(repeat('00',32),'hex')
    ELSE NULL
  END
FROM runs`); err != nil {
				t.Fatalf("insert valid restore evidence boundaries: %v", err)
			}
		})
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
	var rowCountValidatorRemoved bool
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM information_schema.tables
   WHERE table_schema='public' AND table_name IN
     ('backup_runs','backup_artifacts','restore_verifications')),
  (SELECT count(*) FROM information_schema.tables
   WHERE table_schema='public' AND table_name IN
     ('system_settings','operational_modes')),
  EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=19 AND is_applied),
  EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=20 AND is_applied),
  to_regprocedure('happylearn_valid_restore_row_counts(jsonb)') IS NULL`).
		Scan(
			&backupTables,
			&foundationTables,
			&migration19Applied,
			&migration20Applied,
			&rowCountValidatorRemoved,
		); err != nil {
		t.Fatal(err)
	}
	if backupTables != 0 || foundationTables != 2 || !migration19Applied ||
		migration20Applied || !rowCountValidatorRemoved {
		t.Fatalf(
			"backup_tables=%d foundation_tables=%d migration19=%t migration20=%t row_count_validator_removed=%t",
			backupTables,
			foundationTables,
			migration19Applied,
			migration20Applied,
			rowCountValidatorRemoved,
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

func assertBackupRestoreIndex(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	indexName, tableName, columns, orders, predicate string,
) {
	t.Helper()

	var actualTable, actualColumns, actualOrders, actualPredicate string
	var valid, ready bool
	if err := pool.QueryRow(ctx, `
SELECT
  table_relation.relname,
  (
    SELECT string_agg(a.attname, ',' ORDER BY key.ordinality)
    FROM unnest(i.indkey) WITH ORDINALITY AS key(attnum, ordinality)
    JOIN pg_attribute a
      ON a.attrelid=i.indrelid
      AND a.attnum=key.attnum
    WHERE key.ordinality <= i.indnkeyatts
  ),
  (
    SELECT string_agg(
      CASE
        WHEN pg_index_column_has_property(i.indexrelid, position, 'desc')
        THEN 'DESC'
        ELSE 'ASC'
      END,
      ',' ORDER BY position
    )
    FROM generate_series(1,i.indnkeyatts) AS positions(position)
  ),
  COALESCE(pg_get_expr(i.indpred,i.indrelid),''),
  i.indisvalid,
  i.indisready
FROM pg_index i
JOIN pg_class index_relation ON index_relation.oid=i.indexrelid
JOIN pg_namespace n ON n.oid=index_relation.relnamespace
JOIN pg_class table_relation ON table_relation.oid=i.indrelid
WHERE n.nspname='public' AND index_relation.relname=$1`, indexName).
		Scan(
			&actualTable,
			&actualColumns,
			&actualOrders,
			&actualPredicate,
			&valid,
			&ready,
		); err != nil {
		t.Fatal(err)
	}
	if actualTable != tableName ||
		actualColumns != columns ||
		actualOrders != orders ||
		actualPredicate != predicate ||
		!valid ||
		!ready {
		t.Fatalf(
			"index=%q table=%q columns=%q orders=%q predicate=%q valid=%t ready=%t",
			indexName,
			actualTable,
			actualColumns,
			actualOrders,
			actualPredicate,
			valid,
			ready,
		)
	}
}

func assertBackupRestoreCheckDefinition(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	constraintName, expected string,
) {
	t.Helper()

	var constraintType, definition string
	if err := pool.QueryRow(ctx, `
SELECT c.contype::text,pg_get_constraintdef(c.oid,true)
FROM pg_constraint c
JOIN pg_class r ON r.oid=c.conrelid
JOIN pg_namespace n ON n.oid=r.relnamespace
WHERE n.nspname='public' AND c.conname=$1`, constraintName).
		Scan(&constraintType, &definition); err != nil {
		t.Fatal(err)
	}
	if constraintType != "c" || definition != expected {
		t.Fatalf(
			"constraint=%q type=%q definition=%q",
			constraintName,
			constraintType,
			definition,
		)
	}
}
