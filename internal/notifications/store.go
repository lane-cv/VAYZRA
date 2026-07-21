package notifications

import (
	"context"
	"github.com/google/uuid"
)

type Store interface {
	List(context.Context, uuid.UUID, Cursor) ([]Notification, Cursor, error)
	UnreadCount(context.Context, uuid.UUID) (int64, error)
	MarkRead(context.Context, uuid.UUID, uuid.UUID) error
	MarkAllRead(context.Context, uuid.UUID) (int64, error)
}
