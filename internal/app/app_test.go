package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/teaching"
)

func TestApplicationMountsStudentTeachingRoutes(t *testing.T) {
	h := New(Dependencies{Auth: &appStudentAuth{}, StudentTeaching: &appStudentTeaching{}})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/student/catalog", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func TestLivenessIncludesRequestID(t *testing.T) {
	h := New(Dependencies{Ready: func(context.Context) error { return nil }})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if w.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatal(w.Body.String())
	}
}

func TestReadinessReturnsStableError(t *testing.T) {
	h := New(Dependencies{Ready: func(context.Context) error { return context.DeadlineExceeded }})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("missing request ID")
	}
	if got := w.Body.String(); !strings.Contains(got, `"code":"not_ready"`) || !strings.Contains(got, `"message":"服务暂不可用"`) || !strings.Contains(got, `"requestId":`) {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestReadinessWithoutDependencyReturnsStableError(t *testing.T) {
	h := New(Dependencies{})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("missing request ID")
	}
	if got := w.Body.String(); !strings.Contains(got, `"code":"not_ready"`) || !strings.Contains(got, `"requestId":`) {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestAuthRoutesUseOriginAndCSRFProtection(t *testing.T) {
	h := New(Dependencies{Ready: func(context.Context) error { return nil }, Auth: &appFakeAuth{}, PublicOrigin: "https://learn.example.com", CookieSecure: true})
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://learn.example.com")
	loginResult := httptest.NewRecorder()
	h.ServeHTTP(loginResult, login)
	if loginResult.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginResult.Code, loginResult.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logout.Header.Set("Origin", "https://learn.example.com")
	logout.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	logoutResult := httptest.NewRecorder()
	h.ServeHTTP(logoutResult, logout)
	if logoutResult.Code != http.StatusForbidden || !strings.Contains(logoutResult.Body.String(), `"csrf_invalid"`) {
		t.Fatalf("logout status=%d body=%s", logoutResult.Code, logoutResult.Body.String())
	}

	crossSite := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	crossSite.Header.Set("Content-Type", "application/json")
	crossSite.Header.Set("Origin", "https://evil.example")
	crossSiteResult := httptest.NewRecorder()
	h.ServeHTTP(crossSiteResult, crossSite)
	if crossSiteResult.Code != http.StatusForbidden || !strings.Contains(crossSiteResult.Body.String(), `"forbidden"`) {
		t.Fatalf("cross-site status=%d body=%s", crossSiteResult.Code, crossSiteResult.Body.String())
	}
}

func TestAuthRoutesForwardTrustedProxyConfiguration(t *testing.T) {
	svc := &appFakeAuth{}
	h := New(Dependencies{
		Ready:             func(context.Context) error { return nil },
		Auth:              svc,
		PublicOrigin:      "https://learn.example.com",
		TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	r.RemoteAddr = "10.1.2.3:443"
	r.Header.Set("X-Forwarded-For", "198.51.100.4")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://learn.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK || svc.loginInput.IP == nil || svc.loginInput.IP.String() != "198.51.100.4" {
		t.Fatalf("status=%d input=%#v body=%s", w.Code, svc.loginInput, w.Body.String())
	}
}

func TestApplicationServesConsoleFromStaticFiles(t *testing.T) {
	h := New(Dependencies{StaticFiles: fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>console</html>")},
	}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/students", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "console") {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

type appFakeAuth struct {
	loginInput auth.LoginInput
}

func (a *appFakeAuth) Login(_ context.Context, input auth.LoginInput) (auth.Authentication, string, error) {
	a.loginInput = input
	return auth.Authentication{User: auth.User{ID: uuid.MustParse("84c0f591-e99a-4a91-8250-25c159e1823a"), Username: "student01", Role: auth.RoleStudent, Status: auth.StatusActive}}, "opaque-token", nil
}
func (*appFakeAuth) Authenticate(context.Context, string) (auth.Authentication, error) {
	return auth.Authentication{}, auth.ErrUnauthenticated
}
func (*appFakeAuth) ChangePassword(context.Context, auth.ChangePasswordInput) (auth.Authentication, string, error) {
	return auth.Authentication{}, "", auth.ErrUnauthenticated
}
func (*appFakeAuth) Logout(context.Context, string) error       { return nil }
func (*appFakeAuth) LogoutOthers(context.Context, string) error { return nil }

type appStudentAuth struct{ appFakeAuth }

func (*appStudentAuth) Authenticate(context.Context, string) (auth.Authentication, error) {
	return auth.Authentication{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}}, nil
}

type appStudentTeaching struct{}

func (*appStudentTeaching) Browse(context.Context, teaching.Principal, teaching.BrowseInput) ([]teaching.StudentCatalogNode, teaching.CatalogCursor, error) {
	return []teaching.StudentCatalogNode{}, teaching.CatalogCursor{}, nil
}
func (*appStudentTeaching) Recent(context.Context, teaching.Principal, int) ([]teaching.RecentLesson, error) {
	return nil, nil
}
func (*appStudentTeaching) GetLesson(context.Context, teaching.Principal, uuid.UUID) (teaching.StudentLesson, error) {
	return teaching.StudentLesson{}, nil
}
func (*appStudentTeaching) GetPosition(context.Context, teaching.Principal, uuid.UUID) (teaching.LessonProgress, error) {
	return teaching.LessonProgress{}, nil
}
func (*appStudentTeaching) Search(context.Context, teaching.Principal, teaching.SearchInput) ([]teaching.SearchResult, teaching.SearchCursor, error) {
	return nil, teaching.SearchCursor{}, nil
}
func (*appStudentTeaching) UpdateProgress(context.Context, teaching.Principal, teaching.ProgressInput) error {
	return nil
}
