package audit

import (
	"errors"
	"net"
	"time"

	"github.com/google/uuid"
)

var ErrInvalidEvent = errors.New("invalid audit event")

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
