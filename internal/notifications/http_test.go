package notifications

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type fakeHTTPService struct {
	recipient, notification uuid.UUID
	calls                   string
	cursor                  Cursor
	markErr                 error
}

func (f *fakeHTTPService) List(_ context.Context, _ uuid.UUID, cursor Cursor) ([]Notification, Cursor, error) {
	f.calls = "list"
	f.cursor = cursor
	return []Notification{}, Cursor{}, nil
}
func (f *fakeHTTPService) UnreadCount(_ context.Context, id uuid.UUID) (int64, error) {
	f.recipient = id
	f.calls = "count"
	return 4, nil
}
func (f *fakeHTTPService) MarkRead(_ context.Context, r, id uuid.UUID) error {
	f.recipient = r
	f.notification = id
	f.calls = "read"
	return f.markErr
}
func (f *fakeHTTPService) MarkAllRead(_ context.Context, r uuid.UUID) (int64, error) {
	f.recipient = r
	f.calls = "all"
	return 3, nil
}

func TestHTTPAllowsBothActiveAuthenticatedRolesAndDoesNotExposeRecipient(t *testing.T) {
	for _, role := range []auth.Role{auth.RoleStudent, auth.RoleAdmin} {
		svc := &fakeHTTPService{}
		req := httptest.NewRequest(http.MethodGet, "/unread-count", nil)
		id := uuid.New()
		req = req.WithContext(auth.ContextWithUser(req.Context(), auth.User{ID: id, Role: role, Status: auth.StatusActive}))
		w := httptest.NewRecorder()
		NewHandler(svc).Routes().ServeHTTP(w, req)
		if w.Code != 200 || svc.recipient != id || strings.Contains(w.Body.String(), id.String()) {
			t.Fatalf("role=%s status=%d body=%s", role, w.Code, w.Body.String())
		}
	}
}

func TestHTTPMarkReadRequiresExactEmptyJSONObject(t *testing.T) {
	id := uuid.New()
	for _, tc := range []struct {
		body, ct string
		want     int
	}{{`{}`, "application/json", 200}, {``, "application/json", 400}, {`[]`, "application/json", 400}, {`{"x":1}`, "application/json", 400}, {`{}`, "text/plain", 415}} {
		svc := &fakeHTTPService{}
		req := httptest.NewRequest(http.MethodPost, "/"+id.String()+"/read", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.ct)
		req = req.WithContext(auth.ContextWithUser(req.Context(), auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}))
		w := httptest.NewRecorder()
		NewHandler(svc).Routes().ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("body=%q status=%d response=%s", tc.body, w.Code, w.Body.String())
		}
	}
}

func TestHTTPRejectsUnknownOrDuplicateListQuery(t *testing.T) {
	for _, url := range []string{"/?other=x", "/?limit=1&limit=2", "/?limit=0", "/?cursor="} {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(auth.ContextWithUser(req.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
		w := httptest.NewRecorder()
		NewHandler(&fakeHTTPService{}).Routes().ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("url=%s status=%d body=%s", url, w.Code, w.Body.String())
		}
	}
}
