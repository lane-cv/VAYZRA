package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestServiceScopesReadsAndUsesStableCursor(t *testing.T) {
	recipient := uuid.New()
	store := &fakeStore{items: []Notification{{ID: uuid.New(), Kind: KindQACreated, CreatedAt: time.Now().UTC()}}}
	svc := NewService(store)
	items, next, err := svc.List(context.Background(), recipient, Cursor{Limit: 20})
	if err != nil || len(items) != 1 || store.recipient != recipient || next.Limit != 20 {
		t.Fatalf("List() items=%v next=%+v err=%v recipient=%s", items, next, err, store.recipient)
	}
	if _, _, err = svc.List(context.Background(), uuid.Nil, Cursor{Limit: 20}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil recipient err=%v", err)
	}
}

func TestServiceForeignMarkReadIsNotFound(t *testing.T) {
	store := &fakeStore{markErr: ErrNotFound}
	err := NewService(store).MarkRead(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestServiceMarkAllDelegatesRecipientScopedCutoff(t *testing.T) {
	recipient := uuid.New()
	store := &fakeStore{}
	count, err := NewService(store).MarkAllRead(context.Background(), recipient)
	if err != nil || count != 3 || store.recipient != recipient {
		t.Fatalf("count=%d err=%v recipient=%s", count, err, store.recipient)
	}
}

type fakeStore struct {
	items     []Notification
	recipient uuid.UUID
	markErr   error
}

func (f *fakeStore) List(_ context.Context, id uuid.UUID, c Cursor) ([]Notification, Cursor, error) {
	f.recipient = id
	return f.items, Cursor{CreatedAt: time.Now().UTC(), ID: uuid.New(), Limit: c.Limit}, nil
}
func (f *fakeStore) UnreadCount(_ context.Context, id uuid.UUID) (int64, error) {
	f.recipient = id
	return 2, nil
}
func (f *fakeStore) MarkRead(_ context.Context, id, _ uuid.UUID) error {
	f.recipient = id
	return f.markErr
}
func (f *fakeStore) MarkAllRead(_ context.Context, id uuid.UUID) (int64, error) {
	f.recipient = id
	return 3, nil
}
