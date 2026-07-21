package files

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

const maxUploadJSONBody = 32 * 1024

type UploadHTTPService interface {
	Create(context.Context, Principal, CreateUploadInput) (UploadView, error)
	Status(context.Context, Principal, uuid.UUID) (UploadView, error)
	PutPart(context.Context, Principal, PutPartInput) (PartView, error)
	Complete(context.Context, Principal, uuid.UUID) (CompletedUpload, error)
	Cancel(context.Context, Principal, uuid.UUID) error
}

type UploadHTTPConfig struct{ TrustedProxyCIDRs []netip.Prefix }

type UploadHandler struct {
	service           UploadHTTPService
	trustedProxyCIDRs []netip.Prefix
}

func NewUploadHandler(service UploadHTTPService) *UploadHandler {
	return NewUploadHandlerWithConfig(service, UploadHTTPConfig{})
}

func NewUploadHandlerWithConfig(service UploadHTTPService, cfg UploadHTTPConfig) *UploadHandler {
	return &UploadHandler{service: service, trustedProxyCIDRs: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...)}
}

func (h *UploadHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore)
	r.Use(auth.RequireRole(auth.RoleAdmin))
	r.Post("/", h.Create)
	r.Get("/{id}", h.Status)
	r.Put("/{id}/parts/{number}", h.PutPart)
	r.Post("/{id}/complete", h.Complete)
	r.Post("/{id}/cancel", h.Cancel)
	return r
}

func (h *UploadHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName    string `json:"displayName"`
		DeclaredMIME   string `json:"declaredMime"`
		ExpectedSize   int64  `json:"expectedSize"`
		ExpectedSHA256 string `json:"expectedSha256"`
	}
	if !decodeUploadJSON(w, r, &body) {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	view, err := h.service.Create(r.Context(), actor, CreateUploadInput{DisplayName: body.DisplayName, DeclaredMIME: body.DeclaredMIME, ExpectedSize: body.ExpectedSize, ExpectedSHA256: body.ExpectedSHA256})
	if err != nil {
		uploadHTTPError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data UploadView `json:"data"`
	}{view})
}

func (h *UploadHandler) Status(w http.ResponseWriter, r *http.Request) {
	id, ok := uploadRouteID(w, r)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	view, err := h.service.Status(r.Context(), actor, id)
	if err != nil {
		uploadHTTPError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data UploadView `json:"data"`
	}{view})
}

func (h *UploadHandler) PutPart(w http.ResponseWriter, r *http.Request) {
	id, ok := uploadRouteID(w, r)
	if !ok {
		return
	}
	number, err := strconv.Atoi(chi.URLParam(r, "number"))
	if err != nil || number < 1 || number > 10000 {
		uploadBad(w, r)
		return
	}
	if values := r.Header.Values("Content-Type"); len(values) != 1 || values[0] != "application/octet-stream" {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持二进制分片")
		return
	}
	hashes := r.Header.Values("X-Part-SHA256")
	if len(hashes) != 1 || !validHash(hashes[0]) {
		uploadBad(w, r)
		return
	}
	if r.ContentLength < 1 || r.ContentLength > UploadPartSize || len(r.Header.Values("Content-Length")) > 1 {
		uploadBad(w, r)
		return
	}
	if values := r.Header.Values("Content-Length"); len(values) == 1 {
		parsed, parseErr := strconv.ParseInt(values[0], 10, 64)
		if parseErr != nil || parsed != r.ContentLength {
			uploadBad(w, r)
			return
		}
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, r.ContentLength+1)
	part, err := h.service.PutPart(r.Context(), actor, PutPartInput{SessionID: id, Number: number, Size: r.ContentLength, SHA256: hashes[0], Body: r.Body})
	if err != nil {
		uploadHTTPError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data PartView `json:"data"`
	}{part})
}

func (h *UploadHandler) Complete(w http.ResponseWriter, r *http.Request) {
	if !decodeUploadJSON(w, r, &struct{}{}) {
		return
	}
	id, ok := uploadRouteID(w, r)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	completed, err := h.service.Complete(r.Context(), actor, id)
	if err != nil {
		uploadHTTPError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data CompletedUpload `json:"data"`
	}{completed})
}

func (h *UploadHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	if !decodeUploadJSON(w, r, &struct{}{}) {
		return
	}
	id, ok := uploadRouteID(w, r)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.Cancel(r.Context(), actor, id); err != nil {
		uploadHTTPError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *UploadHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	user, _ := auth.UserFromContext(r.Context())
	address, err := httpx.ClientIP(r, h.trustedProxyCIDRs)
	if err != nil {
		uploadBad(w, r)
		return Principal{}, false
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(address.AsSlice())}, true
}

func decodeUploadJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" || len(parameters) != 0 {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
			return false
		}
		uploadBad(w, r)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		uploadBad(w, r)
		return false
	}
	return true
}

func uploadRouteID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || id == uuid.Nil {
		uploadBad(w, r)
		return uuid.Nil, false
	}
	return id, true
}

func uploadBad(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
}

func uploadHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, ErrUploadExpired):
		httpx.Error(w, r, http.StatusGone, "upload_expired", "上传会话已过期")
	case errors.Is(err, ErrTooManyPartRequests):
		httpx.Error(w, r, http.StatusTooManyRequests, "upload_part_busy", "上传分片并发过多")
	case errors.Is(err, ErrUploadPartConflict):
		httpx.Error(w, r, http.StatusConflict, "upload_part_conflict", "分片内容冲突")
	case errors.Is(err, ErrUploadIncomplete):
		httpx.Error(w, r, http.StatusConflict, "upload_incomplete", "上传分片不完整")
	case errors.Is(err, ErrDraftConflict):
		httpx.Error(w, r, http.StatusConflict, "draft_conflict", "草稿已更新，请刷新后重试")
	case errors.Is(err, ErrUploadConflict):
		httpx.Error(w, r, http.StatusConflict, "upload_conflict", "上传状态冲突")
	case errors.Is(err, ErrPartHashMismatch), errors.Is(err, ErrFinalHashMismatch):
		httpx.Error(w, r, http.StatusUnprocessableEntity, "upload_hash_mismatch", "上传校验失败")
	case errors.Is(err, ErrInvalid), strings.Contains(err.Error(), "request body too large"):
		uploadBad(w, r)
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}

var _ UploadHTTPService = (*UploadService)(nil)
