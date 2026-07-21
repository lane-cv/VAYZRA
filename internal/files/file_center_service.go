package files

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type FileCenterService struct {
	store FileCenterStore
	now   func() time.Time
}

func NewFileCenterService(store FileCenterStore, now func() time.Time) *FileCenterService {
	if now == nil {
		now = time.Now
	}
	return &FileCenterService{store: store, now: now}
}

func (s *FileCenterService) List(ctx context.Context, actor Principal, filter FileFilter, cursor Cursor) (FilePage, error) {
	if s == nil || s.store == nil || validatePrincipal(actor) != nil {
		return FilePage{}, ErrForbidden
	}
	filter.Name = strings.ToLower(strings.TrimSpace(filter.Name))
	if cursor.Limit == 0 {
		cursor.Limit = 50
	}
	if !validFileFilter(filter) || cursor.Limit < 1 || cursor.Limit > 100 || (cursor.AfterCreatedAt.IsZero() != (cursor.AfterID == uuid.Nil)) {
		return FilePage{}, ErrInvalid
	}
	return s.store.ListFiles(ctx, filter, cursor)
}

func (s *FileCenterService) Detail(ctx context.Context, actor Principal, fileID uuid.UUID) (FileDetail, error) {
	if s == nil || s.store == nil || validatePrincipal(actor) != nil {
		return FileDetail{}, ErrForbidden
	}
	if fileID == uuid.Nil {
		return FileDetail{}, ErrInvalid
	}
	return s.store.FileDetail(ctx, fileID)
}

func (s *FileCenterService) Retry(ctx context.Context, actor Principal, versionID uuid.UUID) error {
	if s == nil || s.store == nil || validatePrincipal(actor) != nil {
		return ErrForbidden
	}
	if versionID == uuid.Nil {
		return ErrInvalid
	}
	version, err := s.store.FileVersion(ctx, versionID)
	if err != nil {
		return err
	}
	if version.ProcessingState != "failed" || !retryableFailure(version.FailureCategory) {
		return ErrFileNotRetryable
	}
	return s.store.RetryFile(ctx, actor, versionID)
}

func (s *FileCenterService) Replace(ctx context.Context, actor Principal, fileID, uploadedVersionID uuid.UUID) error {
	if s == nil || s.store == nil || validatePrincipal(actor) != nil {
		return ErrForbidden
	}
	if fileID == uuid.Nil || uploadedVersionID == uuid.Nil {
		return ErrInvalid
	}
	return s.store.ReplaceFile(ctx, actor, fileID, uploadedVersionID, s.now().UTC().Add(FileRetentionPeriod))
}

func (s *FileCenterService) RollbackDraftBinding(ctx context.Context, actor Principal, fileID, lessonID, versionID uuid.UUID) error {
	if s == nil || s.store == nil || validatePrincipal(actor) != nil {
		return ErrForbidden
	}
	if fileID == uuid.Nil || lessonID == uuid.Nil || versionID == uuid.Nil {
		return ErrInvalid
	}
	detail, err := s.store.FileDetail(ctx, fileID)
	if err != nil {
		return err
	}
	found := false
	for _, version := range detail.Versions {
		if version.ID == versionID {
			found = true
			if version.RetentionUntil != nil && !version.RetentionUntil.After(s.now()) {
				return ErrFileVersionExpired
			}
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	return s.store.RollbackFile(ctx, actor, fileID, lessonID, versionID)
}

func (s *FileCenterService) RequestDelete(ctx context.Context, actor Principal, fileID uuid.UUID) error {
	if s == nil || s.store == nil || validatePrincipal(actor) != nil {
		return ErrForbidden
	}
	if fileID == uuid.Nil {
		return ErrInvalid
	}
	detail, err := s.store.FileDetail(ctx, fileID)
	if err != nil {
		return err
	}
	if len(detail.References) != 0 {
		return ErrFileInUse
	}
	return s.store.DeleteFile(ctx, actor, fileID, s.now().UTC().Add(FileRetentionPeriod))
}

func validFileFilter(filter FileFilter) bool {
	if !utf8.ValidString(filter.Name) || utf8.RuneCountInString(filter.Name) > 100 {
		return false
	}
	if !oneOf(filter.Type, "", "document", "image", "office", "video", "text") || !oneOf(filter.State, "", "pending_scan", "processing", "ready", "rejected", "failed") || !oneOf(filter.Reference, "", "referenced", "unreferenced", "draft", "published") {
		return false
	}
	return filter.CreatedFrom == nil || filter.CreatedTo == nil || !filter.CreatedFrom.After(*filter.CreatedTo)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func retryableFailure(category string) bool {
	switch category {
	case "scanner_unavailable", "scanner_definitions_stale", "parser_unavailable", "conversion_failed", "probe_failed", "storage_unavailable", "database_unavailable", "workspace_unavailable", "preview_unavailable", "lease_expired":
		return true
	default:
		return false
	}
}
