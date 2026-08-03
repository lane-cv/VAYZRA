package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/secretfile"
	"happylearn.local/app/internal/release"
)

const (
	perCheckTimeout = 5 * time.Second
	totalTimeout    = 45 * time.Second
	maxResponseBody = 64 * 1024
)

type checkResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMS int64  `json:"durationMs"`
	TraceID    string `json:"traceId"`
}

type acceptanceResult struct {
	Status   string        `json:"status"`
	Category string        `json:"category"`
	Checks   []checkResult `json:"checks"`
}

type check struct {
	name string
	run  func(context.Context) error
}

type acceptanceConfig struct {
	manifest      release.Manifest
	publicURL     string
	internalURL   string
	databaseURL   string
	redisURL      string
	metricsSecret string
	minioEndpoint string
	minioAccess   string
	minioSecret   string
	minioTLS      bool
}

func main() {
	if run(context.Background(), os.Args[1:], os.Stdout, os.Getenv) != 0 {
		os.Exit(1)
	}
}

func run(parent context.Context, args []string, output io.Writer, getenv func(string) string) int {
	config, err := loadConfig(args, getenv)
	if err != nil {
		_ = json.NewEncoder(output).Encode(acceptanceResult{Status: "fail", Category: "configuration_unavailable", Checks: []checkResult{}})
		return 1
	}
	traceID, err := newTraceID()
	if err != nil {
		_ = json.NewEncoder(output).Encode(acceptanceResult{Status: "fail", Category: "trace_unavailable", Checks: []checkResult{}})
		return 1
	}
	ctx, cancel := context.WithTimeout(parent, totalTimeout)
	defer cancel()
	checks := productionChecks(config, traceID)
	result := executeChecks(ctx, checks, traceID, time.Now)
	if json.NewEncoder(output).Encode(result) != nil {
		return 1
	}
	if result.Status != "pass" {
		return 1
	}
	return 0
}

func executeChecks(ctx context.Context, checks []check, traceID string, now func() time.Time) acceptanceResult {
	result := acceptanceResult{Status: "pass", Category: "acceptance_passed", Checks: make([]checkResult, 0, len(checks))}
	for _, item := range checks {
		started := now()
		checkCtx, cancel := context.WithTimeout(ctx, perCheckTimeout)
		err := item.run(checkCtx)
		cancel()
		duration := now().Sub(started).Milliseconds()
		if duration < 0 {
			duration = 0
		}
		status := "pass"
		if err != nil {
			status = "fail"
			result.Status = "fail"
			result.Category = "acceptance_failed"
		}
		result.Checks = append(result.Checks, checkResult{Name: item.name, Status: status, DurationMS: duration, TraceID: traceID})
	}
	return result
}

func loadConfig(args []string, getenv func(string) string) (acceptanceConfig, error) {
	set := flag.NewFlagSet("acceptance", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	manifestPath := set.String("manifest", "", "")
	if len(args) == 0 || args[0] != "run" || set.Parse(args[1:]) != nil || set.NArg() != 0 || *manifestPath == "" {
		return acceptanceConfig{}, errors.New("invalid arguments")
	}
	manifest, err := readManifest(*manifestPath)
	if err != nil {
		return acceptanceConfig{}, err
	}
	publicURL, err := safeBaseURL(getenv("HAPPYLEARN_ACCEPTANCE_PUBLIC_URL"))
	if err != nil {
		return acceptanceConfig{}, err
	}
	internalURL, err := safeBaseURL(getenv("HAPPYLEARN_ACCEPTANCE_INTERNAL_URL"))
	if err != nil {
		return acceptanceConfig{}, err
	}
	databaseURL, err := requiredFileSecret(getenv, "HAPPYLEARN_DATABASE_URL", 8*1024)
	if err != nil {
		return acceptanceConfig{}, err
	}
	redisURL, err := requiredFileSecret(getenv, "HAPPYLEARN_REDIS_URL", 8*1024)
	if err != nil {
		return acceptanceConfig{}, err
	}
	metrics, err := requiredFileSecret(getenv, "HAPPYLEARN_METRICS_BEARER_SECRET", 4*1024)
	if err != nil {
		return acceptanceConfig{}, err
	}
	access, err := requiredFileSecret(getenv, "HAPPYLEARN_MINIO_ACCESS_KEY", 4*1024)
	if err != nil {
		return acceptanceConfig{}, err
	}
	secret, err := requiredFileSecret(getenv, "HAPPYLEARN_MINIO_SECRET_KEY", 4*1024)
	if err != nil {
		return acceptanceConfig{}, err
	}
	endpoint := getenv("HAPPYLEARN_MINIO_ENDPOINT")
	if endpoint == "" || strings.ContainsAny(endpoint, "\r\n/?#@") {
		return acceptanceConfig{}, errors.New("invalid object store endpoint")
	}
	tls, err := strconv.ParseBool(getenv("HAPPYLEARN_MINIO_USE_TLS"))
	if err != nil {
		return acceptanceConfig{}, errors.New("invalid object store transport")
	}
	return acceptanceConfig{manifest: manifest, publicURL: publicURL, internalURL: internalURL, databaseURL: databaseURL, redisURL: redisURL, metricsSecret: metrics, minioEndpoint: endpoint, minioAccess: access, minioSecret: secret, minioTLS: tls}, nil
}

func productionChecks(config acceptanceConfig, traceID string) []check {
	client := &http.Client{Timeout: perCheckTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return []check{
		{name: "schema_build_compatibility", run: func(ctx context.Context) error { return checkReadiness(ctx, client, config) }},
		{name: "login_challenge", run: func(ctx context.Context) error {
			return checkHTTP(ctx, client, config.publicURL+"/api/v1/auth/challenge", "", http.StatusOK, "image/png")
		}},
		{name: "database_constant_read", run: func(ctx context.Context) error { return checkDatabase(ctx, config.databaseURL) }},
		{name: "redis_round_trip", run: func(ctx context.Context) error { return checkRedis(ctx, config.redisURL, traceID) }},
		{name: "object_store_bucket_list", run: func(ctx context.Context) error { return checkObjectStore(ctx, config) }},
		{name: "static_asset", run: func(ctx context.Context) error {
			return checkHTTP(ctx, client, config.publicURL+"/", "", http.StatusOK, "text/html")
		}},
		{name: "private_metrics_authorization", run: func(ctx context.Context) error {
			return checkHTTP(ctx, client, config.internalURL+"/internal/metrics", config.metricsSecret, http.StatusOK, "text/plain")
		}},
		{name: "public_internal_metrics_denial", run: func(ctx context.Context) error {
			return checkHTTP(ctx, client, config.publicURL+"/internal/metrics", "", http.StatusNotFound, "")
		}},
	}
}

func checkReadiness(ctx context.Context, client *http.Client, config acceptanceConfig) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, config.internalURL+"/internal/readiness", nil)
	if err != nil {
		return errors.New("readiness unavailable")
	}
	request.Header.Set("Authorization", "Bearer "+config.metricsSecret)
	response, err := client.Do(request)
	if err != nil {
		return errors.New("readiness unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > maxResponseBody {
		return errors.New("readiness unavailable")
	}
	var body struct {
		Status           string `json:"status"`
		Version          string `json:"version"`
		Commit           string `json:"commit"`
		SchemaVersion    int64  `json:"schemaVersion"`
		MinSchemaVersion int64  `json:"minSchemaVersion"`
		MaxSchemaVersion int64  `json:"maxSchemaVersion"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.Status != "ready" || body.Version != config.manifest.Version || body.Commit != config.manifest.Commit || !config.manifest.CompatibleWithSchema(body.SchemaVersion) {
		return errors.New("readiness mismatch")
	}
	if body.MinSchemaVersion != config.manifest.MinSchemaVersion || body.MaxSchemaVersion != config.manifest.MaxSchemaVersion {
		return errors.New("readiness compatibility mismatch")
	}
	return nil
}

func checkHTTP(ctx context.Context, client *http.Client, endpoint, bearer string, status int, contentType string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("http check unavailable")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("http check unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != status || (contentType != "" && !strings.HasPrefix(response.Header.Get("Content-Type"), contentType)) {
		return errors.New("http check failed")
	}
	read, err := io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil || read > maxResponseBody {
		return errors.New("http response invalid")
	}
	return nil
}

func checkDatabase(ctx context.Context, databaseURL string) error {
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		return errors.New("database unavailable")
	}
	defer pool.Close()
	var constant int
	if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&constant); err != nil || constant != 1 {
		return errors.New("database check failed")
	}
	return nil
}

func checkRedis(ctx context.Context, redisURL, traceID string) error {
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return errors.New("redis unavailable")
	}
	client := redis.NewClient(options)
	defer client.Close()
	key := "happylearn:acceptance:" + traceID
	value := "acceptance"
	if err := client.Set(ctx, key, value, 30*time.Second).Err(); err != nil {
		return errors.New("redis check failed")
	}
	defer client.Del(context.Background(), key)
	got, err := client.Get(ctx, key).Result()
	if err != nil || got != value {
		return errors.New("redis check failed")
	}
	if removed, err := client.Del(ctx, key).Result(); err != nil || removed != 1 {
		return errors.New("redis check failed")
	}
	return nil
}

func checkObjectStore(ctx context.Context, config acceptanceConfig) error {
	client, err := minio.New(config.minioEndpoint, &minio.Options{Creds: credentials.NewStaticV4(config.minioAccess, config.minioSecret, ""), Secure: config.minioTLS})
	if err != nil {
		return errors.New("object store unavailable")
	}
	if _, err := client.ListBuckets(ctx); err != nil {
		return errors.New("object store check failed")
	}
	return nil
}

func readManifest(path string) (release.Manifest, error) {
	if !filepath.IsAbs(path) {
		return release.Manifest{}, errors.New("manifest unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return release.Manifest{}, errors.New("manifest unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return release.Manifest{}, errors.New("manifest unavailable")
	}
	return release.ParseManifest(data)
}

func requiredFileSecret(getenv func(string) string, name string, maxBytes int64) (string, error) {
	if getenv(name) != "" || getenv(name+"_FILE") == "" {
		return "", errors.New("secret unavailable")
	}
	value, err := secretfile.Read(getenv(name + "_FILE"))
	if err != nil || int64(len(value)) > maxBytes {
		return "", errors.New("secret unavailable")
	}
	return string(value), nil
}

func safeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("invalid base URL")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func newTraceID() (string, error) {
	var body [16]byte
	if _, err := rand.Read(body[:]); err != nil {
		return "", fmt.Errorf("create trace")
	}
	return hex.EncodeToString(body[:]), nil
}
