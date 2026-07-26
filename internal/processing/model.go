package processing

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	KindProcessFile = "process_file"
	StateQueued     = "queued"
	StateRunning    = "running"
	StateCompleted  = "completed"
	StateFailed     = "failed"
)

var (
	ErrNoJob     = errors.New("no processing job available")
	ErrLeaseLost = errors.New("processing lease lost")
)

type Job struct {
	ID            uuid.UUID
	FileVersionID uuid.UUID
	Kind          string
	State         string
	Attempts      int
	LeaseOwner    string
	LeaseUntil    time.Time
}

type Result struct {
	DetectedMIME    string
	ScanResult      string
	BrowserPlayable bool
	VideoContainer  string
	VideoCodec      string
	VideoDurationMS *int64
	VideoWidth      *int
	VideoHeight     *int
	Preview         *PreviewResult
	AIText          *PreviewResult
}

type PreviewResult struct {
	Kind        string
	ObjectKey   string
	ContentType string
	Size        int64
	SHA256      string
}

type ArtifactState string

const (
	ArtifactReserved      ArtifactState = "reserved"
	ArtifactStored        ArtifactState = "stored"
	ArtifactDeletePending ArtifactState = "delete_pending"
)

type ProcessingArtifact struct {
	FileVersionID, ProcessingJobID uuid.UUID
	AttemptNo                      int
	Kind                           string
	ObjectKey                      string
	ContentType                    string
	Size                           int64
	SHA256                         string
}

type SourceFile struct {
	VersionID    uuid.UUID
	ObjectKey    string
	DisplayName  string
	DeclaredMIME string
	Size         int64
	SHA256       string
	Purpose      string
}

type Failure struct {
	Category  string
	Permanent bool
	Rejected  bool
	RetryAt   time.Time
}

type ProcessingError struct {
	Category  string
	Permanent bool
	Rejected  bool
}

func (e *ProcessingError) Error() string { return "file processing failed" }
