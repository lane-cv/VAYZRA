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

func TestFileCleanupEventuallyReclaimsDurablyTrackedProcessingArtifact(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	artifact := ProcessingArtifactCleanupCandidate{ID: uuid.New(), ObjectKey: "previews/version/job/1/ai_text.txt"}
	store := &fileCleanupStoreStub{artifact: artifact, served: true}
	previews := newFakeObjects()
	previews.deleteErr = objectstore.ErrUnavailable
	service := NewFileCleanupService(store, newFakeObjects(), previews, func() time.Time { return now })

	if err := service.Cleanup(context.Background(), 1); err == nil {
		t.Fatal("first cleanup unexpectedly succeeded")
	}
	if store.artifactCompleted != 0 {
		t.Fatal("failed object deletion removed durable registry entry")
	}

	previews.deleteErr = nil
	now = now.Add(fileCleanupLease + time.Second)
	if err := service.Cleanup(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if store.artifactCompleted != 1 || previews.deleteCalls.Load() != 2 {
		t.Fatalf("completed=%d delete_calls=%d", store.artifactCompleted, previews.deleteCalls.Load())
	}
}

type fileCleanupStoreStub struct {
	candidate          FileCleanupCandidate
	served             bool
	completed          int
	artifact           ProcessingArtifactCleanupCandidate
	artifactLeaseUntil time.Time
	artifactCompleted  int
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
func (s *fileCleanupStoreStub) ClaimProcessingArtifactCleanup(_ context.Context, now time.Time, _ string, lease time.Duration) (ProcessingArtifactCleanupCandidate, bool, error) {
	if s.artifact.ID == uuid.Nil || s.artifactCompleted > 0 || (!s.artifactLeaseUntil.IsZero() && !now.After(s.artifactLeaseUntil)) {
		return ProcessingArtifactCleanupCandidate{}, false, nil
	}
	s.artifactLeaseUntil = now.Add(lease)
	return s.artifact, true, nil
}
func (s *fileCleanupStoreStub) CompleteProcessingArtifactCleanup(_ context.Context, candidate ProcessingArtifactCleanupCandidate, _ string, _ time.Time) error {
	if candidate != s.artifact {
		return ErrInvalid
	}
	s.artifactCompleted++
	return nil
}
