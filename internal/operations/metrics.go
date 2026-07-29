package operations

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

func RenderMetrics(now time.Time, samples []Sample) ([]byte, error) {
	if !validSampleTime(now) {
		return nil, ErrInvalid
	}
	lines := make([]string, 0, len(samples))
	seen := make(map[string]struct{}, len(samples))
	for _, sample := range samples {
		if err := ValidateSample(sample, now); err != nil {
			return nil, ErrInvalid
		}
		name := "happylearn_" + string(sample.Metric)
		labelName, labelled := metricLabelName(sample.Metric)
		series := name
		if labelled {
			series += `{` + labelName + `="` + string(sample.Scope) + `"}`
		}
		if _, duplicate := seen[series]; duplicate {
			return nil, ErrInvalid
		}
		seen[series] = struct{}{}
		lines = append(
			lines,
			series+" "+strconv.FormatFloat(sample.Value, 'f', -1, 64),
		)
	}
	sort.Strings(lines)
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func metricLabelName(metric SampleMetric) (string, bool) {
	switch metric {
	case SampleMetricServiceUp,
		SampleMetricServiceReady,
		SampleMetricServiceLatencyMilliseconds,
		SampleMetricPostgresPoolInUse,
		SampleMetricPostgresFailuresTotal,
		SampleMetricPostgresMigrationVersion,
		SampleMetricRedisLimiterUp,
		SampleMetricObjectCapacityBytes,
		SampleMetricObjectUsedBytes,
		SampleMetricObjectFailuresTotal,
		SampleMetricHostServiceCPUPercent,
		SampleMetricHostServiceMemoryBytes,
		SampleMetricHostServiceMemoryLimitBytes,
		SampleMetricHostServiceRestarts:
		return "service", true
	case SampleMetricQueueItems, SampleMetricQueueFailuresTotal:
		return "queue", true
	case SampleMetricBackupAgeSeconds,
		SampleMetricBackupRemoteUp,
		SampleMetricRestoreAgeSeconds:
		return "repository", true
	case SampleMetricAIRuns:
		return "state", true
	case SampleMetricAIRequestsTotal:
		return "status", true
	case SampleMetricAILatencyMilliseconds:
		return "phase", true
	case SampleMetricAITokensTotal:
		return "direction", true
	case SampleMetricSecurityEventsTotal:
		return "category", true
	case SampleMetricFilesystemUsedPercent:
		return "filesystem", true
	case SampleMetricAICostMicroUSD:
		return "", false
	default:
		return "", false
	}
}
