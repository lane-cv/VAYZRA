package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func productionConfigEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"HAPPYLEARN_ENV":                    "production",
		"HAPPYLEARN_DATABASE_URL":           "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL":              "redis://localhost:6379/0",
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET":  "test-login-throttle-secret-0123456789",
		"HAPPYLEARN_PUBLIC_ORIGIN":          "https://learn.example.com",
		"HAPPYLEARN_MINIO_ENDPOINT":         "minio.internal:9000",
		"HAPPYLEARN_MINIO_ACCESS_KEY":       "test-access-key",
		"HAPPYLEARN_MINIO_SECRET_KEY":       "test-secret-key",
		"HAPPYLEARN_MINIO_USE_TLS":          "true",
		"HAPPYLEARN_MINIO_ORIGINALS_BUCKET": "happylearn-originals",
		"HAPPYLEARN_MINIO_PREVIEWS_BUCKET":  "happylearn-previews",
		"HAPPYLEARN_AI_MASTER_KEY":          base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"HAPPYLEARN_METRICS_BEARER_SECRET_FILE": writeConfigSecretFixture(
			t,
			"test-metrics-bearer-secret-0123456789",
			0o600,
		),
		"HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE": writeConfigSecretFixture(
			t,
			"test-host-metrics-hmac-secret-0123456789",
			0o600,
		),
	}
}

func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestProductionRequires32ByteBase64AIMasterKey(t *testing.T) {
	_, err := Load(mapEnv(map[string]string{
		"HAPPYLEARN_ENV":           "production",
		"HAPPYLEARN_AI_MASTER_KEY": base64.StdEncoding.EncodeToString(make([]byte, 31)),
	}))
	if err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_AI_MASTER_KEY") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadParsesAIConfiguration(t *testing.T) {
	env := productionConfigEnv(t)
	env["HAPPYLEARN_AI_MASTER_KEY"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	env["HAPPYLEARN_AI_MASTER_KEY_VERSION"] = "17"
	env["HAPPYLEARN_AI_BUSINESS_TIMEZONE"] = "Asia/Shanghai"
	env["HAPPYLEARN_AI_GLOBAL_CONCURRENCY"] = "8"
	env["HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY"] = "3"
	cfg, err := Load(mapEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cfg.AIMasterKey, bytes.Repeat([]byte{9}, 32)) || cfg.AIMasterKeyVersion != 17 || cfg.AIBusinessTimezone != "Asia/Shanghai" || cfg.AIGlobalConcurrency != 8 || cfg.AIPerStudentConcurrency != 3 || cfg.AIAllowPrivateProvider {
		t.Fatalf("unexpected AI configuration: %#v", cfg)
	}
}

func TestLoadUsesDevelopmentOnlyAIConfigurationDefaults(t *testing.T) {
	cfg, err := Load(mapEnv(map[string]string{
		"HAPPYLEARN_ENV":                   "development",
		"HAPPYLEARN_DATABASE_URL":          "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL":             "redis://localhost:6379/0",
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret-0123456789",
		"HAPPYLEARN_PUBLIC_ORIGIN":         "https://learn.example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AIMasterKey) != 32 || cfg.AIMasterKeyVersion != 1 || cfg.AIBusinessTimezone != "Asia/Shanghai" || cfg.AIGlobalConcurrency != 2 || cfg.AIPerStudentConcurrency != 1 || cfg.AIAllowPrivateProvider {
		t.Fatalf("unexpected development AI configuration: %#v", cfg)
	}
}

func TestLoadRequiresExplicitDevelopmentEnvironmentForAIExceptions(t *testing.T) {
	base := map[string]string{
		"HAPPYLEARN_DATABASE_URL":          "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL":             "redis://localhost:6379/0",
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret-0123456789",
		"HAPPYLEARN_PUBLIC_ORIGIN":         "https://learn.example.com",
	}

	t.Run("fallback master key", func(t *testing.T) {
		_, err := Load(mapEnv(base))
		if err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_AI_MASTER_KEY") {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("private provider", func(t *testing.T) {
		env := make(map[string]string, len(base)+2)
		for key, value := range base {
			env[key] = value
		}
		env["HAPPYLEARN_AI_MASTER_KEY"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
		env["HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER"] = "true"
		_, err := Load(mapEnv(env))
		if err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER") {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestLoadRejectsInvalidAIConfiguration(t *testing.T) {
	for name, tc := range map[string]struct {
		value        string
		wantVariable string
	}{
		"invalid master key encoding": {"not-base64", "HAPPYLEARN_AI_MASTER_KEY"},
		"short master key":            {base64.StdEncoding.EncodeToString(make([]byte, 31)), "HAPPYLEARN_AI_MASTER_KEY"},
		"zero key version":            {"0", "HAPPYLEARN_AI_MASTER_KEY_VERSION"},
		"large key version":           {"32768", "HAPPYLEARN_AI_MASTER_KEY_VERSION"},
		"wrong timezone":              {"UTC", "HAPPYLEARN_AI_BUSINESS_TIMEZONE"},
		"zero global concurrency":     {"0", "HAPPYLEARN_AI_GLOBAL_CONCURRENCY"},
		"large global concurrency":    {"9", "HAPPYLEARN_AI_GLOBAL_CONCURRENCY"},
		"zero student concurrency":    {"0", "HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY"},
		"large student concurrency":   {"3", "HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY"},
	} {
		t.Run(name, func(t *testing.T) {
			env := productionConfigEnv(t)
			switch tc.wantVariable {
			case "HAPPYLEARN_AI_MASTER_KEY":
				env[tc.wantVariable] = tc.value
			case "HAPPYLEARN_AI_MASTER_KEY_VERSION", "HAPPYLEARN_AI_BUSINESS_TIMEZONE", "HAPPYLEARN_AI_GLOBAL_CONCURRENCY", "HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY":
				env[tc.wantVariable] = tc.value
				if tc.wantVariable == "HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY" && tc.value == "3" {
					env["HAPPYLEARN_AI_GLOBAL_CONCURRENCY"] = "2"
				}
			}
			_, err := Load(mapEnv(env))
			if err == nil || !strings.Contains(err.Error(), tc.wantVariable) {
				t.Fatalf("error=%v, want %s validation", err, tc.wantVariable)
			}
		})
	}
}

func TestLoadAllowsPrivateAIProvidersOnlyInDevelopment(t *testing.T) {
	production := productionConfigEnv(t)
	production["HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER"] = "true"
	if _, err := Load(mapEnv(production)); err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER") {
		t.Fatalf("production error=%v", err)
	}

	development := map[string]string{
		"HAPPYLEARN_ENV":                        "development",
		"HAPPYLEARN_DATABASE_URL":               "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL":                  "redis://localhost:6379/0",
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET":      "test-login-throttle-secret-0123456789",
		"HAPPYLEARN_PUBLIC_ORIGIN":              "https://learn.example.com",
		"HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER":  "true",
		"HAPPYLEARN_AI_GLOBAL_CONCURRENCY":      "2",
		"HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY": "1",
	}
	cfg, err := Load(mapEnv(development))
	if err != nil || !cfg.AIAllowPrivateProvider {
		t.Fatalf("cfg=%#v err=%v", cfg, err)
	}
}

func TestLoadRejectsMissingMinIOValuesInProduction(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"HAPPYLEARN_MINIO_ENDPOINT", "HAPPYLEARN_MINIO_ENDPOINT is required in production"},
		{"HAPPYLEARN_MINIO_ACCESS_KEY", "HAPPYLEARN_MINIO_ACCESS_KEY is required in production"},
		{"HAPPYLEARN_MINIO_SECRET_KEY", "HAPPYLEARN_MINIO_SECRET_KEY is required in production"},
		{"HAPPYLEARN_MINIO_ORIGINALS_BUCKET", "HAPPYLEARN_MINIO_ORIGINALS_BUCKET is required in production"},
		{"HAPPYLEARN_MINIO_PREVIEWS_BUCKET", "HAPPYLEARN_MINIO_PREVIEWS_BUCKET is required in production"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := productionConfigEnv(t)
			delete(env, tc.name)
			_, err := Load(func(k string) string { return env[k] })
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadRejectsUnsafeMinIOEndpointsWithoutLeakingCredentials(t *testing.T) {
	for _, endpoint := range []string{
		"https://minio.internal:9000/private",
		"https://access:secret@minio.internal:9000",
	} {
		env := productionConfigEnv(t)
		env["HAPPYLEARN_MINIO_ENDPOINT"] = endpoint
		_, err := Load(func(k string) string { return env[k] })
		if err == nil || err.Error() != "HAPPYLEARN_MINIO_ENDPOINT must be a host and optional port without scheme, credentials, path, query, or fragment" {
			t.Fatalf("endpoint=%q error=%v", endpoint, err)
		}
		if strings.Contains(err.Error(), "access") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), endpoint) {
			t.Fatalf("endpoint credentials leaked in error: %v", err)
		}
	}
}

func TestLoadRejectsInvalidMinIOBucketNames(t *testing.T) {
	for _, variable := range []string{"HAPPYLEARN_MINIO_ORIGINALS_BUCKET", "HAPPYLEARN_MINIO_PREVIEWS_BUCKET"} {
		for _, bucket := range []string{"ab", "HappyLearn", "bad_bucket", "192.0.2.1", "bad..bucket", "-bad-bucket"} {
			env := productionConfigEnv(t)
			env[variable] = bucket
			_, err := Load(func(k string) string { return env[k] })
			want := variable + " must be a valid S3 bucket name"
			if err == nil || err.Error() != want {
				t.Fatalf("variable=%s bucket=%q error=%v, want %q", variable, bucket, err, want)
			}
		}
	}
}

func TestLoadParsesMinIOConfiguration(t *testing.T) {
	env := productionConfigEnv(t)
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinIOEndpoint != "minio.internal:9000" || cfg.MinIOAccessKey != "test-access-key" || cfg.MinIOSecretKey != "test-secret-key" || !cfg.MinIOUseTLS || cfg.MinIOOriginalsBucket != "happylearn-originals" || cfg.MinIOPreviewsBucket != "happylearn-previews" {
		t.Fatalf("unexpected MinIO config: %#v", cfg)
	}
}
func TestLoadRejectsMissingRequiredValues(t *testing.T) {
	_, err := Load(func(key string) string {
		if key == "HAPPYLEARN_ENV" {
			return "development"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_DATABASE_URL") {
		t.Fatalf("expected missing database URL error, got %v", err)
	}
}

func TestLoadUsesSessionDurationsFromSpec(t *testing.T) {
	env := map[string]string{
		"HAPPYLEARN_ENV":                   "development",
		"HAPPYLEARN_DATABASE_URL":          "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL":             "redis://localhost:6379/0",
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret-0123456789",
		"HAPPYLEARN_PUBLIC_ORIGIN":         "https://learn.example.com",
	}

	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionIdleTTL != 7*24*time.Hour || cfg.SessionAbsoluteTTL != 30*24*time.Hour {
		t.Fatalf("unexpected session TTLs: %#v", cfg)
	}
}

func TestLoadParsesTrustedProxyCIDRs(t *testing.T) {
	env := map[string]string{
		"HAPPYLEARN_ENV":                   "development",
		"HAPPYLEARN_DATABASE_URL":          "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL":             "redis://localhost:6379/0",
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret-0123456789",
		"HAPPYLEARN_PUBLIC_ORIGIN":         "https://learn.example.com",
		"HAPPYLEARN_TRUSTED_PROXY_CIDRS":   " 10.0.0.0/8,2001:db8::/32 ",
	}

	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxyCIDRs) != 2 || cfg.TrustedProxyCIDRs[0].String() != "10.0.0.0/8" || cfg.TrustedProxyCIDRs[1].String() != "2001:db8::/32" {
		t.Fatalf("trusted proxy CIDRs = %#v", cfg.TrustedProxyCIDRs)
	}
}

func TestLoadRejectsInvalidTrustedProxyCIDRs(t *testing.T) {
	for _, raw := range []string{"not-a-cidr", "10.0.0.0/8,"} {
		env := map[string]string{
			"HAPPYLEARN_ENV":                   "development",
			"HAPPYLEARN_DATABASE_URL":          "postgres://app:test@localhost/app",
			"HAPPYLEARN_REDIS_URL":             "redis://localhost:6379/0",
			"HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret-0123456789",
			"HAPPYLEARN_PUBLIC_ORIGIN":         "https://learn.example.com",
			"HAPPYLEARN_TRUSTED_PROXY_CIDRS":   raw,
		}
		if _, err := Load(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_TRUSTED_PROXY_CIDRS") {
			t.Fatalf("raw=%q error=%v", raw, err)
		}
	}
}

func TestLoadRejectsUnknownEnvironment(t *testing.T) {
	env := map[string]string{
		"HAPPYLEARN_ENV":                   "prodution",
		"HAPPYLEARN_DATABASE_URL":          "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL":             "redis://localhost:6379/0",
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret-0123456789",
		"HAPPYLEARN_PUBLIC_ORIGIN":         "https://learn.example.com",
	}

	_, err := Load(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_ENV") {
		t.Fatalf("expected invalid environment error, got %v", err)
	}
}

func TestLoadValidatesAndNormalizesPublicOrigin(t *testing.T) {
	base := map[string]string{"HAPPYLEARN_ENV": "development", "HAPPYLEARN_DATABASE_URL": "postgres://app:test@localhost/app", "HAPPYLEARN_REDIS_URL": "redis://localhost:6379/0", "HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret-0123456789"}
	for _, raw := range []string{"", "/relative", "https://user@learn.example.com", "ftp://learn.example.com", "https://learn.example.com/path", "https://learn.example.com?q=1", "https://learn.example.com#fragment", "https://one.example,https://two.example"} {
		env := make(map[string]string, len(base)+1)
		for k, v := range base {
			env[k] = v
		}
		env["HAPPYLEARN_PUBLIC_ORIGIN"] = raw
		if _, err := Load(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_PUBLIC_ORIGIN") {
			t.Fatalf("raw=%q err=%v", raw, err)
		}
	}
	for raw, want := range map[string]string{"https://LEARN.example.com/": "https://learn.example.com", "http://learn.example.com:80/": "http://learn.example.com", "https://learn.example.com:443": "https://learn.example.com"} {
		env := make(map[string]string, len(base)+1)
		for k, v := range base {
			env[k] = v
		}
		env["HAPPYLEARN_PUBLIC_ORIGIN"] = raw
		cfg, err := Load(func(k string) string { return env[k] })
		if err != nil || cfg.PublicOrigin != want {
			t.Fatalf("raw=%q cfg=%q err=%v", raw, cfg.PublicOrigin, err)
		}
	}
}

func TestLoadRequiresHTTPSPublicOriginInProduction(t *testing.T) {
	base := productionConfigEnv(t)

	base["HAPPYLEARN_PUBLIC_ORIGIN"] = "http://learn.example.com"
	if _, err := Load(func(k string) string { return base[k] }); err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_PUBLIC_ORIGIN") {
		t.Fatalf("expected production HTTP origin rejection, got %v", err)
	}

	base["HAPPYLEARN_PUBLIC_ORIGIN"] = "https://learn.example.com"
	cfg, err := Load(func(k string) string { return base[k] })
	if err != nil {
		t.Fatalf("expected production HTTPS origin acceptance, got %v", err)
	}
	if cfg.PublicOrigin != "https://learn.example.com" {
		t.Fatalf("public origin = %q", cfg.PublicOrigin)
	}
}

func TestLoadPreservesIPv6BracketsWhenNormalizingPublicOrigin(t *testing.T) {
	base := map[string]string{
		"HAPPYLEARN_ENV":                   "development",
		"HAPPYLEARN_DATABASE_URL":          "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL":             "redis://localhost:6379/0",
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret-0123456789",
	}
	for raw, want := range map[string]string{
		"https://[2001:db8::1]/":     "https://[2001:db8::1]",
		"https://[2001:db8::1]:8443": "https://[2001:db8::1]:8443",
	} {
		env := make(map[string]string, len(base)+1)
		for k, v := range base {
			env[k] = v
		}
		env["HAPPYLEARN_PUBLIC_ORIGIN"] = raw
		cfg, err := Load(func(k string) string { return env[k] })
		if err != nil || cfg.PublicOrigin != want {
			t.Fatalf("raw=%q cfg=%q err=%v", raw, cfg.PublicOrigin, err)
		}
	}
}

func TestLoadInternalListenAndSecretsFromOwnerOnlyFiles(t *testing.T) {
	env := productionConfigEnv(t)
	env["HAPPYLEARN_METRICS_BEARER_SECRET_FILE"] = writeConfigSecretFixture(
		t,
		"metrics-bearer-0123456789abcdef\n",
		0o600,
	)
	env["HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE"] = writeConfigSecretFixture(
		t,
		"host-hmac-0123456789abcdef012345\n",
		0o600,
	)
	env["HAPPYLEARN_WEBHOOK_URL_SECRET_FILE"] = writeConfigSecretFixture(
		t,
		"https://alerts.example.test/operations\n",
		0o600,
	)
	env["HAPPYLEARN_WEBHOOK_AUTHORIZATION_SECRET_FILE"] = writeConfigSecretFixture(
		t,
		"Bearer webhook-authorization-value\n",
		0o600,
	)

	cfg, err := Load(mapEnv(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InternalListenAddress != ":9090" ||
		cfg.MetricsBearerSecret != "metrics-bearer-0123456789abcdef" ||
		string(cfg.HostMetricsHMACSecret) != "host-hmac-0123456789abcdef012345" ||
		cfg.WebhookURL != "https://alerts.example.test/operations" ||
		cfg.WebhookAuthorization != "Bearer webhook-authorization-value" {
		t.Fatalf("unexpected internal configuration: %#v", cfg)
	}

	env["HAPPYLEARN_INTERNAL_LISTEN"] = "127.0.0.1:19090"
	cfg, err = Load(mapEnv(env))
	if err != nil || cfg.InternalListenAddress != "127.0.0.1:19090" {
		t.Fatalf("listen=%q err=%v", cfg.InternalListenAddress, err)
	}
}

func TestLoadInternalSecretsAcceptsOnlyDocumentedFileVariables(t *testing.T) {
	for variable, fileVariable := range map[string]string{
		"HAPPYLEARN_METRICS_BEARER_SECRET":    "HAPPYLEARN_METRICS_BEARER_SECRET_FILE",
		"HAPPYLEARN_HOST_METRICS_HMAC_SECRET": "HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE",
		"HAPPYLEARN_WEBHOOK_URL":              "HAPPYLEARN_WEBHOOK_URL_SECRET_FILE",
		"HAPPYLEARN_WEBHOOK_AUTHORIZATION":    "HAPPYLEARN_WEBHOOK_AUTHORIZATION_SECRET_FILE",
	} {
		t.Run(variable, func(t *testing.T) {
			env := productionConfigEnv(t)
			env[variable] = "direct-secret-must-not-be-accepted"
			_, err := Load(mapEnv(env))
			if err == nil || !strings.Contains(err.Error(), fileVariable) ||
				strings.Contains(err.Error(), "direct-secret-must-not-be-accepted") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestLoadProductionRequiresInternalMetricsAndHostSampleSecretFiles(t *testing.T) {
	for _, variable := range []string{
		"HAPPYLEARN_METRICS_BEARER_SECRET_FILE",
		"HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE",
	} {
		t.Run(variable, func(t *testing.T) {
			env := productionConfigEnv(t)
			delete(env, variable)
			_, err := Load(mapEnv(env))
			if err == nil || !strings.Contains(err.Error(), variable) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestLoadRejectsUnsafeInternalSecretFilesWithoutSensitiveDetails(t *testing.T) {
	secret := "internal-secret-content-must-not-leak"
	tests := []struct {
		name     string
		variable string
		build    func(*testing.T) string
	}{
		{
			name:     "missing metrics bearer",
			variable: "HAPPYLEARN_METRICS_BEARER_SECRET_FILE",
			build: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "sensitive-missing-path")
			},
		},
		{
			name:     "symlink host hmac",
			variable: "HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE",
			build: func(t *testing.T) string {
				target := writeConfigSecretFixture(t, secret, 0o600)
				link := filepath.Join(t.TempDir(), "sensitive-symlink-path")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
		{
			name:     "group writable webhook URL",
			variable: "HAPPYLEARN_WEBHOOK_URL_SECRET_FILE",
			build: func(t *testing.T) string {
				return writeConfigSecretFixture(t, secret, 0o620)
			},
		},
		{
			name:     "world writable webhook authorization",
			variable: "HAPPYLEARN_WEBHOOK_AUTHORIZATION_SECRET_FILE",
			build: func(t *testing.T) string {
				return writeConfigSecretFixture(t, secret, 0o602)
			},
		},
		{
			name:     "empty metrics bearer",
			variable: "HAPPYLEARN_METRICS_BEARER_SECRET_FILE",
			build: func(t *testing.T) string {
				return writeConfigSecretFixture(t, "\n", 0o600)
			},
		},
		{
			name:     "oversized host hmac",
			variable: "HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE",
			build: func(t *testing.T) string {
				return writeConfigSecretFixture(t, strings.Repeat("x", 8*1024+1), 0o600)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := productionConfigEnv(t)
			path := tc.build(t)
			env[tc.variable] = path
			_, err := Load(mapEnv(env))
			if err == nil || !strings.Contains(err.Error(), tc.variable) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), path) ||
				strings.Contains(err.Error(), secret) {
				t.Fatalf("sensitive detail leaked: %q", err)
			}
		})
	}
}

func writeConfigSecretFixture(
	t *testing.T,
	body string,
	mode os.FileMode,
) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
