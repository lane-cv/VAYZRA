package files

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

type qaAccessHTTPStub struct {
	status QAFileStatus
	opened OpenedFile
	err    error
	opens  int
}

func (s *qaAccessHTTPStub) Status(context.Context, Principal, uuid.UUID) (QAFileStatus, error) {
	return s.status, s.err
}
func (s *qaAccessHTTPStub) Open(context.Context, Principal, QAOpenInput) (OpenedFile, error) {
	s.opens++
	return s.opened, s.err
}

func TestQAAccessHTTPStatusIsSafeAndRoutesAreStrict(t *testing.T) {
	id := uuid.New()
	svc := &qaAccessHTTPStub{status: QAFileStatus{FileVersionID: id, ProcessingState: "ready", DetectedMIME: "image/png", Size: 4, PreviewAvailable: true}}
	h := httpx.RequestID(NewQAAccessHandler(svc, nil).Routes())
	request := func(method, path string, user auth.User) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "192.0.2.4:1234"
		r = r.WithContext(auth.ContextWithUser(r.Context(), user))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	student := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	w := request(http.MethodGet, "/"+id.String()+"/status", student)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"fileVersionId"`) || strings.Contains(w.Body.String(), "fileId") || strings.Contains(w.Body.String(), "object") || w.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{{http.MethodPost, "/" + id.String() + "/status", 405}, {http.MethodGet, "/" + strings.ToUpper(id.String()) + "/status", 404}, {http.MethodGet, "/" + id.String() + "/status?threadId=" + uuid.NewString(), 404}} {
		got := request(tc.method, tc.path, student)
		if got.Code != tc.want {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, got.Code, got.Body.String())
		}
		if !strings.Contains(got.Body.String(), `"requestId":`) || got.Header().Get("Content-Type") != "application/json; charset=utf-8" || got.Header().Get("Cache-Control") != "no-store, private" || got.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s %s non-uniform error headers=%v body=%s", tc.method, tc.path, got.Header(), got.Body.String())
		}
	}
	unknown := request(http.MethodGet, "/"+id.String()+"/unknown", student)
	if unknown.Code != 404 || !strings.Contains(unknown.Body.String(), `"code":"not_found"`) || !strings.Contains(unknown.Body.String(), `"requestId":`) || unknown.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unknown status=%d headers=%v body=%s", unknown.Code, unknown.Header(), unknown.Body.String())
	}
}

func TestQAAccessHTTPStreamsAndRejectsNonQAActor(t *testing.T) {
	id := uuid.New()
	svc := &qaAccessHTTPStub{opened: OpenedFile{Body: io.NopCloser(strings.NewReader("data")), DisplayName: "答疑.pdf", ContentType: "application/pdf", Size: 4, Partial: true, Range: ResponseRange{Start: 4, End: 7, Total: 12}, Playable: true}}
	h := httpx.RequestID(NewQAAccessHandler(svc, nil).Routes())
	r := httptest.NewRequest(http.MethodGet, "/"+id.String()+"/download", nil)
	r.RemoteAddr = "192.0.2.4:1234"
	r.Header.Set("Range", "bytes=4-7")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "data" || w.Header().Get("Content-Range") != "bytes 4-7/12" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("status=%d body=%q headers=%v", w.Code, w.Body.String(), w.Header())
	}
	r = httptest.NewRequest(http.MethodGet, "/"+id.String()+"/download", nil)
	r.RemoteAddr = "192.0.2.4:1234"
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: "guest", Status: auth.StatusActive}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || svc.opens != 1 {
		t.Fatalf("status=%d opens=%d body=%s", w.Code, svc.opens, w.Body.String())
	}
}

func TestQAAccessHTTPReportsTransferAuditFailureThroughLogCallback(t *testing.T) {
	id := uuid.New()
	svc := &qaAccessHTTPStub{opened: OpenedFile{
		Body: io.NopCloser(&failingGateReader{}),
		ReportFailure: func(context.Context, string) error {
			return errors.New("qa audit backend secret")
		},
	}}
	var categories []string
	h := httpx.RequestID(NewQAAccessHandlerWithLog(
		svc,
		nil,
		func(category string) {
			categories = append(categories, category)
		},
	).Routes())
	r := httptest.NewRequest(http.MethodGet, "/"+id.String()+"/preview", nil)
	r.RemoteAddr = "192.0.2.4:1234"
	r = r.WithContext(auth.ContextWithUser(
		r.Context(),
		auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive},
	))

	h.ServeHTTP(httptest.NewRecorder(), r)

	if len(categories) != 1 || categories[0] != "write_failed" {
		t.Fatalf("categories = %v, want [write_failed]", categories)
	}
}

func TestQAAccessHTTPRandomAndForeignUseUniformNotFoundEnvelope(t *testing.T) {
	svc := &qaAccessHTTPStub{err: ErrNotFound}
	h := httpx.RequestID(NewQAAccessHandler(svc, nil).Routes())
	student := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	for _, suffix := range []string{"/status", "/preview", "/download"} {
		var shape string
		for _, id := range []uuid.UUID{uuid.New(), uuid.New()} {
			r := httptest.NewRequest(http.MethodGet, "/"+id.String()+suffix, nil)
			r.RemoteAddr = "192.0.2.4:1234"
			r = r.WithContext(auth.ContextWithUser(r.Context(), student))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			body := w.Body.String()
			if w.Code != 404 || !strings.Contains(body, `"code":"not_found"`) || !strings.Contains(body, `"requestId":`) {
				t.Fatalf("%s status=%d body=%s", suffix, w.Code, body)
			}
			normalized := body
			start := strings.Index(normalized, `"requestId":"`)
			if start >= 0 {
				tail := normalized[start+len(`"requestId":"`):]
				if end := strings.Index(tail, `"`); end >= 0 {
					normalized = normalized[:start] + `"requestId":"<id>` + tail[end:]
				}
			}
			if shape != "" && shape != normalized {
				t.Fatalf("non-uniform %s: %q != %q", suffix, shape, normalized)
			}
			shape = normalized
		}
	}
}
