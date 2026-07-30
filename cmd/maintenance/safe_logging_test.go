package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"happylearn.local/app/internal/platform/safelog"
)

func TestRunMaintenanceWithLogEmitsFixedConfigurationFailure(t *testing.T) {
	const invalidEnvironment = "maintenance-error-secret"
	var output bytes.Buffer
	err := runMaintenanceWithLog(
		context.Background(),
		[]string{"cleanup-files"},
		func(name string) string {
			if name == "HAPPYLEARN_ENV" {
				return invalidEnvironment
			}
			return ""
		},
		&output,
		func() time.Time {
			return time.Date(2026, time.July, 30, 15, 0, 0, 0, time.UTC)
		},
	)
	if err == nil {
		t.Fatal("runMaintenanceWithLog succeeded")
	}

	records := decodeMaintenanceSafeLogs(t, output.Bytes())
	if len(records) != 2 ||
		records[0]["event"] != "maintenance.configuration" ||
		records[0]["stage"] != "start" ||
		records[1]["event"] != "maintenance.failure" ||
		records[1]["stage"] != "configuration" {
		t.Fatalf("records = %#v", records)
	}
	if bytes.Contains(output.Bytes(), []byte(invalidEnvironment)) {
		t.Fatalf("configuration detail leaked in %q", output.Bytes())
	}
}

func TestLogMaintenanceResultUsesFixedSuccessAndFailureEvents(t *testing.T) {
	const errorSecret = "maintenance-runtime-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, errorSecret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}

	logMaintenanceResult(logger, "cleanup_files", nil)
	logMaintenanceResult(logger, "cleanup_files", errors.New(errorSecret))

	records := decodeMaintenanceSafeLogs(t, output.Bytes())
	if len(records) != 2 ||
		records[0]["event"] != "maintenance.success" ||
		records[1]["event"] != "maintenance.failure" ||
		records[1]["stage"] != "cleanup_files" {
		t.Fatalf("records = %#v", records)
	}
	if bytes.Contains(output.Bytes(), []byte(errorSecret)) {
		t.Fatalf("runtime error leaked in %q", output.Bytes())
	}
}

func decodeMaintenanceSafeLogs(t *testing.T, output []byte) []map[string]any {
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
