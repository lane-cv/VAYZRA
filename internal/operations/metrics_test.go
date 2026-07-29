package operations

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMetricsRenderFixedPrometheusFamiliesAndLabelsDeterministically(t *testing.T) {
	now := time.Date(2026, 7, 30, 2, 30, 0, 0, time.UTC)
	window := now.Add(-5 * time.Minute)
	samples := []Sample{
		{
			Source: SampleSourceWorker, Metric: SampleMetricQueueItems,
			Scope: SampleScopeProcessing, Value: 7, Unit: SampleUnitCount,
			ObservedAt: now,
		},
		{
			Source: SampleSourceApp, Metric: SampleMetricAIRequestsTotal,
			Scope: SampleScopeSucceeded, Value: 12, Unit: SampleUnitCount,
			ObservedAt: now, WindowStartedAt: &window,
		},
		{
			Source: SampleSourcePostgres, Metric: SampleMetricServiceUp,
			Scope: SampleScopePostgres, Value: 1, Unit: SampleUnitBoolean,
			ObservedAt: now,
		},
		{
			Source: SampleSourceApp, Metric: SampleMetricBackupAgeSeconds,
			Scope: SampleScopeLocal, Value: 3600, Unit: SampleUnitSeconds,
			ObservedAt: now,
		},
		{
			Source: SampleSourceApp, Metric: SampleMetricServiceUp,
			Scope: SampleScopeApp, Value: 1, Unit: SampleUnitBoolean,
			ObservedAt: now,
		},
	}
	got, err := RenderMetrics(now, samples)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`happylearn_ai_requests_total{status="succeeded"} 12`,
		`happylearn_backup_age_seconds{repository="local"} 3600`,
		`happylearn_queue_items{queue="processing"} 7`,
		`happylearn_service_up{service="app"} 1`,
		`happylearn_service_up{service="postgres"} 1`,
		"",
	}, "\n")
	if string(got) != want {
		t.Fatalf("metrics:\n%s\nwant:\n%s", got, want)
	}
}

func TestMetricsRenderRejectsDynamicUnknownAndDuplicateSeries(t *testing.T) {
	now := time.Date(2026, 7, 30, 2, 30, 0, 0, time.UTC)
	valid := Sample{
		Source: SampleSourcePostgres, Metric: SampleMetricServiceUp,
		Scope: SampleScopePostgres, Value: 1, Unit: SampleUnitBoolean,
		ObservedAt: now,
	}
	tests := []struct {
		name    string
		samples []Sample
	}{
		{
			name: "unknown metric",
			samples: []Sample{{
				Source: SampleSourcePostgres, Metric: "database_password",
				Scope: SampleScopePostgres, Value: 1, Unit: SampleUnitCount,
				ObservedAt: now,
			}},
		},
		{
			name: "uuid-like label",
			samples: []Sample{{
				Source: SampleSourceHost, Metric: SampleMetricServiceUp,
				Scope: "550e8400-e29b-41d4-a716-446655440000",
				Value: 1, Unit: SampleUnitBoolean, ObservedAt: now,
			}},
		},
		{
			name:    "duplicate series",
			samples: []Sample{valid, valid},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderMetrics(now, tc.samples)
			if !errors.Is(err, ErrInvalid) || got != nil {
				t.Fatalf("RenderMetrics()=(%q,%v) want=(nil,ErrInvalid)", got, err)
			}
		})
	}
}
