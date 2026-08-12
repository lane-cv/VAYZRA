package operations

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/queuepolicy"
)

const dashboardQueueFailureWindow = 15 * time.Minute

type dashboardRow interface {
	Scan(...any) error
}

type dashboardRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

type dashboardDB interface {
	QueryRow(context.Context, string, ...any) dashboardRow
	Query(context.Context, string, ...any) (dashboardRows, error)
}

type postgresDashboardDB struct {
	pool *pgxpool.Pool
}

func (database postgresDashboardDB) QueryRow(
	ctx context.Context,
	query string,
	args ...any,
) dashboardRow {
	return database.pool.QueryRow(ctx, query, args...)
}

func (database postgresDashboardDB) Query(
	ctx context.Context,
	query string,
	args ...any,
) (dashboardRows, error) {
	return database.pool.Query(ctx, query, args...)
}

type PostgresDashboardReader struct {
	db dashboardDB
}

var (
	_ StudentDashboardReader  = (*PostgresDashboardReader)(nil)
	_ QuestionDashboardReader = (*PostgresDashboardReader)(nil)
	_ AIDashboardReader       = (*PostgresDashboardReader)(nil)
	_ QueueDashboardReader    = (*PostgresDashboardReader)(nil)
	_ BackupDashboardReader   = (*PostgresDashboardReader)(nil)
	_ AlertDashboardReader    = (*PostgresDashboardReader)(nil)
	_ AuditDashboardReader    = (*PostgresDashboardReader)(nil)
)

func NewPostgresDashboardReader(pool *pgxpool.Pool) *PostgresDashboardReader {
	if pool == nil {
		return &PostgresDashboardReader{}
	}
	return newPostgresDashboardReaderDB(postgresDashboardDB{pool: pool})
}

func newPostgresDashboardReaderDB(db dashboardDB) *PostgresDashboardReader {
	return &PostgresDashboardReader{db: db}
}

func (reader *PostgresDashboardReader) ReadStudentSummary(
	ctx context.Context,
	now time.Time,
) (StudentSummary, error) {
	now, err := reader.validateRequest(ctx, now)
	if err != nil {
		return StudentSummary{}, err
	}
	var active, disabled, unknown, future int64
	err = reader.db.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE status='active')::bigint,
  count(*) FILTER (WHERE status='disabled')::bigint,
  count(*) FILTER (WHERE status NOT IN ('active','disabled'))::bigint,
  count(*) FILTER (WHERE created_at>$1 OR updated_at>$1)::bigint
FROM users
WHERE role='student' AND deleted_at IS NULL`,
		now,
	).Scan(&active, &disabled, &unknown, &future)
	if err != nil {
		return StudentSummary{}, err
	}
	if !validDashboardCount(active) || !validDashboardCount(disabled) ||
		unknown != 0 || future != 0 {
		return StudentSummary{}, ErrInvalid
	}
	state := DataStateHealthy
	if active == 0 && disabled == 0 {
		state = DataStateEmpty
	}
	return StudentSummary{
		State:      state,
		ObservedAt: dashboardObservation(now),
		Active:     active,
		Disabled:   disabled,
	}, nil
}

func (reader *PostgresDashboardReader) ReadQuestionSummary(
	ctx context.Context,
	now time.Time,
) (QuestionSummary, error) {
	now, err := reader.validateRequest(ctx, now)
	if err != nil {
		return QuestionSummary{}, err
	}
	var waiting, oldestWaitSeconds, unknown, future int64
	err = reader.db.QueryRow(ctx, `
SELECT
  count(*) FILTER (
    WHERE q.status IN ('pending','in_progress')
  )::bigint,
  COALESCE(floor(extract(epoch FROM (
    $1::timestamptz - min(q.last_message_at) FILTER (
      WHERE q.status IN ('pending','in_progress')
    )
  ))),0)::bigint,
  count(*) FILTER (
    WHERE q.status NOT IN ('pending','in_progress','waiting_student','completed')
  )::bigint,
  count(*) FILTER (
    WHERE q.last_message_at>$1 OR q.created_at>$1 OR q.updated_at>$1
  )::bigint
FROM qa_threads AS q
JOIN users AS u
  ON u.id=q.student_id
 AND u.role='student'
 AND u.deleted_at IS NULL`,
		now,
	).Scan(&waiting, &oldestWaitSeconds, &unknown, &future)
	if err != nil {
		return QuestionSummary{}, err
	}
	if !validDashboardCount(waiting) ||
		!validDashboardSeconds(oldestWaitSeconds) ||
		(waiting == 0 && oldestWaitSeconds != 0) ||
		unknown != 0 || future != 0 {
		return QuestionSummary{}, ErrInvalid
	}
	state := DataStateHealthy
	if waiting == 0 {
		state = DataStateEmpty
	}
	return QuestionSummary{
		State:             state,
		ObservedAt:        dashboardObservation(now),
		Waiting:           waiting,
		OldestWaitSeconds: oldestWaitSeconds,
	}, nil
}

func (reader *PostgresDashboardReader) ReadAISummary(
	ctx context.Context,
	now time.Time,
) (AISummary, error) {
	now, err := reader.validateRequest(ctx, now)
	if err != nil {
		return AISummary{}, err
	}
	dayStart := shanghaiDayStart(now)
	var (
		requests, succeeded, terminal            int64
		firstByteMilliseconds, totalMilliseconds int64
		costMicroUSD, unknownUsage               int64
		unknown, future                          int64
	)
	err = reader.db.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE created_at <= $1)::bigint,
  count(*) FILTER (
    WHERE created_at <= $1 AND status='succeeded'
  )::bigint,
  count(*) FILTER (
    WHERE created_at <= $1
      AND status IN ('succeeded','failed','cancelled')
  )::bigint,
  COALESCE(floor(avg(first_byte_ms) FILTER (
    WHERE created_at <= $1
  )),0)::bigint,
  COALESCE(floor(avg(total_ms) FILTER (
    WHERE created_at <= $1
  )),0)::bigint,
  COALESCE(sum(cost_micro_usd) FILTER (
    WHERE created_at <= $1
  ),0)::bigint,
  count(*) FILTER (
    WHERE created_at <= $1
      AND status IN ('succeeded','failed','cancelled')
      AND usage_source='unknown'
  )::bigint,
  count(*) FILTER (
    WHERE status NOT IN ('queued','streaming','succeeded','failed','cancelled')
  )::bigint,
  count(*) FILTER (
    WHERE created_at > $1 OR updated_at > $1
      OR started_at > $1 OR completed_at > $1
  )::bigint
FROM ai_runs
WHERE created_at >= $2`,
		now, dayStart,
	).Scan(
		&requests,
		&succeeded,
		&terminal,
		&firstByteMilliseconds,
		&totalMilliseconds,
		&costMicroUSD,
		&unknownUsage,
		&unknown,
		&future,
	)
	if err != nil {
		return AISummary{}, err
	}
	if !validDashboardCount(requests) ||
		!validDashboardCount(succeeded) ||
		!validDashboardCount(terminal) ||
		succeeded > terminal || terminal > requests ||
		!validDashboardCount(unknownUsage) ||
		unknownUsage > terminal ||
		!validDashboardDurationMilliseconds(firstByteMilliseconds) ||
		!validDashboardDurationMilliseconds(totalMilliseconds) ||
		!validDashboardCount(costMicroUSD) ||
		(requests == 0 &&
			(firstByteMilliseconds != 0 ||
				totalMilliseconds != 0 ||
				costMicroUSD != 0)) ||
		unknown != 0 || future != 0 {
		return AISummary{}, ErrInvalid
	}
	successRate := float64(0)
	if terminal > 0 {
		successRate = float64(succeeded) * 100 / float64(terminal)
	}
	if !validDashboardRate(successRate) {
		return AISummary{}, ErrInvalid
	}
	state := DataStateHealthy
	if requests == 0 {
		state = DataStateEmpty
	} else if unknownUsage > 0 {
		state = DataStateDegraded
	}
	return AISummary{
		State:                        state,
		ObservedAt:                   dashboardObservation(now),
		Requests:                     requests,
		SuccessRatePercent:           successRate,
		FirstByteLatencyMilliseconds: firstByteMilliseconds,
		TotalLatencyMilliseconds:     totalMilliseconds,
		DailyCostMicroUSD:            costMicroUSD,
	}, nil
}

func (reader *PostgresDashboardReader) ReadQueueSummaries(
	ctx context.Context,
	now time.Time,
) ([]QueueSummary, error) {
	now, err := reader.validateRequest(ctx, now)
	if err != nil {
		return nil, err
	}
	windowStart := now.Add(-dashboardQueueFailureWindow)
	rows, err := reader.db.Query(ctx, `
WITH queue_rows AS (
  SELECT
    1 AS queue_order,
    'processing'::text AS queue,
    count(*) FILTER (
      WHERE attempts<4
        AND available_at <= $1
        AND created_at <= $1
        AND (
          state='queued'
          OR (state='running' AND lease_until < $1)
        )
    )::bigint AS queued,
    count(*) FILTER (
      WHERE state='running' AND lease_until >= $1
        AND created_at <= $1
    )::bigint AS streaming,
    count(*) FILTER (
      WHERE (
        state='failed' AND updated_at >= $2 AND updated_at <= $1
      ) OR (
        state='running' AND lease_until < $1 AND attempts>=4
      )
    )::bigint AS failed,
    0::bigint AS expired,
    count(*) FILTER (
      WHERE state NOT IN ('queued','running','completed','failed')
    )::bigint AS unknown,
    count(*) FILTER (
      WHERE created_at>$1 OR updated_at>$1
    )::bigint AS future
  FROM file_processing_jobs
  WHERE state IN ('queued','running')
    OR (state='failed' AND updated_at >= $2)
    OR state NOT IN ('queued','running','completed','failed')

  UNION ALL

  SELECT
    2,
    'ai',
    count(*) FILTER (
      WHERE status='queued' AND created_at <= $1
    )::bigint,
    count(*) FILTER (
      WHERE status='streaming' AND lease_expires_at >= $1
        AND created_at <= $1
    )::bigint,
    count(*) FILTER (
      WHERE status='failed'
        AND completed_at >= $2 AND completed_at <= $1
    )::bigint,
    count(*) FILTER (
      WHERE status='streaming' AND lease_expires_at < $1
        AND created_at <= $1
    )::bigint,
    count(*) FILTER (
      WHERE status NOT IN ('queued','streaming','succeeded','failed','cancelled')
    )::bigint,
    count(*) FILTER (
      WHERE created_at>$1 OR updated_at>$1
        OR started_at>$1 OR completed_at>$1
    )::bigint
  FROM ai_runs
  WHERE status IN ('queued','streaming')
    OR (status='failed' AND completed_at >= $2)
    OR status NOT IN ('queued','streaming','succeeded','failed','cancelled')

  UNION ALL

  SELECT
    3,
    'outbox',
    count(*) FILTER (
      WHERE published_at IS NULL
        AND next_attempt_at <= $1
        AND attempts<$3
        AND (lease_until IS NULL OR lease_until <= $1)
        AND created_at <= $1
    )::bigint,
    count(*) FILTER (
      WHERE published_at IS NULL
        AND lease_owner IS NOT NULL AND lease_until > $1
        AND created_at <= $1
    )::bigint,
    count(*) FILTER (
      WHERE (
        published_at >= $2 AND published_at <= $1
          AND last_error_category IS NOT NULL
          AND last_error_category<>''
      ) OR (
        published_at IS NULL
          AND attempts>=$3
          AND (lease_until IS NULL OR lease_until <= $1)
      )
    )::bigint,
    0::bigint,
    count(*) FILTER (
      WHERE published_at IS NULL
        AND ((lease_owner IS NULL)<>(lease_until IS NULL))
    )::bigint,
    count(*) FILTER (
      WHERE created_at>$1 OR published_at>$1
    )::bigint
  FROM outbox_events
  WHERE published_at IS NULL
    OR (published_at >= $2 AND last_error_category IS NOT NULL)
)
SELECT queue,queued,streaming,failed,expired,unknown,future
FROM queue_rows
ORDER BY queue_order`,
		now, windowStart, queuepolicy.OutboxMaxAttempts,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	order := DashboardQueueOrder()
	result := make([]QueueSummary, 0, len(order))
	for rows.Next() {
		var (
			queueRaw                           string
			queued, streaming, failed, expired int64
			unknown, future                    int64
		)
		if err := rows.Scan(
			&queueRaw,
			&queued,
			&streaming,
			&failed,
			&expired,
			&unknown,
			&future,
		); err != nil {
			return nil, err
		}
		if len(result) >= len(order) ||
			DashboardQueue(queueRaw) != order[len(result)] ||
			!validDashboardCount(queued) ||
			!validDashboardCount(streaming) ||
			!validDashboardCount(failed) ||
			!validDashboardCount(expired) ||
			unknown != 0 || future != 0 {
			return nil, ErrInvalid
		}
		state := DataStateHealthy
		if failed > 0 || expired > 0 {
			state = DataStateDegraded
		}
		result = append(result, QueueSummary{
			Queue:      DashboardQueue(queueRaw),
			State:      state,
			ObservedAt: dashboardObservation(now),
			Queued:     queued,
			Streaming:  streaming,
			Failed:     failed,
			Expired:    expired,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(order) {
		return nil, ErrInvalid
	}
	return result, nil
}

func (reader *PostgresDashboardReader) ReadBackupSummary(
	ctx context.Context,
	now time.Time,
) (BackupSummary, error) {
	now, err := reader.validateRequest(ctx, now)
	if err != nil {
		return BackupSummary{}, err
	}
	var (
		localState, remoteState, restoreState *string
		localAt, remoteAt, restoreAt          *time.Time
		rtoSeconds, unknown, future           int64
	)
	err = reader.db.QueryRow(ctx, `
SELECT
  local.state,
  local.finished_at,
  remote.state,
  remote.finished_at,
  restore.state,
  restore.finished_at,
  COALESCE(restore.rto_seconds,0)::bigint,
  (
    SELECT count(*) FROM backup_runs
    WHERE finished_at IS NOT NULL
      AND state NOT IN ('succeeded','degraded','failed')
  ) + (
    SELECT count(*) FROM restore_verifications
    WHERE finished_at IS NOT NULL
      AND state NOT IN ('succeeded','failed')
  ) AS unknown,
  (
    SELECT count(*) FROM backup_runs WHERE finished_at>$1
  ) + (
    SELECT count(*) FROM restore_verifications WHERE finished_at>$1
  ) AS future
FROM (VALUES (1)) AS root(singleton)
LEFT JOIN LATERAL (
  SELECT
    CASE
      WHEN state IN ('succeeded','degraded')
        AND local_snapshot_id IS NOT NULL
        AND btrim(local_snapshot_id)<>'' THEN 'succeeded'
      WHEN state='failed' THEN 'failed'
      ELSE NULL
    END AS state,
    finished_at
  FROM backup_runs
  WHERE state IN ('succeeded','degraded','failed')
    AND finished_at IS NOT NULL
    AND finished_at <= $1
  ORDER BY finished_at DESC,id DESC
  LIMIT 1
) AS local ON true
LEFT JOIN LATERAL (
  SELECT
    CASE
      WHEN state='degraded' THEN 'degraded'
      WHEN state='succeeded'
        AND remote_snapshot_id IS NOT NULL
        AND btrim(remote_snapshot_id)<>'' THEN 'succeeded'
      WHEN state='failed'
        AND remote_snapshot_id IS NOT NULL
        AND btrim(remote_snapshot_id)<>'' THEN 'failed'
      ELSE NULL
    END AS state,
    finished_at
  FROM backup_runs
  WHERE state IN ('succeeded','degraded','failed')
    AND finished_at IS NOT NULL
    AND finished_at <= $1
    AND (
      state='degraded'
      OR (remote_snapshot_id IS NOT NULL AND btrim(remote_snapshot_id)<>'')
    )
  ORDER BY finished_at DESC,id DESC
  LIMIT 1
) AS remote ON true
LEFT JOIN LATERAL (
  SELECT
    state,
    finished_at,
    CASE WHEN state='succeeded' THEN COALESCE(rto_seconds,0) ELSE 0 END
      AS rto_seconds
  FROM restore_verifications
  WHERE state IN ('succeeded','failed')
    AND finished_at IS NOT NULL
    AND finished_at <= $1
  ORDER BY finished_at DESC,id DESC
  LIMIT 1
) AS restore ON true`,
		now,
	).Scan(
		&localState,
		&localAt,
		&remoteState,
		&remoteAt,
		&restoreState,
		&restoreAt,
		&rtoSeconds,
		&unknown,
		&future,
	)
	if err != nil {
		return BackupSummary{}, err
	}
	if unknown != 0 || future != 0 || !validDashboardSeconds(rtoSeconds) {
		return BackupSummary{}, ErrInvalid
	}
	local, ok := dashboardBackupPoint(localState, localAt, now, false)
	if !ok {
		return BackupSummary{}, ErrInvalid
	}
	remote, ok := dashboardBackupPoint(remoteState, remoteAt, now, true)
	if !ok {
		return BackupSummary{}, ErrInvalid
	}
	restore, ok := dashboardRestorePoint(
		restoreState,
		restoreAt,
		rtoSeconds,
		now,
	)
	if !ok {
		return BackupSummary{}, ErrInvalid
	}
	state := DataStateHealthy
	if local.State == RecoveryStateEmpty &&
		remote.State == RecoveryStateEmpty &&
		restore.State == RecoveryStateEmpty {
		state = DataStateEmpty
	} else if local.State == RecoveryStateDegraded ||
		local.State == RecoveryStateFailed ||
		remote.State == RecoveryStateDegraded ||
		remote.State == RecoveryStateFailed ||
		restore.State == RecoveryStateDegraded ||
		restore.State == RecoveryStateFailed {
		state = DataStateDegraded
	}
	return BackupSummary{
		State:      state,
		ObservedAt: dashboardObservation(now),
		Local:      local,
		Remote:     remote,
		Restore:    restore,
	}, nil
}

func (reader *PostgresDashboardReader) ReadAlertSummary(
	ctx context.Context,
	now time.Time,
) (AlertSummary, error) {
	now, err := reader.validateRequest(ctx, now)
	if err != nil {
		return AlertSummary{}, err
	}
	var warning, critical, unknown, future int64
	err = reader.db.QueryRow(ctx, `
SELECT
  count(*) FILTER (
    WHERE state IN ('open','acknowledged')
      AND severity='warning'
      AND (consecutive_failures >= 2 OR version >= 2)
      AND last_observed_at <= $1
  )::bigint,
  count(*) FILTER (
    WHERE state IN ('open','acknowledged')
      AND severity='critical'
      AND (consecutive_failures >= 2 OR version >= 2)
      AND last_observed_at <= $1
  )::bigint,
  count(*) FILTER (
    WHERE state NOT IN ('open','acknowledged','resolved')
      OR severity NOT IN ('warning','critical')
  )::bigint,
  count(*) FILTER (
    WHERE first_observed_at>$1 OR last_observed_at>$1
      OR acknowledged_at>$1 OR resolved_at>$1
  )::bigint
FROM operational_alerts`,
		now,
	).Scan(&warning, &critical, &unknown, &future)
	if err != nil {
		return AlertSummary{}, err
	}
	if !validDashboardCount(warning) ||
		!validDashboardCount(critical) ||
		unknown != 0 || future != 0 {
		return AlertSummary{}, ErrInvalid
	}
	return AlertSummary{
		State:        DataStateHealthy,
		ObservedAt:   dashboardObservation(now),
		OpenWarning:  warning,
		OpenCritical: critical,
	}, nil
}

func (reader *PostgresDashboardReader) ReadRecentAudit(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]AuditSummary, error) {
	now, err := reader.validateRequest(ctx, now)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxRecentAudit {
		return nil, ErrInvalid
	}
	rows, err := reader.db.Query(ctx, `
SELECT
  CASE
    WHEN action LIKE 'auth.%' OR action LIKE 'login.%'
      THEN 'authentication'
    WHEN action LIKE 'authorization.%'
      THEN 'authorization'
    WHEN action LIKE 'file.%'
      THEN 'files'
    WHEN action LIKE 'student.%'
      OR action LIKE 'catalog.%'
      OR action LIKE 'lesson.%'
      OR action LIKE 'qa.%'
      THEN 'teaching'
    WHEN action LIKE 'ai.%'
      THEN 'ai'
    WHEN action='operations.backup_requested'
      OR action LIKE 'backup.%'
      THEN 'backup'
    WHEN action LIKE 'operations.%'
      THEN 'operations'
    ELSE 'unknown'
  END AS category,
  CASE
    WHEN metadata->>'outcome' IN ('succeeded','failed','denied','rejected','attempted')
      THEN metadata->>'outcome'
    ELSE 'unknown'
  END AS outcome,
  occurred_at,
  occurred_at > $1 AS future
FROM audit_logs
ORDER BY occurred_at DESC,id DESC
LIMIT $2`,
		now, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AuditSummary, 0, limit)
	for rows.Next() {
		var categoryRaw, outcomeRaw string
		var occurredAt time.Time
		var future bool
		if err := rows.Scan(&categoryRaw, &outcomeRaw, &occurredAt, &future); err != nil {
			return nil, err
		}
		category := AuditCategory(categoryRaw)
		outcome := AuditOutcome(outcomeRaw)
		if len(result) >= limit ||
			!validAuditCategory(category) ||
			!validAuditOutcome(outcome) ||
			!validDashboardTime(occurredAt) ||
			future || occurredAt.After(now) ||
			(len(result) > 0 &&
				occurredAt.After(result[len(result)-1].OccurredAt)) {
			return nil, ErrInvalid
		}
		result = append(result, AuditSummary{
			Category:   category,
			Outcome:    outcome,
			OccurredAt: occurredAt.UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (reader *PostgresDashboardReader) validateRequest(
	ctx context.Context,
	now time.Time,
) (time.Time, error) {
	if reader == nil || reader.db == nil {
		return time.Time{}, errStoreClosed
	}
	if ctx == nil {
		return time.Time{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if !validDashboardTime(now) {
		return time.Time{}, ErrInvalid
	}
	return now.UTC(), nil
}

func shanghaiDayStart(now time.Time) time.Time {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	local := now.In(location)
	return time.Date(
		local.Year(),
		local.Month(),
		local.Day(),
		0, 0, 0, 0,
		location,
	).UTC()
}

func dashboardObservation(now time.Time) *time.Time {
	copy := now.UTC()
	return &copy
}

func dashboardBackupPoint(
	state *string,
	completedAt *time.Time,
	now time.Time,
	allowDegraded bool,
) (BackupPointSummary, bool) {
	if state == nil {
		return BackupPointSummary{State: RecoveryStateEmpty}, completedAt == nil
	}
	recoveryState := RecoveryState(*state)
	if !validRecoveryState(recoveryState) ||
		recoveryState == RecoveryStateEmpty ||
		(recoveryState == RecoveryStateDegraded && !allowDegraded) ||
		completedAt == nil ||
		!validDashboardTime(*completedAt) ||
		completedAt.After(now) {
		return BackupPointSummary{}, false
	}
	return BackupPointSummary{
		State:       recoveryState,
		CompletedAt: dashboardObservation(*completedAt),
	}, true
}

func dashboardRestorePoint(
	state *string,
	completedAt *time.Time,
	rtoSeconds int64,
	now time.Time,
) (RestorePointSummary, bool) {
	point, ok := dashboardBackupPoint(state, completedAt, now, false)
	if !ok {
		return RestorePointSummary{}, false
	}
	if point.State == RecoveryStateEmpty {
		return RestorePointSummary{State: RecoveryStateEmpty}, rtoSeconds == 0
	}
	if point.State == RecoveryStateFailed && rtoSeconds != 0 {
		return RestorePointSummary{}, false
	}
	return RestorePointSummary{
		State:       point.State,
		CompletedAt: point.CompletedAt,
		RTOSeconds:  rtoSeconds,
	}, true
}

var _ dashboardRow = pgx.Row(nil)
