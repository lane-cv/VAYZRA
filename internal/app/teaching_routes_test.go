package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/teaching"
)

type appAdminAuth struct{ appFakeAuth }

func (*appAdminAuth) Authenticate(context.Context, string) (auth.Authentication, error) {
	return auth.Authentication{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}, nil
}

type appTeachingRead struct{ lists int }

func (s *appTeachingRead) ListAdminCatalog(context.Context, teaching.Principal, teaching.AdminCatalogInput) ([]teaching.AdminCatalogItem, teaching.AdminCatalogCursor, error) {
	s.lists++
	return nil, teaching.AdminCatalogCursor{}, nil
}
func (*appTeachingRead) GetAdminLesson(context.Context, teaching.Principal, uuid.UUID) (teaching.AdminLessonDetail, error) {
	return teaching.AdminLessonDetail{}, nil
}
func (*appTeachingRead) ListAdminRevisions(context.Context, teaching.Principal, uuid.UUID, int, teaching.RevisionCursor) ([]teaching.Revision, teaching.RevisionCursor, error) {
	return nil, teaching.RevisionCursor{}, nil
}
func (*appTeachingRead) CreateCatalog(context.Context, teaching.Principal, teaching.CatalogCreateInput) (teaching.CatalogNode, error) {
	return teaching.CatalogNode{}, nil
}
func (*appTeachingRead) RenameCatalog(context.Context, teaching.Principal, teaching.CatalogRenameInput) (teaching.CatalogNode, error) {
	return teaching.CatalogNode{}, nil
}
func (*appTeachingRead) ReorderCatalog(context.Context, teaching.Principal, teaching.CatalogReorderInput) error {
	return nil
}
func (*appTeachingRead) ArchiveCatalog(context.Context, teaching.Principal, teaching.CatalogArchiveInput) error {
	return nil
}
func (*appTeachingRead) CreateLesson(context.Context, teaching.Principal, teaching.CreateLessonInput) (teaching.Draft, error) {
	return teaching.Draft{}, nil
}
func (*appTeachingRead) SaveDraft(context.Context, teaching.Principal, teaching.SaveDraftInput) (teaching.Draft, error) {
	return teaching.Draft{}, nil
}
func (*appTeachingRead) Publish(context.Context, teaching.Principal, teaching.PublishInput) (teaching.Revision, error) {
	return teaching.Revision{}, nil
}
func (*appTeachingRead) Withdraw(context.Context, teaching.Principal, uuid.UUID) error { return nil }
func (*appTeachingRead) ArchiveLesson(context.Context, teaching.Principal, uuid.UUID) error {
	return nil
}

func TestApplicationMountsAuthenticatedAdminTeachingReads(t *testing.T) {
	svc := &appTeachingRead{}
	h := New(Dependencies{Auth: &appAdminAuth{}, Teaching: svc})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/catalog", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || svc.lists != 1 || w.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("status=%d lists=%d cache=%q body=%s", w.Code, svc.lists, w.Header().Get("Cache-Control"), w.Body.String())
	}
}
