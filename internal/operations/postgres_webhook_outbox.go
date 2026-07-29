package operations

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var webhookAttemptDelays = [...]time.Duration{
	time.Minute,
	5 * time.Minute,
	30 * time.Minute,
}

func (store *PostgresAlertStore) enqueueWebhookTransition(
	ctx context.Context,
	tx pgx.Tx,
	kind AlertTransitionKind,
	alert Alert,
) error {
	if store == nil || tx == nil || ctx == nil {
		return ErrInvalid
	}
	if !store.webhookEnabled ||
		kind == AlertTransitionNone ||
		kind == AlertTransitionUpdated {
		return nil
	}
	if !validWebhookTransition(kind, alert) ||
		store.clock == nil ||
		store.newUUID == nil {
		return ErrInvalid
	}
	enqueuedAt := postgresAlertTime(store.clock())
	if !validSampleTime(enqueuedAt) ||
		enqueuedAt.Before(alert.FirstObservedAt) {
		return ErrInvalid
	}
	eventID := store.newUUID()
	if eventID == uuid.Nil {
		return ErrInvalid
	}
	var storedEventID uuid.UUID
	var storedEnqueuedAt time.Time
	err := tx.QueryRow(ctx, `
INSERT INTO alert_webhook_events(
  id,alert_id,transition_kind,alert_version,category,severity,state,
  summary,current_value,threshold_value,first_observed_at,last_observed_at,
  enqueued_at
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT(alert_id,transition_kind,alert_version) DO NOTHING
RETURNING id,enqueued_at`,
		eventID,
		alert.ID,
		string(kind),
		alert.Version,
		alert.Category,
		string(alert.Severity),
		string(alert.State),
		alert.Summary,
		alert.CurrentValue,
		alert.ThresholdValue,
		alert.FirstObservedAt.UTC(),
		alert.LastObservedAt.UTC(),
		enqueuedAt,
	).Scan(&storedEventID, &storedEnqueuedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		var existing Alert
		var existingKind string
		err = tx.QueryRow(ctx, `
SELECT id,transition_kind,alert_version,category,severity,state,summary,
       current_value,threshold_value,first_observed_at,last_observed_at,
       enqueued_at
FROM alert_webhook_events
WHERE alert_id=$1 AND transition_kind=$2 AND alert_version=$3
FOR UPDATE`,
			alert.ID,
			string(kind),
			alert.Version,
		).Scan(
			&storedEventID,
			&existingKind,
			&existing.Version,
			&existing.Category,
			&existing.Severity,
			&existing.State,
			&existing.Summary,
			&existing.CurrentValue,
			&existing.ThresholdValue,
			&existing.FirstObservedAt,
			&existing.LastObservedAt,
			&storedEnqueuedAt,
		)
		if err != nil {
			return err
		}
		if AlertTransitionKind(existingKind) != kind ||
			existing.Category != alert.Category ||
			existing.Severity != alert.Severity ||
			existing.State != alert.State ||
			existing.Summary != alert.Summary ||
			existing.CurrentValue != alert.CurrentValue ||
			existing.ThresholdValue != alert.ThresholdValue ||
			!existing.FirstObservedAt.Equal(alert.FirstObservedAt) ||
			!existing.LastObservedAt.Equal(alert.LastObservedAt) {
			return ErrConflict
		}
	} else if err != nil {
		return err
	}
	storedEnqueuedAt = postgresAlertTime(storedEnqueuedAt)
	for index, delay := range webhookAttemptDelays {
		deliveryID := store.newUUID()
		if deliveryID == uuid.Nil {
			return ErrInvalid
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO alert_deliveries(
  id,alert_id,event_id,attempt,destination,delivery_state,scheduled_at
) VALUES($1,$2,$3,$4,'webhook','pending',$5)
ON CONFLICT(event_id,attempt,destination)
  WHERE event_id IS NOT NULL
DO NOTHING`,
			deliveryID,
			alert.ID,
			storedEventID,
			index+1,
			storedEnqueuedAt.Add(delay),
		); err != nil {
			return err
		}
	}
	return nil
}

func validWebhookTransition(kind AlertTransitionKind, alert Alert) bool {
	switch kind {
	case AlertTransitionOpened,
		AlertTransitionUpgraded,
		AlertTransitionResolved:
	default:
		return false
	}
	if alert.ID == uuid.Nil ||
		alert.Version < 1 ||
		!alertIdentifier.MatchString(alert.Category) ||
		!safeAlertSummary(alert.Summary) ||
		!validAlertFloat(alert.CurrentValue) ||
		!validAlertFloat(alert.ThresholdValue) ||
		!validSampleTime(alert.FirstObservedAt) ||
		!validSampleTime(alert.LastObservedAt) ||
		alert.LastObservedAt.Before(alert.FirstObservedAt) {
		return false
	}
	if _, ok := alertCategories[alert.Category]; !ok {
		return false
	}
	switch alert.Severity {
	case AlertSeverityWarning, AlertSeverityCritical:
	default:
		return false
	}
	switch alert.State {
	case AlertStateOpen, AlertStateAcknowledged, AlertStateResolved:
	default:
		return false
	}
	return true
}
