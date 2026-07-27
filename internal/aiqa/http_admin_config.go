package aiqa

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"

	"happylearn.local/app/internal/platform/redisx"
)

type AdminConfigHTTPService = AdminConfigService

type AdminConfigHTTPConfig struct {
	TrustedProxyCIDRs   []netip.Prefix
	ProviderTestLimiter redisx.ProviderTestRateLimiter
}

type AdminConfigHandler struct {
	service             AdminConfigHTTPService
	trusted             []netip.Prefix
	providerTestLimiter redisx.ProviderTestRateLimiter
}

func NewAdminConfigHandler(s AdminConfigHTTPService, trusted []netip.Prefix) *AdminConfigHandler {
	return NewAdminConfigHandlerWithConfig(s, AdminConfigHTTPConfig{TrustedProxyCIDRs: trusted})
}

func NewAdminConfigHandlerWithConfig(s AdminConfigHTTPService, cfg AdminConfigHTTPConfig) *AdminConfigHandler {
	return &AdminConfigHandler{service: s, trusted: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...), providerTestLimiter: cfg.ProviderTestLimiter}
}
func (h *AdminConfigHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore)
	r.Use(auth.RequireRole(auth.RoleAdmin))
	r.Get("/providers", h.list)
	r.Post("/providers", h.create)
	r.Put("/providers/{id}", h.update)
	r.Post("/providers/{id}/test", h.testProvider)
	r.Put("/active-provider", h.activate)
	r.Get("/providers/{id}/models", h.models)
	r.Put("/providers/{id}/models/{modelId}", h.putModel)
	r.Get("/prompts", h.prompts)
	r.Put("/prompts/{subject}", h.putPrompt)
	r.Get("/limits", h.limits)
	r.Put("/limits/global", h.putGlobal)
	r.Put("/limits/students/{studentId}", h.putStudent)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { httpx.Error(w, r, 404, "not_found", "资源不存在") })
	return r
}

func (h *AdminConfigHandler) testProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := routeID(w, r, "id")
	if !ok {
		return
	}
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	if h.providerTestLimiter == nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "PROVIDER_UNAVAILABLE", "供应商测试暂不可用")
		return
	}
	decision, err := h.providerTestLimiter.AllowProviderTest(r.Context(), p.User.ID)
	if err != nil {
		httpx.Error(w, r, http.StatusServiceUnavailable, "internal_error", "服务暂不可用")
		return
	}
	if !decision.Allowed {
		seconds := int(math.Ceil(decision.RetryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		httpx.Error(w, r, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后重试")
		return
	}
	result, err := h.service.TestProvider(r.Context(), p, id)
	if err != nil {
		switch {
		case errors.Is(err, ErrProviderTestBusy):
			w.Header().Set("X-Error-Code", "PROVIDER_UNAVAILABLE")
			httpx.JSON(w, http.StatusConflict, result)
		case errors.Is(err, ErrProviderUnavailable):
			w.Header().Set("X-Error-Code", "PROVIDER_UNAVAILABLE")
			httpx.JSON(w, http.StatusServiceUnavailable, result)
		default:
			configError(w, r, err)
		}
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}
func (h *AdminConfigHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		return Principal{}, false
	}
	ip, e := httpx.ClientIP(r, h.trusted)
	if e != nil {
		bad(w, r)
		return Principal{}, false
	}
	return Principal{User: u, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(ip.AsSlice())}, true
}
func (h *AdminConfigHandler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.ListProviders(r.Context(), p)
	if e != nil {
		configError(w, r, e)
		return
	}
	if v == nil {
		v = []ProviderView{}
	}
	httpx.JSON(w, 200, struct {
		Data []ProviderView `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name         string       `json:"name"`
		BaseURL      string       `json:"baseUrl"`
		APIKey       string       `json:"apiKey"`
		ProtocolMode ProtocolMode `json:"protocolMode"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	k := r.Header.Values("Idempotency-Key")
	if len(k) != 1 || len(k[0]) < 16 {
		bad(w, r)
		return
	}
	v, e := h.service.CreateProvider(r.Context(), p, CreateProviderInput{Name: b.Name, BaseURL: b.BaseURL, APIKey: b.APIKey, ProtocolMode: b.ProtocolMode, IdempotencyKey: k[0]})
	if e != nil {
		configError(w, r, e)
		return
	}
	httpx.JSON(w, 201, struct {
		Data ProviderView `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := routeID(w, r, "id")
	if !ok {
		return
	}
	var b struct {
		Name            string       `json:"name"`
		BaseURL         string       `json:"baseUrl"`
		ProtocolMode    ProtocolMode `json:"protocolMode"`
		APIKey          *string      `json:"apiKey"`
		ExpectedVersion int64        `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.UpdateProvider(r.Context(), p, UpdateProviderInput{ID: id, Name: b.Name, BaseURL: b.BaseURL, ProtocolMode: b.ProtocolMode, APIKey: b.APIKey, ExpectedVersion: b.ExpectedVersion})
	if e != nil {
		configError(w, r, e)
		return
	}
	httpx.JSON(w, 200, struct {
		Data ProviderView `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) activate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ProviderID      uuid.UUID `json:"providerId"`
		ExpectedVersion int64     `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.ActivateProvider(r.Context(), p, b.ProviderID, b.ExpectedVersion)
	if e != nil {
		configError(w, r, e)
		return
	}
	httpx.JSON(w, 200, struct {
		Data ProviderView `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) models(w http.ResponseWriter, r *http.Request) {
	id, ok := routeID(w, r, "id")
	if !ok {
		return
	}
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.ListModels(r.Context(), p, id)
	if e != nil {
		configError(w, r, e)
		return
	}
	if v == nil {
		v = []ModelView{}
	}
	httpx.JSON(w, 200, struct {
		Data []ModelView `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) putModel(w http.ResponseWriter, r *http.Request) {
	pid, ok := routeID(w, r, "id")
	if !ok {
		return
	}
	id, ok := routeID(w, r, "modelId")
	if !ok {
		return
	}
	var b PutModelInput
	if !decode(w, r, &b) {
		return
	}
	b.ProviderID = pid
	b.ID = id
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.PutModel(r.Context(), p, b)
	if e != nil {
		configError(w, r, e)
		return
	}
	httpx.JSON(w, 200, struct {
		Data ModelView `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) prompts(w http.ResponseWriter, r *http.Request) {
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.ListPrompts(r.Context(), p)
	if e != nil {
		configError(w, r, e)
		return
	}
	if v == nil {
		v = []PromptView{}
	}
	httpx.JSON(w, 200, struct {
		Data []PromptView `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) putPrompt(w http.ResponseWriter, r *http.Request) {
	sub := Subject(chi.URLParam(r, "subject"))
	if !subjectOK(sub) {
		bad(w, r)
		return
	}
	var b struct {
		Body            string `json:"body"`
		ExpectedVersion int64  `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.PutPrompt(r.Context(), p, PutPromptInput{Subject: sub, Body: b.Body, ExpectedVersion: b.ExpectedVersion})
	if e != nil {
		configError(w, r, e)
		return
	}
	httpx.JSON(w, 200, struct {
		Data PromptView `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) limits(w http.ResponseWriter, r *http.Request) {
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.GetLimits(r.Context(), p)
	if e != nil {
		configError(w, r, e)
		return
	}
	httpx.JSON(w, 200, struct {
		Data LimitViews `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) putGlobal(w http.ResponseWriter, r *http.Request) {
	var b PutLimitsInput
	if !decode(w, r, &b) {
		return
	}
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.PutGlobalLimits(r.Context(), p, b)
	if e != nil {
		configError(w, r, e)
		return
	}
	httpx.JSON(w, 200, struct {
		Data LimitView `json:"data"`
	}{v})
}
func (h *AdminConfigHandler) putStudent(w http.ResponseWriter, r *http.Request) {
	id, ok := routeID(w, r, "studentId")
	if !ok {
		return
	}
	var b PutLimitsInput
	if !decode(w, r, &b) {
		return
	}
	p, ok := h.actor(w, r)
	if !ok {
		return
	}
	v, e := h.service.PutStudentLimits(r.Context(), p, id, b)
	if e != nil {
		configError(w, r, e)
		return
	}
	httpx.JSON(w, 200, struct {
		Data LimitView `json:"data"`
	}{v})
}
func configError(w http.ResponseWriter, r *http.Request, e error) {
	switch {
	case errors.Is(e, ErrForbidden):
		httpx.Error(w, r, 403, "forbidden", "无权访问")
	case errors.Is(e, ErrNotFound):
		httpx.Error(w, r, 404, "not_found", "资源不存在")
	case errors.Is(e, ErrConfigConflict):
		httpx.Error(w, r, 409, "config_conflict", "配置已更新")
	case errors.Is(e, ErrAIDisabled):
		httpx.Error(w, r, 409, "AI_DISABLED", "AI 未启用")
	case errors.Is(e, ErrInvalidInput):
		bad(w, r)
	default:
		httpx.Error(w, r, 500, "internal_error", "服务暂不可用")
	}
}
func bad(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, 400, "invalid_request", "请求参数无效")
}
func routeID(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	raw := chi.URLParam(r, key)
	id, e := uuid.Parse(raw)
	if e != nil || id == uuid.Nil || id.String() != raw {
		bad(w, r)
		return uuid.Nil, false
	}
	return id, true
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	types := r.Header.Values("Content-Type")
	if len(types) != 1 || types[0] != "application/json" {
		bad(w, r)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(target); e != nil {
		bad(w, r)
		return false
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		bad(w, r)
		return false
	}
	return true
}

var _ = context.Background
var _ = uuid.Nil
