package students

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

func TestRoutesRejectStudentAndStrictlyDecodeCreate(t *testing.T) {
	h := NewHandler(fakeHTTPService{})
	studentRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	studentRequest = studentRequest.WithContext(withUser(studentRequest.Context(), auth.User{Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, studentRequest)
	if w.Code != http.StatusForbidden {
		t.Fatalf("student status=%d", w.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"student01","displayName":"甲","temporaryPassword":"Temporary Password 42!","other":true}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(withUser(r.Context(), auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("strict decode status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRoutesRejectInvalidIDAndStatus(t *testing.T) {
	h := NewHandler(fakeHTTPService{})
	admin := auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}
	r := httptest.NewRequest(http.MethodPost, "/not-a-uuid/status", strings.NewReader(`{"status":"disabled"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(withUser(r.Context(), admin))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("id status=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/"+uuid.NewString()+"/status", strings.NewReader(`{"status":"admin"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(withUser(r.Context(), admin))
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status status=%d", w.Code)
	}
}

type fakeHTTPService struct{}

func (fakeHTTPService) List(context.Context, Principal, int, uuid.UUID) ([]auth.User, uuid.UUID, error) {
	return nil, uuid.Nil, nil
}
func (fakeHTTPService) Create(context.Context, Principal, CreateInput) (auth.User, error) {
	return auth.User{}, nil
}
func (fakeHTTPService) SetStatus(context.Context, Principal, uuid.UUID, auth.Status) error {
	return nil
}
func (fakeHTTPService) ResetPassword(context.Context, Principal, uuid.UUID, string) error { return nil }
func withUser(ctx context.Context, user auth.User) context.Context {
	return auth.ContextWithUser(ctx, user)
}
