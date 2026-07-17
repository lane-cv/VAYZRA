package students

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

func TestAdminStudentsListIsNeverStored(t *testing.T) {
	h := NewHandler(fakeHTTPService{})
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withUser(context.Background(), auth.User{Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("status=%d cache=%q", w.Code, w.Header().Get("Cache-Control"))
	}
}
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

func TestCreateUsesTrustedForwardedClientIPAndRejectsMalformedForwarding(t *testing.T) {
	svc := &capturingHTTPService{}
	h := NewHandlerWithConfig(svc, HTTPConfig{TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}})
	admin := auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"student01","displayName":"甲","temporaryPassword":"Temporary Password 42!"}`))
	request.RemoteAddr = "10.1.2.3:443"
	request.Header.Set("X-Forwarded-For", "198.51.100.4")
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(withUser(request.Context(), admin))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, request)
	if w.Code != http.StatusCreated || svc.actor.IP.String() != "198.51.100.4" {
		t.Fatalf("status=%d actor=%#v", w.Code, svc.actor)
	}

	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"student02","displayName":"乙","temporaryPassword":"Temporary Password 42!"}`))
	request.RemoteAddr = "203.0.113.8:443"
	request.Header.Set("X-Forwarded-For", "198.51.100.4")
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(withUser(request.Context(), admin))
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, request)
	if w.Code != http.StatusCreated || svc.actor.IP.String() != "203.0.113.8" {
		t.Fatalf("untrusted status=%d actor=%#v", w.Code, svc.actor)
	}
	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"student01","displayName":"甲","temporaryPassword":"Temporary Password 42!"}`))
	request.RemoteAddr = "10.1.2.3:443"
	request.Header.Set("X-Forwarded-For", "not-an-ip")
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(withUser(request.Context(), admin))
	w = httptest.NewRecorder()
	h.Routes().ServeHTTP(w, request)
	if w.Code != http.StatusBadRequest || svc.creates != 2 {
		t.Fatalf("status=%d creates=%d", w.Code, svc.creates)
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

type capturingHTTPService struct {
	fakeHTTPService
	actor   Principal
	creates int
}

func (s *capturingHTTPService) Create(_ context.Context, actor Principal, _ CreateInput) (auth.User, error) {
	s.actor = actor
	s.creates++
	return auth.User{ID: uuid.New(), Role: auth.RoleStudent}, nil
}
