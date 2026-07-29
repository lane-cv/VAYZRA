package safehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

var (
	errInvalidRedirect = errors.New("invalid outbound redirect")

	// ErrRedirectRejected reports a response redirect forbidden by client policy.
	ErrRedirectRejected = errors.New("outbound redirect rejected")
	// ErrResponseTooLarge reports a response that exceeded MaxResponseBytes.
	ErrResponseTooLarge = errors.New("outbound response exceeds limit")
)

// Timeouts bounds every network stage and the complete request.
type Timeouts struct {
	Connect        time.Duration
	ResponseHeader time.Duration
	TLSHandshake   time.Duration
	IdleStream     time.Duration
	Total          time.Duration
}

// RedirectPolicy controls whether the client follows validated redirects.
type RedirectPolicy uint8

const (
	// FollowSameOriginRedirects preserves the Phase 4 AI provider behavior.
	FollowSameOriginRedirects RedirectPolicy = iota
	// RejectRedirects rejects every redirect, including same-origin redirects.
	RejectRedirects
)

// ClientOptions configures the bounded outbound client.
type ClientOptions struct {
	Timeouts         Timeouts
	Redirects        RedirectPolicy
	MaxResponseBytes int64
	RootCAs          *x509.CertPool
}

// NewClient returns a client that resolves and pins an address immediately
// before each request. It never inherits proxy settings.
func NewClient(policy Policy, options ClientOptions) *http.Client {
	return newClientWithDialContext(policy, options, nil)
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

func newClientWithDialContext(policy Policy, options ClientOptions, dialContext dialContextFunc) *http.Client {
	options.Timeouts = normalizeTimeouts(options.Timeouts)
	return &http.Client{
		Transport: safeRoundTripper{policy: policy, options: options, dialContext: dialContext},
		Timeout:   options.Timeouts.Total,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if options.Redirects == RejectRedirects {
				scrubRequestURL(req)
				scrubRequests(via)
				return ErrRedirectRejected
			}
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
			if _, err := policy.ValidateRequestURL(req.Context(), req.URL); err != nil {
				scrubRequestURL(req)
				scrubRequests(via)
				return err
			}
			return nil
		},
	}
}

func normalizeTimeouts(timeouts Timeouts) Timeouts {
	if timeouts.Connect <= 0 {
		timeouts.Connect = defaultConnectTimeout
	}
	if timeouts.ResponseHeader <= 0 {
		timeouts.ResponseHeader = 30 * time.Second
	}
	if timeouts.TLSHandshake <= 0 {
		timeouts.TLSHandshake = 5 * time.Second
	}
	if timeouts.IdleStream <= 0 {
		timeouts.IdleStream = 30 * time.Second
	}
	if timeouts.Total <= 0 {
		timeouts.Total = 120 * time.Second
	}
	return timeouts
}

type safeRoundTripper struct {
	policy      Policy
	options     ClientOptions
	dialContext dialContextFunc
}

func (s safeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	connectCtx, cancelConnect := context.WithTimeout(req.Context(), s.options.Timeouts.Connect)
	defer cancelConnect()
	connectDeadline, _ := connectCtx.Deadline()

	addresses, err := s.policy.ValidateRequestURL(connectCtx, req.URL)
	if err != nil {
		scrubRequestURL(req)
		return nil, err
	}
	port := req.URL.Port()
	if port == "" {
		switch req.URL.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			return nil, fmt.Errorf("unsupported provider scheme %q", req.URL.Scheme)
		}
	}
	timeouts := s.options.Timeouts
	dialContext := s.dialContext
	if dialContext == nil {
		dialer := &net.Dialer{}
		dialContext = dialer.DialContext
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialCtx, cancelDial := context.WithDeadline(ctx, connectDeadline)
			defer cancelDial()
			return dialContext(dialCtx, network, net.JoinHostPort(addresses[0].String(), port))
		},
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: s.options.RootCAs},
		TLSHandshakeTimeout:   timeouts.TLSHandshake,
		ResponseHeaderTimeout: timeouts.ResponseHeader,
		IdleConnTimeout:       timeouts.IdleStream,
	}
	response, err := transport.RoundTrip(req)
	if err != nil {
		transport.CloseIdleConnections()
		scrubRequestURL(req)
		return nil, err
	}
	response.Body = &transportLifecycleBody{
		body:      newIdleTimeoutBody(response.Body, timeouts.IdleStream),
		transport: transport,
	}
	if s.options.MaxResponseBytes > 0 {
		response.Body = &responseLimitBody{
			body:      response.Body,
			remaining: s.options.MaxResponseBytes,
		}
	}
	if isRedirect(response.StatusCode) {
		if s.options.Redirects == RejectRedirects {
			_ = response.Body.Close()
			scrubRequestURL(req)
			return nil, ErrRedirectRejected
		}
		if err := s.validateRedirect(req, response); err != nil {
			_ = response.Body.Close()
			scrubRequestURL(req)
			return nil, err
		}
	}
	return response, nil
}

type transportLifecycleBody struct {
	mu        sync.Mutex
	body      io.ReadCloser
	transport *http.Transport
	once      sync.Once
	err       error
}

func (b *transportLifecycleBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	body := b.body
	b.mu.Unlock()
	if body == nil {
		return 0, net.ErrClosed
	}
	return body.Read(p)
}

func (b *transportLifecycleBody) Close() error {
	b.once.Do(func() {
		b.mu.Lock()
		body, transport := b.body, b.transport
		b.body, b.transport = nil, nil
		b.mu.Unlock()
		b.err = body.Close()
		transport.CloseIdleConnections()
	})
	return b.err
}

func (s safeRoundTripper) validateRedirect(req *http.Request, response *http.Response) error {
	location := response.Header.Get("Location")
	if location == "" {
		return nil
	}
	next, err := req.URL.Parse(location)
	if err != nil {
		return errInvalidRedirect
	}
	if _, err := s.policy.ValidateRequestURL(req.Context(), next); err != nil {
		return err
	}
	if !sameOrigin(req.URL, next) {
		return fmt.Errorf("cross-origin provider redirect rejected")
	}
	return nil
}

func isRedirect(status int) bool {
	return status == http.StatusMovedPermanently ||
		status == http.StatusFound ||
		status == http.StatusSeeOther ||
		status == http.StatusTemporaryRedirect ||
		status == http.StatusPermanentRedirect
}

func sameOrigin(left, right *url.URL) bool {
	return left.Scheme == right.Scheme &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		originPort(left) == originPort(right)
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

type responseLimitBody struct {
	body      io.ReadCloser
	remaining int64
	exceeded  bool
}

func (b *responseLimitBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.exceeded {
		return 0, ErrResponseTooLarge
	}
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.body.Read(extra[:])
		if n > 0 {
			b.exceeded = true
			_ = b.body.Close()
			return 0, ErrResponseTooLarge
		}
		return 0, err
	}
	readSize := len(p)
	if b.remaining < int64(readSize) {
		readSize = int(b.remaining) + 1
	}
	n, err := b.body.Read(p[:readSize])
	if int64(n) > b.remaining {
		exposed := int(b.remaining)
		b.remaining = 0
		b.exceeded = true
		_ = b.body.Close()
		return exposed, ErrResponseTooLarge
	}
	b.remaining -= int64(n)
	return n, err
}

func (b *responseLimitBody) Close() error {
	return b.body.Close()
}
