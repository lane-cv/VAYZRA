package aiqa

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSafeTransportRejectsRedirectToPrivateAddress(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer server.Close()

	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{
		"supplier.test":   {netip.MustParseAddr("127.0.0.1")},
		"169.254.169.254": {netip.MustParseAddr("169.254.169.254")},
	}}}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: time.Second})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	_, err := client.Get("http://supplier.test:" + port + "/")
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("got %v, want forbidden redirect error", err)
	}
}

func TestSafeTransportRejectsUnsafeInitialRequestShape(t *testing.T) {
	t.Parallel()
	resolver := &fakeResolver{answers: map[string][]netip.Addr{"supplier.test": {netip.MustParseAddr("93.184.216.34")}}}
	client := NewSafeHTTPClient(URLPolicy{Resolver: resolver}, GatewayTimeouts{Total: time.Second})
	for _, raw := range []string{
		"http://supplier.test/v1", "https://supplier.test:8443/v1", "https://user:pass@supplier.test/v1", "https://93.184.216.34/v1",
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
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{"supplier.test": {netip.MustParseAddr("127.0.0.1")}}}}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: time.Second})
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
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{
		"supplier.test": {netip.MustParseAddr("127.0.0.1")}, "other.test": {netip.MustParseAddr("127.0.0.1")},
	}}}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: time.Second})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	req, _ := http.NewRequest(http.MethodGet, "http://supplier.test:"+port+"/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected cross-origin redirect to be rejected")
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
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{"supplier.test": {netip.MustParseAddr("127.0.0.1")}}}}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: time.Second})
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
	secret := "do-not-log-me"
	policy := URLPolicy{Resolver: &fakeResolver{err: io.ErrUnexpectedEOF}}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: time.Second})
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

func TestSafeTransportDoesNotLeakQueryInRedirectError(t *testing.T) {
	t.Parallel()
	const secret = "redirect-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.test/next?token="+secret, http.StatusFound)
	}))
	defer server.Close()
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{
		"supplier.test": {netip.MustParseAddr("127.0.0.1")}, "other.test": {netip.MustParseAddr("127.0.0.1")},
	}}}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: time.Second})
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
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{"supplier.test": {netip.MustParseAddr("127.0.0.1")}}}}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: time.Second})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	_, err := client.Get("http://supplier.test:" + port + "/v1")
	if err == nil {
		t.Fatal("expected malformed redirect error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("malformed redirect leaked secret: %v", err)
	}
}

func TestSafeTransportPinsValidatedAddressForDial(t *testing.T) {
	t.Parallel()
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{"supplier.test": {netip.MustParseAddr("127.0.0.1")}}}}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: time.Second})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	response, err := client.Get("http://supplier.test:" + port + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if gotHost != "supplier.test:"+port {
		t.Fatalf("Host = %q, want supplier hostname", gotHost)
	}
}

func TestSafeTransportRevalidatesEveryNewRequest(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer server.Close()
	resolver := &fakeResolver{answers: map[string][]netip.Addr{"supplier.test": {netip.MustParseAddr("127.0.0.1")}}}
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: resolver}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: time.Second})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	url := "http://supplier.test:" + port + "/"
	for range 2 {
		response, err := client.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	if len(resolver.lookups) != 2 {
		t.Fatalf("lookups = %d, want 2", len(resolver.lookups))
	}
}

func TestSafeGatewayCompletedStreamPromptlyClosesOneShotConnection(t *testing.T) {
	closed := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	server.Start()
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	baseURL, err := url.Parse("http://supplier.test:" + port + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{
		answers: map[string][]netip.Addr{"supplier.test": {netip.MustParseAddr("127.0.0.1")}},
	}}
	cfg := RuntimeProviderConfig{
		BaseURL: baseURL, ProtocolMode: ProtocolChatCompletions, APIKey: []byte("secret"),
		Timeouts: GatewayTimeouts{
			Connect: time.Second, ResponseHeader: time.Second, IdleStream: 5 * time.Second, Total: time.Second,
		},
	}
	if err = NewSafeGateway(policy).Stream(context.Background(), cfg, testGatewayRequest(), func(GatewayEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("completed one-shot provider connection remained idle")
	}
}

func TestSafeTransportAppliesTotalTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	policy := URLPolicy{DevelopmentAllowPrivate: true, Resolver: &fakeResolver{answers: map[string][]netip.Addr{"supplier.test": {netip.MustParseAddr("127.0.0.1")}}}}
	client := NewSafeHTTPClient(policy, GatewayTimeouts{Total: 20 * time.Millisecond})
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	_, err := client.Get("http://supplier.test:" + port + "/")
	if err == nil {
		t.Fatal("expected total timeout")
	}
}
