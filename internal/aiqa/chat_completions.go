package aiqa

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strconv"
)

type chatCompletionsAdapter struct{}

func (chatCompletionsAdapter) endpoint() string { return "chat/completions" }

func (chatCompletionsAdapter) writeRequest(ctx context.Context, writer io.Writer, request GatewayRequest) error {
	if _, err := io.WriteString(writer, `{"model":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, request.Model); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `,"stream":true,"stream_options":{"include_usage":true},"max_tokens":`); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, strconv.Itoa(request.MaxOutputTokens)); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, `,"messages":[`); err != nil {
		return err
	}
	first := true
	writeMessage := func(role, text string) error {
		if !first {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		first = false
		if _, err := io.WriteString(writer, `{"role":`); err != nil {
			return err
		}
		if err := writeJSONString(writer, role); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, `,"content":`); err != nil {
			return err
		}
		if err := writeJSONString(writer, text); err != nil {
			return err
		}
		_, err := io.WriteString(writer, "}")
		return err
	}
	if request.SystemPrompt != "" {
		if err := writeMessage("system", request.SystemPrompt); err != nil {
			return err
		}
	}
	imagesWritten := false
	for index, turn := range request.Turns {
		role := "user"
		if turn.Role == "assistant" {
			role = "assistant"
		}
		if index == len(request.Turns)-1 && role == "user" && len(request.Images) > 0 {
			if !first {
				if _, err := io.WriteString(writer, ","); err != nil {
					return err
				}
			}
			first = false
			if err := writeChatMultimodalMessage(ctx, writer, turn.Text, request.Images); err != nil {
				return err
			}
			imagesWritten = true
			continue
		}
		if err := writeMessage(role, turn.Text); err != nil {
			return err
		}
	}
	if len(request.Images) > 0 && !imagesWritten {
		if !first {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		if err := writeChatMultimodalMessage(ctx, writer, "", request.Images); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "]}")
	return err
}

func writeChatMultimodalMessage(ctx context.Context, writer io.Writer, text string, images []GatewayImage) error {
	if _, err := io.WriteString(writer, `{"role":"user","content":[`); err != nil {
		return err
	}
	offset := 0
	if text != "" {
		if _, err := io.WriteString(writer, `{"type":"text","text":`); err != nil {
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
		if _, err := io.WriteString(writer, `{"type":"image_url","image_url":{"url":"data:`); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, image.MediaType); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, `;base64,`); err != nil {
			return err
		}
		if err := streamBase64Image(ctx, writer, image); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, `"}}`); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "]}")
	return err
}

func streamBase64Image(ctx context.Context, writer io.Writer, image GatewayImage) error {
	reader, err := image.Open(ctx)
	if err != nil {
		return err
	}
	defer reader.Close()
	encoder := base64.NewEncoder(base64.StdEncoding, writer)
	_, copyErr := io.CopyBuffer(encoder, &contextReader{ctx: ctx, reader: reader}, make([]byte, 32<<10))
	closeErr := encoder.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

func (chatCompletionsAdapter) handleEvent(event sseEvent, callback func(GatewayEvent) error) error {
	if event.Data == "" || event.Data == "[DONE]" {
		return nil
	}
	var chunk chatChunk
	if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
		return gatewayError("malformed_stream", nil)
	}
	if len(chunk.Choices) == 0 && chunk.Usage == nil {
		return gatewayError("malformed_stream", nil)
	}
	if chunk.Usage != nil {
		if chunk.Usage.PromptTokens < 0 || chunk.Usage.CompletionTokens < 0 {
			return gatewayError("malformed_stream", nil)
		}
		if err := callback(GatewayEvent{Kind: "usage", InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}); err != nil {
			return err
		}
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			if err := callback(GatewayEvent{Kind: "delta", Delta: choice.Delta.Content}); err != nil {
				return err
			}
		}
		if choice.FinishReason != nil {
			if err := callback(GatewayEvent{Kind: "completed", FinishReason: *choice.FinishReason}); err != nil {
				return err
			}
		}
	}
	return nil
}
