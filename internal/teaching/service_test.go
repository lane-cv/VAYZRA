package teaching

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

func TestSaveDraftRejectsStaleLockVersionWithoutOverwrite(t *testing.T) {
	lessonID := uuid.New()
	store := &fakeCatalogStore{draft: Draft{LessonID: lessonID, LockVersion: 7, Title: "current"}}
	svc := NewService(store, allowPublication{}, fixedTeachingClock)
	_, err := svc.SaveDraft(context.Background(), adminTeachingPrincipal(), SaveDraftInput{LessonID: lessonID, ExpectedVersion: 6, Title: "stale", Audience: Audience{Mode: AudienceAll}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
	if store.draft.Title != "current" {
		t.Fatalf("draft was overwritten: %#v", store.draft)
	}
}

func TestPublishFreezesDraftAudienceAndWritesAuditAndOutbox(t *testing.T) {
	lessonID, studentID := uuid.New(), uuid.New()
	store := &fakeCatalogStore{draft: Draft{
		LessonID: lessonID, Title: "Lesson", BodyMarkdown: "Body", LockVersion: 3,
		Audience:       Audience{Mode: AudienceSelected, UserIDs: []uuid.UUID{studentID}},
		ExternalVideos: []ExternalVideo{{ID: uuid.New(), URL: "https://video.example.test/watch", Title: "Video"}},
	}}
	audits := &fakeTeachingAudit{}
	svc := NewService(&fakeTeachingUOW{fakeCatalogStore: store, audit: audits}, allowPublication{}, fixedTeachingClock)
	revision, err := svc.Publish(context.Background(), adminTeachingPrincipal(), PublishInput{LessonID: lessonID, ExpectedVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	if revision.LessonID != lessonID || revision.Audience.Mode != AudienceSelected || len(revision.Audience.UserIDs) != 1 || revision.Audience.UserIDs[0] != studentID {
		t.Fatalf("revision did not freeze draft audience: %#v", revision)
	}
	if !store.published || !store.finalized || store.outboxKind != "lesson.published" {
		t.Fatalf("publication transaction=%#v", store)
	}
	if audits.last.Action != "lesson.published" || audits.last.TargetID != lessonID.String() {
		t.Fatalf("audit=%#v", audits.last)
	}
}

func fixedTeachingClock() time.Time { return time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC) }
func adminTeachingPrincipal() Principal {
	return Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "request-123", IP: net.ParseIP("192.0.2.4")}
}

type fakeCatalogStore struct {
	draft      Draft
	published  bool
	finalized  bool
	outboxKind string
}

func (s *fakeCatalogStore) CreateCatalog(context.Context, CatalogCreateInput) (CatalogNode, error) {
	return CatalogNode{}, nil
}
func (s *fakeCatalogStore) RenameCatalog(context.Context, CatalogRenameInput) (CatalogNode, error) {
	return CatalogNode{}, nil
}
func (s *fakeCatalogStore) ReorderCatalog(context.Context, CatalogReorderInput) error { return nil }
func (s *fakeCatalogStore) ArchiveCatalog(context.Context, CatalogArchiveInput) error { return nil }
func (s *fakeCatalogStore) CreateLesson(context.Context, CreateLessonInput) (Draft, error) {
	return Draft{}, nil
}
func (s *fakeCatalogStore) GetDraft(_ context.Context, lessonID uuid.UUID) (Draft, error) {
	if s.draft.LessonID != lessonID {
		return Draft{}, ErrNotFound
	}
	return s.draft, nil
}
func (s *fakeCatalogStore) SaveDraft(_ context.Context, in SaveDraftInput) (Draft, error) {
	if s.draft.LessonID != in.LessonID {
		return Draft{}, ErrNotFound
	}
	if s.draft.LockVersion != in.ExpectedVersion {
		return Draft{}, ErrConflict
	}
	s.draft.Title, s.draft.LockVersion = in.Title, s.draft.LockVersion+1
	return s.draft, nil
}
func (s *fakeCatalogStore) Publish(_ context.Context, in PublishInput) (Revision, error) {
	if in.ExpectedVersion != s.draft.LockVersion {
		return Revision{}, ErrConflict
	}
	s.published, s.finalized, s.outboxKind = true, true, "lesson.published"
	return Revision{ID: uuid.New(), LessonID: in.LessonID, Version: 1, Title: s.draft.Title, BodyMarkdown: s.draft.BodyMarkdown, Audience: s.draft.Audience, ExternalVideos: s.draft.ExternalVideos, PublishedBy: in.ActorID, PublishedAt: fixedTeachingClock()}, nil
}
func (s *fakeCatalogStore) Withdraw(context.Context, WithdrawInput) error { return nil }
func (s *fakeCatalogStore) Browse(context.Context, BrowseInput) ([]CatalogNode, error) {
	return nil, nil
}
func (s *fakeCatalogStore) Search(context.Context, SearchInput) ([]Revision, SearchCursor, error) {
	return nil, SearchCursor{}, nil
}
func (s *fakeCatalogStore) UpdateProgress(context.Context, uuid.UUID, ProgressInput) error {
	return nil
}

type fakeTeachingAudit struct{ last audit.Event }

func (a *fakeTeachingAudit) Write(_ context.Context, event audit.Event) error {
	a.last = event
	return nil
}

type fakeTeachingUOW struct {
	*fakeCatalogStore
	audit audit.Writer
}

func (u *fakeTeachingUOW) WithinTx(ctx context.Context, fn func(TxStore, audit.Writer) error) error {
	return fn(u.fakeCatalogStore, u.audit)
}

func (s *fakeCatalogStore) ArchiveLesson(context.Context, uuid.UUID) error { return nil }
