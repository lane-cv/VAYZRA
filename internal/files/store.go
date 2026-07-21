package files

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type UploadStore interface {
	CreateSession(context.Context, UploadSession) error
	GetSession(context.Context, uuid.UUID, uuid.UUID) (UploadSession, []UploadPart, error)
	AdmitPart(context.Context, uuid.UUID, uuid.UUID, int, int64, string, time.Time) (UploadSession, *UploadPart, error)
	RecordPart(context.Context, uuid.UUID, UploadPart) (UploadPart, error)
	BeginCompletion(context.Context, uuid.UUID, uuid.UUID, time.Time) (UploadSession, []UploadPart, *CompletedUpload, error)
	ReopenCompletion(context.Context, uuid.UUID) error
	FinishCompletion(context.Context, UploadSession, Principal) (CompletedUpload, error)
	CancelSession(context.Context, uuid.UUID, uuid.UUID, UploadState) (UploadSession, error)
	ClaimCleanup(context.Context, time.Time, int) ([]UploadSession, error)
	ConfirmCleanup(context.Context, uuid.UUID) (UploadSession, error)
	FinishCleanup(context.Context, uuid.UUID) error
}
type AccessStore interface {
	ResolveAccess(context.Context, uuid.UUID, uuid.UUID, AccessAction) (Delivery, error)
	WriteAccessLog(context.Context, AccessLog) error
}

type BindingStore interface {
	ReplaceDraftBindings(context.Context, Principal, uuid.UUID, int64, []DraftBindingInput) ([]DraftBinding, error)
}
