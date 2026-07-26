package aiqa

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
)

var errSSEEventTooLarge = errors.New("sse event too large")

type sseEvent struct {
	Name string
	Data string
}

func readSSE(ctx context.Context, reader io.Reader, callback func(sseEvent) error) error {
	buffered := bufio.NewReaderSize(reader, MaxGatewayEventBytes+2)
	var name string
	var data []string
	hasData := false
	eventBytes := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := buffered.ReadString('\n')
		if len(line) > MaxGatewayEventBytes+1 || errors.Is(err, bufio.ErrBufferFull) {
			return errSSEEventTooLarge
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			if hasData {
				if err := callback(sseEvent{Name: name, Data: strings.Join(data, "\n")}); err != nil {
					return err
				}
			}
			name, data, hasData, eventBytes = "", nil, false, 0
			if errors.Is(err, io.EOF) {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field, value = line, ""
		} else if strings.HasPrefix(value, " ") {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			name = value
			eventBytes += len(value)
		case "data":
			data = append(data, value)
			hasData = true
			eventBytes += len(value)
		}
		if eventBytes > MaxGatewayEventBytes {
			return errSSEEventTooLarge
		}
		if errors.Is(err, io.EOF) {
			if hasData {
				if err := callback(sseEvent{Name: name, Data: strings.Join(data, "\n")}); err != nil {
					return err
				}
			}
			return nil
		}
	}
}
