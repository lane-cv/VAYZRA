package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"happylearn.local/app/internal/platform/safelog"
)

func TestProductionRunnerLogsAreNonNilSafeCallbacks(t *testing.T) {
	const secretCategory = "runner-secret-category"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, secretCategory)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	logs := newProductionRunnerLogs(logger)
	callbacks := []struct {
		event    string
		callback func(string)
	}{
		{"upload.cleanup", logs.uploadCleanup},
		{"notifications.outbox", logs.outbox},
		{"ai.runner", logs.ai},
		{"operations.alert", logs.alert},
		{"operations.webhook", logs.webhook},
		{"operations.retention", logs.retention},
		{"redis.login", logs.loginLimiter},
		{"redis.progress", logs.progressLimiter},
		{"redis.search", logs.searchLimiter},
		{"redis.provider_test", logs.providerTestLimiter},
	}
	for _, callback := range callbacks {
		t.Run(callback.event, func(t *testing.T) {
			if callback.callback == nil {
				t.Fatal("production callback is nil")
			}
			output.Reset()
			callback.callback(secretCategory)
			if bytes.Contains(output.Bytes(), []byte(secretCategory)) {
				t.Fatalf("callback leaked configured marker: %q", output.Bytes())
			}
			records := decodeServerSafeLogs(t, output.Bytes())
			if len(records) != 1 ||
				records[0]["event"] != callback.event ||
				records[0]["category"] != "hidden" {
				t.Fatalf("records = %#v", records)
			}
		})
	}
}

func TestServerConstructorUsesSafeRuntimeErrorLog(t *testing.T) {
	const secret = "server-runtime-error-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, secret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	server := newServerWithLog(":0", http.NotFoundHandler(), logger, "public")
	if server.ErrorLog == nil {
		t.Fatal("server ErrorLog is nil")
	}

	server.ErrorLog.Print(secret)

	if bytes.Contains(output.Bytes(), []byte(secret)) {
		t.Fatalf("server runtime log leaked marker: %q", output.Bytes())
	}
	records := decodeServerSafeLogs(t, output.Bytes())
	if len(records) != 1 ||
		records[0]["event"] != "http.server.error" ||
		records[0]["service"] != "public" {
		t.Fatalf("records = %#v", records)
	}
}

func TestServerLifecycleLogsOrderedFixedEvents(t *testing.T) {
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	signals, cancel := context.WithCancel(context.Background())
	cancel()
	server := newFakeServerLifecycle()

	if err := runServerLifecyclesWithLog(
		signals,
		[]serverLifecycle{server},
		func() {},
		logger,
	); err != nil {
		t.Fatalf("runServerLifecyclesWithLog: %v", err)
	}

	records := decodeServerSafeLogs(t, output.Bytes())
	var events []string
	for _, record := range records {
		events = append(events, record["event"].(string))
	}
	if want := []string{"server.started", "server.shutdown", "server.stopped"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if records[1]["stage"] != "start" {
		t.Fatalf("shutdown record = %#v", records[1])
	}
}

func TestServerLifecycleLogsStoppedAfterRecoverableFailure(t *testing.T) {
	const secret = "server-listen-error-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, secret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	server := newFakeServerLifecycle()
	server.listen <- errors.New(secret)

	err = runServerLifecyclesWithLog(
		context.Background(),
		[]serverLifecycle{server},
		func() {},
		logger,
	)
	if err == nil || err.Error() != "server start" {
		t.Fatalf("err = %v, want server start", err)
	}
	if bytes.Contains(output.Bytes(), []byte(secret)) {
		t.Fatalf("lifecycle log leaked marker: %q", output.Bytes())
	}

	records := decodeServerSafeLogs(t, output.Bytes())
	var events []string
	for _, record := range records {
		events = append(events, record["event"].(string))
	}
	want := []string{"server.started", "server.shutdown", "server.error", "server.stopped"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func decodeServerSafeLogs(t *testing.T, output []byte) []map[string]any {
	t.Helper()
	if len(bytes.TrimSpace(output)) == 0 {
		return nil
	}
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
