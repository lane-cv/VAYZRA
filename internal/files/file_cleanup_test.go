package files

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/objectstore"
)

func TestFileCleanupDeletesObjectsBeforeCompletingMetadata(t *testing.T) {
	candidate := FileCleanupCandidate{FileID: uuid.New(), VersionID: uuid.New(), OriginalKey: "originals/private", PreviewKeys: []string{"previews/one", "previews/two"}}
	store := &fileCleanupStoreStub{candidate: candidate}
	originals, previews := newFakeObjects(), newFakeObjects()
	service := NewFileCleanupService(store, originals, previews, func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) })
	if err := service.Cleanup(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if store.completed != 1 || originals.deleteCalls.Load() != 1 || previews.deleteCalls.Load() != 2 {
		t.Fatalf("completed=%d originals=%d previews=%d", store.completed, originals.deleteCalls.Load(), previews.deleteCalls.Load())
	}
}

func TestFileCleanupRetainsMetadataWhenObjectDeletionFails(t *testing.T) {
	store := &fileCleanupStoreStub{candidate: FileCleanupCandidate{FileID: uuid.New(), VersionID: uuid.New(), OriginalKey: "original"}}
	originals := newFakeObjects()
	originals.deleteErr = objectstore.ErrUnavailable
	service := NewFileCleanupService(store, originals, newFakeObjects(), time.Now)
	if err := service.Cleanup(context.Background(), 1); err == nil || store.completed != 0 {
		t.Fatalf("completed=%d err=%v", store.completed, err)
	}
}

type fileCleanupStoreStub struct {
	candidate FileCleanupCandidate
	served    bool
	completed int
}

func (s *fileCleanupStoreStub) ClaimFileCleanup(context.Context, time.Time, string, time.Duration) (FileCleanupCandidate, bool, error) {
	if s.served {
		return FileCleanupCandidate{}, false, nil
	}
	s.served = true
	return s.candidate, true, nil
}
func (s *fileCleanupStoreStub) CompleteFileCleanup(_ context.Context, candidate FileCleanupCandidate, _ string, _ time.Time) error {
	if candidate.VersionID != s.candidate.VersionID {
		return ErrInvalid
	}
	s.completed++
	return nil
}
