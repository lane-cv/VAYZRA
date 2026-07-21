package qanda

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

type StudentHTTPService interface {
	CreateThread(context.Context, Principal, CreateThreadInput) (Thread, Message, error)
	ListStudentThreads(context.Context, Principal, Status, ThreadCursor) ([]Thread, ThreadCursor, error)
	GetStudentThread(context.Context, Principal, uuid.UUID) (ThreadDetail, error)
	ListStudentMessages(context.Context, Principal, uuid.UUID, MessageCursor) ([]Message, MessageCursor, error)
	AddStudentMessage(context.Context, Principal, AddMessageInput) (Thread, Message, error)
}

// AdminHTTPService is intentionally narrow until the teacher workflow lands.
// Keeping a distinct interface prevents student handlers from gaining teacher
// methods merely because both roles share one concrete PostgreSQL service.
type AdminHTTPService interface {
	ListAdminThreads(context.Context, Principal, AdminThreadFilter, ThreadCursor) ([]Thread, ThreadCursor, error)
	GetAdminThread(context.Context, Principal, uuid.UUID) (AdminThreadDetail, error)
	AddAdminMessage(context.Context, Principal, AddAdminMessageInput) (Thread, Message, error)
	ChangeStatus(context.Context, Principal, ChangeStatusInput) (Thread, error)
	AddTeacherNote(context.Context, Principal, AddTeacherNoteInput) (TeacherNote, error)
}

type HTTPServices struct {
	Student StudentHTTPService
	Admin   AdminHTTPService
}

type threadDTO struct {
	ID            uuid.UUID  `json:"id"`
	Title         string     `json:"title"`
	Status        Status     `json:"status"`
	Version       int64      `json:"version"`
	LastMessageAt time.Time  `json:"lastMessageAt"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
}

type attachmentDTO struct {
	FileVersionID uuid.UUID `json:"fileVersionId"`
	SortPosition  int       `json:"sortPosition"`
	DisplayName   string    `json:"displayName"`
}

type messageDTO struct {
	ID          uuid.UUID       `json:"id"`
	SenderRole  auth.Role       `json:"senderRole"`
	Kind        MessageKind     `json:"kind"`
	Body        string          `json:"body"`
	CreatedAt   time.Time       `json:"createdAt"`
	Attachments []attachmentDTO `json:"attachments"`
}

type threadDetailDTO struct {
	Thread            threadDTO    `json:"thread"`
	Messages          []messageDTO `json:"messages"`
	NextMessageCursor string       `json:"nextMessageCursor,omitempty"`
}

func threadView(thread Thread) threadDTO {
	return threadDTO{
		ID: thread.ID, Title: thread.Title, Status: thread.Status, Version: thread.Version,
		LastMessageAt: thread.LastMessageAt, CreatedAt: thread.CreatedAt, UpdatedAt: thread.UpdatedAt,
		CompletedAt: thread.CompletedAt,
	}
}

func messageView(message Message) messageDTO {
	attachments := make([]attachmentDTO, len(message.Attachments))
	for i, attachment := range message.Attachments {
		attachments[i] = attachmentDTO{FileVersionID: attachment.FileVersionID, SortPosition: attachment.SortPosition, DisplayName: attachment.DisplayName}
	}
	return messageDTO{ID: message.ID, SenderRole: message.SenderRole, Kind: message.Kind, Body: message.Body, CreatedAt: message.CreatedAt, Attachments: attachments}
}

func detailView(detail ThreadDetail) threadDetailDTO {
	messages := make([]messageDTO, len(detail.Messages))
	for i := range detail.Messages {
		messages[i] = messageView(detail.Messages[i])
	}
	return threadDetailDTO{Thread: threadView(detail.Thread), Messages: messages, NextMessageCursor: encodeMessageCursor(detail.NextMessageCursor)}
}

type threadCursorWire struct {
	LastMessageAt string `json:"lastMessageAt"`
	ID            string `json:"id"`
}

type messageCursorWire struct {
	CreatedAt string `json:"createdAt"`
	ID        string `json:"id"`
}

func encodeThreadCursor(cursor ThreadCursor) string {
	if cursor.ID == uuid.Nil || cursor.LastMessageAt.IsZero() {
		return ""
	}
	return encodeCursor(threadCursorWire{LastMessageAt: cursor.LastMessageAt.UTC().Format(time.RFC3339Nano), ID: cursor.ID.String()})
}

func encodeMessageCursor(cursor MessageCursor) string {
	if cursor.ID == uuid.Nil || cursor.CreatedAt.IsZero() {
		return ""
	}
	return encodeCursor(messageCursorWire{CreatedAt: cursor.CreatedAt.UTC().Format(time.RFC3339Nano), ID: cursor.ID.String()})
}

func encodeCursor(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeThreadCursor(raw string, now time.Time) (ThreadCursor, error) {
	if raw == "" {
		return ThreadCursor{}, nil
	}
	var wire threadCursorWire
	if err := decodeCursor(raw, &wire); err != nil {
		return ThreadCursor{}, ErrInvalidInput
	}
	at, id, err := cursorParts(wire.LastMessageAt, wire.ID, now)
	if err != nil {
		return ThreadCursor{}, err
	}
	if encodeThreadCursor(ThreadCursor{LastMessageAt: at, ID: id}) != raw {
		return ThreadCursor{}, ErrInvalidInput
	}
	return ThreadCursor{LastMessageAt: at, ID: id}, nil
}

func decodeMessageCursor(raw string, now time.Time) (MessageCursor, error) {
	if raw == "" {
		return MessageCursor{}, nil
	}
	var wire messageCursorWire
	if err := decodeCursor(raw, &wire); err != nil {
		return MessageCursor{}, ErrInvalidInput
	}
	at, id, err := cursorParts(wire.CreatedAt, wire.ID, now)
	if err != nil {
		return MessageCursor{}, err
	}
	if encodeMessageCursor(MessageCursor{CreatedAt: at, ID: id}) != raw {
		return MessageCursor{}, ErrInvalidInput
	}
	return MessageCursor{CreatedAt: at, ID: id}, nil
}

func decodeCursor(raw string, target any) error {
	if len(raw) > 512 || strings.Contains(raw, "=") {
		return ErrInvalidInput
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidInput
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidInput
	}
	return nil
}

func cursorParts(rawTime, rawID string, now time.Time) (time.Time, uuid.UUID, error) {
	at, err := time.Parse(time.RFC3339Nano, rawTime)
	if err != nil || at.Location() != time.UTC || at.Format(time.RFC3339Nano) != rawTime || at.After(now.UTC()) {
		return time.Time{}, uuid.Nil, ErrInvalidInput
	}
	id, err := uuid.Parse(rawID)
	if err != nil || id == uuid.Nil || id.String() != rawID {
		return time.Time{}, uuid.Nil, ErrInvalidInput
	}
	return at.UTC(), id, nil
}

func qandaError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrInvalidInput):
		qandaBad(w, r)
	case errors.Is(err, ErrIdempotencyConflict):
		httpx.Error(w, r, http.StatusConflict, "idempotency_conflict", "幂等键与已有请求冲突")
	case errors.Is(err, ErrInvalidStatusTransition):
		httpx.Error(w, r, http.StatusConflict, "invalid_status_transition", "状态变更无效")
	case errors.Is(err, ErrThreadConflict):
		httpx.Error(w, r, http.StatusConflict, "thread_conflict", "问题已被更新，请刷新后重试")
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}

func qandaBad(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
}
