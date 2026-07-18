package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"happylearn.local/app/internal/files"
)

type appUploadService struct{ creates int }

func (s *appUploadService) Create(context.Context, files.Principal, files.CreateUploadInput) (files.UploadView, error) {
	s.creates++
	return files.UploadView{ID: uuid.New(), State: files.UploadOpen}, nil
}
func (*appUploadService) Status(context.Context, files.Principal, uuid.UUID) (files.UploadView, error) {
	return files.UploadView{}, nil
}
func (*appUploadService) PutPart(context.Context, files.Principal, files.PutPartInput) (files.PartView, error) {
	return files.PartView{}, nil
}
func (*appUploadService) Complete(context.Context, files.Principal, uuid.UUID) (files.CompletedUpload, error) {
	return files.CompletedUpload{}, nil
}
func (*appUploadService) Cancel(context.Context, files.Principal, uuid.UUID) error { return nil }

func TestApplicationMountsUploadRoutesBehindAuthOriginAndCSRF(t *testing.T) {
	service := &appUploadService{}
	h := New(Dependencies{Auth: &appAdminAuth{}, Uploads: service, PublicOrigin: "https://learn.example.com"})
	body := `{"displayName":"lesson.pdf","declaredMime":"application/pdf","expectedSize":3,"expectedSha256":"4d4b21e9ef71e1291183a46b913ae6f2a0d68f4e83bb5c89ae36c15ebc1cb64f"}`
	request := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/admin/uploads", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("X-CSRF-Token", "upload-csrf-token")
		r.AddCookie(&http.Cookie{Name: "hl_session", Value: "token"})
		r.AddCookie(&http.Cookie{Name: "hl_csrf", Value: "upload-csrf-token"})
		return r
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("https://learn.example.com"))
	if w.Code != http.StatusCreated || service.creates != 1 || w.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("status=%d creates=%d cache=%q body=%s", w.Code, service.creates, w.Header().Get("Cache-Control"), w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("https://evil.example"))
	if w.Code != http.StatusForbidden || service.creates != 1 {
		t.Fatalf("cross-site status=%d creates=%d body=%s", w.Code, service.creates, w.Body.String())
	}
}
