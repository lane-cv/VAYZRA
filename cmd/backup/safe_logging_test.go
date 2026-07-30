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

func TestRunProgramWithLogEmitsFixedStartAndSuccess(t *testing.T) {
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	err = runProgramWithLog(
		context.Background(),
		[]string{"restore-http-probe"},
		func(string) string { return "" },
		programFactories{
			runRestoreHTTPProbe: func(context.Context) error {
				return nil
			},
		},
		logger,
	)
	if err != nil {
		t.Fatalf("runProgramWithLog: %v", err)
	}

	records := decodeBackupSafeLogs(t, output.Bytes())
	if len(records) != 2 ||
		records[0]["event"] != "backup.start" ||
		records[1]["event"] != "backup.success" ||
		records[0]["stage"] != "restore_http_probe" ||
		records[1]["stage"] != "restore_http_probe" {
		t.Fatalf("records = %#v", records)
	}
}

func TestRunProgramWithLogEmitsFixedFailureWithoutRawError(t *testing.T) {
	const errorSecret = "backup-error-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, errorSecret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	err = runProgramWithLog(
		context.Background(),
		[]string{"restore-http-probe"},
		func(string) string { return "" },
		programFactories{
			runRestoreHTTPProbe: func(context.Context) error {
				return errors.New(errorSecret)
			},
		},
		logger,
	)
	if err == nil {
		t.Fatal("runProgramWithLog succeeded")
	}

	records := decodeBackupSafeLogs(t, output.Bytes())
	if len(records) != 2 ||
		records[1]["event"] != "backup.failure" ||
		records[1]["stage"] != "restore_http_probe" {
		t.Fatalf("records = %#v", records)
	}
	if bytes.Contains(output.Bytes(), []byte(errorSecret)) {
		t.Fatalf("raw error leaked in %q", output.Bytes())
	}
}

func TestProductionRestoreCheckLogsOnlyFixedFailureCategory(t *testing.T) {
	const secret = "restore-check-diagnostic-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, secret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	factories := productionProgramFactoriesWithLog(logger)
	err = factories.runRestoreCheck(
		context.Background(),
		restoreCheckInput{},
		func(string) string { return secret },
	)
	if !errors.Is(err, errWorkflowUnavailable) {
		t.Fatalf("restore check error=%v", err)
	}

	records := decodeBackupSafeLogs(t, output.Bytes())
	if len(records) != 1 ||
		records[0]["event"] != "backup.restore_check_failure" ||
		records[0]["category"] != "input" {
		t.Fatalf("records = %#v", records)
	}
	if bytes.Contains(output.Bytes(), []byte(secret)) {
		t.Fatalf("restore check diagnostic leaked secret in %q", output.Bytes())
	}
}

func decodeBackupSafeLogs(t *testing.T, output []byte) []map[string]any {
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
