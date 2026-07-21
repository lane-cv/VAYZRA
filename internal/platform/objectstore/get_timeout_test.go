package objectstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestObjectTransferTimeoutSupportsMaximumFileBeyondOperationTimeout(t *testing.T) {
	got := objectTransferTimeout(524288000)
	if got <= 30*time.Second {
		t.Fatalf("timeout=%s still bounded by operation timeout", got)
	}
	minimum := time.Duration(524288000/minDownloadBytesPerSecond) * time.Second
	if got < minimum {
		t.Fatalf("timeout=%s below minimum transfer duration=%s", got, minimum)
	}
	ctx, cancel := newObjectTransferContext(context.Background(), 524288000)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < minimum-time.Second {
		t.Fatalf("deadline ok=%t remaining=%s", ok, time.Until(deadline))
	}
}
func TestObjectTransferTimeoutIsBounded(t *testing.T) {
	if got := objectTransferTimeout(1); got < minimumObjectTransferTimeout || got > maximumObjectTransferTimeout {
		t.Fatalf("timeout=%s", got)
	}
}
func TestCancelReadCloserCloseCancelsTransferContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	wrapped := &cancelReadCloser{ReadCloser: io.NopCloser(strings.NewReader("x")), cancel: cancel}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("close did not cancel transfer context")
	}
}

func TestEstablishObjectReaderClosesLateSuccess(t *testing.T) {
	closed := make(chan struct{})
	producerDone := make(chan struct{})
	reader := &closeSignalReader{closed: closed}
	started := make(chan struct{})
	release := make(chan struct{})
	_, err := establishObjectReader(context.Background(), 10*time.Millisecond, func(context.Context) (io.ReadCloser, error) {
		close(started)
		<-release
		defer close(producerDone)
		return reader, nil
	})
	if err == nil {
		t.Fatal("expected establishment timeout")
	}
	<-started
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("late reader was not closed")
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("producer goroutine did not exit")
	}
}

func TestEstablishObjectReaderClosesSuccessAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan struct{})
	producerDone := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := establishObjectReader(ctx, time.Hour, func(context.Context) (io.ReadCloser, error) {
			<-release
			defer close(producerDone)
			return &closeSignalReader{closed: closed}, nil
		})
		result <- err
	}()
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context cancellation", err)
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("reader returned after caller cancellation was not closed")
	}
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("producer goroutine did not exit")
	}
}

type closeSignalReader struct{ closed chan struct{} }

func (*closeSignalReader) Read([]byte) (int, error) { return 0, io.EOF }
func (r *closeSignalReader) Close() error           { close(r.closed); return nil }
