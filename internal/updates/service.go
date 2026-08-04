package updates

import (
	"context"
	"errors"
	"net"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

var (
	ErrInvalid           = errors.New("invalid update request")
	ErrForbidden         = errors.New("update access forbidden")
	ErrAgentUnavailable  = errors.New("update agent unavailable")
	ErrUpdateBusy        = errors.New("update already in progress")
	ErrDirtyCheckout     = errors.New("update checkout is dirty")
	ErrUpdateUnavailable = errors.New("updates are not configured")
)

type Principal struct {
	User      auth.User
	RequestID string
	IP        net.IP
}

type Agent interface {
	Status(context.Context) (Status, error)
	Check(context.Context) (Status, error)
	Apply(context.Context) (Status, error)
}

type HTTPService interface {
	Status(context.Context, Principal) (Status, error)
	Check(context.Context, Principal) (Status, error)
	Apply(context.Context, Principal) (Status, error)
}

type service struct {
	agent Agent
	audit audit.Writer
}

func NewService(agent Agent, writer audit.Writer) (HTTPService, error) {
	if agent == nil {
		return nil, ErrInvalid
	}
	return &service{agent: agent, audit: writer}, nil
}

func (s *service) Status(ctx context.Context, principal Principal) (Status, error) {
	if err := authorize(principal); err != nil {
		return Status{}, err
	}
	return s.agent.Status(ctx)
}

func (s *service) Check(ctx context.Context, principal Principal) (Status, error) {
	if err := authorize(principal); err != nil {
		return Status{}, err
	}
	return s.agent.Check(ctx)
}

func (s *service) Apply(ctx context.Context, principal Principal) (Status, error) {
	if err := authorize(principal); err != nil {
		return Status{}, err
	}
	if s.audit != nil {
		if err := s.audit.Write(ctx, audit.Event{
			ActorUserID: principal.User.ID,
			Action:      "operations.update_requested",
			TargetType:  "application_update",
			TargetID:    "global",
			Metadata:    map[string]any{"status": "requested"},
			RequestID:   principal.RequestID,
			IP:          append(net.IP(nil), principal.IP...),
		}); err != nil {
			return Status{}, err
		}
	}
	return s.agent.Apply(ctx)
}

func authorize(principal Principal) error {
	if principal.User.ID == uuid.Nil ||
		principal.User.Role != auth.RoleAdmin ||
		principal.User.Status != auth.StatusActive {
		return ErrForbidden
	}
	return nil
}
