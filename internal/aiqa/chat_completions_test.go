package aiqa

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChatCompletionsExactRequestAndIncrementalImage(t *testing.T) {
	image := &trackingImage{remaining: 20 << 20}
	var gotPath, gotAuth string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	request := testGatewayRequest()
	request.Images = []GatewayImage{{MediaType: "image/png", Size: 20 << 20, Open: image.open}}
	cfg := testRuntimeConfig(t, server, ProtocolChatCompletions)
	cfg.Timeouts.Total = time.Minute
	var events []GatewayEvent
	err := NewGateway(server.Client()).Stream(context.Background(), cfg, request, func(event GatewayEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if gotPath != "/v1/chat/completions" || gotAuth != "Bearer test-secret" {
		t.Fatalf("path=%q auth=%q", gotPath, gotAuth)
	}
	if body["model"] != request.Model || body["stream"] != true || body["max_tokens"] != float64(321) {
		t.Fatalf("unexpected request body fields: %#v", body)
	}
	options, _ := body["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Fatalf("stream_options=%#v", options)
	}
	if _, exists := body["tools"]; exists {
		t.Fatal("unsupported tools included")
	}
	encoded, _ := json.Marshal(body)
	for _, forbidden := range []string{"/private/", "object-key", "student_id", "studentId"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("request leaked %q", forbidden)
		}
	}
	if image.opens != 1 || !image.closed || image.maxRead > 64<<10 {
		t.Fatalf("image opens=%d closed=%v maxRead=%d", image.opens, image.closed, image.maxRead)
	}
	if len(events) != 1 || events[0].Kind != "usage" || events[0].InputTokens != 7 || events[0].OutputTokens != 3 {
		t.Fatalf("events=%#v", events)
	}

	messages := body["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages=%#v", messages)
	}
	lastContent := messages[len(messages)-1].(map[string]any)["content"].([]any)
	if lastContent[0].(map[string]any)["type"] != "text" || lastContent[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("image content=%#v", lastContent)
	}
}

func TestChatCompletionsSplitFramesUsageDoneAndPostWriteNoRetry(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, part := range []string{
			": comment\r\n\r\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"hel",
			"lo\"},\"finish_reason\":null}]}\r\n\r\n",
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\r\n\r\n",
			"data: [DONE]\r\n\r\n",
		} {
			_, _ = io.WriteString(w, part)
			flusher.Flush()
		}
	}))
	defer server.Close()
	var events []GatewayEvent
	err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolChatCompletions), testGatewayRequest(), func(event GatewayEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("post-write stream was retried: hits=%d", hits)
	}
	if len(events) != 3 || events[0].Delta != "hello" || events[1].Kind != "usage" || events[2].Kind != "completed" || events[2].FinishReason != "stop" {
		t.Fatalf("events=%#v", events)
	}
}

type trackingImage struct {
	remaining int64
	opens     int
	maxRead   int
	closed    bool
}

func (i *trackingImage) open(context.Context) (io.ReadCloser, error) {
	i.opens++
	return &trackingImageReader{owner: i}, nil
}

type trackingImageReader struct{ owner *trackingImage }

func (r *trackingImageReader) Read(p []byte) (int, error) {
	if len(p) > r.owner.maxRead {
		r.owner.maxRead = len(p)
	}
	if r.owner.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.owner.remaining {
		n = int(r.owner.remaining)
	}
	for j := 0; j < n; j++ {
		p[j] = byte(j)
	}
	r.owner.remaining -= int64(n)
	return n, nil
}

func (r *trackingImageReader) Close() error {
	r.owner.closed = true
	return nil
}

func TestChatCompletionsAnswerCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 11; i++ {
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\""+strings.Repeat("x", 10000)+"\"}}]}\n\n")
		}
	}))
	defer server.Close()
	err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolChatCompletions), testGatewayRequest(), func(GatewayEvent) error { return nil })
	if categoryOf(err) != "response_too_large" {
		t.Fatalf("category=%q err=%v", categoryOf(err), err)
	}
}

func TestChatCompletionsMalformedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: not-json\n\n")
	}))
	defer server.Close()
	err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolChatCompletions), testGatewayRequest(), func(GatewayEvent) error { return nil })
	if categoryOf(err) != "malformed_stream" {
		t.Fatalf("category=%q err=%v", categoryOf(err), err)
	}
}
