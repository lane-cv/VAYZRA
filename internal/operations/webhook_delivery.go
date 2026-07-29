package operations

import (
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultWebhookLeaseDuration = 30 * time.Second
	maxWebhookLeaseDuration     = 5 * time.Minute
)

var (
	ErrWebhookNotConfigured  = errors.New("webhook not configured")
	ErrWebhookDeliveryFailed = errors.New("webhook delivery failed")
)

type WebhookEvent struct {
	ID              uuid.UUID
	AlertID         uuid.UUID
	TransitionKind  AlertTransitionKind
	AlertVersion    int64
	Category        string
	Severity        AlertSeverity
	State           AlertState
	Summary         string
	CurrentValue    float64
	ThresholdValue  float64
	FirstObservedAt time.Time
	LastObservedAt  time.Time
}

type WebhookDeliveryJob struct {
	ID             uuid.UUID
	EventID        uuid.UUID
	AlertID        uuid.UUID
	Attempt        int
	ScheduledAt    time.Time
	StartedAt      time.Time
	ClaimOwner     string
	ClaimToken     uuid.UUID
	ClaimExpiresAt time.Time
	Event          WebhookEvent
}

type WebhookDeliveryResult struct {
	Succeeded       bool
	Retryable       bool
	HTTPStatusClass int
	ErrorCategory   string
}

var webhookClaimOwner = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var webhookDeliveryErrorCategories = map[string]struct{}{
	"client_error":       {},
	"network":            {},
	"protocol_error":     {},
	"rate_limited":       {},
	"redirect_rejected":  {},
	"request_too_large":  {},
	"response_read":      {},
	"response_too_large": {},
	"server_error":       {},
	"timeout":            {},
}

func validWebhookDeliveryResult(result WebhookDeliveryResult) bool {
	if result.HTTPStatusClass < 0 || result.HTTPStatusClass > 5 {
		return false
	}
	if result.Succeeded {
		return !result.Retryable &&
			result.HTTPStatusClass == 2 &&
			result.ErrorCategory == ""
	}
	if result.ErrorCategory == "" {
		return false
	}
	_, ok := webhookDeliveryErrorCategories[result.ErrorCategory]
	return ok
}
