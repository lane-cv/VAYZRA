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
	"happylearn.local/app/internal/auth"
)

type alertHTTPStub struct {
	*operationsHTTPStub
	page        AlertPage
	listErr     error
	acknowledge Alert
	ackErr      error
	filter      AlertFilter
	ackID       uuid.UUID
	listCalls   int
	ackCalls    int
}

func (stub *alertHTTPStub) ListAlerts(
	_ context.Context,
	_ Principal,
	filter AlertFilter,
) (AlertPage, error) {
	stub.listCalls++
	stub.filter = filter
	return stub.page, stub.listErr
}

func (stub *alertHTTPStub) AcknowledgeAlert(
	_ context.Context,
	_ Principal,
	id uuid.UUID,
) (Alert, error) {
	stub.ackCalls++
	stub.ackID = id
	return stub.acknowledge, stub.ackErr
}

func TestOperationsHTTPListsAlertsWithExactFiltersAndStableCursor(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	firstID := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	secondID := uuid.MustParse("10000000-0000-4000-8000-000000000002")
	before := AlertCursor{
		LastObservedAt: now.Add(time.Minute),
		ID:             uuid.MustParse("10000000-0000-4000-8000-000000000003"),
	}
	next := AlertCursor{LastObservedAt: now, ID: secondID}
	stub := &alertHTTPStub{
		operationsHTTPStub: &operationsHTTPStub{},
		page: AlertPage{
			Items: []Alert{
				alertHTTPFixture(firstID, now.Add(time.Minute)),
				alertHTTPFixture(secondID, now),
			},
			Next: &next,
		},
	}
	handler := NewAdminHandler(stub, nil).Routes()
	request := operationsHTTPRequest(
		http.MethodGet,
		"/alerts?state=open&severity=critical&category=backup&limit=2&before="+
			encodeAlertCursor(before),
		"",
		auth.RoleAdmin,
	)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK ||
		result.Header().Get("Cache-Control") != "no-store, private" ||
		stub.listCalls != 1 {
		t.Fatalf(
			"status=%d calls=%d headers=%v body=%s",
			result.Code,
			stub.listCalls,
			result.Header(),
			result.Body.String(),
		)
	}
	if stub.filter.State != AlertStateOpen ||
		stub.filter.Severity != AlertSeverityCritical ||
		stub.filter.Category != "backup" ||
		stub.filter.Limit != 2 ||
		stub.filter.Before == nil ||
		!stub.filter.Before.LastObservedAt.Equal(before.LastObservedAt) ||
		stub.filter.Before.ID != before.ID {
		t.Fatalf("filter=%+v", stub.filter)
	}
	body := result.Body.String()
	for _, expected := range []string{
		`"id":"` + firstID.String() + `"`,
		`"dedupeKey":"backup_local_age"`,
		`"category":"backup"`,
		`"severity":"critical"`,
		`"state":"open"`,
		`"firstObservedAt":"2026-07-30T08:00:00Z"`,
		`"lastObservedAt":"2026-07-30T09:01:00Z"`,
		`"currentValue":108001`,
		`"thresholdValue":108000`,
		`"summary":"Verified local backup is overdue"`,
		`"next":"` + encodeAlertCursor(next) + `"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s in %s", expected, body)
		}
	}
	for _, forbidden := range []string{
		"dedupe_key", "acknowledged_by", "consecutiveFailures",
		"consecutiveSuccesses", "version",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("internal field %q leaked in %s", forbidden, body)
		}
	}
}

func TestOperationsHTTPRejectsInvalidAlertFiltersBeforeService(t *testing.T) {
	validCursor := encodeAlertCursor(AlertCursor{
		LastObservedAt: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC),
		ID:             uuid.MustParse("10000000-0000-4000-8000-000000000001"),
	})
	tests := map[string]string{
		"unknown query":      "/alerts?studentId=private",
		"duplicate query":    "/alerts?state=open&state=resolved",
		"empty query value":  "/alerts?state=",
		"unknown state":      "/alerts?state=OPEN",
		"unknown severity":   "/alerts?severity=emergency",
		"unknown category":   "/alerts?category=private",
		"zero limit":         "/alerts?limit=0",
		"leading zero limit": "/alerts?limit=01",
		"oversized limit":    "/alerts?limit=101",
		"malformed cursor":   "/alerts?before=private",
		"padded cursor":      "/alerts?before=" + validCursor + "=",
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			stub := &alertHTTPStub{operationsHTTPStub: &operationsHTTPStub{}}
			result := httptest.NewRecorder()
			NewAdminHandler(stub, nil).Routes().ServeHTTP(
				result,
				operationsHTTPRequest(http.MethodGet, target, "", auth.RoleAdmin),
			)
			if result.Code != http.StatusBadRequest || stub.listCalls != 0 {
				t.Fatalf(
					"status=%d calls=%d body=%s",
					result.Code,
					stub.listCalls,
					result.Body.String(),
				)
			}
			assertOperationsErrorEnvelope(t, result, "invalid_request")
		})
	}
}

func TestOperationsHTTPAcknowledgesCanonicalAlertAndMapsStableErrors(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	id := uuid.MustParse("1abc0000-0000-4000-8000-000000000001")
	acknowledged := alertHTTPFixture(id, now)
	acknowledged.State = AlertStateAcknowledged
	acknowledgedBy := uuid.MustParse("20000000-0000-4000-8000-000000000001")
	acknowledged.AcknowledgedBy = acknowledgedBy
	acknowledged.AcknowledgedAt = &now
	stub := &alertHTTPStub{
		operationsHTTPStub: &operationsHTTPStub{},
		acknowledge:        acknowledged,
	}
	handler := NewAdminHandler(stub, nil).Routes()
	result := httptest.NewRecorder()
	handler.ServeHTTP(
		result,
		operationsHTTPRequest(
			http.MethodPost,
			"/alerts/"+id.String()+"/acknowledge",
			"",
			auth.RoleAdmin,
		),
	)
	if result.Code != http.StatusOK || stub.ackCalls != 1 || stub.ackID != id ||
		!strings.Contains(result.Body.String(), `"state":"acknowledged"`) ||
		!strings.Contains(result.Body.String(), `"acknowledgedBy":"`+acknowledgedBy.String()+`"`) {
		t.Fatalf(
			"status=%d calls=%d id=%s body=%s",
			result.Code,
			stub.ackCalls,
			stub.ackID,
			result.Body.String(),
		)
	}

	for name, test := range map[string]struct {
		target string
		body   string
		err    error
		status int
		code   string
		calls  int
	}{
		"noncanonical UUID": {
			target: "/alerts/" + strings.ToUpper(id.String()) + "/acknowledge",
			status: http.StatusBadRequest, code: "invalid_request",
		},
		"malformed UUID": {
			target: "/alerts/private/acknowledge",
			status: http.StatusBadRequest, code: "invalid_request",
		},
		"unexpected query": {
			target: "/alerts/" + id.String() + "/acknowledge?force=true",
			status: http.StatusBadRequest, code: "invalid_request",
		},
		"unexpected body": {
			target: "/alerts/" + id.String() + "/acknowledge",
			body:   `{}`,
			status: http.StatusBadRequest, code: "invalid_request",
		},
		"not found": {
			target: "/alerts/" + id.String() + "/acknowledge",
			err:    ErrAlertNotFound,
			status: http.StatusNotFound, code: "alert_not_found", calls: 1,
		},
		"already resolved": {
			target: "/alerts/" + id.String() + "/acknowledge",
			err:    ErrAlertAlreadyResolved,
			status: http.StatusConflict, code: "alert_already_resolved", calls: 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			local := &alertHTTPStub{
				operationsHTTPStub: &operationsHTTPStub{},
				ackErr:             test.err,
			}
			localResult := httptest.NewRecorder()
			NewAdminHandler(local, nil).Routes().ServeHTTP(
				localResult,
				operationsHTTPRequest(
					http.MethodPost,
					test.target,
					test.body,
					auth.RoleAdmin,
				),
			)
			if localResult.Code != test.status || local.ackCalls != test.calls {
				t.Fatalf(
					"status=%d calls=%d body=%s",
					localResult.Code,
					local.ackCalls,
					localResult.Body.String(),
				)
			}
			assertOperationsErrorEnvelope(t, localResult, test.code)
		})
	}
}

func TestOperationsHTTPAlertRoutesAreAdminOnly(t *testing.T) {
	id := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	stub := &alertHTTPStub{operationsHTTPStub: &operationsHTTPStub{}}
	handler := NewAdminHandler(stub, nil).Routes()
	for method, target := range map[string]string{
		http.MethodGet:  "/alerts",
		http.MethodPost: "/alerts/" + id.String() + "/acknowledge",
	} {
		result := httptest.NewRecorder()
		handler.ServeHTTP(
			result,
			operationsHTTPRequest(method, target, "", auth.RoleStudent),
		)
		if result.Code != http.StatusForbidden ||
			stub.listCalls != 0 || stub.ackCalls != 0 {
			t.Fatalf(
				"%s %s status=%d list=%d ack=%d body=%s",
				method,
				target,
				result.Code,
				stub.listCalls,
				stub.ackCalls,
				result.Body.String(),
			)
		}
	}
}

func TestOperationsAlertServiceRequiresActiveAdmin(t *testing.T) {
	alerts := &alertServiceStoreStub{
		page:  AlertPage{Items: []Alert{}},
		alert: alertHTTPFixture(uuid.New(), time.Now().UTC()),
	}
	service, err := NewServiceWithDashboardAndAlerts(
		&fakeStore{settings: validSettings()},
		&operationsAuditReader{},
		&dashboardServiceReaderStub{},
		alerts,
	)
	if err != nil {
		t.Fatal(err)
	}
	admin := operationsAdmin(uuid.New())
	if _, err := service.(AlertHTTPService).ListAlerts(
		context.Background(),
		admin,
		AlertFilter{Limit: 50},
	); err != nil {
		t.Fatal(err)
	}
	student := admin
	student.User.Role = auth.RoleStudent
	if _, err := service.(AlertHTTPService).ListAlerts(
		context.Background(),
		student,
		AlertFilter{Limit: 50},
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("student list error=%v", err)
	}
	if _, err := service.(AlertHTTPService).AcknowledgeAlert(
		context.Background(),
		student,
		alerts.alert.ID,
	); !errors.Is(err, ErrForbidden) {
		t.Fatalf("student acknowledge error=%v", err)
	}
	if alerts.listCalls != 1 || alerts.ackCalls != 0 {
		t.Fatalf("list=%d ack=%d", alerts.listCalls, alerts.ackCalls)
	}
}

type alertServiceStoreStub struct {
	page      AlertPage
	alert     Alert
	listCalls int
	ackCalls  int
}

func (stub *alertServiceStoreStub) ListAlerts(
	context.Context,
	AlertFilter,
) (AlertPage, error) {
	stub.listCalls++
	return stub.page, nil
}

func (stub *alertServiceStoreStub) AcknowledgeAlert(
	context.Context,
	Principal,
	uuid.UUID,
) (Alert, error) {
	stub.ackCalls++
	return stub.alert, nil
}

func alertHTTPFixture(id uuid.UUID, lastObservedAt time.Time) Alert {
	return Alert{
		ID: id, DedupeKey: "backup_local_age", Category: "backup",
		Severity: AlertSeverityCritical, State: AlertStateOpen,
		FirstObservedAt:     time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC),
		LastObservedAt:      lastObservedAt,
		CurrentValue:        108001,
		ThresholdValue:      108000,
		Summary:             "Verified local backup is overdue",
		ConsecutiveFailures: 2,
		Version:             2,
	}
}
