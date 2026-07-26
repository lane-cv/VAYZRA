package aiqa

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/httpx"
)

const (
	maxStudentAIRequestBytes = 256 * 1024
	maxStudentMessageRunes   = 20_000
	maxStudentAIPage         = 100
)

type studentThreadDTO struct {
	ID            uuid.UUID `json:"id"`
	Title         string    `json:"title"`
	Subject       Subject   `json:"subject"`
	LastMessageAt time.Time `json:"lastMessageAt"`
	CreatedAt     time.Time `json:"createdAt"`
}

type studentAttachmentDTO struct {
	FileVersionID uuid.UUID `json:"fileVersionId"`
	SortPosition  int       `json:"sortPosition"`
	DisplayName   string    `json:"displayName"`
	DetectedMIME  string    `json:"detectedMime,omitempty"`
	Size          int64     `json:"size"`
}

type studentMessageDTO struct {
	ID          uuid.UUID              `json:"id"`
	Role        string                 `json:"role"`
	Body        string                 `json:"body"`
	RunID       *uuid.UUID             `json:"runId,omitempty"`
	Attachments []studentAttachmentDTO `json:"attachments"`
	CreatedAt   time.Time              `json:"createdAt"`
}

type studentRunDTO struct {
	ID           uuid.UUID `json:"id"`
	Status       RunStatus `json:"status"`
	AttemptNo    int       `json:"attemptNo"`
	LastSequence int64     `json:"lastSequence"`
	ErrorCode    string    `json:"errorCode,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type studentThreadDetailDTO struct {
	Thread            studentThreadDTO    `json:"thread"`
	Messages          []studentMessageDTO `json:"messages"`
	ActiveRun         *studentRunDTO      `json:"activeRun,omitempty"`
	NextMessageCursor string              `json:"nextMessageCursor,omitempty"`
}

func studentThreadView(v Thread) studentThreadDTO {
	return studentThreadDTO{ID: v.ID, Title: v.Title, Subject: v.Subject, LastMessageAt: v.LastMessageAt, CreatedAt: v.CreatedAt}
}

func studentMessageView(v Message) studentMessageDTO {
	attachments := make([]studentAttachmentDTO, len(v.Attachments))
	for i := range v.Attachments {
		attachments[i] = studentAttachmentDTO{
			FileVersionID: v.Attachments[i].FileVersionID, SortPosition: i, DisplayName: v.Attachments[i].DisplayName,
			DetectedMIME: v.Attachments[i].DetectedMIME, Size: v.Attachments[i].Size,
		}
	}
	var runID *uuid.UUID
	if v.RunID != uuid.Nil {
		id := v.RunID
		runID = &id
	}
	return studentMessageDTO{ID: v.ID, Role: v.Role, Body: v.Body, RunID: runID, Attachments: attachments, CreatedAt: v.CreatedAt}
}

func studentRunView(v Run) studentRunDTO {
	return studentRunDTO{
		ID: v.ID, Status: v.Status, AttemptNo: v.AttemptNo, LastSequence: v.LastSequence,
		ErrorCode: v.ErrorCode, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
	}
}

func studentDetailView(v ThreadDetail) studentThreadDetailDTO {
	messages := make([]studentMessageDTO, len(v.Messages))
	for i := range v.Messages {
		messages[i] = studentMessageView(v.Messages[i])
	}
	var active *studentRunDTO
	if v.ActiveRun != nil {
		dto := studentRunView(*v.ActiveRun)
		active = &dto
	}
	return studentThreadDetailDTO{
		Thread: studentThreadView(v.Thread), Messages: messages, ActiveRun: active,
		NextMessageCursor: encodeAIMessageCursor(v.NextMessageCursor),
	}
}

func decodeStudentAIJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
		return false
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" || len(params) != 0 && !(len(params) == 1 && strings.EqualFold(params["charset"], "utf-8")) {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxStudentAIRequestBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
		} else {
			studentAIInvalid(w, r)
		}
		return false
	}
	if !utf8.Valid(raw) {
		studentAIInvalid(w, r)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
		} else {
			studentAIInvalid(w, r)
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		studentAIInvalid(w, r)
		return false
	}
	return true
}

func studentAIIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) == 0 {
		httpx.Error(w, r, http.StatusBadRequest, "idempotency_key_required", "缺少幂等键")
		return "", false
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) != values[0] || strings.Contains(values[0], ",") || !validIdempotency(values[0]) {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_idempotency_key", "幂等键无效")
		return "", false
	}
	return values[0], true
}

func studentAICanonicalUUID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

func validStudentAIText(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed != "" && utf8.ValidString(raw) && utf8.RuneCountInString(raw) <= maxStudentMessageRunes
}

func studentAIError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, ErrInvalidInput):
		studentAIInvalid(w, r)
	case errors.Is(err, ErrAIDisabled):
		httpx.Error(w, r, http.StatusServiceUnavailable, "AI_DISABLED", "AI 答疑暂不可用")
	case errors.Is(err, ErrQuotaExceeded):
		httpx.Error(w, r, http.StatusTooManyRequests, "QUOTA_EXCEEDED", "AI 使用额度不足")
	case errors.Is(err, ErrAIBusy):
		httpx.Error(w, r, http.StatusConflict, "AI_BUSY", "已有 AI 回答正在生成")
	case errors.Is(err, ErrAttachmentNotReady):
		httpx.Error(w, r, http.StatusConflict, "ATTACHMENT_NOT_READY", "附件尚未处理完成")
	case errors.Is(err, ErrContextTooLarge):
		httpx.Error(w, r, http.StatusUnprocessableEntity, "CONTEXT_TOO_LARGE", "问题上下文过长")
	case errors.Is(err, ErrProviderUnavailable):
		httpx.Error(w, r, http.StatusServiceUnavailable, "PROVIDER_UNAVAILABLE", "AI 服务暂不可用")
	case errors.Is(err, ErrRunConflict):
		httpx.Error(w, r, http.StatusConflict, "RUN_CONFLICT", "运行状态冲突")
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}

func studentAIInvalid(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
}

type aiThreadCursorWire struct {
	LastMessageAt string `json:"lastMessageAt"`
	ID            string `json:"id"`
}
type aiMessageCursorWire struct {
	CreatedAt string `json:"createdAt"`
	ID        string `json:"id"`
}

func encodeAIThreadCursor(v ThreadCursor) string {
	if v.ID == uuid.Nil || v.LastMessageAt.IsZero() {
		return ""
	}
	return encodeAICursor(aiThreadCursorWire{LastMessageAt: v.LastMessageAt.UTC().Format(time.RFC3339Nano), ID: v.ID.String()})
}
func encodeAIMessageCursor(v MessageCursor) string {
	if v.ID == uuid.Nil || v.CreatedAt.IsZero() {
		return ""
	}
	return encodeAICursor(aiMessageCursorWire{CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339Nano), ID: v.ID.String()})
}
func encodeAICursor(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeAIThreadCursor(raw string, now time.Time) (ThreadCursor, error) {
	if raw == "" {
		return ThreadCursor{}, nil
	}
	var wire aiThreadCursorWire
	if err := decodeAICursor(raw, &wire); err != nil {
		return ThreadCursor{}, err
	}
	at, id, err := aiCursorParts(wire.LastMessageAt, wire.ID, now)
	if err != nil {
		return ThreadCursor{}, err
	}
	out := ThreadCursor{LastMessageAt: at, ID: id}
	if encodeAIThreadCursor(out) != raw {
		return ThreadCursor{}, ErrInvalidInput
	}
	return out, nil
}
func decodeAIMessageCursor(raw string, now time.Time) (MessageCursor, error) {
	if raw == "" {
		return MessageCursor{}, nil
	}
	var wire aiMessageCursorWire
	if err := decodeAICursor(raw, &wire); err != nil {
		return MessageCursor{}, err
	}
	at, id, err := aiCursorParts(wire.CreatedAt, wire.ID, now)
	if err != nil {
		return MessageCursor{}, err
	}
	out := MessageCursor{CreatedAt: at, ID: id}
	if encodeAIMessageCursor(out) != raw {
		return MessageCursor{}, ErrInvalidInput
	}
	return out, nil
}
func decodeAICursor(raw string, target any) error {
	if len(raw) > 512 || strings.Contains(raw, "=") {
		return ErrInvalidInput
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != raw {
		return ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return ErrInvalidInput
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidInput
	}
	return nil
}
func aiCursorParts(rawTime, rawID string, now time.Time) (time.Time, uuid.UUID, error) {
	at, err := time.Parse(time.RFC3339Nano, rawTime)
	if err != nil || at.Location() != time.UTC || at.Format(time.RFC3339Nano) != rawTime || at.After(now.UTC()) {
		return time.Time{}, uuid.Nil, ErrInvalidInput
	}
	id, err := studentAICanonicalUUID(rawID)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidInput
	}
	return at.UTC(), id, nil
}

func studentAIQuery(r *http.Request, allowed ...string) (url.Values, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, ErrInvalidInput
	}
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[key] = true
	}
	for key, values := range query {
		if !set[key] || len(values) != 1 {
			return nil, ErrInvalidInput
		}
	}
	return query, nil
}
func explicitEmptyAIQuery(query url.Values, keys ...string) bool {
	for _, key := range keys {
		if values, exists := query[key]; exists && values[0] == "" {
			return true
		}
	}
	return false
}
func studentAILimit(raw string) (int, error) {
	if raw == "" {
		return 20, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(n) != raw || n < 1 || n > maxStudentAIPage {
		return 0, ErrInvalidInput
	}
	return n, nil
}
