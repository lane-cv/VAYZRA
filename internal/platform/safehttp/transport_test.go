package safehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestSafeTransportRejectsRedirectToMetadataAddress(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer server.Close()

	policy := Policy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{
		"supplier.test":   {netip.MustParseAddr("127.0.0.1")},
		"169.254.169.254": {netip.MustParseAddr("169.254.169.254")},
	}}}
	client := NewClient(policy, ClientOptions{Timeouts: Timeouts{Total: time.Second}})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	_, err := client.Get("http://supplier.test:" + port + "/")
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("got %v, want forbidden redirect error", err)
	}
}

func TestSafeTransportRejectsUnsafeInitialRequestShape(t *testing.T) {
	t.Parallel()
	resolver := &fakeResolver{answers: map[string][]netip.Addr{
		"supplier.test": {netip.MustParseAddr("93.184.216.34")},
	}}
	client := NewClient(Policy{Resolver: resolver}, ClientOptions{Timeouts: Timeouts{Total: time.Second}})
	for _, raw := range []string{
		"http://supplier.test/v1",
		"https://supplier.test:8443/v1",
		"https://user:pass@supplier.test/v1",
		"https://93.184.216.34/v1",
	} {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = client.Do(req); err == nil {
			t.Fatalf("unsafe initial URL %q was accepted", raw)
		}
	}
}

func TestSafeTransportAllowsQueryOnInitialRequest(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("model") != "test" {
			t.Errorf("query was not forwarded")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	policy := loopbackPolicy("supplier.test")
	client := NewClient(policy, ClientOptions{Timeouts: Timeouts{Total: time.Second}})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	response, err := client.Get("http://supplier.test:" + port + "/v1?model=test")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestSafeTransportRejectsCrossOriginRedirect(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.test/next", http.StatusFound)
	}))
	defer server.Close()
	policy := Policy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{
		"supplier.test": {netip.MustParseAddr("127.0.0.1")},
		"other.test":    {netip.MustParseAddr("127.0.0.1")},
	}}}
	client := NewClient(policy, ClientOptions{Timeouts: Timeouts{Total: time.Second}})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	req, _ := http.NewRequest(http.MethodGet, "http://supplier.test:"+port+"/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected cross-origin redirect to be rejected")
	}
}

func TestSafeTransportCanRejectAllRedirectsForWebhooks(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer server.Close()
	client := NewClient(loopbackPolicy("webhook.test"), ClientOptions{
		Timeouts:  Timeouts{Total: time.Second},
		Redirects: RejectRedirects,
	})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	_, err := client.Get("http://webhook.test:" + port + "/start")
	if !errors.Is(err, ErrRedirectRejected) {
		t.Fatalf("got %v, want ErrRedirectRejected", err)
	}
}

func TestSafeTransportAllowsRelativeSameOriginRedirect(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer same-origin" {
			t.Errorf("same-origin credential was not preserved")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	client := NewClient(loopbackPolicy("supplier.test"), ClientOptions{Timeouts: Timeouts{Total: time.Second}})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	req, _ := http.NewRequest(http.MethodGet, "http://supplier.test:"+port+"/start", nil)
	req.Header.Set("Authorization", "Bearer same-origin")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestSafeTransportDoesNotLeakQueryInErrors(t *testing.T) {
	t.Parallel()
	const secret = "do-not-log-me"
	policy := Policy{Resolver: &fakeResolver{err: io.ErrUnexpectedEOF}}
	client := NewClient(policy, ClientOptions{Timeouts: Timeouts{Total: time.Second}})
	for _, raw := range []string{
		"https://supplier.test/v1?token=" + secret,
		"https://supplier.test/v1?token=" + secret + "#fragment",
	} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		_, err := client.Do(req)
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked query secret: %v", err)
		}
	}
}

func TestSafeTransportDoesNotLeakRedirectLocation(t *testing.T) {
	t.Parallel()
	const secret = "redirect-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.test/next?token="+secret, http.StatusFound)
	}))
	defer server.Close()
	policy := Policy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{
		"supplier.test": {netip.MustParseAddr("127.0.0.1")},
		"other.test":    {netip.MustParseAddr("127.0.0.1")},
	}}}
	client := NewClient(policy, ClientOptions{Timeouts: Timeouts{Total: time.Second}})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	_, err := client.Get("http://supplier.test:" + port + "/v1?token=" + secret)
	if err == nil {
		t.Fatal("expected redirect error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("redirect error leaked query secret: %v", err)
	}
}

func TestSafeTransportDoesNotLeakMalformedRedirectLocation(t *testing.T) {
	t.Parallel()
	const secret = "malformed-location-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://[::1?token="+secret)
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	client := NewClient(loopbackPolicy("supplier.test"), ClientOptions{Timeouts: Timeouts{Total: time.Second}})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	_, err := client.Get("http://supplier.test:" + port + "/v1")
	if err == nil {
		t.Fatal("expected malformed redirect error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("malformed redirect leaked secret: %v", err)
	}
}

func TestIdleTimeoutBodyIgnoresStaleTimerGeneration(t *testing.T) {
	body := newIdleTimeoutBody(io.NopCloser(strings.NewReader("active")), time.Hour)
	defer body.Close()
	body.mu.Lock()
	stale := body.generation
	body.generation++
	body.mu.Unlock()
	body.expire(stale)
	if _, err := body.Read(make([]byte, 1)); err != nil {
		t.Fatalf("stale timer closed active stream: %v", err)
	}
}

func TestSafeTransportPinsValidatedAddressAndRejectsRebinding(t *testing.T) {
	t.Parallel()
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	resolver := &sequenceResolver{answers: [][]netip.Addr{
		{netip.MustParseAddr("127.0.0.1")},
		{netip.MustParseAddr("10.0.0.1")},
	}}
	policy := Policy{DevelopmentAllowPrivate: true, Resolver: resolver}
	client := NewClient(policy, ClientOptions{Timeouts: Timeouts{Total: time.Second}})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	target := "http://supplier.test:" + port + "/v1"

	response, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if gotHost != "supplier.test:"+port {
		t.Fatalf("Host = %q, want supplier hostname", gotHost)
	}
	if _, err = client.Get(target); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("second request got %v, want rebinding rejection", err)
	}
	if resolver.lookups != 2 {
		t.Fatalf("lookups = %d, want 2", resolver.lookups)
	}
}

func TestTransportLifecycleBodyCloseIsIdempotentAndReleasesReferences(t *testing.T) {
	underlying := &lifecycleBodyStub{}
	body := &transportLifecycleBody{body: underlying, transport: &http.Transport{}}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if underlying.closes != 1 || body.body != nil || body.transport != nil {
		t.Fatalf("closes=%d body_retained=%t transport_retained=%t",
			underlying.closes, body.body != nil, body.transport != nil)
	}
	if _, err := body.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read after close=%v", err)
	}
}

func TestSafeTransportBoundsResponseBody(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "12345")
	}))
	defer server.Close()
	client := NewClient(loopbackPolicy("webhook.test"), ClientOptions{
		Timeouts:         Timeouts{Total: time.Second},
		MaxResponseBytes: 4,
	})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	response, err := client.Get("http://webhook.test:" + port)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("read error = %v, want ErrResponseTooLarge", err)
	}
	if len(body) > 4 {
		t.Fatalf("exposed %d response bytes, want at most 4", len(body))
	}
}

func TestSafeTransportAllowsResponseAtExactLimit(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "1234")
	}))
	defer server.Close()
	client := NewClient(loopbackPolicy("webhook.test"), ClientOptions{
		Timeouts:         Timeouts{Total: time.Second},
		MaxResponseBytes: 4,
	})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	response, err := client.Get("http://webhook.test:" + port)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "1234" {
		t.Fatalf("body = %q", body)
	}
}

func TestSafeTransportRequiresTLS12OrNewer(t *testing.T) {
	t.Parallel()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS11, MaxVersion: tls.VersionTLS11}
	server.StartTLS()
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client := NewClient(Policy{
		DevelopmentAllowPrivate: true,
		Resolver: &fakeResolver{answers: map[string][]netip.Addr{
			"example.com": {netip.MustParseAddr("127.0.0.1")},
		}},
	}, ClientOptions{
		Timeouts: Timeouts{Total: time.Second},
		RootCAs:  roots,
	})
	port := strings.TrimPrefix(server.URL, "https://127.0.0.1:")
	if _, err := client.Get("https://example.com:" + port); err == nil {
		t.Fatal("TLS 1.1 endpoint was accepted")
	}
}

func TestSafeTransportAppliesResponseHeaderAndTotalTimeouts(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			_, _ = io.WriteString(w, "late")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")

	for _, tt := range []struct {
		name     string
		timeouts Timeouts
	}{
		{"response header", Timeouts{ResponseHeader: 20 * time.Millisecond, Total: time.Second}},
		{"total", Timeouts{ResponseHeader: time.Second, Total: 20 * time.Millisecond}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(loopbackPolicy("supplier.test"), ClientOptions{Timeouts: tt.timeouts})
			if _, err := client.Get("http://supplier.test:" + port); err == nil {
				t.Fatal("expected timeout")
			}
		})
	}
}

func TestSafeTransportSharesAbsoluteConnectDeadlineWithResolverAndDial(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		connect time.Duration
		want    time.Duration
	}{
		{name: "default", want: 5 * time.Second},
		{name: "custom", connect: 80 * time.Millisecond, want: 80 * time.Millisecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &deadlineResolver{answer: netip.MustParseAddr("127.0.0.1")}
			var dialDeadline time.Time
			client := newClientWithDialContext(
				Policy{DevelopmentAllowPrivate: true, Resolver: resolver},
				ClientOptions{Timeouts: Timeouts{Connect: tt.connect, Total: 30 * time.Second}},
				func(ctx context.Context, _, _ string) (net.Conn, error) {
					var ok bool
					dialDeadline, ok = ctx.Deadline()
					if !ok {
						t.Error("dial context had no deadline")
					}
					return nil, errors.New("dial stopped")
				},
			)
			started := time.Now()
			if _, err := client.Get("http://supplier.test/"); err == nil {
				t.Fatal("expected dial error")
			}
			if !resolver.has {
				t.Fatal("resolver context had no deadline")
			}
			if !resolver.deadline.Equal(dialDeadline) {
				t.Fatalf("resolver deadline %s != dial deadline %s", resolver.deadline, dialDeadline)
			}
			remaining := resolver.deadline.Sub(started)
			if remaining < tt.want-20*time.Millisecond || remaining > tt.want+500*time.Millisecond {
				t.Fatalf("connect deadline remaining = %s, want about %s", remaining, tt.want)
			}
		})
	}
}

func TestNormalizeTimeoutsBoundsEveryNetworkStage(t *testing.T) {
	got := normalizeTimeouts(Timeouts{})
	if got.Connect != 5*time.Second ||
		got.ResponseHeader != 30*time.Second ||
		got.TLSHandshake != 5*time.Second ||
		got.IdleStream != 30*time.Second ||
		got.Total != 120*time.Second {
		t.Fatalf("unexpected defaults: %+v", got)
	}
}

func TestResponseLimitBodyMaxInt64DoesNotOverflow(t *testing.T) {
	body := &responseLimitBody{
		body:      io.NopCloser(strings.NewReader("x")),
		remaining: math.MaxInt64,
	}
	buffer := make([]byte, 1)
	n, err := body.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || buffer[0] != 'x' {
		t.Fatalf("read n=%d buffer=%q", n, buffer[:n])
	}
}

type lifecycleBodyStub struct {
	closes int
}

func (*lifecycleBodyStub) Read([]byte) (int, error) { return 0, io.EOF }
func (b *lifecycleBodyStub) Close() error {
	b.closes++
	return nil
}

func loopbackPolicy(host string) Policy {
	return Policy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{
		host: {netip.MustParseAddr("127.0.0.1")},
	}}}
}
