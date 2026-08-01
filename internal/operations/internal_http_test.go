package operations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type internalMetricsStub struct {
	samples []Sample
	err     error
}

func (s internalMetricsStub) LatestMetrics(
	context.Context,
	time.Time,
) ([]Sample, error) {
	return append([]Sample(nil), s.samples...), s.err
}

type internalSampleSink struct {
	mu      sync.Mutex
	batches [][]Sample
	err     error
}

func (s *internalSampleSink) InsertSamples(
	_ context.Context,
	_ time.Time,
	samples []Sample,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, append([]Sample(nil), samples...))
	return s.err
}

type internalNonceStore struct {
	mu        sync.Mutex
	seen      map[string]struct{}
	claims    int
	ttl       time.Duration
	err       error
	forceUsed bool
}

func (s *internalNonceStore) Claim(
	_ context.Context,
	nonce string,
	ttl time.Duration,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	s.ttl = ttl
	if s.err != nil {
		return false, s.err
	}
	if s.forceUsed {
		return false, nil
	}
	if s.seen == nil {
		s.seen = make(map[string]struct{})
	}
	if _, exists := s.seen[nonce]; exists {
		return false, nil
	}
	s.seen[nonce] = struct{}{}
	return true, nil
}

func TestInternalMetricsRequiresExactBearerAndUsesPrometheusHeaders(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)
	handler := mustInternalHandler(t, InternalHTTPConfig{
		MetricsBearerSecret:   "metrics-bearer-secret",
		HostMetricsHMACSecret: []byte("host-hmac-secret"),
		Clock:                 func() time.Time { return now },
		Metrics: internalMetricsStub{samples: []Sample{{
			Source: SampleSourcePostgres, Metric: SampleMetricServiceUp,
			Scope: SampleScopePostgres, Value: 1, Unit: SampleUnitBoolean,
			ObservedAt: now,
		}}},
		Samples: &internalSampleSink{},
		Nonces:  &internalNonceStore{},
	})

	for name, authorization := range map[string]string{
		"missing":          "",
		"wrong":            "Bearer wrong-secret",
		"wrong scheme":     "Basic metrics-bearer-secret",
		"extra whitespace": "Bearer  metrics-bearer-secret",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != http.StatusNotFound || result.Body.String() != "404 page not found\n" {
				t.Fatalf("status=%d body=%q", result.Code, result.Body.String())
			}
			if got := result.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control=%q", got)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	request.Header.Set("Authorization", "Bearer metrics-bearer-secret")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", result.Code, result.Body.String())
	}
	if got := result.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("Content-Type=%q", got)
	}
	if got := result.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if result.Body.String() != `happylearn_service_up{service="postgres"} 1`+"\n" {
		t.Fatalf("body=%q", result.Body.String())
	}
}

func TestInternalMetricsFailsClosedWithoutLeakingProviderErrors(t *testing.T) {
	handler := mustInternalHandler(t, InternalHTTPConfig{
		MetricsBearerSecret:   "metrics-bearer-secret",
		HostMetricsHMACSecret: []byte("host-hmac-secret"),
		Clock:                 time.Now,
		Metrics:               internalMetricsStub{err: errors.New("secret database coordinates")},
		Samples:               &internalSampleSink{},
		Nonces:                &internalNonceStore{},
	})
	request := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	request.Header.Set("Authorization", "Bearer metrics-bearer-secret")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusServiceUnavailable ||
		strings.Contains(result.Body.String(), "secret") ||
		strings.Contains(result.Body.String(), "database") {
		t.Fatalf("status=%d body=%q", result.Code, result.Body.String())
	}
	if got := result.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestInternalHostSamplesAuthenticatesCanonicalPayloadAndRejectsReplay(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)
	secret := []byte("host-hmac-secret")
	sink := &internalSampleSink{}
	nonces := &internalNonceStore{}
	handler := mustInternalHandler(t, InternalHTTPConfig{
		MetricsBearerSecret:   "metrics-bearer-secret",
		HostMetricsHMACSecret: secret,
		Clock:                 func() time.Time { return now },
		Metrics:               internalMetricsStub{},
		Samples:               sink,
		Nonces:                nonces,
	})
	payload := HostPayload{
		SchemaVersion: 1,
		ObservedAt:    now,
		Services: []HostServiceSample{{
			Service: "app", Up: true, CPUPercent: 12.5,
			MemoryBytes: 1024, MemoryLimitBytes: 2048,
			Restarts: hostRestartCount(2),
		}},
		Filesystems: []FilesystemSample{{
			Filesystem:  "root",
			UsedPercent: 37.5,
		}},
	}
	body := canonicalHostPayload(t, payload)
	nonce := "0123456789abcdef0123456789abcdef"

	first := signedHostSampleRequest(t, secret, now.Unix(), nonce, body)
	firstResult := httptest.NewRecorder()
	handler.ServeHTTP(firstResult, first)
	if firstResult.Code != http.StatusNoContent || firstResult.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", firstResult.Code, firstResult.Body.String())
	}
	if nonces.claims != 1 || nonces.ttl != 120*time.Second {
		t.Fatalf("claims=%d ttl=%s", nonces.claims, nonces.ttl)
	}
	if len(sink.batches) != 1 || len(sink.batches[0]) != 6 {
		t.Fatalf("batches=%#v", sink.batches)
	}
	for _, sample := range sink.batches[0] {
		if sample.Source != SampleSourceHost || !sample.ObservedAt.Equal(now) {
			t.Fatalf("unexpected sample: %#v", sample)
		}
	}

	replay := signedHostSampleRequest(t, secret, now.Unix(), nonce, body)
	replayResult := httptest.NewRecorder()
	handler.ServeHTTP(replayResult, replay)
	if replayResult.Code != http.StatusNotFound || len(sink.batches) != 1 {
		t.Fatalf("status=%d batches=%d", replayResult.Code, len(sink.batches))
	}
	if got := replayResult.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	t.Log("PHASE5_FAILURE_EVIDENCE case=host_sample_replay actual=rejected maintenance=normal alert=suppressed plaintext_dump=absent")
}

func hostRestartCount(value int64) *int64 {
	return &value
}

func TestInternalHostSamplesTreatsNullRestartsAsUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)
	secret := []byte("host-hmac-secret")
	sink := &internalSampleSink{}
	handler := mustInternalHandler(t, InternalHTTPConfig{
		MetricsBearerSecret:   "metrics-bearer-secret",
		HostMetricsHMACSecret: secret,
		Clock:                 func() time.Time { return now },
		Metrics:               internalMetricsStub{},
		Samples:               sink,
		Nonces:                &internalNonceStore{},
	})
	body := []byte(`{"schemaVersion":1,"observedAt":"2026-07-30T03:00:00Z","services":[{"service":"app","up":false,"cpuPercent":0,"memoryBytes":0,"memoryLimitBytes":0,"restarts":null}],"filesystems":[{"filesystem":"root","usedPercent":1}]}` + "\n")
	request := signedHostSampleRequest(
		t,
		secret,
		now.Unix(),
		"abcdef0123456789abcdef0123456789",
		body,
	)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%q", result.Code, result.Body.String())
	}
	if len(sink.batches) != 1 || len(sink.batches[0]) != 5 {
		t.Fatalf("batches=%#v", sink.batches)
	}
	for _, sample := range sink.batches[0] {
		if sample.Metric == SampleMetricHostServiceRestarts {
			t.Fatalf("unavailable restart count was persisted: %#v", sample)
		}
	}
}

func TestInternalHostSamplesEnforcesTimestampWindowAndHeaderGrammar(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)
	secret := []byte("host-hmac-secret")
	body := canonicalHostPayload(t, HostPayload{
		SchemaVersion: 1,
		ObservedAt:    now,
		Services: []HostServiceSample{{
			Service: "worker", Up: true, MemoryLimitBytes: 1,
		}},
		Filesystems: []FilesystemSample{},
	})
	tests := []struct {
		name      string
		timestamp string
		nonce     string
		signature string
		want      int
	}{
		{"past boundary", strconv.FormatInt(now.Add(-90*time.Second).Unix(), 10), strings.Repeat("a", 32), "", http.StatusNoContent},
		{"future boundary", strconv.FormatInt(now.Add(90*time.Second).Unix(), 10), strings.Repeat("b", 32), "", http.StatusNoContent},
		{"past skew", strconv.FormatInt(now.Add(-91*time.Second).Unix(), 10), strings.Repeat("c", 32), "", http.StatusNotFound},
		{"future skew", strconv.FormatInt(now.Add(91*time.Second).Unix(), 10), strings.Repeat("d", 32), "", http.StatusNotFound},
		{"timestamp syntax", "+1", strings.Repeat("e", 32), "", http.StatusNotFound},
		{"uppercase nonce", strconv.FormatInt(now.Unix(), 10), strings.Repeat("A", 32), "", http.StatusNotFound},
		{"short nonce", strconv.FormatInt(now.Unix(), 10), strings.Repeat("f", 31), "", http.StatusNotFound},
		{"wrong signature", strconv.FormatInt(now.Unix(), 10), strings.Repeat("0", 32), "sha256=" + strings.Repeat("0", 64), http.StatusNotFound},
		{"uppercase signature", strconv.FormatInt(now.Unix(), 10), strings.Repeat("1", 32), "sha256=" + strings.Repeat("A", 64), http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			timestamp, _ := strconv.ParseInt(tc.timestamp, 10, 64)
			request := signedHostSampleRequest(t, secret, timestamp, tc.nonce, body)
			request.Header.Set("X-HL-Timestamp", tc.timestamp)
			if tc.signature != "" {
				request.Header.Set("X-HL-Signature", tc.signature)
			}
			result := httptest.NewRecorder()
			mustInternalHandler(t, InternalHTTPConfig{
				MetricsBearerSecret:   "metrics-bearer-secret",
				HostMetricsHMACSecret: secret,
				Clock:                 func() time.Time { return now },
				Metrics:               internalMetricsStub{},
				Samples:               &internalSampleSink{},
				Nonces:                &internalNonceStore{},
			}).ServeHTTP(result, request)
			if result.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%q", result.Code, tc.want, result.Body.String())
			}
		})
	}
}

func TestInternalHostSamplesRejectsNoncanonicalUnknownAndUnsafePayloads(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC)
	secret := []byte("host-hmac-secret")
	valid := HostPayload{
		SchemaVersion: 1,
		ObservedAt:    now,
		Services: []HostServiceSample{{
			Service: "postgres", Up: true, MemoryLimitBytes: 1,
		}},
		Filesystems: []FilesystemSample{},
	}
	validBody := canonicalHostPayload(t, valid)
	unknown := bytes.TrimSuffix(validBody, []byte("\n"))
	unknown = append(unknown[:len(unknown)-1], []byte(`,"metric":"password"}`+"\n")...)
	uuidLabel := valid
	uuidLabel.Services[0].Service = "550e8400-e29b-41d4-a716-446655440000"
	tests := []struct {
		name string
		body []byte
	}{
		{"noncanonical whitespace", bytes.Replace(validBody, []byte(`"schemaVersion"`), []byte(` "schemaVersion"`), 1)},
		{"unknown metric field", unknown},
		{"uuid-like service label", canonicalHostPayload(t, uuidLabel)},
		{"oversized body", append(validBody, bytes.Repeat([]byte(" "), MaxHostPayloadBytes+1)...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sink := &internalSampleSink{}
			nonces := &internalNonceStore{}
			handler := mustInternalHandler(t, InternalHTTPConfig{
				MetricsBearerSecret:   "metrics-bearer-secret",
				HostMetricsHMACSecret: secret,
				Clock:                 func() time.Time { return now },
				Metrics:               internalMetricsStub{},
				Samples:               sink,
				Nonces:                nonces,
			})
			request := signedHostSampleRequest(
				t,
				secret,
				now.Unix(),
				"0123456789abcdef0123456789abcdef",
				tc.body,
			)
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != http.StatusNotFound || len(sink.batches) != 0 ||
				nonces.claims != 0 {
				t.Fatalf(
					"status=%d batches=%d claims=%d body=%q",
					result.Code,
					len(sink.batches),
					nonces.claims,
					result.Body.String(),
				)
			}
		})
	}
}

func TestInternalHandlerDeniesPublicAndUnknownRoutes(t *testing.T) {
	handler := mustInternalHandler(t, InternalHTTPConfig{
		MetricsBearerSecret:   "metrics-bearer-secret",
		HostMetricsHMACSecret: []byte("host-hmac-secret"),
		Clock:                 time.Now,
		Metrics:               internalMetricsStub{},
		Samples:               &internalSampleSink{},
		Nonces:                &internalNonceStore{},
	})
	for _, target := range []string{
		"/", "/api/v1/admin/operations/dashboard", "/internal",
		"/internal/metrics/", "/internal/host-samples/",
	} {
		result := httptest.NewRecorder()
		handler.ServeHTTP(
			result,
			httptest.NewRequest(http.MethodGet, target, nil),
		)
		if result.Code != http.StatusNotFound {
			t.Fatalf("target=%q status=%d", target, result.Code)
		}
	}
}

func TestInternalHostSampleRedisNonceClaimIsAtomicAndExpires(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisHostNonceStore(client)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const nonce = "0123456789abcdef0123456789abcdef"
	claimed, err := store.Claim(ctx, nonce, 120*time.Second)
	if err != nil || !claimed {
		t.Fatalf("first claim=(%t,%v)", claimed, err)
	}
	claimed, err = store.Claim(ctx, nonce, 120*time.Second)
	if err != nil || claimed {
		t.Fatalf("replay claim=(%t,%v)", claimed, err)
	}
	mini.FastForward(121 * time.Second)
	claimed, err = store.Claim(ctx, nonce, 120*time.Second)
	if err != nil || !claimed {
		t.Fatalf("expired claim=(%t,%v)", claimed, err)
	}
}

func mustInternalHandler(t *testing.T, cfg InternalHTTPConfig) http.Handler {
	t.Helper()
	handler, err := NewInternalHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func canonicalHostPayload(t *testing.T, payload HostPayload) []byte {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func signedHostSampleRequest(
	t *testing.T,
	secret []byte,
	timestamp int64,
	nonce string,
	body []byte,
) *http.Request {
	t.Helper()
	timestampText := strconv.FormatInt(timestamp, 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestampText))
	mac.Write([]byte("\n"))
	mac.Write([]byte(nonce))
	mac.Write([]byte("\n"))
	mac.Write(body)
	request := httptest.NewRequest(
		http.MethodPost,
		"/internal/host-samples",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-HL-Timestamp", timestampText)
	request.Header.Set("X-HL-Nonce", nonce)
	request.Header.Set("X-HL-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return request
}
