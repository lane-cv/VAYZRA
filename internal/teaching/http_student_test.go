package teaching

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/redisx"
)

func TestStudentRoutesHideUnauthorizedLessonAndSetNoStore(t *testing.T) {
	lessonID := uuid.New()
	h := NewStudentHandler(&fakeStudentHTTPService{getErr: ErrNotFound}).Routes()
	r := httptest.NewRequest(http.MethodGet, "/lessons/"+lessonID.String(), nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) || w.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("status=%d cache=%q body=%s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}

	r = httptest.NewRequest(http.MethodGet, "/catalog", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("admin status=%d", w.Code)
	}
}

func TestStudentProgressJSONIsStrictAndBounded(t *testing.T) {
	svc := &fakeStudentHTTPService{}
	h := NewStudentHandler(svc).Routes()
	student := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	r := httptest.NewRequest(http.MethodPost, "/progress", strings.NewReader(`{"revisionId":"`+uuid.NewString()+`","viewed":true,"other":true}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.ContextWithUser(r.Context(), student))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || svc.progressCalled {
		t.Fatalf("unknown JSON status=%d called=%t", w.Code, svc.progressCalled)
	}
}

func TestStudentProgressRequiresVerifiedSessionBeforeDispatch(t *testing.T) {
	svc := &fakeStudentHTTPService{}
	h := NewStudentHandler(svc).Routes()
	r := httptest.NewRequest(http.MethodPost, "/progress", strings.NewReader(`{"revisionId":"`+uuid.NewString()+`","observedAt":"2026-07-18T02:00:00Z"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || svc.progressCalled {
		t.Fatalf("status=%d dispatched=%t body=%s", w.Code, svc.progressCalled, w.Body.String())
	}
}

func TestStudentProgressRateLimitDoesNotDispatch(t *testing.T) {
	svc := &fakeStudentHTTPService{}
	limiter := &fakeProgressWriteLimiter{decision: redisx.ProgressDecision{RetryAfter: 2 * time.Second}}
	studentHandler := NewStudentHandlerWithConfig(svc, StudentHTTPConfig{ProgressLimiter: limiter})
	authHandler := auth.NewHTTPHandler(&studentSessionAuth{sessionID: uuid.New()}, auth.HTTPConfig{})
	router := chi.NewRouter()
	router.Use(authHandler.Authenticate)
	router.Mount("/", studentHandler.Routes())
	r := httptest.NewRequest(http.MethodPost, "/progress", strings.NewReader(`{"revisionId":"`+uuid.NewString()+`","observedAt":"2026-07-18T02:00:00Z"}`))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "2" || svc.progressCalled || limiter.calls != 1 {
		t.Fatalf("status=%d retry=%q dispatched=%t calls=%d body=%s", w.Code, w.Header().Get("Retry-After"), svc.progressCalled, limiter.calls, w.Body.String())
	}
}

func TestStudentSearchCursorBoundsRejectBeforeServiceDispatch(t *testing.T) {
	svc := &fakeStudentHTTPService{}
	h := NewStudentHandler(svc).Routes()
	student := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	maxCursor := encodeStudentCursor(SearchCursor{SortKey: math.MinInt64, ID: uuid.New()})
	if len(maxCursor) != maxStudentCursorEncodedLength {
		t.Fatalf("max cursor length=%d want=%d", len(maxCursor), maxStudentCursorEncodedLength)
	}
	r := httptest.NewRequest(http.MethodGet, "/search?q=lesson&limit=1&cursor="+maxCursor, nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), student))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !svc.searchCalled {
		t.Fatalf("max valid cursor status=%d dispatched=%t body=%s", w.Code, svc.searchCalled, w.Body.String())
	}

	for _, cursor := range []string{strings.Repeat("A", maxStudentCursorEncodedLength+1), strings.Repeat("A", 32*1024)} {
		svc.searchCalled = false
		r = httptest.NewRequest(http.MethodGet, "/search?q=lesson&cursor="+cursor, nil)
		r = r.WithContext(auth.ContextWithUser(r.Context(), student))
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || svc.searchCalled || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("cursor length=%d status=%d dispatched=%t body=%s", len(cursor), w.Code, svc.searchCalled, w.Body.String())
		}
	}
}

type fakeStudentHTTPService struct {
	getErr         error
	progressCalled bool
	searchCalled   bool
}

func (*fakeStudentHTTPService) Browse(context.Context, Principal, BrowseInput) ([]CatalogNode, error) {
	return nil, nil
}
func (f *fakeStudentHTTPService) GetLesson(context.Context, Principal, uuid.UUID) (Revision, error) {
	return Revision{}, f.getErr
}
func (f *fakeStudentHTTPService) Search(context.Context, Principal, SearchInput) ([]Revision, SearchCursor, error) {
	f.searchCalled = true
	return nil, SearchCursor{}, nil
}
func (f *fakeStudentHTTPService) UpdateProgress(context.Context, Principal, ProgressInput) error {
	f.progressCalled = true
	return nil
}

var _ = errors.Is
var _ = time.Now

type fakeProgressWriteLimiter struct {
	decision redisx.ProgressDecision
	calls    int
}

func (f *fakeProgressWriteLimiter) AllowProgressWrite(context.Context, uuid.UUID) (redisx.ProgressDecision, error) {
	f.calls++
	return f.decision, nil
}

type studentSessionAuth struct{ sessionID uuid.UUID }

func (*studentSessionAuth) Login(context.Context, auth.LoginInput) (auth.Authentication, string, error) {
	return auth.Authentication{}, "", auth.ErrUnauthenticated
}
func (s *studentSessionAuth) Authenticate(context.Context, string) (auth.Authentication, error) {
	return auth.Authentication{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}, Session: auth.Session{ID: s.sessionID}}, nil
}
func (*studentSessionAuth) ChangePassword(context.Context, auth.ChangePasswordInput) (auth.Authentication, string, error) {
	return auth.Authentication{}, "", auth.ErrUnauthenticated
}
func (*studentSessionAuth) Logout(context.Context, string) error       { return nil }
func (*studentSessionAuth) LogoutOthers(context.Context, string) error { return nil }
