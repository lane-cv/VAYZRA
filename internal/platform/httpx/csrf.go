package httpx

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
)

const (
	CSRFCookieName = "hl_csrf"
	csrfHeaderName = "X-CSRF-Token"
)

// CSRF requires an equal, nonempty cookie and request header for every unsafe
// request, except the login endpoint that establishes the cookie.
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) || (r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login") {
			next.ServeHTTP(w, r)
			return
		}
		cookieValue, ok := csrfCookie(r)
		headerValues := r.Header.Values(csrfHeaderName)
		if !ok || len(headerValues) != 1 || headerValues[0] == "" || subtle.ConstantTimeCompare([]byte(cookieValue), []byte(headerValues[0])) != 1 {
			Error(w, r, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func csrfCookie(r *http.Request) (string, bool) {
	var value string
	found := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == CSRFCookieName {
			found++
			value = cookie.Value
		}
	}
	return value, found == 1 && value != ""
}

// NewCSRFToken returns a 256-bit browser-readable double-submit token.
func NewCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read csrf token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
