package database_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"happylearn.local/app/db/migrations"
)

const alertSettingsMigrationCleanupTimeout = 5 * time.Second

func TestAlertSettingsCanonicalKeyMigrationHasGooseStatementBoundaries(
	t *testing.T,
) {
	raw, err := migrations.FS.ReadFile(
		"00026_alert_settings_canonical_remote_key.sql",
	)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	const begin = "-- +goose StatementBegin\nDO $$"
	const end = "END $$;\n-- +goose StatementEnd"
	if strings.Count(sql, "-- +goose StatementBegin") != 2 ||
		strings.Count(sql, "-- +goose StatementEnd") != 2 ||
		strings.Count(sql, begin) != 2 ||
		strings.Count(sql, end) != 2 {
		t.Fatal("migration does not wrap both DO blocks in Goose boundaries")
	}
	down := strings.Index(sql, "-- +goose Down")
	firstBegin := strings.Index(sql, begin)
	firstEnd := strings.Index(sql, end)
	secondBegin := strings.LastIndex(sql, begin)
	secondEnd := strings.LastIndex(sql, end)
	if firstBegin < 0 || firstEnd <= firstBegin || down <= firstEnd ||
		secondBegin <= down || secondEnd <= secondBegin {
		t.Fatalf(
			"invalid Goose boundary order: first=%d/%d down=%d second=%d/%d",
			firstBegin,
			firstEnd,
			down,
			secondBegin,
			secondEnd,
		)
	}
}

func TestAlertSettingsCanonicalKeyMigrationDefaultsBoundariesAndHistory(
	t *testing.T,
) {
	pool, ctx := migratedOperationsMonitoring(t)
	provider, closeProvider := migrationProvider(
		t,
		pool.Config().ConnString(),
	)
	t.Cleanup(closeProvider)
	alertID := uuid.MustParse("26000000-0000-4000-8000-000000000001")
	dependencyAlertID := uuid.MustParse(
		"26000000-0000-4000-8000-000000000002",
	)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			alertSettingsMigrationCleanupTimeout,
		)
		defer cancel()
		if err := restoreAlertSettingsMigrationState(
			cleanupCtx,
			pool,
			provider,
			alertID,
			dependencyAlertID,
		); err != nil {
			t.Errorf("restore alert settings migration state: %v", err)
		}
	})
	if _, err := provider.DownTo(ctx, 25); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO operational_alerts(
  id,dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  resolved_at,current_value,threshold_value,summary
) VALUES(
  $1,'backup_remote_replication','backup','warning','resolved',
  clock_timestamp()-interval '1 minute',clock_timestamp(),clock_timestamp(),
  0,1,'Remote backup replication is unavailable'
)`, alertID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operational_alerts(
  id,dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  resolved_at,current_value,threshold_value,summary
) VALUES(
  $1,'backup_remote_replication_dependency_unavailable',
  'backup','warning','resolved',
  clock_timestamp()-interval '1 minute',clock_timestamp(),clock_timestamp(),
  1,1,'Remote backup status is unavailable'
)`, dependencyAlertID); err != nil {
		t.Fatal(err)
	}
	eventID := uuid.MustParse("26000000-0000-4000-8000-000000000003")
	deliveryID := uuid.MustParse("26000000-0000-4000-8000-000000000004")
	if _, err := pool.Exec(ctx, `
INSERT INTO alert_webhook_events(
  id,alert_id,transition_kind,alert_version,category,severity,state,
  summary,current_value,threshold_value,first_observed_at,last_observed_at,
  enqueued_at
) VALUES(
  $1,$2,'resolved',1,'backup','warning','resolved',
  'Remote backup replication is unavailable',0,1,
  clock_timestamp()-interval '1 minute',clock_timestamp(),clock_timestamp()
)`, eventID, alertID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO alert_deliveries(
  id,alert_id,event_id,attempt,destination,delivery_state,scheduled_at
) VALUES($1,$2,$3,1,'webhook','pending',clock_timestamp())`,
		deliveryID, alertID, eventID); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}

	var (
		key                   string
		linkedEventAlertID    uuid.UUID
		linkedDeliveryAlertID uuid.UUID
		linkedDeliveryEventID uuid.UUID
		backupWarning         int
		backupCritical        int
		ageWarning            int
		ageCritical           int
		processingWarning     int
		processingCritical    int
		loginWarning          int
		loginCritical         int
		authorizationWarning  int
		authorizationCritical int
	)
	if err := pool.QueryRow(ctx, `
SELECT dedupe_key,
       (SELECT alert_id FROM alert_webhook_events WHERE id=$2),
       (SELECT alert_id FROM alert_deliveries WHERE id=$3),
       (SELECT event_id FROM alert_deliveries WHERE id=$3),
       backup_filesystem_warning_percent,backup_filesystem_critical_percent,
       local_backup_age_warning_hours,local_backup_age_critical_hours,
       processing_failure_warning_count,processing_failure_critical_count,
       login_failure_warning_count,login_failure_critical_count,
       authorization_denial_warning_count,authorization_denial_critical_count
FROM operational_alerts CROSS JOIN system_settings
WHERE operational_alerts.id=$1`, alertID, eventID, deliveryID).Scan(
		&key,
		&linkedEventAlertID,
		&linkedDeliveryAlertID,
		&linkedDeliveryEventID,
		&backupWarning,
		&backupCritical,
		&ageWarning,
		&ageCritical,
		&processingWarning,
		&processingCritical,
		&loginWarning,
		&loginCritical,
		&authorizationWarning,
		&authorizationCritical,
	); err != nil {
		t.Fatal(err)
	}
	if key != "backup_remote_sync" ||
		linkedEventAlertID != alertID ||
		linkedDeliveryAlertID != alertID ||
		linkedDeliveryEventID != eventID ||
		backupWarning != 75 || backupCritical != 90 ||
		ageWarning != 25 || ageCritical != 30 ||
		processingWarning != 5 || processingCritical != 20 ||
		loginWarning != 20 || loginCritical != 100 ||
		authorizationWarning != 50 || authorizationCritical != 200 {
		t.Fatalf(
			"key=%q event_alert=%s delivery_alert=%s delivery_event=%s thresholds=%d/%d %d/%d %d/%d %d/%d %d/%d",
			key,
			linkedEventAlertID,
			linkedDeliveryAlertID,
			linkedDeliveryEventID,
			backupWarning,
			backupCritical,
			ageWarning,
			ageCritical,
			processingWarning,
			processingCritical,
			loginWarning,
			loginCritical,
			authorizationWarning,
			authorizationCritical,
		)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT dedupe_key FROM operational_alerts WHERE id=$1`,
		dependencyAlertID,
	).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "backup_remote_sync_dependency_unavailable" {
		t.Fatalf("dependency key=%q", key)
	}

	for _, statement := range []string{
		`UPDATE system_settings SET backup_filesystem_warning_percent=1,backup_filesystem_critical_percent=2,local_backup_age_warning_hours=1,local_backup_age_critical_hours=2,processing_queue_warning=1,processing_queue_critical=2,processing_failure_warning_count=1,processing_failure_critical_count=2,login_failure_warning_count=1,login_failure_critical_count=2,authorization_denial_warning_count=1,authorization_denial_critical_count=2`,
		`UPDATE system_settings SET backup_filesystem_warning_percent=99,backup_filesystem_critical_percent=100,local_backup_age_warning_hours=2147483646,local_backup_age_critical_hours=2147483647,processing_queue_warning=2147483646,processing_queue_critical=2147483647,processing_failure_warning_count=2147483646,processing_failure_critical_count=2147483647,login_failure_warning_count=2147483646,login_failure_critical_count=2147483647,authorization_denial_warning_count=2147483646,authorization_denial_critical_count=2147483647`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("valid boundary rejected: %v", err)
		}
	}

	_, err := pool.Exec(ctx, `
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary
) VALUES(
  'backup_remote_replication','backup','warning','open',
  now(),now(),0,1,'legacy'
)`)
	assertAlertSettingsPostgresError(
		t,
		err,
		"23514",
		"operational_alerts_no_legacy_remote_keys_check",
	)

	if _, err := provider.DownTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT dedupe_key FROM operational_alerts WHERE id=$1`,
		alertID,
	).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "backup_remote_replication" {
		t.Fatalf("down key=%q", key)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT dedupe_key FROM operational_alerts WHERE id=$1`,
		dependencyAlertID,
	).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "backup_remote_replication_dependency_unavailable" {
		t.Fatalf("down dependency key=%q", key)
	}
	if err := restoreAlertSettingsMigrationState(
		ctx,
		pool,
		provider,
		alertID,
		dependencyAlertID,
	); err != nil {
		t.Fatal(err)
	}
	assertAlertSettingsMigrationLatestClean(
		t,
		ctx,
		pool,
		alertID,
		dependencyAlertID,
	)
}

func TestAlertSettingsCanonicalKeyMigrationRejectsInvalidThresholds(
	t *testing.T,
) {
	pool, ctx := migratedOperationsMonitoring(t)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			alertSettingsMigrationCleanupTimeout,
		)
		defer cancel()
		if err := resetAlertThresholdSettings(cleanupCtx, pool); err != nil {
			t.Errorf("reset alert threshold settings: %v", err)
		}
	})
	tests := []struct {
		name       string
		statement  string
		code       string
		constraint string
	}{
		{"backup percent lower", `UPDATE system_settings SET backup_filesystem_warning_percent=0`, "23514", "system_settings_threshold_order_check"},
		{"backup percent upper", `UPDATE system_settings SET backup_filesystem_critical_percent=101`, "23514", "system_settings_threshold_order_check"},
		{"backup percent order", `UPDATE system_settings SET backup_filesystem_warning_percent=90,backup_filesystem_critical_percent=90`, "23514", "system_settings_threshold_order_check"},
		{"local age lower", `UPDATE system_settings SET local_backup_age_warning_hours=0`, "23514", "system_settings_threshold_order_check"},
		{"local age upper", `UPDATE system_settings SET local_backup_age_critical_hours=2147483648`, "22003", ""},
		{"local age order", `UPDATE system_settings SET local_backup_age_warning_hours=30,local_backup_age_critical_hours=30`, "23514", "system_settings_threshold_order_check"},
		{"processing failures lower", `UPDATE system_settings SET processing_failure_warning_count=0`, "23514", "system_settings_threshold_order_check"},
		{"processing failures upper", `UPDATE system_settings SET processing_failure_critical_count=2147483648`, "22003", ""},
		{"processing failures order", `UPDATE system_settings SET processing_failure_warning_count=20,processing_failure_critical_count=20`, "23514", "system_settings_threshold_order_check"},
		{"login failures lower", `UPDATE system_settings SET login_failure_warning_count=0`, "23514", "system_settings_threshold_order_check"},
		{"login failures upper", `UPDATE system_settings SET login_failure_critical_count=2147483648`, "22003", ""},
		{"login failures order", `UPDATE system_settings SET login_failure_warning_count=100,login_failure_critical_count=100`, "23514", "system_settings_threshold_order_check"},
		{"authorization denials lower", `UPDATE system_settings SET authorization_denial_warning_count=0`, "23514", "system_settings_threshold_order_check"},
		{"authorization denials upper", `UPDATE system_settings SET authorization_denial_critical_count=2147483648`, "22003", ""},
		{"authorization denials order", `UPDATE system_settings SET authorization_denial_warning_count=200,authorization_denial_critical_count=200`, "23514", "system_settings_threshold_order_check"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, test.statement)
			assertAlertSettingsPostgresError(
				t,
				err,
				test.code,
				test.constraint,
			)
		})
	}
}

func TestAlertSettingsCanonicalKeyMigrationFailsClosedOnUnresolvedCollisions(
	t *testing.T,
) {
	t.Run("up", func(t *testing.T) {
		pool, ctx := migratedOperationsMonitoring(t)
		provider, closeProvider := migrationProvider(
			t,
			pool.Config().ConnString(),
		)
		t.Cleanup(closeProvider)
		legacyID := uuid.MustParse("26000000-0000-4000-8000-000000000011")
		canonicalID := uuid.MustParse("26000000-0000-4000-8000-000000000012")
		registerAlertSettingsMigrationRestore(
			t,
			pool,
			provider,
			legacyID,
			canonicalID,
		)
		assertAlertSettingsMigrationLatestClean(
			t,
			ctx,
			pool,
			legacyID,
			canonicalID,
		)
		if _, err := provider.DownTo(ctx, 25); err != nil {
			t.Fatal(err)
		}
		insertUnresolvedRemoteAlerts(
			t,
			ctx,
			pool,
			map[uuid.UUID]string{
				legacyID:    "backup_remote_replication",
				canonicalID: "backup_remote_sync",
			},
		)
		if _, err := provider.Up(ctx); err == nil {
			t.Fatal("up migration merged unresolved aliases")
		}
		if err := restoreAlertSettingsMigrationState(
			ctx,
			pool,
			provider,
			legacyID,
			canonicalID,
		); err != nil {
			t.Fatal(err)
		}
		assertAlertSettingsMigrationLatestClean(
			t,
			ctx,
			pool,
			legacyID,
			canonicalID,
		)
	})

	t.Run("down starts clean and restores removed constraint", func(t *testing.T) {
		pool, ctx := migratedOperationsMonitoring(t)
		provider, closeProvider := migrationProvider(
			t,
			pool.Config().ConnString(),
		)
		t.Cleanup(closeProvider)
		legacyID := uuid.MustParse("26000000-0000-4000-8000-000000000021")
		canonicalID := uuid.MustParse("26000000-0000-4000-8000-000000000022")
		registerAlertSettingsMigrationRestore(
			t,
			pool,
			provider,
			legacyID,
			canonicalID,
		)
		assertAlertSettingsMigrationLatestClean(
			t,
			ctx,
			pool,
			legacyID,
			canonicalID,
		)
		if _, err := pool.Exec(ctx, `
ALTER TABLE operational_alerts
DROP CONSTRAINT operational_alerts_no_legacy_remote_keys_check`); err != nil {
			t.Fatal(err)
		}
		insertUnresolvedRemoteAlerts(
			t,
			ctx,
			pool,
			map[uuid.UUID]string{
				legacyID:    "backup_remote_replication",
				canonicalID: "backup_remote_sync",
			},
		)
		if _, err := provider.DownTo(ctx, 25); err == nil {
			t.Fatal("down migration merged unresolved aliases")
		}
		if err := restoreAlertSettingsMigrationState(
			ctx,
			pool,
			provider,
			legacyID,
			canonicalID,
		); err != nil {
			t.Fatal(err)
		}
		assertAlertSettingsMigrationLatestClean(
			t,
			ctx,
			pool,
			legacyID,
			canonicalID,
		)
	})
}

func registerAlertSettingsMigrationRestore(
	t *testing.T,
	pool *pgxpool.Pool,
	provider *goose.Provider,
	firstID uuid.UUID,
	secondID uuid.UUID,
) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			alertSettingsMigrationCleanupTimeout,
		)
		defer cancel()
		if err := restoreAlertSettingsMigrationState(
			ctx,
			pool,
			provider,
			firstID,
			secondID,
		); err != nil {
			t.Errorf("restore alert settings migration state: %v", err)
		}
	})
}

func restoreAlertSettingsMigrationState(
	ctx context.Context,
	pool *pgxpool.Pool,
	provider *goose.Provider,
	firstID uuid.UUID,
	secondID uuid.UUID,
) error {
	if _, err := pool.Exec(ctx, `
DELETE FROM operational_alerts
WHERE id=$1 OR id=$2`, firstID, secondID); err != nil {
		return fmt.Errorf("delete collision fixtures: %w", err)
	}
	var columnsPresent, constraintPresent bool
	if err := pool.QueryRow(ctx, `
SELECT
  EXISTS(
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public'
      AND table_name='system_settings'
      AND column_name='backup_filesystem_warning_percent'
  ),
  EXISTS(
    SELECT 1 FROM pg_constraint
    WHERE conrelid='public.operational_alerts'::regclass
      AND conname='operational_alerts_no_legacy_remote_keys_check'
  )`).Scan(&columnsPresent, &constraintPresent); err != nil {
		return fmt.Errorf("inspect alert migration schema: %w", err)
	}
	if columnsPresent && !constraintPresent {
		if _, err := pool.Exec(ctx, `
ALTER TABLE operational_alerts
ADD CONSTRAINT operational_alerts_no_legacy_remote_keys_check CHECK (
  dedupe_key NOT IN (
    'backup_remote_replication',
    'backup_remote_replication_dependency_unavailable'
  )
)`); err != nil {
			return fmt.Errorf("restore legacy-key constraint: %w", err)
		}
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("restore latest migrations: %w", err)
	}
	if err := resetAlertThresholdSettings(ctx, pool); err != nil {
		return err
	}
	return verifyAlertSettingsMigrationLatestClean(
		ctx,
		pool,
		firstID,
		secondID,
	)
}

func resetAlertThresholdSettings(
	ctx context.Context,
	pool *pgxpool.Pool,
) error {
	if _, err := pool.Exec(ctx, `
UPDATE system_settings
SET disk_warning_percent=75,disk_critical_percent=90,
    backup_filesystem_warning_percent=75,
    backup_filesystem_critical_percent=90,
    local_backup_age_warning_hours=25,
    local_backup_age_critical_hours=30,
    ai_error_warning_percent=10,ai_error_critical_percent=25,
    processing_queue_warning=20,processing_queue_critical=100,
    processing_failure_warning_count=5,
    processing_failure_critical_count=20,
    login_failure_warning_count=20,login_failure_critical_count=100,
    authorization_denial_warning_count=50,
    authorization_denial_critical_count=200
WHERE singleton_id=true`); err != nil {
		return fmt.Errorf("reset alert threshold settings: %w", err)
	}
	return nil
}

func verifyAlertSettingsMigrationLatestClean(
	ctx context.Context,
	pool *pgxpool.Pool,
	firstID uuid.UUID,
	secondID uuid.UUID,
) error {
	var version, columns, constraints, fixtures int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT max(version_id) FROM goose_db_version WHERE is_applied),
  (SELECT count(*) FROM information_schema.columns
   WHERE table_schema='public'
     AND table_name='system_settings'
     AND column_name IN (
       'backup_filesystem_warning_percent',
       'authorization_denial_critical_count'
     )),
  (SELECT count(*) FROM pg_constraint
   WHERE conrelid='public.operational_alerts'::regclass
     AND conname='operational_alerts_no_legacy_remote_keys_check'),
  (SELECT count(*) FROM operational_alerts WHERE id=$1 OR id=$2)`,
		firstID,
		secondID,
	).Scan(&version, &columns, &constraints, &fixtures); err != nil {
		return fmt.Errorf("verify clean alert migration state: %w", err)
	}
	if version != 27 || columns != 2 || constraints != 1 || fixtures != 0 {
		return fmt.Errorf(
			"dirty alert migration state: version=%d columns=%d constraints=%d fixtures=%d",
			version,
			columns,
			constraints,
			fixtures,
		)
	}
	return nil
}

func assertAlertSettingsMigrationLatestClean(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	firstID uuid.UUID,
	secondID uuid.UUID,
) {
	t.Helper()
	if err := verifyAlertSettingsMigrationLatestClean(
		ctx,
		pool,
		firstID,
		secondID,
	); err != nil {
		t.Fatal(err)
	}
}

func insertUnresolvedRemoteAlerts(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixtures map[uuid.UUID]string,
) {
	t.Helper()
	for id, key := range fixtures {
		if _, err := pool.Exec(ctx, `
INSERT INTO operational_alerts(
  id,dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary
) VALUES($1,$2,'backup','warning','open',now(),now(),0,1,'remote')`,
			id,
			key,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func assertAlertSettingsPostgresError(
	t *testing.T,
	err error,
	code string,
	constraint string,
) {
	t.Helper()
	var postgresErr *pgconn.PgError
	if err == nil ||
		!errors.As(err, &postgresErr) ||
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
