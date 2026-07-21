package qanda

import (
	"context"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
)

type Store interface {
	ListStudentThreads(context.Context, uuid.UUID, Status, ThreadCursor) ([]Thread, ThreadCursor, error)
	GetStudentThread(context.Context, uuid.UUID, uuid.UUID) (Thread, error)
	ListStudentMessages(context.Context, uuid.UUID, uuid.UUID, MessageCursor) ([]Message, MessageCursor, error)
}

type TxStore interface {
	FindMessageByIdempotency(context.Context, uuid.UUID, string) (Thread, Message, error)
	CreateThreadWithFirstMessage(context.Context, uuid.UUID, CreateThreadInput, time.Time) (Thread, Message, bool, error)
	LockStudentThread(context.Context, uuid.UUID, uuid.UUID) (Thread, error)
	AppendStudentMessage(context.Context, Thread, uuid.UUID, AddMessageInput, Status, time.Time) (Thread, Message, error)
	ActiveAdminID(context.Context) (uuid.UUID, error)
}

type NotificationWriter interface {
	Notify(context.Context, NotificationIntent) error
}

type UnitOfWork interface {
	WithinTx(context.Context, func(TxStore, audit.Writer, NotificationWriter) error) error
}
