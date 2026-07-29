package operations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

func TestOperationsHTTPWebhookTestUsesStableSafeResponses(t *testing.T) {
	for name, test := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"delivered": {
			status: http.StatusOK,
		},
		"not configured": {
			err: ErrWebhookNotConfigured, status: http.StatusConflict,
			code: "webhook_not_configured",
		},
		"delivery failed": {
			err: ErrWebhookDeliveryFailed, status: http.StatusBadGateway,
			code: "webhook_delivery_failed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &webhookHTTPStub{
				operationsHTTPStub: &operationsHTTPStub{},
				err:                test.err,
			}
			result := httptest.NewRecorder()
			NewAdminHandler(stub, nil).Routes().ServeHTTP(
				result,
				operationsHTTPRequest(
					http.MethodPost,
					"/webhook-test",
					"",
					auth.RoleAdmin,
				),
			)
			if result.Code != test.status || stub.calls != 1 {
				t.Fatalf(
					"status=%d calls=%d body=%s",
					result.Code,
					stub.calls,
					result.Body.String(),
				)
			}
			body := result.Body.String()
			if test.code != "" {
				assertOperationsErrorEnvelope(t, result, test.code)
			} else if body != "{\"data\":{\"status\":\"delivered\"}}\n" {
				t.Fatalf("body=%s", body)
			}
			for _, forbidden := range []string{
				"secret", "alerts.example", "traceId", "rawError",
				"timeout", "network", "response_too_large",
			} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("response leaked %q: %s", forbidden, body)
				}
			}
		})
	}
}

func TestOperationsHTTPWebhookTestRejectsInputAndNonAdminBeforeService(
	t *testing.T,
) {
	for name, request := range map[string]*http.Request{
		"query": operationsHTTPRequest(
			http.MethodPost,
			"/webhook-test?target=https://private.test",
			"",
			auth.RoleAdmin,
		),
		"body": operationsHTTPRequest(
			http.MethodPost,
			"/webhook-test",
			`{"url":"https://private.test"}`,
			auth.RoleAdmin,
		),
		"student": operationsHTTPRequest(
			http.MethodPost,
			"/webhook-test",
			"",
			auth.RoleStudent,
		),
	} {
		t.Run(name, func(t *testing.T) {
			stub := &webhookHTTPStub{
				operationsHTTPStub: &operationsHTTPStub{},
			}
			result := httptest.NewRecorder()
			NewAdminHandler(stub, nil).Routes().ServeHTTP(result, request)
			wantStatus := http.StatusBadRequest
			if name == "student" {
				wantStatus = http.StatusForbidden
			}
			if result.Code != wantStatus || stub.calls != 0 {
				t.Fatalf(
					"status=%d calls=%d body=%s",
					result.Code,
					stub.calls,
					result.Body.String(),
				)
			}
		})
	}
}

func TestOperationsWebhookTestServiceUsesFixedSyntheticPayload(t *testing.T) {
	sender := &webhookTestSenderStub{
		enabled: true,
		result: WebhookDeliveryResult{
			Succeeded: true, HTTPStatusClass: 2,
		},
	}
	service := webhookTestService(t, sender)
	if err := service.TestWebhook(
		context.Background(),
		operationsAdminWebhookPrincipal(),
	); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 {
		t.Fatalf("calls=%d", sender.calls)
	}
	encoded, err := json.Marshal(sender.payload)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schemaVersion":1,"alertId":"00000000-0000-4000-8000-000000000001","category":"processing","severity":"warning","state":"open","summary":"Webhook connectivity test","firstObservedAt":"2000-01-01T00:00:00Z","lastObservedAt":"2000-01-01T00:00:00Z","currentValue":0,"threshold":0,"dashboardPath":"/admin/alerts"}`
	if string(encoded) != want {
		t.Fatalf("payload=%s", encoded)
	}
	for _, forbidden := range []string{
		"student", "metric", "url", "authorization", "trace", "error",
	} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("payload leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestOperationsWebhookTestServiceFailsClosed(t *testing.T) {
	for name, test := range map[string]struct {
		sender    *webhookTestSenderStub
		principal Principal
		want      error
		calls     int
	}{
		"disabled": {
			sender:    &webhookTestSenderStub{},
			principal: operationsAdminWebhookPrincipal(),
			want:      ErrWebhookNotConfigured,
		},
		"delivery": {
			sender: &webhookTestSenderStub{
				enabled: true,
				result: WebhookDeliveryResult{
					Retryable: true, ErrorCategory: "network",
				},
			},
			principal: operationsAdminWebhookPrincipal(),
			want:      ErrWebhookDeliveryFailed,
			calls:     1,
		},
		"student": {
			sender: &webhookTestSenderStub{enabled: true},
			principal: func() Principal {
				value := operationsAdminWebhookPrincipal()
				value.User.Role = auth.RoleStudent
				return value
			}(),
			want: ErrForbidden,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := webhookTestService(t, test.sender)
			err := service.TestWebhook(
				context.Background(),
				test.principal,
			)
			if !errors.Is(err, test.want) ||
				test.sender.calls != test.calls {
				t.Fatalf(
					"error=%v calls=%d",
					err,
					test.sender.calls,
				)
			}
		})
	}
}

type webhookHTTPStub struct {
	*operationsHTTPStub
	err   error
	calls int
}

func (stub *webhookHTTPStub) TestWebhook(
	context.Context,
	Principal,
) error {
	stub.calls++
	return stub.err
}

type webhookTestSenderStub struct {
	enabled bool
	result  WebhookDeliveryResult
	payload WebhookPayload
	calls   int
}

func (stub *webhookTestSenderStub) Enabled() bool {
	return stub.enabled
}

func (stub *webhookTestSenderStub) Send(
	_ context.Context,
	payload WebhookPayload,
) WebhookDeliveryResult {
	stub.calls++
	stub.payload = payload
	return stub.result
}

func webhookTestService(
	t *testing.T,
	sender WebhookTestSender,
) WebhookTestHTTPService {
	t.Helper()
	service, err := NewServiceWithDashboardAlertsAndWebhook(
		&fakeStore{settings: validSettings()},
		&operationsAuditReader{},
		&dashboardServiceReaderStub{},
		&alertServiceStoreStub{},
		sender,
	)
	if err != nil {
		t.Fatal(err)
	}
	testService, ok := service.(WebhookTestHTTPService)
	if !ok {
		t.Fatalf("service lacks webhook test API: %T", service)
	}
	return testService
}

func operationsAdminWebhookPrincipal() Principal {
	return operationsAdmin(
		uuid.MustParse("70000000-0000-4000-8000-000000000010"),
	)
}
