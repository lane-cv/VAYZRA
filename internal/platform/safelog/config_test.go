package safelog

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"happylearn.local/app/internal/platform/config"
)

func TestNewFromConfigRegistersEveryConfiguredSecretRepresentation(t *testing.T) {
	aiKey := []byte("0123456789abcdef0123456789abcdef")
	hmacKey := []byte("host-hmac-0123456789abcdef012345")
	cfg := loggingConfigFixture(aiKey, hmacKey)
	var output bytes.Buffer
	logger, err := NewFromConfig(&output, func() time.Time { return fixedTime }, cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}

	markers := []string{
		cfg.DatabaseURL,
		"database-password-secret",
		"query-password-secret",
		cfg.RedisURL,
		"redis-password-secret",
		cfg.LoginThrottleSecret,
		cfg.MinIOAccessKey,
		cfg.MinIOSecretKey,
		string(aiKey),
		base64.StdEncoding.EncodeToString(aiKey),
		hex.EncodeToString(aiKey),
		cfg.MetricsBearerSecret,
		string(hmacKey),
		base64.StdEncoding.EncodeToString(hmacKey),
		hex.EncodeToString(hmacKey),
		cfg.WebhookURL,
		cfg.WebhookAuthorization,
	}
	for _, marker := range markers {
		output.Reset()
		logger.Info("config.marker", Field{
			Name:  "stage",
			Value: "prefix-" + marker + "-suffix",
		})
		if output.Len() == 0 {
			t.Fatalf("registered marker %q caused an unexpected dropped record", marker)
		}
		if bytes.Contains(output.Bytes(), []byte(marker)) {
			t.Fatalf("configured marker %q leaked in %q", marker, output.Bytes())
		}
	}
}

func TestNewFromConfigRegistersPercentEncodedURLSecretFragments(t *testing.T) {
	cfg := loggingConfigFixture(
		[]byte("0123456789abcdef0123456789abcdef"),
		nil,
	)
	cfg.DatabaseURL = "postgres://app:p%40ssword%2Dsecret@db.example/happylearn?sslpassword=query%2Dpassword%2Dsecret"
	cfg.RedisURL = "redis://:redis%2Dpassword%2Dsecret@redis.example/0"

	var output bytes.Buffer
	logger, err := NewFromConfig(&output, func() time.Time { return fixedTime }, cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}

	for _, marker := range []string{
		"p%40ssword%2Dsecret",
		"p@ssword-secret",
		"query%2Dpassword%2Dsecret",
		"redis%2Dpassword%2Dsecret",
	} {
		output.Reset()
		logger.Info("config.marker", Field{
			Name:  "path",
			Value: "/prefix/" + marker + "/suffix",
		})
		if output.Len() == 0 {
			t.Fatalf("encoded marker %q caused an unexpected dropped record", marker)
		}
		if bytes.Contains(output.Bytes(), []byte(marker)) {
			t.Fatalf("encoded marker %q leaked in %q", marker, output.Bytes())
		}
	}
}

func TestNewFromConfigRegistersGenericKeyQuerySecrets(t *testing.T) {
	cfg := loggingConfigFixture(
		[]byte("0123456789abcdef0123456789abcdef"),
		nil,
	)
	cfg.RedisURL = "redis://:redis-password-secret@redis.example/0?key=private%2Dkey%2Dmaterial"

	var output bytes.Buffer
	logger, err := NewFromConfig(&output, func() time.Time { return fixedTime }, cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	for _, marker := range []string{
		"private-key-material",
		"private%2Dkey%2Dmaterial",
	} {
		output.Reset()
		logger.Info("config.marker", Field{
			Name:  "stage",
			Value: "prefix-" + marker + "-suffix",
		})
		if output.Len() == 0 || bytes.Contains(output.Bytes(), []byte(marker)) {
			t.Fatalf("generic key marker %q was not safely redacted: %q", marker, output.Bytes())
		}
	}
}

func TestNewFromConfigSkipsEmptyOptionalSecrets(t *testing.T) {
	cfg := loggingConfigFixture(
		[]byte("0123456789abcdef0123456789abcdef"),
		nil,
	)
	cfg.MetricsBearerSecret = ""
	cfg.HostMetricsHMACSecret = nil
	cfg.WebhookURL = ""
	cfg.WebhookAuthorization = ""

	var output bytes.Buffer
	logger, err := NewFromConfig(&output, func() time.Time { return fixedTime }, cfg)
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	logger.Info("config.ready", Field{Name: "stage", Value: "loaded"})
	decodeSingleRecord(t, output.Bytes())
}

func TestNewFromConfigFailsClosedForMissingOrShortRequiredSecrets(t *testing.T) {
	base := loggingConfigFixture(
		[]byte("0123456789abcdef0123456789abcdef"),
		nil,
	)
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"database missing", func(cfg *config.Config) { cfg.DatabaseURL = "" }},
		{"database URL short", func(cfg *config.Config) { cfg.DatabaseURL = "db" }},
		{"redis missing", func(cfg *config.Config) { cfg.RedisURL = "" }},
		{"login secret short", func(cfg *config.Config) { cfg.LoginThrottleSecret = "short" }},
		{"MinIO access key short", func(cfg *config.Config) { cfg.MinIOAccessKey = "short" }},
		{"MinIO secret key missing", func(cfg *config.Config) { cfg.MinIOSecretKey = "" }},
		{"AI key short", func(cfg *config.Config) { cfg.AIMasterKey = []byte("short") }},
		{
			"database password short",
			func(cfg *config.Config) {
				cfg.DatabaseURL = "postgres://app:short@db.example/happylearn"
			},
		},
		{
			"secret query value empty",
			func(cfg *config.Config) {
				cfg.DatabaseURL = "postgres://db.example/happylearn?sslpassword="
			},
		},
		{
			"malformed query name",
			func(cfg *config.Config) {
				cfg.DatabaseURL = "postgres://db.example/happylearn?sslpassword%ZZ=query-password-secret"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			cfg.AIMasterKey = bytes.Clone(base.AIMasterKey)
			test.mutate(&cfg)
			if _, err := NewFromConfig(
				&bytes.Buffer{},
				func() time.Time { return fixedTime },
				cfg,
			); err == nil || strings.Contains(err.Error(), "short") {
				t.Fatalf("error = %v, want fixed fail-closed error", err)
			}
		})
	}
}

func TestNewFromConfigFailsClosedForShortOptionalSecrets(t *testing.T) {
	base := loggingConfigFixture(
		[]byte("0123456789abcdef0123456789abcdef"),
		nil,
	)
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"metrics", func(cfg *config.Config) { cfg.MetricsBearerSecret = "short" }},
		{"HMAC", func(cfg *config.Config) { cfg.HostMetricsHMACSecret = []byte("short") }},
		{"webhook URL", func(cfg *config.Config) { cfg.WebhookURL = "short" }},
		{"webhook authorization", func(cfg *config.Config) { cfg.WebhookAuthorization = "short" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.mutate(&cfg)
			if _, err := NewFromConfig(
				&bytes.Buffer{},
				func() time.Time { return fixedTime },
				cfg,
			); err == nil {
				t.Fatal("NewFromConfig accepted short optional secret")
			}
		})
	}
}

func loggingConfigFixture(aiKey, hmacKey []byte) config.Config {
	return config.Config{
		Environment:           "production",
		DatabaseURL:           "postgres://app:database-password-secret@db.example/happylearn?sslpassword=query-password-secret",
		RedisURL:              "redis://:redis-password-secret@redis.example/0",
		LoginThrottleSecret:   "login-throttle-secret-0123456789",
		MinIOAccessKey:        "minio-access-key-secret",
		MinIOSecretKey:        "minio-secret-key-secret",
		AIMasterKey:           bytes.Clone(aiKey),
		MetricsBearerSecret:   "metrics-bearer-secret",
		HostMetricsHMACSecret: bytes.Clone(hmacKey),
		WebhookURL:            "https://alerts.example.test/hooks/private",
		WebhookAuthorization:  "Bearer-webhook-authorization-secret",
	}
}
