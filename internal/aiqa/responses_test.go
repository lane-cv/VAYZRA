package aiqa

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponsesExactRequestAndEvents(t *testing.T) {
	var path string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_text.delta\ndata: {\"delta\":\"answer\"}\n\n" +
				"event: response.completed\ndata: {\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":8,\"output_tokens\":5}}}\n\n",
		))
	}))
	defer server.Close()
	request := testGatewayRequest()
	request.Images = []GatewayImage{{MediaType: "image/jpeg", Size: 3, Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("img")), nil
	}}}
	var events []GatewayEvent
	err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolResponses), request, func(event GatewayEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/responses" || body["model"] != request.Model || body["instructions"] != request.SystemPrompt || body["stream"] != true || body["max_output_tokens"] != float64(321) {
		t.Fatalf("path=%q body=%#v", path, body)
	}
	if _, exists := body["tools"]; exists {
		t.Fatal("unsupported tools included")
	}
	input := body["input"].([]any)
	content := input[len(input)-1].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["type"] != "input_text" || content[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("content=%#v", content)
	}
	if len(events) != 3 || events[0].Delta != "answer" || events[1].Kind != "usage" || events[2].Kind != "completed" {
		t.Fatalf("events=%#v", events)
	}
}

func TestResponsesMalformedStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: not-json\n\n"))
	}))
	defer server.Close()
	err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolResponses), testGatewayRequest(), func(GatewayEvent) error { return nil })
	if categoryOf(err) != "malformed_stream" {
		t.Fatalf("category=%q err=%v", categoryOf(err), err)
	}
}

func TestResponsesCumulativeAnswerCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 11; i++ {
			_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"delta\":\""+strings.Repeat("x", 10000)+"\"}\n\n")
		}
	}))
	defer server.Close()
	err := NewGateway(server.Client()).Stream(context.Background(), testRuntimeConfig(t, server, ProtocolResponses), testGatewayRequest(), func(GatewayEvent) error { return nil })
	if categoryOf(err) != "response_too_large" {
		t.Fatalf("category=%q err=%v", categoryOf(err), err)
	}
}
