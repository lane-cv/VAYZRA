package teaching

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

const maxAdminRequestBody = 64 * 1024

type AdminHTTPConfig struct{ TrustedProxyCIDRs []netip.Prefix }
type AdminHandler struct {
	service           AdminHTTPService
	trustedProxyCIDRs []netip.Prefix
}

func NewAdminHandler(service AdminHTTPService) *AdminHandler {
	return NewAdminHandlerWithConfig(service, AdminHTTPConfig{})
}
func NewAdminHandlerWithConfig(service AdminHTTPService, cfg AdminHTTPConfig) *AdminHandler {
	return &AdminHandler{service: service, trustedProxyCIDRs: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...)}
}
func (h *AdminHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore)
	r.Use(auth.RequireRole(auth.RoleAdmin))
	r.Post("/catalog/{kind}", h.CreateCatalog)
	r.Patch("/catalog/{kind}/{id}", h.RenameCatalog)
	r.Post("/catalog/{kind}/{id}/reorder", h.ReorderCatalog)
	r.Post("/catalog/{kind}/{id}/archive", h.ArchiveCatalog)
	r.Post("/lessons", h.CreateLesson)
	r.Put("/lessons/{id}/draft", h.SaveDraft)
	r.Post("/lessons/{id}/publish", h.Publish)
	r.Post("/lessons/{id}/withdraw", h.Withdraw)
	return r
}
func (h *AdminHandler) CreateCatalog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		ParentID    string `json:"parentId"`
		SortKey     int64  `json:"sortKey"`
	}
	if !decodeAdminJSON(w, r, &body) {
		return
	}
	parent, ok := optionalUUID(w, r, body.ParentID)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	node, err := h.service.CreateCatalog(r.Context(), actor, CatalogCreateInput{Kind: catalogKind(chi.URLParam(r, "kind")), ParentID: parent, Name: body.Name, Description: body.Description, SortKey: body.SortKey})
	if err != nil {
		adminError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data CatalogNode `json:"data"`
	}{node})
}
func (h *AdminHandler) RenameCatalog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeAdminJSON(w, r, &body) {
		return
	}
	id, ok := routeUUID(w, r, "id")
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	node, err := h.service.RenameCatalog(r.Context(), actor, CatalogRenameInput{Kind: catalogKind(chi.URLParam(r, "kind")), ID: id, Name: body.Name})
	if err != nil {
		adminError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data CatalogNode `json:"data"`
	}{node})
}
func (h *AdminHandler) ReorderCatalog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SortKey int64 `json:"sortKey"`
	}
	if !decodeAdminJSON(w, r, &body) {
		return
	}
	id, ok := routeUUID(w, r, "id")
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.ReorderCatalog(r.Context(), actor, CatalogReorderInput{Kind: catalogKind(chi.URLParam(r, "kind")), ID: id, SortKey: body.SortKey}); err != nil {
		adminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *AdminHandler) ArchiveCatalog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Archived bool `json:"archived"`
	}
	if !decodeAdminJSON(w, r, &body) {
		return
	}
	id, ok := routeUUID(w, r, "id")
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.ArchiveCatalog(r.Context(), actor, CatalogArchiveInput{Kind: catalogKind(chi.URLParam(r, "kind")), ID: id, Archived: body.Archived}); err != nil {
		adminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *AdminHandler) CreateLesson(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChapterID string `json:"chapterId"`
		Title     string `json:"title"`
	}
	if !decodeAdminJSON(w, r, &body) {
		return
	}
	chapter, ok := parseAdminUUID(w, r, body.ChapterID)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	draft, err := h.service.CreateLesson(r.Context(), actor, CreateLessonInput{ChapterID: chapter, Title: body.Title})
	if err != nil {
		adminError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data Draft `json:"data"`
	}{draft})
}
func (h *AdminHandler) SaveDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title          string          `json:"title"`
		Summary        string          `json:"summary"`
		BodyMarkdown   string          `json:"bodyMarkdown"`
		SortKey        int64           `json:"sortKey"`
		Audience       Audience        `json:"audience"`
		ExternalVideos []ExternalVideo `json:"externalVideos"`
	}
	if !decodeAdminJSON(w, r, &body) {
		return
	}
	id, ok := routeUUID(w, r, "id")
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	draft, err := h.service.SaveDraft(r.Context(), actor, SaveDraftInput{LessonID: id, ExpectedVersion: version, Title: body.Title, Summary: body.Summary, BodyMarkdown: body.BodyMarkdown, SortKey: body.SortKey, Audience: body.Audience, ExternalVideos: body.ExternalVideos})
	if err != nil {
		adminError(w, r, err)
		return
	}
	w.Header().Set("ETag", strconv.FormatInt(draft.LockVersion, 10))
	httpx.JSON(w, http.StatusOK, struct {
		Data Draft `json:"data"`
	}{draft})
}
func (h *AdminHandler) Publish(w http.ResponseWriter, r *http.Request) {
	id, ok := routeUUID(w, r, "id")
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	revision, err := h.service.Publish(r.Context(), actor, PublishInput{LessonID: id, ExpectedVersion: version})
	if err != nil {
		adminError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data Revision `json:"data"`
	}{revision})
}
func (h *AdminHandler) Withdraw(w http.ResponseWriter, r *http.Request) {
	id, ok := routeUUID(w, r, "id")
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.Withdraw(r.Context(), actor, id); err != nil {
		adminError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *AdminHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	user, _ := auth.UserFromContext(r.Context())
	addr, err := httpx.ClientIP(r, h.trustedProxyCIDRs)
	if err != nil {
		adminBad(w, r)
		return Principal{}, false
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}, true
}
func catalogKind(raw string) CatalogKind { return CatalogKind(raw) }
func routeUUID(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	return parseAdminUUID(w, r, chi.URLParam(r, key))
}
func parseAdminUUID(w http.ResponseWriter, r *http.Request, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		adminBad(w, r)
		return uuid.Nil, false
	}
	return id, true
}
func optionalUUID(w http.ResponseWriter, r *http.Request, raw string) (uuid.UUID, bool) {
	if raw == "" {
		return uuid.Nil, true
	}
	return parseAdminUUID(w, r, raw)
}
func ifMatchVersion(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.Trim(r.Header.Get("If-Match"), "\"")
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		adminBad(w, r)
		return 0, false
	}
	return v, true
}
func decodeAdminJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		adminUnsupported(w, r)
		return false
	}
	media, _, err := mime.ParseMediaType(types[0])
	if err != nil || media != "application/json" {
		adminUnsupported(w, r)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAdminRequestBody)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(target); err != nil {
		adminDecodeError(w, r, err)
		return false
	}
	if err = d.Decode(&struct{}{}); err != io.EOF {
		adminBad(w, r)
		return false
	}
	return true
}
func adminDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
		return
	}
	adminBad(w, r)
}
func adminBad(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
}
func adminUnsupported(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
}
func adminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, ErrConflict):
		httpx.Error(w, r, http.StatusConflict, "draft_conflict", "草稿已更新，请刷新后重试")
	case errors.Is(err, ErrNotPublishable):
		httpx.Error(w, r, http.StatusUnprocessableEntity, "lesson_not_publishable", "课程暂不可发布")
	case errors.Is(err, ErrInvalid):
		adminBad(w, r)
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}
