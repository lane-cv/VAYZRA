package aiqa

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type RuntimeStore interface {
	AdmitRun(context.Context, RuntimeAdmission) (ThreadDetail, Run, error)
	GetRunByIdempotency(context.Context, uuid.UUID, string) (ThreadDetail, Run, error)
	ListThreads(context.Context, uuid.UUID, ThreadCursor) ([]Thread, ThreadCursor, error)
	GetThread(context.Context, uuid.UUID, uuid.UUID, MessageCursor) (ThreadDetail, error)
	LoadContext(context.Context, uuid.UUID, uuid.UUID) ([]Message, error)
	GetRun(context.Context, uuid.UUID, uuid.UUID) (Run, error)
	CancelRun(context.Context, uuid.UUID, uuid.UUID, time.Time) (Run, error)
	RetryRun(context.Context, RuntimeRetryAdmission) (ThreadDetail, Run, error)
}

type RunTerminalStore interface {
	FailRun(context.Context, uuid.UUID, uuid.UUID, string, time.Time) (Run, error)
	SucceedRun(context.Context, uuid.UUID, uuid.UUID, string, TerminalUsage, time.Time) (Run, error)
}
