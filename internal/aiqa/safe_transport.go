package aiqa

import (
	"net/http"
	"time"

	"happylearn.local/app/internal/platform/safehttp"
)

// GatewayTimeouts bounds supplier connections and streaming responses.
type GatewayTimeouts struct {
	Connect        time.Duration
	ResponseHeader time.Duration
	IdleStream     time.Duration
	Total          time.Duration
}

// NewSafeHTTPClient preserves the Phase 4 AI provider API while delegating all
// egress validation, address pinning, redirects, and timeouts to safehttp.
func NewSafeHTTPClient(policy URLPolicy, timeouts GatewayTimeouts) *http.Client {
	return safehttp.NewClient(policy.safeHTTPPolicy(), safehttp.ClientOptions{
		Timeouts: safehttp.Timeouts{
			Connect:        timeouts.Connect,
			ResponseHeader: timeouts.ResponseHeader,
			IdleStream:     timeouts.IdleStream,
			Total:          timeouts.Total,
		},
	})
}
