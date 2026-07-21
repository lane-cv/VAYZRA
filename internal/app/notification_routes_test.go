package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/notifications"
)

type appNotifications struct {
	recipient    uuid.UUID
	markAllCalls int
}

func (*appNotifications) List(context.Context, uuid.UUID, notifications.Cursor) ([]notifications.Notification, notifications.Cursor, error) {
	return []notifications.Notification{}, notifications.Cursor{}, nil
}
func (s *appNotifications) UnreadCount(_ context.Context, id uuid.UUID) (int64, error) {
	s.recipient = id
	return 2, nil
}
func (*appNotifications) MarkRead(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (s *appNotifications) MarkAllRead(context.Context, uuid.UUID) (int64, error) {
	s.markAllCalls++
	return 0, nil
}

func TestNotificationRoutesMountForAuthenticatedRoles(t *testing.T) {
	svc := &appNotifications{}
	h := New(Dependencies{Auth: &appStudentAuth{}, Notifications: svc})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-count", nil)
	r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || svc.recipient == uuid.Nil || !strings.Contains(w.Body.String(), `"count":2`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestNotificationMutationsRequireApplicationOriginAndCSRF(t *testing.T) {
	for _, tc := range []struct {
		name         string
		origin, csrf string
		want, calls  int
	}{{"valid", "https://learn.example.com", "token", 200, 1}, {"missing origin", "", "token", 403, 0}, {"missing csrf", "https://learn.example.com", "", 403, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &appNotifications{}
			h := New(Dependencies{Auth: &appStudentAuth{}, Notifications: svc, PublicOrigin: "https://learn.example.com"})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/read-all", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			r.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
			if tc.csrf != "" {
				r.Header.Set("X-CSRF-Token", tc.csrf)
				r.AddCookie(&http.Cookie{Name: "hl_csrf", Value: tc.csrf})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || svc.markAllCalls != tc.calls {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, svc.markAllCalls, w.Body.String())
			}
		})
	}
}
