package operations

import (
	"net/http"

	"happylearn.local/app/internal/platform/httpx"
)

const logoutPath = "/api/v1/auth/logout"

func UnsafeWriteGate(gate WriteGate) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if safeMethod(r.Method) || r.URL.Path == logoutPath {
				next.ServeHTTP(w, r)
				return
			}
			if gate == nil {
				httpx.Error(
					w,
					r,
					http.StatusServiceUnavailable,
					"maintenance_mode",
					"系统维护中，请稍后重试",
				)
				return
			}
			release, err := gate.AcquireShared(r.Context())
			if err != nil || release == nil {
				httpx.Error(
					w,
					r,
					http.StatusServiceUnavailable,
					"maintenance_mode",
					"系统维护中，请稍后重试",
				)
				return
			}
			defer release()
			next.ServeHTTP(w, r)
		})
	}
}

func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
