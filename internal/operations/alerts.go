package operations

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrAlertNotFound             = errors.New("alert not found")
	ErrAlertAlreadyResolved      = errors.New("alert already resolved")
	ErrAlertCollectorUnavailable = errors.New("alert collector unavailable")
)

type Direction string

const (
	DirectionAbove Direction = "above"
	DirectionBelow Direction = "below"
)

type RemoteReplicationState uint8

const (
	RemoteReplicationUnknown RemoteReplicationState = iota
	RemoteReplicationDisabled
	RemoteReplicationEnabled
)

type Rule struct {
	DedupeKey      string
	Category       string
	Summary        string
	Warning        float64
	Critical       float64
	Direction      Direction
	MinimumSamples int
}

type Evaluation struct {
	Rule        Rule
	Value       float64
	ObservedAt  time.Time
	Available   bool
	SampleCount int
}

type AlertSeverity string

const (
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityCritical AlertSeverity = "critical"
)

type AlertState string

const (
	AlertStateOpen         AlertState = "open"
	AlertStateAcknowledged AlertState = "acknowledged"
	AlertStateResolved     AlertState = "resolved"
)

type Alert struct {
	ID                   uuid.UUID     `json:"id"`
	DedupeKey            string        `json:"dedupeKey"`
	Category             string        `json:"category"`
	Severity             AlertSeverity `json:"severity"`
	State                AlertState    `json:"state"`
	FirstObservedAt      time.Time     `json:"firstObservedAt"`
	LastObservedAt       time.Time     `json:"lastObservedAt"`
	AcknowledgedBy       uuid.UUID     `json:"acknowledgedBy,omitempty"`
	AcknowledgedAt       *time.Time    `json:"acknowledgedAt,omitempty"`
	ResolvedAt           *time.Time    `json:"resolvedAt,omitempty"`
	CurrentValue         float64       `json:"currentValue"`
	ThresholdValue       float64       `json:"thresholdValue"`
	Summary              string        `json:"summary"`
	TraceID              string        `json:"traceId,omitempty"`
	ConsecutiveFailures  int           `json:"consecutiveFailures"`
	ConsecutiveSuccesses int           `json:"consecutiveSuccesses"`
	Version              int64         `json:"version"`
}

type AlertTransitionKind string

const (
	AlertTransitionNone     AlertTransitionKind = "none"
	AlertTransitionOpened   AlertTransitionKind = "opened"
	AlertTransitionUpdated  AlertTransitionKind = "updated"
	AlertTransitionUpgraded AlertTransitionKind = "upgraded"
	AlertTransitionResolved AlertTransitionKind = "resolved"
)

type AlertTransition struct {
	Kind  AlertTransitionKind
	Alert *Alert
}

type AlertCursor struct {
	LastObservedAt time.Time
	ID             uuid.UUID
}

type AlertFilter struct {
	State    AlertState
	Severity AlertSeverity
	Category string
	Before   *AlertCursor
	Limit    int
}

type AlertPage struct {
	Items []Alert
	Next  *AlertCursor
}

type AlertDelivery struct {
	ID              uuid.UUID
	AlertID         uuid.UUID
	Attempt         int
	Destination     string
	Outcome         string
	HTTPStatusClass int
	ErrorCategory   string
	StartedAt       time.Time
	FinishedAt      time.Time
}

type AlertStore interface {
	ListAlerts(context.Context, AlertFilter) (AlertPage, error)
	AcknowledgeAlert(context.Context, Principal, uuid.UUID) (Alert, error)
}

type alertCondition string

const (
	alertConditionHealthy  alertCondition = "healthy"
	alertConditionWarning  alertCondition = "warning"
	alertConditionCritical alertCondition = "critical"
	alertConditionNeutral  alertCondition = "neutral"
)

var alertIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

var alertCategories = map[string]struct{}{
	"storage":    {},
	"backup":     {},
	"ai":         {},
	"processing": {},
	"security":   {},
}

var alertDependencySummaries = map[string]string{
	"filesystem_root_usage":     "Root filesystem metrics are unavailable",
	"filesystem_backup_usage":   "Backup filesystem metrics are unavailable",
	"backup_local_age":          "Local backup status is unavailable",
	"backup_remote_replication": "Remote backup status is unavailable",
	"ai_error_rate":             "AI request metrics are unavailable",
	"processing_queue_depth":    "Processing queue metrics are unavailable",
	"processing_failures":       "Processing failure metrics are unavailable",
	"login_failures":            "Login failure metrics are unavailable",
	"authorization_denials":     "Authorization denial metrics are unavailable",
}

func DefaultAlertRules(settings Settings) ([]Rule, error) {
	if err := ValidateSettings(settings); err != nil {
		return nil, err
	}
	rules := []Rule{
		{
			DedupeKey: "filesystem_root_usage", Category: "storage",
			Summary:   "Root filesystem usage is high",
			Warning:   float64(settings.DiskWarningPercent),
			Critical:  float64(settings.DiskCriticalPercent),
			Direction: DirectionAbove, MinimumSamples: 1,
		},
		{
			DedupeKey: "filesystem_backup_usage", Category: "storage",
			Summary:   "Backup filesystem usage is high",
			Warning:   float64(settings.DiskWarningPercent),
			Critical:  float64(settings.DiskCriticalPercent),
			Direction: DirectionAbove, MinimumSamples: 1,
		},
		{
			DedupeKey: "backup_local_age", Category: "backup",
			Summary: "Verified local backup is overdue",
			Warning: 25 * 60 * 60, Critical: 30 * 60 * 60,
			Direction: DirectionAbove, MinimumSamples: 1,
		},
		{
			DedupeKey: "backup_remote_replication", Category: "backup",
			Summary: "Remote backup replication is unavailable",
			Warning: 1, Critical: -1,
			Direction: DirectionBelow, MinimumSamples: 1,
		},
		{
			DedupeKey: "ai_error_rate", Category: "ai",
			Summary:   "AI error rate is high",
			Warning:   float64(settings.AIErrorWarningPercent),
			Critical:  float64(settings.AIErrorCriticalPercent),
			Direction: DirectionAbove, MinimumSamples: 20,
		},
		{
			DedupeKey: "processing_queue_depth", Category: "processing",
			Summary:   "Processing queue depth is high",
			Warning:   float64(settings.ProcessingQueueWarning),
			Critical:  float64(settings.ProcessingQueueCritical),
			Direction: DirectionAbove, MinimumSamples: 1,
		},
		{
			DedupeKey: "processing_failures", Category: "processing",
			Summary: "Processing failures are high",
			Warning: 5, Critical: 20,
			Direction: DirectionAbove, MinimumSamples: 1,
		},
		{
			DedupeKey: "login_failures", Category: "security",
			Summary: "Login failures are high",
			Warning: 20, Critical: 100,
			Direction: DirectionAbove, MinimumSamples: 1,
		},
		{
			DedupeKey: "authorization_denials", Category: "security",
			Summary: "Authorization denials are high",
			Warning: 50, Critical: 200,
			Direction: DirectionAbove, MinimumSamples: 1,
		},
	}
	for _, rule := range rules {
		if err := validateAlertRule(rule); err != nil {
			return nil, err
		}
	}
	return rules, nil
}

func BuildAlertEvaluations(
	rules []Rule,
	samples []Sample,
	now time.Time,
	remoteReplicationState RemoteReplicationState,
) ([]Evaluation, error) {
	if !validSampleTime(now) ||
		len(rules) == 0 ||
		!validRemoteReplicationState(remoteReplicationState) {
		return nil, ErrInvalid
	}
	now = now.UTC()
	indexed := make(map[sampleSeries]Sample, len(samples))
	for _, sample := range samples {
		if err := ValidateSample(sample, now); err != nil {
			return nil, err
		}
		key := sampleSeries{
			source: sample.Source,
			metric: sample.Metric,
			scope:  sample.Scope,
		}
		if _, duplicate := indexed[key]; duplicate {
			return nil, ErrInvalid
		}
		indexed[key] = sample
	}
	evaluations := make([]Evaluation, 0, len(rules))
	for _, rule := range rules {
		if err := validateAlertRule(rule); err != nil {
			return nil, err
		}
		if rule.DedupeKey == "backup_remote_replication" {
			switch remoteReplicationState {
			case RemoteReplicationUnknown:
				evaluations = append(evaluations, Evaluation{
					Rule: rule, ObservedAt: now,
					Available: false, SampleCount: 0,
				})
				continue
			case RemoteReplicationDisabled:
				evaluation := Evaluation{
					Rule: rule, Value: 1, ObservedAt: now,
					Available: true, SampleCount: 1,
				}
				evaluations = append(evaluations, evaluation)
				dependency, err := alertDependencyEvaluation(evaluation, now)
				if err != nil {
					return nil, err
				}
				evaluations = append(evaluations, dependency)
				continue
			}
		}
		evaluation, err := alertEvaluationForRule(rule, indexed, now)
		if err != nil {
			return nil, err
		}
		evaluations = append(evaluations, evaluation)
		dependency, err := alertDependencyEvaluation(evaluation, now)
		if err != nil {
			return nil, err
		}
		evaluations = append(evaluations, dependency)
	}
	return evaluations, nil
}

func validRemoteReplicationState(state RemoteReplicationState) bool {
	switch state {
	case RemoteReplicationUnknown,
		RemoteReplicationDisabled,
		RemoteReplicationEnabled:
		return true
	default:
		return false
	}
}

func alertDependencyEvaluation(
	evaluation Evaluation,
	now time.Time,
) (Evaluation, error) {
	summary, ok := alertDependencySummaries[evaluation.Rule.DedupeKey]
	if !ok {
		return Evaluation{}, ErrInvalid
	}
	rule := Rule{
		DedupeKey:      evaluation.Rule.DedupeKey + "_dependency_unavailable",
		Category:       evaluation.Rule.Category,
		Summary:        summary,
		Warning:        1,
		Critical:       2,
		Direction:      DirectionAbove,
		MinimumSamples: 1,
	}
	if err := validateAlertRule(rule); err != nil {
		return Evaluation{}, err
	}
	value := float64(0)
	if !evaluation.Available {
		value = 1
	}
	return Evaluation{
		Rule: rule, Value: value, ObservedAt: now.UTC(),
		Available: true, SampleCount: 1,
	}, nil
}

func alertEvaluationForRule(
	rule Rule,
	samples map[sampleSeries]Sample,
	now time.Time,
) (Evaluation, error) {
	evaluation := Evaluation{
		Rule: rule, ObservedAt: now, Available: false, SampleCount: 0,
	}
	if rule.DedupeKey == "ai_error_rate" {
		return aiErrorRateEvaluation(rule, samples, now)
	}
	series, ok := alertSeriesForRule(rule.DedupeKey)
	if !ok {
		return Evaluation{}, ErrInvalid
	}
	sample, ok := samples[series]
	if !ok || !alertSampleFresh(sample, now) {
		return evaluation, nil
	}
	switch rule.DedupeKey {
	case "processing_failures", "login_failures", "authorization_denials":
		if !alertSampleHasExactWindow(sample, 15*time.Minute) {
			return Evaluation{}, ErrInvalid
		}
	}
	evaluation.Value = sample.Value
	evaluation.ObservedAt = sample.ObservedAt.UTC()
	evaluation.Available = true
	evaluation.SampleCount = 1
	return evaluation, nil
}

func aiErrorRateEvaluation(
	rule Rule,
	samples map[sampleSeries]Sample,
	now time.Time,
) (Evaluation, error) {
	evaluation := Evaluation{
		Rule: rule, ObservedAt: now, Available: false, SampleCount: 0,
	}
	succeeded, succeededOK := samples[sampleSeries{
		source: SampleSourceApp, metric: SampleMetricAIRequestsTotal,
		scope: SampleScopeSucceeded,
	}]
	failed, failedOK := samples[sampleSeries{
		source: SampleSourceApp, metric: SampleMetricAIRequestsTotal,
		scope: SampleScopeFailed,
	}]
	if !succeededOK || !failedOK ||
		!alertSampleFresh(succeeded, now) ||
		!alertSampleFresh(failed, now) {
		return evaluation, nil
	}
	if succeeded.WindowStartedAt == nil || failed.WindowStartedAt == nil ||
		!succeeded.ObservedAt.Equal(failed.ObservedAt) ||
		!succeeded.WindowStartedAt.Equal(*failed.WindowStartedAt) ||
		!alertSampleHasExactWindow(succeeded, 15*time.Minute) ||
		!alertSampleHasExactWindow(failed, 15*time.Minute) {
		return Evaluation{}, ErrInvalid
	}
	total := succeeded.Value + failed.Value
	if !validAlertFloat(total) || total < 0 || total > maxExactFloatInteger ||
		math.Trunc(total) != total {
		return Evaluation{}, ErrInvalid
	}
	evaluation.Available = true
	evaluation.SampleCount = int(total)
	evaluation.ObservedAt = succeeded.ObservedAt.UTC()
	if failed.ObservedAt.After(evaluation.ObservedAt) {
		evaluation.ObservedAt = failed.ObservedAt.UTC()
	}
	if total > 0 {
		evaluation.Value = failed.Value / total * 100
	}
	if !validAlertFloat(evaluation.Value) {
		return Evaluation{}, ErrInvalid
	}
	return evaluation, nil
}

func alertSampleHasExactWindow(sample Sample, duration time.Duration) bool {
	return sample.WindowStartedAt != nil &&
		sample.ObservedAt.Sub(*sample.WindowStartedAt) == duration
}

func alertSampleFresh(sample Sample, now time.Time) bool {
	return !sample.ObservedAt.Before(now.Add(-DashboardSampleFreshFor))
}

func alertSeriesForRule(dedupeKey string) (sampleSeries, bool) {
	switch dedupeKey {
	case "filesystem_root_usage":
		return sampleSeries{
			source: SampleSourceHost, metric: SampleMetricFilesystemUsedPercent,
			scope: SampleScopeRoot,
		}, true
	case "filesystem_backup_usage":
		return sampleSeries{
			source: SampleSourceHost, metric: SampleMetricFilesystemUsedPercent,
			scope: SampleScopeBackup,
		}, true
	case "backup_local_age":
		return sampleSeries{
			source: SampleSourceApp, metric: SampleMetricBackupAgeSeconds,
			scope: SampleScopeLocal,
		}, true
	case "backup_remote_replication":
		return sampleSeries{
			source: SampleSourceApp, metric: SampleMetricBackupRemoteUp,
			scope: SampleScopeRemote,
		}, true
	case "processing_queue_depth":
		return sampleSeries{
			source: SampleSourceWorker, metric: SampleMetricQueueItems,
			scope: SampleScopeProcessing,
		}, true
	case "processing_failures":
		return sampleSeries{
			source: SampleSourceWorker, metric: SampleMetricQueueFailuresTotal,
			scope: SampleScopeProcessing,
		}, true
	case "login_failures":
		return sampleSeries{
			source: SampleSourceApp, metric: SampleMetricSecurityEventsTotal,
			scope: SampleScopeLoginFailure,
		}, true
	case "authorization_denials":
		return sampleSeries{
			source: SampleSourceApp, metric: SampleMetricSecurityEventsTotal,
			scope: SampleScopeAuthorizationDenial,
		}, true
	default:
		return sampleSeries{}, false
	}
}

func classifyAlertEvaluation(evaluation Evaluation) (alertCondition, error) {
	if err := validateAlertEvaluation(evaluation); err != nil {
		return "", err
	}
	if !evaluation.Available {
		return alertConditionNeutral, nil
	}
	if evaluation.SampleCount < evaluation.Rule.MinimumSamples {
		return alertConditionNeutral, nil
	}
	switch evaluation.Rule.Direction {
	case DirectionAbove:
		if evaluation.Value >= evaluation.Rule.Critical {
			return alertConditionCritical, nil
		}
		if evaluation.Value >= evaluation.Rule.Warning {
			return alertConditionWarning, nil
		}
	case DirectionBelow:
		if evaluation.Value < evaluation.Rule.Critical {
			return alertConditionCritical, nil
		}
		if evaluation.Value < evaluation.Rule.Warning {
			return alertConditionWarning, nil
		}
	default:
		return "", ErrInvalid
	}
	return alertConditionHealthy, nil
}

func validateAlertEvaluation(evaluation Evaluation) error {
	if err := validateAlertRule(evaluation.Rule); err != nil {
		return err
	}
	if !validAlertFloat(evaluation.Value) || evaluation.Value < 0 ||
		!validSampleTime(evaluation.ObservedAt) ||
		evaluation.SampleCount < 0 {
		return ErrInvalid
	}
	return nil
}

func validateAlertRule(rule Rule) error {
	if !alertIdentifier.MatchString(rule.DedupeKey) ||
		len(rule.DedupeKey) > 128 ||
		!alertIdentifier.MatchString(rule.Category) {
		return ErrInvalid
	}
	if _, ok := alertCategories[rule.Category]; !ok {
		return ErrInvalid
	}
	if !safeAlertSummary(rule.Summary) ||
		!validAlertFloat(rule.Warning) ||
		!validAlertFloat(rule.Critical) ||
		rule.MinimumSamples < 1 {
		return ErrInvalid
	}
	switch rule.Direction {
	case DirectionAbove:
		if rule.Critical <= rule.Warning {
			return ErrInvalid
		}
	case DirectionBelow:
		if rule.Critical >= rule.Warning {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func safeAlertSummary(summary string) bool {
	if !utf8.ValidString(summary) ||
		strings.TrimSpace(summary) != summary ||
		summary == "" ||
		utf8.RuneCountInString(summary) > 240 {
		return false
	}
	for _, value := range summary {
		if unicode.IsControl(value) {
			return false
		}
	}
	return true
}

func validAlertFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
