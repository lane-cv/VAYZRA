package qanda

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

type Service struct {
	store Store
	uow   UnitOfWork
	now   func() time.Time
}

func NewService(store Store, uow UnitOfWork, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, uow: uow, now: now}
}

func (s *Service) CreateThread(ctx context.Context, actor Principal, in CreateThreadInput) (Thread, Message, error) {
	if err := authorizeStudent(actor); err != nil {
		return Thread{}, Message{}, err
	}
	in, err := normalizeCreateInput(in)
	if err != nil {
		return Thread{}, Message{}, err
	}
	if s.uow == nil {
		return Thread{}, Message{}, ErrInvalidInput
	}
	var thread Thread
	var message Message
	err = s.uow.WithinTx(ctx, func(store TxStore, audits audit.Writer, notifications NotificationWriter) error {
		thread, message, err = store.FindMessageByIdempotency(ctx, actor.User.ID, in.IdempotencyKey)
		if err == nil {
			if message.Kind != MessageKindInitial {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		attachments, validateErr := validateMessageAttachments(ctx, store, actor, in.Attachments)
		if validateErr != nil {
			return validateErr
		}
		var created bool
		thread, message, created, err = store.CreateThreadWithFirstMessage(ctx, actor.User.ID, in, s.now().UTC())
		if err != nil || !created {
			return err
		}
		if err = bindMessageAttachments(ctx, store, message.ID, attachments); err != nil {
			return err
		}
		message.Attachments = attachments
		if err = audits.Write(ctx, auditEvent(actor, "qa.thread_created", thread.ID, map[string]any{
			"messageCount": "1", "attachmentCount": strconv.Itoa(len(in.Attachments)),
		})); err != nil {
			return err
		}
		adminID, findErr := store.ActiveAdminID(ctx)
		if findErr != nil {
			return findErr
		}
		return notifications.Notify(ctx, NotificationIntent{
			RecipientUserID: adminID,
			Kind:            "qa_created", Title: "New student question", Summary: "A student created a question.",
			TargetType: "qa_thread", TargetID: thread.ID, TargetPath: "/admin/questions/" + thread.ID.String(),
			DedupeKey: "qa-created:" + message.ID.String(),
		})
	})
	if err != nil {
		return Thread{}, Message{}, err
	}
	return thread, message, nil
}

func (s *Service) ListStudentThreads(ctx context.Context, actor Principal, status Status, cursor ThreadCursor) ([]Thread, ThreadCursor, error) {
	if err := authorizeStudent(actor); err != nil {
		return nil, ThreadCursor{}, err
	}
	if cursor.Limit == 0 {
		cursor.Limit = 20
	}
	if cursor.Limit < 1 || cursor.Limit > 50 || !validStatusFilter(status) || !validThreadCursor(cursor) {
		return nil, ThreadCursor{}, ErrInvalidInput
	}
	if s.store == nil {
		return nil, ThreadCursor{}, ErrInvalidInput
	}
	return s.store.ListStudentThreads(ctx, actor.User.ID, status, cursor)
}

func (s *Service) GetStudentThread(ctx context.Context, actor Principal, threadID uuid.UUID) (ThreadDetail, error) {
	if err := authorizeStudent(actor); err != nil {
		return ThreadDetail{}, err
	}
	if threadID == uuid.Nil {
		return ThreadDetail{}, ErrInvalidInput
	}
	if s.store == nil {
		return ThreadDetail{}, ErrInvalidInput
	}
	thread, err := s.store.GetStudentThread(ctx, actor.User.ID, threadID)
	if err != nil {
		return ThreadDetail{}, err
	}
	messages, next, err := s.store.ListStudentMessages(ctx, actor.User.ID, threadID, MessageCursor{Limit: 100})
	if err != nil {
		return ThreadDetail{}, err
	}
	return ThreadDetail{Thread: thread, Messages: messages, NextMessageCursor: next}, nil
}

func (s *Service) ListStudentMessages(ctx context.Context, actor Principal, threadID uuid.UUID, cursor MessageCursor) ([]Message, MessageCursor, error) {
	if err := authorizeStudent(actor); err != nil {
		return nil, MessageCursor{}, err
	}
	if cursor.Limit == 0 {
		cursor.Limit = 50
	}
	if threadID == uuid.Nil || cursor.Limit < 1 || cursor.Limit > 100 || !validMessageCursor(cursor) {
		return nil, MessageCursor{}, ErrInvalidInput
	}
	if s.store == nil {
		return nil, MessageCursor{}, ErrInvalidInput
	}
	return s.store.ListStudentMessages(ctx, actor.User.ID, threadID, cursor)
}

func (s *Service) AddStudentMessage(ctx context.Context, actor Principal, in AddMessageInput) (Thread, Message, error) {
	if err := authorizeStudent(actor); err != nil {
		return Thread{}, Message{}, err
	}
	in, err := normalizeAddMessageInput(in)
	if err != nil {
		return Thread{}, Message{}, err
	}
	if s.uow == nil {
		return Thread{}, Message{}, ErrInvalidInput
	}
	var thread Thread
	var message Message
	err = s.uow.WithinTx(ctx, func(store TxStore, audits audit.Writer, notifications NotificationWriter) error {
		thread, err = store.LockStudentThread(ctx, actor.User.ID, in.ThreadID)
		if err != nil {
			return err
		}
		existingThread, existingMessage, findErr := store.FindMessageByIdempotency(ctx, actor.User.ID, in.IdempotencyKey)
		if findErr == nil {
			if existingMessage.Kind != MessageKindStudentFollowUp || existingThread.ID != in.ThreadID {
				return ErrIdempotencyConflict
			}
			thread, message = existingThread, existingMessage
			return nil
		}
		if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		next, nextErr := NextStatus(thread.Status, ActionStudentFollowUp, auth.RoleStudent)
		if nextErr != nil {
			return nextErr
		}
		attachments, validateErr := validateMessageAttachments(ctx, store, actor, in.Attachments)
		if validateErr != nil {
			return validateErr
		}
		activityAt := s.now().UTC()
		minimumActivityAt := thread.LastMessageAt.UTC().Add(time.Microsecond)
		if activityAt.Before(minimumActivityAt) {
			activityAt = minimumActivityAt
		}
		thread, message, err = store.AppendStudentMessage(ctx, thread, actor.User.ID, in, next, activityAt)
		if err != nil {
			return err
		}
		if err = bindMessageAttachments(ctx, store, message.ID, attachments); err != nil {
			return err
		}
		message.Attachments = attachments
		if err = audits.Write(ctx, auditEvent(actor, "qa.student_followed_up", thread.ID, map[string]any{
			"messageCount": "1", "attachmentCount": strconv.Itoa(len(in.Attachments)),
		})); err != nil {
			return err
		}
		adminID, findErr := store.ActiveAdminID(ctx)
		if findErr != nil {
			return findErr
		}
		return notifications.Notify(ctx, NotificationIntent{
			RecipientUserID: adminID,
			Kind:            "qa_followed_up", Title: "Student follow-up", Summary: "A student followed up on a question.",
			TargetType: "qa_thread", TargetID: thread.ID, TargetPath: "/admin/questions/" + thread.ID.String(),
			DedupeKey: "qa-followed-up:" + message.ID.String(),
		})
	})
	if err != nil {
		return Thread{}, Message{}, err
	}
	return thread, message, nil
}

func (s *Service) ListAdminThreads(ctx context.Context, actor Principal, filter AdminThreadFilter, cursor ThreadCursor) ([]Thread, ThreadCursor, error) {
	if err := authorizeAdmin(actor); err != nil {
		return nil, ThreadCursor{}, err
	}
	if cursor.Limit == 0 {
		cursor.Limit = 20
	}
	if cursor.Limit < 1 || cursor.Limit > 50 || !validStatusFilter(filter.Status) || !validThreadCursor(cursor) || (!filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To)) {
		return nil, ThreadCursor{}, ErrInvalidInput
	}
	if s.store == nil {
		return nil, ThreadCursor{}, ErrInvalidInput
	}
	return s.store.ListAdminThreads(ctx, filter, cursor)
}

func (s *Service) GetAdminThread(ctx context.Context, actor Principal, threadID uuid.UUID, cursor MessageCursor) (AdminThreadDetail, error) {
	if err := authorizeAdmin(actor); err != nil {
		return AdminThreadDetail{}, err
	}
	if cursor.Limit == 0 {
		cursor.Limit = 100
	}
	if threadID == uuid.Nil || cursor.Limit < 1 || cursor.Limit > 100 || !validMessageCursor(cursor) || s.store == nil {
		return AdminThreadDetail{}, ErrInvalidInput
	}
	thread, err := s.store.GetAdminThread(ctx, threadID)
	if err != nil {
		return AdminThreadDetail{}, err
	}
	messages, next, err := s.store.ListAdminMessages(ctx, threadID, cursor)
	if err != nil {
		return AdminThreadDetail{}, err
	}
	notes, err := s.store.ListTeacherNotes(ctx, threadID)
	if err != nil {
		return AdminThreadDetail{}, err
	}
	return AdminThreadDetail{Thread: thread, Messages: messages, Notes: notes, NextMessageCursor: next}, nil
}

func (s *Service) AddAdminMessage(ctx context.Context, actor Principal, in AddAdminMessageInput) (Thread, Message, error) {
	if err := authorizeAdmin(actor); err != nil {
		return Thread{}, Message{}, err
	}
	in, err := normalizeAddAdminMessageInput(in)
	if err != nil {
		return Thread{}, Message{}, err
	}
	if s.uow == nil {
		return Thread{}, Message{}, ErrInvalidInput
	}
	var thread Thread
	var message Message
	err = s.uow.WithinTx(ctx, func(store TxStore, audits audit.Writer, notifications NotificationWriter) error {
		thread, err = store.LockAdminThread(ctx, in.ThreadID)
		if err != nil {
			return err
		}
		existingThread, existingMessage, findErr := store.FindAdminMessageByIdempotency(ctx, actor.User.ID, in.IdempotencyKey)
		if findErr == nil {
			if existingMessage.Kind != MessageKindAdminReply || existingThread.ID != in.ThreadID {
				return ErrIdempotencyConflict
			}
			thread, message = existingThread, existingMessage
			return nil
		}
		if !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if thread.Version != in.ExpectedVersion {
			return ErrThreadConflict
		}
		next, nextErr := NextStatus(thread.Status, ActionAdminReply, auth.RoleAdmin)
		if nextErr != nil {
			return nextErr
		}
		attachments, validateErr := validateMessageAttachments(ctx, store, actor, in.Attachments)
		if validateErr != nil {
			return validateErr
		}
		activityAt := monotonicActivity(s.now().UTC(), thread.LastMessageAt)
		old := thread.Status
		thread, message, err = store.AppendAdminMessage(ctx, thread, actor.User.ID, in, next, activityAt)
		if err != nil {
			return err
		}
		if err = bindMessageAttachments(ctx, store, message.ID, attachments); err != nil {
			return err
		}
		message.Attachments = attachments
		if err = audits.Write(ctx, auditEvent(actor, "qa.admin_replied", thread.ID, map[string]any{"messageCount": "1", "attachmentCount": strconv.Itoa(len(in.Attachments)), "oldStatus": string(old), "newStatus": string(next)})); err != nil {
			return err
		}
		return notifications.Notify(ctx, NotificationIntent{RecipientUserID: thread.StudentID, Kind: "qa_replied", Title: "Teacher reply", Summary: "Your teacher replied to a question.", TargetType: "qa_thread", TargetID: thread.ID, TargetPath: "/student/questions/" + thread.ID.String(), DedupeKey: "qa-replied:" + message.ID.String()})
	})
	if err != nil {
		return Thread{}, Message{}, err
	}
	return thread, message, nil
}

func validateMessageAttachments(ctx context.Context, store TxStore, actor Principal, inputs []AttachmentInput) ([]Attachment, error) {
	if len(inputs) == 0 {
		return []Attachment{}, nil
	}
	validator, ok := store.(QAAttachmentValidator)
	if !ok {
		return nil, ErrInvalidInput
	}
	return validator.ValidateForMessage(ctx, actor, inputs)
}

func bindMessageAttachments(ctx context.Context, store TxStore, messageID uuid.UUID, attachments []Attachment) error {
	if len(attachments) == 0 {
		return nil
	}
	binder, ok := store.(QAAttachmentBinder)
	if !ok {
		return ErrInvalidInput
	}
	return binder.BindMessageAttachments(ctx, messageID, attachments)
}

func (s *Service) ChangeStatus(ctx context.Context, actor Principal, in ChangeStatusInput) (Thread, error) {
	if err := authorizeAdmin(actor); err != nil {
		return Thread{}, err
	}
	if in.ThreadID == uuid.Nil || in.ExpectedVersion < 1 || !validStatusFilter(in.Status) || in.Status == "" || s.uow == nil {
		return Thread{}, ErrInvalidInput
	}
	var thread Thread
	err := s.uow.WithinTx(ctx, func(store TxStore, audits audit.Writer, notifications NotificationWriter) error {
		var err error
		thread, err = store.LockAdminThread(ctx, in.ThreadID)
		if err != nil {
			return err
		}
		if thread.Version != in.ExpectedVersion {
			return ErrThreadConflict
		}
		var action Action
		switch {
		case in.Status == StatusInProgress && thread.Status == StatusPending:
			action = ActionClaim
		case in.Status == StatusInProgress && thread.Status == StatusCompleted:
			action = ActionReopen
		case in.Status == StatusCompleted:
			action = ActionComplete
		default:
			return ErrInvalidStatusTransition
		}
		next, err := NextStatus(thread.Status, action, auth.RoleAdmin)
		if err != nil {
			return err
		}
		old := thread.Status
		thread, err = store.UpdateAdminThreadStatus(ctx, thread, next, s.now().UTC())
		if err != nil {
			return err
		}
		if err = audits.Write(ctx, auditEvent(actor, "qa.status_changed", thread.ID, map[string]any{"oldStatus": string(old), "newStatus": string(next)})); err != nil {
			return err
		}
		return notifications.Notify(ctx, NotificationIntent{RecipientUserID: thread.StudentID, Kind: "qa_status_changed", Title: "Question status changed", Summary: "Your question status changed.", TargetType: "qa_thread", TargetID: thread.ID, TargetPath: "/student/questions/" + thread.ID.String(), DedupeKey: "qa-status:" + thread.ID.String() + ":" + strconv.FormatInt(thread.Version, 10)})
	})
	if err != nil {
		return Thread{}, err
	}
	return thread, nil
}

func (s *Service) AddTeacherNote(ctx context.Context, actor Principal, in AddTeacherNoteInput) (TeacherNote, error) {
	if err := authorizeAdmin(actor); err != nil {
		return TeacherNote{}, err
	}
	in, err := normalizeTeacherNoteInput(in)
	if err != nil {
		return TeacherNote{}, err
	}
	if s.uow == nil {
		return TeacherNote{}, ErrInvalidInput
	}
	var note TeacherNote
	err = s.uow.WithinTx(ctx, func(store TxStore, audits audit.Writer, _ NotificationWriter) error {
		if _, lockErr := store.LockAdminThread(ctx, in.ThreadID); lockErr != nil {
			return lockErr
		}
		note, err = store.InsertTeacherNote(ctx, in.ThreadID, actor.User.ID, in.Body, s.now().UTC())
		if err != nil {
			return err
		}
		return audits.Write(ctx, auditEvent(actor, "qa.teacher_note_added", in.ThreadID, map[string]any{"noteCount": "1"}))
	})
	if err != nil {
		return TeacherNote{}, err
	}
	return note, nil
}

func monotonicActivity(now, last time.Time) time.Time {
	minimum := last.UTC().Add(time.Microsecond)
	if now.Before(minimum) {
		return minimum
	}
	return now
}

func authorizeStudent(actor Principal) error {
	if actor.User.ID == uuid.Nil || actor.User.Role != auth.RoleStudent || actor.User.Status != auth.StatusActive || strings.TrimSpace(actor.RequestID) == "" || actor.IP == nil {
		return ErrForbidden
	}
	return nil
}

func authorizeAdmin(actor Principal) error {
	if actor.User.ID == uuid.Nil || actor.User.Role != auth.RoleAdmin || actor.User.Status != auth.StatusActive || strings.TrimSpace(actor.RequestID) == "" || actor.IP == nil {
		return ErrForbidden
	}
	return nil
}

func validThreadCursor(cursor ThreadCursor) bool {
	zeroTime := cursor.LastMessageAt.IsZero()
	zeroID := cursor.ID == uuid.Nil
	return zeroTime == zeroID
}

func validStatusFilter(status Status) bool {
	switch status {
	case "", StatusPending, StatusInProgress, StatusWaitingStudent, StatusCompleted:
		return true
	default:
		return false
	}
}

func validMessageCursor(cursor MessageCursor) bool {
	zeroTime := cursor.CreatedAt.IsZero()
	zeroID := cursor.ID == uuid.Nil
	return zeroTime == zeroID
}

func auditEvent(actor Principal, action string, threadID uuid.UUID, metadata map[string]any) audit.Event {
	return audit.Event{
		ActorUserID: actor.User.ID,
		Action:      action,
		TargetType:  "qa_thread",
		TargetID:    threadID.String(),
		Metadata:    metadata,
		RequestID:   actor.RequestID,
		IP:          append(net.IP(nil), actor.IP...),
	}
}
