package httpx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"happylearn.local/app/internal/platform/safelog"
)

func TestSafeRequestLogEmitsOnlyBoundedRequestMetadata(t *testing.T) {
	const (
		querySecret         = "query-signature-secret"
		authorizationSecret = "authorization-secret"
		cookieSecret        = "cookie-secret"
		bodySecret          = "body-secret"
	)
	var output bytes.Buffer
	logger, err := safelog.New(&output, requestLogClock(
		time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.July, 30, 10, 0, 1, 500_000_000, time.UTC),
	))
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}

	handler := RequestID(SafeRequestLog(logger, requestLogClock(
		time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.July, 30, 10, 0, 1, 500_000_000, time.UTC),
	))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	})))

	request := httptest.NewRequest(
		http.MethodPost,
		"http://example.test/files/a%3Fb?signature="+querySecret,
		strings.NewReader(bodySecret),
	)
	request.Header.Set("X-Request-ID", "request-id-123")
	request.Header.Set("Authorization", "Bearer "+authorizationSecret)
	request.Header.Set("Cookie", "session="+cookieSecret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode request log %q: %v", output.Bytes(), err)
	}
	want := map[string]any{
		"timestamp":   "2026-07-30T10:00:00Z",
		"level":       "info",
		"event":       "http.request",
		"request_id":  "request-id-123",
		"method":      http.MethodPost,
		"path":        "/files/a%3Fb",
		"status":      float64(http.StatusCreated),
		"duration_ms": float64(1500),
		"bytes":       float64(5),
	}
	if !reflect.DeepEqual(record, want) {
		t.Fatalf("request log = %#v, want %#v", record, want)
	}
	for _, secret := range []string{
		querySecret,
		authorizationSecret,
		cookieSecret,
		bodySecret,
	} {
		if bytes.Contains(output.Bytes(), []byte(secret)) {
			t.Fatalf("request log leaked %q: %q", secret, output.Bytes())
		}
	}
}

func TestSafeRequestLogDefaultsUntouchedResponseToStatusOK(t *testing.T) {
	var output bytes.Buffer
	logger, err := safelog.New(&output, func() time.Time {
		return time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	handler := SafeRequestLog(logger, func() time.Time {
		return time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC)
	})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode request log %q: %v", output.Bytes(), err)
	}
	if got := record["status"]; got != float64(http.StatusOK) {
		t.Fatalf("status = %#v, want 200", got)
	}
	if got := record["bytes"]; got != float64(0) {
		t.Fatalf("bytes = %#v, want 0", got)
	}
	if _, present := record["request_id"]; present {
		t.Fatalf("unexpected empty request_id field in %#v", record)
	}
}

func TestSafeRequestLogRecordsRecoveredPanicAsInternalServerError(t *testing.T) {
	const panicSecret = "panic-secret-marker"
	var output bytes.Buffer
	logger, err := safelog.New(&output, func() time.Time {
		return time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC)
	}, panicSecret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	formatted := false
	handler := RequestID(SafeRequestLog(logger, func() time.Time {
		return time.Date(2026, time.July, 30, 10, 0, 0, 0, time.UTC)
	})(SafeRecoverer(logger)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			panic(unformattablePanic{
				formatted: &formatted,
				secret:    panicSecret,
			})
		},
	))))
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	request.Header.Set("X-Request-ID", "request-id-500")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if formatted {
		t.Fatal("SafeRecoverer formatted the recovered panic value")
	}
	if bytes.Contains(output.Bytes(), []byte(panicSecret)) {
		t.Fatalf("panic marker leaked in %q", output.Bytes())
	}
	records := decodeRequestLogRecords(t, output.Bytes())
	if len(records) != 2 {
		t.Fatalf("records = %#v, want panic and request records", records)
	}
	panicRecord, requestRecord := records[0], records[1]
	if got := panicRecord["event"]; got != "http.panic" {
		t.Fatalf("panic event = %#v, want http.panic", got)
	}
	if got := panicRecord["category"]; got != "handler" {
		t.Fatalf("panic category = %#v, want handler", got)
	}
	if got := requestRecord["status"]; got != float64(http.StatusInternalServerError) {
		t.Fatalf("logged status = %#v, want 500", got)
	}
	if got := requestRecord["request_id"]; got != "request-id-500" {
		t.Fatalf("request_id = %#v, want request-id-500", got)
	}
}

func TestSafeRecovererRepanicsAbortHandlerWithoutLogging(t *testing.T) {
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	handler := SafeRecoverer(logger)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		},
	))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		handler.ServeHTTP(response, request)
	}()

	if recovered != http.ErrAbortHandler {
		t.Fatalf("recovered = %#v, want http.ErrAbortHandler", recovered)
	}
	if output.Len() != 0 {
		t.Fatalf("abort handler produced a panic log: %q", output.Bytes())
	}
}

func TestSafeRequestLogPreservesResponseWriterCapabilities(t *testing.T) {
	for _, test := range []struct {
		name       string
		protoMajor int
		check      func(*testing.T, http.ResponseWriter, *capabilityResponseWriter)
	}{
		{
			name:       "HTTP/1",
			protoMajor: 1,
			check: func(t *testing.T, writer http.ResponseWriter, original *capabilityResponseWriter) {
				t.Helper()
				if _, ok := writer.(http.Flusher); !ok {
					t.Error("http.Flusher was not preserved")
				}
				if _, ok := writer.(http.Hijacker); !ok {
					t.Error("http.Hijacker was not preserved")
				}
				if _, ok := writer.(io.ReaderFrom); !ok {
					t.Error("io.ReaderFrom was not preserved")
				}
				unwrapper, ok := writer.(interface {
					Unwrap() http.ResponseWriter
				})
				if !ok || unwrapper.Unwrap() != original {
					t.Error("Unwrap did not return the original writer")
				}
				if _, err := writer.(io.ReaderFrom).ReadFrom(strings.NewReader("stream")); err != nil {
					t.Errorf("ReadFrom: %v", err)
				}
				writer.(http.Flusher).Flush()
				if original.body.String() != "stream" || original.readFromCalls != 1 {
					t.Errorf(
						"ReadFrom body/calls = %q/%d, want stream/1",
						original.body.String(),
						original.readFromCalls,
					)
				}
				if original.flushes != 1 {
					t.Errorf("Flush calls = %d, want 1", original.flushes)
				}
			},
		},
		{
			name:       "HTTP/2",
			protoMajor: 2,
			check: func(t *testing.T, writer http.ResponseWriter, original *capabilityResponseWriter) {
				t.Helper()
				if _, ok := writer.(http.Flusher); !ok {
					t.Error("http.Flusher was not preserved")
				}
				if _, ok := writer.(http.Pusher); !ok {
					t.Error("http.Pusher was not preserved")
				}
				unwrapper, ok := writer.(interface {
					Unwrap() http.ResponseWriter
				})
				if !ok || unwrapper.Unwrap() != original {
					t.Error("Unwrap did not return the original writer")
				}
				if err := writer.(http.Pusher).Push("/asset", nil); err != nil {
					t.Errorf("Push: %v", err)
				}
				writer.(http.Flusher).Flush()
				if !reflect.DeepEqual(original.pushes, []string{"/asset"}) {
					t.Errorf("Push targets = %v, want [/asset]", original.pushes)
				}
				if original.flushes != 1 {
					t.Errorf("Flush calls = %d, want 1", original.flushes)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger, err := safelog.New(io.Discard, time.Now)
			if err != nil {
				t.Fatalf("safelog.New: %v", err)
			}
			capabilityWriter := newCapabilityResponseWriter()
			handler := SafeRequestLog(logger, time.Now)(http.HandlerFunc(
				func(writer http.ResponseWriter, _ *http.Request) {
					test.check(t, writer, capabilityWriter)
				},
			))

			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.ProtoMajor = test.protoMajor
			handler.ServeHTTP(capabilityWriter, request)
			if capabilityWriter.status != http.StatusOK {
				t.Fatalf("status = %d, want 200", capabilityWriter.status)
			}
		})
	}
}

func TestSafeRequestLogDropsInvalidPathWithoutChangingResponse(t *testing.T) {
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	handler := SafeRequestLog(logger, time.Now)(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusAccepted)
		},
	))
	request := &http.Request{
		Method:     http.MethodGet,
		URL:        &url.URL{Path: "//attacker.example/private"},
		Header:     make(http.Header),
		ProtoMajor: 1,
	}
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.Code)
	}
	if output.Len() != 0 {
		t.Fatalf("unsafe path produced a request log: %q", output.Bytes())
	}
}

func requestLogClock(values ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		value := values[index]
		if index < len(values)-1 {
			index++
		}
		return value
	}
}

func decodeRequestLogRecords(t *testing.T, output []byte) []map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode request log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

type unformattablePanic struct {
	formatted *bool
	secret    string
}

func (value unformattablePanic) String() string {
	*value.formatted = true
	return value.secret
}

type capabilityResponseWriter struct {
	header        http.Header
	body          bytes.Buffer
	status        int
	flushes       int
	readFromCalls int
	pushes        []string
}

func newCapabilityResponseWriter() *capabilityResponseWriter {
	return &capabilityResponseWriter{header: make(http.Header)}
}

func (writer *capabilityResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *capabilityResponseWriter) WriteHeader(status int) {
	writer.status = status
}

func (writer *capabilityResponseWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.body.Write(value)
}

func (writer *capabilityResponseWriter) Flush() {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	writer.flushes++
}

func (writer *capabilityResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("not connected")
}

func (writer *capabilityResponseWriter) ReadFrom(reader io.Reader) (int64, error) {
	writer.readFromCalls++
	return io.Copy(&writer.body, reader)
}

func (writer *capabilityResponseWriter) Push(target string, _ *http.PushOptions) error {
	writer.pushes = append(writer.pushes, target)
	return nil
}

var (
	_ http.ResponseWriter = (*capabilityResponseWriter)(nil)
	_ http.Flusher        = (*capabilityResponseWriter)(nil)
	_ http.Hijacker       = (*capabilityResponseWriter)(nil)
	_ io.ReaderFrom       = (*capabilityResponseWriter)(nil)
	_ http.Pusher         = (*capabilityResponseWriter)(nil)
)
