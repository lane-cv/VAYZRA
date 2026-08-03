package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAcceptanceRequiresBoundedNamedChecks(t *testing.T) {
	names := []string{"schema_build_compatibility", "login_challenge", "database_constant_read", "redis_round_trip", "object_store_bucket_list", "static_asset", "private_metrics_authorization", "public_internal_metrics_denial"}
	checks := make([]check, 0, len(names))
	for _, name := range names {
		checks = append(checks, check{name: name, run: func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("check has no deadline")
			}
			return nil
		}})
	}
	now := time.Now()
	clock := func() time.Time { now = now.Add(time.Millisecond); return now }
	result := executeChecks(context.Background(), checks, "trace_12345678", clock)
	if result.Status != "pass" || len(result.Checks) != len(names) {
		t.Fatalf("result=%+v", result)
	}
	for index, item := range result.Checks {
		if item.Name != names[index] || item.Status != "pass" || item.TraceID != "trace_12345678" {
			t.Fatalf("check=%+v", item)
		}
	}
}

func TestAcceptanceFailureOutputContainsNoOperationalValues(t *testing.T) {
	secret := "postgres://user:password@private-db/internal"
	checks := []check{{name: "database_constant_read", run: func(context.Context) error { return errors.New(secret) }}}
	result := executeChecks(context.Background(), checks, "trace_12345678", time.Now)
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) || strings.Contains(string(body), "private-db") || result.Status != "fail" {
		t.Fatalf("unsafe result=%s", body)
	}
}

func TestSafeBaseURLRejectsCredentialsQueriesAndFragments(t *testing.T) {
	for _, value := range []string{"https://user:pass@example.com", "https://example.com?q=secret", "https://example.com/#secret", "file:///tmp/socket", "https://example.com/path"} {
		if _, err := safeBaseURL(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	if got, err := safeBaseURL("http://app:8080"); err != nil || got != "http://app:8080" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestExecuteChecksContinuesForCompleteEvidence(t *testing.T) {
	calls := 0
	checks := []check{{name: "first", run: func(context.Context) error { calls++; return errors.New("failed") }}, {name: "second", run: func(context.Context) error { calls++; return nil }}}
	result := executeChecks(context.Background(), checks, "trace_12345678", time.Now)
	if calls != 2 || len(result.Checks) != 2 || result.Checks[0].Status != "fail" || result.Checks[1].Status != "pass" {
		t.Fatalf("calls=%d result=%+v", calls, result)
	}
}
