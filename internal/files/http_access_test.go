package files

import (
	"context"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type accessHTTPStub struct {
	opened OpenedFile
	err    error
	input  OpenInput
}

func (s *accessHTTPStub) Open(_ context.Context, _ Principal, in OpenInput) (OpenedFile, error) {
	s.input = in
	return s.opened, s.err
}
func TestAccessHTTPHeadersAndPartialBody(t *testing.T) {
	id := uuid.New()
	svc := &accessHTTPStub{opened: OpenedFile{Body: io.NopCloser(strings.NewReader("0123456789")), DisplayName: "物理 讲义.pdf", ContentType: "application/pdf", Size: 10, Partial: true, Range: ResponseRange{Start: 100, End: 109, Total: 1000}, Playable: true}}
	h := httpx.RequestID(NewAccessHandler(svc, nil).Routes())
	req := httptest.NewRequest(http.MethodGet, "/"+id.String()+"/preview", nil)
	req.RemoteAddr = "192.0.2.2:1234"
	req.Header.Set("Range", "bytes=100-109")
	req = req.WithContext(auth.ContextWithUser(req.Context(), auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 206 || w.Body.String() != "0123456789" || w.Header().Get("Content-Range") != "bytes 100-109/1000" || w.Header().Get("Cache-Control") != "no-store, private" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("code=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

type deadlineResponseWriter struct {
	header      http.Header
	deadlines   []time.Time
	unblock     chan struct{}
	writeCalls  int
	blockWrites bool
}

func newDeadlineResponseWriter(block bool) *deadlineResponseWriter {
	return &deadlineResponseWriter{header: make(http.Header), unblock: make(chan struct{}, 1), blockWrites: block}
}
func (w *deadlineResponseWriter) Header() http.Header { return w.header }
func (w *deadlineResponseWriter) WriteHeader(int)     {}
func (w *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	if !deadline.IsZero() {
		delay := time.Until(deadline)
		if delay < 0 {
			delay = 0
		}
		time.AfterFunc(delay, func() {
			select {
			case w.unblock <- struct{}{}:
			default:
			}
		})
	}
	return nil
}
func (w *deadlineResponseWriter) Write(p []byte) (int, error) {
	w.writeCalls++
	if w.blockWrites {
		<-w.unblock
		return 0, &timeoutWriteError{}
	}
	return len(p), nil
}

type timeoutWriteError struct{}

func (*timeoutWriteError) Error() string   { return "write timeout" }
func (*timeoutWriteError) Timeout() bool   { return true }
func (*timeoutWriteError) Temporary() bool { return true }

func TestAccessHTTPWriteIdleDeadlineUnblocksStalledClient(t *testing.T) {
	id := uuid.New()
	failures := 0
	svc := &accessHTTPStub{opened: OpenedFile{
		Body: io.NopCloser(strings.NewReader("blocked")), DisplayName: "video.mp4",
		ContentType: "video/mp4", Size: 7,
		ReportFailure: func(context.Context, string) error { failures++; return nil },
	}}
	handler := NewAccessHandler(svc, nil)
	handler.writeIdleTimeout = 20 * time.Millisecond
	h := httpx.RequestID(handler.Routes())
	req := httptest.NewRequest(http.MethodGet, "/"+id.String()+"/preview", nil)
	req.RemoteAddr = "192.0.2.2:1234"
	req = req.WithContext(auth.ContextWithUser(req.Context(), auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := newDeadlineResponseWriter(true)
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, req); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handler remained blocked after write idle deadline")
	}
	if failures != 1 {
		t.Fatalf("failure reports=%d, want 1", failures)
	}
	if len(w.deadlines) < 2 || w.deadlines[0].IsZero() || !w.deadlines[len(w.deadlines)-1].IsZero() {
		t.Fatalf("deadlines=%v, want non-zero write deadline followed by clear", w.deadlines)
	}
}

func TestAccessHTTPWriteDeadlineRefreshesBeforeEveryWrite(t *testing.T) {
	id := uuid.New()
	body := strings.Repeat("x", 64*1024+1)
	svc := &accessHTTPStub{opened: OpenedFile{Body: io.NopCloser(strings.NewReader(body)), DisplayName: "x.bin", ContentType: "application/octet-stream", Size: int64(len(body))}}
	handler := NewAccessHandler(svc, nil)
	handler.writeIdleTimeout = time.Second
	h := httpx.RequestID(handler.Routes())
	req := httptest.NewRequest(http.MethodGet, "/"+id.String()+"/preview", nil)
	req.RemoteAddr = "192.0.2.2:1234"
	req = req.WithContext(auth.ContextWithUser(req.Context(), auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := newDeadlineResponseWriter(false)
	h.ServeHTTP(w, req)
	if w.writeCalls != 2 {
		t.Fatalf("writes=%d, want 2", w.writeCalls)
	}
	if len(w.deadlines) != 3 || w.deadlines[0].IsZero() || w.deadlines[1].IsZero() || !w.deadlines[2].IsZero() {
		t.Fatalf("deadlines=%v, want one refresh per write and final clear", w.deadlines)
	}
}
