package aiqa

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"net"
	"net/url"
	"time"
)

var (
	ErrForbidden           = errors.New("ai configuration forbidden")
	ErrInvalidInput        = errors.New("invalid ai configuration input")
	ErrNotFound            = errors.New("ai configuration not found")
	ErrConfigConflict      = errors.New("ai configuration conflict")
	ErrAIDisabled          = errors.New("ai disabled")
	ErrProviderUnavailable = errors.New("AI provider unavailable")
	ErrProviderTestBusy    = errors.New("AI provider test already active")
)

type ProtocolMode string
type Modality string
type Subject string

const (
	ProtocolChatCompletions ProtocolMode = "chat_completions"
	ProtocolResponses       ProtocolMode = "responses"
	ModalityText            Modality     = "text"
	ModalityVision          Modality     = "vision"
	SubjectMath             Subject      = "math"
	SubjectPhysics          Subject      = "physics"
)

type Principal struct {
	User      auth.User
	RequestID string
	IP        net.IP
}
type ProviderView struct {
	ID           uuid.UUID    `json:"id"`
	Name         string       `json:"name"`
	BaseURL      string       `json:"baseUrl"`
	ProtocolMode ProtocolMode `json:"protocolMode"`
	Active       bool         `json:"active"`
	HasKey       bool         `json:"hasKey"`
	KeyUpdatedAt time.Time    `json:"keyUpdatedAt"`
	Version      int64        `json:"version"`
}
type ModelView struct {
	ID                  uuid.UUID  `json:"id"`
	ProviderID          uuid.UUID  `json:"providerId"`
	UpstreamModelID     string     `json:"upstreamModelId"`
	Modality            Modality   `json:"modality"`
	ContextTokens       int64      `json:"contextTokens"`
	MaxOutputTokens     int64      `json:"maxOutputTokens"`
	ImageQuotaTokens    int64      `json:"imageQuotaTokens"`
	InputPriceMicroUSD  int64      `json:"inputPriceMicroUsd"`
	OutputPriceMicroUSD int64      `json:"outputPriceMicroUsd"`
	Enabled             bool       `json:"enabled"`
	QuotaBlockedAt      *time.Time `json:"quotaBlockedAt,omitempty"`
	QuotaBlockReason    string     `json:"quotaBlockReason,omitempty"`
	Version             int64      `json:"version"`
}
type PromptView struct {
	ID      uuid.UUID `json:"id"`
	Subject Subject   `json:"subject"`
	Version int64     `json:"version"`
	Body    string    `json:"body"`
	Active  bool      `json:"active"`
}
type LimitValue struct {
	Mode  string `json:"mode"`
	Value *int64 `json:"value,omitempty"`
}
type LimitView struct {
	DailyRequests   LimitValue `json:"dailyRequests"`
	MonthlyRequests LimitValue `json:"monthlyRequests"`
	DailyTokens     LimitValue `json:"dailyTokens"`
	MonthlyTokens   LimitValue `json:"monthlyTokens"`
	Version         int64      `json:"version"`
}
type LimitViews struct {
	Global   LimitView               `json:"global"`
	Students map[uuid.UUID]LimitView `json:"students"`
}
type CreateProviderInput struct {
	Name, BaseURL, APIKey, IdempotencyKey string
	ProtocolMode                          ProtocolMode
}
type UpdateProviderInput struct {
	ID              uuid.UUID
	Name, BaseURL   string
	ProtocolMode    ProtocolMode
	APIKey          *string
	ExpectedVersion int64
}
type PutModelInput struct {
	ProviderID, ID                                                                            uuid.UUID
	UpstreamModelID                                                                           string
	Modality                                                                                  Modality
	ContextTokens, MaxOutputTokens, ImageQuotaTokens, InputPriceMicroUSD, OutputPriceMicroUSD int64
	Enabled, ClearQuotaBlock                                                                  bool
	ExpectedVersion                                                                           int64
}
type PutPromptInput struct {
	Subject         Subject
	Body            string
	ExpectedVersion int64
}
type PutLimitsInput struct {
	DailyRequests, MonthlyRequests, DailyTokens, MonthlyTokens LimitValue
	ExpectedVersion                                            int64
}
type RuntimeProviderConfig struct {
	ProviderID   uuid.UUID
	BaseURL      *url.URL
	ProtocolMode ProtocolMode
	APIKey       []byte
	Model        ModelView
	Prompt       PromptView
	Timeouts     GatewayTimeouts
}

type ConnectivityResult struct {
	OK            bool         `json:"ok"`
	Protocol      ProtocolMode `json:"protocol"`
	LatencyMS     int64        `json:"latencyMs"`
	ErrorCategory string       `json:"errorCategory"`
}
type RuntimeConfigSource interface {
	ForRun(context.Context, Subject, Modality) (RuntimeProviderConfig, error)
}
