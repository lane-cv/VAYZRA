package aiqa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type StudentService interface {
	CreateThread(context.Context, Principal, CreateThreadInput) (ThreadDetail, Run, error)
	ListThreads(context.Context, Principal, ThreadCursor) ([]Thread, ThreadCursor, error)
	GetThread(context.Context, Principal, uuid.UUID, MessageCursor) (ThreadDetail, error)
	AddMessage(context.Context, Principal, AddMessageInput) (ThreadDetail, Run, error)
	CancelRun(context.Context, Principal, uuid.UUID) (Run, error)
	RetryRun(context.Context, Principal, uuid.UUID, string) (Run, error)
}

type studentService struct {
	store       RuntimeStore
	config      RuntimeConfigSource
	attachments AttachmentContextStore
	now         func() time.Time
}

func NewStudentService(store RuntimeStore, config RuntimeConfigSource, attachments AttachmentContextStore, now func() time.Time) StudentService {
	if now == nil {
		now = time.Now
	}
	return &studentService{store: store, config: config, attachments: attachments, now: now}
}

func (s *studentService) CreateThread(ctx context.Context, principal Principal, input CreateThreadInput) (ThreadDetail, Run, error) {
	if !studentOK(principal) {
		return ThreadDetail{}, Run{}, ErrNotFound
	}
	if !subjectOK(input.Subject) || !validTitle(input.Title) || !validBody(input.Body) || !validIdempotency(input.IdempotencyKey) || validateAttachmentInputs(input.Attachments) != nil {
		return ThreadDetail{}, Run{}, ErrInvalidInput
	}
	if detail, run, err := s.store.GetRunByIdempotency(ctx, principal.User.ID, input.IdempotencyKey); err == nil {
		if detail.Thread.Title != strings.TrimSpace(input.Title) || detail.Thread.Subject != input.Subject || run.TriggerBody != strings.TrimSpace(input.Body) ||
			!sameAttachmentBindings(input.Attachments, run.TriggerAttachments) {
			return ThreadDetail{}, Run{}, ErrRunConflict
		}
		return detail, run, nil
	} else if !errors.Is(err, ErrNotFound) {
		return ThreadDetail{}, Run{}, err
	}
	return s.admit(ctx, principal.User.ID, uuid.New(), true, input.Title, input.Subject, input.Body, input.IdempotencyKey, input.Attachments)
}

func (s *studentService) AddMessage(ctx context.Context, principal Principal, input AddMessageInput) (ThreadDetail, Run, error) {
	if !studentOK(principal) {
		return ThreadDetail{}, Run{}, ErrNotFound
	}
	if input.ThreadID == uuid.Nil || !validBody(input.Body) || !validIdempotency(input.IdempotencyKey) || validateAttachmentInputs(input.Attachments) != nil {
		return ThreadDetail{}, Run{}, ErrInvalidInput
	}
	if detail, run, err := s.store.GetRunByIdempotency(ctx, principal.User.ID, input.IdempotencyKey); err == nil {
		if run.ThreadID != input.ThreadID || run.TriggerBody != strings.TrimSpace(input.Body) ||
			!sameAttachmentBindings(input.Attachments, run.TriggerAttachments) {
			return ThreadDetail{}, Run{}, ErrRunConflict
		}
		return detail, run, nil
	} else if !errors.Is(err, ErrNotFound) {
		return ThreadDetail{}, Run{}, err
	}
	detail, err := s.store.GetThread(ctx, principal.User.ID, input.ThreadID, MessageCursor{Limit: 1})
	if err != nil {
		return ThreadDetail{}, Run{}, err
	}
	return s.admit(ctx, principal.User.ID, input.ThreadID, false, "", detail.Thread.Subject, input.Body, input.IdempotencyKey, input.Attachments)
}

func (s *studentService) admit(ctx context.Context, studentID, threadID uuid.UUID, create bool, title string, subject Subject, body, key string, attachmentInputs []AttachmentInput) (ThreadDetail, Run, error) {
	metadata, err := s.attachments.ValidateForAI(ctx, studentID, func() uuid.UUID {
		if create {
			return uuid.Nil
		}
		return threadID
	}(), attachmentInputs)
	if err != nil {
		return ThreadDetail{}, Run{}, err
	}
	modality := ModalityText
	imageCount := 0
	var extracted strings.Builder
	for _, attachment := range metadata {
		if attachment.Modality == ModalityVision {
			modality = ModalityVision
			imageCount++
			continue
		}
		text, loadErr := s.attachments.LoadAIText(ctx, studentID, attachment.FileVersionID)
		if loadErr != nil {
			return ThreadDetail{}, Run{}, loadErr
		}
		extracted.WriteString(text)
	}
	cfg, err := s.config.ForRun(ctx, subject, modality)
	if err != nil {
		return ThreadDetail{}, Run{}, err
	}
	defer zeroBytes(cfg.APIKey)

	var history []Message
	if !create {
		history, err = s.store.LoadContext(ctx, studentID, threadID)
		if err != nil {
			return ThreadDetail{}, Run{}, err
		}
	}
	turns, err := selectContext(cfg.Prompt.Body, history, body, extracted.String(), imageCount, cfg.Model.ImageQuotaTokens, cfg.Model.ContextTokens, cfg.Model.MaxOutputTokens)
	if err != nil {
		return ThreadDetail{}, Run{}, err
	}
	texts := make([]string, len(turns))
	for i := range turns {
		texts[i] = turns[i].Text
	}
	texts = append(texts, body)
	now := s.now()
	reservation := EstimateQuotaReservation(cfg.Prompt.Body, texts, extracted.String(), imageCount, cfg.Model.ImageQuotaTokens, cfg.Model.MaxOutputTokens, now)
	sum := sha256.Sum256([]byte(cfg.Prompt.Body))
	admission := RuntimeAdmission{
		StudentID: studentID, ThreadID: threadID, CreateThread: create, ThreadTitle: strings.TrimSpace(title), Subject: subject,
		MessageID: uuid.New(), MessageBody: strings.TrimSpace(body), IdempotencyKey: key, Attachments: metadata, AttemptNo: 1,
		SelectedTurns: turns, ExtractedText: extracted.String(), ImageCount: imageCount,
		Snapshot:    RuntimeSnapshot{Provider: cfg, PromptSHA256: hex.EncodeToString(sum[:])},
		Reservation: reservation, Now: now,
	}
	return s.store.AdmitRun(ctx, admission)
}

func (s *studentService) ListThreads(ctx context.Context, principal Principal, cursor ThreadCursor) ([]Thread, ThreadCursor, error) {
	if !studentOK(principal) {
		return nil, ThreadCursor{}, ErrNotFound
	}
	return s.store.ListThreads(ctx, principal.User.ID, cursor)
}

func (s *studentService) GetThread(ctx context.Context, principal Principal, id uuid.UUID, cursor MessageCursor) (ThreadDetail, error) {
	if !studentOK(principal) || id == uuid.Nil {
		return ThreadDetail{}, ErrNotFound
	}
	return s.store.GetThread(ctx, principal.User.ID, id, cursor)
}

func (s *studentService) CancelRun(ctx context.Context, principal Principal, id uuid.UUID) (Run, error) {
	if !studentOK(principal) || id == uuid.Nil {
		return Run{}, ErrNotFound
	}
	return s.store.CancelRun(ctx, principal.User.ID, id, s.now())
}

func (s *studentService) RetryRun(ctx context.Context, principal Principal, sourceRunID uuid.UUID, key string) (Run, error) {
	if !studentOK(principal) || sourceRunID == uuid.Nil {
		return Run{}, ErrNotFound
	}
	if !validIdempotency(key) {
		return Run{}, ErrInvalidInput
	}
	source, err := s.store.GetRun(ctx, principal.User.ID, sourceRunID)
	if err != nil {
		return Run{}, err
	}
	if source.Status != RunFailed && source.Status != RunCancelled {
		return Run{}, ErrRunConflict
	}
	if _, run, existingErr := s.store.GetRunByIdempotency(ctx, principal.User.ID, key); existingErr == nil {
		if run.TriggerMessageID != source.TriggerMessageID {
			return Run{}, ErrRunConflict
		}
		return run, nil
	} else if !errors.Is(existingErr, ErrNotFound) {
		return Run{}, existingErr
	}
	detail, err := s.store.GetThread(ctx, principal.User.ID, source.ThreadID, MessageCursor{Limit: 1})
	if err != nil {
		return Run{}, err
	}
	modality := ModalityText
	imageCount := 0
	var extracted strings.Builder
	for _, attachment := range source.TriggerAttachments {
		if attachment.Modality == ModalityVision {
			modality = ModalityVision
			imageCount++
			continue
		}
		text, loadErr := s.attachments.LoadAIText(ctx, principal.User.ID, attachment.FileVersionID)
		if loadErr != nil {
			return Run{}, loadErr
		}
		extracted.WriteString(text)
	}
	history, err := s.store.LoadContext(ctx, principal.User.ID, source.ThreadID)
	if err != nil {
		return Run{}, err
	}
	triggerIndex := -1
	for i := range history {
		if history[i].ID == source.TriggerMessageID {
			triggerIndex = i
			break
		}
	}
	if triggerIndex < 0 || history[triggerIndex].Role != "student" {
		return Run{}, ErrNotFound
	}
	current := history[triggerIndex].Body
	cfg, err := s.config.ForRun(ctx, detail.Thread.Subject, modality)
	if err != nil {
		return Run{}, err
	}
	defer zeroBytes(cfg.APIKey)
	turns, err := selectContext(cfg.Prompt.Body, history[:triggerIndex], current, extracted.String(), imageCount, cfg.Model.ImageQuotaTokens, cfg.Model.ContextTokens, cfg.Model.MaxOutputTokens)
	if err != nil {
		return Run{}, err
	}
	texts := make([]string, 0, len(turns)+1)
	for i := range turns {
		texts = append(texts, turns[i].Text)
	}
	texts = append(texts, current)
	sum := sha256.Sum256([]byte(cfg.Prompt.Body))
	now := s.now()
	res := EstimateQuotaReservation(cfg.Prompt.Body, texts, extracted.String(), imageCount, cfg.Model.ImageQuotaTokens, cfg.Model.MaxOutputTokens, now)
	_, run, err := s.store.RetryRun(ctx, RuntimeRetryAdmission{
		StudentID: principal.User.ID, SourceRunID: sourceRunID, RunID: uuid.New(), IdempotencyKey: key,
		Snapshot: RuntimeSnapshot{Provider: cfg, PromptSHA256: hex.EncodeToString(sum[:])}, Reservation: res, Now: now,
	})
	return run, err
}

func selectContext(system string, history []Message, current, extracted string, imageCount int, imageQuotaTokens, contextLimit, maxOutput int64) ([]GatewayContextTurn, error) {
	mandatory := int64(len([]byte(system))+len([]byte(current))+len([]byte(extracted))) + int64(imageCount)*imageQuotaTokens + maxOutput
	if contextLimit <= 0 || maxOutput <= 0 || mandatory > contextLimit {
		return nil, ErrContextTooLarge
	}
	type pair struct{ user, assistant Message }
	pairs := make([]pair, 0)
	for i := 0; i+1 < len(history); i++ {
		if history[i].Role == "student" && history[i+1].Role == "assistant" {
			pairs = append(pairs, pair{history[i], history[i+1]})
			i++
		}
	}
	remaining := contextLimit - mandatory
	selected := make([]pair, 0, len(pairs))
	for i := len(pairs) - 1; i >= 0; i-- {
		size := int64(len([]byte(pairs[i].user.Body)) + len([]byte(pairs[i].assistant.Body)))
		if size > remaining {
			break
		}
		remaining -= size
		selected = append(selected, pairs[i])
	}
	out := make([]GatewayContextTurn, 0, len(selected)*2)
	for i := len(selected) - 1; i >= 0; i-- {
		out = append(out, GatewayContextTurn{Role: "student", Text: selected[i].user.Body}, GatewayContextTurn{Role: "assistant", Text: selected[i].assistant.Body})
	}
	return out, nil
}

func studentOK(p Principal) bool {
	return p.User.ID != uuid.Nil && p.User.Role == auth.RoleStudent && p.User.Status == auth.StatusActive
}
func validTitle(v string) bool       { n := len([]rune(strings.TrimSpace(v))); return n >= 1 && n <= 160 }
func validBody(v string) bool        { n := len([]rune(strings.TrimSpace(v))); return n >= 1 && n <= 100000 }
func validIdempotency(v string) bool { n := len(v); return n >= 16 && n <= 128 }

func sameAttachmentBindings(inputs []AttachmentInput, persisted []AttachmentMetadata) bool {
	if len(inputs) != len(persisted) {
		return false
	}
	ordered := append([]AttachmentInput(nil), inputs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SortPosition < ordered[j].SortPosition })
	for i := range ordered {
		if ordered[i].FileVersionID != persisted[i].FileVersionID {
			return false
		}
	}
	return true
}

var _ StudentService = (*studentService)(nil)
