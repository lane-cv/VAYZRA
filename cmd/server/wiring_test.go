package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/config"
)

func TestBuildApplicationWiresAuthRoutesAndConfiguredSecurity(t *testing.T) {
	var openedURL string
	migrated := false
	closed := false
	h, closeResources, err := buildApplication(context.Background(), config.Config{
		DatabaseURL:  "postgres://app:secret@db.example/happylearn",
		PublicOrigin: "https://learn.example.com",
		CookieSecure: true,
	}, applicationDependencies{
		open:    func(_ context.Context, url string) (*pgxpool.Pool, error) { openedURL = url; return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { migrated = true; return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		close:   func(*pgxpool.Pool) { closed = true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if openedURL != "postgres://app:secret@db.example/happylearn" || !migrated {
		t.Fatalf("opened=%q migrated=%t", openedURL, migrated)
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"student01","password":"Long Temporary Password 42!"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://learn.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !hasSecureSessionCookie(w.Result().Cookies()) {
		t.Fatal("login route did not receive CookieSecure configuration")
	}
	closeResources()
	if !closed {
		t.Fatal("resources not closed")
	}
}

func TestBuildApplicationForwardsTrustedProxyCIDRs(t *testing.T) {
	svc := &serverCapturingAuth{}
	h, closeResources, err := buildApplication(context.Background(), config.Config{
		DatabaseURL:       "postgres://app:secret@db.example/happylearn",
		PublicOrigin:      "https://learn.example.com",
		TrustedProxyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return svc, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		close:   func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeResources)
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

func TestBuildApplicationClosesPoolAndHidesMigrationFailure(t *testing.T) {
	closed := false
	secret := "postgres://app:very-secret@db.example/happylearn"
	_, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return errors.New(secret) },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { t.Fatal("newAuth should not run"); return nil, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return nil },
		close:   func(*pgxpool.Pool) { closed = true },
	})
	if closeResources != nil || err == nil || strings.Contains(err.Error(), secret) || !closed {
		t.Fatalf("closeResourcesNil=%t error=%v", closeResources == nil, err)
	}
}

type serverCapturingAuth struct {
	serverFakeAuth
	loginInput auth.LoginInput
}

func (s *serverCapturingAuth) Login(_ context.Context, input auth.LoginInput) (auth.Authentication, string, error) {
	s.loginInput = input
	return serverFakeAuth{}.Login(context.Background(), input)
}

type serverFakeAuth struct{}

func (serverFakeAuth) Login(context.Context, auth.LoginInput) (auth.Authentication, string, error) {
	return auth.Authentication{User: auth.User{ID: uuid.MustParse("84c0f591-e99a-4a91-8250-25c159e1823a"), Username: "student01", Role: auth.RoleStudent, Status: auth.StatusActive}}, "opaque-token", nil
}
func (serverFakeAuth) Authenticate(context.Context, string) (auth.Authentication, error) {
	return auth.Authentication{}, auth.ErrUnauthenticated
}
func (serverFakeAuth) ChangePassword(context.Context, auth.ChangePasswordInput) (auth.Authentication, string, error) {
	return auth.Authentication{}, "", auth.ErrUnauthenticated
}
func (serverFakeAuth) Logout(context.Context, string) error       { return nil }
func (serverFakeAuth) LogoutOthers(context.Context, string) error { return nil }

func hasSecureSessionCookie(cookies []*http.Cookie) bool {
	for _, cookie := range cookies {
		if cookie.Name == "hl_session" && cookie.Secure && cookie.HttpOnly {
			return true
		}
	}
	return false
}

func TestBuildApplicationReadyRequiresObjectStoreAndHidesFailure(t *testing.T) {
	called := false
	h, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		objectReady: func(context.Context, config.Config) (func(context.Context) error, error) {
			called = true
			return func(context.Context) error { return errors.New("private object endpoint secret") }, nil
		},
		close: func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeResources()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	if !called || w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("called=%t status=%d body=%s", called, w.Code, w.Body.String())
	}
}

func TestReadyHandlerReturnsWhenSharedDependencyBudgetExpires(t *testing.T) {
	cancelled := make(chan struct{})
	h, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		objectReady: func(context.Context, config.Config) (func(context.Context) error, error) {
			return func(ctx context.Context) error { <-ctx.Done(); close(cancelled); return ctx.Err() }, nil
		},
		readinessTimeout: 20 * time.Millisecond,
		close:            func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeResources()
	started := time.Now()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health/ready", nil))
	if w.Code != http.StatusServiceUnavailable || time.Since(started) > 500*time.Millisecond || strings.Contains(w.Body.String(), "deadline") {
		t.Fatalf("status=%d elapsed=%s body=%s", w.Code, time.Since(started), w.Body.String())
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("blocking object readiness context not cancelled")
	}
}
