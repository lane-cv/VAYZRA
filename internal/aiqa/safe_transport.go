package aiqa

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var errInvalidProviderRedirect = errors.New("invalid provider redirect")

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
				scrubRequestURL(req)
				scrubRequests(via)
				return fmt.Errorf("provider redirect limit exceeded")
			}
			if len(via) > 0 && !sameOrigin(via[0].URL, req.URL) {
				scrubRequestURL(req)
				scrubRequests(via)
				return fmt.Errorf("cross-origin provider redirect rejected")
			}
			if _, err := policy.validateRequestURL(req.Context(), req.URL); err != nil {
				scrubRequestURL(req)
				scrubRequests(via)
				return err
			}
			return nil
		},
	}
}

type safeRoundTripper struct {
	policy   URLPolicy
	timeouts GatewayTimeouts
}

func (s safeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	addresses, err := s.policy.validateRequestURL(req.Context(), req.URL)
	if err != nil {
		scrubRequestURL(req)
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
		Proxy:             nil,
		DisableKeepAlives: true,
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
		scrubRequestURL(req)
		return nil, err
	}
	if isRedirect(response.StatusCode) {
		if err := s.validateRedirect(req, response); err != nil {
			_ = response.Body.Close()
			scrubRequestURL(req)
			return nil, err
		}
	}
	response.Body = newIdleTimeoutBody(response.Body, s.timeouts.IdleStream)
	return response, nil
}

func (s safeRoundTripper) validateRedirect(req *http.Request, response *http.Response) error {
	location := response.Header.Get("Location")
	if location == "" {
		return nil
	}
	next, err := req.URL.Parse(location)
	if err != nil {
		return errInvalidProviderRedirect
	}
	if _, err := s.policy.validateRequestURL(req.Context(), next); err != nil {
		return err
	}
	if !sameOrigin(req.URL, next) {
		return fmt.Errorf("cross-origin provider redirect rejected")
	}
	return nil
}

func isRedirect(status int) bool {
	return status == http.StatusMovedPermanently || status == http.StatusFound || status == http.StatusSeeOther || status == http.StatusTemporaryRedirect || status == http.StatusPermanentRedirect
}

func sameOrigin(left, right *url.URL) bool {
	return left.Scheme == right.Scheme && strings.EqualFold(left.Hostname(), right.Hostname()) && originPort(left) == originPort(right)
}

func originPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func scrubRequestURL(req *http.Request) {
	if req == nil || req.URL == nil {
		return
	}
	req.URL.RawQuery = ""
	req.URL.ForceQuery = false
	req.URL.Fragment = ""
}

func scrubRequests(requests []*http.Request) {
	for _, req := range requests {
		scrubRequestURL(req)
	}
}

type idleTimeoutBody struct {
	body       io.ReadCloser
	timer      *time.Timer
	timeout    time.Duration
	mu         sync.Mutex
	done       bool
	generation uint64
}

func newIdleTimeoutBody(body io.ReadCloser, timeout time.Duration) *idleTimeoutBody {
	b := &idleTimeoutBody{body: body, timeout: timeout}
	b.scheduleLocked()
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
		b.generation++
		b.timer.Stop()
		b.scheduleLocked()
	} else if err != nil {
		b.done = true
		b.timer.Stop()
	}
	return n, err
}

func (b *idleTimeoutBody) scheduleLocked() {
	generation := b.generation
	b.timer = time.AfterFunc(b.timeout, func() { b.expire(generation) })
}

func (b *idleTimeoutBody) expire(generation uint64) {
	b.mu.Lock()
	if b.done || generation != b.generation {
		b.mu.Unlock()
		return
	}
	b.done = true
	b.mu.Unlock()
	_ = b.body.Close()
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
