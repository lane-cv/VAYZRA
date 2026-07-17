package students

import (
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

const maxRequestBody = 32 * 1024

type HTTPConfig struct{ TrustedProxyCIDRs []netip.Prefix }
type Handler struct {
	service           HTTPService
	trustedProxyCIDRs []netip.Prefix
}

func NewHandler(service HTTPService) *Handler { return NewHandlerWithConfig(service, HTTPConfig{}) }
func NewHandlerWithConfig(service HTTPService, cfg HTTPConfig) *Handler {
	return &Handler{service: service, trustedProxyCIDRs: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...)}
}
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireRole(auth.RoleAdmin))
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Post("/{studentID}/status", h.SetStatus)
	r.Post("/{studentID}/reset-password", h.ResetPassword)
	return r
}
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			bad(w, r)
			return
		}
		limit = n
	}
	after, ok := parseCursor(w, r, r.URL.Query().Get("cursor"))
	if !ok {
		return
	}
	actor, err := h.principal(r)
	if err != nil {
		bad(w, r)
		return
	}
	users, next, err := h.service.List(r.Context(), actor, limit, after)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	views := make([]StudentView, 0, len(users))
	for _, u := range users {
		views = append(views, studentView(u))
	}
	var nextCursor any
	if next != uuid.Nil {
		nextCursor = next.String()
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []StudentView `json:"data"`
		Meta struct {
			NextCursor any `json:"nextCursor"`
		} `json:"meta"`
	}{Data: views, Meta: struct {
		NextCursor any `json:"nextCursor"`
	}{NextCursor: nextCursor}})
}
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username          string `json:"username"`
		DisplayName       string `json:"displayName"`
		TemporaryPassword string `json:"temporaryPassword"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !validUsername(normalizeUsername(input.Username)) || !validDisplayName(input.DisplayName) || auth.ValidatePassword(input.TemporaryPassword) != nil {
		bad(w, r)
		return
	}
	actor, err := h.principal(r)
	if err != nil {
		bad(w, r)
		return
	}
	student, err := h.service.Create(r.Context(), actor, CreateInput{Username: input.Username, DisplayName: input.DisplayName, TemporaryPassword: input.TemporaryPassword})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data StudentView `json:"data"`
	}{Data: studentView(student)})
}
func (h *Handler) SetStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := studentID(w, r)
	if !ok {
		return
	}
	var input struct {
		Status auth.Status `json:"status"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Status != auth.StatusActive && input.Status != auth.StatusDisabled {
		bad(w, r)
		return
	}
	actor, err := h.principal(r)
	if err != nil {
		bad(w, r)
		return
	}
	if err := h.service.SetStatus(r.Context(), actor, id, input.Status); err != nil {
		writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := studentID(w, r)
	if !ok {
		return
	}
	var input struct {
		TemporaryPassword string `json:"temporaryPassword"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if auth.ValidatePassword(input.TemporaryPassword) != nil {
		bad(w, r)
		return
	}
	actor, err := h.principal(r)
	if err != nil {
		bad(w, r)
		return
	}
	if err := h.service.ResetPassword(r.Context(), actor, id, input.TemporaryPassword); err != nil {
		writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type StudentView struct {
	ID                 string      `json:"id"`
	Username           string      `json:"username"`
	DisplayName        string      `json:"displayName"`
	Status             auth.Status `json:"status"`
	MustChangePassword bool        `json:"mustChangePassword"`
	CreatedAt          string      `json:"createdAt"`
}

func studentView(u auth.User) StudentView {
	return StudentView{ID: u.ID.String(), Username: u.Username, DisplayName: u.DisplayName, Status: u.Status, MustChangePassword: u.MustChangePassword, CreatedAt: u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")}
}
func (h *Handler) principal(r *http.Request) (Principal, error) {
	user, _ := auth.UserFromContext(r.Context())
	ip, err := h.clientIP(r)
	if err != nil {
		return Principal{}, err
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: ip}, nil
}
func (h *Handler) clientIP(r *http.Request) (net.IP, error) {
	addr, err := httpx.ClientIP(r, h.trustedProxyCIDRs)
	if err != nil {
		return nil, err
	}
	return append(net.IP(nil), addr.AsSlice()...), nil
}
func parseCursor(w http.ResponseWriter, r *http.Request, raw string) (uuid.UUID, bool) {
	if raw == "" {
		return uuid.Nil, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		bad(w, r)
		return uuid.Nil, false
	}
	return id, true
}
func studentID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "studentID"))
	if err != nil {
		bad(w, r)
		return uuid.Nil, false
	}
	return id, true
}
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		unsupported(w, r)
		return false
	}
	media, _, err := mime.ParseMediaType(types[0])
	if err != nil || media != "application/json" {
		unsupported(w, r)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		decodeError(w, r, err)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		decodeError(w, r, err)
		return false
	}
	return true
}
func decodeError(w http.ResponseWriter, r *http.Request, err error) {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
		return
	}
	bad(w, r)
}
func bad(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
}
func unsupported(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
}
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, auth.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, ErrInvalidInput), errors.Is(err, auth.ErrConflict):
		bad(w, r)
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}

var _ = strings.TrimSpace
