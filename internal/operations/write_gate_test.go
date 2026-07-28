package operations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type recordingWriteGate struct {
	mu     sync.Mutex
	err    error
	events []string
}

type writeGateFunc func(context.Context) (func(), error)

func (f writeGateFunc) AcquireShared(ctx context.Context) (func(), error) {
	return f(ctx)
}

func (g *recordingWriteGate) AcquireShared(context.Context) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events, "acquire")
	if g.err != nil {
		return nil, g.err
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.events = append(g.events, "release")
	}, nil
}

func (g *recordingWriteGate) record(event string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.events = append(g.events, event)
}

func (g *recordingWriteGate) snapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.events...)
}

func TestOperationalGateBypassesOnlySafeMethodsAndExactLogoutPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		method     string
		path       string
		wantEvents []string
	}{
		{name: "get", method: http.MethodGet, path: "/api/v1/student/catalog", wantEvents: []string{"handler"}},
		{name: "head", method: http.MethodHead, path: "/api/v1/student/catalog", wantEvents: []string{"handler"}},
		{name: "options", method: http.MethodOptions, path: "/api/v1/student/catalog", wantEvents: []string{"handler"}},
		{name: "logout", method: http.MethodPost, path: "/api/v1/auth/logout", wantEvents: []string{"handler"}},
		{name: "unsafe", method: http.MethodPost, path: "/api/v1/student/progress", wantEvents: []string{"acquire", "handler", "release"}},
		{name: "logout others is gated", method: http.MethodPost, path: "/api/v1/auth/logout-others", wantEvents: []string{"acquire", "handler", "release"}},
		{name: "logout trailing slash is gated", method: http.MethodPost, path: "/api/v1/auth/logout/", wantEvents: []string{"acquire", "handler", "release"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate := &recordingWriteGate{}
			handler := UnsafeWriteGate(gate)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				gate.record("handler")
				w.WriteHeader(http.StatusNoContent)
			}))
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, httptest.NewRequest(tc.method, tc.path, nil))
			if result.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
			}
			if got := strings.Join(gate.snapshot(), ","); got != strings.Join(tc.wantEvents, ",") {
				t.Fatalf("events=%v want=%v", gate.snapshot(), tc.wantEvents)
			}
		})
	}
}

func TestMissingOperationalGateFailsClosedOnlyForUnsafeWrites(t *testing.T) {
	for _, tc := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCalled bool
	}{
		{
			name: "safe method", method: http.MethodGet,
			path:       "/api/v1/student/catalog",
			wantStatus: http.StatusNoContent, wantCalled: true,
		},
		{
			name: "logout", method: http.MethodPost,
			path:       logoutPath,
			wantStatus: http.StatusNoContent, wantCalled: true,
		},
		{
			name: "unsafe write", method: http.MethodPost,
			path:       "/api/v1/student/progress",
			wantStatus: http.StatusServiceUnavailable, wantCalled: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := UnsafeWriteGate(nil)(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					called = true
					w.WriteHeader(http.StatusNoContent)
				},
			))
			result := httptest.NewRecorder()
			handler.ServeHTTP(
				result,
				httptest.NewRequest(tc.method, tc.path, nil),
			)
			if result.Code != tc.wantStatus || called != tc.wantCalled {
				t.Fatalf(
					"status=%d called=%t want_status=%d want_called=%t body=%s",
					result.Code,
					called,
					tc.wantStatus,
					tc.wantCalled,
					result.Body.String(),
				)
			}
		})
	}
}

func TestOperationalGateDenialReturnsStableMaintenanceResponse(t *testing.T) {
	for _, gateErr := range []error{ErrLeaseHeld, errors.New("secret gate detail")} {
		gate := &recordingWriteGate{err: gateErr}
		called := false
		handler := UnsafeWriteGate(gate)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called = true
		}))
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings", nil))
		if called || result.Code != http.StatusServiceUnavailable ||
			!strings.Contains(result.Body.String(), `"code":"maintenance_mode"`) ||
			strings.Contains(result.Body.String(), "secret gate detail") {
			t.Fatalf("called=%t status=%d body=%s", called, result.Code, result.Body.String())
		}
	}

	t.Run("missing release", func(t *testing.T) {
		called := false
		handler := UnsafeWriteGate(writeGateFunc(func(context.Context) (func(), error) {
			return nil, nil
		}))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			called = true
		}))
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, httptest.NewRequest(http.MethodPost, "/api/v1/student/progress", nil))
		if called || result.Code != http.StatusServiceUnavailable ||
			!strings.Contains(result.Body.String(), `"code":"maintenance_mode"`) {
			t.Fatalf("called=%t status=%d body=%s", called, result.Code, result.Body.String())
		}
	})
}

func TestOperationalGateReleasesBeforeOuterRecovererCompletes(t *testing.T) {
	gate := &recordingWriteGate{}
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Use(UnsafeWriteGate(gate))
	router.Post("/api/v1/panic", func(http.ResponseWriter, *http.Request) {
		gate.record("handler")
		panic("boom")
	})
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest(http.MethodPost, "/api/v1/panic", nil))
	if result.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
	if got := strings.Join(gate.snapshot(), ","); got != "acquire,handler,release" {
		t.Fatalf("events=%s", got)
	}
}
