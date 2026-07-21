package files

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

func TestFileCenterListNormalizesAndBoundsFilters(t *testing.T) {
	store := &fileCenterStoreStub{}
	service := NewFileCenterService(store, time.Now)
	actor := fileCenterActor()
	_, err := service.List(context.Background(), actor, FileFilter{Name: "  Newton  ", State: "failed", Reference: "unreferenced"}, Cursor{Limit: 101})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
	_, err = service.List(context.Background(), actor, FileFilter{Name: "  Newton  ", State: "failed", Reference: "unreferenced"}, Cursor{Limit: 25})
	if err != nil || store.filter.Name != "newton" || store.cursor.Limit != 25 {
		t.Fatalf("filter=%+v cursor=%+v err=%v", store.filter, store.cursor, err)
	}
}

func TestFileCenterRetryAllowsOnlyTransientFailures(t *testing.T) {
	versionID := uuid.New()
	store := &fileCenterStoreStub{version: FileVersionDetail{ID: versionID, ProcessingState: "failed", FailureCategory: "conversion_failed"}}
	service := NewFileCenterService(store, time.Now)
	if err := service.Retry(context.Background(), fileCenterActor(), versionID); err != nil || store.retried != versionID {
		t.Fatalf("retried=%s err=%v", store.retried, err)
	}
	store.version = FileVersionDetail{ID: versionID, ProcessingState: "rejected", FailureCategory: "malware"}
	if err := service.Retry(context.Background(), fileCenterActor(), versionID); !errors.Is(err, ErrFileNotRetryable) {
		t.Fatalf("err=%v", err)
	}
}

func TestFileCenterRollbackHonorsRetentionAndDeleteReferences(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	fileID, versionID, lessonID := uuid.New(), uuid.New(), uuid.New()
	store := &fileCenterStoreStub{
		detail: FileDetail{ID: fileID, Versions: []FileVersionDetail{{ID: versionID, RetentionUntil: ptrTime(now.Add(time.Hour))}}},
	}
	service := NewFileCenterService(store, func() time.Time { return now })
	if err := service.RollbackDraftBinding(context.Background(), fileCenterActor(), fileID, lessonID, versionID); err != nil || store.rolledBack != versionID {
		t.Fatalf("rolledBack=%s err=%v", store.rolledBack, err)
	}
	store.detail.Versions[0].RetentionUntil = ptrTime(now.Add(-time.Second))
	if err := service.RollbackDraftBinding(context.Background(), fileCenterActor(), fileID, lessonID, versionID); !errors.Is(err, ErrFileVersionExpired) {
		t.Fatalf("err=%v", err)
	}
	store.detail.References = []FileReference{{Kind: "published", LessonID: lessonID, LessonTitle: "Motion"}}
	if err := service.RequestDelete(context.Background(), fileCenterActor(), fileID); !errors.Is(err, ErrFileInUse) {
		t.Fatalf("err=%v", err)
	}
}

type fileCenterStoreStub struct {
	filter     FileFilter
	cursor     Cursor
	detail     FileDetail
	version    FileVersionDetail
	retried    uuid.UUID
	rolledBack uuid.UUID
}

func (s *fileCenterStoreStub) ListFiles(_ context.Context, filter FileFilter, cursor Cursor) (FilePage, error) {
	s.filter, s.cursor = filter, cursor
	return FilePage{}, nil
}
func (s *fileCenterStoreStub) FileDetail(_ context.Context, id uuid.UUID) (FileDetail, error) {
	if s.detail.ID == uuid.Nil {
		s.detail.ID = id
	}
	return s.detail, nil
}
func (s *fileCenterStoreStub) FileVersion(context.Context, uuid.UUID) (FileVersionDetail, error) {
	return s.version, nil
}
func (s *fileCenterStoreStub) RetryFile(_ context.Context, _ Principal, id uuid.UUID) error {
	s.retried = id
	return nil
}
func (s *fileCenterStoreStub) ReplaceFile(context.Context, Principal, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}
func (s *fileCenterStoreStub) RollbackFile(_ context.Context, _ Principal, _, _ uuid.UUID, versionID uuid.UUID) error {
	s.rolledBack = versionID
	return nil
}
func (s *fileCenterStoreStub) DeleteFile(context.Context, Principal, uuid.UUID, time.Time) error {
	return nil
}

func fileCenterActor() Principal {
	return Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "request-1", IP: net.ParseIP("127.0.0.1")}
}
func ptrTime(value time.Time) *time.Time { return &value }
