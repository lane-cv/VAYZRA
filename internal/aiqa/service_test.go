package aiqa

import (
	"context"
	"errors"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

func TestStudentServiceRejectsNonStudentAndInvalidSubject(t *testing.T) {
	svc := NewStudentService(&fakeRuntimeStore{}, &fakeRuntimeConfig{}, fakeAttachmentContextStore{}, func() time.Time { return time.Now() })
	admin := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}
	if _, _, err := svc.CreateThread(context.Background(), admin, CreateThreadInput{Title: "x", Body: "question", Subject: SubjectMath, IdempotencyKey: "1234567890abcdef"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("admin should be hidden as not found, got %v", err)
	}
	student := studentPrincipal()
	if _, _, err := svc.CreateThread(context.Background(), student, CreateThreadInput{Title: "x", Body: "question", Subject: "chemistry", IdempotencyKey: "1234567890abcdef"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid subject: %v", err)
	}
}

func TestStudentServiceSelectsVisionAndRejectsContextTooLarge(t *testing.T) {
	store := &fakeRuntimeStore{}
	cfg := fakeRuntimeConfig{contextTokens: 20}
	atts := fakeAttachmentContextStore{metadata: []AttachmentMetadata{{FileVersionID: uuid.New(), Modality: ModalityVision}}}
	svc := NewStudentService(store, &cfg, atts, time.Now)
	_, _, err := svc.CreateThread(context.Background(), studentPrincipal(), CreateThreadInput{
		Title: "x", Body: "current is deliberately too large", Subject: SubjectMath,
		IdempotencyKey: "1234567890abcdef", Attachments: []AttachmentInput{{FileVersionID: atts.metadata[0].FileVersionID}},
	})
	if !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("expected context too large, got %v", err)
	}
	if cfg.lastModality != ModalityVision {
		t.Fatalf("expected vision routing, got %q", cfg.lastModality)
	}
}

func TestStudentServiceKeepsCompletePairsNewestFirst(t *testing.T) {
	student := studentPrincipal()
	threadID := uuid.New()
	store := &fakeRuntimeStore{context: []Message{
		{Role: "student", Body: "old-u"}, {Role: "assistant", Body: "old-a"},
		{Role: "student", Body: "new-u"}, {Role: "assistant", Body: "new-a"},
	}}
	cfg := fakeRuntimeConfig{contextTokens: 33}
	svc := NewStudentService(store, &cfg, fakeAttachmentContextStore{}, time.Now)
	_, _, err := svc.AddMessage(context.Background(), student, AddMessageInput{
		ThreadID: threadID, Body: "current", IdempotencyKey: "1234567890abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.admission.SelectedTurns) != 2 || store.admission.SelectedTurns[0].Text != "new-u" || store.admission.SelectedTurns[1].Text != "new-a" {
		t.Fatalf("expected newest complete pair only, got %#v", store.admission.SelectedTurns)
	}
}

func TestStudentServiceIdempotencyReturnsBeforeConfigOrAttachments(t *testing.T) {
	student := studentPrincipal()
	firstFile, secondFile := uuid.New(), uuid.New()
	existing := Run{
		ID: uuid.New(), ThreadID: uuid.New(), TriggerBody: "same", Status: RunQueued,
		TriggerAttachments: []AttachmentMetadata{{FileVersionID: firstFile}, {FileVersionID: secondFile}},
	}
	store := &fakeRuntimeStore{existingRun: &existing, thread: Thread{ID: existing.ThreadID, Title: "same", Subject: SubjectMath}}
	svc := NewStudentService(store, failingRuntimeConfig{}, failingAttachmentStore{}, time.Now)
	_, got, err := svc.CreateThread(context.Background(), student, CreateThreadInput{
		Title: "same", Body: "same", Subject: SubjectMath, IdempotencyKey: "existing-run-key-0001",
		Attachments: []AttachmentInput{{FileVersionID: secondFile, SortPosition: 1}, {FileVersionID: firstFile, SortPosition: 0}},
	})
	if err != nil || got.ID != existing.ID {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, _, err = svc.CreateThread(context.Background(), student, CreateThreadInput{
		Title: "same", Body: "different", Subject: SubjectMath, IdempotencyKey: "existing-run-key-0001",
		Attachments: []AttachmentInput{{FileVersionID: firstFile, SortPosition: 0}, {FileVersionID: secondFile, SortPosition: 1}},
	}); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("same key with different payload=%v", err)
	}
	if _, _, err = svc.CreateThread(context.Background(), student, CreateThreadInput{
		Title: "same", Body: "same", Subject: SubjectMath, IdempotencyKey: "existing-run-key-0001",
		Attachments: []AttachmentInput{{FileVersionID: firstFile, SortPosition: 1}, {FileVersionID: secondFile, SortPosition: 0}},
	}); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("same key with changed attachment order=%v", err)
	}
	if _, _, err = svc.CreateThread(context.Background(), student, CreateThreadInput{
		Title: "same", Body: "same", Subject: SubjectMath, IdempotencyKey: "existing-run-key-0001",
		Attachments: []AttachmentInput{{FileVersionID: firstFile, SortPosition: 0}, {FileVersionID: uuid.New(), SortPosition: 1}},
	}); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("same key with changed attachment identity=%v", err)
	}
}

func TestStudentServiceRetryEligibilityAndAttemptDelegation(t *testing.T) {
	student := studentPrincipal()
	source := Run{ID: uuid.New(), ThreadID: uuid.New(), TriggerMessageID: uuid.New(), Status: RunFailed, Modality: ModalityText, ReservedTokenCount: 500}
	store := &fakeRuntimeStore{sourceRun: source, thread: Thread{ID: source.ThreadID, StudentID: student.User.ID, Subject: SubjectMath}}
	cfg := &fakeRuntimeConfig{}
	svc := NewStudentService(store, cfg, fakeAttachmentContextStore{}, time.Now)
	got, err := svc.RetryRun(context.Background(), student, source.ID, "retry-service-key-0001")
	if err != nil || got.ID == uuid.Nil || store.retry.SourceRunID != source.ID || store.retry.Reservation.TokenCount < source.ReservedTokenCount {
		t.Fatalf("got=%+v retry=%+v err=%v", got, store.retry, err)
	}
	source.Status = RunSucceeded
	store.sourceRun = source
	if _, err = svc.RetryRun(context.Background(), student, source.ID, "retry-service-key-0002"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("succeeded retry=%v", err)
	}
}

type fakeRuntimeStore struct {
	context     []Message
	admission   RuntimeAdmission
	existingRun *Run
	sourceRun   Run
	thread      Thread
	retry       RuntimeRetryAdmission
}

func (f *fakeRuntimeStore) AdmitRun(_ context.Context, a RuntimeAdmission) (ThreadDetail, Run, error) {
	f.admission = a
	now := time.Now()
	run := Run{ID: uuid.New(), ThreadID: a.ThreadID, TriggerMessageID: a.MessageID, Status: RunQueued, AttemptNo: a.AttemptNo, CreatedAt: now, UpdatedAt: now}
	return ThreadDetail{Thread: Thread{ID: a.ThreadID, StudentID: a.StudentID}}, run, nil
}
func (f *fakeRuntimeStore) GetRunByIdempotency(context.Context, uuid.UUID, string) (ThreadDetail, Run, error) {
	if f.existingRun != nil {
		return ThreadDetail{Thread: f.thread}, *f.existingRun, nil
	}
	return ThreadDetail{}, Run{}, ErrNotFound
}
func (f *fakeRuntimeStore) ListThreads(context.Context, uuid.UUID, ThreadCursor) ([]Thread, ThreadCursor, error) {
	return nil, ThreadCursor{}, nil
}
func (f *fakeRuntimeStore) GetThread(context.Context, uuid.UUID, uuid.UUID, MessageCursor) (ThreadDetail, error) {
	return ThreadDetail{Thread: f.thread}, nil
}
func (f *fakeRuntimeStore) LoadContext(context.Context, uuid.UUID, uuid.UUID) ([]Message, error) {
	return append([]Message(nil), f.context...), nil
}
func (f *fakeRuntimeStore) GetRun(context.Context, uuid.UUID, uuid.UUID) (Run, error) {
	if f.sourceRun.ID != uuid.Nil {
		return f.sourceRun, nil
	}
	return Run{}, ErrNotFound
}
func (f *fakeRuntimeStore) CancelRun(context.Context, uuid.UUID, uuid.UUID, time.Time) (Run, error) {
	return Run{}, nil
}
func (f *fakeRuntimeStore) RetryRun(_ context.Context, retry RuntimeRetryAdmission) (ThreadDetail, Run, error) {
	f.retry = retry
	return ThreadDetail{Thread: f.thread}, Run{ID: retry.RunID, ThreadID: f.thread.ID, Status: RunQueued, AttemptNo: f.sourceRun.AttemptNo + 1}, nil
}

type fakeRuntimeConfig struct {
	lastModality  Modality
	contextTokens int64
}

func (f *fakeRuntimeConfig) ForRun(_ context.Context, subject Subject, modality Modality) (RuntimeProviderConfig, error) {
	f.lastModality = modality
	u, _ := url.Parse("https://ai.example.test/v1")
	n := f.contextTokens
	if n == 0 {
		n = 1000
	}
	return RuntimeProviderConfig{
		ProviderID: uuid.New(), BaseURL: u, ProtocolMode: ProtocolResponses,
		Model:  ModelView{ID: uuid.New(), ProviderID: uuid.New(), UpstreamModelID: "model", Modality: modality, ContextTokens: n, MaxOutputTokens: 10, ImageQuotaTokens: 50, Enabled: true},
		Prompt: PromptView{ID: uuid.New(), Subject: subject, Version: 1, Body: "sys", Active: true},
	}, nil
}

type fakeAttachmentContextStore struct{ metadata []AttachmentMetadata }

func (f fakeAttachmentContextStore) ValidateForAI(context.Context, uuid.UUID, uuid.UUID, []AttachmentInput) ([]AttachmentMetadata, error) {
	return f.metadata, nil
}

type failingRuntimeConfig struct{}

func (failingRuntimeConfig) ForRun(context.Context, Subject, Modality) (RuntimeProviderConfig, error) {
	return RuntimeProviderConfig{}, errors.New("must not load config")
}

type failingAttachmentStore struct{}

func (failingAttachmentStore) ValidateForAI(context.Context, uuid.UUID, uuid.UUID, []AttachmentInput) ([]AttachmentMetadata, error) {
	return nil, errors.New("must not validate attachments")
}
func (failingAttachmentStore) LoadAIText(context.Context, uuid.UUID, uuid.UUID) (string, error) {
	return "", errors.New("must not load text")
}
func (failingAttachmentStore) OpenAIImage(context.Context, uuid.UUID, uuid.UUID) (io.ReadCloser, string, int64, error) {
	return nil, "", 0, errors.New("must not open image")
}
func (fakeAttachmentContextStore) LoadAIText(context.Context, uuid.UUID, uuid.UUID) (string, error) {
	return "", nil
}
func (fakeAttachmentContextStore) OpenAIImage(context.Context, uuid.UUID, uuid.UUID) (io.ReadCloser, string, int64, error) {
	return nil, "", 0, nil
}

func studentPrincipal() Principal {
	return Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}}
}
