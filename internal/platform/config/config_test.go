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
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret",
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

func TestLoadRejectsUnknownEnvironment(t *testing.T) {
	env := map[string]string{
		"HAPPYLEARN_ENV":                   "prodution",
		"HAPPYLEARN_DATABASE_URL":          "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL":             "redis://localhost:6379/0",
		"HAPPYLEARN_LOGIN_THROTTLE_SECRET": "test-login-throttle-secret",
		"HAPPYLEARN_PUBLIC_ORIGIN":         "https://learn.example.com",
	}

	_, err := Load(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_ENV") {
		t.Fatalf("expected invalid environment error, got %v", err)
	}
}
