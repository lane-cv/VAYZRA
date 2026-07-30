package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"happylearn.local/app/internal/platform/safelog"
)

func TestRetentionSafeLogRecordsStartAndSuccessCount(t *testing.T) {
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}

	logRetentionStart(logger)
	logRetentionResult(logger, 3, nil)

	records := decodeRetentionSafeLogs(t, output.Bytes())
	if len(records) != 2 ||
		records[0]["event"] != "backup.retention.start" ||
		records[1]["event"] != "backup.retention.success" ||
		records[1]["count"] != float64(3) {
		t.Fatalf("records = %#v", records)
	}
}

func TestRetentionSafeLogUsesFixedFailureStageWithoutRawError(t *testing.T) {
	const errorSecret = "retention-error-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, errorSecret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}

	logRetentionResult(logger, 0, errors.New(errorSecret))

	records := decodeRetentionSafeLogs(t, output.Bytes())
	if len(records) != 1 ||
		records[0]["event"] != "backup.retention.failure" ||
		records[0]["stage"] != "retention" {
		t.Fatalf("records = %#v", records)
	}
	if bytes.Contains(output.Bytes(), []byte(errorSecret)) {
		t.Fatalf("raw error leaked in %q", output.Bytes())
	}
}

func decodeRetentionSafeLogs(t *testing.T, output []byte) []map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}
