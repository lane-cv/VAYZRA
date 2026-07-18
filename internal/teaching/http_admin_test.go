package teaching

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

func TestAdminRoutesRejectStudentAndUnknownDraftJSON(t *testing.T) {
	h := NewAdminHandler(&fakeAdminHTTPService{}).Routes()
	r := httptest.NewRequest(http.MethodPost, "/lessons", strings.NewReader(`{"chapterId":"`+uuid.NewString()+`","title":"Lesson"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("student status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/lessons", strings.NewReader(`{"chapterId":"`+uuid.NewString()+`","title":"Lesson","other":true}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminDraftBoundaryReturnsStableConflictAndNoStore(t *testing.T) {
	service := &fakeAdminHTTPService{saveErr: ErrConflict}
	h := NewAdminHandler(service).Routes()
	r := httptest.NewRequest(http.MethodPut, "/lessons/not-a-uuid/draft", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", "1")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid UUID status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPut, "/lessons/"+uuid.NewString()+"/draft", strings.NewReader(`{"title":"Lesson","audience":{"Mode":"all"}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", "bad")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad if-match status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPut, "/lessons/"+uuid.NewString()+"/draft", strings.NewReader(`{"title":"Lesson","audience":{"Mode":"all"}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", "1")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"draft_conflict"`) || w.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("status=%d cache=%q body=%s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
}

func TestAdminPublishReturnsStableNotPublishable(t *testing.T) {
	h := NewAdminHandler(&fakeAdminHTTPService{publishErr: ErrNotPublishable}).Routes()
	r := httptest.NewRequest(http.MethodPost, "/lessons/"+uuid.NewString()+"/publish", nil)
	r.Header.Set("If-Match", "1")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"code":"lesson_not_publishable"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAdminPublishInfrastructureFailureReturnsInternalError(t *testing.T) {
	h := NewAdminHandler(&fakeAdminHTTPService{publishErr: errors.New("readiness database unavailable")}).Routes()
	r := httptest.NewRequest(http.MethodPost, "/lessons/"+uuid.NewString()+"/publish", nil)
	r.Header.Set("If-Match", "1")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

type fakeAdminHTTPService struct{ saveErr, publishErr error }

func (*fakeAdminHTTPService) CreateCatalog(context.Context, Principal, CatalogCreateInput) (CatalogNode, error) {
	return CatalogNode{}, nil
}
func (*fakeAdminHTTPService) RenameCatalog(context.Context, Principal, CatalogRenameInput) (CatalogNode, error) {
	return CatalogNode{}, nil
}
func (*fakeAdminHTTPService) ReorderCatalog(context.Context, Principal, CatalogReorderInput) error {
	return nil
}
func (*fakeAdminHTTPService) ArchiveCatalog(context.Context, Principal, CatalogArchiveInput) error {
	return nil
}
func (*fakeAdminHTTPService) CreateLesson(context.Context, Principal, CreateLessonInput) (Draft, error) {
	return Draft{}, nil
}
func (f *fakeAdminHTTPService) SaveDraft(context.Context, Principal, SaveDraftInput) (Draft, error) {
	return Draft{}, f.saveErr
}
func (f *fakeAdminHTTPService) Publish(context.Context, Principal, PublishInput) (Revision, error) {
	return Revision{}, f.publishErr
}
func (*fakeAdminHTTPService) Withdraw(context.Context, Principal, uuid.UUID) error { return nil }

var _ = errors.Is

func (*fakeAdminHTTPService) ArchiveLesson(context.Context, Principal, uuid.UUID) error { return nil }

func TestAdminPublishAndWithdrawRejectBodies(t *testing.T) {
	h := NewAdminHandler(&fakeAdminHTTPService{}).Routes()
	for _, endpoint := range []string{"publish", "withdraw"} {
		r := httptest.NewRequest(http.MethodPost, "/lessons/"+uuid.NewString()+"/"+endpoint, strings.NewReader(`{"unexpected":true}`))
		r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d", endpoint, w.Code)
		}
	}
}
