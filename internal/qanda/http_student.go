package qanda

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

const maxQANDARequestBody = 64 * 1024

type StudentHTTPConfig struct {
	TrustedProxyCIDRs []netip.Prefix
}

type StudentHandler struct {
	service           StudentHTTPService
	trustedProxyCIDRs []netip.Prefix
	now               func() time.Time
}

func NewStudentHandler(service StudentHTTPService) *StudentHandler {
	return NewStudentHandlerWithConfig(service, StudentHTTPConfig{})
}

func NewStudentHandlerWithConfig(service StudentHTTPService, cfg StudentHTTPConfig) *StudentHandler {
	return &StudentHandler{service: service, trustedProxyCIDRs: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...), now: time.Now}
}

func (h *StudentHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore)
	r.Use(auth.RequireRole(auth.RoleStudent))
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/{id}", h.Get)
	r.Get("/{id}/messages", h.ListMessages)
	r.Post("/{id}/messages", h.AddMessage)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不被允许")
	})
	return r
}

func (h *StudentHandler) List(w http.ResponseWriter, r *http.Request) {
	query, ok := qandaQuery(w, r, "status", "cursor", "limit")
	if !ok {
		return
	}
	statusRaw, _, ok := qandaSingleQuery(w, r, query, "status")
	if !ok || !validStatusFilter(Status(statusRaw)) {
		if ok {
			qandaBad(w, r)
		}
		return
	}
	limit, ok := qandaLimit(w, r, query)
	if !ok {
		return
	}
	cursorRaw, _, ok := qandaSingleQuery(w, r, query, "cursor")
	if !ok {
		return
	}
	cursor, err := decodeThreadCursor(cursorRaw, h.now())
	if err != nil {
		qandaBad(w, r)
		return
	}
	cursor.Limit = limit
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	threads, next, err := h.service.ListStudentThreads(r.Context(), actor, Status(statusRaw), cursor)
	if err != nil {
		qandaError(w, r, err)
		return
	}
	data := make([]threadDTO, len(threads))
	for i := range threads {
		data[i] = threadView(threads[i])
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []threadDTO `json:"data"`
		Meta struct {
			NextCursor string `json:"nextCursor,omitempty"`
		} `json:"meta"`
	}{Data: data, Meta: struct {
		NextCursor string `json:"nextCursor,omitempty"`
	}{NextCursor: encodeThreadCursor(next)}})
}

func (h *StudentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string                `json:"title"`
		Body        string                `json:"body"`
		Attachments []attachmentInputWire `json:"attachments"`
	}
	if !decodeQANDAJSON(w, r, &body) {
		return
	}
	key, ok := qandaIdempotencyKey(w, r)
	if !ok {
		return
	}
	attachments, ok := qandaAttachments(w, r, body.Attachments)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	thread, message, err := h.service.CreateThread(r.Context(), actor, CreateThreadInput{Title: body.Title, Body: body.Body, IdempotencyKey: key, Attachments: attachments})
	if err != nil {
		qandaError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data struct {
			Thread  threadDTO  `json:"thread"`
			Message messageDTO `json:"message"`
		} `json:"data"`
	}{Data: struct {
		Thread  threadDTO  `json:"thread"`
		Message messageDTO `json:"message"`
	}{Thread: threadView(thread), Message: messageView(message)}})
}

func (h *StudentHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := qandaStudentRouteID(w, r)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	detail, err := h.service.GetStudentThread(r.Context(), actor, id)
	if err != nil {
		qandaError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data threadDetailDTO `json:"data"`
	}{Data: detailView(detail)})
}

func (h *StudentHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	id, ok := qandaStudentRouteID(w, r)
	if !ok {
		return
	}
	query, ok := qandaQuery(w, r, "cursor", "limit")
	if !ok {
		return
	}
	limit, ok := qandaLimit(w, r, query)
	if !ok {
		return
	}
	cursorRaw, _, ok := qandaSingleQuery(w, r, query, "cursor")
	if !ok {
		return
	}
	cursor, err := decodeMessageCursor(cursorRaw, h.now())
	if err != nil {
		qandaBad(w, r)
		return
	}
	cursor.Limit = limit
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	messages, next, err := h.service.ListStudentMessages(r.Context(), actor, id, cursor)
	if err != nil {
		qandaError(w, r, err)
		return
	}
	data := make([]messageDTO, len(messages))
	for i := range messages {
		data[i] = messageView(messages[i])
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []messageDTO `json:"data"`
		Meta struct {
			NextCursor string `json:"nextCursor,omitempty"`
		} `json:"meta"`
	}{Data: data, Meta: struct {
		NextCursor string `json:"nextCursor,omitempty"`
	}{NextCursor: encodeMessageCursor(next)}})
}

func (h *StudentHandler) AddMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := qandaStudentRouteID(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64                 `json:"expectedVersion"`
		Body            string                `json:"body"`
		Attachments     []attachmentInputWire `json:"attachments"`
	}
	if !decodeQANDAJSON(w, r, &body) {
		return
	}
	key, ok := qandaIdempotencyKey(w, r)
	if !ok {
		return
	}
	attachments, ok := qandaAttachments(w, r, body.Attachments)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	thread, message, err := h.service.AddStudentMessage(r.Context(), actor, AddMessageInput{ThreadID: id, ExpectedVersion: body.ExpectedVersion, Body: body.Body, IdempotencyKey: key, Attachments: attachments})
	if err != nil {
		qandaError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data struct {
			Thread  threadDTO  `json:"thread"`
			Message messageDTO `json:"message"`
		} `json:"data"`
	}{Data: struct {
		Thread  threadDTO  `json:"thread"`
		Message messageDTO `json:"message"`
	}{Thread: threadView(thread), Message: messageView(message)}})
}

func (h *StudentHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.ID == uuid.Nil || user.Role != auth.RoleStudent || user.Status != auth.StatusActive {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
		return Principal{}, false
	}
	addr, err := httpx.ClientIP(r, h.trustedProxyCIDRs)
	if err != nil {
		qandaBad(w, r)
		return Principal{}, false
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}, true
}

type attachmentInputWire struct {
	FileVersionID string `json:"fileVersionId"`
	SortPosition  int    `json:"sortPosition"`
}

func qandaAttachments(w http.ResponseWriter, r *http.Request, wire []attachmentInputWire) ([]AttachmentInput, bool) {
	attachments := make([]AttachmentInput, len(wire))
	for i, attachment := range wire {
		id, err := uuid.Parse(attachment.FileVersionID)
		if err != nil || id == uuid.Nil || id.String() != attachment.FileVersionID {
			qandaBad(w, r)
			return nil, false
		}
		attachments[i] = AttachmentInput{FileVersionID: id, SortPosition: attachment.SortPosition}
	}
	return attachments, true
}

func decodeQANDAJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxQANDARequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		qandaJSONDecodeError(w, r, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		qandaJSONDecodeError(w, r, err)
		return false
	}
	return true
}

func qandaJSONDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
		return
	}
	qandaBad(w, r)
}

func qandaIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) == 0 {
		httpx.Error(w, r, http.StatusBadRequest, "idempotency_key_required", "缺少幂等键")
		return "", false
	}
	if len(values) != 1 || !validIdempotencyKey(values[0]) {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_idempotency_key", "幂等键无效")
		return "", false
	}
	return values[0], true
}

func qandaStudentRouteID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := chi.URLParam(r, "id")
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
		return uuid.Nil, false
	}
	return id, true
}

func qandaRouteID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := chi.URLParam(r, "id")
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		qandaBad(w, r)
		return uuid.Nil, false
	}
	return id, true
}

func qandaQuery(w http.ResponseWriter, r *http.Request, allowed ...string) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		qandaBad(w, r)
		return nil, false
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range query {
		if _, ok := allowedSet[key]; !ok {
			qandaBad(w, r)
			return nil, false
		}
	}
	return query, true
}

func qandaSingleQuery(w http.ResponseWriter, r *http.Request, query url.Values, key string) (string, bool, bool) {
	values, present := query[key]
	if !present {
		return "", false, true
	}
	if len(values) != 1 || values[0] == "" {
		qandaBad(w, r)
		return "", true, false
	}
	return values[0], true, true
}

func qandaLimit(w http.ResponseWriter, r *http.Request, query url.Values) (int, bool) {
	raw, present, ok := qandaSingleQuery(w, r, query, "limit")
	if !ok {
		return 0, false
	}
	if !present {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 50 {
		qandaBad(w, r)
		return 0, false
	}
	return limit, true
}
