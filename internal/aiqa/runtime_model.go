package aiqa

import (
	"time"

	"github.com/google/uuid"
)

type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunStreaming RunStatus = "streaming"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

type Thread struct {
	ID            uuid.UUID
	StudentID     uuid.UUID
	Title         string
	Subject       Subject
	LastMessageAt time.Time
	CreatedAt     time.Time
}

type Message struct {
	ID          uuid.UUID
	ThreadID    uuid.UUID
	Role        string
	Body        string
	RunID       uuid.UUID
	Attachments []AttachmentMetadata
	CreatedAt   time.Time
}

type Run struct {
	ID                 uuid.UUID
	ThreadID           uuid.UUID
	TriggerMessageID   uuid.UUID
	TriggerBody        string
	TriggerAttachments []AttachmentMetadata
	Status             RunStatus
	AttemptNo          int
	LastSequence       int64
	ErrorCode          string
	Modality           Modality
	ReservedTokenCount int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type ThreadDetail struct {
	Thread            Thread
	Messages          []Message
	ActiveRun         *Run
	NextMessageCursor MessageCursor
}

type CreateThreadInput struct {
	Title, Body, IdempotencyKey string
	Subject                     Subject
	Attachments                 []AttachmentInput
}

type AddMessageInput struct {
	ThreadID             uuid.UUID
	Body, IdempotencyKey string
	Attachments          []AttachmentInput
}

type ThreadCursor struct {
	LastMessageAt time.Time
	ID            uuid.UUID
	Limit         int
}

type MessageCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
	Limit     int
}

type GatewayContextTurn struct {
	Role string
	Text string
}

type RuntimeSnapshot struct {
	Provider     RuntimeProviderConfig
	PromptSHA256 string
}

type RuntimeAdmission struct {
	StudentID      uuid.UUID
	ThreadID       uuid.UUID
	CreateThread   bool
	ThreadTitle    string
	Subject        Subject
	MessageID      uuid.UUID
	MessageBody    string
	IdempotencyKey string
	Attachments    []AttachmentMetadata
	AttemptNo      int
	SelectedTurns  []GatewayContextTurn
	ExtractedText  string
	ImageCount     int
	Snapshot       RuntimeSnapshot
	Reservation    QuotaReservation
	Now            time.Time
}

type RuntimeRetryAdmission struct {
	StudentID      uuid.UUID
	SourceRunID    uuid.UUID
	RunID          uuid.UUID
	IdempotencyKey string
	Snapshot       RuntimeSnapshot
	Reservation    QuotaReservation
	Now            time.Time
}

type TerminalUsage struct {
	InputTokens, OutputTokens, CostMicroUSD int64
	UsageSource, FinishReason, ErrorCode    string
}
