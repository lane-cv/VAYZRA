package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginGuardRejectsCrossSiteMutation(t *testing.T) {
	h := OriginGuard("https://learn.example.com")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	r := httptest.NewRequest(http.MethodPost, "https://learn.example.com/api/v1/auth/logout", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestOriginGuardAcceptsExactOriginAndRefererFallback(t *testing.T) {
	for _, header := range []struct{ key, value string }{
		{"Origin", "https://learn.example.com"},
		{"Referer", "https://learn.example.com/api/v1/auth/logout"},
	} {
		h := OriginGuard("https://learn.example.com")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		r := httptest.NewRequest(http.MethodPost, "https://learn.example.com/api/v1/auth/logout", nil)
		r.Header.Set(header.key, header.value)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s: status=%d", header.key, w.Code)
		}
	}
}

func TestOriginGuardRejectsMissingOrAmbiguousSource(t *testing.T) {
	for _, configure := range []func(*http.Request){
		func(*http.Request) {},
		func(r *http.Request) {
			r.Header.Add("Origin", "https://learn.example.com")
			r.Header.Add("Origin", "https://evil.example")
		},
		func(r *http.Request) { r.Header.Set("Referer", "https://learn.example.com@evil.example/path") },
	} {
		h := OriginGuard("https://learn.example.com")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		r := httptest.NewRequest(http.MethodPost, "https://learn.example.com/api/v1/auth/logout", nil)
		configure(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d", w.Code)
		}
	}
}
