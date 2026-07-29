package operations

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresSampleStore struct {
	pool *pgxpool.Pool
}

var _ SampleStore = (*PostgresSampleStore)(nil)

func NewPostgresSampleStore(pool *pgxpool.Pool) *PostgresSampleStore {
	return &PostgresSampleStore{pool: pool}
}

func (store *PostgresSampleStore) InsertSamples(
	ctx context.Context,
	now time.Time,
	samples []Sample,
) error {
	if store == nil || store.pool == nil {
		return errStoreClosed
	}
	if len(samples) == 0 || len(samples) > MaxSampleInsertBatch {
		return ErrInvalid
	}
	rows := make([][]any, len(samples))
	for index, sample := range samples {
		if err := ValidateSample(sample, now); err != nil {
			return err
		}
		var windowStartedAt any
		if sample.WindowStartedAt != nil {
			windowStartedAt = sample.WindowStartedAt.UTC()
		}
		rows[index] = []any{
			string(sample.Source),
			string(sample.Metric),
			string(sample.Scope),
			sample.Value,
			string(sample.Unit),
			sample.ObservedAt.UTC(),
			windowStartedAt,
		}
	}

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.CopyFrom(
		ctx,
		pgx.Identifier{"operational_samples"},
		[]string{
			"source",
			"metric_name",
			"scope",
			"value",
			"unit",
			"observed_at",
			"window_started_at",
		},
		pgx.CopyFromRows(rows),
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *PostgresSampleStore) ReadLatestSamples(
	ctx context.Context,
	request SampleReadRequest,
) (SampleReadResult, error) {
	result := SampleReadResult{
		Source:    request.Source,
		Metric:    request.Metric,
		Scope:     request.Scope,
		Freshness: SampleFreshnessEmpty,
		Samples:   []Sample{},
	}
	if store == nil || store.pool == nil {
		return result, errStoreClosed
	}
	if !validSampleTime(request.Now) ||
		request.Limit < 1 || request.Limit > MaxSampleReadLimit ||
		request.FreshFor <= 0 || request.FreshFor > MaxSampleFreshFor {
		return result, ErrInvalid
	}
	rule, ok := sampleRuleFor(request.Source, request.Metric, request.Scope)
	if !ok {
		return result, ErrInvalid
	}
	result.Unit = rule.unit

	rows, err := store.pool.Query(ctx, `
SELECT source,metric_name,scope,value,unit,observed_at,window_started_at
FROM operational_samples
WHERE source=$1 AND metric_name=$2 AND scope=$3 AND unit=$4
ORDER BY observed_at DESC,ctid DESC
LIMIT $5`,
		string(request.Source),
		string(request.Metric),
		string(request.Scope),
		string(rule.unit),
		request.Limit,
	)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			source          string
			metric          string
			scope           string
			unit            string
			value           float64
			observedAt      time.Time
			windowStartedAt *time.Time
		)
		if err := rows.Scan(
			&source,
			&metric,
			&scope,
			&value,
			&unit,
			&observedAt,
			&windowStartedAt,
		); err != nil {
			return result, err
		}
		sample := Sample{
			Source:          SampleSource(source),
			Metric:          SampleMetric(metric),
			Scope:           SampleScope(scope),
			Value:           value,
			Unit:            SampleUnit(unit),
			ObservedAt:      observedAt.UTC(),
			WindowStartedAt: utcSampleTime(windowStartedAt),
		}
		if err := ValidateSample(sample, request.Now); err != nil {
			return result, err
		}
		result.Samples = append(result.Samples, sample)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Samples) == 0 {
		return result, nil
	}
	if result.Samples[0].ObservedAt.Before(request.Now.Add(-request.FreshFor)) {
		result.Freshness = SampleFreshnessStale
	} else {
		result.Freshness = SampleFreshnessFresh
	}
	return result, nil
}

func (store *PostgresSampleStore) DeleteExpiredSamples(
	ctx context.Context,
	now time.Time,
	retentionDays int,
) (int64, error) {
	if store == nil || store.pool == nil {
		return 0, errStoreClosed
	}
	if !validSampleTime(now) || retentionDays < 1 || retentionDays > 30 {
		return 0, ErrInvalid
	}
	cutoff := now.UTC().AddDate(0, 0, -retentionDays)
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
WITH expired AS (
  SELECT ctid
  FROM operational_samples
  WHERE observed_at < $1
  ORDER BY observed_at,source,metric_name,scope,unit,ctid
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
DELETE FROM operational_samples AS samples
USING expired
WHERE samples.ctid=expired.ctid`,
		cutoff,
		MaxSampleRetentionBatch,
	)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func utcSampleTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
