package auth

import (
	"net"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleAdmin   Role = "admin"
	RoleStudent Role = "student"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type User struct {
	ID                 uuid.UUID
	Username           string
	DisplayName        string
	Role               Role
	Status             Status
	PasswordHash       string
	MustChangePassword bool
	CreatedAt          time.Time
}

type Session struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	TokenHash         [32]byte
	UserAgent         string
	IP                net.IP
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
	RevokeReason      *string
}

type CreateUserParams struct {
	Username           string
	DisplayName        string
	Role               Role
	Status             Status
	PasswordHash       string
	MustChangePassword bool
}

type CreateSessionParams struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	TokenHash         [32]byte
	UserAgent         string
	IP                net.IP
	CreatedAt         time.Time
	LastSeenAt        time.Time
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
}

type PasswordRotationParams struct {
	UserID               uuid.UUID
	ExpectedPasswordHash string
	PasswordHash         string
	MustChangePassword   bool
	ReplacementSession   CreateSessionParams
}
