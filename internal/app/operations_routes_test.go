package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/operations"
)

type appAdminOperations struct {
	puts int
	acks int
}

func (*appAdminOperations) GetSettings(context.Context, operations.Principal) (operations.Settings, error) {
	settings := operations.Settings{
		Version: 1, SiteName: "HappyLearn", SoftDeleteRetentionDays: 30,
		AuditRetentionDays: 365, OperationalSampleRetentionDays: 7,
		BackupHour: 3, BackupTimezone: "Asia/Shanghai",
		DiskWarningPercent: 75, DiskCriticalPercent: 90,
		AIErrorWarningPercent: 10, AIErrorCriticalPercent: 25,
		ProcessingQueueWarning: 20, ProcessingQueueCritical: 100,
	}
	return settings, nil
}

func (s *appAdminOperations) UpdateSettings(_ context.Context, _ operations.Principal, settings operations.Settings) (operations.Settings, error) {
	s.puts++
	settings.Version++
	return settings, nil
}

func (*appAdminOperations) ListAudit(context.Context, operations.Principal, audit.AuditFilter) (audit.AuditPage, error) {
	return audit.AuditPage{Items: []audit.Record{}}, nil
}

func (*appAdminOperations) GetDashboard(context.Context, operations.Principal) (operations.Dashboard, error) {
	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	return operations.Dashboard{
		ObservedAt:       now,
		Services:         []operations.ServiceHealth{},
		Queues:           []operations.QueueSummary{},
		RecentAuditState: operations.DataStateEmpty,
		RecentAudit:      []operations.AuditSummary{},
	}, nil
}

func (*appAdminOperations) ListAlerts(
	context.Context,
	operations.Principal,
	operations.AlertFilter,
) (operations.AlertPage, error) {
	return operations.AlertPage{Items: []operations.Alert{}}, nil
}

func (service *appAdminOperations) AcknowledgeAlert(
	_ context.Context,
	_ operations.Principal,
	id uuid.UUID,
) (operations.Alert, error) {
	service.acks++
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	return operations.Alert{
		ID: id, DedupeKey: "processing_queue_depth", Category: "processing",
		Severity:        operations.AlertSeverityWarning,
		State:           operations.AlertStateAcknowledged,
		FirstObservedAt: now.Add(-time.Minute), LastObservedAt: now,
		CurrentValue: 21, ThresholdValue: 20,
		Summary: "Processing queue depth is high",
	}, nil
}

func TestApplicationMountsAdminOperationsWithOriginAndCSRF(t *testing.T) {
	service := &appAdminOperations{}
	handler := New(Dependencies{
		Auth: &appAdminAuth{}, AdminOperations: service,
		PublicOrigin: "https://learn.example.com",
	})
	for _, path := range []string{
		"/api/v1/admin/operations/dashboard",
		"/api/v1/admin/operations/settings",
		"/api/v1/admin/operations/audit",
		"/api/v1/admin/operations/alerts",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != http.StatusOK ||
			result.Header().Get("Cache-Control") != "no-store, private" {
			t.Fatalf("%s status=%d headers=%v body=%s", path, result.Code, result.Header(), result.Body.String())
		}
	}

	body := `{
		"version":1,"siteName":"HappyLearn","siteAnnouncement":"",
		"softDeleteRetentionDays":30,"auditRetentionDays":365,
		"operationalSampleRetentionDays":7,"backupHour":3,"backupMinute":0,
		"backupTimezone":"Asia/Shanghai","diskWarningPercent":75,
		"diskCriticalPercent":90,"aiErrorWarningPercent":10,
		"aiErrorCriticalPercent":25,"processingQueueWarning":20,
		"processingQueueCritical":100
	}`
	for name, configure := range map[string]func(*http.Request){
		"cross origin": func(request *http.Request) {
			request.Header.Set("Origin", "https://evil.example")
			request.AddCookie(&http.Cookie{Name: "hl_csrf", Value: "csrf-token"})
			request.Header.Set("X-CSRF-Token", "csrf-token")
		},
		"missing CSRF": func(request *http.Request) {
			request.Header.Set("Origin", "https://learn.example.com")
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/operations/settings", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
			configure(request)
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != http.StatusForbidden || service.puts != 0 {
				t.Fatalf("status=%d puts=%d body=%s", result.Code, service.puts, result.Body.String())
			}
		})
	}

	valid := httptest.NewRequest(http.MethodPut, "/api/v1/admin/operations/settings", strings.NewReader(body))
	valid.Header.Set("Content-Type", "application/json")
	valid.Header.Set("Origin", "https://learn.example.com")
	valid.Header.Set("X-CSRF-Token", "csrf-token")
	valid.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	valid.AddCookie(&http.Cookie{Name: "hl_csrf", Value: "csrf-token"})
	validResult := httptest.NewRecorder()
	handler.ServeHTTP(validResult, valid)
	if validResult.Code != http.StatusOK || service.puts != 1 {
		t.Fatalf("status=%d puts=%d body=%s", validResult.Code, service.puts, validResult.Body.String())
	}
}

func TestApplicationProtectsAlertAcknowledgementWithAdminOriginAndCSRF(t *testing.T) {
	service := &appAdminOperations{}
	handler := New(Dependencies{
		Auth: &appAdminAuth{}, AdminOperations: service,
		PublicOrigin: "https://learn.example.com",
	})
	id := uuid.MustParse("1abc0000-0000-4000-8000-000000000001")
	target := "/api/v1/admin/operations/alerts/" + id.String() + "/acknowledge"
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
			request := httptest.NewRequest(http.MethodPost, target, nil)
			request.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
			configure(request)
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != http.StatusForbidden || service.acks != 0 {
				t.Fatalf(
					"status=%d acks=%d body=%s",
					result.Code,
					service.acks,
					result.Body.String(),
				)
			}
		})
	}
	valid := httptest.NewRequest(http.MethodPost, target, nil)
	valid.Header.Set("Origin", "https://learn.example.com")
	valid.Header.Set("X-CSRF-Token", "csrf-token")
	valid.AddCookie(&http.Cookie{Name: "hl_session", Value: "opaque-token"})
	valid.AddCookie(&http.Cookie{Name: "hl_csrf", Value: "csrf-token"})
	validResult := httptest.NewRecorder()
	handler.ServeHTTP(validResult, valid)
	if validResult.Code != http.StatusOK || service.acks != 1 ||
		!strings.Contains(validResult.Body.String(), `"state":"acknowledged"`) {
		t.Fatalf(
			"status=%d acks=%d body=%s",
			validResult.Code,
			service.acks,
			validResult.Body.String(),
		)
	}
}
