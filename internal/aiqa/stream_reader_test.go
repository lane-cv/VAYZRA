package aiqa

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStreamReaderSSEFieldsCRLFCommentsAndEmptyEvents(t *testing.T) {
	input := ":keepalive\r\nevent: named\r\ndata: one\r\ndata: two\r\n\r\n\r\ndata:\r\n\r\n"
	var events []sseEvent
	err := readSSE(context.Background(), strings.NewReader(input), func(event sseEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Name != "named" || events[0].Data != "one\ntwo" || events[1].Data != "" {
		t.Fatalf("events=%#v", events)
	}
}

func TestStreamReaderRejectsOversizedEventAndDispatchesAtEOF(t *testing.T) {
	err := readSSE(context.Background(), strings.NewReader("data: "+strings.Repeat("x", MaxGatewayEventBytes+1)+"\n\n"), func(sseEvent) error { return nil })
	if !errors.Is(err, errSSEEventTooLarge) {
		t.Fatalf("oversize err=%v", err)
	}
	var events []sseEvent
	err = readSSE(context.Background(), strings.NewReader("data: complete-at-eof"), func(event sseEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil || len(events) != 1 || events[0].Data != "complete-at-eof" {
		t.Fatalf("EOF err=%v events=%#v", err, events)
	}
}

func TestStreamReaderHonorsCancellationAndCallbackError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := readSSE(ctx, strings.NewReader("data: x\n\n"), func(sseEvent) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	callbackErr := errors.New("callback")
	err = readSSE(context.Background(), strings.NewReader("data: x\n\n"), func(sseEvent) error { return callbackErr })
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback err=%v", err)
	}
}
