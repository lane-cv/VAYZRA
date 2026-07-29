package operations

import (
	"context"
	"errors"
	"net"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/audit"
)

const (
	alertDedupeAdvisorySalt int64 = 845103122
	alertRunnerAdvisoryKey  int64 = 845103123
)

type PostgresAlertStore struct {
	pool           *pgxpool.Pool
	collectors     []AlertSampleCollector
	webhookEnabled bool
	clock          func() time.Time
	newUUID        func() uuid.UUID
}

var _ AlertStore = (*PostgresAlertStore)(nil)

func NewPostgresAlertStore(pool *pgxpool.Pool) *PostgresAlertStore {
	return newPostgresAlertStoreWithCollectors(
		pool,
		NewPostgresAlertCollectors(pool),
	)
}

func NewPostgresAlertStoreWithWebhookOutbox(
	pool *pgxpool.Pool,
	clock func() time.Time,
	newUUID func() uuid.UUID,
) (*PostgresAlertStore, error) {
	if pool == nil || clock == nil || newUUID == nil {
		return nil, ErrInvalid
	}
	store := newPostgresAlertStoreWithCollectors(
		pool,
		NewPostgresAlertCollectors(pool),
	)
	store.webhookEnabled = true
	store.clock = clock
	store.newUUID = newUUID
	return store, nil
}

func newPostgresAlertStoreWithCollectors(
	pool *pgxpool.Pool,
	collectors []AlertSampleCollector,
) *PostgresAlertStore {
	return &PostgresAlertStore{
		pool:       pool,
		collectors: append([]AlertSampleCollector(nil), collectors...),
		clock:      time.Now,
		newUUID:    uuid.New,
	}
}

func (store *PostgresAlertStore) EvaluateAlert(
	ctx context.Context,
	evaluation Evaluation,
) (AlertTransition, error) {
	condition, err := classifyAlertEvaluation(evaluation)
	if err != nil || store == nil || store.pool == nil || ctx == nil {
		if err != nil {
			return AlertTransition{}, err
		}
		return AlertTransition{}, ErrInvalid
	}
	if condition == alertConditionNeutral {
		return AlertTransition{Kind: AlertTransitionNone}, nil
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return AlertTransition{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`,
		evaluation.Rule.DedupeKey,
		alertDedupeAdvisorySalt,
	); err != nil {
		return AlertTransition{}, err
	}
	current, err := selectUnresolvedAlert(ctx, tx, evaluation.Rule.DedupeKey)
	if errors.Is(err, pgx.ErrNoRows) {
		if condition == alertConditionHealthy {
			if err := tx.Commit(ctx); err != nil {
				return AlertTransition{}, err
			}
			return AlertTransition{Kind: AlertTransitionNone}, nil
		}
		if _, err := insertAlertCandidate(ctx, tx, evaluation, condition); err != nil {
			return AlertTransition{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return AlertTransition{}, err
		}
		return AlertTransition{Kind: AlertTransitionNone}, nil
	}
	if err != nil {
		return AlertTransition{}, err
	}
	if !evaluation.ObservedAt.After(current.LastObservedAt) ||
		evaluation.ObservedAt.Sub(current.LastObservedAt) < time.Minute {
		if err := tx.Commit(ctx); err != nil {
			return AlertTransition{}, err
		}
		if !alertVisible(current) {
			return AlertTransition{Kind: AlertTransitionNone}, nil
		}
		return AlertTransition{Kind: AlertTransitionNone, Alert: &current}, nil
	}
	if condition == alertConditionHealthy {
		if !alertVisible(current) {
			if _, err := tx.Exec(
				ctx,
				`DELETE FROM operational_alerts WHERE id=$1`,
				current.ID,
			); err != nil {
				return AlertTransition{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return AlertTransition{}, err
			}
			return AlertTransition{Kind: AlertTransitionNone}, nil
		}
		return store.recordHealthyEvaluation(ctx, tx, current, evaluation)
	}
	return store.recordFailingEvaluation(ctx, tx, current, evaluation, condition)
}

func insertAlertCandidate(
	ctx context.Context,
	tx pgx.Tx,
	evaluation Evaluation,
	condition alertCondition,
) (Alert, error) {
	severity, threshold := failingSeverityAndThreshold(evaluation.Rule, condition)
	return queryAlert(ctx, tx, `
INSERT INTO operational_alerts(
  dedupe_key,category,severity,state,first_observed_at,last_observed_at,
  current_value,threshold_value,summary,trace_id,
  consecutive_failures,consecutive_successes
) VALUES($1,$2,$3,'open',$4,$4,$5,$6,$7,'',1,0)
RETURNING `+alertSelectColumns,
		evaluation.Rule.DedupeKey,
		evaluation.Rule.Category,
		severity,
		evaluation.ObservedAt.UTC(),
		evaluation.Value,
		threshold,
		evaluation.Rule.Summary,
	)
}

func (store *PostgresAlertStore) recordFailingEvaluation(
	ctx context.Context,
	tx pgx.Tx,
	current Alert,
	evaluation Evaluation,
	condition alertCondition,
) (AlertTransition, error) {
	observedSeverity, _ := failingSeverityAndThreshold(evaluation.Rule, condition)
	severity := current.Severity
	kind := AlertTransitionUpdated
	wasVisible := alertVisible(current)
	if observedSeverity == AlertSeverityCritical &&
		current.Severity != AlertSeverityCritical {
		severity = AlertSeverityCritical
		if wasVisible {
			kind = AlertTransitionUpgraded
		}
	}
	threshold := evaluation.Rule.Warning
	if severity == AlertSeverityCritical {
		threshold = evaluation.Rule.Critical
	}
	failures := current.ConsecutiveFailures + 1
	if !wasVisible && failures >= 2 {
		kind = AlertTransitionOpened
	}
	updated, err := queryAlert(ctx, tx, `
UPDATE operational_alerts
SET category=$2,severity=$3,last_observed_at=$4,current_value=$5,
    threshold_value=$6,summary=$7,consecutive_failures=$8,
    consecutive_successes=0,version=version+1
WHERE id=$1 AND state<>'resolved'
RETURNING `+alertSelectColumns,
		current.ID,
		evaluation.Rule.Category,
		severity,
		evaluation.ObservedAt.UTC(),
		evaluation.Value,
		threshold,
		evaluation.Rule.Summary,
		failures,
	)
	if err != nil {
		return AlertTransition{}, err
	}
	transitionKind := kind
	if !wasVisible && failures < 2 {
		transitionKind = AlertTransitionNone
	}
	if err := store.enqueueWebhookTransition(
		ctx,
		tx,
		transitionKind,
		updated,
	); err != nil {
		return AlertTransition{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AlertTransition{}, err
	}
	if !wasVisible && failures < 2 {
		return AlertTransition{Kind: AlertTransitionNone}, nil
	}
	return AlertTransition{Kind: kind, Alert: &updated}, nil
}

func (store *PostgresAlertStore) recordHealthyEvaluation(
	ctx context.Context,
	tx pgx.Tx,
	current Alert,
	evaluation Evaluation,
) (AlertTransition, error) {
	successes := current.ConsecutiveSuccesses + 1
	resolve := successes >= 3
	query := `
UPDATE operational_alerts
SET last_observed_at=$2,current_value=$3,threshold_value=$4,
    consecutive_failures=0,consecutive_successes=$5,version=version+1
WHERE id=$1 AND state<>'resolved'
RETURNING ` + alertSelectColumns
	args := []any{
		current.ID,
		evaluation.ObservedAt.UTC(),
		evaluation.Value,
		evaluation.Rule.Warning,
		successes,
	}
	if resolve {
		query = `
UPDATE operational_alerts
SET state='resolved',last_observed_at=$2,resolved_at=$2,
    current_value=$3,threshold_value=$4,consecutive_failures=0,
    consecutive_successes=$5,version=version+1
WHERE id=$1 AND state<>'resolved'
RETURNING ` + alertSelectColumns
	}
	updated, err := queryAlert(ctx, tx, query, args...)
	if err != nil {
		return AlertTransition{}, err
	}
	kind := AlertTransitionUpdated
	if resolve {
		kind = AlertTransitionResolved
	}
	if err := store.enqueueWebhookTransition(ctx, tx, kind, updated); err != nil {
		return AlertTransition{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AlertTransition{}, err
	}
	return AlertTransition{Kind: kind, Alert: &updated}, nil
}

func failingSeverityAndThreshold(
	rule Rule,
	condition alertCondition,
) (AlertSeverity, float64) {
	if condition == alertConditionCritical {
		return AlertSeverityCritical, rule.Critical
	}
	return AlertSeverityWarning, rule.Warning
}

func (store *PostgresAlertStore) ListAlerts(
	ctx context.Context,
	filter AlertFilter,
) (AlertPage, error) {
	if store == nil || store.pool == nil || ctx == nil ||
		validateAlertFilter(filter) != nil {
		return AlertPage{}, ErrInvalid
	}
	var beforeTime any
	var beforeID any
	if filter.Before != nil {
		beforeTime = filter.Before.LastObservedAt.UTC()
		beforeID = filter.Before.ID
	}
	rows, err := store.pool.Query(ctx, `
SELECT `+alertSelectColumns+`
FROM operational_alerts
WHERE (consecutive_failures >= 2 OR version >= 2)
  AND ($1::text='' OR state=$1)
  AND ($2::text='' OR severity=$2)
  AND ($3::text='' OR category=$3)
  AND (
    $4::timestamptz IS NULL OR
    (last_observed_at,id) < ($4,$5::uuid)
  )
ORDER BY last_observed_at DESC,id DESC
LIMIT $6`,
		string(filter.State),
		string(filter.Severity),
		filter.Category,
		beforeTime,
		beforeID,
		filter.Limit+1,
	)
	if err != nil {
		return AlertPage{}, err
	}
	defer rows.Close()
	items := make([]Alert, 0, filter.Limit+1)
	for rows.Next() {
		alert, scanErr := scanAlert(rows)
		if scanErr != nil {
			return AlertPage{}, scanErr
		}
		items = append(items, alert)
	}
	if err := rows.Err(); err != nil {
		return AlertPage{}, err
	}
	page := AlertPage{Items: items}
	if len(items) > filter.Limit {
		page.Items = items[:filter.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &AlertCursor{
			LastObservedAt: last.LastObservedAt,
			ID:             last.ID,
		}
	}
	return page, nil
}

func validateAlertFilter(filter AlertFilter) error {
	if filter.Limit < 1 || filter.Limit > 100 {
		return ErrInvalid
	}
	switch filter.State {
	case "", AlertStateOpen, AlertStateAcknowledged, AlertStateResolved:
	default:
		return ErrInvalid
	}
	switch filter.Severity {
	case "", AlertSeverityWarning, AlertSeverityCritical:
	default:
		return ErrInvalid
	}
	if filter.Category != "" {
		if _, ok := alertCategories[filter.Category]; !ok {
			return ErrInvalid
		}
	}
	if filter.Before != nil &&
		(!validSampleTime(filter.Before.LastObservedAt) ||
			filter.Before.ID == uuid.Nil) {
		return ErrInvalid
	}
	return nil
}

func (store *PostgresAlertStore) AcknowledgeAlert(
	ctx context.Context,
	principal Principal,
	id uuid.UUID,
) (Alert, error) {
	if store == nil || store.pool == nil || ctx == nil || id == uuid.Nil {
		return Alert{}, ErrInvalid
	}
	if err := authorizeSettings(principal); err != nil {
		return Alert{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return Alert{}, err
	}
	defer tx.Rollback(ctx)
	current, err := queryAlert(ctx, tx, `
SELECT `+alertSelectColumns+`
FROM operational_alerts
WHERE id=$1 AND (consecutive_failures >= 2 OR version >= 2)
FOR UPDATE`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alert{}, ErrAlertNotFound
	}
	if err != nil {
		return Alert{}, err
	}
	if current.State == AlertStateResolved {
		return Alert{}, ErrAlertAlreadyResolved
	}
	if current.State == AlertStateAcknowledged {
		if err := tx.Commit(ctx); err != nil {
			return Alert{}, err
		}
		return current, nil
	}
	acknowledged, err := queryAlert(ctx, tx, `
UPDATE operational_alerts
SET state='acknowledged',acknowledged_by=$2,
    acknowledged_at=clock_timestamp(),version=version+1
WHERE id=$1 AND state='open'
RETURNING `+alertSelectColumns,
		id,
		principal.User.ID,
	)
	if err != nil {
		return Alert{}, err
	}
	if err := audit.NewPostgresWriter(tx).Write(ctx, audit.Event{
		ActorUserID: principal.User.ID,
		Action:      "operations.alert_acknowledged",
		TargetType:  "operational_alert",
		TargetID:    id.String(),
		Metadata: map[string]any{
			"status": string(AlertStateAcknowledged),
		},
		RequestID: principal.RequestID,
		IP:        append(net.IP(nil), principal.IP...),
	}); err != nil {
		return Alert{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Alert{}, err
	}
	return acknowledged, nil
}

var alertDeliveryErrorCategory = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (store *PostgresAlertStore) RecordAlertDelivery(
	ctx context.Context,
	delivery AlertDelivery,
) error {
	if store == nil || store.pool == nil || ctx == nil ||
		!validAlertDelivery(delivery) {
		return ErrInvalid
	}
	delivery.StartedAt = postgresAlertTime(delivery.StartedAt)
	delivery.FinishedAt = postgresAlertTime(delivery.FinishedAt)
	if delivery.FinishedAt.Before(delivery.StartedAt) {
		return ErrInvalid
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var storedID uuid.UUID
	err = tx.QueryRow(ctx, `
INSERT INTO alert_deliveries(
  id,alert_id,attempt,destination,delivery_state,outcome,
  http_status_class,error_category,started_at,finished_at
) VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,$9)
ON CONFLICT(alert_id,attempt,destination)
  WHERE event_id IS NULL
DO NOTHING
RETURNING id`,
		delivery.ID,
		delivery.AlertID,
		delivery.Attempt,
		delivery.Destination,
		delivery.Outcome,
		nullableStatusClass(delivery.HTTPStatusClass),
		delivery.ErrorCategory,
		delivery.StartedAt.UTC(),
		delivery.FinishedAt.UTC(),
	).Scan(&storedID)
	if err == nil {
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var existing AlertDelivery
	var statusClass *int
	err = tx.QueryRow(ctx, `
SELECT id,alert_id,attempt,destination,outcome,http_status_class,
       error_category,started_at,finished_at
FROM alert_deliveries
WHERE alert_id=$1 AND attempt=$2 AND destination=$3
  AND event_id IS NULL
FOR UPDATE`,
		delivery.AlertID,
		delivery.Attempt,
		delivery.Destination,
	).Scan(
		&existing.ID,
		&existing.AlertID,
		&existing.Attempt,
		&existing.Destination,
		&existing.Outcome,
		&statusClass,
		&existing.ErrorCategory,
		&existing.StartedAt,
		&existing.FinishedAt,
	)
	if err != nil {
		return err
	}
	if statusClass != nil {
		existing.HTTPStatusClass = *statusClass
	}
	if !sameAlertDelivery(existing, delivery) {
		return ErrConflict
	}
	return tx.Commit(ctx)
}

func validAlertDelivery(delivery AlertDelivery) bool {
	if delivery.ID == uuid.Nil || delivery.AlertID == uuid.Nil ||
		delivery.Attempt < 1 || delivery.Attempt > 4 ||
		delivery.Destination != "webhook" ||
		(delivery.Outcome != "succeeded" &&
			delivery.Outcome != "failed" &&
			delivery.Outcome != "cancelled") ||
		delivery.HTTPStatusClass < 0 || delivery.HTTPStatusClass > 5 ||
		(delivery.ErrorCategory != "" &&
			!alertDeliveryErrorCategory.MatchString(delivery.ErrorCategory)) ||
		!validSampleTime(delivery.StartedAt) ||
		!validSampleTime(delivery.FinishedAt) ||
		delivery.FinishedAt.Before(delivery.StartedAt) {
		return false
	}
	return true
}

func nullableStatusClass(statusClass int) any {
	if statusClass == 0 {
		return nil
	}
	return statusClass
}

func sameAlertDelivery(left, right AlertDelivery) bool {
	return left.AlertID == right.AlertID &&
		left.Attempt == right.Attempt &&
		left.Destination == right.Destination &&
		left.Outcome == right.Outcome &&
		left.HTTPStatusClass == right.HTTPStatusClass &&
		left.ErrorCategory == right.ErrorCategory &&
		left.StartedAt.Equal(right.StartedAt) &&
		left.FinishedAt.Equal(right.FinishedAt)
}

func postgresAlertTime(value time.Time) time.Time {
	return value.UTC().Round(time.Microsecond)
}

type AlertRunnerLease struct {
	mu       sync.Mutex
	conn     *pgxpool.Conn
	released bool
}

func (store *PostgresAlertStore) TryAcquireAlertRunnerLease(
	ctx context.Context,
) (AlertLease, bool, error) {
	if store == nil || store.pool == nil || ctx == nil {
		return nil, false, ErrInvalid
	}
	conn, err := store.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRow(
		ctx,
		`SELECT pg_try_advisory_lock($1)`,
		alertRunnerAdvisoryKey,
	).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	return &AlertRunnerLease{conn: conn}, true, nil
}

func (store *PostgresAlertStore) LoadAlertEvaluations(
	ctx context.Context,
	now time.Time,
) ([]Evaluation, error) {
	if store == nil || store.pool == nil || ctx == nil || !validSampleTime(now) {
		return nil, ErrInvalid
	}
	settings, err := NewPostgresStore(store.pool).GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := DefaultAlertRules(settings)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	collected := make([]Sample, 0, 8)
	collectorErrors := make([]error, 0)
	remoteReplicationState := RemoteReplicationUnknown
	for _, collector := range store.collectors {
		if collector == nil {
			collectorErrors = append(
				collectorErrors,
				ErrAlertCollectorUnavailable,
			)
			continue
		}
		collection, collectErr := collector.Collect(ctx, now)
		if collectErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			collectorErrors = append(
				collectorErrors,
				ErrAlertCollectorUnavailable,
			)
			continue
		}
		collected = append(collected, collection.Samples...)
		if collection.RemoteReplicationConfigured != nil {
			collectedState := RemoteReplicationDisabled
			if *collection.RemoteReplicationConfigured {
				collectedState = RemoteReplicationEnabled
			}
			if remoteReplicationState != RemoteReplicationUnknown &&
				remoteReplicationState != collectedState {
				return nil, ErrInvalid
			}
			remoteReplicationState = collectedState
		}
	}
	indexed := make(map[sampleSeries]struct{}, len(collected))
	for _, sample := range collected {
		if err := ValidateSample(sample, now); err != nil {
			return nil, err
		}
		key := sampleSeries{
			source: sample.Source, metric: sample.Metric, scope: sample.Scope,
		}
		if _, duplicate := indexed[key]; duplicate {
			return nil, ErrInvalid
		}
		indexed[key] = struct{}{}
	}
	latest, err := NewPostgresSampleStore(store.pool).
		insertAndLatestMetrics(ctx, now, collected)
	if err != nil {
		return nil, err
	}
	samples := append([]Sample(nil), collected...)
	for _, sample := range latest {
		if sample.Source == SampleSourceHost {
			samples = append(samples, sample)
		}
	}
	evaluations, err := BuildAlertEvaluations(
		rules,
		samples,
		now,
		remoteReplicationState,
	)
	if err != nil {
		return nil, err
	}
	return evaluations, errors.Join(collectorErrors...)
}

func (lease *AlertRunnerLease) Release(ctx context.Context) error {
	if lease == nil {
		return ErrInvalid
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return nil
	}
	lease.released = true
	conn := lease.conn
	lease.conn = nil
	if conn == nil {
		return nil
	}
	var unlocked bool
	err := conn.QueryRow(
		ctx,
		`SELECT pg_advisory_unlock($1)`,
		alertRunnerAdvisoryKey,
	).Scan(&unlocked)
	if err != nil || !unlocked {
		raw := conn.Hijack()
		closeErr := raw.Close(context.Background())
		if err != nil {
			return err
		}
		return closeErr
	}
	conn.Release()
	return nil
}

const alertSelectColumns = `
id,dedupe_key,category,severity,state,first_observed_at,last_observed_at,
acknowledged_by,acknowledged_at,resolved_at,current_value,threshold_value,
summary,trace_id,consecutive_failures,consecutive_successes,version`

type alertScanner interface {
	Scan(...any) error
}

func selectUnresolvedAlert(
	ctx context.Context,
	tx pgx.Tx,
	dedupeKey string,
) (Alert, error) {
	return queryAlert(ctx, tx, `
SELECT `+alertSelectColumns+`
FROM operational_alerts
WHERE dedupe_key=$1 AND state<>'resolved'
FOR UPDATE`, dedupeKey)
}

func queryAlert(
	ctx context.Context,
	tx pgx.Tx,
	query string,
	args ...any,
) (Alert, error) {
	return scanAlert(tx.QueryRow(ctx, query, args...))
}

func scanAlert(row alertScanner) (Alert, error) {
	var (
		alert          Alert
		severity       string
		state          string
		acknowledgedBy *uuid.UUID
	)
	err := row.Scan(
		&alert.ID,
		&alert.DedupeKey,
		&alert.Category,
		&severity,
		&state,
		&alert.FirstObservedAt,
		&alert.LastObservedAt,
		&acknowledgedBy,
		&alert.AcknowledgedAt,
		&alert.ResolvedAt,
		&alert.CurrentValue,
		&alert.ThresholdValue,
		&alert.Summary,
		&alert.TraceID,
		&alert.ConsecutiveFailures,
		&alert.ConsecutiveSuccesses,
		&alert.Version,
	)
	if err != nil {
		return Alert{}, err
	}
	alert.Severity = AlertSeverity(severity)
	alert.State = AlertState(state)
	alert.FirstObservedAt = alert.FirstObservedAt.UTC()
	alert.LastObservedAt = alert.LastObservedAt.UTC()
	if acknowledgedBy != nil {
		alert.AcknowledgedBy = *acknowledgedBy
	}
	alert.AcknowledgedAt = utcAlertTime(alert.AcknowledgedAt)
	alert.ResolvedAt = utcAlertTime(alert.ResolvedAt)
	return alert, nil
}

func utcAlertTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func alertVisible(alert Alert) bool {
	return alert.ConsecutiveFailures >= 2 || alert.Version >= 2
}
