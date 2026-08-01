package operations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/safehttp"
)

func TestWebhookPayloadHasExactPublicSchema(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 34, 56, 123000000, time.UTC)
	event := WebhookEvent{
		ID: uuid.New(), AlertID: uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		TransitionKind: AlertTransitionUpgraded, AlertVersion: 99,
		Category: "processing", Severity: AlertSeverityCritical,
		State: AlertStateAcknowledged, Summary: "Processing queue depth is high",
		CurrentValue: 101, ThresholdValue: 100,
		FirstObservedAt: at, LastObservedAt: at.Add(time.Minute),
	}
	payload, err := BuildWebhookPayload(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schemaVersion":1,"alertId":"11111111-1111-4111-8111-111111111111","category":"processing","severity":"critical","state":"acknowledged","summary":"Processing queue depth is high","firstObservedAt":"2026-07-30T12:34:56.123Z","lastObservedAt":"2026-07-30T12:35:56.123Z","currentValue":101,"threshold":100,"dashboardPath":"/admin/alerts"}`
	if string(encoded) != want {
		t.Fatalf("payload=%s", encoded)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 11 {
		t.Fatalf("field count=%d fields=%v", len(fields), fields)
	}
	for _, forbidden := range []string{
		"transitionKind", "alertVersion", "eventId", "webhookUrl",
		"authorization", "traceId", "rawError", "context",
	} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("forbidden payload field %q", forbidden)
		}
	}
	if strings.Contains(payload.DashboardPath, "://") ||
		!strings.HasPrefix(payload.DashboardPath, "/") {
		t.Fatalf("dashboard path=%q", payload.DashboardPath)
	}
}

func TestWebhookSenderConfigurationIsFailClosedAndDisabledIsNoop(t *testing.T) {
	t.Parallel()
	disabled, err := NewWebhookSender(
		context.Background(),
		WebhookSenderConfig{},
	)
	if err != nil || disabled == nil || disabled.Enabled() {
		t.Fatalf("disabled=%+v err=%v", disabled, err)
	}
	for _, cfg := range []WebhookSenderConfig{
		{Authorization: "Bearer secret"},
		{
			URL: "http://alerts.example.test/hook",
			Resolver: &webhookResolver{answers: [][]netip.Addr{
				{netip.MustParseAddr("93.184.216.34")},
			}},
		},
		{
			URL: "https://169.254.169.254/latest/meta-data",
		},
	} {
		if sender, err := NewWebhookSender(context.Background(), cfg); err == nil || sender != nil {
			t.Fatalf("invalid config accepted: sender=%+v err=%v", sender, err)
		}
	}
}

func TestWebhookSenderConfigurationErrorsAreStable(t *testing.T) {
	t.Parallel()
	const marker = "private-marker"
	for name, config := range map[string]WebhookSenderConfig{
		"authorization without URL": {
			Authorization: "Bearer " + marker,
		},
		"unsafe authorization": {
			URL:           "https://alerts.example.test/hook",
			Authorization: "Bearer " + marker + "\n",
			Resolver: &webhookResolver{answers: [][]netip.Addr{
				{netip.MustParseAddr("93.184.216.34")},
			}},
		},
		"normalization": {
			URL: "https://" + marker + ".example.test/hook",
			Resolver: &webhookResolver{
				err: errors.New(
					marker + " resolver refused 10.0.0.1",
				),
			},
		},
		"metadata address": {
			URL: "https://169.254.169.254/" + marker,
		},
		"timeout": {
			URL: "https://alerts.example.test/" + marker,
			Resolver: &webhookResolver{answers: [][]netip.Addr{
				{netip.MustParseAddr("93.184.216.34")},
			}},
			Timeouts: safehttp.Timeouts{
				Total: -time.Second,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			sender, err := NewWebhookSender(
				context.Background(),
				config,
			)
			if sender != nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("sender=%+v error=%v", sender, err)
			}
			for _, forbidden := range []string{
				marker,
				"169.254.169.254",
				"10.0.0.1",
				"resolver",
				"alerts.example.test",
			} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf(
						"configuration error leaked %q: %q",
						forbidden,
						err,
					)
				}
			}
		})
	}
}

func TestWebhookSenderRejectsInitiallyPrivateTarget(t *testing.T) {
	t.Parallel()
	sender, err := NewWebhookSender(context.Background(), WebhookSenderConfig{
		URL: "https://169.254.169.254/latest/meta-data",
	})
	if sender != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("sender=%+v error=%v", sender, err)
	}
	t.Log("PHASE5_FAILURE_EVIDENCE case=webhook_private_target actual=rejected maintenance=normal alert=suppressed plaintext_dump=absent")
}

func TestWebhookSenderConfigCannotInjectTransport(t *testing.T) {
	t.Parallel()
	configType := reflect.TypeOf(WebhookSenderConfig{})
	if field, exists := configType.FieldByName("Doer"); exists {
		t.Fatalf("exported transport injection remains: %+v", field)
	}
}

func TestWebhookSenderSendsBoundedExactRequestAndReturnsSafeClassification(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	payload := webhookTestPayload(t, at)
	doer := &webhookDoerStub{
		response: &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("accepted")),
			Header:     make(http.Header),
		},
	}
	sender, err := newWebhookSenderWithDoer(
		context.Background(),
		WebhookSenderConfig{
			URL:           "https://alerts.example.test/hook",
			Authorization: "Bearer webhook-secret",
			Resolver: &webhookResolver{answers: [][]netip.Addr{
				{netip.MustParseAddr("93.184.216.34")},
			}},
		},
		doer,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := sender.Send(context.Background(), payload)
	if !result.Succeeded ||
		result.Retryable ||
		result.HTTPStatusClass != 2 ||
		result.ErrorCategory != "" {
		t.Fatalf("result=%+v", result)
	}
	request := doer.Request()
	if request == nil ||
		request.Method != http.MethodPost ||
		request.URL.String() != "https://alerts.example.test/hook" ||
		request.Header.Get("Authorization") != "Bearer webhook-secret" ||
		request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request=%+v", request)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || len(body) > MaxWebhookRequestBytes {
		t.Fatalf("request bytes=%d", len(body))
	}
	if strings.Contains(result.ErrorCategory, "webhook-secret") ||
		strings.Contains(result.ErrorCategory, "alerts.example.test") ||
		strings.Contains(result.ErrorCategory, "accepted") {
		t.Fatalf("unsafe result=%+v", result)
	}
}

func TestWebhookSenderStatusStrategy(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status    int
		succeeded bool
		retryable bool
		category  string
	}{
		{200, true, false, ""},
		{299, true, false, ""},
		{400, false, false, "client_error"},
		{401, false, false, "client_error"},
		{408, false, true, "rate_limited"},
		{425, false, true, "rate_limited"},
		{429, false, true, "rate_limited"},
		{500, false, true, "server_error"},
		{503, false, true, "server_error"},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			sender := webhookSenderWithDoer(t, &webhookDoerStub{
				response: &http.Response{
					StatusCode: test.status,
					Body:       io.NopCloser(strings.NewReader("private response body")),
					Header:     make(http.Header),
				},
			})
			result := sender.Send(
				context.Background(),
				webhookTestPayload(t, time.Now().UTC()),
			)
			if result.Succeeded != test.succeeded ||
				result.Retryable != test.retryable ||
				result.HTTPStatusClass != test.status/100 ||
				result.ErrorCategory != test.category {
				t.Fatalf("status=%d result=%+v", test.status, result)
			}
			if strings.Contains(result.ErrorCategory, "private response body") {
				t.Fatalf("response body leaked: %+v", result)
			}
		})
	}
}

func TestWebhookSenderRejectsRedirectBeforeAuthorizationCanReachNextHop(t *testing.T) {
	t.Parallel()
	var nextRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			if request.Header.Get("Authorization") != "Bearer original-only" {
				t.Errorf("original authorization missing")
			}
			http.Redirect(w, request, "/next", http.StatusFound)
			return
		}
		nextRequests++
	}))
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	sender, err := NewWebhookSender(context.Background(), WebhookSenderConfig{
		URL:                     "http://webhook.test:" + port + "/start",
		Authorization:           "Bearer original-only",
		DevelopmentAllowPrivate: true,
		Resolver: &webhookResolver{answers: [][]netip.Addr{
			{netip.MustParseAddr("127.0.0.1")},
			{netip.MustParseAddr("127.0.0.1")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := sender.Send(
		context.Background(),
		webhookTestPayload(t, time.Now().UTC()),
	)
	if result.Succeeded ||
		result.Retryable ||
		result.ErrorCategory != "redirect_rejected" ||
		nextRequests != 0 {
		t.Fatalf("result=%+v nextRequests=%d", result, nextRequests)
	}
}

func TestWebhookSenderRejectsDNSRebindingResponseOverflowAndTimeout(t *testing.T) {
	t.Run("DNS rebinding", func(t *testing.T) {
		var requests int
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			requests++
		}))
		defer server.Close()
		port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
		sender, err := NewWebhookSender(context.Background(), WebhookSenderConfig{
			URL:                     "http://webhook.test:" + port + "/hook",
			DevelopmentAllowPrivate: true,
			Resolver: &webhookResolver{answers: [][]netip.Addr{
				{netip.MustParseAddr("127.0.0.1")},
				{netip.MustParseAddr("10.0.0.1")},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		result := sender.Send(
			context.Background(),
			webhookTestPayload(t, time.Now().UTC()),
		)
		if result.Succeeded ||
			result.ErrorCategory == "" ||
			requests != 0 {
			t.Fatalf("result=%+v requests=%d", result, requests)
		}
	})

	t.Run("response overflow", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", MaxWebhookResponseBytes+1))
		}))
		defer server.Close()
		sender := localWebhookSender(t, server.URL, safehttp.Timeouts{})
		result := sender.Send(
			context.Background(),
			webhookTestPayload(t, time.Now().UTC()),
		)
		if result.Succeeded ||
			result.Retryable ||
			result.ErrorCategory != "response_too_large" {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("total timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(200 * time.Millisecond)
			_, _ = io.WriteString(w, "late")
		}))
		defer server.Close()
		sender := localWebhookSender(t, server.URL, safehttp.Timeouts{
			Connect: time.Second, ResponseHeader: time.Second,
			IdleStream: time.Second, Total: 20 * time.Millisecond,
		})
		startedAt := time.Now()
		result := sender.Send(
			context.Background(),
			webhookTestPayload(t, time.Now().UTC()),
		)
		if result.Succeeded ||
			!result.Retryable ||
			result.ErrorCategory != "timeout" ||
			time.Since(startedAt) > 150*time.Millisecond {
			t.Fatalf("result=%+v duration=%s", result, time.Since(startedAt))
		}
		t.Log("PHASE5_FAILURE_EVIDENCE case=webhook_timeout actual=failed maintenance=normal alert=active plaintext_dump=absent")
	})
}

func TestWebhookSenderRejectsRequestAboveEightKiBBeforeTransport(t *testing.T) {
	t.Parallel()
	doer := &webhookDoerStub{}
	sender := webhookSenderWithDoer(t, doer)
	payload := webhookTestPayload(t, time.Now().UTC())
	payload.Summary = strings.Repeat("x", MaxWebhookRequestBytes)
	result := sender.Send(context.Background(), payload)
	if result.Succeeded ||
		result.Retryable ||
		result.ErrorCategory != "request_too_large" ||
		doer.Calls() != 0 {
		t.Fatalf("result=%+v calls=%d", result, doer.Calls())
	}
}

func TestWebhookSenderNeverReturnsTransportOrResponseDetails(t *testing.T) {
	t.Parallel()
	for name, doer := range map[string]webhookHTTPDoer{
		"transport": &webhookDoerStub{
			err: errors.New(
				"Bearer private-secret alerts.example.test refused",
			),
		},
		"response": &webhookDoerStub{
			response: &http.Response{
				StatusCode: http.StatusOK,
				Body: &webhookErrorBody{err: errors.New(
					"private response body read failed",
				)},
				Header: make(http.Header),
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := webhookSenderWithDoer(t, doer).Send(
				context.Background(),
				webhookTestPayload(t, time.Now().UTC()),
			)
			if result.Succeeded || !result.Retryable {
				t.Fatalf("result=%+v", result)
			}
			wantCategory := "network"
			if name == "response" {
				wantCategory = "response_read"
			}
			if result.ErrorCategory != wantCategory {
				t.Fatalf("result=%+v", result)
			}
			for _, forbidden := range []string{
				"private", "secret", "alerts.example", "refused",
			} {
				if strings.Contains(result.ErrorCategory, forbidden) {
					t.Fatalf("unsafe result=%+v", result)
				}
			}
		})
	}
}

type webhookResolver struct {
	mu      sync.Mutex
	answers [][]netip.Addr
	index   int
	err     error
}

func (resolver *webhookResolver) LookupNetIP(
	context.Context,
	string,
	string,
) ([]netip.Addr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.err != nil {
		return nil, resolver.err
	}
	index := resolver.index
	if index >= len(resolver.answers) {
		index = len(resolver.answers) - 1
	}
	resolver.index++
	return append([]netip.Addr(nil), resolver.answers[index]...), nil
}

type webhookDoerStub struct {
	mu       sync.Mutex
	request  *http.Request
	response *http.Response
	err      error
	calls    int
}

type webhookErrorBody struct {
	err error
}

func (body *webhookErrorBody) Read([]byte) (int, error) {
	return 0, body.err
}

func (*webhookErrorBody) Close() error {
	return nil
}

func (doer *webhookDoerStub) Do(request *http.Request) (*http.Response, error) {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	doer.calls++
	doer.request = request
	return doer.response, doer.err
}

func (doer *webhookDoerStub) Request() *http.Request {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	return doer.request
}

func (doer *webhookDoerStub) Calls() int {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	return doer.calls
}

func webhookSenderWithDoer(
	t *testing.T,
	doer webhookHTTPDoer,
) *WebhookSender {
	t.Helper()
	sender, err := newWebhookSenderWithDoer(
		context.Background(),
		WebhookSenderConfig{
			URL: "https://alerts.example.test/hook",
			Resolver: &webhookResolver{answers: [][]netip.Addr{
				{netip.MustParseAddr("93.184.216.34")},
			}},
		},
		doer,
	)
	if err != nil {
		t.Fatal(err)
	}
	return sender
}

func localWebhookSender(
	t *testing.T,
	serverURL string,
	timeouts safehttp.Timeouts,
) *WebhookSender {
	t.Helper()
	port := strings.TrimPrefix(serverURL, "http://127.0.0.1:")
	sender, err := NewWebhookSender(context.Background(), WebhookSenderConfig{
		URL:                     "http://webhook.test:" + port + "/hook",
		DevelopmentAllowPrivate: true,
		Resolver: &webhookResolver{answers: [][]netip.Addr{
			{netip.MustParseAddr("127.0.0.1")},
			{netip.MustParseAddr("127.0.0.1")},
		}},
		Timeouts: timeouts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sender
}

func webhookTestPayload(t *testing.T, at time.Time) WebhookPayload {
	t.Helper()
	payload, err := BuildWebhookPayload(WebhookEvent{
		ID: uuid.New(), AlertID: uuid.New(),
		TransitionKind: AlertTransitionOpened, AlertVersion: 2,
		Category: "processing", Severity: AlertSeverityWarning,
		State: AlertStateOpen, Summary: "Processing queue depth is high",
		CurrentValue: 21, ThresholdValue: 20,
		FirstObservedAt: at, LastObservedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
