package aiqa

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
