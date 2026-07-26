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
			Total:      10 * time.Second,
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

func TestGatewayProtocolTransportMatrix(t *testing.T) {
	protocols := []ProtocolMode{ProtocolChatCompletions, ProtocolResponses}
	for _, protocol := range protocols {
		t.Run(string(protocol), func(t *testing.T) {
			t.Run("status redaction and body cap", func(t *testing.T) {
				for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadRequest, http.StatusInternalServerError} {
					var readBytes atomic.Int64
					var closed atomic.Bool
					client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: status,
							Header:     make(http.Header),
							Body: &countingReadCloser{
								Reader: strings.NewReader(strings.Repeat("provider-secret", 1000)),
								read:   &readBytes,
								closed: &closed,
							},
							Request: request,
						}, nil
					})}
					server := httptest.NewServer(http.NotFoundHandler())
					defer server.Close()
					err := NewGateway(client).Stream(context.Background(), testRuntimeConfig(t, server, protocol), testGatewayRequest(), func(GatewayEvent) error { return nil })
					want := "upstream_4xx"
					switch status {
					case http.StatusUnauthorized:
						want = "auth"
					case http.StatusTooManyRequests:
						want = "rate_limited"
					case http.StatusInternalServerError:
						want = "upstream_5xx"
					}
					if categoryOf(err) != want || strings.Contains(err.Error(), "provider-secret") {
						t.Fatalf("status=%d category=%q err=%v", status, categoryOf(err), err)
					}
					if readBytes.Load() > maxUpstreamErrorBytes || !closed.Load() {
						t.Fatalf("status=%d read=%d closed=%v", status, readBytes.Load(), closed.Load())
					}
				}
			})

			t.Run("idle timeout", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					<-request.Context().Done()
				}))
				defer server.Close()
				cfg := testRuntimeConfig(t, server, protocol)
				cfg.Timeouts.IdleStream = 15 * time.Millisecond
				cfg.Timeouts.Total = time.Second
				err := NewGateway(server.Client()).Stream(context.Background(), cfg, testGatewayRequest(), func(GatewayEvent) error { return nil })
				if categoryOf(err) != "timeout" {
					t.Fatalf("category=%q err=%v", categoryOf(err), err)
				}
			})

			t.Run("total timeout beats valid stream bytes", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					flusher := w.(http.Flusher)
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-request.Context().Done():
							return
						case <-ticker.C:
							if protocol == ProtocolChatCompletions {
								_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\r\n\r\n")
							} else {
								_, _ = io.WriteString(w, "event: response.output_text.delta\r\ndata: {\"delta\":\"x\"}\r\n\r\n")
							}
							flusher.Flush()
						}
					}
				}))
				defer server.Close()
				cfg := testRuntimeConfig(t, server, protocol)
				cfg.Timeouts.IdleStream = 50 * time.Millisecond
				cfg.Timeouts.Total = 30 * time.Millisecond
				err := NewGateway(server.Client()).Stream(context.Background(), cfg, testGatewayRequest(), func(GatewayEvent) error { return nil })
				if categoryOf(err) != "timeout" {
					t.Fatalf("category=%q err=%v", categoryOf(err), err)
				}
			})

			t.Run("caller cancellation", func(t *testing.T) {
				server := httptest.NewServer(http.NotFoundHandler())
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				err := NewGateway(server.Client()).Stream(ctx, testRuntimeConfig(t, server, protocol), testGatewayRequest(), func(GatewayEvent) error { return nil })
				if categoryOf(err) != "cancelled" {
					t.Fatalf("category=%q err=%v", categoryOf(err), err)
				}
			})

			t.Run("callback failure", func(t *testing.T) {
				callbackErr := errors.New("consumer failed")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if protocol == ProtocolChatCompletions {
						_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n")
					} else {
						_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"delta\":\"x\"}\n\nevent: response.completed\ndata: {\"response\":{\"status\":\"completed\"}}\n\n")
					}
				}))
				defer server.Close()
				err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, protocol), testGatewayRequest(), func(GatewayEvent) error {
					return callbackErr
				})
				if categoryOf(err) != "stream_interrupted" || !errors.Is(err, callbackErr) {
					t.Fatalf("category=%q err=%v", categoryOf(err), err)
				}
			})

			t.Run("split frames and CRLF", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					flusher := w.(http.Flusher)
					var parts []string
					if protocol == ProtocolChatCompletions {
						parts = []string{": comment\r\n\r\n", "data: {\"choices\":[{\"delta\":{\"content\":\"sp", "lit\"}}]}\r\n\r\n", "data: [DONE]\r\n\r\n"}
					} else {
						parts = []string{": comment\r\n\r\n", "event: response.output_text.delta\r\ndata: {\"delta\":\"sp", "lit\"}\r\n\r\n", "event: response.completed\r\ndata: {\"response\":{\"status\":\"completed\"}}\r\n\r\n"}
					}
					for _, part := range parts {
						_, _ = io.WriteString(w, part)
						flusher.Flush()
					}
				}))
				defer server.Close()
				var answer strings.Builder
				err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, protocol), testGatewayRequest(), func(event GatewayEvent) error {
					answer.WriteString(event.Delta)
					return nil
				})
				if err != nil || answer.String() != "split" {
					t.Fatalf("answer=%q err=%v", answer.String(), err)
				}
			})

			t.Run("event cap", func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+strings.Repeat("x", MaxGatewayEventBytes+1)+"\n\n")
				}))
				defer server.Close()
				err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, protocol), testGatewayRequest(), func(GatewayEvent) error { return nil })
				if categoryOf(err) != "response_too_large" {
					t.Fatalf("category=%q err=%v", categoryOf(err), err)
				}
			})
		})
	}
}

func TestGatewayRejectsUnapprovedImagesBeforeNetwork(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		mediaType string
		size      int64
	}{
		{name: "svg", mediaType: "image/svg+xml", size: 1},
		{name: "parameterized", mediaType: "image/png; charset=utf-8", size: 1},
		{name: "vendor", mediaType: "image/vnd.microsoft.icon", size: 1},
		{name: "zero length", mediaType: "image/png", size: 0},
		{name: "negative length", mediaType: "image/png", size: -1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer server.Close()
			request := testGatewayRequest()
			request.Images = []GatewayImage{{
				MediaType: testCase.mediaType,
				Size:      testCase.size,
				Open: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("x")), nil
				},
			}}
			err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolChatCompletions), request, func(GatewayEvent) error { return nil })
			if categoryOf(err) != "upstream_4xx" || hits.Load() != 0 {
				t.Fatalf("category=%q hits=%d err=%v", categoryOf(err), hits.Load(), err)
			}
		})
	}
}

func TestGatewayImageMIMEAllowlistIsExact(t *testing.T) {
	for _, mediaType := range []string{"image/jpeg", "image/png", "image/webp", "image/gif"} {
		if !isAllowedGatewayImageType(mediaType) {
			t.Fatalf("approved media type rejected: %q", mediaType)
		}
	}
	for _, mediaType := range []string{"image/svg+xml", "image/png; charset=utf-8", "image/vnd.microsoft.icon", "IMAGE/PNG", "text/plain"} {
		if isAllowedGatewayImageType(mediaType) {
			t.Fatalf("unapproved media type accepted: %q", mediaType)
		}
	}
}

func TestGatewayRejectsNonSSESuccessForBothProtocols(t *testing.T) {
	for _, protocol := range []ProtocolMode{ProtocolChatCompletions, ProtocolResponses} {
		t.Run(string(protocol), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"secret":"provider-detail"}`)
			}))
			defer server.Close()
			err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, protocol), testGatewayRequest(), func(GatewayEvent) error { return nil })
			if categoryOf(err) != "malformed_stream" || strings.Contains(err.Error(), "provider-detail") {
				t.Fatalf("category=%q err=%v", categoryOf(err), err)
			}
		})
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

type countingReadCloser struct {
	io.Reader
	read   *atomic.Int64
	closed *atomic.Bool
}

func (reader *countingReadCloser) Read(buffer []byte) (int, error) {
	n, err := reader.Reader.Read(buffer)
	reader.read.Add(int64(n))
	return n, err
}

func (reader *countingReadCloser) Close() error {
	reader.closed.Store(true)
	return nil
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
