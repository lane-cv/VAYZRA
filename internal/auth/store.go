package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound = errors.New("auth record not found")
	ErrConflict = errors.New("auth record conflict")
)

type UserStore interface {
	FindByUsername(context.Context, string) (User, error)
	FindByID(context.Context, uuid.UUID) (User, error)
	Create(context.Context, CreateUserParams) (User, error)
	UpdatePassword(context.Context, uuid.UUID, string, bool) error
	SetStatus(context.Context, uuid.UUID, Status) error
	ListStudents(context.Context, int, uuid.UUID) ([]User, error)
}

type SessionStore interface {
	Create(context.Context, CreateSessionParams) error
	FindActiveByTokenHash(context.Context, [32]byte, time.Time) (Session, User, error)
	Touch(context.Context, uuid.UUID, time.Time, time.Time) error
	Revoke(context.Context, uuid.UUID, string) error
	RevokeAllForUser(context.Context, uuid.UUID, string) error
	RevokeAllExceptForUser(context.Context, uuid.UUID, uuid.UUID, string) error
}

type PasswordRotationStore interface {
	RotatePassword(context.Context, PasswordRotationParams) error
}
