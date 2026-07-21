package app

import (
	"context"
	"github.com/google/uuid"
	"happylearn.local/app/internal/files"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type appFileAccess struct{ calls int }

func (s *appFileAccess) Open(context.Context, files.Principal, files.OpenInput) (files.OpenedFile, error) {
	s.calls++
	return files.OpenedFile{Body: io.NopCloser(strings.NewReader("pdf")), DisplayName: "x.pdf", ContentType: "application/pdf", Size: 3}, nil
}
func TestApplicationMountsStudentFileAccessBehindAuthentication(t *testing.T) {
	svc := &appFileAccess{}
	h := New(Dependencies{Auth: &appStudentAuth{}, FileAccess: svc})
	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+id.String()+"/preview", nil)
	req.AddCookie(&http.Cookie{Name: "hl_session", Value: "token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || w.Body.String() != "pdf" || svc.calls != 1 {
		t.Fatalf("status=%d body=%q calls=%d", w.Code, w.Body.String(), svc.calls)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/"+id.String()+"/preview", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 || svc.calls != 1 {
		t.Fatalf("unauth status=%d calls=%d", w.Code, svc.calls)
	}
}

type appFileBinding struct{ calls int }

func (s *appFileBinding) Replace(context.Context, files.Principal, uuid.UUID, int64, []files.DraftBindingInput) ([]files.DraftBinding, error) {
	s.calls++
	return []files.DraftBinding{}, nil
}
func TestApplicationMountsAdminLessonFileBindings(t *testing.T) {
	svc := &appFileBinding{}
	h := New(Dependencies{Auth: &appAdminAuth{}, Teaching: &appTeachingRead{}, FileBindings: svc, PublicOrigin: "https://learn.example.com"})
	id := uuid.New()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/lessons/"+id.String()+"/files", strings.NewReader(`{"expectedVersion":1,"files":[]}`))
	req.RemoteAddr = net.JoinHostPort("192.0.2.2", "1234")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://learn.example.com")
	req.Header.Set("X-CSRF-Token", "csrf-token-123")
	req.AddCookie(&http.Cookie{Name: "hl_session", Value: "token"})
	req.AddCookie(&http.Cookie{Name: "hl_csrf", Value: "csrf-token-123"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || svc.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, svc.calls, w.Body.String())
	}
}

type appFileCenter struct{ calls int }

func (s *appFileCenter) List(context.Context, files.Principal, files.FileFilter, files.Cursor) (files.FilePage, error) {
	s.calls++
	return files.FilePage{Items: []files.FileListItem{}}, nil
}
func (*appFileCenter) Detail(context.Context, files.Principal, uuid.UUID) (files.FileDetail, error) {
	return files.FileDetail{}, nil
}
func (*appFileCenter) Retry(context.Context, files.Principal, uuid.UUID) error { return nil }
func (*appFileCenter) Replace(context.Context, files.Principal, uuid.UUID, uuid.UUID) error {
	return nil
}
func (*appFileCenter) RollbackDraftBinding(context.Context, files.Principal, uuid.UUID, uuid.UUID, uuid.UUID) error {
	return nil
}
func (*appFileCenter) RequestDelete(context.Context, files.Principal, uuid.UUID) error { return nil }

func TestApplicationMountsAdminFileCenter(t *testing.T) {
	service := &appFileCenter{}
	handler := New(Dependencies{Auth: &appAdminAuth{}, FileCenter: service, PublicOrigin: "https://learn.example.com"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/files/?limit=25", nil)
	req.RemoteAddr = net.JoinHostPort("192.0.2.2", "1234")
	req.AddCookie(&http.Cookie{Name: "hl_session", Value: "token"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || service.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, service.calls, w.Body.String())
	}
}
