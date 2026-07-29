package operations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

type dashboardServiceReaderStub struct {
	dashboard Dashboard
	err       error
	calls     int
}

func (stub *dashboardServiceReaderStub) Assemble(context.Context) (Dashboard, error) {
	stub.calls++
	return cloneDashboard(stub.dashboard), stub.err
}

type dashboardHTTPServiceStub struct {
	dashboard Dashboard
	err       error
	calls     int
}

func (*dashboardHTTPServiceStub) GetSettings(context.Context, Principal) (Settings, error) {
	return validSettings(), nil
}

func (*dashboardHTTPServiceStub) UpdateSettings(
	_ context.Context,
	_ Principal,
	settings Settings,
) (Settings, error) {
	return settings, nil
}

func (*dashboardHTTPServiceStub) ListAudit(
	context.Context,
	Principal,
	audit.AuditFilter,
) (audit.AuditPage, error) {
	return audit.AuditPage{Items: []audit.Record{}}, nil
}

func (stub *dashboardHTTPServiceStub) GetDashboard(
	context.Context,
	Principal,
) (Dashboard, error) {
	stub.calls++
	return cloneDashboard(stub.dashboard), stub.err
}

func TestDashboardServiceRequiresActiveAdminAndConfiguredAssembler(t *testing.T) {
	now := sampleTestClock()
	store := &fakeStore{settings: validSettings()}
	auditReader := &operationsAuditReader{page: audit.AuditPage{Items: []audit.Record{}}}
	dashboardReader := &dashboardServiceReaderStub{dashboard: dashboardHTTPFixture(now)}
	service, err := NewServiceWithDashboard(store, auditReader, dashboardReader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.GetDashboard(context.Background(), operationsAdmin(uuid.New()))
	if err != nil {
		t.Fatal(err)
	}
	if dashboardReader.calls != 1 || !got.ObservedAt.Equal(now) {
		t.Fatalf("calls=%d dashboard=%+v", dashboardReader.calls, got)
	}

	student := operationsAdmin(uuid.New())
	student.User.Role = auth.RoleStudent
	if _, err := service.GetDashboard(context.Background(), student); !errors.Is(err, ErrForbidden) {
		t.Fatalf("student error=%v", err)
	}
	if dashboardReader.calls != 1 {
		t.Fatalf("unauthorized request reached assembler: %d", dashboardReader.calls)
	}

	for _, test := range []struct {
		name      string
		store     ServiceStore
		audit     audit.FilteredReader
		dashboard DashboardReader
	}{
		{name: "nil store", audit: auditReader, dashboard: dashboardReader},
		{name: "nil audit", store: store, dashboard: dashboardReader},
		{name: "nil dashboard", store: store, audit: auditReader},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := NewServiceWithDashboard(test.store, test.audit, test.dashboard)
			if !errors.Is(err, ErrInvalid) || got != nil {
				t.Fatalf("service=%v error=%v", got, err)
			}
		})
	}
}

func TestOperationsHTTPDashboardIsAdminOnlyStrictNoStoreAndAggregateOnly(t *testing.T) {
	now := sampleTestClock()
	stub := &dashboardHTTPServiceStub{dashboard: dashboardHTTPFixture(now)}
	handler := NewAdminHandler(stub, nil).Routes()

	result := httptest.NewRecorder()
	handler.ServeHTTP(
		result,
		operationsHTTPRequest(http.MethodGet, "/dashboard", "", auth.RoleAdmin),
	)
	if result.Code != http.StatusOK ||
		result.Header().Get("Cache-Control") != "no-store, private" ||
		stub.calls != 1 {
		t.Fatalf(
			"status=%d calls=%d headers=%v body=%s",
			result.Code,
			stub.calls,
			result.Header(),
			result.Body.String(),
		)
	}
	body := result.Body.String()
	for _, field := range []string{
		`"observedAt":"2026-07-29T07:00:00Z"`,
		`"students":{"state":"healthy"`,
		`"questions":{"state":"healthy"`,
		`"ai":{"state":"healthy"`,
		`"storage":{"state":"healthy"`,
		`"services":[`,
		`"service":"app"`,
		`"queues":[`,
		`"queue":"processing"`,
		`"backup":{"state":"healthy"`,
		`"alerts":{"state":"healthy"`,
		`"recentAuditState":"healthy"`,
		`"category":"operations"`,
		`"outcome":"succeeded"`,
	} {
		if !strings.Contains(body, field) {
			t.Fatalf("missing %s in %s", field, body)
		}
	}
	for _, forbidden := range []string{
		"studentName", "userId", "objectKey", "fileName", "prompt",
		"response", "requestId", "traceId", "databaseURL", "secret",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unsafe field %q in %s", forbidden, body)
		}
	}

	for _, target := range []string{
		"/dashboard?",
		"/dashboard?unknown=value",
		"/dashboard?unknown=",
	} {
		before := stub.calls
		invalid := httptest.NewRecorder()
		handler.ServeHTTP(
			invalid,
			operationsHTTPRequest(http.MethodGet, target, "", auth.RoleAdmin),
		)
		if invalid.Code != http.StatusBadRequest || stub.calls != before {
			t.Fatalf(
				"target=%q status=%d calls=%d body=%s",
				target,
				invalid.Code,
				stub.calls,
				invalid.Body.String(),
			)
		}
		assertOperationsErrorEnvelope(t, invalid, "invalid_request")
	}

	student := httptest.NewRecorder()
	handler.ServeHTTP(
		student,
		operationsHTTPRequest(http.MethodGet, "/dashboard", "", auth.RoleStudent),
	)
	if student.Code != http.StatusForbidden || stub.calls != 1 {
		t.Fatalf("student status=%d calls=%d body=%s", student.Code, stub.calls, student.Body.String())
	}
	assertOperationsErrorEnvelope(t, student, "forbidden")
}

func TestOperationsHTTPDashboardInternalFailureIsUniformAndSanitized(t *testing.T) {
	stub := &dashboardHTTPServiceStub{err: errors.New("secret database detail")}
	handler := NewAdminHandler(stub, nil).Routes()
	result := httptest.NewRecorder()
	handler.ServeHTTP(
		result,
		operationsHTTPRequest(http.MethodGet, "/dashboard", "", auth.RoleAdmin),
	)
	if result.Code != http.StatusInternalServerError ||
		strings.Contains(result.Body.String(), "secret") {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
	assertOperationsErrorEnvelope(t, result, "internal_error")
}

func dashboardHTTPFixture(now time.Time) Dashboard {
	at := cloneDashboardTime(&now)
	return Dashboard{
		ObservedAt: now,
		Students: StudentSummary{
			State: DataStateHealthy, ObservedAt: at, Active: 2, Disabled: 1,
		},
		Questions: QuestionSummary{
			State: DataStateHealthy, ObservedAt: at,
			Waiting: 1, OldestWaitSeconds: 30,
		},
		AI: AISummary{
			State: DataStateHealthy, ObservedAt: at,
			Requests: 4, SuccessRatePercent: 75,
			FirstByteLatencyMilliseconds: 25,
			TotalLatencyMilliseconds:     200,
			DailyCostMicroUSD:            100,
		},
		Storage: StorageSummary{
			State: DataStateHealthy, ObservedAt: at,
			UsedBytes: 10, CapacityBytes: 100, WarningPercent: 75,
		},
		Services: []ServiceHealth{{
			Service: ServiceApp, State: DataStateDegraded, ObservedAt: at,
		}},
		Queues: []QueueSummary{{
			Queue: QueueProcessing, State: DataStateHealthy, ObservedAt: at,
		}},
		Backup: BackupSummary{
			State: DataStateHealthy, ObservedAt: at,
			Local:   BackupPointSummary{State: RecoveryStateSucceeded, CompletedAt: at},
			Remote:  BackupPointSummary{State: RecoveryStateEmpty},
			Restore: RestorePointSummary{State: RecoveryStateEmpty},
		},
		Alerts: AlertSummary{
			State: DataStateHealthy, ObservedAt: at, OpenWarning: 1,
		},
		RecentAuditState: DataStateHealthy,
		RecentAudit: []AuditSummary{{
			Category: AuditCategoryOperations, Outcome: AuditOutcomeSucceeded,
			OccurredAt: now,
		}},
	}
}
