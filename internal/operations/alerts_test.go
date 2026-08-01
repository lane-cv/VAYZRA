package operations

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestDefaultAlertRulesCoverApprovedThresholds(t *testing.T) {
	settings := validSettings()
	settings.DiskWarningPercent = 76
	settings.DiskCriticalPercent = 91
	settings.BackupFilesystemWarningPercent = 77
	settings.BackupFilesystemCriticalPercent = 92
	settings.LocalBackupAgeWarningHours = 26
	settings.LocalBackupAgeCriticalHours = 31
	settings.AIErrorWarningPercent = 11
	settings.AIErrorCriticalPercent = 26
	settings.ProcessingQueueWarning = 21
	settings.ProcessingQueueCritical = 101
	settings.ProcessingFailureWarningCount = 6
	settings.ProcessingFailureCriticalCount = 21
	settings.LoginFailureWarningCount = 22
	settings.LoginFailureCriticalCount = 102
	settings.AuthorizationDenialWarningCount = 52
	settings.AuthorizationDenialCriticalCount = 202

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
			category: "storage", warning: 77, critical: 92,
			direction: DirectionAbove, minimumSamples: 1,
		},
		"backup_local_age": {
			category: "backup", warning: 26 * 60 * 60, critical: 31 * 60 * 60,
			direction: DirectionAbove, minimumSamples: 1,
		},
		AlertKeyBackupRemoteSync: {
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
			category: "processing", warning: 6, critical: 21,
			direction: DirectionAbove, minimumSamples: 1,
		},
		"login_failures": {
			category: "security", warning: 22, critical: 102,
			direction: DirectionAbove, minimumSamples: 1,
		},
		"authorization_denials": {
			category: "security", warning: 52, critical: 202,
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
		DedupeKey: AlertKeyBackupRemoteSync, Category: "backup",
		Summary: "Remote backup replication",
		Warning: 1, Critical: -1, Direction: DirectionBelow, MinimumSamples: 1,
	}
	tests := []struct {
		name       string
		evaluation Evaluation
		want       alertCondition
	}{
		{
			name: "unavailable is neutral",
			evaluation: Evaluation{
				Rule: above, Value: 0, ObservedAt: now,
				Available: false, SampleCount: 0,
			},
			want: alertConditionNeutral,
		},
		{
			name: "below minimum is neutral",
			evaluation: Evaluation{
				Rule: above, Value: 100, ObservedAt: now,
				Available: true, SampleCount: 19,
			},
			want: alertConditionNeutral,
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
	evaluations, err := BuildAlertEvaluations(
		rules,
		samples,
		now,
		RemoteReplicationEnabled,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != 2*len(rules) {
		t.Fatalf("evaluations=%d rules=%d", len(evaluations), len(rules))
	}
	byKey := make(map[string]Evaluation, len(evaluations))
	for _, evaluation := range evaluations {
		byKey[evaluation.Rule.DedupeKey] = evaluation
		if strings.HasSuffix(
			evaluation.Rule.DedupeKey,
			"_dependency_unavailable",
		) {
			if !evaluation.Available || evaluation.Value != 0 ||
				!evaluation.ObservedAt.Equal(now) {
				t.Fatalf("dependency evaluation=%+v", evaluation)
			}
			continue
		}
		if !evaluation.Available || !evaluation.ObservedAt.Equal(now) {
			t.Fatalf("evaluation=%+v", evaluation)
		}
	}
	expectedValues := map[string]float64{
		"filesystem_root_usage":     80,
		"filesystem_backup_usage":   81,
		"backup_local_age":          26 * 60 * 60,
		AlertKeyBackupRemoteSync: 0,
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
	evaluations, err := BuildAlertEvaluations(
		rules,
		samples,
		now,
		RemoteReplicationUnknown,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluations) != 2*len(rules)-1 {
		t.Fatalf("evaluations=%d want=%d", len(evaluations), 2*len(rules)-1)
	}
	for _, evaluation := range evaluations {
		if strings.HasPrefix(
			evaluation.Rule.DedupeKey,
			AlertKeyBackupRemoteSync,
		) {
			if evaluation.Rule.DedupeKey != AlertKeyBackupRemoteSync ||
				evaluation.Available {
				t.Fatalf("remote unknown evaluation=%+v", evaluation)
			}
			continue
		}
		if strings.HasSuffix(
			evaluation.Rule.DedupeKey,
			"_dependency_unavailable",
		) {
			if !evaluation.Available || evaluation.Value != 1 ||
				!evaluation.ObservedAt.Equal(now) {
				t.Fatalf("dependency evaluation=%+v", evaluation)
			}
			continue
		}
		if evaluation.Available {
			t.Fatalf("unexpected available evaluation=%+v", evaluation)
		}
		if !evaluation.ObservedAt.Equal(now) {
			t.Fatalf("unavailable observedAt=%s want=%s", evaluation.ObservedAt, now)
		}
	}
}

func TestBuildAlertEvaluationsSeparatesDependencyAvailabilityFromThresholds(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	rules, err := DefaultAlertRules(validSettings())
	if err != nil {
		t.Fatal(err)
	}
	evaluations, err := BuildAlertEvaluations(
		rules,
		[]Sample{alertSample(
			SampleSourceHost,
			SampleMetricFilesystemUsedPercent,
			SampleScopeRoot,
			10,
			now,
			nil,
		)},
		now,
		RemoteReplicationUnknown,
	)
	if err != nil {
		t.Fatal(err)
	}
	byKey := make(map[string]Evaluation, len(evaluations))
	for _, evaluation := range evaluations {
		byKey[evaluation.Rule.DedupeKey] = evaluation
	}
	root := byKey["filesystem_root_usage"]
	rootDependency := byKey["filesystem_root_usage_dependency_unavailable"]
	if !root.Available || root.Value != 10 ||
		!rootDependency.Available || rootDependency.Value != 0 ||
		rootDependency.Rule.Category != "storage" ||
		rootDependency.Rule.Summary != "Root filesystem metrics are unavailable" {
		t.Fatalf("root=%+v dependency=%+v", root, rootDependency)
	}
	missing := byKey["processing_queue_depth"]
	missingDependency := byKey["processing_queue_depth_dependency_unavailable"]
	if missing.Available ||
		!missingDependency.Available || missingDependency.Value != 1 ||
		missingDependency.Rule.Warning != 1 ||
		missingDependency.Rule.Category != "processing" ||
		missingDependency.Rule.Summary != "Processing queue metrics are unavailable" {
		t.Fatalf("threshold=%+v dependency=%+v", missing, missingDependency)
	}
	condition, err := classifyAlertEvaluation(missingDependency)
	if err != nil || condition != alertConditionWarning {
		t.Fatalf("dependency condition=%q err=%v", condition, err)
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
	if _, err := BuildAlertEvaluations(
		rules,
		[]Sample{sample, sample},
		now,
		RemoteReplicationUnknown,
	); err == nil {
		t.Fatal("duplicate series accepted")
	}
	sample.Unit = SampleUnitCount
	if _, err := BuildAlertEvaluations(
		rules,
		[]Sample{sample},
		now,
		RemoteReplicationUnknown,
	); err == nil {
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
	if _, err := BuildAlertEvaluations(
		rules,
		oversizedTotal,
		now,
		RemoteReplicationUnknown,
	); err == nil {
		t.Fatal("inexact AI terminal total accepted")
	}
}

func TestBuildAlertEvaluationsRemoteReplicationTriState(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	rules, err := DefaultAlertRules(validSettings())
	if err != nil {
		t.Fatal(err)
	}
	var remote Rule
	for _, rule := range rules {
		if rule.DedupeKey == AlertKeyBackupRemoteSync {
			remote = rule
			break
		}
	}
	oldOutage := alertSample(
		SampleSourceApp,
		SampleMetricBackupRemoteUp,
		SampleScopeRemote,
		0,
		now.Add(-DashboardSampleFreshFor-time.Nanosecond),
		nil,
	)
	tests := []struct {
		name            string
		state           RemoteReplicationState
		wantEvaluations int
		wantAvailable   bool
		wantValue       float64
		wantDependency  bool
	}{
		{
			name:  "unknown is neutral without dependency",
			state: RemoteReplicationUnknown, wantEvaluations: 1,
		},
		{
			name:  "disabled is explicitly healthy",
			state: RemoteReplicationDisabled, wantEvaluations: 2,
			wantAvailable: true, wantValue: 1, wantDependency: true,
		},
		{
			name:  "enabled uses current sample truth",
			state: RemoteReplicationEnabled, wantEvaluations: 2,
			wantAvailable: true, wantDependency: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			samples := []Sample{oldOutage}
			if test.state == RemoteReplicationEnabled {
				samples[0].ObservedAt = now
			}
			evaluations, err := BuildAlertEvaluations(
				[]Rule{remote},
				samples,
				now,
				test.state,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(evaluations) != test.wantEvaluations {
				t.Fatalf("evaluations=%+v", evaluations)
			}
			threshold := evaluations[0]
			if threshold.Available != test.wantAvailable ||
				threshold.Value != test.wantValue {
				t.Fatalf("threshold=%+v", threshold)
			}
			if test.wantDependency {
				dependency := evaluations[1]
				if !dependency.Available ||
					dependency.Value != 0 {
					t.Fatalf("dependency=%+v", dependency)
				}
			}
		})
	}
	if _, err := BuildAlertEvaluations(
		[]Rule{remote},
		nil,
		now,
		RemoteReplicationState(255),
	); err == nil {
		t.Fatal("invalid remote replication state accepted")
	}
}

func TestBuildAlertEvaluationsAIErrorRateCountsCancelledAsNonSuccess(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	window := now.Add(-15 * time.Minute)
	rules, err := DefaultAlertRules(validSettings())
	if err != nil {
		t.Fatal(err)
	}
	var ai Rule
	for _, rule := range rules {
		if rule.DedupeKey == "ai_error_rate" {
			ai = rule
			break
		}
	}
	for _, test := range []struct {
		name          string
		succeeded     float64
		nonSuccessful float64
		wantSamples   int
		wantValue     float64
		wantCondition alertCondition
	}{
		{
			name:      "nineteen successes and one cancelled",
			succeeded: 19, nonSuccessful: 1, wantSamples: 20,
			wantValue:     5,
			wantCondition: alertConditionHealthy,
		},
		{
			name:      "cancelled participates in the minimum",
			succeeded: 18, nonSuccessful: 1, wantSamples: 19,
			wantValue:     100.0 / 19,
			wantCondition: alertConditionNeutral,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			evaluations, err := BuildAlertEvaluations(
				[]Rule{ai},
				[]Sample{
					alertSample(
						SampleSourceApp,
						SampleMetricAIRequestsTotal,
						SampleScopeSucceeded,
						test.succeeded,
						now,
						&window,
					),
					// Failed scope intentionally aggregates failed and cancelled
					// terminal runs so it matches dashboard success-rate semantics.
					alertSample(
						SampleSourceApp,
						SampleMetricAIRequestsTotal,
						SampleScopeFailed,
						test.nonSuccessful,
						now,
						&window,
					),
				},
				now,
				RemoteReplicationUnknown,
			)
			if err != nil {
				t.Fatal(err)
			}
			evaluation := evaluations[0]
			if evaluation.SampleCount != test.wantSamples ||
				math.Abs(evaluation.Value-test.wantValue) > 1e-9 {
				t.Fatalf("evaluation=%+v", evaluation)
			}
			condition, err := classifyAlertEvaluation(evaluation)
			if err != nil || condition != test.wantCondition {
				t.Fatalf("condition=%q err=%v", condition, err)
			}
		})
	}
}

func TestBuildAlertEvaluationsRequiresMatchingExactFifteenMinuteWindows(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	rules, err := DefaultAlertRules(validSettings())
	if err != nil {
		t.Fatal(err)
	}
	exactStart := now.Add(-15 * time.Minute)
	tests := map[string][]Sample{
		"AI end mismatch": {
			alertSample(
				SampleSourceApp,
				SampleMetricAIRequestsTotal,
				SampleScopeSucceeded,
				20,
				now,
				&exactStart,
			),
			alertSample(
				SampleSourceApp,
				SampleMetricAIRequestsTotal,
				SampleScopeFailed,
				1,
				now.Add(-time.Second),
				&exactStart,
			),
		},
		"AI non exact duration": {
			alertSample(
				SampleSourceApp,
				SampleMetricAIRequestsTotal,
				SampleScopeSucceeded,
				20,
				now,
				timePointerForAlertTest(now.Add(-14*time.Minute)),
			),
			alertSample(
				SampleSourceApp,
				SampleMetricAIRequestsTotal,
				SampleScopeFailed,
				1,
				now,
				timePointerForAlertTest(now.Add(-14*time.Minute)),
			),
		},
		"processing accepts no twenty four hour window": {
			alertSample(
				SampleSourceWorker,
				SampleMetricQueueFailuresTotal,
				SampleScopeProcessing,
				5,
				now,
				timePointerForAlertTest(now.Add(-24*time.Hour)),
			),
		},
		"login requires exact window": {
			alertSample(
				SampleSourceApp,
				SampleMetricSecurityEventsTotal,
				SampleScopeLoginFailure,
				20,
				now,
				timePointerForAlertTest(now.Add(-16*time.Minute)),
			),
		},
		"authorization requires exact window": {
			alertSample(
				SampleSourceApp,
				SampleMetricSecurityEventsTotal,
				SampleScopeAuthorizationDenial,
				50,
				now,
				timePointerForAlertTest(now.Add(-14*time.Minute)),
			),
		},
	}
	for name, samples := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildAlertEvaluations(
				rules,
				samples,
				now,
				RemoteReplicationUnknown,
			); err == nil {
				t.Fatal("non-exact alert window accepted")
			}
		})
	}
}

func timePointerForAlertTest(value time.Time) *time.Time {
	return &value
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
