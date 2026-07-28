package operations

import (
	"context"
	"net"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type Principal struct {
	User      auth.User
	RequestID string
	IP        net.IP
}

type ModeSnapshot struct {
	Mode      string
	OwnerID   uuid.UUID
	ExpiresAt time.Time
	Version   int64
}

type LeaseRequest struct {
	Mode      string
	OwnerID   uuid.UUID
	ExpiresAt time.Time
}

type Store interface {
	GetSettings(context.Context) (Settings, error)
	UpdateSettings(context.Context, Principal, Settings) (Settings, error)
	GetMode(context.Context) (ModeSnapshot, error)
	AcquireLease(context.Context, LeaseRequest) (Lease, error)
	RenewLease(context.Context, Lease, time.Time) (Lease, error)
	TransitionLease(context.Context, Lease, string, time.Time) (Lease, error)
	ReleaseLease(context.Context, Lease) error
}

type WriteGate interface {
	AcquireShared(context.Context) (release func(), err error)
}

type ClaimGate interface {
	ClaimsAllowed(context.Context) (bool, error)
}

type SettingsRejectionAuditor interface {
	AuditSettingsRejection(context.Context, Principal, string) error
}

type SettingsStore interface {
	GetSettings(context.Context) (Settings, error)
	UpdateSettings(context.Context, Principal, Settings) (Settings, error)
}

type ServiceStore interface {
	SettingsStore
	SettingsRejectionAuditor
}

type HTTPService interface {
	GetSettings(context.Context, Principal) (Settings, error)
	UpdateSettings(context.Context, Principal, Settings) (Settings, error)
}

type ModeReader interface {
	GetMode(context.Context) (ModeSnapshot, error)
}

type LeaseManager interface {
	AcquireLease(context.Context, LeaseRequest) (Lease, error)
	RenewLease(context.Context, Lease, time.Time) (Lease, error)
	TransitionLease(context.Context, Lease, string, time.Time) (Lease, error)
	ReleaseLease(context.Context, Lease) error
}

// LeaseSessionCloser must be closed before its backing PostgreSQL pool.
type LeaseSessionCloser interface {
	Close(context.Context) error
}
