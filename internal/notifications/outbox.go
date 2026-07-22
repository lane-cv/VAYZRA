package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
)

const (
	OutboxBatchLimit    = 50
	OutboxLeaseDuration = 30 * time.Second
	OutboxMaxAttempts   = 10
)

var (
	ErrPermanentOutbox = errors.New("permanent outbox failure")
	ErrLeaseLost       = errors.New("outbox lease lost")
)

type OutboxEvent struct {
	ID         uuid.UUID
	Kind       string
	Payload    json.RawMessage
	Attempts   int
	LeaseOwner string
	LeaseUntil time.Time
}

type OutboxStore interface {
	Claim(context.Context, string) ([]OutboxEvent, error)
	DeliverLessonPublication(context.Context, OutboxEvent, string) error
	Complete(context.Context, uuid.UUID, string) error
	Fail(context.Context, uuid.UUID, string, string, bool) error
}

type LessonPublicationPayload struct{ LessonID, RevisionID uuid.UUID }

type categorizedOutboxError struct{ category string }

func (e categorizedOutboxError) Error() string   { return "outbox event rejected: " + e.category }
func (e categorizedOutboxError) Unwrap() error   { return ErrPermanentOutbox }
func permanentOutboxError(category string) error { return categorizedOutboxError{category: category} }
func outboxErrorCategory(err error) string {
	var categorized categorizedOutboxError
	if errors.As(err, &categorized) {
		return categorized.category
	}
	return "delivery_failed"
}

func decodeLessonPublicationPayload(raw json.RawMessage) (LessonPublicationPayload, error) {
	var versioned struct {
		SchemaVersion int       `json:"schemaVersion"`
		LessonID      uuid.UUID `json:"lessonId"`
		RevisionID    uuid.UUID `json:"revisionId"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&versioned); err == nil && versioned.SchemaVersion == 1 && versioned.LessonID != uuid.Nil && versioned.RevisionID != uuid.Nil && decoderAtEOF(dec) {
		return LessonPublicationPayload{versioned.LessonID, versioned.RevisionID}, nil
	}
	// Rows emitted before schema versioning used this exact two-field shape.
	var legacy struct {
		LessonID   uuid.UUID `json:"lesson_id"`
		RevisionID uuid.UUID `json:"revision_id"`
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&legacy); err == nil && legacy.LessonID != uuid.Nil && legacy.RevisionID != uuid.Nil && decoderAtEOF(dec) {
		return LessonPublicationPayload{legacy.LessonID, legacy.RevisionID}, nil
	}
	return LessonPublicationPayload{}, permanentOutboxError("payload_invalid")
}

func decoderAtEOF(dec *json.Decoder) bool {
	var extra any
	return errors.Is(dec.Decode(&extra), io.EOF)
}

func retryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 10 {
		return 15 * time.Minute
	}
	d := time.Second << (attempts - 1)
	if d > 15*time.Minute {
		return 15 * time.Minute
	}
	return d
}
