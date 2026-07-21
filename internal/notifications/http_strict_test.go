package notifications

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

func notificationRequest(method, target, body string, user *auth.User) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if user != nil {
		req = req.WithContext(auth.ContextWithUser(req.Context(), *user))
	}
	return req
}

func serveNotification(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	httpx.RequestID(h).ServeHTTP(w, req)
	return w
}

func activeNotificationUser() *auth.User {
	return &auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
}

func assertNotificationJSONHeaders(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Header().Get("Cache-Control") != "no-store, private" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("headers=%v", w.Header())
	}
	if !strings.Contains(w.Body.String(), `"requestId":"`) {
		t.Fatalf("missing requestId: %s", w.Body.String())
	}
}

func TestHTTPListStrictCursorAndLimitMatrix(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	id := uuid.New()
	valid := encodeNotificationCursor(Cursor{CreatedAt: now.Add(-time.Minute), ID: id})
	raw := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	tests := []struct {
		name, url string
		want      int
		limit     int
	}{
		{"default", "/", 200, 20}, {"limit one", "/?limit=1", 200, 1}, {"limit hundred", "/?limit=100", 200, 100}, {"valid cursor", "/?cursor=" + valid, 200, 20},
		{"zero", "/?limit=0", 400, 0}, {"over", "/?limit=101", 400, 0}, {"leading zero", "/?limit=01", 400, 0}, {"duplicate", "/?limit=1&limit=2", 400, 0}, {"empty limit", "/?limit=", 400, 0}, {"empty cursor", "/?cursor=", 400, 0}, {"unknown", "/?x=1", 400, 0},
		{"padded cursor", "/?cursor=" + valid + "%3D", 400, 0}, {"cursor unknown", "/?cursor=" + raw(`{"createdAt":"2026-07-22T11:59:00Z","id":"`+id.String()+`","x":1}`), 400, 0}, {"future", "/?cursor=" + raw(`{"createdAt":"2026-07-23T11:59:00Z","id":"`+id.String()+`"}`), 400, 0}, {"uppercase uuid", "/?cursor=" + raw(`{"createdAt":"2026-07-22T11:59:00Z","id":"`+strings.ToUpper(id.String())+`"}`), 400, 0}, {"garbage", "/?cursor=not_base64!", 400, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeHTTPService{}
			h := NewHandler(svc)
			h.now = func() time.Time { return now }
			w := serveNotification(h.Routes(), notificationRequest(http.MethodGet, tc.url, "", activeNotificationUser()))
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.want == 200 && svc.cursor.Limit != tc.limit {
				t.Fatalf("cursor=%+v", svc.cursor)
			}
		})
	}
}

func TestHTTPMutationStrictBodyAndContentTypeMatrix(t *testing.T) {
	id := uuid.New()
	large := `{}` + strings.Repeat(" ", maxRequestBody)
	for _, path := range []string{"/" + id.String() + "/read", "/read-all"} {
		for _, tc := range []struct {
			name, body string
			types      []string
			want       int
		}{
			{"valid", `{}`, []string{"application/json"}, 200}, {"valid charset", ` { } `, []string{"application/json; charset=utf-8"}, 200}, {"empty", "", []string{"application/json"}, 400}, {"array", `[]`, []string{"application/json"}, 400}, {"unknown", `{"x":1}`, []string{"application/json"}, 400}, {"multiple", `{}{}`, []string{"application/json"}, 400}, {"trailing", `{} x`, []string{"application/json"}, 400}, {"too large", large, []string{"application/json"}, 413}, {"missing type", `{}`, nil, 415}, {"wrong type", `{}`, []string{"text/plain"}, 415}, {"duplicate type", `{}`, []string{"application/json", "application/json"}, 415},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				req := notificationRequest(http.MethodPost, path, tc.body, activeNotificationUser())
				for _, v := range tc.types {
					req.Header.Add("Content-Type", v)
				}
				w := serveNotification(NewHandler(&fakeHTTPService{}).Routes(), req)
				if w.Code != tc.want {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestHTTPUniformNotFoundAuthenticationAndRouteErrors(t *testing.T) {
	id := uuid.New()
	svc := &fakeHTTPService{markErr: ErrNotFound}
	for _, path := range []string{"/not-a-uuid/read", "/" + id.String() + "/read"} {
		req := notificationRequest(http.MethodPost, path, `{}`, activeNotificationUser())
		req.Header.Set("Content-Type", "application/json")
		w := serveNotification(NewHandler(svc).Routes(), req)
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
			t.Fatalf("path=%s status=%d body=%s", path, w.Code, w.Body.String())
		}
		assertNotificationJSONHeaders(t, w)
	}
	for _, tc := range []struct {
		name string
		user *auth.User
		want int
	}{{"unauthenticated", nil, 401}, {"inactive", &auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusDisabled}, 403}} {
		w := serveNotification(NewHandler(&fakeHTTPService{}).Routes(), notificationRequest(http.MethodGet, "/unread-count", "", tc.user))
		if w.Code != tc.want {
			t.Fatalf("%s status=%d", tc.name, w.Code)
		}
		assertNotificationJSONHeaders(t, w)
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{{http.MethodGet, "/missing", 404}, {http.MethodDelete, "/unread-count", 405}} {
		w := serveNotification(NewHandler(&fakeHTTPService{}).Routes(), notificationRequest(tc.method, tc.path, "", activeNotificationUser()))
		if w.Code != tc.want {
			t.Fatalf("%s %s status=%d", tc.method, tc.path, w.Code)
		}
		assertNotificationJSONHeaders(t, w)
	}
}
