package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/config"
	"happylearn.local/app/internal/platform/safelog"
)

func TestInternalServerBuildKeepsPrivateRoutesOutOfPublicApplication(t *testing.T) {
	const (
		querySecret = "internal-query-secret"
		authSecret  = "internal-auth-secret"
		bodySecret  = "internal-body-secret"
	)
	var logOutput bytes.Buffer
	logger, err := safelog.New(
		&logOutput,
		time.Now,
		querySecret,
		authSecret,
		bodySecret,
	)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	internalFactoryCalls := 0
	runtime, closeResources, err := buildApplicationRuntime(
		context.Background(),
		config.Config{
			MetricsBearerSecret:   "metrics-secret",
			HostMetricsHMACSecret: []byte("host-secret"),
		},
		applicationDependencies{
			logger: logger,
			open: func(context.Context, string) (*pgxpool.Pool, error) {
				return nil, nil
			},
			migrate: func(context.Context, *pgxpool.Pool) error { return nil },
			newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) {
				return serverAdminAuth{}, nil
			},
			ready: func(*pgxpool.Pool) func(context.Context) error {
				return func(context.Context) error { return nil }
			},
			newInternal: func(
				_ *pgxpool.Pool,
				_ *redis.Client,
				cfg config.Config,
			) (http.Handler, error) {
				internalFactoryCalls++
				if cfg.MetricsBearerSecret != "metrics-secret" ||
					string(cfg.HostMetricsHMACSecret) != "host-secret" {
					t.Fatalf("internal config=%#v", cfg)
				}
				return http.HandlerFunc(func(
					w http.ResponseWriter,
					r *http.Request,
				) {
					if r.URL.Path == "/internal/metrics" {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					http.NotFound(w, r)
				}), nil
			},
			close: func(*pgxpool.Pool) {},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeResources)
	if internalFactoryCalls != 1 || runtime.Public == nil || runtime.Internal == nil {
		t.Fatalf(
			"factoryCalls=%d public=%v internal=%v",
			internalFactoryCalls,
			runtime.Public,
			runtime.Internal,
		)
	}

	internalResult := httptest.NewRecorder()
	internalRequest := httptest.NewRequest(
		http.MethodGet,
		"/internal/metrics?signature="+querySecret,
		strings.NewReader(bodySecret),
	)
	internalRequest.Header.Set("Authorization", "Bearer "+authSecret)
	internalRequest.Header.Set("X-Request-ID", "request-id-internal")
	runtime.Internal.ServeHTTP(internalResult, internalRequest)
	if internalResult.Code != http.StatusNoContent {
		t.Fatalf("internal status=%d", internalResult.Code)
	}
	var requestLog map[string]any
	if err := json.Unmarshal(logOutput.Bytes(), &requestLog); err != nil {
		t.Fatalf("decode internal request log %q: %v", logOutput.Bytes(), err)
	}
	if requestLog["event"] != "http.request" ||
		requestLog["path"] != "/internal/metrics" ||
		requestLog["status"] != float64(http.StatusNoContent) {
		t.Fatalf("internal request log=%#v", requestLog)
	}
	for _, secret := range []string{querySecret, authSecret, bodySecret} {
		if bytes.Contains(logOutput.Bytes(), []byte(secret)) {
			t.Fatalf("internal request log leaked %q: %q", secret, logOutput.Bytes())
		}
	}
	publicResult := httptest.NewRecorder()
	publicRequest := httptest.NewRequest(
		http.MethodGet,
		"/internal/metrics?signature="+querySecret,
		strings.NewReader(bodySecret),
	)
	publicRequest.Header.Set("Authorization", "Bearer "+authSecret)
	publicRequest.Header.Set("X-Request-ID", "request-id-public-boundary")
	runtime.Public.ServeHTTP(publicResult, publicRequest)
	if publicResult.Code != http.StatusNotFound {
		t.Fatalf("public internal-route status=%d", publicResult.Code)
	}
	records := decodeServerSafeLogs(t, logOutput.Bytes())
	if len(records) != 2 ||
		records[1]["event"] != "http.request" ||
		records[1]["path"] != "/internal/metrics" ||
		records[1]["status"] != float64(http.StatusNotFound) ||
		records[1]["request_id"] != "request-id-public-boundary" {
		t.Fatalf("public boundary request logs=%#v", records)
	}
	for _, secret := range []string{querySecret, authSecret, bodySecret} {
		if bytes.Contains(logOutput.Bytes(), []byte(secret)) {
			t.Fatalf("public boundary request log leaked %q: %q", secret, logOutput.Bytes())
		}
	}
}

func TestInternalServerFactoryFailureIsSanitizedAndClosesResources(t *testing.T) {
	closed := false
	runtime, closeResources, err := buildApplicationRuntime(
		context.Background(),
		config.Config{},
		applicationDependencies{
			open: func(context.Context, string) (*pgxpool.Pool, error) {
				return nil, nil
			},
			migrate: func(context.Context, *pgxpool.Pool) error { return nil },
			newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) {
				return serverAdminAuth{}, nil
			},
			ready: func(*pgxpool.Pool) func(context.Context) error {
				return func(context.Context) error { return nil }
			},
			newInternal: func(
				*pgxpool.Pool,
				*redis.Client,
				config.Config,
			) (http.Handler, error) {
				return nil, errors.New("secret internal dependency detail")
			},
			close: func(*pgxpool.Pool) { closed = true },
		},
	)
	if runtime != nil ||
		closeResources != nil || !closed ||
		err == nil || err.Error() != "initialize internal listener" ||
		strings.Contains(err.Error(), "secret") {
		t.Fatalf(
			"runtime=%#v closePresent=%t closed=%t err=%v",
			runtime,
			closeResources != nil,
			closed,
			err,
		)
	}
}

func TestInternalServerPublicBoundaryDeniesPrefixBeforeSPAFallback(t *testing.T) {
	spa := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>console</html>"))
	})
	public := publicOnlyHandler(spa)
	for _, target := range []string{
		"/internal",
		"/internal/",
		"/internal/metrics",
		"/internal/host-samples",
	} {
		result := httptest.NewRecorder()
		public.ServeHTTP(
			result,
			httptest.NewRequest(http.MethodGet, target, nil),
		)
		if result.Code != http.StatusNotFound ||
			strings.Contains(result.Body.String(), "<html>") {
			t.Fatalf(
				"target=%q status=%d body=%q",
				target,
				result.Code,
				result.Body.String(),
			)
		}
	}
	allowed := httptest.NewRecorder()
	public.ServeHTTP(
		allowed,
		httptest.NewRequest(http.MethodGet, "/admin", nil),
	)
	if allowed.Code != http.StatusOK ||
		!strings.Contains(allowed.Body.String(), "<html>") {
		t.Fatalf("allowed status=%d body=%q", allowed.Code, allowed.Body.String())
	}
}

func TestInternalServerDevelopmentWithoutSecretFilesStartsDenyAllListener(t *testing.T) {
	handler, err := newProductionInternalHandler(
		nil,
		nil,
		config.Config{Environment: "development"},
	)
	if err != nil || handler == nil {
		t.Fatalf("handler=%v err=%v", handler, err)
	}
	for _, target := range []string{
		"/internal/metrics",
		"/internal/host-samples",
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
