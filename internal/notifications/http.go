package notifications

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

const maxRequestBody = 64 * 1024

type HTTPService interface {
	List(context.Context, uuid.UUID, Cursor) ([]Notification, Cursor, error)
	UnreadCount(context.Context, uuid.UUID) (int64, error)
	MarkRead(context.Context, uuid.UUID, uuid.UUID) error
	MarkAllRead(context.Context, uuid.UUID) (int64, error)
}
type Handler struct {
	service HTTPService
	now     func() time.Time
}

func NewHandler(s HTTPService) *Handler { return &Handler{service: s, now: time.Now} }
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore, notificationNoSniff, h.requireActive)
	r.Get("/", h.list)
	r.Get("/unread-count", h.count)
	r.Post("/{id}/read", h.markRead)
	r.Post("/read-all", h.markAll)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { httpx.Error(w, r, 404, "not_found", "资源不存在") })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, 405, "method_not_allowed", "请求方法不被允许")
	})
	return r
}
func notificationNoSniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
func (h *Handler) requireActive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFromContext(r.Context())
		if !ok {
			httpx.Error(w, r, 401, "unauthenticated", "请先登录")
			return
		}
		if u.Status != auth.StatusActive || (u.Role != auth.RoleAdmin && u.Role != auth.RoleStudent) {
			httpx.Error(w, r, 403, "forbidden", "无权访问")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func userID(r *http.Request) uuid.UUID { u, _ := auth.UserFromContext(r.Context()); return u.ID }

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "cursor" && k != "limit") || len(v) != 1 || v[0] == "" {
			bad(w, r)
			return
		}
	}
	limit := 20
	if raw := q.Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || strconv.Itoa(n) != raw || n < 1 || n > 100 {
			bad(w, r)
			return
		}
		limit = n
	}
	cursor, err := decodeNotificationCursor(q.Get("cursor"), h.now())
	if err != nil {
		bad(w, r)
		return
	}
	cursor.Limit = limit
	items, next, err := h.service.List(r.Context(), userID(r), cursor)
	if err != nil {
		notifyError(w, r, err)
		return
	}
	httpx.JSON(w, 200, struct {
		Data []Notification `json:"data"`
		Meta struct {
			NextCursor string `json:"nextCursor,omitempty"`
		} `json:"meta"`
	}{Data: items, Meta: struct {
		NextCursor string `json:"nextCursor,omitempty"`
	}{encodeNotificationCursor(next)}})
}
func (h *Handler) count(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		bad(w, r)
		return
	}
	n, err := h.service.UnreadCount(r.Context(), userID(r))
	if err != nil {
		notifyError(w, r, err)
		return
	}
	httpx.JSON(w, 200, struct {
		Data struct {
			Count int64 `json:"count"`
		} `json:"data"`
	}{Data: struct {
		Count int64 `json:"count"`
	}{n}})
}
func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || !exactEmptyObject(w, r) {
		if r.URL.RawQuery != "" {
			bad(w, r)
		}
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || id == uuid.Nil || id.String() != chi.URLParam(r, "id") {
		notFound(w, r)
		return
	}
	if err = h.service.MarkRead(r.Context(), userID(r), id); err != nil {
		notifyError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"data": map[string]any{}})
}
func (h *Handler) markAll(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || !exactEmptyObject(w, r) {
		if r.URL.RawQuery != "" {
			bad(w, r)
		}
		return
	}
	n, err := h.service.MarkAllRead(r.Context(), userID(r))
	if err != nil {
		notifyError(w, r, err)
		return
	}
	httpx.JSON(w, 200, struct {
		Data struct {
			Count int64 `json:"count"`
		} `json:"data"`
	}{Data: struct {
		Count int64 `json:"count"`
	}{n}})
}

func exactEmptyObject(w http.ResponseWriter, r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return unsupported(w, r)
	}
	mt, p, err := mime.ParseMediaType(values[0])
	if err != nil || mt != "application/json" || len(p) > 1 || (len(p) == 1 && !strings.EqualFold(p["charset"], "utf-8")) {
		return unsupported(w, r)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	var raw map[string]json.RawMessage
	d := json.NewDecoder(r.Body)
	if err = d.Decode(&raw); err != nil {
		var too *http.MaxBytesError
		if errors.As(err, &too) {
			httpx.Error(w, r, 413, "request_too_large", "请求体过大")
		} else {
			bad(w, r)
		}
		return false
	}
	if raw == nil || len(raw) != 0 {
		bad(w, r)
		return false
	}
	if err = d.Decode(&struct{}{}); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求体过大")
		} else {
			bad(w, r)
		}
		return false
	}
	return true
}
func unsupported(w http.ResponseWriter, r *http.Request) bool {
	httpx.Error(w, r, 415, "unsupported_media_type", "仅支持 application/json")
	return false
}
func bad(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, 400, "invalid_request", "请求参数无效")
}
func notFound(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, 404, "not_found", "资源不存在")
}
func notifyError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrNotFound) {
		notFound(w, r)
	} else if errors.Is(err, ErrInvalidInput) {
		bad(w, r)
	} else {
		httpx.Error(w, r, 500, "internal_error", "服务暂不可用")
	}
}

type cursorWire struct {
	CreatedAt string `json:"createdAt"`
	ID        string `json:"id"`
}

func encodeNotificationCursor(c Cursor) string {
	if c.ID == uuid.Nil || c.CreatedAt.IsZero() {
		return ""
	}
	b, _ := json.Marshal(cursorWire{c.CreatedAt.UTC().Format(time.RFC3339Nano), c.ID.String()})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeNotificationCursor(raw string, now time.Time) (Cursor, error) {
	if raw == "" {
		return Cursor{}, nil
	}
	if len(raw) > 512 || strings.Contains(raw, "=") {
		return Cursor{}, ErrInvalidInput
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil || base64.RawURLEncoding.EncodeToString(b) != raw {
		return Cursor{}, ErrInvalidInput
	}
	var v cursorWire
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&v); e != nil {
		return Cursor{}, ErrInvalidInput
	}
	if e = d.Decode(&struct{}{}); e != io.EOF {
		return Cursor{}, ErrInvalidInput
	}
	at, e := time.Parse(time.RFC3339Nano, v.CreatedAt)
	if e != nil || at.Location() != time.UTC || at.Format(time.RFC3339Nano) != v.CreatedAt || at.After(now.UTC()) {
		return Cursor{}, ErrInvalidInput
	}
	id, e := uuid.Parse(v.ID)
	if e != nil || id == uuid.Nil || id.String() != v.ID {
		return Cursor{}, ErrInvalidInput
	}
	c := Cursor{CreatedAt: at.UTC(), ID: id}
	if encodeNotificationCursor(c) != raw {
		return Cursor{}, ErrInvalidInput
	}
	return c, nil
}
