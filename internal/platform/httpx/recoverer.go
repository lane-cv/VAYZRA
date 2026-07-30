package httpx

import (
	"net/http"

	"happylearn.local/app/internal/platform/safelog"
)

// SafeRecoverer converts handler panics into HTTP 500 responses without
// formatting or logging the recovered value or stack. http.ErrAbortHandler is
// re-panicked so net/http can preserve its connection-abort semantics.
func SafeRecoverer(logger safelog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}

				fields := []safelog.Field{{
					Name:  "category",
					Value: "handler",
				}}
				if requestID := RequestIDFromContext(request.Context()); requestID != "" {
					fields = append(fields, safelog.Field{
						Name:  "request_id",
						Value: requestID,
					})
				}
				logger.Error("http.panic", fields...)
				writer.WriteHeader(http.StatusInternalServerError)
			}()

			next.ServeHTTP(writer, request)
		})
	}
}
