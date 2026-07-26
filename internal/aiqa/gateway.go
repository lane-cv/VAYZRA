package aiqa

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	MaxGatewayEventBytes  = 16 << 10
	MaxGatewayAnswerChars = 100000
	maxUpstreamErrorBytes = 4 << 10
)

var errGatewayConsumerComplete = errors.New("gateway consumer complete")

type GatewayRequest struct {
	RunID           uuid.UUID
	Model           string
	SystemPrompt    string
	Turns           []GatewayTurn
	Images          []GatewayImage
	MaxOutputTokens int
}

type GatewayTurn struct {
	Role string
	Text string
}

type GatewayImage struct {
	MediaType string
	Size      int64
	Open      func(context.Context) (io.ReadCloser, error)
}

type GatewayEvent struct {
	Kind         string
	Delta        string
	InputTokens  int64
	OutputTokens int64
	FinishReason string
}

type Gateway interface {
	Stream(context.Context, RuntimeProviderConfig, GatewayRequest, func(GatewayEvent) error) error
}

type GatewayError struct {
	Category string
	cause    error
}

func (e *GatewayError) Error() string { return e.Category }
func (e *GatewayError) Unwrap() error { return e.cause }

type compatibleGateway struct {
	client *http.Client
}

func NewGateway(client *http.Client) Gateway {
	if client == nil {
		client = http.DefaultClient
	}
	return &compatibleGateway{client: client}
}

type protocolAdapter interface {
	endpoint() string
	writeRequest(context.Context, io.Writer, GatewayRequest) error
	handleEvent(sseEvent, func(GatewayEvent) error) error
}

func (g *compatibleGateway) Stream(ctx context.Context, cfg RuntimeProviderConfig, request GatewayRequest, callback func(GatewayEvent) error) error {
	callerCtx := ctx
	if cfg.Timeouts.Total > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeouts.Total)
		defer cancel()
	}
	if err := validateGatewayRequest(cfg, request, callback); err != nil {
		return err
	}
	var adapter protocolAdapter
	switch cfg.ProtocolMode {
	case ProtocolChatCompletions:
		adapter = chatCompletionsAdapter{}
	case ProtocolResponses:
		adapter = responsesAdapter{}
	default:
		return gatewayError("upstream_4xx", nil)
	}
	authorization := "Bearer " + string(cfg.APIKey)
	zeroBytes(cfg.APIKey)
	defer func() { authorization = "" }()

	observed := false
	for attempt := 0; attempt < 2; attempt++ {
		written := atomic.Bool{}
		trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
			written.Store(true)
		}}
		attemptCtx := httptrace.WithClientTrace(ctx, trace)
		httpRequest, body, err := newStreamingRequest(attemptCtx, cfg, request, adapter, authorization)
		if err != nil {
			return err
		}
		response, doErr := g.client.Do(httpRequest)
		httpRequest.Header.Del("Authorization")
		if doErr != nil {
			_ = body.Close()
			if attempt == 0 && !written.Load() && !observed && ctx.Err() == nil {
				continue
			}
			return classifyTransportError(callerCtx, ctx, doErr)
		}
		observed = true
		authorization = ""
		err = g.consumeResponse(callerCtx, ctx, cfg, response, adapter, callback)
		if err != nil {
			return err
		}
		return nil
	}
	return gatewayError("stream_interrupted", nil)
}

func validateGatewayRequest(cfg RuntimeProviderConfig, request GatewayRequest, callback func(GatewayEvent) error) error {
	if cfg.BaseURL == nil || cfg.BaseURL.Scheme == "" || cfg.BaseURL.Host == "" ||
		request.RunID == uuid.Nil || strings.TrimSpace(request.Model) == "" ||
		request.MaxOutputTokens <= 0 || callback == nil {
		return gatewayError("upstream_4xx", nil)
	}
	for _, turn := range request.Turns {
		if (turn.Role != "student" && turn.Role != "assistant") || turn.Text == "" {
			return gatewayError("upstream_4xx", nil)
		}
	}
	for _, image := range request.Images {
		if !isAllowedGatewayImageType(image.MediaType) || image.Size <= 0 || image.Open == nil {
			return gatewayError("upstream_4xx", nil)
		}
	}
	return nil
}

func newStreamingRequest(ctx context.Context, cfg RuntimeProviderConfig, request GatewayRequest, adapter protocolAdapter, authorization string) (*http.Request, *io.PipeReader, error) {
	endpoint := *cfg.BaseURL
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/" + adapter.endpoint()
	endpoint.RawPath = ""

	reader, writer := io.Pipe()
	go func() {
		err := adapter.writeRequest(ctx, writer, request)
		_ = writer.CloseWithError(err)
	}()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), reader)
	if err != nil {
		_ = reader.Close()
		return nil, nil, gatewayError("upstream_4xx", nil)
	}
	httpRequest.Header.Set("Authorization", authorization)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	return httpRequest, reader, nil
}

func isAllowedGatewayImageType(mediaType string) bool {
	switch mediaType {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func (g *compatibleGateway) consumeResponse(callerCtx, ctx context.Context, cfg RuntimeProviderConfig, response *http.Response, adapter protocolAdapter, callback func(GatewayEvent) error) error {
	if response == nil || response.Body == nil {
		return gatewayError("stream_interrupted", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxUpstreamErrorBytes))
		switch {
		case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
			return gatewayError("auth", nil)
		case response.StatusCode == http.StatusTooManyRequests:
			return gatewayError("rate_limited", nil)
		case response.StatusCode >= 500:
			return gatewayError("upstream_5xx", nil)
		default:
			return gatewayError("upstream_4xx", nil)
		}
	}
	if !isEventStreamContentType(response.Header.Get("Content-Type")) {
		_ = response.Body.Close()
		return gatewayError("malformed_stream", nil)
	}

	body := response.Body
	if cfg.Timeouts.IdleStream > 0 {
		body = newGatewayIdleBody(body, cfg.Timeouts.IdleStream)
	}
	defer body.Close()
	answerChars := 0
	terminal := false
	err := readSSE(ctx, body, func(event sseEvent) error {
		if event.Data == "[DONE]" {
			terminal = true
		}
		return adapter.handleEvent(event, func(mapped GatewayEvent) error {
			if mapped.Kind == "delta" {
				answerChars += len([]rune(mapped.Delta))
				if answerChars > MaxGatewayAnswerChars {
					return gatewayError("response_too_large", nil)
				}
			}
			if mapped.Kind == "completed" {
				terminal = true
			}
			if err := callback(mapped); err != nil {
				return gatewayError("stream_interrupted", err)
			}
			return nil
		})
	})
	if errors.Is(err, errGatewayConsumerComplete) {
		return nil
	}
	if err == nil {
		if !terminal {
			return gatewayError("stream_interrupted", nil)
		}
		return nil
	}
	var safe *GatewayError
	if errors.As(err, &safe) {
		return safe
	}
	if errors.Is(callerCtx.Err(), context.Canceled) {
		return gatewayError("cancelled", nil)
	}
	if errors.Is(callerCtx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return gatewayError("timeout", nil)
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return gatewayError("cancelled", nil)
	}
	if errors.Is(err, errGatewayIdleTimeout) {
		return gatewayError("timeout", nil)
	}
	if errors.Is(err, errSSEEventTooLarge) {
		return gatewayError("response_too_large", nil)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return gatewayError("stream_interrupted", nil)
	}
	return gatewayError("malformed_stream", nil)
}

func classifyTransportError(callerCtx, streamCtx context.Context, err error) error {
	if errors.Is(callerCtx.Err(), context.Canceled) {
		return gatewayError("cancelled", nil)
	}
	if errors.Is(callerCtx.Err(), context.DeadlineExceeded) || errors.Is(streamCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return gatewayError("timeout", nil)
	}
	if errors.Is(streamCtx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return gatewayError("cancelled", nil)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return gatewayError("timeout", nil)
	}
	return gatewayError("stream_interrupted", nil)
}

func isEventStreamContentType(header string) bool {
	mediaType, parameters, err := mime.ParseMediaType(header)
	if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		return false
	}
	for name, value := range parameters {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func gatewayError(category string, cause error) error {
	return &GatewayError{Category: category, cause: cause}
}

func writeJSONString(writer io.Writer, value string) error {
	quoted, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.Write(quoted)
	return err
}

var errGatewayIdleTimeout = errors.New("gateway idle timeout")

type gatewayIdleBody struct {
	body     io.ReadCloser
	timeout  time.Duration
	mu       sync.Mutex
	timer    *time.Timer
	closed   bool
	timedOut atomic.Bool
}

func newGatewayIdleBody(body io.ReadCloser, timeout time.Duration) io.ReadCloser {
	idle := &gatewayIdleBody{body: body, timeout: timeout}
	idle.reset()
	return idle
}

func (b *gatewayIdleBody) Read(buffer []byte) (int, error) {
	n, err := b.body.Read(buffer)
	if b.timedOut.Load() {
		return n, errGatewayIdleTimeout
	}
	if n > 0 {
		b.reset()
	}
	return n, err
}

func (b *gatewayIdleBody) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(b.timeout, func() {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		b.timedOut.Store(true)
		b.closed = true
		b.mu.Unlock()
		_ = b.body.Close()
	})
}

func (b *gatewayIdleBody) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
	return b.body.Close()
}
