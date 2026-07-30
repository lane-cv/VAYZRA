package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"happylearn.local/app/internal/platform/safelog"
)

func TestApplicationSafeLoggingMiddlewareRecordsRequests(t *testing.T) {
	var output bytes.Buffer
	logger, err := safelog.New(&output, func() time.Time {
		return time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	handler := New(Dependencies{Logger: logger})
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/health/live?signature=query-secret-marker",
		nil,
	)
	request.Header.Set("X-Request-ID", "request-id-live")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	records := decodeSafeLogRecords(t, output.Bytes())
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one request record", records)
	}
	if got := records[0]["event"]; got != "http.request" {
		t.Fatalf("event = %#v, want http.request", got)
	}
	if got := records[0]["path"]; got != "/api/v1/health/live" {
		t.Fatalf("path = %#v, want query-free path", got)
	}
	if bytes.Contains(output.Bytes(), []byte("query-secret-marker")) {
		t.Fatalf("query leaked in %q", output.Bytes())
	}
}

func TestApplicationSafeRecovererNeverFormatsPanicValue(t *testing.T) {
	const panicSecret = "application-panic-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, func() time.Time {
		return time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	}, panicSecret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	formatted := false
	handler := New(Dependencies{
		Logger: logger,
		Ready: func(context.Context) error {
			panic(appUnformattablePanic{
				formatted: &formatted,
				secret:    panicSecret,
			})
		},
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	request.Header.Set("X-Request-ID", "request-id-panic")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if formatted {
		t.Fatal("application recovery formatted the panic value")
	}
	if bytes.Contains(output.Bytes(), []byte(panicSecret)) {
		t.Fatalf("panic marker leaked in %q", output.Bytes())
	}
	records := decodeSafeLogRecords(t, output.Bytes())
	if len(records) != 2 ||
		records[0]["event"] != "http.panic" ||
		records[1]["event"] != "http.request" {
		t.Fatalf("records = %#v, want panic then request", records)
	}
}

func decodeSafeLogRecords(t *testing.T, output []byte) []map[string]any {
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

type appUnformattablePanic struct {
	formatted *bool
	secret    string
}

func (value appUnformattablePanic) String() string {
	*value.formatted = true
	return value.secret
}
