package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxRequestBodyBytes = 64 << 10
	requiredBearer      = "Bearer e2e-provider-key"
	defaultSlowDelay    = 30 * time.Second
)

var allowedCases = []string{
	"success",
	"slow-first-byte",
	"idle-timeout",
	"disconnect-after-delta",
	"malformed-event",
	"no-usage",
	"usage-over-reservation",
	"429",
	"500",
}

type providerOptions struct {
	slowDelay time.Duration
	logger    *log.Logger
}

type provider struct {
	slowDelay time.Duration
	logger    *log.Logger
	mu        sync.Mutex
	counts    map[string]int
}

func main() {
	listen := flag.String("listen", ":8090", "HTTP listen address")
	flag.Parse()

	logger := log.New(os.Stderr, "fake-ai-provider: ", log.LstdFlags)
	server := &http.Server{
		Addr:              *listen,
		Handler:           newProviderHandler(providerOptions{logger: logger}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	shutdownContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownContext.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	logger.Printf("listening address=%s", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Printf("server stopped category=listen")
		os.Exit(1)
	}
}

func newProviderHandler(options providerOptions) http.Handler {
	delay := options.slowDelay
	if delay <= 0 {
		delay = defaultSlowDelay
	}
	logger := options.logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	instance := &provider{
		slowDelay: delay,
		logger:    logger,
		counts:    make(map[string]int),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", instance.health)
	mux.HandleFunc("GET /test/counts", instance.hitCounts)
	mux.HandleFunc("POST /v1/chat/completions", instance.chatCompletions)
	mux.HandleFunc("POST /v1/responses", instance.responses)
	return mux
}

func (p *provider) health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, "ok\n")
}

func (p *provider) hitCounts(writer http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	counts := make(map[string]int, len(p.counts))
	for label, count := range p.counts {
		counts[label] = count
	}
	p.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(writer).Encode(counts)
}

func (p *provider) chatCompletions(writer http.ResponseWriter, request *http.Request) {
	p.serveProtocol(writer, request, "chat_completions", writeChatStream)
}

func (p *provider) responses(writer http.ResponseWriter, request *http.Request) {
	p.serveProtocol(writer, request, "responses", writeResponsesStream)
}

type streamWriter func(context.Context, http.ResponseWriter, http.Flusher, string, time.Duration)

func (p *provider) serveProtocol(writer http.ResponseWriter, request *http.Request, protocol string, stream streamWriter) {
	if request.Header.Get("Authorization") != requiredBearer {
		p.logger.Print("request rejected category=auth")
		writeError(writer, http.StatusUnauthorized, "fixture_unauthorized")
		return
	}
	body, err := readBoundedBody(request.Body)
	if errors.Is(err, errRequestTooLarge) {
		p.logger.Print("request rejected category=body_too_large")
		writeError(writer, http.StatusRequestEntityTooLarge, "fixture_body_too_large")
		return
	}
	if err != nil {
		p.logger.Print("request rejected category=invalid_json")
		writeError(writer, http.StatusBadRequest, "fixture_invalid_json")
		return
	}
	caseName, err := selectCase(body)
	clear(body)
	if err != nil {
		p.logger.Print("request rejected category=invalid_case")
		writeError(writer, http.StatusBadRequest, "fixture_invalid_case")
		return
	}
	p.increment(protocol + "." + caseName)

	switch caseName {
	case "429":
		writeError(writer, http.StatusTooManyRequests, "fixture_rate_limited")
		return
	case "500":
		writeError(writer, http.StatusInternalServerError, "fixture_upstream_failure")
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		p.logger.Print("request rejected category=streaming_unsupported")
		writeError(writer, http.StatusInternalServerError, "fixture_streaming_unsupported")
		return
	}
	stream(request.Context(), writer, flusher, caseName, p.slowDelay)
}

var errRequestTooLarge = errors.New("request body too large")

func readBoundedBody(reader io.ReadCloser) ([]byte, error) {
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, maxRequestBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxRequestBodyBytes {
		clear(body)
		return nil, errRequestTooLarge
	}
	return body, nil
}

func selectCase(body []byte) (string, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return "", err
	}
	selected := "success"
	found := false
	walkJSONStrings(value, func(text string) {
		for _, caseName := range allowedCases {
			if strings.Contains(text, "[case:"+caseName+"]") {
				selected = caseName
				found = true
				return
			}
		}
	})
	if strings.Contains(string(body), "[case:") && !found {
		return "", errors.New("unknown case marker")
	}
	return selected, nil
}

func walkJSONStrings(value any, visit func(string)) {
	switch typed := value.(type) {
	case string:
		visit(typed)
	case []any:
		for _, item := range typed {
			walkJSONStrings(item, visit)
		}
	case map[string]any:
		for _, item := range typed {
			walkJSONStrings(item, visit)
		}
	}
}

func (p *provider) increment(label string) {
	p.mu.Lock()
	p.counts[label]++
	p.mu.Unlock()
}

func writeChatStream(ctx context.Context, writer http.ResponseWriter, flusher http.Flusher, caseName string, delay time.Duration) {
	if caseName == "slow-first-byte" && !wait(ctx, delay) {
		return
	}
	setSSEHeaders(writer)
	writeSplitFrame(writer, flusher, "data: {\"choices\":[{\"delta\":{\"content\":\"Fixture \"}}]}\n\n")
	switch caseName {
	case "disconnect-after-delta":
		return
	case "malformed-event":
		writeSplitFrame(writer, flusher, "data: not-json\n\n")
		return
	case "idle-timeout":
		if !wait(ctx, delay) {
			return
		}
	}
	writeSplitFrame(writer, flusher, "data: {\"choices\":[{\"delta\":{\"content\":\"answer: $x=2$.\"}}]}\n\n")
	switch caseName {
	case "no-usage":
		writeSplitFrame(writer, flusher, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	case "usage-over-reservation":
		writeSplitFrame(writer, flusher, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8000,\"completion_tokens\":5000}}\n\n")
	default:
		writeSplitFrame(writer, flusher, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":5}}\n\n")
	}
	writeSplitFrame(writer, flusher, "data: [DONE]\n\n")
}

func writeResponsesStream(ctx context.Context, writer http.ResponseWriter, flusher http.Flusher, caseName string, delay time.Duration) {
	if caseName == "slow-first-byte" && !wait(ctx, delay) {
		return
	}
	setSSEHeaders(writer)
	writeSplitFrame(writer, flusher, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Fixture \"}\n\n")
	switch caseName {
	case "disconnect-after-delta":
		return
	case "malformed-event":
		writeSplitFrame(writer, flusher, "event: response.output_text.delta\ndata: not-json\n\n")
		return
	case "idle-timeout":
		if !wait(ctx, delay) {
			return
		}
	}
	writeSplitFrame(writer, flusher, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"answer: $x=2$.\"}\n\n")
	switch caseName {
	case "no-usage":
		writeSplitFrame(writer, flusher, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	case "usage-over-reservation":
		writeSplitFrame(writer, flusher, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":8000,\"output_tokens\":5000}}}\n\n")
	default:
		writeSplitFrame(writer, flusher, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":8,\"output_tokens\":5}}}\n\n")
	}
}

func setSSEHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeSplitFrame(writer io.Writer, flusher http.Flusher, frame string) {
	middle := len(frame) / 2
	_, _ = io.WriteString(writer, frame[:middle])
	flusher.Flush()
	_, _ = io.WriteString(writer, frame[middle:])
	flusher.Flush()
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func writeError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = fmt.Fprintf(writer, "{\"error\":%q}\n", code)
}
