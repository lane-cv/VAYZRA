package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testAuthorization = "Bearer e2e-provider-key"

func TestSuccessStreamsExactProtocolEventsAcrossWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		body string
		want string
	}{
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: `{"messages":[{"role":"user","content":"[case:success]"}]}`,
			want: "data: {\"choices\":[{\"delta\":{\"content\":\"Fixture \"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"answer: $x=2$.\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":5}}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"[case:success]"}]}]}`,
			want: "event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Fixture \"}\n\n" +
				"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer: $x=2$.\"}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":8,\"output_tokens\":5}}}\n\n",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			writer := newRecordingResponseWriter()
			request := authenticatedRequest(t, context.Background(), tt.path, tt.body)

			newProviderHandler(providerOptions{}).ServeHTTP(writer, request)

			if writer.statusCode() != http.StatusOK {
				t.Fatalf("status=%d body=%q", writer.statusCode(), writer.body.String())
			}
			if got := writer.body.String(); got != tt.want {
				t.Fatalf("stream mismatch\n got: %q\nwant: %q", got, tt.want)
			}
			if writer.flushes < 3 {
				t.Fatalf("flushes=%d, want at least 3", writer.flushes)
			}
			if !writer.hasSplitSSEFrame() {
				t.Fatal("no SSE frame was split across response writes")
			}
			if !writer.hasFlushBetweenSplitWrites() {
				t.Fatal("split SSE frame was buffered instead of flushed between writes")
			}
			if got := writer.Header().Get("Content-Type"); got != "text/event-stream" {
				t.Fatalf("content-type=%q", got)
			}
		})
	}
}

func TestTerminalVariantsAndFaultMarkers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		path       string
		marker     string
		wantStatus int
		want       []string
		notWant    []string
	}{
		{"chat no usage", "/v1/chat/completions", "[case:no-usage]", 200, []string{`"finish_reason":"stop"`, "data: [DONE]"}, []string{`"usage"`}},
		{"responses no usage", "/v1/responses", "[case:no-usage]", 200, []string{`"status":"completed"`}, []string{`"usage"`}},
		{"chat over reservation", "/v1/chat/completions", "[case:usage-over-reservation]", 200, []string{`"prompt_tokens":8000`, `"completion_tokens":5000`}, nil},
		{"responses over reservation", "/v1/responses", "[case:usage-over-reservation]", 200, []string{`"input_tokens":8000`, `"output_tokens":5000`}, nil},
		{"chat disconnect", "/v1/chat/completions", "[case:disconnect-after-delta]", 200, []string{`"content":"Fixture "`}, []string{"[DONE]", "finish_reason"}},
		{"responses disconnect", "/v1/responses", "[case:disconnect-after-delta]", 200, []string{`response.output_text.delta`, `"delta":"Fixture "`}, []string{"response.completed"}},
		{"chat malformed", "/v1/chat/completions", "[case:malformed-event]", 200, []string{"data: not-json"}, []string{"[DONE]"}},
		{"responses malformed", "/v1/responses", "[case:malformed-event]", 200, []string{"event: response.output_text.delta", "data: not-json"}, []string{"response.completed"}},
		{"rate limited", "/v1/chat/completions", "[case:429]", 429, []string{`"error":"fixture_rate_limited"`}, nil},
		{"upstream failure", "/v1/responses", "[case:500]", 500, []string{`"error":"fixture_upstream_failure"`}, nil},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			request := authenticatedRequest(t, context.Background(), tt.path, `{"prompt":"`+tt.marker+`"}`)
			newProviderHandler(providerOptions{}).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
			}
			for _, fragment := range tt.want {
				if !strings.Contains(recorder.Body.String(), fragment) {
					t.Fatalf("body %q does not contain %q", recorder.Body.String(), fragment)
				}
			}
			for _, fragment := range tt.notWant {
				if strings.Contains(recorder.Body.String(), fragment) {
					t.Fatalf("body %q unexpectedly contains %q", recorder.Body.String(), fragment)
				}
			}
		})
	}
}

func TestSlowCasesStopWhenRequestContextIsCancelled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		marker         string
		wantFirstDelta bool
	}{
		{"[case:slow-first-byte]", false},
		{"[case:idle-timeout]", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.marker, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			request := authenticatedRequest(t, ctx, "/v1/chat/completions", `{"prompt":"`+tt.marker+`"}`)
			writer := newRecordingResponseWriter()
			started := make(chan struct{})
			done := make(chan struct{})
			go func() {
				close(started)
				newProviderHandler(providerOptions{slowDelay: time.Hour}).ServeHTTP(writer, request)
				close(done)
			}()
			<-started
			if tt.wantFirstDelta {
				waitFor(t, time.Second, func() bool { return writer.flushCount() > 0 })
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("handler did not stop after context cancellation")
			}
			got := writer.bodyString()
			if tt.wantFirstDelta && !strings.Contains(got, `"content":"Fixture "`) {
				t.Fatalf("body=%q, want first delta", got)
			}
			if strings.Contains(got, "[DONE]") {
				t.Fatalf("body=%q unexpectedly completed", got)
			}
		})
	}
}

func TestRequestBodyIsBoundedAndAuthorizationIsRequired(t *testing.T) {
	t.Parallel()
	handler := newProviderHandler(providerOptions{})

	oversized := authenticatedRequest(t, context.Background(), "/v1/chat/completions", `{"prompt":"`+strings.Repeat("x", maxRequestBodyBytes)+`"}`)
	oversizedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(oversizedRecorder, oversized)
	if oversizedRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d body=%q", oversizedRecorder.Code, oversizedRecorder.Body.String())
	}

	unauthorized := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"prompt":"[case:success]"}`))
	unauthorizedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%q", unauthorizedRecorder.Code, unauthorizedRecorder.Body.String())
	}
}

func TestCountsAndLogsNeverExposeAuthorizationOrRequestContent(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	handler := newProviderHandler(providerOptions{logger: log.New(&logs, "", 0)})
	secret := "synthetic-secret-that-must-not-escape"
	requestText := "private fixture request text"

	badAuth := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"prompt":"`+requestText+` [case:success]"}`))
	badAuth.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(httptest.NewRecorder(), badAuth)

	good := authenticatedRequest(t, context.Background(), "/v1/responses", `{"prompt":"`+requestText+` [case:success]"}`)
	handler.ServeHTTP(httptest.NewRecorder(), good)

	countsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(countsRecorder, httptest.NewRequest(http.MethodGet, "/test/counts", nil))
	if countsRecorder.Code != http.StatusOK {
		t.Fatalf("counts status=%d body=%q", countsRecorder.Code, countsRecorder.Body.String())
	}
	var counts map[string]int
	if err := json.Unmarshal(countsRecorder.Body.Bytes(), &counts); err != nil {
		t.Fatalf("counts JSON: %v body=%q", err, countsRecorder.Body.String())
	}
	if len(counts) != 1 || counts["responses.success"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	combined := logs.String() + countsRecorder.Body.String()
	for _, forbidden := range []string{secret, requestText, testAuthorization, "Authorization"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("logs/counts exposed %q: %q", forbidden, combined)
		}
	}
	for label := range counts {
		if label != "responses.success" {
			t.Fatalf("unexpected count label %q", label)
		}
	}
}

func TestHealthAndMethods(t *testing.T) {
	t.Parallel()
	handler := newProviderHandler(providerOptions{})
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if health.Code != http.StatusOK || health.Body.String() != "ok\n" {
		t.Fatalf("health status=%d body=%q", health.Code, health.Body.String())
	}
	method := httptest.NewRecorder()
	handler.ServeHTTP(method, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status=%d", method.Code)
	}
}

func authenticatedRequest(t *testing.T, ctx context.Context, path, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Authorization", testAuthorization)
	request.Header.Set("Content-Type", "application/json")
	return request
}

type recordingResponseWriter struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	status  int
	writes  []string
	flushes int
	flushAt []int
}

func newRecordingResponseWriter() *recordingResponseWriter {
	return &recordingResponseWriter{header: make(http.Header)}
}

func (w *recordingResponseWriter) Header() http.Header { return w.header }

func (w *recordingResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *recordingResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.writes = append(w.writes, string(p))
	return w.body.Write(p)
}

func (w *recordingResponseWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.flushes++
	w.flushAt = append(w.flushAt, len(w.writes))
}

func (w *recordingResponseWriter) hasFlushBetweenSplitWrites() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, writeCount := range w.flushAt {
		if writeCount > 0 && writeCount < len(w.writes) &&
			!strings.HasSuffix(w.writes[writeCount-1], "\n\n") &&
			strings.Contains(w.writes[writeCount-1]+w.writes[writeCount], "\n\n") {
			return true
		}
	}
	return false
}

func (w *recordingResponseWriter) statusCode() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *recordingResponseWriter) bodyString() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func (w *recordingResponseWriter) flushCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushes
}

func (w *recordingResponseWriter) hasSplitSSEFrame() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for index := 0; index+1 < len(w.writes); index++ {
		if !strings.HasSuffix(w.writes[index], "\n\n") && strings.Contains(w.writes[index]+w.writes[index+1], "\n\n") {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

var _ http.Flusher = (*recordingResponseWriter)(nil)
var _ io.Writer = (*recordingResponseWriter)(nil)
