package operations

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresWebhookDeliveryStore struct {
	pool *pgxpool.Pool
}

const claimWebhookDeliverySQL = `
WITH candidate AS (
  SELECT delivery.id
  FROM alert_deliveries delivery
  WHERE delivery.event_id IS NOT NULL
    AND delivery.delivery_state IN ('pending','claimed')
    AND (
      CASE
        WHEN delivery.delivery_state='pending' THEN delivery.scheduled_at
        WHEN delivery.delivery_state='claimed' THEN delivery.claim_expires_at
      END
    ) <= $1
    AND (
      delivery.attempt=1
      OR COALESCE((
        SELECT true
        FROM alert_deliveries previous
        WHERE previous.event_id=delivery.event_id
          AND previous.attempt=delivery.attempt-1
          AND previous.destination=delivery.destination
          AND previous.delivery_state='failed'
        LIMIT 1
      ),false)
    )
    AND NOT COALESCE((
      SELECT true
      FROM alert_deliveries active
      WHERE active.event_id=delivery.event_id
        AND active.id<>delivery.id
        AND active.delivery_state='claimed'
        AND active.claim_expires_at > $1
      LIMIT 1
    ),false)
    AND NOT COALESCE((
      SELECT true
      FROM alert_deliveries succeeded
      WHERE succeeded.event_id=delivery.event_id
        AND succeeded.delivery_state='succeeded'
      LIMIT 1
    ),false)
  ORDER BY
    (
      CASE
        WHEN delivery.delivery_state='pending' THEN delivery.scheduled_at
        WHEN delivery.delivery_state='claimed' THEN delivery.claim_expires_at
      END
    ),
    delivery.event_id,
    delivery.attempt,
    delivery.id
  FOR UPDATE OF delivery SKIP LOCKED
  LIMIT 1
),
claimed AS (
  UPDATE alert_deliveries delivery
  SET delivery_state='claimed',
      claim_owner=$2,
      claim_token=$3,
      claim_expires_at=$4,
      started_at=$1,
      finished_at=NULL,
      outcome=NULL,
      http_status_class=NULL,
      error_category=''
  FROM candidate
  WHERE delivery.id=candidate.id
  RETURNING delivery.*
)
SELECT
  claimed.id,claimed.event_id,claimed.alert_id,claimed.attempt,
  claimed.scheduled_at,claimed.started_at,claimed.claim_owner,
  claimed.claim_token,claimed.claim_expires_at,
  event.id,event.alert_id,event.transition_kind,event.alert_version,
  event.category,event.severity,event.state,event.summary,
  event.current_value,event.threshold_value,
  event.first_observed_at,event.last_observed_at
FROM claimed
JOIN alert_webhook_events event ON event.id=claimed.event_id`

func NewPostgresWebhookDeliveryStore(
	pool *pgxpool.Pool,
) *PostgresWebhookDeliveryStore {
	return &PostgresWebhookDeliveryStore{pool: pool}
}

func (store *PostgresWebhookDeliveryStore) Claim(
	ctx context.Context,
	owner string,
	token uuid.UUID,
	now time.Time,
	leaseDuration time.Duration,
) (*WebhookDeliveryJob, error) {
	if store == nil || store.pool == nil || ctx == nil ||
		!webhookClaimOwner.MatchString(owner) ||
		token == uuid.Nil ||
		!validSampleTime(now) ||
		leaseDuration <= 0 ||
		leaseDuration > maxWebhookLeaseDuration {
		return nil, ErrInvalid
	}
	now = postgresAlertTime(now)
	expiresAt := postgresAlertTime(now.Add(leaseDuration))
	row := store.pool.QueryRow(ctx, claimWebhookDeliverySQL,
		now,
		owner,
		token,
		expiresAt,
	)
	var (
		job           WebhookDeliveryJob
		eventKind     string
		eventSeverity string
		eventState    string
		eventID       uuid.UUID
		eventAlertID  uuid.UUID
	)
	err := row.Scan(
		&job.ID,
		&job.EventID,
		&job.AlertID,
		&job.Attempt,
		&job.ScheduledAt,
		&job.StartedAt,
		&job.ClaimOwner,
		&job.ClaimToken,
		&job.ClaimExpiresAt,
		&eventID,
		&eventAlertID,
		&eventKind,
		&job.Event.AlertVersion,
		&job.Event.Category,
		&eventSeverity,
		&eventState,
		&job.Event.Summary,
		&job.Event.CurrentValue,
		&job.Event.ThresholdValue,
		&job.Event.FirstObservedAt,
		&job.Event.LastObservedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.Event.ID = eventID
	job.Event.AlertID = eventAlertID
	job.Event.TransitionKind = AlertTransitionKind(eventKind)
	job.Event.Severity = AlertSeverity(eventSeverity)
	job.Event.State = AlertState(eventState)
	job.ScheduledAt = job.ScheduledAt.UTC()
	job.StartedAt = job.StartedAt.UTC()
	job.ClaimExpiresAt = job.ClaimExpiresAt.UTC()
	job.Event.FirstObservedAt = job.Event.FirstObservedAt.UTC()
	job.Event.LastObservedAt = job.Event.LastObservedAt.UTC()
	if job.EventID != job.Event.ID ||
		job.AlertID != job.Event.AlertID ||
		!validWebhookTransition(job.Event.TransitionKind, Alert{
			ID:              job.Event.AlertID,
			Category:        job.Event.Category,
			Severity:        job.Event.Severity,
			State:           job.Event.State,
			Summary:         job.Event.Summary,
			CurrentValue:    job.Event.CurrentValue,
			ThresholdValue:  job.Event.ThresholdValue,
			FirstObservedAt: job.Event.FirstObservedAt,
			LastObservedAt:  job.Event.LastObservedAt,
			Version:         job.Event.AlertVersion,
		}) {
		return nil, ErrInvalid
	}
	return &job, nil
}

func (store *PostgresWebhookDeliveryStore) Complete(
	ctx context.Context,
	job WebhookDeliveryJob,
	result WebhookDeliveryResult,
	finishedAt time.Time,
) error {
	if store == nil || store.pool == nil || ctx == nil ||
		!validWebhookDeliveryJobClaim(job) ||
		!validWebhookDeliveryResult(result) ||
		!validSampleTime(finishedAt) {
		return ErrInvalid
	}
	finishedAt = postgresAlertTime(finishedAt)
	if finishedAt.Before(job.StartedAt) {
		return ErrInvalid
	}
	state := "failed"
	outcome := "failed"
	if result.Succeeded {
		state = "succeeded"
		outcome = "succeeded"
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `
UPDATE alert_deliveries
SET delivery_state=$6,
    outcome=$7,
    http_status_class=$8,
    error_category=$9,
    finished_at=$10,
    claim_owner=NULL,
    claim_token=NULL,
    claim_expires_at=NULL
WHERE id=$1
  AND event_id=$2
  AND alert_id=$3
  AND delivery_state='claimed'
  AND claim_owner=$4
  AND claim_token=$5
  AND claim_expires_at > $10`,
		job.ID,
		job.EventID,
		job.AlertID,
		job.ClaimOwner,
		job.ClaimToken,
		state,
		outcome,
		nullableStatusClass(result.HTTPStatusClass),
		result.ErrorCategory,
		finishedAt,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	if result.Succeeded || !result.Retryable {
		if _, err := tx.Exec(ctx, `
UPDATE alert_deliveries
SET delivery_state='cancelled',
    outcome='cancelled',
    http_status_class=NULL,
    error_category='',
    finished_at=$2,
    claim_owner=NULL,
    claim_token=NULL,
    claim_expires_at=NULL
WHERE event_id=$1
  AND attempt > $3
  AND delivery_state='pending'`,
			job.EventID,
			finishedAt,
			job.Attempt,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (store *PostgresWebhookDeliveryStore) Abandon(
	ctx context.Context,
	job WebhookDeliveryJob,
) error {
	if store == nil || store.pool == nil || ctx == nil ||
		!validWebhookDeliveryJobClaim(job) {
		return ErrInvalid
	}
	command, err := store.pool.Exec(ctx, `
UPDATE alert_deliveries
SET delivery_state='pending',
    started_at=NULL,
    finished_at=NULL,
    outcome=NULL,
    http_status_class=NULL,
    error_category='',
    claim_owner=NULL,
    claim_token=NULL,
    claim_expires_at=NULL
WHERE id=$1
  AND event_id=$2
  AND alert_id=$3
  AND delivery_state='claimed'
  AND claim_owner=$4
  AND claim_token=$5`,
		job.ID,
		job.EventID,
		job.AlertID,
		job.ClaimOwner,
		job.ClaimToken,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func validWebhookDeliveryJobClaim(job WebhookDeliveryJob) bool {
	return job.ID != uuid.Nil &&
		job.EventID != uuid.Nil &&
		job.AlertID != uuid.Nil &&
		job.Attempt >= 1 &&
		job.Attempt <= len(webhookAttemptDelays) &&
		webhookClaimOwner.MatchString(job.ClaimOwner) &&
		job.ClaimToken != uuid.Nil &&
		validSampleTime(job.ScheduledAt) &&
		validSampleTime(job.StartedAt) &&
		validSampleTime(job.ClaimExpiresAt) &&
		!job.ClaimExpiresAt.Before(job.StartedAt)
}
