package files

import (
	"context"
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
	ErrRangeNotSatisfiable = errors.New("range not satisfiable")
	ErrAccessUnavailable   = errors.New("file access unavailable")
	ErrDraftConflict       = errors.New("draft version conflict")
	ErrFileInUse           = errors.New("file is referenced")
	ErrFileVersionExpired  = errors.New("file version expired")
	ErrFileNotRetryable    = errors.New("file processing is not retryable")
	ErrFileTooLarge        = errors.New("file too large")
	ErrFileTypeRejected    = errors.New("file type rejected")
)

const FileRetentionPeriod = 30 * 24 * time.Hour

type UploadState string

type UploadPurpose string

const (
	UploadPurposeTeaching UploadPurpose = "teaching"
	UploadPurposeQA       UploadPurpose = "qa_attachment"
)

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
	Purpose        UploadPurpose
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
type AccessPolicy string

const (
	PolicyPreview  AccessPolicy = "preview"
	PolicyDownload AccessPolicy = "download"
)

type AccessAction string

const (
	ActionPreview  AccessAction = "preview"
	ActionDownload AccessAction = "download"
)

type AccessResult string

const (
	AccessAllowed   AccessResult = "allow"
	AccessDenied    AccessResult = "deny"
	AccessMalformed AccessResult = "malformed"
	AccessFailed    AccessResult = "fail"
)

type DraftBindingInput struct {
	FileVersionID uuid.UUID    `json:"fileVersionId"`
	Policy        AccessPolicy `json:"policy"`
	DisplayName   string       `json:"displayName"`
	Description   string       `json:"description"`
	SortPosition  int64        `json:"sortPosition"`
}

type DraftBinding struct {
	ID       uuid.UUID `json:"id"`
	LessonID uuid.UUID `json:"lessonId"`
	DraftBindingInput
}

type Delivery struct {
	VersionID   uuid.UUID
	RevisionID  uuid.UUID
	ObjectKey   string
	DisplayName string
	ContentType string
	Size        int64
	Policy      AccessPolicy
	Playable    bool
	Preview     bool
}

type AccessLog struct {
	ActorUserID         uuid.UUID
	RequestedVersionID  uuid.UUID
	VersionID           uuid.UUID
	RevisionID          uuid.UUID
	QAMessageID         uuid.UUID
	Action              AccessAction
	Result              AccessResult
	Reason              string
	RequestID           string
	IP                  net.IP
	RangeStart          *int64
	RangeEnd            *int64
	PlaybackSessionHash string
}

type QAOpenInput struct {
	VersionID uuid.UUID
	Action    AccessAction
	Range     string
}

// QAFileStatus is deliberately capability-safe: it contains neither storage
// coordinates nor file ownership/provenance identifiers.
type QAFileStatus struct {
	FileVersionID    uuid.UUID `json:"fileVersionId"`
	ProcessingState  string    `json:"processingState"`
	FailureCategory  string    `json:"failureCategory,omitempty"`
	DetectedMIME     string    `json:"detectedMime,omitempty"`
	Size             int64     `json:"size"`
	PreviewAvailable bool      `json:"previewAvailable"`
}

type QADelivery struct {
	Delivery
	MessageID uuid.UUID
}

type OpenInput struct {
	VersionID       uuid.UUID
	Action          AccessAction
	Range           string
	PlaybackSession string
}

type RangeError struct{ Size int64 }

func (e *RangeError) Error() string { return ErrRangeNotSatisfiable.Error() }
func (e *RangeError) Unwrap() error { return ErrRangeNotSatisfiable }

type ResponseRange struct{ Start, End, Total int64 }

type OpenedFile struct {
	Body          io.ReadCloser
	DisplayName   string
	ContentType   string
	Size          int64
	Partial       bool
	Range         ResponseRange
	Playable      bool
	ReportFailure func(context.Context, string) error
}

type FileFilter struct {
	Name        string
	Type        string
	State       string
	Reference   string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

type Cursor struct {
	AfterCreatedAt time.Time
	AfterID        uuid.UUID
	Limit          int
}

type FileReference struct {
	Kind        string    `json:"kind"`
	LessonID    uuid.UUID `json:"lessonId"`
	LessonTitle string    `json:"lessonTitle"`
	RevisionID  uuid.UUID `json:"revisionId,omitempty"`
}

type FileVersionDetail struct {
	ID              uuid.UUID  `json:"id"`
	FileID          uuid.UUID  `json:"fileId"`
	Version         int64      `json:"version"`
	DisplayName     string     `json:"displayName"`
	DeclaredMIME    string     `json:"declaredMime"`
	DetectedMIME    string     `json:"detectedMime,omitempty"`
	Size            int64      `json:"size"`
	ProcessingState string     `json:"processingState"`
	FailureCategory string     `json:"failureCategory,omitempty"`
	PreviewState    string     `json:"previewState,omitempty"`
	BrowserPlayable bool       `json:"browserPlayable"`
	CreatedAt       time.Time  `json:"createdAt"`
	RetentionUntil  *time.Time `json:"retentionUntil,omitempty"`
}

type FileListItem struct {
	ID         uuid.UUID         `json:"id"`
	CreatedAt  time.Time         `json:"createdAt"`
	DeletedAt  *time.Time        `json:"deletedAt,omitempty"`
	Latest     FileVersionDetail `json:"latest"`
	References int               `json:"referenceCount"`
}

type FilePage struct {
	Items      []FileListItem `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

type FileDetail struct {
	ID         uuid.UUID           `json:"id"`
	CreatedAt  time.Time           `json:"createdAt"`
	DeletedAt  *time.Time          `json:"deletedAt,omitempty"`
	Versions   []FileVersionDetail `json:"versions"`
	References []FileReference     `json:"references"`
}
