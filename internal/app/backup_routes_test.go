package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/operations"
)

type appAdminBackups struct {
	creates int
}

func (s *appAdminBackups) RequestManual(
	_ context.Context,
	_ operations.Principal,
	_ string,
) (backup.Run, error) {
	s.creates++
	return backup.Run{
		ID:      uuid.MustParse("20000000-0000-4000-8000-000000000001"),
		Trigger: backup.TriggerManual, State: backup.StateQueued,
		RequestedAt: time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC),
	}, nil
}

func (*appAdminBackups) List(
	context.Context,
	operations.Principal,
	backup.Filter,
) (backup.Page, error) {
	return backup.Page{Items: []backup.RunSummary{}}, nil
}

func (*appAdminBackups) Get(
	context.Context,
	operations.Principal,
	uuid.UUID,
) (backup.RunDetail, error) {
	return backup.RunDetail{}, backup.ErrNotFound
}

func TestApplicationMountsAdminBackupAPIsWithOriginAndCSRF(t *testing.T) {
	service := &appAdminBackups{}
	handler := New(Dependencies{
		Auth: &appAdminAuth{}, AdminBackups: service,
		PublicOrigin: "https://learn.example.com",
	})
	list := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/operations/backups",
		nil,
	)
	list.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	listResult := httptest.NewRecorder()
	handler.ServeHTTP(listResult, list)
	if listResult.Code != http.StatusOK ||
		listResult.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("status=%d headers=%v body=%s", listResult.Code, listResult.Header(), listResult.Body.String())
	}

	for name, configure := range map[string]func(*http.Request){
		"cross origin": func(request *http.Request) {
			request.Header.Set("Origin", "https://evil.example")
			request.Header.Set("X-CSRF-Token", "csrf-token")
			request.AddCookie(&http.Cookie{Name: "hl_csrf", Value: "csrf-token"})
		},
		"missing CSRF": func(request *http.Request) {
			request.Header.Set("Origin", "https://learn.example.com")
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/admin/operations/backups",
				nil,
			)
			request.Header.Set("Idempotency-Key", "manual:key-123")
			request.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
			configure(request)
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != http.StatusForbidden || service.creates != 0 {
				t.Fatalf("status=%d creates=%d body=%s", result.Code, service.creates, result.Body.String())
			}
		})
	}

	valid := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/operations/backups",
		nil,
	)
	valid.Header.Set("Idempotency-Key", "manual:key-123")
	valid.Header.Set("Origin", "https://learn.example.com")
	valid.Header.Set("X-CSRF-Token", "csrf-token")
	valid.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	valid.AddCookie(&http.Cookie{Name: "hl_csrf", Value: "csrf-token"})
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, valid)
	if result.Code != http.StatusAccepted || service.creates != 1 ||
		!strings.Contains(result.Body.String(), `"state":"queued"`) {
		t.Fatalf("status=%d creates=%d body=%s", result.Code, service.creates, result.Body.String())
	}
}
