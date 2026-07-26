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

func TestStudentServiceAttachmentNotReadyAndTextRouting(t *testing.T) {
	student := studentPrincipal()
	cfg := &fakeRuntimeConfig{}
	notReady := NewStudentService(&fakeRuntimeStore{}, cfg, fakeAttachmentContextStore{err: ErrAttachmentNotReady}, time.Now)
	if _, _, err := notReady.CreateThread(context.Background(), student, CreateThreadInput{
		Title: "x", Body: "question", Subject: SubjectMath, IdempotencyKey: "attachment-ready-0001",
	}); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("attachment readiness=%v", err)
	}
	textID := uuid.New()
	store := &fakeRuntimeStore{}
	textService := NewStudentService(store, cfg, fakeAttachmentContextStore{
		metadata: []AttachmentMetadata{{FileVersionID: textID, Modality: ModalityText}},
		text:     map[uuid.UUID]string{textID: "extracted"},
	}, time.Now)
	if _, _, err := textService.CreateThread(context.Background(), student, CreateThreadInput{
		Title: "x", Body: "question", Subject: SubjectMath, IdempotencyKey: "text-routing-key-0001",
		Attachments: []AttachmentInput{{FileVersionID: textID}},
	}); err != nil {
		t.Fatal(err)
	}
	if cfg.lastModality != ModalityText || store.admission.ExtractedText != "extracted" {
		t.Fatalf("modality=%s extracted=%q", cfg.lastModality, store.admission.ExtractedText)
	}
}

func TestStudentServiceIdempotentFollowupCancelAndBusy(t *testing.T) {
	student := studentPrincipal()
	threadID := uuid.New()
	existing := Run{ID: uuid.New(), ThreadID: threadID, TriggerBody: "followup", Status: RunQueued}
	store := &fakeRuntimeStore{existingRun: &existing, thread: Thread{ID: threadID, StudentID: student.User.ID, Subject: SubjectMath}}
	svc := NewStudentService(store, &fakeRuntimeConfig{}, fakeAttachmentContextStore{}, time.Now)
	if _, got, err := svc.AddMessage(context.Background(), student, AddMessageInput{
		ThreadID: threadID, Body: "followup", IdempotencyKey: "followup-idem-key-01",
	}); err != nil || got.ID != existing.ID {
		t.Fatalf("followup=%+v err=%v", got, err)
	}
	store.existingRun = nil
	store.cancelRun = Run{ID: existing.ID, ThreadID: threadID, Status: RunCancelled}
	if got, err := svc.CancelRun(context.Background(), student, existing.ID); err != nil || got.Status != RunCancelled ||
		store.cancelStudent != student.User.ID || store.cancelID != existing.ID {
		t.Fatalf("cancel=%+v err=%v", got, err)
	}
	store.admitErr = ErrAIBusy
	if _, _, err := svc.AddMessage(context.Background(), student, AddMessageInput{
		ThreadID: threadID, Body: "busy", IdempotencyKey: "active-busy-key-0001",
	}); !errors.Is(err, ErrAIBusy) {
		t.Fatalf("busy=%v", err)
	}
}

func TestStudentServiceRejectsStreamingRetry(t *testing.T) {
	student := studentPrincipal()
	source := Run{ID: uuid.New(), ThreadID: uuid.New(), TriggerMessageID: uuid.New(), Status: RunStreaming}
	store := &fakeRuntimeStore{sourceRun: source}
	svc := NewStudentService(store, &fakeRuntimeConfig{}, fakeAttachmentContextStore{}, time.Now)
	if _, err := svc.RetryRun(context.Background(), student, source.ID, "streaming-retry-key1"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("streaming retry=%v", err)
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
	imageID, textID := uuid.New(), uuid.New()
	source := Run{
		ID: uuid.New(), ThreadID: uuid.New(), TriggerMessageID: uuid.New(), Status: RunFailed, Modality: ModalityVision, ReservedTokenCount: 50,
		TriggerAttachments: []AttachmentMetadata{{FileVersionID: imageID, Modality: ModalityVision}, {FileVersionID: textID, Modality: ModalityText}},
	}
	store := &fakeRuntimeStore{
		sourceRun: source, thread: Thread{ID: source.ThreadID, StudentID: student.User.ID, Subject: SubjectMath},
		context: []Message{
			{ID: uuid.New(), Role: "student", Body: "prior-u"}, {ID: uuid.New(), Role: "assistant", Body: "prior-a"},
			{ID: source.TriggerMessageID, Role: "student", Body: "retry-current"},
		},
	}
	cfg := &fakeRuntimeConfig{contextTokens: 5000, imageQuota: 900}
	var validated []AttachmentInput
	svc := NewStudentService(store, cfg, fakeAttachmentContextStore{
		metadata: source.TriggerAttachments, text: map[uuid.UUID]string{textID: "doc-text"}, validated: &validated,
	}, time.Now)
	got, err := svc.RetryRun(context.Background(), student, source.ID, "retry-service-key-0001")
	wantTokens := int64(len([]byte("sysprior-uprior-aretry-currentdoc-text")) + 900 + 10)
	if err != nil || got.ID == uuid.Nil || store.retry.SourceRunID != source.ID || store.retry.Reservation.TokenCount != wantTokens || cfg.lastModality != ModalityVision {
		t.Fatalf("got=%+v retry=%+v err=%v", got, store.retry, err)
	}
	if len(validated) != 2 || validated[0].FileVersionID != imageID || validated[0].SortPosition != 0 ||
		validated[1].FileVersionID != textID || validated[1].SortPosition != 1 {
		t.Fatalf("ordered retry bindings=%+v", validated)
	}
	source.Status = RunSucceeded
	store.sourceRun = source
	if _, err = svc.RetryRun(context.Background(), student, source.ID, "retry-service-key-0002"); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("succeeded retry=%v", err)
	}
}

func TestStudentServiceRetryRevalidatesVisionBeforeMutation(t *testing.T) {
	student := studentPrincipal()
	triggerID, imageID := uuid.New(), uuid.New()
	source := Run{
		ID: uuid.New(), ThreadID: uuid.New(), TriggerMessageID: triggerID, Status: RunFailed,
		TriggerAttachments: []AttachmentMetadata{{FileVersionID: imageID, Modality: ModalityVision}},
	}
	store := &fakeRuntimeStore{
		sourceRun: source, thread: Thread{ID: source.ThreadID, StudentID: student.User.ID, Subject: SubjectMath},
		context: []Message{{ID: triggerID, Role: "student", Body: "retry"}},
	}
	svc := NewStudentService(store, &fakeRuntimeConfig{}, fakeAttachmentContextStore{err: ErrAttachmentNotReady}, time.Now)
	if _, err := svc.RetryRun(context.Background(), student, source.ID, "retry-unready-image1"); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("unready retry=%v", err)
	}
	if store.retry.RunID != uuid.Nil {
		t.Fatalf("retry mutated store before validation: %+v", store.retry)
	}

	existing := Run{ID: uuid.New(), ThreadID: source.ThreadID, TriggerMessageID: triggerID, Status: RunQueued}
	store.existingRun = &existing
	if got, err := svc.RetryRun(context.Background(), student, source.ID, "retry-idempotent-img"); err != nil || got.ID != existing.ID {
		t.Fatalf("idempotent replay must precede validation: got=%+v err=%v", got, err)
	}
}

type fakeRuntimeStore struct {
	context       []Message
	admission     RuntimeAdmission
	existingRun   *Run
	sourceRun     Run
	thread        Thread
	retry         RuntimeRetryAdmission
	admitErr      error
	cancelStudent uuid.UUID
	cancelID      uuid.UUID
	cancelRun     Run
}

func (f *fakeRuntimeStore) AdmitRun(_ context.Context, a RuntimeAdmission) (ThreadDetail, Run, error) {
	f.admission = a
	if f.admitErr != nil {
		return ThreadDetail{}, Run{}, f.admitErr
	}
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
func (f *fakeRuntimeStore) CancelRun(_ context.Context, studentID, runID uuid.UUID, _ time.Time) (Run, error) {
	f.cancelStudent, f.cancelID = studentID, runID
	return f.cancelRun, nil
}
func (f *fakeRuntimeStore) RetryRun(_ context.Context, retry RuntimeRetryAdmission) (ThreadDetail, Run, error) {
	f.retry = retry
	return ThreadDetail{Thread: f.thread}, Run{ID: retry.RunID, ThreadID: f.thread.ID, Status: RunQueued, AttemptNo: f.sourceRun.AttemptNo + 1}, nil
}

type fakeRuntimeConfig struct {
	lastModality  Modality
	contextTokens int64
	imageQuota    int64
}

func (f *fakeRuntimeConfig) ForRun(_ context.Context, subject Subject, modality Modality) (RuntimeProviderConfig, error) {
	f.lastModality = modality
	u, _ := url.Parse("https://ai.example.test/v1")
	n := f.contextTokens
	if n == 0 {
		n = 1000
	}
	imageQuota := f.imageQuota
	if imageQuota == 0 {
		imageQuota = 50
	}
	return RuntimeProviderConfig{
		ProviderID: uuid.New(), BaseURL: u, ProtocolMode: ProtocolResponses,
		Model:  ModelView{ID: uuid.New(), ProviderID: uuid.New(), UpstreamModelID: "model", Modality: modality, ContextTokens: n, MaxOutputTokens: 10, ImageQuotaTokens: imageQuota, Enabled: true},
		Prompt: PromptView{ID: uuid.New(), Subject: subject, Version: 1, Body: "sys", Active: true},
	}, nil
}

type fakeAttachmentContextStore struct {
	metadata  []AttachmentMetadata
	text      map[uuid.UUID]string
	err       error
	validated *[]AttachmentInput
}

func (f fakeAttachmentContextStore) ValidateForAI(_ context.Context, _, _ uuid.UUID, inputs []AttachmentInput) ([]AttachmentMetadata, error) {
	if f.validated != nil {
		*f.validated = append([]AttachmentInput(nil), inputs...)
	}
	if f.err != nil {
		return nil, f.err
	}
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
func (f fakeAttachmentContextStore) LoadAIText(_ context.Context, _ uuid.UUID, id uuid.UUID) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.text[id], nil
}
func (fakeAttachmentContextStore) OpenAIImage(context.Context, uuid.UUID, uuid.UUID) (io.ReadCloser, string, int64, error) {
	return nil, "", 0, nil
}

func studentPrincipal() Principal {
	return Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}}
}
