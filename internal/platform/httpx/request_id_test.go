package httpx

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestRequestIDAcceptsValidClientID(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := RequestIDFromContext(r.Context()); got != "client_id-123" {
			t.Fatalf("request ID in context = %q", got)
		}
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "client_id-123")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if got := w.Header().Get("X-Request-ID"); got != "client_id-123" {
		t.Fatalf("response request ID = %q", got)
	}
}

func TestRequestIDReplacesInvalidClientID(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "not a valid request id")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if got := w.Header().Get("X-Request-ID"); !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(got) {
		t.Fatalf("generated request ID = %q", got)
	}
}
