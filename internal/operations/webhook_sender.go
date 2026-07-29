package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/safehttp"
)

const (
	WebhookSchemaVersion     = 1
	WebhookDashboardPath     = "/admin/alerts"
	MaxWebhookRequestBytes   = 8 << 10
	MaxWebhookResponseBytes  = 16 << 10
	maxWebhookAuthorization  = 4 << 10
	maxWebhookNetworkTimeout = 30 * time.Second
)

// WebhookPayload is the complete, public webhook contract. Internal delivery
// identifiers and transition bookkeeping deliberately do not appear here.
type WebhookPayload struct {
	SchemaVersion   int           `json:"schemaVersion"`
	AlertID         string        `json:"alertId"`
	Category        string        `json:"category"`
	Severity        AlertSeverity `json:"severity"`
	State           AlertState    `json:"state"`
	Summary         string        `json:"summary"`
	FirstObservedAt time.Time     `json:"firstObservedAt"`
	LastObservedAt  time.Time     `json:"lastObservedAt"`
	CurrentValue    float64       `json:"currentValue"`
	Threshold       float64       `json:"threshold"`
	DashboardPath   string        `json:"dashboardPath"`
}

func BuildWebhookPayload(event WebhookEvent) (WebhookPayload, error) {
	alert := Alert{
		ID:              event.AlertID,
		Category:        event.Category,
		Severity:        event.Severity,
		State:           event.State,
		Summary:         event.Summary,
		CurrentValue:    event.CurrentValue,
		ThresholdValue:  event.ThresholdValue,
		FirstObservedAt: event.FirstObservedAt,
		LastObservedAt:  event.LastObservedAt,
		Version:         event.AlertVersion,
	}
	if event.ID == uuid.Nil ||
		!validWebhookTransition(event.TransitionKind, alert) {
		return WebhookPayload{}, ErrInvalid
	}
	return WebhookPayload{
		SchemaVersion:   WebhookSchemaVersion,
		AlertID:         event.AlertID.String(),
		Category:        event.Category,
		Severity:        event.Severity,
		State:           event.State,
		Summary:         event.Summary,
		FirstObservedAt: event.FirstObservedAt.UTC(),
		LastObservedAt:  event.LastObservedAt.UTC(),
		CurrentValue:    event.CurrentValue,
		Threshold:       event.ThresholdValue,
		DashboardPath:   WebhookDashboardPath,
	}, nil
}

type webhookHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type WebhookSenderConfig struct {
	URL                     string
	Authorization           string
	DevelopmentAllowPrivate bool
	Resolver                safehttp.Resolver
	Timeouts                safehttp.Timeouts
}

type WebhookSender struct {
	enabled       bool
	endpoint      *url.URL
	authorization string
	doer          webhookHTTPDoer
}

func NewWebhookSender(
	ctx context.Context,
	config WebhookSenderConfig,
) (*WebhookSender, error) {
	return newWebhookSenderWithDoer(ctx, config, nil)
}

func newWebhookSenderWithDoer(
	ctx context.Context,
	config WebhookSenderConfig,
	doer webhookHTTPDoer,
) (*WebhookSender, error) {
	if ctx == nil {
		return nil, ErrInvalid
	}
	if config.URL == "" {
		if config.Authorization != "" {
			return nil, ErrInvalid
		}
		return &WebhookSender{}, nil
	}
	if strings.TrimSpace(config.URL) != config.URL ||
		!safeWebhookAuthorization(config.Authorization) {
		return nil, ErrInvalid
	}
	timeouts, err := webhookTimeouts(config.Timeouts)
	if err != nil {
		return nil, ErrInvalid
	}
	policy := safehttp.Policy{
		DevelopmentAllowPrivate: config.DevelopmentAllowPrivate,
		Resolver:                config.Resolver,
	}
	endpoint, err := policy.NormalizeBaseURL(ctx, config.URL)
	if err != nil {
		return nil, ErrInvalid
	}
	if doer == nil {
		doer = safehttp.NewClient(policy, safehttp.ClientOptions{
			Timeouts:         timeouts,
			Redirects:        safehttp.RejectRedirects,
			MaxResponseBytes: MaxWebhookResponseBytes,
		})
	}
	return &WebhookSender{
		enabled:       true,
		endpoint:      endpoint,
		authorization: config.Authorization,
		doer:          doer,
	}, nil
}

func (sender *WebhookSender) Enabled() bool {
	return sender != nil && sender.enabled
}

func (sender *WebhookSender) Send(
	ctx context.Context,
	payload WebhookPayload,
) WebhookDeliveryResult {
	if sender == nil || !sender.enabled || sender.endpoint == nil ||
		sender.doer == nil || ctx == nil {
		return webhookProtocolFailure()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return webhookProtocolFailure()
	}
	if len(body) > MaxWebhookRequestBytes {
		return WebhookDeliveryResult{ErrorCategory: "request_too_large"}
	}
	if !validWebhookPayload(payload) {
		return webhookProtocolFailure()
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		sender.endpoint.String(),
		bytes.NewReader(body),
	)
	if err != nil {
		return webhookProtocolFailure()
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if sender.authorization != "" {
		request.Header.Set("Authorization", sender.authorization)
	}
	response, err := sender.doer.Do(request)
	if err != nil {
		return classifyWebhookTransportError(ctx, err)
	}
	if response == nil || response.Body == nil {
		return webhookProtocolFailure()
	}
	defer response.Body.Close()
	if err := consumeWebhookResponse(response.Body); err != nil {
		statusClass := webhookStatusClass(response.StatusCode)
		if errors.Is(err, safehttp.ErrResponseTooLarge) {
			return WebhookDeliveryResult{
				HTTPStatusClass: statusClass,
				ErrorCategory:   "response_too_large",
			}
		}
		return WebhookDeliveryResult{
			Retryable:       true,
			HTTPStatusClass: statusClass,
			ErrorCategory:   "response_read",
		}
	}
	return classifyWebhookStatus(response.StatusCode)
}

func validWebhookPayload(payload WebhookPayload) bool {
	alertID, err := uuid.Parse(payload.AlertID)
	if err != nil || alertID == uuid.Nil ||
		payload.SchemaVersion != WebhookSchemaVersion ||
		!alertIdentifier.MatchString(payload.Category) ||
		!safeAlertSummary(payload.Summary) ||
		!validAlertFloat(payload.CurrentValue) ||
		!validAlertFloat(payload.Threshold) ||
		!validSampleTime(payload.FirstObservedAt) ||
		!validSampleTime(payload.LastObservedAt) ||
		payload.LastObservedAt.Before(payload.FirstObservedAt) ||
		payload.DashboardPath != WebhookDashboardPath {
		return false
	}
	if _, ok := alertCategories[payload.Category]; !ok {
		return false
	}
	switch payload.Severity {
	case AlertSeverityWarning, AlertSeverityCritical:
	default:
		return false
	}
	switch payload.State {
	case AlertStateOpen, AlertStateAcknowledged, AlertStateResolved:
	default:
		return false
	}
	return true
}

func safeWebhookAuthorization(value string) bool {
	if len(value) > maxWebhookAuthorization ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func webhookTimeouts(value safehttp.Timeouts) (safehttp.Timeouts, error) {
	durations := []*time.Duration{
		&value.Connect,
		&value.ResponseHeader,
		&value.TLSHandshake,
		&value.IdleStream,
		&value.Total,
	}
	defaults := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		5 * time.Second,
		10 * time.Second,
		15 * time.Second,
	}
	for index, duration := range durations {
		if *duration < 0 || *duration > maxWebhookNetworkTimeout {
			return safehttp.Timeouts{}, ErrInvalid
		}
		if *duration == 0 {
			*duration = defaults[index]
		}
	}
	return value, nil
}

func consumeWebhookResponse(body io.Reader) error {
	limited := io.LimitReader(body, MaxWebhookResponseBytes+1)
	contents, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(contents) > MaxWebhookResponseBytes {
		return safehttp.ErrResponseTooLarge
	}
	return nil
}

func classifyWebhookTransportError(
	ctx context.Context,
	err error,
) WebhookDeliveryResult {
	if errors.Is(err, safehttp.ErrRedirectRejected) {
		return WebhookDeliveryResult{ErrorCategory: "redirect_rejected"}
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded) ||
		(errors.As(err, &networkError) && networkError.Timeout()) {
		return WebhookDeliveryResult{
			Retryable:     true,
			ErrorCategory: "timeout",
		}
	}
	return WebhookDeliveryResult{
		Retryable:     true,
		ErrorCategory: "network",
	}
}

func classifyWebhookStatus(status int) WebhookDeliveryResult {
	statusClass := webhookStatusClass(status)
	if status >= 200 && status <= 299 {
		return WebhookDeliveryResult{
			Succeeded:       true,
			HTTPStatusClass: statusClass,
		}
	}
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests:
		return WebhookDeliveryResult{
			Retryable:       true,
			HTTPStatusClass: statusClass,
			ErrorCategory:   "rate_limited",
		}
	}
	if status >= 500 && status <= 599 {
		return WebhookDeliveryResult{
			Retryable:       true,
			HTTPStatusClass: statusClass,
			ErrorCategory:   "server_error",
		}
	}
	if status >= 400 && status <= 499 {
		return WebhookDeliveryResult{
			HTTPStatusClass: statusClass,
			ErrorCategory:   "client_error",
		}
	}
	if status >= 300 && status <= 399 {
		return WebhookDeliveryResult{
			HTTPStatusClass: statusClass,
			ErrorCategory:   "redirect_rejected",
		}
	}
	return WebhookDeliveryResult{
		HTTPStatusClass: statusClass,
		ErrorCategory:   "protocol_error",
	}
}

func webhookStatusClass(status int) int {
	statusClass := status / 100
	if statusClass < 0 || statusClass > 5 {
		return 0
	}
	return statusClass
}

func webhookProtocolFailure() WebhookDeliveryResult {
	return WebhookDeliveryResult{ErrorCategory: "protocol_error"}
}
