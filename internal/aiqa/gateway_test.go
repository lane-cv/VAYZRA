package aiqa

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testRuntimeConfig(t *testing.T, server *httptest.Server, protocol ProtocolMode) RuntimeProviderConfig {
	t.Helper()
	base, err := url.Parse(server.URL + "/v1/")
	if err != nil {
		t.Fatal(err)
	}
	return RuntimeProviderConfig{
		BaseURL:      base,
		ProtocolMode: protocol,
		APIKey:       []byte("test-secret"),
		Timeouts: GatewayTimeouts{
			IdleStream: 50 * time.Millisecond,
			Total:      time.Second,
		},
	}
}

func testGatewayRequest() GatewayRequest {
	return GatewayRequest{
		RunID:           uuid.New(),
		Model:           "compatible-model",
		SystemPrompt:    "Be concise.",
		Turns:           []GatewayTurn{{Role: "student", Text: "Explain inertia."}, {Role: "assistant", Text: "It resists change."}, {Role: "student", Text: "How does mass affect it?"}},
		MaxOutputTokens: 321,
	}
}

func TestGatewayDispatchesProtocols(t *testing.T) {
	for _, protocol := range []ProtocolMode{ProtocolChatCompletions, ProtocolResponses} {
		t.Run(string(protocol), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if protocol == ProtocolChatCompletions {
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
				} else {
					_, _ = w.Write([]byte("event: response.completed\ndata: {\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n"))
				}
			}))
			defer server.Close()

			err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, protocol), testGatewayRequest(), func(GatewayEvent) error { return nil })
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
		})
	}
}

func TestGatewayRejectsUnsupportedProtocolWithoutNetwork(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	cfg := testRuntimeConfig(t, server, ProtocolMode("other"))
	err := NewGateway(server.Client()).Stream(context.Background(), cfg, testGatewayRequest(), func(GatewayEvent) error { return nil })
	if categoryOf(err) != "upstream_4xx" {
		t.Fatalf("category=%q err=%v", categoryOf(err), err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server hits=%d", hits.Load())
	}
}

func TestGatewayErrorsExposeOnlyStableCategory(t *testing.T) {
	secretBody := "provider-secret-diagnostic"
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(strings.Repeat(secretBody, 10000)))
			}))
			defer server.Close()
			err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolChatCompletions), testGatewayRequest(), func(GatewayEvent) error { return nil })
			want := "upstream_4xx"
			if status == http.StatusUnauthorized {
				want = "auth"
			} else if status == http.StatusTooManyRequests {
				want = "rate_limited"
			} else if status >= 500 {
				want = "upstream_5xx"
			}
			if categoryOf(err) != want {
				t.Fatalf("category=%q want=%q err=%v", categoryOf(err), want, err)
			}
			if strings.Contains(err.Error(), secretBody) {
				t.Fatalf("raw upstream body leaked: %v", err)
			}
		})
	}
}

func TestGatewayCancellationAndCallbackFailure(t *testing.T) {
	callbackErr := errors.New("consumer stopped")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
	}))
	defer server.Close()
	err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolChatCompletions), testGatewayRequest(), func(GatewayEvent) error { return callbackErr })
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error not preserved: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = NewGateway(server.Client()).Stream(ctx, testRuntimeConfig(t, server, ProtocolChatCompletions), testGatewayRequest(), func(GatewayEvent) error { return nil })
	if categoryOf(err) != "cancelled" {
		t.Fatalf("category=%q err=%v", categoryOf(err), err)
	}
}

func TestGatewayRetriesExactlyOnceOnlyBeforeWriteAndReopensImages(t *testing.T) {
	var attempts atomic.Int32
	var opens atomic.Int32
	var closes atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		if attempt == 1 {
			_, _ = io.Copy(io.Discard, request.Body)
			return nil, errors.New("dial failed")
		}
		_, err := io.Copy(io.Discard, request.Body)
		if err != nil {
			return nil, err
		}
		return sseResponse("data: [DONE]\n\n"), nil
	})}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	request := testGatewayRequest()
	request.Images = []GatewayImage{{
		MediaType: "image/png",
		Size:      3,
		Open: func(context.Context) (io.ReadCloser, error) {
			opens.Add(1)
			return &countingCloser{Reader: strings.NewReader("img"), closes: &closes}, nil
		},
	}}
	if err := NewGateway(client).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolChatCompletions), request, func(GatewayEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || opens.Load() != 2 || closes.Load() != 2 {
		t.Fatalf("attempts=%d opens=%d closes=%d", attempts.Load(), opens.Load(), closes.Load())
	}
}

func TestGatewayPostWriteEOFIsNotRetriedAndResponseBodyCloses(t *testing.T) {
	var attempts atomic.Int32
	var closed atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts.Add(1)
		if trace := httptrace.ContextClientTrace(request.Context()); trace != nil && trace.WroteRequest != nil {
			trace.WroteRequest(httptrace.WroteRequestInfo{})
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       &errorReadCloser{err: io.ErrUnexpectedEOF, closed: &closed},
			Request:    request,
		}, nil
	})}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	err := NewGateway(client).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolChatCompletions), testGatewayRequest(), func(GatewayEvent) error { return nil })
	if categoryOf(err) != "stream_interrupted" || attempts.Load() != 1 || !closed.Load() {
		t.Fatalf("category=%q attempts=%d closed=%v err=%v", categoryOf(err), attempts.Load(), closed.Load(), err)
	}
}

func TestGatewayIdleTimeout(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	cfg := testRuntimeConfig(t, server, ProtocolChatCompletions)
	cfg.Timeouts.IdleStream = 20 * time.Millisecond
	err := NewGateway(server.Client()).Stream(context.Background(), cfg, testGatewayRequest(), func(GatewayEvent) error { return nil })
	<-started
	if categoryOf(err) != "timeout" {
		t.Fatalf("category=%q err=%v", categoryOf(err), err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type countingCloser struct {
	io.Reader
	closes *atomic.Int32
}

func (closer *countingCloser) Close() error {
	closer.closes.Add(1)
	return nil
}

type errorReadCloser struct {
	err    error
	closed *atomic.Bool
}

func (reader *errorReadCloser) Read([]byte) (int, error) { return 0, reader.err }
func (reader *errorReadCloser) Close() error {
	reader.closed.Store(true)
	return nil
}

func categoryOf(err error) string {
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) {
		return gatewayErr.Category
	}
	return ""
}
