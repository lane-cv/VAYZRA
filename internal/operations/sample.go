package operations

import (
	"context"
	"math"
	"time"
)

const (
	MaxSampleInsertBatch    = 100
	MaxSampleReadLimit      = 100
	MaxSampleRetentionBatch = 1000
	MaxSampleFreshFor       = 24 * time.Hour

	maxSampleFutureSkew  = 2 * time.Minute
	maxSampleWindow      = 24 * time.Hour
	maxExactFloatInteger = float64(1<<53 - 1)
)

type SampleSource string

const (
	SampleSourceApp         SampleSource = "app"
	SampleSourcePostgres    SampleSource = "postgres"
	SampleSourceRedis       SampleSource = "redis"
	SampleSourceObjectStore SampleSource = "object_store"
	SampleSourceWorker      SampleSource = "worker"
	SampleSourceHost        SampleSource = "host"
)

type SampleMetric string

const (
	SampleMetricServiceUp                   SampleMetric = "service_up"
	SampleMetricServiceLatencyMilliseconds  SampleMetric = "service_latency_milliseconds"
	SampleMetricPostgresPoolInUse           SampleMetric = "postgres_pool_in_use"
	SampleMetricPostgresFailuresTotal       SampleMetric = "postgres_transaction_failures_total"
	SampleMetricPostgresMigrationVersion    SampleMetric = "postgres_migration_version"
	SampleMetricRedisLimiterUp              SampleMetric = "redis_limiter_up"
	SampleMetricObjectCapacityBytes         SampleMetric = "object_capacity_bytes"
	SampleMetricObjectUsedBytes             SampleMetric = "object_used_bytes"
	SampleMetricObjectFailuresTotal         SampleMetric = "object_operation_failures_total"
	SampleMetricQueueItems                  SampleMetric = "queue_items"
	SampleMetricQueueFailuresTotal          SampleMetric = "queue_failures_total"
	SampleMetricBackupAgeSeconds            SampleMetric = "backup_age_seconds"
	SampleMetricRestoreAgeSeconds           SampleMetric = "restore_age_seconds"
	SampleMetricAIRequestsTotal             SampleMetric = "ai_requests_total"
	SampleMetricAILatencyMilliseconds       SampleMetric = "ai_latency_milliseconds"
	SampleMetricAITokensTotal               SampleMetric = "ai_tokens_total"
	SampleMetricAICostMicroUSD              SampleMetric = "ai_cost_micro_usd"
	SampleMetricSecurityEventsTotal         SampleMetric = "security_events_total"
	SampleMetricFilesystemUsedPercent       SampleMetric = "filesystem_used_percent"
	SampleMetricHostServiceCPUPercent       SampleMetric = "host_service_cpu_percent"
	SampleMetricHostServiceMemoryBytes      SampleMetric = "host_service_memory_bytes"
	SampleMetricHostServiceMemoryLimitBytes SampleMetric = "host_service_memory_limit_bytes"
	SampleMetricHostServiceRestarts         SampleMetric = "host_service_restarts"
)

type SampleScope string

const (
	SampleScopeApp                 SampleScope = "app"
	SampleScopeCaddy               SampleScope = "caddy"
	SampleScopePostgres            SampleScope = "postgres"
	SampleScopeRedis               SampleScope = "redis"
	SampleScopeObjectStore         SampleScope = "object_store"
	SampleScopeWorker              SampleScope = "worker"
	SampleScopeProcessing          SampleScope = "processing"
	SampleScopeAI                  SampleScope = "ai"
	SampleScopeOutbox              SampleScope = "outbox"
	SampleScopeLocal               SampleScope = "local"
	SampleScopeRemote              SampleScope = "remote"
	SampleScopeRoot                SampleScope = "root"
	SampleScopeBackup              SampleScope = "backup"
	SampleScopeSucceeded           SampleScope = "succeeded"
	SampleScopeFailed              SampleScope = "failed"
	SampleScopeFirstByte           SampleScope = "first_byte"
	SampleScopeTotal               SampleScope = "total"
	SampleScopeInput               SampleScope = "input"
	SampleScopeOutput              SampleScope = "output"
	SampleScopeLoginFailure        SampleScope = "login_failure"
	SampleScopeAuthorizationDenial SampleScope = "authorization_denial"
	SampleScopeFileAccessDenial    SampleScope = "file_access_denial"
)

type SampleUnit string

const (
	SampleUnitBoolean      SampleUnit = "boolean"
	SampleUnitCount        SampleUnit = "count"
	SampleUnitMilliseconds SampleUnit = "milliseconds"
	SampleUnitVersion      SampleUnit = "version"
	SampleUnitBytes        SampleUnit = "bytes"
	SampleUnitSeconds      SampleUnit = "seconds"
	SampleUnitPercent      SampleUnit = "percent"
	SampleUnitMicroUSD     SampleUnit = "micro_usd"
)

type Sample struct {
	Source          SampleSource `json:"source"`
	Metric          SampleMetric `json:"metric"`
	Scope           SampleScope  `json:"scope"`
	Value           float64      `json:"value"`
	Unit            SampleUnit   `json:"unit"`
	ObservedAt      time.Time    `json:"observedAt"`
	WindowStartedAt *time.Time   `json:"windowStartedAt,omitempty"`
}

type SampleFreshness string

const (
	SampleFreshnessFresh SampleFreshness = "fresh"
	SampleFreshnessStale SampleFreshness = "stale"
	SampleFreshnessEmpty SampleFreshness = "empty"
)

type SampleReadRequest struct {
	Source   SampleSource
	Metric   SampleMetric
	Scope    SampleScope
	Limit    int
	Now      time.Time
	FreshFor time.Duration
}

type SampleReadResult struct {
	Source    SampleSource
	Metric    SampleMetric
	Scope     SampleScope
	Unit      SampleUnit
	Freshness SampleFreshness
	Samples   []Sample
}

type Collector interface {
	Collect(context.Context, time.Time) ([]Sample, error)
}

type SampleStore interface {
	InsertSamples(context.Context, time.Time, []Sample) error
	ReadLatestSamples(context.Context, SampleReadRequest) (SampleReadResult, error)
	DeleteExpiredSamples(context.Context, time.Time, int) (int64, error)
}

type sampleWindowPolicy uint8

const (
	sampleWindowForbidden sampleWindowPolicy = iota
	sampleWindowRequired
)

type sampleSeries struct {
	source SampleSource
	metric SampleMetric
	scope  SampleScope
}

type sampleRule struct {
	unit         SampleUnit
	windowPolicy sampleWindowPolicy
}

var sampleRules = buildSampleRules()

func ValidateSample(sample Sample, now time.Time) error {
	if !validSampleTime(now) ||
		!validSampleTime(sample.ObservedAt) ||
		sample.ObservedAt.After(now.Add(maxSampleFutureSkew)) {
		return ErrInvalid
	}
	rule, ok := sampleRuleFor(sample.Source, sample.Metric, sample.Scope)
	if !ok || sample.Unit != rule.unit || !validSampleValue(sample.Value, sample.Unit) {
		return ErrInvalid
	}
	switch rule.windowPolicy {
	case sampleWindowForbidden:
		if sample.WindowStartedAt != nil {
			return ErrInvalid
		}
	case sampleWindowRequired:
		if sample.WindowStartedAt == nil ||
			!validSampleTime(*sample.WindowStartedAt) ||
			!sample.WindowStartedAt.Before(sample.ObservedAt) ||
			sample.ObservedAt.Sub(*sample.WindowStartedAt) > maxSampleWindow {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validSampleValue(value float64, unit SampleUnit) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) ||
		value < 0 || (value == 0 && math.Signbit(value)) ||
		value > maxExactFloatInteger {
		return false
	}
	switch unit {
	case SampleUnitBoolean:
		return value == 0 || value == 1
	case SampleUnitPercent:
		return value <= 100
	case SampleUnitCount, SampleUnitVersion, SampleUnitBytes, SampleUnitMicroUSD:
		return math.Trunc(value) == value
	case SampleUnitMilliseconds, SampleUnitSeconds:
		return true
	default:
		return false
	}
}

func validSampleTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999
}

func sampleRuleFor(
	source SampleSource,
	metric SampleMetric,
	scope SampleScope,
) (sampleRule, bool) {
	rule, ok := sampleRules[sampleSeries{source: source, metric: metric, scope: scope}]
	return rule, ok
}

func buildSampleRules() map[sampleSeries]sampleRule {
	rules := make(map[sampleSeries]sampleRule)
	add := func(
		source SampleSource,
		metric SampleMetric,
		scope SampleScope,
		unit SampleUnit,
		windowPolicy sampleWindowPolicy,
	) {
		rules[sampleSeries{source: source, metric: metric, scope: scope}] = sampleRule{
			unit:         unit,
			windowPolicy: windowPolicy,
		}
	}

	add(SampleSourceApp, SampleMetricServiceUp, SampleScopeApp, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourcePostgres, SampleMetricServiceUp, SampleScopePostgres, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourceRedis, SampleMetricServiceUp, SampleScopeRedis, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourceObjectStore, SampleMetricServiceUp, SampleScopeObjectStore, SampleUnitBoolean, sampleWindowForbidden)
	add(SampleSourceWorker, SampleMetricServiceUp, SampleScopeWorker, SampleUnitBoolean, sampleWindowForbidden)
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

	for _, scope := range []SampleScope{SampleScopeProcessing, SampleScopeAI, SampleScopeOutbox} {
		add(SampleSourceWorker, SampleMetricQueueItems, scope, SampleUnitCount, sampleWindowForbidden)
		add(SampleSourceWorker, SampleMetricQueueFailuresTotal, scope, SampleUnitCount, sampleWindowRequired)
	}
	for _, scope := range []SampleScope{SampleScopeLocal, SampleScopeRemote} {
		add(SampleSourceApp, SampleMetricBackupAgeSeconds, scope, SampleUnitSeconds, sampleWindowForbidden)
	}
	add(SampleSourceApp, SampleMetricRestoreAgeSeconds, SampleScopeLocal, SampleUnitSeconds, sampleWindowForbidden)

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
	return rules
}
