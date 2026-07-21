package notifications

import (
	"context"
	"github.com/google/uuid"
)

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }
func (s *Service) List(ctx context.Context, recipient uuid.UUID, cursor Cursor) ([]Notification, Cursor, error) {
	if recipient == uuid.Nil || s == nil || s.store == nil || cursor.Limit < 1 || cursor.Limit > 100 || (cursor.ID == uuid.Nil) != cursor.CreatedAt.IsZero() {
		return nil, Cursor{}, ErrInvalidInput
	}
	return s.store.List(ctx, recipient, cursor)
}
func (s *Service) UnreadCount(ctx context.Context, recipient uuid.UUID) (int64, error) {
	if recipient == uuid.Nil || s == nil || s.store == nil {
		return 0, ErrInvalidInput
	}
	return s.store.UnreadCount(ctx, recipient)
}
func (s *Service) MarkRead(ctx context.Context, recipient, id uuid.UUID) error {
	if recipient == uuid.Nil || id == uuid.Nil || s == nil || s.store == nil {
		return ErrInvalidInput
	}
	return s.store.MarkRead(ctx, recipient, id)
}
func (s *Service) MarkAllRead(ctx context.Context, recipient uuid.UUID) (int64, error) {
	if recipient == uuid.Nil || s == nil || s.store == nil {
		return 0, ErrInvalidInput
	}
	return s.store.MarkAllRead(ctx, recipient)
}
