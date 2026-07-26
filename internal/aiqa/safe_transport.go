package aiqa

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// GatewayTimeouts bounds supplier connections and streaming responses.
type GatewayTimeouts struct {
	Connect        time.Duration
	ResponseHeader time.Duration
	IdleStream     time.Duration
	Total          time.Duration
}

// NewSafeHTTPClient returns a client which resolves and pins a provider address
// immediately before each request. It intentionally does not inherit proxies.
func NewSafeHTTPClient(policy URLPolicy, timeouts GatewayTimeouts) *http.Client {
	if timeouts.Connect <= 0 {
		timeouts.Connect = 5 * time.Second
	}
	if timeouts.ResponseHeader <= 0 {
		timeouts.ResponseHeader = 30 * time.Second
	}
	if timeouts.IdleStream <= 0 {
		timeouts.IdleStream = 30 * time.Second
	}
	if timeouts.Total <= 0 {
		timeouts.Total = 120 * time.Second
	}

	return &http.Client{
		Transport: safeRoundTripper{policy: policy, timeouts: timeouts},
		Timeout:   timeouts.Total,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 3 {
				return fmt.Errorf("provider redirect limit exceeded")
			}
			_, err := policy.NormalizeBaseURL(req.Context(), req.URL.String())
			return err
		},
	}
}

type safeRoundTripper struct {
	policy   URLPolicy
	timeouts GatewayTimeouts
}

func (s safeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, fmt.Errorf("provider request has no URL")
	}
	if net.ParseIP(req.URL.Hostname()) != nil {
		return nil, fmt.Errorf("provider request must use a hostname")
	}
	addresses, err := s.policy.ValidateResolved(req.Context(), req.URL.Hostname())
	if err != nil {
		return nil, err
	}
	port := req.URL.Port()
	if port == "" {
		if req.URL.Scheme == "https" {
			port = "443"
		} else if req.URL.Scheme == "http" {
			port = "80"
		} else {
			return nil, fmt.Errorf("unsupported provider scheme %q", req.URL.Scheme)
		}
	}
	dialer := net.Dialer{Timeout: s.timeouts.Connect}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: s.timeouts.ResponseHeader,
		IdleConnTimeout:       s.timeouts.IdleStream,
	}
	response, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	response.Body = newIdleTimeoutBody(response.Body, s.timeouts.IdleStream)
	return response, nil
}

type idleTimeoutBody struct {
	body    io.ReadCloser
	timer   *time.Timer
	timeout time.Duration
	mu      sync.Mutex
	done    bool
}

func newIdleTimeoutBody(body io.ReadCloser, timeout time.Duration) *idleTimeoutBody {
	b := &idleTimeoutBody{body: body, timeout: timeout}
	b.timer = time.AfterFunc(timeout, func() { _ = b.Close() })
	return b
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return n, err
	}
	if err == nil && n > 0 {
		b.timer.Reset(b.timeout)
	} else if err != nil {
		b.done = true
		b.timer.Stop()
	}
	return n, err
}

func (b *idleTimeoutBody) Close() error {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return nil
	}
	b.done = true
	b.timer.Stop()
	b.mu.Unlock()
	return b.body.Close()
}
