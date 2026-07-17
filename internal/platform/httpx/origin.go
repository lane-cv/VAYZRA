package httpx

import (
	"net/http"
	"net/url"
	"strings"
)

// OriginGuard permits safe requests and requires every unsafe request to come
// from the configured public origin. A missing Origin is allowed only when an
// unambiguous Referer has the same origin.
func OriginGuard(publicOrigin string) func(http.Handler) http.Handler {
	expected, ok := parseOrigin(publicOrigin, false)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			if !ok || !requestHasExpectedOrigin(r, expected) {
				Error(w, r, http.StatusForbidden, "forbidden", "请求来源不被允许")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type origin struct {
	scheme string
	host   string
	port   string
}

func requestHasExpectedOrigin(r *http.Request, expected origin) bool {
	if values := r.Header.Values("Origin"); len(values) > 0 {
		if len(values) != 1 {
			return false
		}
		actual, ok := parseOrigin(values[0], true)
		return ok && actual == expected
	}
	values := r.Header.Values("Referer")
	if len(values) != 1 {
		return false
	}
	actual, ok := parseOrigin(values[0], false)
	return ok && actual == expected
}

func parseOrigin(value string, originOnly bool) (origin, bool) {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return origin{}, false
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return origin{}, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return origin{}, false
	}
	if originOnly && (u.Path != "" || u.RawQuery != "") {
		return origin{}, false
	}
	if u.Hostname() == "" {
		return origin{}, false
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return origin{scheme: u.Scheme, host: strings.ToLower(u.Hostname()), port: port}, true
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
