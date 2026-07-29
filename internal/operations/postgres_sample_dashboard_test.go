package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPostgresSampleDashboardReaderReadsConsistentStorageStates(t *testing.T) {
	ctx := context.Background()
	pool, samples := migratedSampleStore(t)
	now := sampleTestClock()
	reader, err := NewPostgresSampleDashboardReader(pool, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		values    []Sample
		wantState DataState
		wantUsed  int64
		wantTotal int64
		wantAt    time.Time
		wantErr   error
	}{
		{
			name: "fresh below warning",
			values: []Sample{
				storageSample(SampleMetricObjectUsedBytes, 500, now.Add(-30*time.Second)),
				storageSample(SampleMetricObjectCapacityBytes, 1000, now.Add(-20*time.Second)),
			},
			wantState: DataStateHealthy,
			wantUsed:  500,
			wantTotal: 1000,
			wantAt:    now.Add(-30 * time.Second),
		},
		{
			name: "warning threshold is degraded",
			values: []Sample{
				storageSample(SampleMetricObjectUsedBytes, 750, now.Add(-time.Second)),
				storageSample(SampleMetricObjectCapacityBytes, 1000, now.Add(-time.Second)),
			},
			wantState: DataStateDegraded,
			wantUsed:  750,
			wantTotal: 1000,
			wantAt:    now.Add(-time.Second),
		},
		{
			name: "exact freshness boundary is fresh",
			values: []Sample{
				storageSample(SampleMetricObjectUsedBytes, 1, now.Add(-time.Minute)),
				storageSample(SampleMetricObjectCapacityBytes, 10, now.Add(-time.Minute)),
			},
			wantState: DataStateHealthy,
			wantUsed:  1,
			wantTotal: 10,
			wantAt:    now.Add(-time.Minute),
		},
		{
			name: "one stale member makes the pair stale",
			values: []Sample{
				storageSample(SampleMetricObjectUsedBytes, 500, now.Add(-time.Minute-time.Microsecond)),
				storageSample(SampleMetricObjectCapacityBytes, 1000, now.Add(-time.Second)),
			},
			wantState: DataStateStale,
			wantUsed:  500,
			wantTotal: 1000,
			wantAt:    now.Add(-time.Minute - time.Microsecond),
		},
		{
			name:      "both missing is explicit empty",
			wantState: DataStateEmpty,
			wantAt:    now,
		},
		{
			name: "partial pair is explicit empty without partial values",
			values: []Sample{
				storageSample(SampleMetricObjectUsedBytes, 500, now.Add(-time.Second)),
			},
			wantState: DataStateEmpty,
			wantAt:    now,
		},
		{
			name: "zero capacity is degraded",
			values: []Sample{
				storageSample(SampleMetricObjectUsedBytes, 0, now.Add(-time.Second)),
				storageSample(SampleMetricObjectCapacityBytes, 0, now.Add(-time.Second)),
			},
			wantState: DataStateDegraded,
			wantAt:    now.Add(-time.Second),
		},
		{
			name: "used beyond capacity fails closed",
			values: []Sample{
				storageSample(SampleMetricObjectUsedBytes, 1001, now.Add(-time.Second)),
				storageSample(SampleMetricObjectCapacityBytes, 1000, now.Add(-time.Second)),
			},
			wantErr: ErrInvalid,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE operational_samples`); err != nil {
				t.Fatal(err)
			}
			if len(test.values) != 0 {
				if err := samples.InsertSamples(ctx, now, test.values); err != nil {
					t.Fatal(err)
				}
			}
			got, err := reader.ReadStorageSummary(ctx, now)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("ReadStorageSummary() error=%v want=%v", err, test.wantErr)
				}
				if got != (StorageSummary{}) {
					t.Fatalf("failed storage summary=%+v want zero", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.State != test.wantState ||
				got.UsedBytes != test.wantUsed ||
				got.CapacityBytes != test.wantTotal ||
				got.WarningPercent != map[bool]int{true: 0, false: 75}[test.wantState == DataStateEmpty] ||
				got.ObservedAt == nil ||
				!got.ObservedAt.Equal(test.wantAt) ||
				got.ObservedAt.Location() != time.UTC {
				t.Fatalf("ReadStorageSummary()=%+v", got)
			}
		})
	}
}

func TestPostgresSampleDashboardReaderStorageUsesLatestFixedSeriesAndRejectsPollution(t *testing.T) {
	ctx := context.Background()
	pool, samples := migratedSampleStore(t)
	now := sampleTestClock()
	reader, err := NewPostgresSampleDashboardReader(pool, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := samples.InsertSamples(ctx, now, []Sample{
		storageSample(SampleMetricObjectUsedBytes, 900, now.Add(-30*time.Second)),
		storageSample(SampleMetricObjectUsedBytes, 100, now.Add(-time.Second)),
		storageSample(SampleMetricObjectCapacityBytes, 1000, now.Add(-time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
) VALUES
  ('host','object_used_bytes','object_store',999,'bytes',$1,NULL),
  ('object_store','private_metric','object_store',999,'bytes',$1,NULL)`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadStorageSummary(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsedBytes != 100 || got.CapacityBytes != 1000 || got.State != DataStateHealthy {
		t.Fatalf("fixed latest storage=%+v", got)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
) VALUES ('object_store','object_used_bytes','object_store',50,'private_unit',$1,NULL)`,
		now.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	got, err = reader.ReadStorageSummary(ctx, now.Add(time.Second))
	if !errors.Is(err, ErrInvalid) || got != (StorageSummary{}) {
		t.Fatalf("polluted storage=%+v error=%v", got, err)
	}
}

func TestPostgresSampleDashboardReaderRejectsInvalidConstructionAndCalls(t *testing.T) {
	ctx := context.Background()
	pool, _ := migratedSampleStore(t)
	now := sampleTestClock()
	for _, freshFor := range []time.Duration{0, MaxSampleFreshFor + time.Nanosecond} {
		if reader, err := NewPostgresSampleDashboardReader(pool, freshFor); !errors.Is(err, ErrInvalid) || reader != nil {
			t.Fatalf("freshFor=%v reader=%v error=%v", freshFor, reader, err)
		}
	}
	if reader, err := NewPostgresSampleDashboardReader(nil, time.Minute); !errors.Is(err, ErrInvalid) || reader != nil {
		t.Fatalf("nil pool reader=%v error=%v", reader, err)
	}
	reader, err := NewPostgresSampleDashboardReader(pool, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reader.ReadStorageSummary(nil, now); !errors.Is(err, ErrInvalid) || got != (StorageSummary{}) {
		t.Fatalf("nil context storage=%+v error=%v", got, err)
	}
	if got, err := reader.ReadStorageSummary(ctx, time.Time{}); !errors.Is(err, ErrInvalid) || got != (StorageSummary{}) {
		t.Fatalf("zero clock storage=%+v error=%v", got, err)
	}
	if got, err := reader.ReadServiceHealth(nil, now); !errors.Is(err, ErrInvalid) || got != nil {
		t.Fatalf("nil context services=%+v error=%v", got, err)
	}
	if got, err := reader.ReadServiceHealth(ctx, time.Time{}); !errors.Is(err, ErrInvalid) || got != nil {
		t.Fatalf("zero clock services=%+v error=%v", got, err)
	}
}

func storageSample(metric SampleMetric, value float64, observedAt time.Time) Sample {
	return Sample{
		Source:     SampleSourceObjectStore,
		Metric:     metric,
		Scope:      SampleScopeObjectStore,
		Value:      value,
		Unit:       SampleUnitBytes,
		ObservedAt: observedAt,
	}
}

func TestPostgresSampleDashboardReaderReadsFixedOrderedServiceHealth(t *testing.T) {
	ctx := context.Background()
	pool, samples := migratedSampleStore(t)
	now := sampleTestClock()
	reader, err := NewPostgresSampleDashboardReader(pool, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	values := []Sample{
		serviceUpSample(ServiceApp, 1, now.Add(-6*time.Second)),
		serviceUpSample(ServicePostgres, 1, now.Add(-5*time.Second)),
		serviceLatencySample(ServicePostgres, 12, now.Add(-4*time.Second)),
		serviceUpSample(ServiceRedis, 1, now.Add(-3*time.Second)),
		serviceLatencySample(ServiceRedis, 20, now.Add(-time.Minute-time.Microsecond)),
		serviceUpSample(ServiceObjectStore, 0, now.Add(-2*time.Second)),
		serviceLatencySample(ServiceObjectStore, 30, now.Add(-time.Second)),
		serviceUpSample(ServiceWorker, 1, now.Add(-time.Minute-time.Microsecond)),
	}
	if err := samples.InsertSamples(ctx, now, values); err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadServiceHealth(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []ServiceHealth{
		{
			Service: ServiceApp, State: DataStateDegraded,
			ObservedAt: cloneDashboardTimePointer(now.Add(-6 * time.Second)),
		},
		{
			Service: ServiceCaddy, State: DataStateEmpty,
			ObservedAt: cloneDashboardTimePointer(now),
		},
		{
			Service: ServicePostgres, State: DataStateHealthy,
			ObservedAt:          cloneDashboardTimePointer(now.Add(-5 * time.Second)),
			LatencyMilliseconds: 12,
		},
		{
			Service: ServiceRedis, State: DataStateStale,
			ObservedAt:          cloneDashboardTimePointer(now.Add(-time.Minute - time.Microsecond)),
			LatencyMilliseconds: 20,
		},
		{
			Service: ServiceObjectStore, State: DataStateDegraded,
			ObservedAt:          cloneDashboardTimePointer(now.Add(-2 * time.Second)),
			LatencyMilliseconds: 30,
		},
		{
			Service: ServiceWorker, State: DataStateStale,
			ObservedAt: cloneDashboardTimePointer(now.Add(-time.Minute - time.Microsecond)),
		},
	}
	if len(got) != len(want) {
		t.Fatalf("services=%+v", got)
	}
	for index := range want {
		if got[index].Service != want[index].Service ||
			got[index].State != want[index].State ||
			got[index].LatencyMilliseconds != want[index].LatencyMilliseconds ||
			got[index].ObservedAt == nil ||
			!got[index].ObservedAt.Equal(*want[index].ObservedAt) {
			t.Fatalf("services[%d]=%+v want=%+v", index, got[index], want[index])
		}
	}
}

func TestPostgresSampleDashboardReaderUsesExactServiceSourcesAndNeverInventsLatency(t *testing.T) {
	ctx := context.Background()
	pool, samples := migratedSampleStore(t)
	now := sampleTestClock()
	reader, err := NewPostgresSampleDashboardReader(pool, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	values := []Sample{
		serviceUpSample(ServiceApp, 1, now.Add(-2*time.Second)),
		serviceUpSample(ServiceCaddy, 1, now.Add(-2*time.Second)),
		serviceUpSample(ServicePostgres, 1, now.Add(-2*time.Second)),
		serviceUpSample(ServiceRedis, 1, now.Add(-2*time.Second)),
		serviceUpSample(ServiceObjectStore, 1, now.Add(-2*time.Second)),
		serviceUpSample(ServiceWorker, 1, now.Add(-2*time.Second)),
		serviceLatencySample(ServiceRedis, 0.25, now.Add(-time.Second)),
		serviceLatencySample(ServiceObjectStore, 9, now.Add(-time.Second)),
		{
			Source: SampleSourceHost, Metric: SampleMetricServiceUp,
			Scope: SampleScopeApp, Value: 0, Unit: SampleUnitBoolean,
			ObservedAt: now,
		},
		{
			Source: SampleSourceHost, Metric: SampleMetricServiceUp,
			Scope: SampleScopePostgres, Value: 0, Unit: SampleUnitBoolean,
			ObservedAt: now,
		},
	}
	if err := samples.InsertSamples(ctx, now, values); err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadServiceHealth(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Service != ServiceApp || got[0].State != DataStateDegraded ||
		got[0].LatencyMilliseconds != 0 ||
		got[1].Service != ServiceCaddy || got[1].State != DataStateDegraded ||
		got[1].LatencyMilliseconds != 0 ||
		got[2].Service != ServicePostgres || got[2].State != DataStateDegraded ||
	got[2].LatencyMilliseconds != 0 ||
		got[3].Service != ServiceRedis || got[3].State != DataStateHealthy ||
		got[3].LatencyMilliseconds != 1 ||
		got[4].Service != ServiceObjectStore || got[4].State != DataStateHealthy ||
		got[4].LatencyMilliseconds != 9 ||
		got[5].Service != ServiceWorker || got[5].State != DataStateDegraded ||
		got[5].LatencyMilliseconds != 0 {
		t.Fatalf("source or latency mapping=%+v", got)
	}
}

func TestPostgresSampleDashboardReaderServicePollutionFailsClosed(t *testing.T) {
	ctx := context.Background()
	pool, samples := migratedSampleStore(t)
	now := sampleTestClock()
	reader, err := NewPostgresSampleDashboardReader(pool, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := samples.InsertSamples(ctx, now, []Sample{
		serviceUpSample(ServicePostgres, 1, now.Add(-time.Second)),
		serviceLatencySample(ServicePostgres, 5, now.Add(-time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO operational_samples(
  source,metric_name,scope,value,unit,observed_at,window_started_at
) VALUES ('postgres','service_latency_milliseconds','postgres',99,'private_unit',$1,NULL)`,
		now,
	); err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadServiceHealth(ctx, now)
	if !errors.Is(err, ErrInvalid) || got != nil {
		t.Fatalf("polluted services=%+v error=%v", got, err)
	}
}

func serviceUpSample(
	service DashboardService,
	value float64,
	observedAt time.Time,
) Sample {
	source := map[DashboardService]SampleSource{
		ServiceApp:         SampleSourceApp,
		ServiceCaddy:       SampleSourceHost,
		ServicePostgres:    SampleSourcePostgres,
		ServiceRedis:       SampleSourceRedis,
		ServiceObjectStore: SampleSourceObjectStore,
		ServiceWorker:      SampleSourceWorker,
	}[service]
	return Sample{
		Source: source, Metric: SampleMetricServiceUp,
		Scope: SampleScope(service), Value: value,
		Unit: SampleUnitBoolean, ObservedAt: observedAt,
	}
}

func serviceLatencySample(
	service DashboardService,
	value float64,
	observedAt time.Time,
) Sample {
	source := map[DashboardService]SampleSource{
		ServicePostgres:    SampleSourcePostgres,
		ServiceRedis:       SampleSourceRedis,
		ServiceObjectStore: SampleSourceObjectStore,
	}[service]
	return Sample{
		Source: source, Metric: SampleMetricServiceLatencyMilliseconds,
		Scope: SampleScope(service), Value: value,
		Unit: SampleUnitMilliseconds, ObservedAt: observedAt,
	}
}

func cloneDashboardTimePointer(value time.Time) *time.Time {
	copy := value.UTC()
	return &copy
}
