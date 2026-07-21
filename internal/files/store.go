package files

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type UploadStore interface {
	CreateSession(context.Context, UploadSession) error
	GetSession(context.Context, uuid.UUID, uuid.UUID, UploadPurpose) (UploadSession, []UploadPart, error)
	AdmitPart(context.Context, uuid.UUID, uuid.UUID, UploadPurpose, int, int64, string, time.Time) (UploadSession, *UploadPart, error)
	RecordPart(context.Context, uuid.UUID, UploadPart) (UploadPart, error)
	BeginCompletion(context.Context, uuid.UUID, uuid.UUID, UploadPurpose, time.Time) (UploadSession, []UploadPart, *CompletedUpload, error)
	ReopenCompletion(context.Context, uuid.UUID) error
	FinishCompletion(context.Context, UploadSession, Principal) (CompletedUpload, error)
	CancelSession(context.Context, uuid.UUID, uuid.UUID, UploadPurpose, UploadState) (UploadSession, error)
	ClaimCleanup(context.Context, time.Time, int) ([]UploadSession, error)
	ConfirmCleanup(context.Context, uuid.UUID) (UploadSession, error)
	FinishCleanup(context.Context, uuid.UUID) error
}
type AccessStore interface {
	ResolveAccess(context.Context, uuid.UUID, uuid.UUID, AccessAction) (Delivery, error)
	WriteAccessLog(context.Context, AccessLog) error
}

type BindingStore interface {
	ListDraftBindings(context.Context, uuid.UUID) ([]DraftBinding, error)
	ReplaceDraftBindings(context.Context, Principal, uuid.UUID, int64, []DraftBindingInput) ([]DraftBinding, error)
}

type FileCenterStore interface {
	ListFiles(context.Context, FileFilter, Cursor) (FilePage, error)
	FileDetail(context.Context, uuid.UUID) (FileDetail, error)
	FileVersion(context.Context, uuid.UUID) (FileVersionDetail, error)
	RetryFile(context.Context, Principal, uuid.UUID) error
	ReplaceFile(context.Context, Principal, uuid.UUID, uuid.UUID, time.Time) error
	RollbackFile(context.Context, Principal, uuid.UUID, uuid.UUID, uuid.UUID) error
	DeleteFile(context.Context, Principal, uuid.UUID, time.Time) error
}

type FileCleanupCandidate struct {
	FileID      uuid.UUID
	VersionID   uuid.UUID
	OriginalKey string
	PreviewKeys []string
}

type FileCleanupStore interface {
	ClaimFileCleanup(context.Context, time.Time, string, time.Duration) (FileCleanupCandidate, bool, error)
	CompleteFileCleanup(context.Context, FileCleanupCandidate, string, time.Time) error
}
