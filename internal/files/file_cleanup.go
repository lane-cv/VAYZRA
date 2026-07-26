package files

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/objectstore"
)

const fileCleanupLease = 15 * time.Minute

type FileCleanupService struct {
	store               FileCleanupStore
	originals, previews objectstore.Store
	now                 func() time.Time
}

func NewFileCleanupService(store FileCleanupStore, originals, previews objectstore.Store, now func() time.Time) *FileCleanupService {
	if now == nil {
		now = time.Now
	}
	return &FileCleanupService{store: store, originals: originals, previews: previews, now: now}
}

func (s *FileCleanupService) Cleanup(ctx context.Context, limit int) error {
	if s == nil || s.store == nil || s.originals == nil || s.previews == nil || limit < 1 || limit > 1000 {
		return ErrInvalid
	}
	owner := "cleanup:" + uuid.NewString()
	processedCount := 0
	if artifacts, ok := s.store.(ProcessingArtifactCleanupStore); ok {
		for processedCount < limit {
			candidate, processed, err := artifacts.ClaimProcessingArtifactCleanup(ctx, s.now().UTC(), owner, fileCleanupLease)
			if err != nil {
				return err
			}
			if !processed {
				break
			}
			if candidate.ID == uuid.Nil || candidate.ObjectKey == "" {
				return ErrInvalid
			}
			if err := deleteCleanupObject(ctx, s.previews, candidate.ObjectKey); err != nil {
				return err
			}
			if err := artifacts.CompleteProcessingArtifactCleanup(ctx, candidate, owner, s.now().UTC()); err != nil {
				return err
			}
			processedCount++
		}
	}
	for processedCount < limit {
		candidate, processed, err := s.store.ClaimFileCleanup(ctx, s.now().UTC(), owner, fileCleanupLease)
		if err != nil {
			return err
		}
		if !processed {
			return nil
		}
		if candidate.FileID == uuid.Nil || candidate.VersionID == uuid.Nil || candidate.OriginalKey == "" {
			return ErrInvalid
		}
		if err := deleteCleanupObject(ctx, s.originals, candidate.OriginalKey); err != nil {
			return err
		}
		for _, key := range candidate.PreviewKeys {
			if key == "" {
				return ErrInvalid
			}
			if err := deleteCleanupObject(ctx, s.previews, key); err != nil {
				return err
			}
		}
		if err := s.store.CompleteFileCleanup(ctx, candidate, owner, s.now().UTC()); err != nil {
			return err
		}
		processedCount++
	}
	return nil
}

func deleteCleanupObject(ctx context.Context, store objectstore.Store, key string) error {
	err := store.Delete(ctx, key)
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil
	}
	return err
}
