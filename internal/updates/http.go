package updates

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

type AdminHandler struct {
	service HTTPService
	trusted []netip.Prefix
}

func NewAdminHandler(service HTTPService, trusted []netip.Prefix) *AdminHandler {
	return &AdminHandler{service: service, trusted: append([]netip.Prefix(nil), trusted...)}
}

func (h *AdminHandler) Routes() http.Handler {
	router := chi.NewRouter()
	router.Use(httpx.NoStore, auth.RequireRole(auth.RoleAdmin))
	router.Get("/status", h.status)
	router.Post("/check", h.check)
	router.Post("/apply", h.apply)
	router.Post("/rollback", h.rollback)
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不被允许")
	})
	return router
}

func (h *AdminHandler) status(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) {
		invalid(w, r)
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	status, err := h.service.Status(r.Context(), principal)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeStatus(w, status)
}

func (h *AdminHandler) check(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) || !emptyBody(w, r) {
		invalid(w, r)
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	status, err := h.service.Check(r.Context(), principal)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeStatus(w, status)
}

func (h *AdminHandler) apply(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) || !emptyBody(w, r) {
		invalid(w, r)
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	status, err := h.service.Apply(r.Context(), principal)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, struct {
		Data Status `json:"data"`
	}{Data: status})
}

func (h *AdminHandler) rollback(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) || !emptyBody(w, r) {
		invalid(w, r)
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	status, err := h.service.Rollback(r.Context(), principal)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, struct {
		Data Status `json:"data"`
	}{Data: status})
}

func (h *AdminHandler) principal(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.ID == uuid.Nil || user.Role != auth.RoleAdmin || user.Status != auth.StatusActive {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
		return Principal{}, false
	}
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		invalid(w, r)
		return Principal{}, false
	}
	return Principal{
		User: user, RequestID: httpx.RequestIDFromContext(r.Context()),
		IP: net.IP(addr.AsSlice()),
	}, true
}

func noQuery(r *http.Request) bool {
	return r.URL.RawQuery == ""
}

func emptyBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength > 0 {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2))
	return err == nil && len(raw) == 0
}

func invalid(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
}

func writeStatus(w http.ResponseWriter, status Status) {
	httpx.JSON(w, http.StatusOK, struct {
		Data Status `json:"data"`
	}{Data: status})
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrUpdateBusy):
		httpx.Error(w, r, http.StatusConflict, "update_in_progress", "更新正在进行中")
	case errors.Is(err, ErrDirtyCheckout):
		httpx.Error(w, r, http.StatusPreconditionFailed, "update_checkout_dirty", "部署目录有未提交修改，已停止更新")
	case errors.Is(err, ErrUpdateUnavailable):
		httpx.Error(w, r, http.StatusNotFound, "updates_disabled", "当前部署未启用在线更新")
	case errors.Is(err, ErrRollbackUnavailable):
		httpx.Error(w, r, http.StatusConflict, "rollback_unavailable", "当前更新架构不支持安全的手动回滚")
	case errors.Is(err, ErrAgentProtocolOutdated):
		httpx.Error(w, r, http.StatusConflict, "update_agent_protocol_outdated", "更新代理协议过旧，请在宿主机完整重新部署")
	case errors.Is(err, ErrAgentUnavailable):
		httpx.Error(w, r, http.StatusServiceUnavailable, "update_unavailable", "更新服务暂不可用")
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}
