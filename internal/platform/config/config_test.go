package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRejectsMissingRequiredValues(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_DATABASE_URL") {
		t.Fatalf("expected missing database URL error, got %v", err)
	}
}

func TestLoadUsesSessionDurationsFromSpec(t *testing.T) {
	env := map[string]string{
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
	base := map[string]string{"HAPPYLEARN_DATABASE_URL": "postgres://app:test@localhost/app", "HAPPYLEARN_REDIS_URL": "redis://localhost:6379/0", "HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret-0123456789"}
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
