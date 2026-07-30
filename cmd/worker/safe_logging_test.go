package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"happylearn.local/app/internal/platform/safelog"
	"happylearn.local/app/internal/processing"
)

func TestBuildWorkerWithLogInjectsSafeProcessingCallback(t *testing.T) {
	const secretCategory = "worker-secret-category"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, secretCategory)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	worker, err := buildWorkerWithLog(
		&leaseCountingStore{},
		&workerClaimGate{allowed: true},
		"safe-worker",
		func() (processing.Processor, error) {
			return processorStub{}, nil
		},
		logger,
	)
	if err != nil {
		t.Fatalf("buildWorkerWithLog: %v", err)
	}
	if worker.LogCategory == nil {
		t.Fatal("production processing callback is nil")
	}

	worker.LogCategory(secretCategory)

	if bytes.Contains(output.Bytes(), []byte(secretCategory)) {
		t.Fatalf("worker callback leaked marker: %q", output.Bytes())
	}
	records := decodeWorkerSafeLogs(t, output.Bytes())
	if len(records) != 1 ||
		records[0]["event"] != "processing.worker" ||
		records[0]["category"] != "hidden" {
		t.Fatalf("records = %#v", records)
	}
}

func TestWorkerHealthServerUsesSafeRuntimeErrorLog(t *testing.T) {
	const secret = "worker-health-runtime-error-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, secret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	server := newWorkerHealthServer(http.NotFoundHandler(), logger)
	if server.ErrorLog == nil {
		t.Fatal("worker health ErrorLog is nil")
	}

	server.ErrorLog.Print(secret)

	if bytes.Contains(output.Bytes(), []byte(secret)) {
		t.Fatalf("worker health runtime log leaked marker: %q", output.Bytes())
	}
	records := decodeWorkerSafeLogs(t, output.Bytes())
	if len(records) != 1 ||
		records[0]["event"] != "http.server.error" ||
		records[0]["service"] != "worker-health" {
		t.Fatalf("records = %#v", records)
	}
}

func TestWorkerLifecycleLogsOrderedFixedEvents(t *testing.T) {
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	signals, cancelSignal := context.WithCancel(workerCtx)
	cancelSignal()
	workerDone := make(chan error, 1)
	workerDone <- nil
	events := &workerLifecycleEvents{}

	safeToClean, err := coordinateWorkerRuntimeWithLog(
		signals,
		cancelWorker,
		&fakeWorkerHealthLifecycle{events: events},
		workerDone,
		make(chan error),
		time.Second,
		logger,
	)
	if err != nil || !safeToClean {
		t.Fatalf("safeToClean=%t err=%v", safeToClean, err)
	}

	records := decodeWorkerSafeLogs(t, output.Bytes())
	var logEvents []string
	for _, record := range records {
		logEvents = append(logEvents, record["event"].(string))
	}
	if want := []string{"worker.started", "worker.shutdown", "worker.stopped"}; !reflect.DeepEqual(logEvents, want) {
		t.Fatalf("events = %v, want %v", logEvents, want)
	}
}

func TestWorkerLifecycleLogsStoppedAfterRecoverableFailure(t *testing.T) {
	const secret = "worker-runtime-error-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, secret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	workerDone := make(chan error, 1)
	workerDone <- errors.New(secret)

	safeToClean, err := coordinateWorkerRuntimeWithLog(
		workerCtx,
		cancelWorker,
		&fakeWorkerHealthLifecycle{events: &workerLifecycleEvents{}},
		workerDone,
		make(chan error),
		time.Second,
		logger,
	)
	if err == nil || err.Error() != "worker lifecycle" || !safeToClean {
		t.Fatalf("safeToClean=%t err=%v, want true and worker lifecycle", safeToClean, err)
	}
	if bytes.Contains(output.Bytes(), []byte(secret)) {
		t.Fatalf("lifecycle log leaked marker: %q", output.Bytes())
	}

	records := decodeWorkerSafeLogs(t, output.Bytes())
	var events []string
	for _, record := range records {
		events = append(events, record["event"].(string))
	}
	want := []string{"worker.started", "worker.shutdown", "worker.error", "worker.stopped"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestWorkerHealthHandlerUsesSafeRequestInstrumentation(t *testing.T) {
	const (
		querySecret = "worker-health-query-secret"
		authSecret  = "worker-health-auth-secret"
		bodySecret  = "worker-health-body-secret"
	)
	var output bytes.Buffer
	logger, err := safelog.New(
		&output,
		time.Now,
		querySecret,
		authSecret,
		bodySecret,
	)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	handler := productionWorkerHealthHandler(
		func(context.Context) error { return nil },
		logger,
	)
	request := httptest.NewRequest(
		http.MethodGet,
		"/live?signature="+querySecret,
		strings.NewReader(bodySecret),
	)
	request.Header.Set("Authorization", "Bearer "+authSecret)
	request.Header.Set("X-Request-ID", "request-id-worker-health")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	records := decodeWorkerSafeLogs(t, output.Bytes())
	if len(records) != 1 ||
		records[0]["event"] != "http.request" ||
		records[0]["path"] != "/live" ||
		records[0]["status"] != float64(http.StatusOK) {
		t.Fatalf("records = %#v", records)
	}
	for _, secret := range []string{querySecret, authSecret, bodySecret} {
		if bytes.Contains(output.Bytes(), []byte(secret)) {
			t.Fatalf("worker health log leaked %q: %q", secret, output.Bytes())
		}
	}
}

func decodeWorkerSafeLogs(t *testing.T, output []byte) []map[string]any {
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
