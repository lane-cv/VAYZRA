package httpx

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"happylearn.local/app/internal/platform/safelog"
)

// SafeRequestLog records bounded request and response metadata. It never reads
// request headers, query parameters, cookies, bodies, remote addresses, or
// response bodies.
func SafeRequestLog(logger safelog.Logger, clock func() time.Time) func(http.Handler) http.Handler {
	if clock == nil {
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			startedAt := clock()
			method := request.Method
			path := ""
			if request.URL != nil {
				path = request.URL.EscapedPath()
			}
			requestID := RequestIDFromContext(request.Context())
			wrapped := middleware.NewWrapResponseWriter(writer, request.ProtoMajor)

			defer func() {
				status := wrapped.Status()
				if status == 0 {
					status = http.StatusOK
				}
				fields := []safelog.Field{
					{Name: "method", Value: method},
					{Name: "path", Value: path},
					{Name: "status", Value: status},
					{Name: "duration_ms", Value: clock().Sub(startedAt)},
					{Name: "bytes", Value: wrapped.BytesWritten()},
				}
				if requestID != "" {
					fields = append(fields, safelog.Field{
						Name:  "request_id",
						Value: requestID,
					})
				}
				logger.Info("http.request", fields...)
			}()

			next.ServeHTTP(wrapped, request)
		})
	}
}
