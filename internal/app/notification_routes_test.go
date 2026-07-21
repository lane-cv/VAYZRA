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

type appNotifications struct{ recipient uuid.UUID }

func (*appNotifications) List(context.Context, uuid.UUID, notifications.Cursor) ([]notifications.Notification, notifications.Cursor, error) {
	return []notifications.Notification{}, notifications.Cursor{}, nil
}
func (s *appNotifications) UnreadCount(_ context.Context, id uuid.UUID) (int64, error) {
	s.recipient = id
	return 2, nil
}
func (*appNotifications) MarkRead(context.Context, uuid.UUID, uuid.UUID) error  { return nil }
func (*appNotifications) MarkAllRead(context.Context, uuid.UUID) (int64, error) { return 0, nil }

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
