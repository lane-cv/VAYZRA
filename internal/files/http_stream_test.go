package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

type reasonRecorder struct {
	reasons []string
	ctxErr  error
}

func (r *reasonRecorder) report(ctx context.Context, reason string) error {
	r.reasons = append(r.reasons, reason)
	return nil
}

type midReadFailure struct{ sent bool }

func (r *midReadFailure) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		copy(p, "chunk")
		return 5, nil
	}
	return 0, errors.New("backend detail secret-key")
}
func (*midReadFailure) Close() error { return nil }

type midReadCloseFailure struct{ midReadFailure }

func (*midReadCloseFailure) Close() error { return errors.New("close detail") }

type writeFailure struct{}

func (writeFailure) Write([]byte) (int, error) { return 0, errors.New("client detail") }

type closeFailure struct{ io.Reader }

func (closeFailure) Close() error { return errors.New("close detail") }

func TestDeliveryReportsSanitizedReadWriteAndCloseFailures(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		dst  io.Writer
		body io.ReadCloser
		want string
	}{
		{"read", context.Background(), io.Discard, &midReadFailure{}, "stream_read"},
		{"read-and-close", context.Background(), io.Discard, &midReadCloseFailure{}, "stream_read"},
		{"write", context.Background(), writeFailure{}, io.NopCloser(bytes.NewReader([]byte("body"))), "stream_write"},
		{"close", context.Background(), io.Discard, closeFailure{Reader: bytes.NewReader([]byte("body"))}, "stream_close"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &reasonRecorder{}
			opened := OpenedFile{Body: tt.body, ReportFailure: rec.report}
			if err := deliverOpenedFile(tt.ctx, tt.dst, opened); err == nil {
				t.Fatal("expected error")
			}
			if len(rec.reasons) != 1 || rec.reasons[0] != tt.want {
				t.Fatalf("reasons=%v", rec.reasons)
			}
		})
	}
}
func TestDeliveryCancellationHasDedicatedSanitizedResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := &reasonRecorder{}
	opened := OpenedFile{Body: &midReadFailure{}, ReportFailure: rec.report}
	if err := deliverOpenedFile(ctx, io.Discard, opened); err == nil {
		t.Fatal("expected error")
	}
	if len(rec.reasons) != 1 || rec.reasons[0] != "cancelled" || rec.ctxErr != nil {
		t.Fatalf("reasons=%v", rec.reasons)
	}
}
