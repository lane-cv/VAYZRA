package database_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestOperationsFoundationMigrationContracts(t *testing.T) {
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 18); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}

	var tables, singletonRows, constraints, indexes int
	if err := pool.QueryRow(ctx, `
        SELECT
          (SELECT count(*) FROM information_schema.tables
           WHERE table_schema='public' AND table_name IN
             ('system_settings','operational_modes')),
          (SELECT (SELECT count(*) FROM system_settings)
                + (SELECT count(*) FROM operational_modes)),
          (SELECT count(*) FROM pg_constraint c
           JOIN pg_class r ON r.oid=c.conrelid
           JOIN pg_namespace n ON n.oid=r.relnamespace
           WHERE n.nspname='public' AND (r.relname,c.conname) IN (
             ('system_settings','system_settings_retention_check'),
             ('system_settings','system_settings_backup_clock_check'),
             ('system_settings','system_settings_threshold_order_check'),
             ('operational_modes','operational_modes_state_check'),
             ('operational_modes','operational_modes_lease_shape_check')))`).
		Scan(&tables, &singletonRows, &constraints); err != nil {
		t.Fatal(err)
	}
	if tables != 2 || singletonRows != 2 || constraints != 5 {
		t.Fatalf("tables=%d rows=%d constraints=%d", tables, singletonRows, constraints)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM pg_indexes
WHERE schemaname='public' AND indexname IN ('system_settings_pkey','operational_modes_pkey')`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 2 {
		t.Fatalf("indexes=%d", indexes)
	}
	_, err := pool.Exec(ctx, `
UPDATE operational_modes
SET mode='draining', owner_id=gen_random_uuid(), lease_token_hash=NULL,
    lease_expires_at=now(), entered_at=now()
WHERE singleton_id=true`)
	assertOperationsFoundationPostgresError(t, err, "23514", "operational_modes_lease_shape_check")

	if _, err := provider.DownTo(ctx, 18); err != nil {
		t.Fatal(err)
	}
	var absent bool
	if err := pool.QueryRow(ctx, `
SELECT NOT EXISTS(
  SELECT 1 FROM information_schema.tables
  WHERE table_schema='public' AND table_name IN ('system_settings','operational_modes')
)`).Scan(&absent); err != nil {
		t.Fatal(err)
	}
	if !absent {
		t.Fatal("down migration retained operations foundation tables")
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}

	var siteName, announcement, backupTimezone, mode string
	var settingsSingleton, modeSingleton, settingsUpdated, modeUpdated bool
	var settingsVersion, modeVersion int64
	var softDeleteRetention, auditRetention, sampleRetention, backupHour, backupMinute int
	var diskWarning, diskCritical, aiWarning, aiCritical, queueWarning, queueCritical int
	var updatedBy, modeOwner, leaseToken, leaseExpires, enteredAt any
	if err := pool.QueryRow(ctx, `
	SELECT singleton_id, updated_at IS NOT NULL, version, site_name, site_announcement, soft_delete_retention_days, audit_retention_days,
	       operational_sample_retention_days, backup_hour, backup_minute, backup_timezone,
	       disk_warning_percent, disk_critical_percent, ai_error_warning_percent,
       ai_error_critical_percent, processing_queue_warning, processing_queue_critical, updated_by
FROM system_settings`).Scan(
		&settingsSingleton,
		&settingsUpdated,
		&settingsVersion,
		&siteName,
		&announcement,
		&softDeleteRetention,
		&auditRetention,
		&sampleRetention,
		&backupHour,
		&backupMinute,
		&backupTimezone,
		&diskWarning,
		&diskCritical,
		&aiWarning,
		&aiCritical,
		&queueWarning,
		&queueCritical,
		&updatedBy,
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
SELECT singleton_id, updated_at IS NOT NULL, version, mode, owner_id, lease_token_hash, lease_expires_at, entered_at
FROM operational_modes`).Scan(&modeSingleton, &modeUpdated, &modeVersion, &mode, &modeOwner, &leaseToken, &leaseExpires, &enteredAt); err != nil {
		t.Fatal(err)
	}
	if !settingsSingleton || !modeSingleton || !settingsUpdated || !modeUpdated || settingsVersion != 1 || modeVersion != 1 || siteName != "HappyLearn" || announcement != "" || softDeleteRetention != 30 || auditRetention != 365 ||
		sampleRetention != 7 || backupHour != 3 || backupMinute != 0 ||
		backupTimezone != "Asia/Shanghai" || diskWarning != 75 || diskCritical != 90 ||
		aiWarning != 10 || aiCritical != 25 || queueWarning != 20 || queueCritical != 100 ||
		updatedBy != nil || mode != "normal" || modeOwner != nil || leaseToken != nil || leaseExpires != nil || enteredAt != nil {
		t.Fatalf("unexpected recreated defaults: settings_singleton=%t settings_updated=%t mode_singleton=%t mode_updated=%t settings_version=%d mode_version=%d site=%q announcement=%q soft=%d audit=%d sample=%d backup=%02d:%02d %q disk=%d/%d ai=%d/%d queue=%d/%d updated_by=%v mode=%q owner=%v token=%v expires=%v entered=%v",
			settingsSingleton, settingsUpdated, modeSingleton, modeUpdated, settingsVersion, modeVersion, siteName, announcement, softDeleteRetention, auditRetention, sampleRetention, backupHour, backupMinute, backupTimezone,
			diskWarning, diskCritical, aiWarning, aiCritical, queueWarning, queueCritical, updatedBy, mode, modeOwner, leaseToken, leaseExpires, enteredAt)
	}
}

func TestOperationsFoundationMigrationInvariants(t *testing.T) {
	pool, ctx := migratedOperationsFoundation(t)

	for _, tc := range []struct {
		name, statement, code, constraint string
	}{
		{"soft delete retention below minimum", `UPDATE system_settings SET soft_delete_retention_days=29 WHERE singleton_id=true`, "23514", "system_settings_retention_check"},
		{"soft delete retention above maximum", `UPDATE system_settings SET soft_delete_retention_days=366 WHERE singleton_id=true`, "23514", "system_settings_retention_check"},
		{"audit retention below minimum", `UPDATE system_settings SET audit_retention_days=364 WHERE singleton_id=true`, "23514", "system_settings_retention_check"},
		{"audit retention above maximum", `UPDATE system_settings SET audit_retention_days=2556 WHERE singleton_id=true`, "23514", "system_settings_retention_check"},
		{"sample retention below minimum", `UPDATE system_settings SET operational_sample_retention_days=0 WHERE singleton_id=true`, "23514", "system_settings_retention_check"},
		{"sample retention above maximum", `UPDATE system_settings SET operational_sample_retention_days=31 WHERE singleton_id=true`, "23514", "system_settings_retention_check"},
		{"backup hour below minimum", `UPDATE system_settings SET backup_hour=-1 WHERE singleton_id=true`, "23514", "system_settings_backup_clock_check"},
		{"backup minute above maximum", `UPDATE system_settings SET backup_minute=60 WHERE singleton_id=true`, "23514", "system_settings_backup_clock_check"},
		{"disk threshold order", `UPDATE system_settings SET disk_warning_percent=90, disk_critical_percent=90 WHERE singleton_id=true`, "23514", "system_settings_threshold_order_check"},
		{"AI threshold order", `UPDATE system_settings SET ai_error_warning_percent=25, ai_error_critical_percent=25 WHERE singleton_id=true`, "23514", "system_settings_threshold_order_check"},
		{"processing queue threshold order", `UPDATE system_settings SET processing_queue_warning=100, processing_queue_critical=100 WHERE singleton_id=true`, "23514", "system_settings_threshold_order_check"},
		{"invalid operational mode", `UPDATE operational_modes SET mode='paused', owner_id=gen_random_uuid(), lease_token_hash=decode(repeat('00',32),'hex'), lease_expires_at=now(), entered_at=now() WHERE singleton_id=true`, "23514", "operational_modes_state_check"},
		{"normal mode lease metadata", `UPDATE operational_modes SET owner_id=gen_random_uuid() WHERE singleton_id=true`, "23514", "operational_modes_lease_shape_check"},
		{"maintenance mode missing owner", `UPDATE operational_modes SET mode='draining', owner_id=NULL, lease_token_hash=decode(repeat('00',32),'hex'), lease_expires_at=now(), entered_at=now() WHERE singleton_id=true`, "23514", "operational_modes_lease_shape_check"},
		{"maintenance mode missing lease hash", `UPDATE operational_modes SET mode='draining', owner_id=gen_random_uuid(), lease_token_hash=NULL, lease_expires_at=now(), entered_at=now() WHERE singleton_id=true`, "23514", "operational_modes_lease_shape_check"},
		{"maintenance mode missing expiry", `UPDATE operational_modes SET mode='draining', owner_id=gen_random_uuid(), lease_token_hash=decode(repeat('00',32),'hex'), lease_expires_at=NULL, entered_at=now() WHERE singleton_id=true`, "23514", "operational_modes_lease_shape_check"},
		{"maintenance mode missing entered at", `UPDATE operational_modes SET mode='draining', owner_id=gen_random_uuid(), lease_token_hash=decode(repeat('00',32),'hex'), lease_expires_at=now(), entered_at=NULL WHERE singleton_id=true`, "23514", "operational_modes_lease_shape_check"},
		{"maintenance mode hash too short", `UPDATE operational_modes SET mode='draining', owner_id=gen_random_uuid(), lease_token_hash=decode(repeat('00',31),'hex'), lease_expires_at=now(), entered_at=now() WHERE singleton_id=true`, "23514", "operational_modes_lease_shape_check"},
		{"maintenance mode hash too long", `UPDATE operational_modes SET mode='draining', owner_id=gen_random_uuid(), lease_token_hash=decode(repeat('00',33),'hex'), lease_expires_at=now(), entered_at=now() WHERE singleton_id=true`, "23514", "operational_modes_lease_shape_check"},
		{"false system settings singleton", `INSERT INTO system_settings(singleton_id) VALUES(false)`, "23514", "system_settings_singleton_id_check"},
		{"false operational modes singleton", `INSERT INTO operational_modes(singleton_id) VALUES(false)`, "23514", "operational_modes_singleton_id_check"},
		{"duplicate system settings singleton", `INSERT INTO system_settings(singleton_id) VALUES(true)`, "23505", "system_settings_pkey"},
		{"duplicate operational modes singleton", `INSERT INTO operational_modes(singleton_id) VALUES(true)`, "23505", "operational_modes_pkey"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operationsFoundationTx(t, ctx, pool, func(tx pgx.Tx) {
				_, err := tx.Exec(ctx, tc.statement)
				assertOperationsFoundationPostgresError(t, err, tc.code, tc.constraint)
			})
		})
	}

	t.Run("valid settings and maintenance boundaries", func(t *testing.T) {
		operationsFoundationTx(t, ctx, pool, func(tx pgx.Tx) {
			for _, statement := range []string{
				`UPDATE system_settings SET soft_delete_retention_days=30, audit_retention_days=365, operational_sample_retention_days=1, backup_hour=0, backup_minute=0, disk_warning_percent=1, disk_critical_percent=2, ai_error_warning_percent=1, ai_error_critical_percent=2, processing_queue_warning=1, processing_queue_critical=2 WHERE singleton_id=true`,
				`UPDATE system_settings SET soft_delete_retention_days=365, audit_retention_days=2555, operational_sample_retention_days=30, backup_hour=23, backup_minute=59, disk_warning_percent=99, disk_critical_percent=100, ai_error_warning_percent=99, ai_error_critical_percent=100, processing_queue_warning=99, processing_queue_critical=100 WHERE singleton_id=true`,
			} {
				if _, err := tx.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			for _, mode := range []string{"draining", "backup", "release"} {
				if _, err := tx.Exec(ctx, `
UPDATE operational_modes
SET mode=$1, owner_id=gen_random_uuid(), lease_token_hash=decode(repeat('00',32),'hex'),
    lease_expires_at=now(), entered_at=now()
WHERE singleton_id=true`, mode); err != nil {
					t.Fatalf("valid %s maintenance mode: %v", mode, err)
				}
			}
		})
	})
}

func migratedOperationsFoundation(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 18); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	return pool, ctx
}

func operationsFoundationTx(t *testing.T, ctx context.Context, pool *pgxpool.Pool, test func(pgx.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback operations foundation transaction: %v", err)
		}
	})
	test(tx)
}

func assertOperationsFoundationPostgresError(t *testing.T, err error, code, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatal("constraint accepted invalid mutation")
	}
	var postgresErr *pgconn.PgError
	if !errors.As(err, &postgresErr) || postgresErr.Code != code || postgresErr.ConstraintName != constraint {
		t.Fatalf("expected PostgreSQL code=%s constraint=%s, got %v", code, constraint, err)
	}
}
