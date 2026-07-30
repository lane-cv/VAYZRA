package operations

import (
	"context"
	"net"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/audit"
)

const (
	defaultOperationalSampleRetentionDays = 7
	maxOperationalSampleRetentionDays     = 30
	metadataRetentionDays                 = 365
	metadataRetentionBatch                = 1000
	retentionAdvisoryLockKey              = 845103122
	retentionUnlockTimeout                = 2 * time.Second
)

type RetentionResult struct {
	Samples              int64
	AlertDeliveries      int64
	Alerts               int64
	RestoreVerifications int64
	BackupRuns           int64
}

type RetentionRunner interface {
	RunOnce(context.Context, time.Time, int) (RetentionResult, error)
}

type PostgresRetentionRunner struct {
	pool *pgxpool.Pool
}

var _ RetentionRunner = (*PostgresRetentionRunner)(nil)

func NewPostgresRetentionRunner(pool *pgxpool.Pool) *PostgresRetentionRunner {
	return &PostgresRetentionRunner{pool: pool}
}

func (runner *PostgresRetentionRunner) RunOnce(
	ctx context.Context,
	now time.Time,
	retentionDays int,
) (RetentionResult, error) {
	if ctx == nil || !validSampleTime(now) ||
		retentionDays < 0 || retentionDays > maxOperationalSampleRetentionDays {
		return RetentionResult{}, ErrInvalid
	}
	if retentionDays == 0 {
		retentionDays = defaultOperationalSampleRetentionDays
	}
	if runner == nil || runner.pool == nil {
		return RetentionResult{}, errStoreClosed
	}
	now = now.UTC()
	sampleCutoff := now.AddDate(0, 0, -retentionDays)
	metadataCutoff := now.AddDate(0, 0, -metadataRetentionDays)

	conn, err := runner.pool.Acquire(ctx)
	if err != nil {
		return RetentionResult{}, err
	}
	releaseDirectly := true
	defer func() {
		if releaseDirectly {
			conn.Release()
		}
	}()

	var locked bool
	if err := conn.QueryRow(
		ctx,
		`SELECT pg_try_advisory_lock($1)`,
		retentionAdvisoryLockKey,
	).Scan(&locked); err != nil {
		return RetentionResult{}, err
	}
	if !locked {
		return RetentionResult{}, nil
	}
	releaseDirectly = false
	defer releaseRetentionConnection(conn)

	tx, err := conn.Begin(ctx)
	if err != nil {
		return RetentionResult{}, err
	}
	defer rollbackRetentionTransaction(tx)

	var result RetentionResult
	if result.Samples, err = deleteExpiredRetentionRows(
		ctx,
		tx,
		deleteExpiredSamplesSQL,
		sampleCutoff,
	); err != nil {
		return RetentionResult{}, err
	}
	if result.AlertDeliveries, err = deleteExpiredRetentionRows(
		ctx,
		tx,
		deleteExpiredAlertDeliveriesSQL,
		metadataCutoff,
	); err != nil {
		return RetentionResult{}, err
	}
	if result.Alerts, err = deleteExpiredRetentionRows(
		ctx,
		tx,
		deleteExpiredAlertsSQL,
		metadataCutoff,
	); err != nil {
		return RetentionResult{}, err
	}
	if result.RestoreVerifications, err = deleteExpiredRetentionRows(
		ctx,
		tx,
		deleteExpiredRestoreVerificationsSQL,
		metadataCutoff,
	); err != nil {
		return RetentionResult{}, err
	}
	if result.BackupRuns, err = deleteExpiredRetentionRows(
		ctx,
		tx,
		deleteExpiredBackupRunsSQL,
		metadataCutoff,
	); err != nil {
		return RetentionResult{}, err
	}
	if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{
		Action:     "operations.retention_completed",
		TargetType: "metadata_retention",
		TargetID:   "global",
		Metadata: map[string]any{
			"samples":              strconv.FormatInt(result.Samples, 10),
			"alertDeliveries":      strconv.FormatInt(result.AlertDeliveries, 10),
			"alerts":               strconv.FormatInt(result.Alerts, 10),
			"restoreVerifications": strconv.FormatInt(result.RestoreVerifications, 10),
			"backupRuns":           strconv.FormatInt(result.BackupRuns, 10),
		},
		RequestID: "operations-retention",
		IP:        net.ParseIP("127.0.0.1"),
	}); err != nil {
		return RetentionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RetentionResult{}, err
	}
	return result, nil
}

func deleteExpiredRetentionRows(
	ctx context.Context,
	tx pgx.Tx,
	query string,
	cutoff time.Time,
) (int64, error) {
	tag, err := tx.Exec(ctx, query, cutoff, metadataRetentionBatch)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func rollbackRetentionTransaction(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		retentionUnlockTimeout,
	)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func releaseRetentionConnection(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		retentionUnlockTimeout,
	)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRow(
		ctx,
		`SELECT pg_advisory_unlock($1)`,
		retentionAdvisoryLockKey,
	).Scan(&unlocked); err != nil || !unlocked {
		closeHijackedConnectionWithContext(ctx, conn)
		return
	}
	conn.Release()
}

const deleteExpiredSamplesSQL = `
WITH expired AS (
  SELECT id
  FROM operational_samples
  WHERE observed_at < $1
  ORDER BY observed_at,source,metric_name,scope,unit,id
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
DELETE FROM operational_samples AS samples
USING expired
WHERE samples.id=expired.id`

const deleteExpiredAlertDeliveriesSQL = `
WITH expired AS (
  SELECT delivery.id
  FROM alert_deliveries AS delivery
  JOIN operational_alerts AS alert ON alert.id=delivery.alert_id
  WHERE alert.state='resolved'
    AND delivery.finished_at < $1
    AND (
      (
        delivery.event_id IS NULL
        AND delivery.delivery_state IS NULL
        AND delivery.outcome IN ('succeeded','failed','cancelled')
      )
      OR (
        delivery.event_id IS NOT NULL
        AND delivery.delivery_state IN ('succeeded','failed','cancelled')
      )
    )
  ORDER BY
    delivery.finished_at,
    delivery.alert_id,
    delivery.attempt,
    delivery.destination,
    delivery.id
  LIMIT $2
  FOR UPDATE OF delivery SKIP LOCKED
)
DELETE FROM alert_deliveries AS deliveries
USING expired
WHERE deliveries.id=expired.id`

const deleteExpiredAlertsSQL = `
WITH expired AS (
  SELECT alert.id
  FROM operational_alerts AS alert
  WHERE alert.state='resolved'
    AND alert.resolved_at < $1
    AND NOT EXISTS (
      SELECT 1
      FROM alert_deliveries AS delivery
      WHERE delivery.alert_id=alert.id
    )
  ORDER BY alert.resolved_at,alert.last_observed_at,alert.id
  LIMIT $2
  FOR UPDATE OF alert SKIP LOCKED
)
DELETE FROM operational_alerts AS alerts
USING expired
WHERE alerts.id=expired.id`

const deleteExpiredRestoreVerificationsSQL = `
WITH expired AS (
  SELECT id
  FROM restore_verifications
  WHERE state IN ('succeeded','failed')
    AND finished_at < $1
  ORDER BY finished_at,backup_run_id,id
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
DELETE FROM restore_verifications AS verifications
USING expired
WHERE verifications.id=expired.id`

const deleteExpiredBackupRunsSQL = `
WITH expired AS (
  SELECT run.id
  FROM backup_runs AS run
  WHERE run.state IN ('succeeded','degraded','failed')
    AND run.finished_at < $1
    AND NOT EXISTS (
      SELECT 1
      FROM restore_verifications AS verification
      WHERE verification.backup_run_id=run.id
    )
  ORDER BY run.finished_at,run.requested_at,run.id
  LIMIT $2
  FOR UPDATE OF run SKIP LOCKED
)
DELETE FROM backup_runs AS runs
USING expired
WHERE runs.id=expired.id`
