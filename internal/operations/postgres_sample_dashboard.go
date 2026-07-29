package operations

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const DashboardSampleFreshFor = 2 * time.Minute

type PostgresSampleDashboardReader struct {
	pool     *pgxpool.Pool
	freshFor time.Duration
}

var _ StorageDashboardReader = (*PostgresSampleDashboardReader)(nil)
var _ ServiceDashboardReader = (*PostgresSampleDashboardReader)(nil)

func NewPostgresSampleDashboardReader(
	pool *pgxpool.Pool,
	freshFor time.Duration,
) (*PostgresSampleDashboardReader, error) {
	if pool == nil || freshFor <= 0 || freshFor > MaxSampleFreshFor {
		return nil, ErrInvalid
	}
	return &PostgresSampleDashboardReader{
		pool:     pool,
		freshFor: freshFor,
	}, nil
}

func (reader *PostgresSampleDashboardReader) ReadStorageSummary(
	ctx context.Context,
	now time.Time,
) (StorageSummary, error) {
	if reader == nil || reader.pool == nil {
		return StorageSummary{}, errStoreClosed
	}
	if ctx == nil || !validSampleTime(now) ||
		reader.freshFor <= 0 || reader.freshFor > MaxSampleFreshFor {
		return StorageSummary{}, ErrInvalid
	}
	now = now.UTC()

	var (
		warningPercent int
		usedValue      *float64
		usedUnit       *string
		usedObserved   *time.Time
		usedWindow     *time.Time
		capacityValue  *float64
		capacityUnit   *string
		capacitySeen   *time.Time
		capacityWindow *time.Time
	)
	err := reader.pool.QueryRow(ctx, `
SELECT settings.disk_warning_percent,
       used.value,used.unit,used.observed_at,used.window_started_at,
       capacity.value,capacity.unit,capacity.observed_at,
       capacity.window_started_at
FROM system_settings AS settings
LEFT JOIN LATERAL (
  SELECT value,unit,observed_at,window_started_at
  FROM operational_samples
  WHERE source='object_store'
    AND metric_name='object_used_bytes'
    AND scope='object_store'
  ORDER BY observed_at DESC,id DESC
  LIMIT 1
) AS used ON true
LEFT JOIN LATERAL (
  SELECT value,unit,observed_at,window_started_at
  FROM operational_samples
  WHERE source='object_store'
    AND metric_name='object_capacity_bytes'
    AND scope='object_store'
  ORDER BY observed_at DESC,id DESC
  LIMIT 1
) AS capacity ON true
WHERE settings.singleton_id=true`).
		Scan(
			&warningPercent,
			&usedValue, &usedUnit, &usedObserved, &usedWindow,
			&capacityValue, &capacityUnit, &capacitySeen, &capacityWindow,
		)
	if errors.Is(err, pgx.ErrNoRows) {
		return StorageSummary{}, ErrConflict
	}
	if err != nil {
		return StorageSummary{}, err
	}
	if warningPercent < 1 || warningPercent > 99 {
		return StorageSummary{}, ErrInvalid
	}

	used, usedPresent, err := dashboardStorageSample(
		SampleMetricObjectUsedBytes,
		usedValue, usedUnit, usedObserved, usedWindow, now,
	)
	if err != nil {
		return StorageSummary{}, err
	}
	capacity, capacityPresent, err := dashboardStorageSample(
		SampleMetricObjectCapacityBytes,
		capacityValue, capacityUnit, capacitySeen, capacityWindow, now,
	)
	if err != nil {
		return StorageSummary{}, err
	}
	if !usedPresent || !capacityPresent {
		return StorageSummary{
			State:      DataStateEmpty,
			ObservedAt: cloneDashboardTime(&now),
		}, nil
	}

	usedBytes := int64(used.Value)
	capacityBytes := int64(capacity.Value)
	if usedBytes > capacityBytes {
		return StorageSummary{}, ErrInvalid
	}
	observedAt := used.ObservedAt
	if capacity.ObservedAt.Before(observedAt) {
		observedAt = capacity.ObservedAt
	}
	state := DataStateHealthy
	if observedAt.Before(now.Add(-reader.freshFor)) {
		state = DataStateStale
	} else if capacityBytes == 0 ||
		float64(usedBytes)*100/float64(capacityBytes) >= float64(warningPercent) {
		state = DataStateDegraded
	}
	return StorageSummary{
		State:          state,
		ObservedAt:     cloneDashboardTime(&observedAt),
		UsedBytes:      usedBytes,
		CapacityBytes:  capacityBytes,
		WarningPercent: warningPercent,
	}, nil
}

func dashboardStorageSample(
	metric SampleMetric,
	value *float64,
	unit *string,
	observedAt *time.Time,
	windowStartedAt *time.Time,
	now time.Time,
) (Sample, bool, error) {
	if value == nil && unit == nil && observedAt == nil && windowStartedAt == nil {
		return Sample{}, false, nil
	}
	if value == nil || unit == nil || observedAt == nil {
		return Sample{}, false, ErrInvalid
	}
	sample := Sample{
		Source:          SampleSourceObjectStore,
		Metric:          metric,
		Scope:           SampleScopeObjectStore,
		Value:           *value,
		Unit:            SampleUnit(*unit),
		ObservedAt:      observedAt.UTC(),
		WindowStartedAt: utcSampleTime(windowStartedAt),
	}
	if sample.ObservedAt.After(now) || ValidateSample(sample, now) != nil {
		return Sample{}, false, ErrInvalid
	}
	return sample, true, nil
}

type dashboardServiceSeries struct {
	service DashboardService
	kind    string
	source  SampleSource
	metric  SampleMetric
	scope   SampleScope
}

var dashboardServiceSampleOrder = [...]dashboardServiceSeries{
	{ServiceApp, "up", SampleSourceApp, SampleMetricServiceUp, SampleScopeApp},
	{ServiceCaddy, "up", SampleSourceHost, SampleMetricServiceUp, SampleScopeCaddy},
	{ServicePostgres, "up", SampleSourcePostgres, SampleMetricServiceUp, SampleScopePostgres},
	{ServicePostgres, "latency", SampleSourcePostgres, SampleMetricServiceLatencyMilliseconds, SampleScopePostgres},
	{ServiceRedis, "up", SampleSourceRedis, SampleMetricServiceUp, SampleScopeRedis},
	{ServiceRedis, "latency", SampleSourceRedis, SampleMetricServiceLatencyMilliseconds, SampleScopeRedis},
	{ServiceObjectStore, "up", SampleSourceObjectStore, SampleMetricServiceUp, SampleScopeObjectStore},
	{ServiceObjectStore, "latency", SampleSourceObjectStore, SampleMetricServiceLatencyMilliseconds, SampleScopeObjectStore},
	{ServiceWorker, "up", SampleSourceWorker, SampleMetricServiceUp, SampleScopeWorker},
}

type dashboardServiceSamples struct {
	up             Sample
	upPresent      bool
	latency        Sample
	latencyPresent bool
	needsLatency   bool
}

func (reader *PostgresSampleDashboardReader) ReadServiceHealth(
	ctx context.Context,
	now time.Time,
) ([]ServiceHealth, error) {
	if reader == nil || reader.pool == nil {
		return nil, errStoreClosed
	}
	if ctx == nil || !validSampleTime(now) ||
		reader.freshFor <= 0 || reader.freshFor > MaxSampleFreshFor {
		return nil, ErrInvalid
	}
	now = now.UTC()
	rows, err := reader.pool.Query(ctx, `
WITH requested(ordinal,service,kind,source,metric_name,scope) AS (
  VALUES
    (1,'app','up','app','service_up','app'),
    (2,'caddy','up','host','service_up','caddy'),
    (3,'postgres','up','postgres','service_up','postgres'),
    (4,'postgres','latency','postgres','service_latency_milliseconds','postgres'),
    (5,'redis','up','redis','service_up','redis'),
    (6,'redis','latency','redis','service_latency_milliseconds','redis'),
    (7,'object_store','up','object_store','service_up','object_store'),
    (8,'object_store','latency','object_store','service_latency_milliseconds','object_store'),
    (9,'worker','up','worker','service_up','worker')
)
SELECT requested.ordinal,requested.service,requested.kind,requested.source,
       requested.metric_name,requested.scope,
       sample.value,sample.unit,sample.observed_at,sample.window_started_at
FROM requested
LEFT JOIN LATERAL (
  SELECT value,unit,observed_at,window_started_at
  FROM operational_samples
  WHERE source=requested.source
    AND metric_name=requested.metric_name
    AND scope=requested.scope
  ORDER BY observed_at DESC,id DESC
  LIMIT 1
) AS sample ON true
ORDER BY requested.ordinal`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	indexed := make(map[DashboardService]dashboardServiceSamples, len(dashboardServiceOrder))
	indexed[ServicePostgres] = dashboardServiceSamples{needsLatency: true}
	indexed[ServiceRedis] = dashboardServiceSamples{needsLatency: true}
	indexed[ServiceObjectStore] = dashboardServiceSamples{needsLatency: true}
	rowIndex := 0
	for rows.Next() {
		var (
			ordinal         int
			service         string
			kind            string
			source          string
			metric          string
			scope           string
			value           *float64
			unit            *string
			observedAt      *time.Time
			windowStartedAt *time.Time
		)
		if err := rows.Scan(
			&ordinal, &service, &kind, &source, &metric, &scope,
			&value, &unit, &observedAt, &windowStartedAt,
		); err != nil {
			return nil, err
		}
		if rowIndex >= len(dashboardServiceSampleOrder) {
			return nil, ErrInvalid
		}
		expected := dashboardServiceSampleOrder[rowIndex]
		if ordinal != rowIndex+1 ||
			DashboardService(service) != expected.service ||
			kind != expected.kind ||
			SampleSource(source) != expected.source ||
			SampleMetric(metric) != expected.metric ||
			SampleScope(scope) != expected.scope {
			return nil, ErrInvalid
		}
		sample, present, err := dashboardServiceSample(
			expected, value, unit, observedAt, windowStartedAt, now,
		)
		if err != nil {
			return nil, err
		}
		current := indexed[expected.service]
		switch expected.kind {
		case "up":
			current.up, current.upPresent = sample, present
		case "latency":
			current.latency, current.latencyPresent = sample, present
		default:
			return nil, ErrInvalid
		}
		indexed[expected.service] = current
		rowIndex++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rowIndex != len(dashboardServiceSampleOrder) {
		return nil, ErrInvalid
	}

	out := make([]ServiceHealth, 0, len(dashboardServiceOrder))
	for _, service := range dashboardServiceOrder {
		values := indexed[service]
		if !values.upPresent {
			out = append(out, ServiceHealth{
				Service:    service,
				State:      DataStateEmpty,
				ObservedAt: cloneDashboardTime(&now),
			})
			continue
		}
		observedAt := values.up.ObservedAt
		state := DataStateDegraded
		latencyMilliseconds := int64(0)
		if values.latencyPresent {
			if values.latency.ObservedAt.Before(observedAt) {
				observedAt = values.latency.ObservedAt
			}
			latencyMilliseconds, err = dashboardLatencyMilliseconds(values.latency.Value)
			if err != nil {
				return nil, err
			}
		}
		if observedAt.Before(now.Add(-reader.freshFor)) {
			state = DataStateStale
		} else if values.up.Value == 1 &&
			(!values.needsLatency || values.latencyPresent) {
			state = DataStateHealthy
		}
		out = append(out, ServiceHealth{
			Service:             service,
			State:               state,
			ObservedAt:          cloneDashboardTime(&observedAt),
			LatencyMilliseconds: latencyMilliseconds,
		})
	}
	return out, nil
}

func dashboardServiceSample(
	series dashboardServiceSeries,
	value *float64,
	unit *string,
	observedAt *time.Time,
	windowStartedAt *time.Time,
	now time.Time,
) (Sample, bool, error) {
	if value == nil && unit == nil && observedAt == nil && windowStartedAt == nil {
		return Sample{}, false, nil
	}
	if value == nil || unit == nil || observedAt == nil {
		return Sample{}, false, ErrInvalid
	}
	sample := Sample{
		Source:          series.source,
		Metric:          series.metric,
		Scope:           series.scope,
		Value:           *value,
		Unit:            SampleUnit(*unit),
		ObservedAt:      observedAt.UTC(),
		WindowStartedAt: utcSampleTime(windowStartedAt),
	}
	if sample.ObservedAt.After(now) || ValidateSample(sample, now) != nil {
		return Sample{}, false, ErrInvalid
	}
	return sample, true, nil
}

func dashboardLatencyMilliseconds(value float64) (int64, error) {
	rounded := math.Ceil(value)
	if !validDashboardDurationMilliseconds(int64(rounded)) ||
		rounded > float64(maxDashboardDuration) {
		return 0, ErrInvalid
	}
	return int64(rounded), nil
}
