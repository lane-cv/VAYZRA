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

type statusAdminService struct {
	*fakeAdminHTTPService
	item   AdminCatalogItem
	detail AdminLessonDetail
}

func (s *statusAdminService) ListAdminCatalog(context.Context, Principal, AdminCatalogInput) ([]AdminCatalogItem, AdminCatalogCursor, error) {
	return []AdminCatalogItem{s.item}, AdminCatalogCursor{}, nil
}

func (s *statusAdminService) GetAdminLesson(context.Context, Principal, uuid.UUID) (AdminLessonDetail, error) {
	return s.detail, nil
}

func (s *statusAdminService) ListAdminRevisions(context.Context, Principal, uuid.UUID, int, RevisionCursor) ([]Revision, RevisionCursor, error) {
	return nil, RevisionCursor{}, nil
}

func TestAdminLessonStatusDTOHasExactlyFourEffectiveStates(t *testing.T) {
	archivedAt := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		wantStatus   string
		archivedAt   *time.Time
		currentID    uuid.UUID
		hasRevisions bool
	}{
		{name: "draft", wantStatus: "draft"},
		{name: "published", wantStatus: "published", currentID: uuid.New(), hasRevisions: true},
		{name: "withdrawn", wantStatus: "withdrawn", hasRevisions: true},
		{name: "archived precedence", wantStatus: "archived", archivedAt: &archivedAt, currentID: uuid.New(), hasRevisions: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lessonID := uuid.New()
			service := &statusAdminService{
				fakeAdminHTTPService: &fakeAdminHTTPService{},
				item: AdminCatalogItem{
					ID: lessonID, Kind: "lesson", Name: "Lesson", ArchivedAt: tt.archivedAt,
					Published: tt.currentID != uuid.Nil, HasRevisions: tt.hasRevisions,
				},
				detail: AdminLessonDetail{
					Lesson:       Lesson{ID: lessonID, ChapterID: uuid.New(), PublishedRevisionID: tt.currentID, ArchivedAt: tt.archivedAt},
					Draft:        Draft{LessonID: lessonID, Title: "Lesson", LockVersion: 1, Audience: Audience{Mode: AudienceAll}},
					HasRevisions: tt.hasRevisions,
				},
			}
			handler := NewAdminHandler(service).Routes()
			admin := auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}
			for _, path := range []string{"/catalog?kind=lesson&includeArchived=true", "/lessons/" + lessonID.String()} {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r = r.WithContext(auth.ContextWithUser(r.Context(), admin))
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body.String())
				}
				body := w.Body.String()
				if !strings.Contains(body, `"status":"`+tt.wantStatus+`"`) {
					t.Fatalf("%s missing status %q: %s", path, tt.wantStatus, body)
				}
				if strings.Contains(body, "HasRevisions") || strings.Contains(body, "hasRevisions") {
					t.Fatalf("store projection leaked into DTO: %s", body)
				}
			}
		})
	}
}
