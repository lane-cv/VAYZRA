package operations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresSampleStore struct {
	pool *pgxpool.Pool
}

var _ SampleStore = (*PostgresSampleStore)(nil)

var errSampleBatchShortWrite = errors.New("sample batch was not fully stored")

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
	if ctx == nil {
		return ErrInvalid
	}
	if len(samples) == 0 || len(samples) > MaxSampleInsertBatch {
		return ErrInvalid
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := copySamples(ctx, tx, now, samples); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (store *PostgresSampleStore) insertAndLatestMetrics(
	ctx context.Context,
	now time.Time,
	samples []Sample,
) ([]Sample, error) {
	if store == nil || store.pool == nil {
		return nil, errStoreClosed
	}
	if ctx == nil || !validSampleTime(now) ||
		len(samples) > MaxSampleInsertBatch {
		return nil, ErrInvalid
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if len(samples) > 0 {
		if err := copySamples(ctx, tx, now, samples); err != nil {
			return nil, err
		}
	}
	latest, err := latestMetrics(ctx, tx, now.UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return latest, nil
}

func copySamples(
	ctx context.Context,
	tx pgx.Tx,
	now time.Time,
	samples []Sample,
) error {
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
	copied, err := tx.CopyFrom(
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
	)
	if err != nil {
		return err
	}
	if copied != int64(len(samples)) {
		return errSampleBatchShortWrite
	}
	return nil
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
	if ctx == nil {
		return result, ErrInvalid
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
WHERE source=$1 AND metric_name=$2 AND scope=$3
ORDER BY observed_at DESC,id DESC
LIMIT $4`,
		string(request.Source),
		string(request.Metric),
		string(request.Scope),
		request.Limit,
	)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	samples := make([]Sample, 0, request.Limit)
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
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(samples) == 0 {
		return result, nil
	}
	result.Samples = samples
	if samples[0].ObservedAt.Before(request.Now.Add(-request.FreshFor)) {
		result.Freshness = SampleFreshnessStale
	} else {
		result.Freshness = SampleFreshnessFresh
	}
	return result, nil
}

func (store *PostgresSampleStore) LatestMetrics(
	ctx context.Context,
	now time.Time,
) ([]Sample, error) {
	if store == nil || store.pool == nil {
		return nil, errStoreClosed
	}
	if ctx == nil || !validSampleTime(now) {
		return nil, ErrInvalid
	}
	return latestMetrics(ctx, store.pool, now.UTC())
}

type latestMetricsQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func latestMetrics(
	ctx context.Context,
	querier latestMetricsQuerier,
	now time.Time,
) ([]Sample, error) {
	rows, err := querier.Query(ctx, `
SELECT DISTINCT ON(metric_name,scope)
  source,metric_name,scope,value,unit,observed_at,window_started_at
FROM operational_samples
WHERE observed_at >= $1
ORDER BY metric_name,scope,observed_at DESC,id DESC`,
		now.Add(-DashboardSampleFreshFor),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	samples := make([]Sample, 0, len(sampleRules))
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
			return nil, err
		}
		sample := Sample{
			Source: SampleSource(source), Metric: SampleMetric(metric),
			Scope: SampleScope(scope), Value: value, Unit: SampleUnit(unit),
			ObservedAt:      observedAt.UTC(),
			WindowStartedAt: utcSampleTime(windowStartedAt),
		}
		if err := ValidateSample(sample, now); err != nil {
			return nil, ErrInvalid
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

func (store *PostgresSampleStore) DeleteExpiredSamples(
	ctx context.Context,
	now time.Time,
	retentionDays int,
) (int64, error) {
	if store == nil || store.pool == nil {
		return 0, errStoreClosed
	}
	if ctx == nil {
		return 0, ErrInvalid
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
  SELECT id
  FROM operational_samples
  WHERE observed_at < $1
  ORDER BY observed_at,source,metric_name,scope,unit,id
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
DELETE FROM operational_samples AS samples
USING expired
WHERE samples.id=expired.id`,
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
