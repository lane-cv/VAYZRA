package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLivenessIncludesRequestID(t *testing.T) {
	h := New(Dependencies{Ready: func(context.Context) error { return nil }})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatal(w.Body.String())
	}
}

func TestReadinessReturnsStableError(t *testing.T) {
	h := New(Dependencies{Ready: func(context.Context) error { return context.DeadlineExceeded }})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("missing request ID")
	}
	if got := w.Body.String(); !strings.Contains(got, `"code":"not_ready"`) || !strings.Contains(got, `"message":"服务暂不可用"`) || !strings.Contains(got, `"requestId":`) {
		t.Fatalf("unexpected body: %s", got)
	}
}
