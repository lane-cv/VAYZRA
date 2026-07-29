package operations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	MaxHostPayloadBytes = 64 * 1024
	hostTimestampSkew   = 90 * time.Second
	hostNonceTTL        = 120 * time.Second
)

var (
	hostNoncePattern     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hostSignaturePattern = regexp.MustCompile(`^sha256=[0-9a-f]{64}$`)
)

type MetricsSource interface {
	LatestMetrics(context.Context, time.Time) ([]Sample, error)
}

type HostSampleStore interface {
	InsertSamples(context.Context, time.Time, []Sample) error
}

type HostNonceStore interface {
	Claim(context.Context, string, time.Duration) (bool, error)
}

type InternalHTTPConfig struct {
	MetricsBearerSecret   string
	HostMetricsHMACSecret []byte
	Clock                 func() time.Time
	Metrics               MetricsSource
	Samples               HostSampleStore
	Nonces                HostNonceStore
}

type HostPayload struct {
	SchemaVersion int                 `json:"schemaVersion"`
	ObservedAt    time.Time           `json:"observedAt"`
	Services      []HostServiceSample `json:"services"`
	Filesystems   []FilesystemSample  `json:"filesystems"`
}

type HostServiceSample struct {
	Service          string  `json:"service"`
	Up               bool    `json:"up"`
	CPUPercent       float64 `json:"cpuPercent"`
	MemoryBytes      int64   `json:"memoryBytes"`
	MemoryLimitBytes int64   `json:"memoryLimitBytes"`
	Restarts         *int64  `json:"restarts"`
}

type FilesystemSample struct {
	Filesystem  string  `json:"filesystem"`
	UsedPercent float64 `json:"usedPercent"`
}

type internalHTTPHandler struct {
	metricsBearerSecret   []byte
	hostMetricsHMACSecret []byte
	clock                 func() time.Time
	metrics               MetricsSource
	samples               HostSampleStore
	nonces                HostNonceStore
}

type RedisHostNonceStore struct {
	client *redis.Client
}

func NewRedisHostNonceStore(client *redis.Client) (*RedisHostNonceStore, error) {
	if client == nil {
		return nil, ErrInvalid
	}
	return &RedisHostNonceStore{client: client}, nil
}

func (s *RedisHostNonceStore) Claim(
	ctx context.Context,
	nonce string,
	ttl time.Duration,
) (bool, error) {
	if s == nil || s.client == nil || ctx == nil ||
		!hostNoncePattern.MatchString(nonce) || ttl != hostNonceTTL {
		return false, ErrInvalid
	}
	return s.client.SetNX(
		ctx,
		"happylearn:operations:host-nonce:"+nonce,
		"1",
		ttl,
	).Result()
}

func NewInternalHandler(cfg InternalHTTPConfig) (http.Handler, error) {
	if cfg.MetricsBearerSecret == "" ||
		len(cfg.HostMetricsHMACSecret) == 0 ||
		cfg.Metrics == nil ||
		cfg.Samples == nil ||
		cfg.Nonces == nil {
		return nil, ErrInvalid
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &internalHTTPHandler{
		metricsBearerSecret:   []byte(cfg.MetricsBearerSecret),
		hostMetricsHMACSecret: append([]byte(nil), cfg.HostMetricsHMACSecret...),
		clock:                 cfg.Clock,
		metrics:               cfg.Metrics,
		samples:               cfg.Samples,
		nonces:                cfg.Nonces,
	}, nil
}

func (h *internalHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/internal/metrics":
		h.serveMetrics(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/internal/host-samples":
		h.serveHostSamples(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *internalHTTPHandler) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if !h.validBearer(r) {
		http.NotFound(w, r)
		return
	}
	now := h.clock().UTC()
	samples, err := h.metrics.LatestMetrics(r.Context(), now)
	if err != nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	body, err := RenderMetrics(now, samples)
	if err != nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *internalHTTPHandler) validBearer(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	return hmac.Equal(
		[]byte(values[0]),
		append([]byte("Bearer "), h.metricsBearerSecret...),
	)
}

func (h *internalHTTPHandler) serveHostSamples(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Header.Get("Content-Type") != "application/json" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxHostPayloadBytes+1))
	if err != nil || len(body) > MaxHostPayloadBytes {
		http.NotFound(w, r)
		return
	}
	now := h.clock().UTC()
	timestamp, nonce, ok := h.authenticateHostSample(r, body, now)
	if !ok {
		http.NotFound(w, r)
		return
	}
	payload, samples, err := parseHostPayload(body, now, timestamp)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	claimed, err := h.nonces.Claim(r.Context(), nonce, hostNonceTTL)
	if err != nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if !claimed {
		http.NotFound(w, r)
		return
	}
	if err := h.samples.InsertSamples(r.Context(), now, samples); err != nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	_ = payload
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *internalHTTPHandler) authenticateHostSample(
	r *http.Request,
	body []byte,
	now time.Time,
) (time.Time, string, bool) {
	timestampText, ok := singleHeader(r, "X-HL-Timestamp")
	if !ok {
		return time.Time{}, "", false
	}
	timestampSeconds, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil || strconv.FormatInt(timestampSeconds, 10) != timestampText {
		return time.Time{}, "", false
	}
	timestamp := time.Unix(timestampSeconds, 0).UTC()
	if timestamp.Before(now.Add(-hostTimestampSkew)) ||
		timestamp.After(now.Add(hostTimestampSkew)) {
		return time.Time{}, "", false
	}
	nonce, ok := singleHeader(r, "X-HL-Nonce")
	if !ok || !hostNoncePattern.MatchString(nonce) {
		return time.Time{}, "", false
	}
	signature, ok := singleHeader(r, "X-HL-Signature")
	if !ok || !hostSignaturePattern.MatchString(signature) {
		return time.Time{}, "", false
	}
	presented, err := hex.DecodeString(signature[len("sha256="):])
	if err != nil {
		return time.Time{}, "", false
	}
	mac := hmac.New(sha256.New, h.hostMetricsHMACSecret)
	_, _ = mac.Write([]byte(timestampText))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write([]byte(nonce))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(body)
	if !hmac.Equal(presented, mac.Sum(nil)) {
		return time.Time{}, "", false
	}
	return timestamp, nonce, true
}

func singleHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	return firstHeader(values)
}

func firstHeader(values []string) (string, bool) {
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func parseHostPayload(
	body []byte,
	now time.Time,
	timestamp time.Time,
) (HostPayload, []Sample, error) {
	var payload HostPayload
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return HostPayload{}, nil, ErrInvalid
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return HostPayload{}, nil, ErrInvalid
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return HostPayload{}, nil, ErrInvalid
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(body, canonical) ||
		payload.SchemaVersion != 1 ||
		!validSampleTime(payload.ObservedAt) ||
		payload.ObservedAt.Nanosecond() != 0 ||
		payload.ObservedAt.Before(timestamp.Add(-hostTimestampSkew)) ||
		payload.ObservedAt.After(timestamp.Add(hostTimestampSkew)) ||
		payload.ObservedAt.Before(now.Add(-hostTimestampSkew)) ||
		payload.ObservedAt.After(now.Add(hostTimestampSkew)) ||
		len(payload.Services) > 16 ||
		len(payload.Filesystems) > 16 ||
		len(payload.Services)+len(payload.Filesystems) == 0 {
		return HostPayload{}, nil, ErrInvalid
	}
	samples := make([]Sample, 0, len(payload.Services)*5+len(payload.Filesystems))
	services := make(map[SampleScope]struct{}, len(payload.Services))
	for _, service := range payload.Services {
		scope, err := NormalizeHostServiceScope(service.Service)
		if err != nil {
			return HostPayload{}, nil, ErrInvalid
		}
		if _, duplicate := services[scope]; duplicate ||
			!finitePercent(service.CPUPercent) ||
			service.MemoryBytes < 0 ||
			service.MemoryLimitBytes < 0 ||
			service.Restarts != nil && *service.Restarts < 0 {
			return HostPayload{}, nil, ErrInvalid
		}
		services[scope] = struct{}{}
		up := float64(0)
		if service.Up {
			up = 1
		}
		samples = append(
			samples,
			hostSample(payload.ObservedAt, SampleMetricServiceUp, scope, up, SampleUnitBoolean),
			hostSample(payload.ObservedAt, SampleMetricHostServiceCPUPercent, scope, service.CPUPercent, SampleUnitPercent),
			hostSample(payload.ObservedAt, SampleMetricHostServiceMemoryBytes, scope, float64(service.MemoryBytes), SampleUnitBytes),
			hostSample(payload.ObservedAt, SampleMetricHostServiceMemoryLimitBytes, scope, float64(service.MemoryLimitBytes), SampleUnitBytes),
		)
		if service.Restarts != nil {
			samples = append(samples, hostSample(
				payload.ObservedAt,
				SampleMetricHostServiceRestarts,
				scope,
				float64(*service.Restarts),
				SampleUnitCount,
			))
		}
	}
	filesystems := make(map[SampleScope]struct{}, len(payload.Filesystems))
	for _, filesystem := range payload.Filesystems {
		var scope SampleScope
		switch filesystem.Filesystem {
		case "root":
			scope = SampleScopeRoot
		case "backup":
			scope = SampleScopeBackup
		default:
			return HostPayload{}, nil, ErrInvalid
		}
		if _, duplicate := filesystems[scope]; duplicate ||
			!finitePercent(filesystem.UsedPercent) {
			return HostPayload{}, nil, ErrInvalid
		}
		filesystems[scope] = struct{}{}
		samples = append(samples, hostSample(
			payload.ObservedAt,
			SampleMetricFilesystemUsedPercent,
			scope,
			filesystem.UsedPercent,
			SampleUnitPercent,
		))
	}
	for _, sample := range samples {
		if err := ValidateSample(sample, now); err != nil {
			return HostPayload{}, nil, ErrInvalid
		}
	}
	return payload, samples, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func hostSample(
	observedAt time.Time,
	metric SampleMetric,
	scope SampleScope,
	value float64,
	unit SampleUnit,
) Sample {
	return Sample{
		Source: SampleSourceHost, Metric: metric, Scope: scope,
		Value: value, Unit: unit, ObservedAt: observedAt.UTC(),
	}
}

func finitePercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) &&
		value >= 0 && value <= 100
}
