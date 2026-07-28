package audit

import (
	"errors"
	"net"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidEvent  = errors.New("invalid audit event")
	ErrInvalidFilter = errors.New("invalid audit filter")
)

type Event struct {
	ActorUserID uuid.UUID
	Action      string
	TargetType  string
	TargetID    string
	Metadata    map[string]any
	RequestID   string
	IP          net.IP
}

type Record struct {
	ID int64
	Event
	OccurredAt time.Time
}

type AuditFilter struct {
	Action     string
	TargetType string
	Outcome    string
	ActorID    uuid.UUID
	From       time.Time
	To         time.Time
	BeforeID   int64
	Limit      int
}

type AuditPage struct {
	Items        []Record
	NextBeforeID int64
}
