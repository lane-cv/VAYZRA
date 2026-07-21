package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

type FileCenterHTTPService interface {
	List(context.Context, Principal, FileFilter, Cursor) (FilePage, error)
	Detail(context.Context, Principal, uuid.UUID) (FileDetail, error)
	Retry(context.Context, Principal, uuid.UUID) error
	Replace(context.Context, Principal, uuid.UUID, uuid.UUID) error
	RollbackDraftBinding(context.Context, Principal, uuid.UUID, uuid.UUID, uuid.UUID) error
	RequestDelete(context.Context, Principal, uuid.UUID) error
}

type FileCenterHandler struct {
	service           FileCenterHTTPService
	trustedProxyCIDRs []netip.Prefix
}

func NewFileCenterHandler(service FileCenterHTTPService, trusted []netip.Prefix) *FileCenterHandler {
	return &FileCenterHandler{service: service, trustedProxyCIDRs: append([]netip.Prefix(nil), trusted...)}
}

func (h *FileCenterHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore)
	r.Use(auth.RequireRole(auth.RoleAdmin))
	r.Get("/", h.List)
	r.Get("/{id}", h.Detail)
	r.Post("/{id}/versions/{versionID}/retry", h.Retry)
	r.Post("/{id}/replace", h.Replace)
	r.Post("/{id}/rollback", h.Rollback)
	r.Delete("/{id}", h.Delete)
	return r
}

func (h *FileCenterHandler) List(w http.ResponseWriter, r *http.Request) {
	filter, cursor, ok := parseFileListQuery(r)
	if !ok {
		fileCenterBad(w, r)
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	page, err := h.service.List(r.Context(), actor, filter, cursor)
	if err != nil {
		fileCenterHTTPError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data FilePage `json:"data"`
	}{page})
}

func (h *FileCenterHandler) Detail(w http.ResponseWriter, r *http.Request) {
	fileID, ok := fileCenterID(w, r, "id")
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	detail, err := h.service.Detail(r.Context(), actor, fileID)
	if err != nil {
		fileCenterHTTPError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data FileDetail `json:"data"`
	}{detail})
}

func (h *FileCenterHandler) Retry(w http.ResponseWriter, r *http.Request) {
	if !decodeUploadJSON(w, r, &struct{}{}) {
		return
	}
	if _, ok := fileCenterID(w, r, "id"); !ok {
		return
	}
	versionID, ok := fileCenterID(w, r, "versionID")
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.Retry(r.Context(), actor, versionID); err != nil {
		fileCenterHTTPError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileCenterHandler) Replace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UploadedVersionID uuid.UUID `json:"uploadedVersionId"`
	}
	if !decodeUploadJSON(w, r, &body) {
		return
	}
	fileID, ok := fileCenterID(w, r, "id")
	if !ok || body.UploadedVersionID == uuid.Nil {
		if ok {
			fileCenterBad(w, r)
		}
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.Replace(r.Context(), actor, fileID, body.UploadedVersionID); err != nil {
		fileCenterHTTPError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileCenterHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LessonID      uuid.UUID `json:"lessonId"`
		FileVersionID uuid.UUID `json:"fileVersionId"`
	}
	if !decodeUploadJSON(w, r, &body) {
		return
	}
	fileID, ok := fileCenterID(w, r, "id")
	if !ok || body.LessonID == uuid.Nil || body.FileVersionID == uuid.Nil {
		if ok {
			fileCenterBad(w, r)
		}
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.RollbackDraftBinding(r.Context(), actor, fileID, body.LessonID, body.FileVersionID); err != nil {
		fileCenterHTTPError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileCenterHandler) Delete(w http.ResponseWriter, r *http.Request) {
	fileID, ok := fileCenterID(w, r, "id")
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.RequestDelete(r.Context(), actor, fileID); err != nil {
		fileCenterHTTPError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FileCenterHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	user, _ := auth.UserFromContext(r.Context())
	address, err := httpx.ClientIP(r, h.trustedProxyCIDRs)
	if err != nil {
		fileCenterBad(w, r)
		return Principal{}, false
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(address.AsSlice())}, true
}

func parseFileListQuery(r *http.Request) (FileFilter, Cursor, bool) {
	query := r.URL.Query()
	allowed := map[string]bool{"q": true, "type": true, "state": true, "reference": true, "from": true, "to": true, "cursor": true, "limit": true}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return FileFilter{}, Cursor{}, false
		}
	}
	filter := FileFilter{Name: strings.TrimSpace(query.Get("q")), Type: query.Get("type"), State: query.Get("state"), Reference: query.Get("reference")}
	for value, target := range map[string]**time.Time{"from": &filter.CreatedFrom, "to": &filter.CreatedTo} {
		if raw := query.Get(value); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return FileFilter{}, Cursor{}, false
			}
			*target = &parsed
		}
	}
	cursor := Cursor{Limit: 50}
	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return FileFilter{}, Cursor{}, false
		}
		cursor.Limit = limit
	}
	if raw := query.Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		var payload struct {
			CreatedAt time.Time `json:"createdAt"`
			ID        uuid.UUID `json:"id"`
		}
		if err != nil || json.Unmarshal(decoded, &payload) != nil || payload.CreatedAt.IsZero() || payload.ID == uuid.Nil {
			return FileFilter{}, Cursor{}, false
		}
		cursor.AfterCreatedAt, cursor.AfterID = payload.CreatedAt, payload.ID
	}
	return filter, cursor, true
}

func encodeFileCursor(createdAt time.Time, id uuid.UUID) string {
	payload, _ := json.Marshal(struct {
		CreatedAt time.Time `json:"createdAt"`
		ID        uuid.UUID `json:"id"`
	}{createdAt, id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func fileCenterID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil || id == uuid.Nil {
		fileCenterBad(w, r)
		return uuid.Nil, false
	}
	return id, true
}

func fileCenterBad(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
}

func fileCenterHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, ErrFileInUse):
		httpx.Error(w, r, http.StatusConflict, "file_in_use", "文件仍被课程引用")
	case errors.Is(err, ErrFileVersionExpired):
		httpx.Error(w, r, http.StatusGone, "file_version_expired", "文件版本已超过保留期")
	case errors.Is(err, ErrFileNotRetryable):
		httpx.Error(w, r, http.StatusConflict, "file_not_retryable", "该失败不可重试")
	case errors.Is(err, ErrAccessUnavailable):
		httpx.Error(w, r, http.StatusConflict, "file_access_unavailable", "文件尚未就绪或不支持现有访问方式")
	case errors.Is(err, ErrInvalid):
		fileCenterBad(w, r)
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}
