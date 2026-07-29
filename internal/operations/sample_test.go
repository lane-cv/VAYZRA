package operations

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestSampleHasOnlyBoundedAggregateFields(t *testing.T) {
	sampleType := reflect.TypeOf(Sample{})
	want := []string{
		"Source",
		"Metric",
		"Scope",
		"Value",
		"Unit",
		"ObservedAt",
		"WindowStartedAt",
	}
	if sampleType.NumField() != len(want) {
		t.Fatalf("Sample fields=%d want=%d", sampleType.NumField(), len(want))
	}
	for index, name := range want {
		if field := sampleType.Field(index); field.Name != name {
			t.Fatalf("Sample field[%d]=%q want=%q", index, field.Name, name)
		}
	}
}

func TestValidateSampleAcceptsExactMetricSignatures(t *testing.T) {
	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	window := now.Add(-15 * time.Minute)
	tests := map[string]Sample{
		"dependency state": {
			Source:     SampleSourcePostgres,
			Metric:     SampleMetricServiceUp,
			Scope:      SampleScopePostgres,
			Value:      1,
			Unit:       SampleUnitBoolean,
			ObservedAt: now,
		},
		"queue depth": {
			Source:     SampleSourceWorker,
			Metric:     SampleMetricQueueItems,
			Scope:      SampleScopeProcessing,
			Value:      12,
			Unit:       SampleUnitCount,
			ObservedAt: now,
		},
		"windowed AI outcome": {
			Source:          SampleSourceApp,
			Metric:          SampleMetricAIRequestsTotal,
			Scope:           SampleScopeSucceeded,
			Value:           20,
			Unit:            SampleUnitCount,
			ObservedAt:      now,
			WindowStartedAt: &window,
		},
		"host filesystem": {
			Source:     SampleSourceHost,
			Metric:     SampleMetricFilesystemUsedPercent,
			Scope:      SampleScopeBackup,
			Value:      74.5,
			Unit:       SampleUnitPercent,
			ObservedAt: now.Add(2 * time.Minute),
		},
	}
	for name, sample := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSample(sample, now); err != nil {
				t.Fatalf("ValidateSample() error=%v", err)
			}
		})
	}
}

func TestValidateSampleRejectsIndividuallyAllowedButIllegalCombinations(t *testing.T) {
	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	base := Sample{
		Source:     SampleSourceWorker,
		Metric:     SampleMetricQueueItems,
		Scope:      SampleScopeProcessing,
		Value:      1,
		Unit:       SampleUnitCount,
		ObservedAt: now,
	}
	tests := map[string]func(*Sample){
		"wrong source": func(sample *Sample) {
			sample.Source = SampleSourceHost
		},
		"wrong scope": func(sample *Sample) {
			sample.Scope = SampleScopeLocal
		},
		"wrong unit": func(sample *Sample) {
			sample.Unit = SampleUnitPercent
		},
		"unknown source": func(sample *Sample) {
			sample.Source = SampleSource("database")
		},
		"unknown metric": func(sample *Sample) {
			sample.Metric = SampleMetric("custom_metric")
		},
		"unknown scope": func(sample *Sample) {
			sample.Scope = SampleScope("custom")
		},
		"unknown unit": func(sample *Sample) {
			sample.Unit = SampleUnit("widgets")
		},
		"UUID-like scope": func(sample *Sample) {
			sample.Scope = SampleScope("11111111-1111-4111-8111-111111111111")
		},
		"user label": func(sample *Sample) {
			sample.Scope = SampleScope("user_42")
		},
		"slash": func(sample *Sample) {
			sample.Scope = SampleScope("processing/private")
		},
		"whitespace": func(sample *Sample) {
			sample.Scope = SampleScope("processing queue")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			sample := base
			mutate(&sample)
			if err := ValidateSample(sample, now); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateSample() error=%v want ErrInvalid", err)
			}
		})
	}
}

func TestValidateSampleRejectsUnsafeValues(t *testing.T) {
	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	tests := map[string]Sample{
		"NaN": {
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: math.NaN(),
			Unit: SampleUnitCount, ObservedAt: now,
		},
		"positive infinity": {
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: math.Inf(1),
			Unit: SampleUnitCount, ObservedAt: now,
		},
		"negative infinity": {
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: math.Inf(-1),
			Unit: SampleUnitCount, ObservedAt: now,
		},
		"negative count": {
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: -1,
			Unit: SampleUnitCount, ObservedAt: now,
		},
		"fractional count": {
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: 1.5,
			Unit: SampleUnitCount, ObservedAt: now,
		},
		"unsafe integer": {
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: float64(1 << 54),
			Unit: SampleUnitCount, ObservedAt: now,
		},
		"boolean above one": {
			Source: SampleSourcePostgres, Metric: SampleMetricServiceUp,
			Scope: SampleScopePostgres, Value: 2,
			Unit: SampleUnitBoolean, ObservedAt: now,
		},
		"percent above one hundred": {
			Source: SampleSourceHost, Metric: SampleMetricFilesystemUsedPercent,
			Scope: SampleScopeRoot, Value: 100.01,
			Unit: SampleUnitPercent, ObservedAt: now,
		},
	}
	for name, sample := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSample(sample, now); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateSample() error=%v want ErrInvalid", err)
			}
		})
	}
}

func TestValidateSampleRejectsTimeAnomaliesAndWindowShape(t *testing.T) {
	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	base := Sample{
		Source:     SampleSourceWorker,
		Metric:     SampleMetricQueueItems,
		Scope:      SampleScopeProcessing,
		Value:      1,
		Unit:       SampleUnitCount,
		ObservedAt: now,
	}
	windowed := Sample{
		Source:     SampleSourceApp,
		Metric:     SampleMetricAIRequestsTotal,
		Scope:      SampleScopeSucceeded,
		Value:      1,
		Unit:       SampleUnitCount,
		ObservedAt: now,
	}
	zero := time.Time{}
	after := now.Add(time.Second)
	validWindow := now.Add(-15 * time.Minute)
	tests := map[string]struct {
		sample Sample
		now    time.Time
	}{
		"zero validation clock":            {sample: base},
		"zero observed time":               {sample: withSampleObserved(base, time.Time{}), now: now},
		"too far in future":                {sample: withSampleObserved(base, now.Add(2*time.Minute+time.Nanosecond)), now: now},
		"instant metric with window":       {sample: withSampleWindow(base, &validWindow), now: now},
		"windowed metric missing window":   {sample: windowed, now: now},
		"windowed metric with zero window": {sample: withSampleWindow(windowed, &zero), now: now},
		"window starts after observation":  {sample: withSampleWindow(windowed, &after), now: now},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSample(test.sample, test.now); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateSample() error=%v want ErrInvalid", err)
			}
		})
	}
}

func withSampleObserved(sample Sample, observedAt time.Time) Sample {
	sample.ObservedAt = observedAt
	return sample
}

func withSampleWindow(sample Sample, windowStartedAt *time.Time) Sample {
	sample.WindowStartedAt = windowStartedAt
	return sample
}
