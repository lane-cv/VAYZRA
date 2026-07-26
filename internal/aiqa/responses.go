package aiqa

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
)

type responsesAdapter struct{}

func (responsesAdapter) endpoint() string { return "responses" }

func (responsesAdapter) writeRequest(ctx context.Context, writer io.Writer, request GatewayRequest) error {
	if _, err := io.WriteString(writer, `{"model":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, request.Model); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `,"instructions":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, request.SystemPrompt); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `,"stream":true,"max_output_tokens":`+strconv.Itoa(request.MaxOutputTokens)+`,"input":[`); err != nil {
		return err
	}
	first := true
	imagesWritten := false
	for index, turn := range request.Turns {
		if !first {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		first = false
		role := "user"
		contentType := "input_text"
		if turn.Role == "assistant" {
			role, contentType = "assistant", "output_text"
		}
		if index == len(request.Turns)-1 && role == "user" && len(request.Images) > 0 {
			if err := writeResponsesMultimodalInput(ctx, writer, turn.Text, request.Images); err != nil {
				return err
			}
			imagesWritten = true
			continue
		}
		if _, err := io.WriteString(writer, `{"role":`); err != nil {
			return err
		}
		if err := writeJSONString(writer, role); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, `,"content":[{"type":`); err != nil {
			return err
		}
		if err := writeJSONString(writer, contentType); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, `,"text":`); err != nil {
			return err
		}
		if err := writeJSONString(writer, turn.Text); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, "}]}"); err != nil {
			return err
		}
	}
	if len(request.Images) > 0 && !imagesWritten {
		if !first {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		if err := writeResponsesMultimodalInput(ctx, writer, "", request.Images); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "]}")
	return err
}

func writeResponsesMultimodalInput(ctx context.Context, writer io.Writer, text string, images []GatewayImage) error {
	if _, err := io.WriteString(writer, `{"role":"user","content":[`); err != nil {
		return err
	}
	offset := 0
	if text != "" {
		if _, err := io.WriteString(writer, `{"type":"input_text","text":`); err != nil {
			return err
		}
		if err := writeJSONString(writer, text); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, "}"); err != nil {
			return err
		}
		offset = 1
	}
	for index, image := range images {
		if index+offset > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(writer, `{"type":"input_image","image_url":"data:`); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, image.MediaType+`;base64,`); err != nil {
			return err
		}
		if err := streamBase64Image(ctx, writer, image); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, `"}`); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "]}")
	return err
}

type responseCompleted struct {
	Response struct {
		Status string `json:"status"`
		Usage  *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

func (responsesAdapter) handleEvent(event sseEvent, callback func(GatewayEvent) error) error {
	if event.Data == "" || event.Data == "[DONE]" {
		return nil
	}
	switch event.Name {
	case "response.output_text.delta":
		var delta struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(event.Data), &delta); err != nil {
			return gatewayError("malformed_stream", nil)
		}
		if delta.Delta != "" {
			return callback(GatewayEvent{Kind: "delta", Delta: delta.Delta})
		}
	case "response.completed":
		var completed responseCompleted
		if err := json.Unmarshal([]byte(event.Data), &completed); err != nil {
			return gatewayError("malformed_stream", nil)
		}
		if completed.Response.Status == "" {
			return gatewayError("malformed_stream", nil)
		}
		if completed.Response.Usage != nil {
			if completed.Response.Usage.InputTokens < 0 || completed.Response.Usage.OutputTokens < 0 {
				return gatewayError("malformed_stream", nil)
			}
			if err := callback(GatewayEvent{Kind: "usage", InputTokens: completed.Response.Usage.InputTokens, OutputTokens: completed.Response.Usage.OutputTokens}); err != nil {
				return err
			}
		}
		return callback(GatewayEvent{Kind: "completed", FinishReason: completed.Response.Status})
	case "response.failed", "response.incomplete":
		return gatewayError("stream_interrupted", nil)
	default:
		var valid any
		if err := json.Unmarshal([]byte(event.Data), &valid); err != nil {
			return gatewayError("malformed_stream", nil)
		}
	}
	return nil
}
