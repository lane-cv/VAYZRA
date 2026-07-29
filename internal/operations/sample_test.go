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

func TestSampleRuleMatrixMatchesTheCompleteFixedContract(t *testing.T) {
	expected := expectedSampleRuleMatrix()
	if !reflect.DeepEqual(sampleRules, expected) {
		t.Fatalf(
			"sample rule matrix mismatch:\n got=%#v\nwant=%#v",
			sampleRules,
			expected,
		)
	}
}

func TestNormalizeHostServiceScopeAcceptsOnlyFixedHostNames(t *testing.T) {
	for raw, want := range map[string]SampleScope{
		"app":      SampleScopeApp,
		"caddy":    SampleScopeCaddy,
		"postgres": SampleScopePostgres,
		"redis":    SampleScopeRedis,
		"minio":    SampleScopeObjectStore,
		"worker":   SampleScopeWorker,
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := NormalizeHostServiceScope(raw)
			if err != nil || got != want {
				t.Fatalf("NormalizeHostServiceScope(%q)=(%q,%v) want=(%q,nil)", raw, got, err, want)
			}
		})
	}
	for _, raw := range []string{
		"",
		"object_store",
		"MINIO",
		" minio",
		"minio ",
		"minio/private",
		"student_42",
		"11111111-1111-4111-8111-111111111111",
	} {
		t.Run("reject "+raw, func(t *testing.T) {
			got, err := NormalizeHostServiceScope(raw)
			if got != "" || !errors.Is(err, ErrInvalid) {
				t.Fatalf("NormalizeHostServiceScope(%q)=(%q,%v) want=(empty,ErrInvalid)", raw, got, err)
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
		"service ready is app only": func(sample *Sample) {
			sample.Source = SampleSourcePostgres
			sample.Metric = SampleMetricServiceReady
			sample.Scope = SampleScopePostgres
			sample.Unit = SampleUnitBoolean
		},
		"AI run state is not a worker queue": func(sample *Sample) {
			sample.Metric = SampleMetricQueueItems
			sample.Scope = SampleScopeAI
		},
		"AI run state uses app source": func(sample *Sample) {
			sample.Metric = SampleMetricAIRuns
			sample.Scope = SampleScopeQueued
		},
		"remote backup availability is not local": func(sample *Sample) {
			sample.Source = SampleSourceApp
			sample.Metric = SampleMetricBackupRemoteUp
			sample.Scope = SampleScopeLocal
			sample.Unit = SampleUnitBoolean
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
			Source: SampleSourceApp, Metric: SampleMetricServiceUp,
			Scope: SampleScopeApp, Value: 2,
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

func TestValidateSampleNumericBoundaries(t *testing.T) {
	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	for name, sample := range map[string]Sample{
		"zero percent": {
			Source: SampleSourceHost, Metric: SampleMetricFilesystemUsedPercent,
			Scope: SampleScopeRoot, Value: 0,
			Unit: SampleUnitPercent, ObservedAt: now,
		},
		"one hundred percent": {
			Source: SampleSourceHost, Metric: SampleMetricFilesystemUsedPercent,
			Scope: SampleScopeRoot, Value: 100,
			Unit: SampleUnitPercent, ObservedAt: now,
		},
		"largest exact count": {
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: float64(1<<53 - 1),
			Unit: SampleUnitCount, ObservedAt: now,
		},
	} {
		t.Run("accept "+name, func(t *testing.T) {
			if err := ValidateSample(sample, now); err != nil {
				t.Fatalf("ValidateSample() error=%v", err)
			}
		})
	}

	negativeZero := math.Copysign(0, -1)
	window := now.Add(-time.Minute)
	for name, sample := range map[string]Sample{
		"negative zero": {
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: negativeZero,
			Unit: SampleUnitCount, ObservedAt: now,
		},
		"first unsafe integer": {
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: float64(1 << 53),
			Unit: SampleUnitCount, ObservedAt: now,
		},
		"fractional bytes": {
			Source: SampleSourceHost, Metric: SampleMetricHostServiceMemoryBytes,
			Scope: SampleScopeApp, Value: 1.5,
			Unit: SampleUnitBytes, ObservedAt: now,
		},
		"fractional version": {
			Source: SampleSourcePostgres, Metric: SampleMetricPostgresMigrationVersion,
			Scope: SampleScopePostgres, Value: 1.5,
			Unit: SampleUnitVersion, ObservedAt: now,
		},
		"fractional micro USD": {
			Source: SampleSourceApp, Metric: SampleMetricAICostMicroUSD,
			Scope: SampleScopeTotal, Value: 1.5,
			Unit: SampleUnitMicroUSD, ObservedAt: now, WindowStartedAt: &window,
		},
		"percent below zero": {
			Source: SampleSourceHost, Metric: SampleMetricFilesystemUsedPercent,
			Scope: SampleScopeRoot, Value: -0.01,
			Unit: SampleUnitPercent, ObservedAt: now,
		},
		"percent above one hundred": {
			Source: SampleSourceHost, Metric: SampleMetricFilesystemUsedPercent,
			Scope: SampleScopeRoot, Value: 100.01,
			Unit: SampleUnitPercent, ObservedAt: now,
		},
	} {
		t.Run("reject "+name, func(t *testing.T) {
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
	equal := now
	validWindow := now.Add(-15 * time.Minute)
	exactWindow := now.Add(-maxSampleWindow)
	tooLongWindow := exactWindow.Add(-time.Nanosecond)
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
		"window equals observation":        {sample: withSampleWindow(windowed, &equal), now: now},
		"window starts after observation":  {sample: withSampleWindow(windowed, &after), now: now},
		"window exceeds maximum":           {sample: withSampleWindow(windowed, &tooLongWindow), now: now},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSample(test.sample, test.now); !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateSample() error=%v want ErrInvalid", err)
			}
		})
	}

	if err := ValidateSample(withSampleObserved(base, now.Add(maxSampleFutureSkew)), now); err != nil {
		t.Fatalf("exact future boundary error=%v", err)
	}
	if err := ValidateSample(withSampleWindow(windowed, &exactWindow), now); err != nil {
		t.Fatalf("exact window boundary error=%v", err)
	}
}

func expectedSampleRuleMatrix() map[sampleSeries]sampleRule {
	expected := make(map[sampleSeries]sampleRule)
	add := func(
		source SampleSource,
		metric SampleMetric,
		scope SampleScope,
		unit SampleUnit,
		windowPolicy sampleWindowPolicy,
	) {
		expected[sampleSeries{source: source, metric: metric, scope: scope}] = sampleRule{
			unit:         unit,
			windowPolicy: windowPolicy,
		}
	}

	add(SampleSourceApp, SampleMetricServiceUp, SampleScopeApp, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourcePostgres, SampleMetricServiceUp, SampleScopePostgres, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourceRedis, SampleMetricServiceUp, SampleScopeRedis, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourceObjectStore, SampleMetricServiceUp, SampleScopeObjectStore, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourceWorker, SampleMetricServiceUp, SampleScopeWorker, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourceApp, SampleMetricServiceReady, SampleScopeApp, SampleUnitBoolean, sampleWindowForbidden)
	for _, scope := range []SampleScope{
		SampleScopeApp,
		SampleScopeCaddy,
		SampleScopePostgres,
		SampleScopeRedis,
		SampleScopeObjectStore,
		SampleScopeWorker,
	} {
		add(SampleSourceHost, SampleMetricServiceUp, scope, SampleUnitBoolean, sampleWindowForbidden)
		add(SampleSourceHost, SampleMetricHostServiceCPUPercent, scope, SampleUnitPercent, sampleWindowForbidden)
		add(SampleSourceHost, SampleMetricHostServiceMemoryBytes, scope, SampleUnitBytes, sampleWindowForbidden)
		add(SampleSourceHost, SampleMetricHostServiceMemoryLimitBytes, scope, SampleUnitBytes, sampleWindowForbidden)
		add(SampleSourceHost, SampleMetricHostServiceRestarts, scope, SampleUnitCount, sampleWindowForbidden)
	}
	for _, sourceScope := range []struct {
		source SampleSource
		scope  SampleScope
	}{
		{SampleSourcePostgres, SampleScopePostgres},
		{SampleSourceRedis, SampleScopeRedis},
		{SampleSourceObjectStore, SampleScopeObjectStore},
	} {
		add(
			sourceScope.source,
			SampleMetricServiceLatencyMilliseconds,
			sourceScope.scope,
			SampleUnitMilliseconds,
			sampleWindowForbidden,
		)
	}
	add(SampleSourcePostgres, SampleMetricPostgresPoolInUse, SampleScopePostgres, SampleUnitCount, sampleWindowForbidden)
	add(SampleSourcePostgres, SampleMetricPostgresFailuresTotal, SampleScopePostgres, SampleUnitCount, sampleWindowRequired)
	add(SampleSourcePostgres, SampleMetricPostgresMigrationVersion, SampleScopePostgres, SampleUnitVersion, sampleWindowForbidden)
	add(SampleSourceRedis, SampleMetricRedisLimiterUp, SampleScopeRedis, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourceObjectStore, SampleMetricObjectCapacityBytes, SampleScopeObjectStore, SampleUnitBytes, sampleWindowForbidden)
	add(SampleSourceObjectStore, SampleMetricObjectUsedBytes, SampleScopeObjectStore, SampleUnitBytes, sampleWindowForbidden)
	add(SampleSourceObjectStore, SampleMetricObjectFailuresTotal, SampleScopeObjectStore, SampleUnitCount, sampleWindowRequired)
	for _, scope := range []SampleScope{SampleScopeProcessing, SampleScopeOutbox} {
		add(SampleSourceWorker, SampleMetricQueueItems, scope, SampleUnitCount, sampleWindowForbidden)
		add(SampleSourceWorker, SampleMetricQueueFailuresTotal, scope, SampleUnitCount, sampleWindowRequired)
	}
	for _, scope := range []SampleScope{SampleScopeLocal, SampleScopeRemote} {
		add(SampleSourceApp, SampleMetricBackupAgeSeconds, scope, SampleUnitSeconds, sampleWindowForbidden)
	}
	add(SampleSourceApp, SampleMetricBackupRemoteUp, SampleScopeRemote, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourceApp, SampleMetricRestoreAgeSeconds, SampleScopeLocal, SampleUnitSeconds, sampleWindowForbidden)
	for _, scope := range []SampleScope{SampleScopeQueued, SampleScopeStreaming, SampleScopeExpired} {
		add(SampleSourceApp, SampleMetricAIRuns, scope, SampleUnitCount, sampleWindowForbidden)
	}
	for _, scope := range []SampleScope{SampleScopeSucceeded, SampleScopeFailed} {
		add(SampleSourceApp, SampleMetricAIRequestsTotal, scope, SampleUnitCount, sampleWindowRequired)
	}
	for _, scope := range []SampleScope{SampleScopeFirstByte, SampleScopeTotal} {
		add(SampleSourceApp, SampleMetricAILatencyMilliseconds, scope, SampleUnitMilliseconds, sampleWindowRequired)
	}
	for _, scope := range []SampleScope{SampleScopeInput, SampleScopeOutput} {
		add(SampleSourceApp, SampleMetricAITokensTotal, scope, SampleUnitCount, sampleWindowRequired)
	}
	add(SampleSourceApp, SampleMetricAICostMicroUSD, SampleScopeTotal, SampleUnitMicroUSD, sampleWindowRequired)
	for _, scope := range []SampleScope{
		SampleScopeLoginFailure,
		SampleScopeAuthorizationDenial,
		SampleScopeFileAccessDenial,
	} {
		add(SampleSourceApp, SampleMetricSecurityEventsTotal, scope, SampleUnitCount, sampleWindowRequired)
	}
	for _, scope := range []SampleScope{SampleScopeRoot, SampleScopeBackup} {
		add(SampleSourceHost, SampleMetricFilesystemUsedPercent, scope, SampleUnitPercent, sampleWindowForbidden)
	}
	return expected
}

func withSampleObserved(sample Sample, observedAt time.Time) Sample {
	sample.ObservedAt = observedAt
	return sample
}

func withSampleWindow(sample Sample, windowStartedAt *time.Time) Sample {
	sample.WindowStartedAt = windowStartedAt
	return sample
}
