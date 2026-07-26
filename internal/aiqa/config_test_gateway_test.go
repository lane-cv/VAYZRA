package aiqa

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestConnectivityUsesMinimalSafePaidProbe(t *testing.T) {
	for _, protocol := range []ProtocolMode{ProtocolChatCompletions, ProtocolResponses} {
		t.Run(string(protocol), func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer probe-secret" {
					t.Errorf("request method=%s authorization=%q", r.Method, r.Header.Get("Authorization"))
				}
				if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if protocol == ProtocolChatCompletions {
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"private-upstream-text\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"private-upstream-text\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			}))
			defer server.Close()

			cfg := connectivityRuntimeConfig(t, server, protocol)
			key := cfg.APIKey
			result, err := NewProviderConnectivityTester(URLPolicy{
				DevelopmentAllowPrivate: true,
				Resolver:                connectivityResolver{address: netip.MustParseAddr("127.0.0.1")},
			}).Test(context.Background(), cfg)
			if err != nil || !result.OK || result.Protocol != protocol || result.ErrorCategory != "" {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if strings.Contains(fmt.Sprintf("%#v", result), "private-upstream-text") {
				t.Fatalf("result leaked upstream text: %#v", result)
			}
			for i, value := range key {
				if value != 0 {
					t.Fatalf("API key byte %d was not zeroed", i)
				}
			}
			assertMinimalConnectivityBody(t, protocol, body)
		})
	}
}

func TestConnectivityClassifiesAndRedactsProviderFailures(t *testing.T) {
	for _, tc := range []struct {
		status   int
		category string
	}{
		{http.StatusUnauthorized, "auth"},
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusInternalServerError, "upstream_5xx"},
	} {
		t.Run(tc.category, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "raw-upstream-secret-body", tc.status)
			}))
			defer server.Close()
			result, err := NewProviderConnectivityTester(URLPolicy{
				DevelopmentAllowPrivate: true,
				Resolver:                connectivityResolver{address: netip.MustParseAddr("127.0.0.1")},
			}).Test(context.Background(), connectivityRuntimeConfig(t, server, ProtocolResponses))
			if err == nil || result.OK || result.ErrorCategory != tc.category {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if strings.Contains(err.Error(), "raw-upstream-secret-body") || strings.Contains(fmt.Sprintf("%#v", result), "raw-upstream-secret-body") {
				t.Fatalf("raw body leaked: result=%#v err=%v", result, err)
			}
		})
	}
}

func TestConnectivityHonorsTotalTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	cfg := connectivityRuntimeConfig(t, server, ProtocolChatCompletions)
	cfg.Timeouts.Total = 20 * time.Millisecond
	result, err := NewProviderConnectivityTester(URLPolicy{
		DevelopmentAllowPrivate: true,
		Resolver:                connectivityResolver{address: netip.MustParseAddr("127.0.0.1")},
	}).Test(context.Background(), cfg)
	if err == nil || result.ErrorCategory != "timeout" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

type connectivityResolver struct {
	address netip.Addr
}

func (r connectivityResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{r.address}, nil
}

func connectivityRuntimeConfig(t *testing.T, server *httptest.Server, protocol ProtocolMode) RuntimeProviderConfig {
	t.Helper()
	raw, err := url.Parse(strings.Replace(server.URL, "127.0.0.1", "supplier.test", 1) + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	return RuntimeProviderConfig{
		ProviderID:   uuidForConnectivityTest,
		BaseURL:      raw,
		ProtocolMode: protocol,
		APIKey:       []byte("probe-secret"),
		Model:        ModelView{UpstreamModelID: "probe-model"},
		Timeouts:     GatewayTimeouts{Connect: time.Second, ResponseHeader: time.Second, IdleStream: time.Second, Total: time.Second},
	}
}

func assertMinimalConnectivityBody(t *testing.T, protocol ProtocolMode, body map[string]any) {
	t.Helper()
	if body["stream"] != true {
		t.Fatalf("stream=%#v", body["stream"])
	}
	maxKey := "max_tokens"
	if protocol == ProtocolResponses {
		maxKey = "max_output_tokens"
	}
	if got, ok := body[maxKey].(float64); !ok || got != 1 {
		t.Fatalf("%s=%#v", maxKey, body[maxKey])
	}
	encoded, _ := json.Marshal(body)
	text := string(encoded)
	if strings.Count(text, `"x"`) != 1 || strings.Contains(text, "private-upstream-text") || strings.Contains(text, "probe-secret") {
		t.Fatalf("probe body is not minimal/redacted: %s", text)
	}
}
