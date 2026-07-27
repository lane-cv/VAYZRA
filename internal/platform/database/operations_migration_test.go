package database_test

import (
	"context"
	"testing"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestOperationsFoundationMigrationContracts(t *testing.T) {
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
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
          (SELECT count(*) FROM pg_constraint
           WHERE conname IN (
             'system_settings_retention_check',
             'system_settings_backup_clock_check',
             'system_settings_threshold_order_check',
             'operational_modes_state_check',
             'operational_modes_lease_shape_check'))`).
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

	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
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
	var settingsVersion, modeVersion int64
	var softDeleteRetention, auditRetention, sampleRetention, backupHour, backupMinute int
	var diskWarning, diskCritical, aiWarning, aiCritical, queueWarning, queueCritical int
	var updatedBy, modeOwner, leaseToken, leaseExpires, enteredAt any
	if err := pool.QueryRow(ctx, `
	SELECT version, site_name, site_announcement, soft_delete_retention_days, audit_retention_days,
	       operational_sample_retention_days, backup_hour, backup_minute, backup_timezone,
	       disk_warning_percent, disk_critical_percent, ai_error_warning_percent,
       ai_error_critical_percent, processing_queue_warning, processing_queue_critical, updated_by
FROM system_settings`).Scan(
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
SELECT version, mode, owner_id, lease_token_hash, lease_expires_at, entered_at
FROM operational_modes`).Scan(&modeVersion, &mode, &modeOwner, &leaseToken, &leaseExpires, &enteredAt); err != nil {
		t.Fatal(err)
	}
	if settingsVersion != 1 || modeVersion != 1 || siteName != "HappyLearn" || announcement != "" || softDeleteRetention != 30 || auditRetention != 365 ||
		sampleRetention != 7 || backupHour != 3 || backupMinute != 0 ||
		backupTimezone != "Asia/Shanghai" || diskWarning != 75 || diskCritical != 90 ||
		aiWarning != 10 || aiCritical != 25 || queueWarning != 20 || queueCritical != 100 ||
		updatedBy != nil || mode != "normal" || modeOwner != nil || leaseToken != nil || leaseExpires != nil || enteredAt != nil {
		t.Fatalf("unexpected recreated defaults: settings_version=%d mode_version=%d site=%q announcement=%q soft=%d audit=%d sample=%d backup=%02d:%02d %q disk=%d/%d ai=%d/%d queue=%d/%d updated_by=%v mode=%q owner=%v token=%v expires=%v entered=%v",
			settingsVersion, modeVersion, siteName, announcement, softDeleteRetention, auditRetention, sampleRetention, backupHour, backupMinute, backupTimezone,
			diskWarning, diskCritical, aiWarning, aiCritical, queueWarning, queueCritical, updatedBy, mode, modeOwner, leaseToken, leaseExpires, enteredAt)
	}
}
