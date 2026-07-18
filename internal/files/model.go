package files

import (
	"errors"
	"io"
	"net"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/auth"
)

const (
	MaxUploadSize  int64 = 524288000
	UploadPartSize int64 = 8 * 1024 * 1024
)

var (
	ErrInvalid             = errors.New("invalid upload request")
	ErrForbidden           = errors.New("upload forbidden")
	ErrNotFound            = errors.New("upload not found")
	ErrUploadExpired       = errors.New("upload expired")
	ErrUploadConflict      = errors.New("upload state conflict")
	ErrUploadPartConflict  = errors.New("upload part conflict")
	ErrTooManyPartRequests = errors.New("too many in-flight upload parts")
	ErrUploadIncomplete    = errors.New("upload incomplete")
	ErrPartHashMismatch    = errors.New("upload part hash mismatch")
	ErrFinalHashMismatch   = errors.New("upload final hash mismatch")
)

type UploadState string

const (
	UploadOpen       UploadState = "open"
	UploadCompleting UploadState = "completing"
	UploadCompleted  UploadState = "completed"
	UploadCancelled  UploadState = "cancelled"
	UploadExpired    UploadState = "expired"
)

type Principal struct {
	User      auth.User
	RequestID string
	IP        net.IP
}

type CreateUploadInput struct {
	DisplayName    string
	DeclaredMIME   string
	ExpectedSize   int64
	ExpectedSHA256 string
}

type PutPartInput struct {
	SessionID uuid.UUID
	Number    int
	Size      int64
	SHA256    string
	Body      io.Reader
}

type UploadSession struct {
	ID             uuid.UUID
	ActorUserID    uuid.UUID
	ObjectKey      string
	MinIOUploadID  string
	DisplayName    string
	DeclaredMIME   string
	ExpectedSize   int64
	ExpectedSHA256 string
	State          UploadState
	ExpiresAt      time.Time
	CreatedAt      time.Time
}

type UploadPart struct {
	SessionID uuid.UUID
	Number    int
	Size      int64
	SHA256    string
	ETag      string
	CreatedAt time.Time
}

type PartView struct {
	Number int    `json:"number"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type UploadView struct {
	ID             uuid.UUID   `json:"id"`
	DisplayName    string      `json:"displayName"`
	DeclaredMIME   string      `json:"declaredMime"`
	ExpectedSize   int64       `json:"expectedSize"`
	ExpectedSHA256 string      `json:"expectedSha256"`
	State          UploadState `json:"state"`
	ExpiresAt      time.Time   `json:"expiresAt"`
	Parts          []PartView  `json:"parts"`
}

type CompletedUpload struct {
	FileID          uuid.UUID `json:"fileId"`
	FileVersionID   uuid.UUID `json:"fileVersionId"`
	ProcessingState string    `json:"processingState"`
}
