package operations

import (
	"math"
	"testing"
	"time"
)

func TestDefaultAlertRulesCoverApprovedThresholds(t *testing.T) {
	settings := validSettings()
	settings.DiskWarningPercent = 76
	settings.DiskCriticalPercent = 91
	settings.AIErrorWarningPercent = 11
	settings.AIErrorCriticalPercent = 26
	settings.ProcessingQueueWarning = 21
	settings.ProcessingQueueCritical = 101

	rules, err := DefaultAlertRules(settings)
	if err != nil {
		t.Fatal(err)
	}
	type expectedRule struct {
		category       string
		warning        float64
		critical       float64
		direction      Direction
		minimumSamples int
	}
	expected := map[string]expectedRule{
		"filesystem_root_usage": {
			category: "storage", warning: 76, critical: 91,
			direction: DirectionAbove, minimumSamples: 1,
		},
		"filesystem_backup_usage": {
			category: "storage", warning: 76, critical: 91,
			direction: DirectionAbove, minimumSamples: 1,
		},
		"backup_local_age": {
			category: "backup", warning: 25 * 60 * 60, critical: 30 * 60 * 60,
			direction: DirectionAbove, minimumSamples: 1,
		},
		"backup_remote_replication": {
			category: "backup", warning: 1, critical: -1,
			direction: DirectionBelow, minimumSamples: 1,
		},
		"ai_error_rate": {
			category: "ai", warning: 11, critical: 26,
			direction: DirectionAbove, minimumSamples: 20,
		},
		"processing_queue_depth": {
			category: "processing", warning: 21, critical: 101,
			direction: DirectionAbove, minimumSamples: 1,
		},
		"processing_failures": {
			category: "processing", warning: 5, critical: 20,
			direction: DirectionAbove, minimumSamples: 1,
		},
		"login_failures": {
			category: "security", warning: 20, critical: 100,
			direction: DirectionAbove, minimumSamples: 1,
		},
		"authorization_denials": {
			category: "security", warning: 50, critical: 200,
			direction: DirectionAbove, minimumSamples: 1,
		},
	}
	if len(rules) != len(expected) {
		t.Fatalf("rules=%d want=%d", len(rules), len(expected))
	}
	for _, rule := range rules {
		want, ok := expected[rule.DedupeKey]
		if !ok {
			t.Fatalf("unexpected rule=%+v", rule)
		}
		if rule.Category != want.category ||
			rule.Warning != want.warning ||
			rule.Critical != want.critical ||
			rule.Direction != want.direction ||
			rule.MinimumSamples != want.minimumSamples ||
			rule.Summary == "" {
			t.Fatalf("rule=%+v want=%+v", rule, want)
		}
		delete(expected, rule.DedupeKey)
	}
	if len(expected) != 0 {
		t.Fatalf("missing rules=%v", expected)
	}
}

func TestClassifyAlertEvaluationHandlesAvailabilityMinimumsAndDirections(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	above := Rule{
		DedupeKey: "ai_error_rate", Category: "ai", Summary: "AI error rate",
		Warning: 10, Critical: 25, Direction: DirectionAbove, MinimumSamples: 20,
	}
	below := Rule{
		DedupeKey: "backup_remote_replication", Category: "backup",
		Summary: "Remote backup replication",
		Warning: 1, Critical: -1, Direction: DirectionBelow, MinimumSamples: 1,
	}
	tests := []struct {
		name       string
		evaluation Evaluation
		want       alertCondition
	}{
		{
			name: "unavailable",
			evaluation: Evaluation{
				Rule: above, Value: 0, ObservedAt: now,
				Available: false, SampleCount: 0,
			},
			want: alertConditionUnavailable,
		},
		{
			name: "below minimum is healthy",
			evaluation: Evaluation{
				Rule: above, Value: 100, ObservedAt: now,
				Available: true, SampleCount: 19,
			},
			want: alertConditionHealthy,
		},
		{
			name: "above healthy",
			evaluation: Evaluation{
				Rule: above, Value: 9.99, ObservedAt: now,
				Available: true, SampleCount: 20,
			},
			want: alertConditionHealthy,
		},
		{
			name: "above warning",
			evaluation: Evaluation{
				Rule: above, Value: 10, ObservedAt: now,
				Available: true, SampleCount: 20,
			},
			want: alertConditionWarning,
		},
		{
			name: "above critical",
			evaluation: Evaluation{
				Rule: above, Value: 25, ObservedAt: now,
				Available: true, SampleCount: 20,
			},
			want: alertConditionCritical,
		},
		{
			name: "below healthy",
			evaluation: Evaluation{
				Rule: below, Value: 1, ObservedAt: now,
				Available: true, SampleCount: 1,
			},
			want: alertConditionHealthy,
		},
		{
			name: "below warning",
			evaluation: Evaluation{
				Rule: below, Value: 0, ObservedAt: now,
				Available: true, SampleCount: 1,
			},
			want: alertConditionWarning,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := classifyAlertEvaluation(test.evaluation)
			if err != nil || got != test.want {
				t.Fatalf("condition=%q err=%v want=%q", got, err, test.want)
			}
		})
	}
}

func TestAlertEvaluationValidationRejectsUnsafeOrNonFiniteData(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	valid := Evaluation{
		Rule: Rule{
			DedupeKey: "processing_queue_depth", Category: "processing",
			Summary: "Processing queue depth",
			Warning: 20, Critical: 100, Direction: DirectionAbove,
			MinimumSamples: 1,
		},
		Value: 20, ObservedAt: now, Available: true, SampleCount: 1,
	}
	mutations := map[string]func(*Evaluation){
		"unsafe dedupe": func(value *Evaluation) {
			value.Rule.DedupeKey = "student/550e8400-e29b-41d4-a716-446655440000"
		},
		"unknown category": func(value *Evaluation) {
			value.Rule.Category = "private"
		},
		"oversized summary": func(value *Evaluation) {
			value.Rule.Summary = string(make([]byte, 241))
		},
		"non finite value": func(value *Evaluation) {
			value.Value = math.Inf(1)
		},
		"negative value": func(value *Evaluation) {
			value.Value = -1
		},
		"zero time": func(value *Evaluation) {
			value.ObservedAt = time.Time{}
		},
		"negative sample count": func(value *Evaluation) {
			value.SampleCount = -1
		},
		"invalid direction": func(value *Evaluation) {
			value.Rule.Direction = Direction("sideways")
		},
		"unordered above": func(value *Evaluation) {
			value.Rule.Critical = value.Rule.Warning
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if _, err := classifyAlertEvaluation(input); err == nil {
				t.Fatal("invalid evaluation accepted")
			}
		})
	}
}

func TestBuildAlertEvaluationsMapsEveryDefaultSeries(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	window := now.Add(-15 * time.Minute)
	settings := validSettings()
	rules, err := DefaultAlertRules(settings)
	if err != nil {
		t.Fatal(err)
	}
	samples := []Sample{
		alertSample(SampleSourceHost, SampleMetricFilesystemUsedPercent, SampleScopeRoot, 80, now, nil),
		alertSample(SampleSourceHost, SampleMetricFilesystemUsedPercent, SampleScopeBackup, 81, now, nil),
		alertSample(SampleSourceApp, SampleMetricBackupAgeSeconds, SampleScopeLocal, 26*60*60, now, nil),
		alertSample(SampleSourceApp, SampleMetricBackupRemoteUp, SampleScopeRemote, 0, now, nil),
		alertSample(SampleSourceApp, SampleMetricAIRequestsTotal, SampleScopeSucceeded, 72, now, &window),
		alertSample(SampleSourceApp, SampleMetricAIRequestsTotal, SampleScopeFailed, 28, now, &window),
		alertSample(SampleSourceWorker, SampleMetricQueueItems, SampleScopeProcessing, 22, now, nil),
		alertSample(SampleSourceWorker, SampleMetricQueueFailuresTotal, SampleScopeProcessing, 6, now, &window),
		alertSample(SampleSourceApp, SampleMetricSecurityEventsTotal, SampleScopeLoginFailure, 21, now, &window),
		alertSample(SampleSourceApp, SampleMetricSecurityEventsTotal, SampleScopeAuthorizationDenial, 51, now, &window),
	}
	evaluations, err := BuildAlertEvaluations(rules, samples, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != len(rules) {
		t.Fatalf("evaluations=%d rules=%d", len(evaluations), len(rules))
	}
	byKey := make(map[string]Evaluation, len(evaluations))
	for _, evaluation := range evaluations {
		byKey[evaluation.Rule.DedupeKey] = evaluation
		if !evaluation.Available || !evaluation.ObservedAt.Equal(now) {
			t.Fatalf("evaluation=%+v", evaluation)
		}
	}
	expectedValues := map[string]float64{
		"filesystem_root_usage":     80,
		"filesystem_backup_usage":   81,
		"backup_local_age":          26 * 60 * 60,
		"backup_remote_replication": 0,
		"ai_error_rate":             28,
		"processing_queue_depth":    22,
		"processing_failures":       6,
		"login_failures":            21,
		"authorization_denials":     51,
	}
	for key, value := range expectedValues {
		evaluation, ok := byKey[key]
		if !ok || math.Abs(evaluation.Value-value) > 1e-9 {
			t.Fatalf("key=%q evaluation=%+v wantValue=%v", key, evaluation, value)
		}
		if key == "ai_error_rate" && evaluation.SampleCount != 100 {
			t.Fatalf("AI sampleCount=%d", evaluation.SampleCount)
		}
	}
}

func TestBuildAlertEvaluationsTreatsMissingAndStaleAsUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	settings := validSettings()
	rules, err := DefaultAlertRules(settings)
	if err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-DashboardSampleFreshFor - time.Nanosecond)
	samples := []Sample{
		alertSample(SampleSourceHost, SampleMetricFilesystemUsedPercent, SampleScopeRoot, 10, stale, nil),
	}
	evaluations, err := BuildAlertEvaluations(rules, samples, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != len(rules)-1 {
		t.Fatalf("evaluations=%d want=%d", len(evaluations), len(rules)-1)
	}
	for _, evaluation := range evaluations {
		if evaluation.Rule.DedupeKey == "backup_remote_replication" {
			t.Fatal("remote rule evaluated when replication is not configured")
		}
		if evaluation.Available {
			t.Fatalf("unexpected available evaluation=%+v", evaluation)
		}
		if !evaluation.ObservedAt.Equal(now) {
			t.Fatalf("unavailable observedAt=%s want=%s", evaluation.ObservedAt, now)
		}
	}
}

func TestBuildAlertEvaluationsRejectsDuplicateOrMismatchedSeries(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	settings := validSettings()
	rules, err := DefaultAlertRules(settings)
	if err != nil {
		t.Fatal(err)
	}
	sample := alertSample(
		SampleSourceHost,
		SampleMetricFilesystemUsedPercent,
		SampleScopeRoot,
		10,
		now,
		nil,
	)
	if _, err := BuildAlertEvaluations(rules, []Sample{sample, sample}, now, false); err == nil {
		t.Fatal("duplicate series accepted")
	}
	sample.Unit = SampleUnitCount
	if _, err := BuildAlertEvaluations(rules, []Sample{sample}, now, false); err == nil {
		t.Fatal("mismatched sample accepted")
	}
	window := now.Add(-15 * time.Minute)
	oversizedTotal := []Sample{
		alertSample(
			SampleSourceApp,
			SampleMetricAIRequestsTotal,
			SampleScopeSucceeded,
			maxExactFloatInteger,
			now,
			&window,
		),
		alertSample(
			SampleSourceApp,
			SampleMetricAIRequestsTotal,
			SampleScopeFailed,
			maxExactFloatInteger,
			now,
			&window,
		),
	}
	if _, err := BuildAlertEvaluations(rules, oversizedTotal, now, false); err == nil {
		t.Fatal("inexact AI terminal total accepted")
	}
}

func alertSample(
	source SampleSource,
	metric SampleMetric,
	scope SampleScope,
	value float64,
	observedAt time.Time,
	windowStartedAt *time.Time,
) Sample {
	unit := SampleUnitCount
	switch metric {
	case SampleMetricFilesystemUsedPercent:
		unit = SampleUnitPercent
	case SampleMetricBackupAgeSeconds:
		unit = SampleUnitSeconds
	case SampleMetricBackupRemoteUp:
		unit = SampleUnitBoolean
	}
	return Sample{
		Source: source, Metric: metric, Scope: scope, Value: value, Unit: unit,
		ObservedAt: observedAt, WindowStartedAt: windowStartedAt,
	}
}
