package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCSRFRequiresMatchingCookieAndHeader(t *testing.T) {
	h := CSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	r.AddCookie(&http.Cookie{Name: "hl_csrf", Value: "token-a"})
	r.Header.Set("X-CSRF-Token", "token-b")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestCSRFAcceptsMatchingTokenAndExemptsOnlyLogin(t *testing.T) {
	for _, tc := range []struct {
		path   string
		cookie string
		header string
	}{
		{"/api/v1/auth/logout", "token-a", "token-a"},
		{"/api/v1/auth/login", "", ""},
	} {
		h := CSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		r := httptest.NewRequest(http.MethodPost, tc.path, nil)
		if tc.cookie != "" {
			r.AddCookie(&http.Cookie{Name: "hl_csrf", Value: tc.cookie})
		}
		r.Header.Set("X-CSRF-Token", tc.header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s: status=%d", tc.path, w.Code)
		}
	}
}
