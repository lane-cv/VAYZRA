package teaching

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type readAdminService struct {
	*fakeAdminHTTPService
	detail    AdminLessonDetail
	revisions []Revision
	catalogs  int
}

func (s *readAdminService) ListAdminCatalog(context.Context, Principal, AdminCatalogInput) ([]AdminCatalogItem, AdminCatalogCursor, error) {
	s.catalogs++
	return []AdminCatalogItem{{ID: uuid.New(), Kind: "lesson", Name: "Lesson", Published: true}}, AdminCatalogCursor{}, nil
}
func (s *readAdminService) GetAdminLesson(context.Context, Principal, uuid.UUID) (AdminLessonDetail, error) {
	return s.detail, nil
}
func (s *readAdminService) ListAdminRevisions(context.Context, Principal, uuid.UUID, int, RevisionCursor) ([]Revision, RevisionCursor, error) {
	return s.revisions, RevisionCursor{}, nil
}

func TestAdminReadDTOsAreLowerCamelAndBounded(t *testing.T) {
	lessonID, revisionID := uuid.New(), uuid.New()
	published := Revision{ID: revisionID, LessonID: lessonID, Version: 2, SourceDraftVersion: 7, PublishedAt: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)}
	svc := &readAdminService{fakeAdminHTTPService: &fakeAdminHTTPService{}, detail: AdminLessonDetail{Lesson: Lesson{ID: lessonID, PublishedRevisionID: revisionID}, Draft: Draft{LessonID: lessonID, LockVersion: 8, Audience: Audience{Mode: AudienceAll}}, Published: &published}, revisions: []Revision{published}}
	h := NewAdminHandler(svc).Routes()
	admin := auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}
	for _, path := range []string{"/catalog", "/lessons/" + lessonID.String(), "/lessons/" + lessonID.String() + "/revisions"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = r.WithContext(auth.ContextWithUser(r.Context(), admin))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if strings.Contains(body, "LessonID") || strings.Contains(body, "SourceDraftVersion") || strings.Contains(body, "PublishedAt") {
			t.Fatalf("domain field leaked: %s", body)
		}
		if path != "/catalog" && !strings.Contains(body, `"sourceDraftVersion":7`) {
			t.Fatalf("source version missing: %s", body)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/catalog?limit=201", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), admin))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || svc.catalogs != 1 {
		t.Fatalf("status=%d dispatches=%d", w.Code, svc.catalogs)
	}
}
