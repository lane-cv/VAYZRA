package qanda

import (
	"errors"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

var (
	ErrForbidden               = errors.New("qanda forbidden")
	ErrInvalidInput            = errors.New("invalid qanda input")
	ErrNotFound                = errors.New("qanda thread not found")
	ErrInvalidStatusTransition = errors.New("invalid qanda status transition")
	ErrIdempotencyConflict     = errors.New("qanda idempotency conflict")
)

type Status string

const (
	StatusPending        Status = "pending"
	StatusInProgress     Status = "in_progress"
	StatusWaitingStudent Status = "waiting_student"
	StatusCompleted      Status = "completed"
)

type Action string

const (
	ActionCreate          Action = "create"
	ActionClaim           Action = "claim"
	ActionAdminReply      Action = "admin_reply"
	ActionStudentFollowUp Action = "student_follow_up"
	ActionComplete        Action = "complete"
	ActionReopen          Action = "reopen"
)

func NextStatus(current Status, action Action, actor auth.Role) (Status, error) {
	switch {
	case actor == auth.RoleStudent && action == ActionCreate && current == "":
		return StatusPending, nil
	case actor == auth.RoleAdmin && action == ActionClaim && current == StatusPending:
		return StatusInProgress, nil
	case actor == auth.RoleAdmin && action == ActionAdminReply &&
		(current == StatusPending || current == StatusInProgress || current == StatusWaitingStudent):
		return StatusWaitingStudent, nil
	case actor == auth.RoleStudent && action == ActionStudentFollowUp &&
		(current == StatusWaitingStudent || current == StatusInProgress || current == StatusCompleted):
		return StatusPending, nil
	case actor == auth.RoleAdmin && action == ActionComplete &&
		(current == StatusPending || current == StatusInProgress || current == StatusWaitingStudent):
		return StatusCompleted, nil
	case actor == auth.RoleAdmin && action == ActionReopen && current == StatusCompleted:
		return StatusInProgress, nil
	default:
		return "", ErrInvalidStatusTransition
	}
}

type Principal struct {
	User      auth.User
	RequestID string
	IP        net.IP
}

type AttachmentInput struct {
	FileVersionID uuid.UUID
	SortPosition  int
}

type CreateThreadInput struct {
	Title, Body, IdempotencyKey string
	Attachments                 []AttachmentInput
}

type AddMessageInput struct {
	ThreadID             uuid.UUID
	Body, IdempotencyKey string
	Attachments          []AttachmentInput
}

type ThreadCursor struct {
	LastMessageAt time.Time
	ID            uuid.UUID
	Limit         int
}

type MessageCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
	Limit     int
}

type Thread struct {
	ID, StudentID                       uuid.UUID
	Title                               string
	Status                              Status
	Version                             int64
	LastMessageAt, CreatedAt, UpdatedAt time.Time
	CompletedAt                         *time.Time
}

type Attachment struct {
	FileVersionID uuid.UUID
	SortPosition  int
	DisplayName   string
}

type Message struct {
	ID, ThreadID, SenderUserID uuid.UUID
	SenderRole                 auth.Role
	Body                       string
	CreatedAt                  time.Time
	Attachments                []Attachment
}

type ThreadDetail struct {
	Thread   Thread
	Messages []Message
}

type NotificationIntent struct {
	RecipientUserID                                         uuid.UUID
	Kind, Title, Summary, TargetType, TargetPath, DedupeKey string
	TargetID                                                uuid.UUID
}

func normalizeCreateInput(in CreateThreadInput) (CreateThreadInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	if !validText(in.Title, 1, 160) || !validText(in.Body, 1, 20000) || !validIdempotencyKey(in.IdempotencyKey) || !validAttachments(in.Attachments) {
		return CreateThreadInput{}, ErrInvalidInput
	}
	in.Attachments = append([]AttachmentInput(nil), in.Attachments...)
	return in, nil
}

func normalizeAddMessageInput(in AddMessageInput) (AddMessageInput, error) {
	in.Body = strings.TrimSpace(in.Body)
	if in.ThreadID == uuid.Nil || !validText(in.Body, 1, 20000) || !validIdempotencyKey(in.IdempotencyKey) || !validAttachments(in.Attachments) {
		return AddMessageInput{}, ErrInvalidInput
	}
	in.Attachments = append([]AttachmentInput(nil), in.Attachments...)
	return in, nil
}

func validText(value string, minRunes, maxRunes int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	n := utf8.RuneCountInString(value)
	return n >= minRunes && n <= maxRunes
}

func validIdempotencyKey(key string) bool {
	if !utf8.ValidString(key) || strings.TrimSpace(key) != key {
		return false
	}
	n := utf8.RuneCountInString(key)
	return n >= 16 && n <= 128
}

func validAttachments(attachments []AttachmentInput) bool {
	if len(attachments) > 20 {
		return false
	}
	files := make(map[uuid.UUID]struct{}, len(attachments))
	positions := make(map[int]struct{}, len(attachments))
	for _, attachment := range attachments {
		if attachment.FileVersionID == uuid.Nil || attachment.SortPosition < 0 || attachment.SortPosition > 19 {
			return false
		}
		if _, exists := files[attachment.FileVersionID]; exists {
			return false
		}
		if _, exists := positions[attachment.SortPosition]; exists {
			return false
		}
		files[attachment.FileVersionID] = struct{}{}
		positions[attachment.SortPosition] = struct{}{}
	}
	return true
}
