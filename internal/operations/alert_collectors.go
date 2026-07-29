package operations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const alertCollectorQueryTimeout = 2 * time.Second

type AlertCollection struct {
	Samples                     []Sample
	RemoteReplicationConfigured *bool
}

type AlertSampleCollector interface {
	Collect(context.Context, time.Time) (AlertCollection, error)
}

type alertCollectorDB interface {
	QueryRow(context.Context, string, ...any) dashboardRow
}

type postgresActivityAlertCollector struct {
	db alertCollectorDB
}

type postgresBackupAlertCollector struct {
	db alertCollectorDB
}

func NewPostgresAlertCollectors(pool *pgxpool.Pool) []AlertSampleCollector {
	database := postgresDashboardDB{pool: pool}
	return []AlertSampleCollector{
		newPostgresBackupAlertCollectorDB(database),
		newPostgresActivityAlertCollectorDB(database),
	}
}

func newPostgresActivityAlertCollectorDB(
	db alertCollectorDB,
) AlertSampleCollector {
	return &postgresActivityAlertCollector{db: db}
}

func newPostgresBackupAlertCollectorDB(
	db alertCollectorDB,
) AlertSampleCollector {
	return &postgresBackupAlertCollector{db: db}
}

func (collector *postgresActivityAlertCollector) Collect(
	ctx context.Context,
	now time.Time,
) (AlertCollection, error) {
	if collector == nil || collector.db == nil {
		return AlertCollection{}, errStoreClosed
	}
	if ctx == nil || !validSampleTime(now) {
		return AlertCollection{}, ErrInvalid
	}
	now = now.UTC()
	windowStart := now.Add(-15 * time.Minute)
	queryCtx, cancel := context.WithTimeout(ctx, alertCollectorQueryTimeout)
	defer cancel()
	var (
		aiSucceeded, aiFailed              int64
		processingQueued, processingFailed int64
		loginFailed, authorizationDenied   int64
	)
	err := collector.db.QueryRow(queryCtx, `
SELECT
  (
    SELECT count(*)::bigint
    FROM ai_runs
    WHERE status='succeeded'
      AND completed_at >= $2
      AND completed_at <= $1
  ),
  (
    SELECT count(*)::bigint
    FROM ai_runs
    WHERE status='failed'
      AND completed_at >= $2
      AND completed_at <= $1
  ),
  (
    SELECT count(*)::bigint
    FROM file_processing_jobs
    WHERE attempts<4
      AND available_at <= $1
      AND created_at <= $1
      AND (
        state='queued'
        OR (state='running' AND lease_until < $1)
      )
  ),
  (
    SELECT count(*)::bigint
    FROM file_processing_jobs
    WHERE (
      state='failed'
      AND updated_at >= $2
      AND updated_at <= $1
    ) OR (
      state='running'
      AND attempts>=4
      AND lease_until >= $2
      AND lease_until < $1
      AND created_at <= $1
    )
  ),
  (
    SELECT count(*)::bigint
    FROM login_events
    WHERE success=false
      AND occurred_at >= $2
      AND occurred_at <= $1
  ),
  (
    SELECT count(*)::bigint
    FROM audit_logs
    WHERE action LIKE 'authorization.%'
      AND metadata->>'outcome'='denied'
      AND occurred_at >= $2
      AND occurred_at <= $1
  )`,
		now,
		windowStart,
	).Scan(
		&aiSucceeded,
		&aiFailed,
		&processingQueued,
		&processingFailed,
		&loginFailed,
		&authorizationDenied,
	)
	if err != nil {
		return AlertCollection{}, err
	}
	for _, value := range []int64{
		aiSucceeded,
		aiFailed,
		processingQueued,
		processingFailed,
		loginFailed,
		authorizationDenied,
	} {
		if !validDashboardCount(value) {
			return AlertCollection{}, ErrInvalid
		}
	}
	window := windowStart
	return AlertCollection{Samples: []Sample{
		{
			Source: SampleSourceApp, Metric: SampleMetricAIRequestsTotal,
			Scope: SampleScopeSucceeded, Value: float64(aiSucceeded),
			Unit: SampleUnitCount, ObservedAt: now, WindowStartedAt: &window,
		},
		{
			Source: SampleSourceApp, Metric: SampleMetricAIRequestsTotal,
			Scope: SampleScopeFailed, Value: float64(aiFailed),
			Unit: SampleUnitCount, ObservedAt: now, WindowStartedAt: &window,
		},
		{
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: float64(processingQueued),
			Unit: SampleUnitCount, ObservedAt: now,
		},
		{
			Source: SampleSourceWorker, Metric: SampleMetricQueueFailuresTotal,
			Scope: SampleScopeProcessing, Value: float64(processingFailed),
			Unit: SampleUnitCount, ObservedAt: now, WindowStartedAt: &window,
		},
		{
			Source: SampleSourceApp, Metric: SampleMetricSecurityEventsTotal,
			Scope: SampleScopeLoginFailure, Value: float64(loginFailed),
			Unit: SampleUnitCount, ObservedAt: now, WindowStartedAt: &window,
		},
		{
			Source: SampleSourceApp, Metric: SampleMetricSecurityEventsTotal,
			Scope: SampleScopeAuthorizationDenial,
			Value: float64(authorizationDenied), Unit: SampleUnitCount,
			ObservedAt: now, WindowStartedAt: &window,
		},
	}}, nil
}

func (collector *postgresBackupAlertCollector) Collect(
	ctx context.Context,
	now time.Time,
) (AlertCollection, error) {
	if collector == nil || collector.db == nil {
		return AlertCollection{}, errStoreClosed
	}
	if ctx == nil || !validSampleTime(now) {
		return AlertCollection{}, ErrInvalid
	}
	now = now.UTC()
	queryCtx, cancel := context.WithTimeout(ctx, alertCollectorQueryTimeout)
	defer cancel()
	var (
		localFinishedAt  *time.Time
		remoteConfigured *bool
		remoteUp         *bool
	)
	err := collector.db.QueryRow(queryCtx, `
WITH latest AS (
  SELECT id,state,finished_at,remote_snapshot_id,error_category
  FROM backup_runs
  WHERE state IN ('succeeded','degraded')
    AND finished_at IS NOT NULL
    AND finished_at <= $1
    AND local_snapshot_id IS NOT NULL
    AND btrim(local_snapshot_id)<>''
  ORDER BY finished_at DESC,id DESC
  LIMIT 1
)
SELECT
  finished_at,
  CASE
    WHEN state='degraded'
      AND (
        error_category='remote_unavailable'
        OR error_category='remote_sync'
      ) THEN true
    WHEN state='succeeded'
      AND remote_snapshot_id IS NOT NULL
      AND btrim(remote_snapshot_id)<>'' THEN true
    WHEN state='succeeded' THEN false
    ELSE NULL
  END AS remote_configured,
  CASE
    WHEN state='degraded'
      AND (
        error_category='remote_unavailable'
        OR error_category='remote_sync'
      ) THEN false
    WHEN state='succeeded'
      AND remote_snapshot_id IS NOT NULL
      AND btrim(remote_snapshot_id)<>'' THEN true
    ELSE NULL
  END AS remote_up
FROM latest`,
		now,
	).Scan(&localFinishedAt, &remoteConfigured, &remoteUp)
	if errors.Is(err, pgx.ErrNoRows) {
		return AlertCollection{Samples: []Sample{}}, nil
	}
	if err != nil {
		return AlertCollection{}, err
	}
	if localFinishedAt == nil || !validSampleTime(*localFinishedAt) ||
		localFinishedAt.After(now) ||
		remoteConfigured == nil ||
		(!*remoteConfigured && remoteUp != nil) ||
		(*remoteConfigured && remoteUp == nil) {
		return AlertCollection{}, ErrInvalid
	}
	age := now.Sub(localFinishedAt.UTC()).Seconds()
	if !validSampleValue(age, SampleUnitSeconds) {
		return AlertCollection{}, ErrInvalid
	}
	samples := []Sample{{
		Source: SampleSourceApp, Metric: SampleMetricBackupAgeSeconds,
		Scope: SampleScopeLocal, Value: age,
		Unit: SampleUnitSeconds, ObservedAt: now,
	}}
	if *remoteConfigured {
		value := float64(0)
		if *remoteUp {
			value = 1
		}
		samples = append(samples, Sample{
			Source: SampleSourceApp, Metric: SampleMetricBackupRemoteUp,
			Scope: SampleScopeRemote, Value: value,
			Unit: SampleUnitBoolean, ObservedAt: now,
		})
	}
	configured := *remoteConfigured
	return AlertCollection{
		Samples: samples, RemoteReplicationConfigured: &configured,
	}, nil
}
