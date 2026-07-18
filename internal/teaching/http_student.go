package teaching

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
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
	"happylearn.local/app/internal/platform/redisx"
)

const maxStudentRequestBody = 16 * 1024

type StudentHTTPConfig struct {
	TrustedProxyCIDRs []netip.Prefix
	ProgressLimiter   redisx.ProgressWriteLimiter
}

type StudentHandler struct {
	service           StudentHTTPService
	trustedProxyCIDRs []netip.Prefix
	progressLimiter   redisx.ProgressWriteLimiter
}

func NewStudentHandler(service StudentHTTPService) *StudentHandler {
	return NewStudentHandlerWithConfig(service, StudentHTTPConfig{})
}
func NewStudentHandlerWithConfig(service StudentHTTPService, cfg StudentHTTPConfig) *StudentHandler {
	return &StudentHandler{service: service, trustedProxyCIDRs: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...), progressLimiter: cfg.ProgressLimiter}
}
func (h *StudentHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore)
	r.Use(auth.RequireRole(auth.RoleStudent))
	r.Get("/catalog", h.Browse)
	r.Get("/lessons/{id}", h.GetLesson)
	r.Get("/search", h.Search)
	r.Post("/progress", h.UpdateProgress)
	return r
}

func (h *StudentHandler) Browse(w http.ResponseWriter, r *http.Request) {
	grade, ok := studentQueryUUID(w, r, "gradeId")
	if !ok {
		return
	}
	term, ok := studentQueryUUID(w, r, "termId")
	if !ok {
		return
	}
	subject, ok := studentQueryUUID(w, r, "subjectId")
	if !ok {
		return
	}
	chapter, ok := studentQueryUUID(w, r, "chapterId")
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	nodes, err := h.service.Browse(r.Context(), actor, BrowseInput{GradeID: grade, TermID: term, SubjectID: subject, ChapterID: chapter})
	if err != nil {
		studentError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []CatalogNode `json:"data"`
	}{nodes})
}

func (h *StudentHandler) GetLesson(w http.ResponseWriter, r *http.Request) {
	id, ok := studentUUID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	lesson, err := h.service.GetLesson(r.Context(), actor, id)
	if err != nil {
		studentError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data Revision `json:"data"`
	}{lesson})
}

func (h *StudentHandler) Search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if len(query["q"]) != 1 {
		studentBad(w, r)
		return
	}
	limit := 20
	if raw := query.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			studentBad(w, r)
			return
		}
	}
	after, ok := decodeStudentCursor(w, r, query.Get("cursor"))
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	results, next, err := h.service.Search(r.Context(), actor, SearchInput{Query: query.Get("q"), Limit: limit, After: after})
	if err != nil {
		studentError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data       []Revision `json:"data"`
		NextCursor string     `json:"nextCursor,omitempty"`
	}{Data: results, NextCursor: encodeStudentCursor(next)})
}

func (h *StudentHandler) UpdateProgress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RevisionID  string    `json:"revisionId"`
		Viewed      bool      `json:"viewed"`
		Anchor      string    `json:"anchor"`
		ScrollRatio float64   `json:"scrollRatio"`
		ObservedAt  time.Time `json:"observedAt"`
	}
	if !decodeStudentJSON(w, r, &body) {
		return
	}
	revisionID, ok := studentUUID(w, r, body.RevisionID)
	if !ok {
		return
	}
	sessionID, ok := auth.SessionIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return
	}
	if h.progressLimiter != nil {
		decision, err := h.progressLimiter.AllowProgressWrite(r.Context(), sessionID)
		if err == nil && !decision.Allowed {
			retryAfter := int64(decision.RetryAfter.Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			httpx.Error(w, r, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后重试")
			return
		}
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.UpdateProgress(r.Context(), actor, ProgressInput{RevisionID: revisionID, Viewed: body.Viewed, Anchor: body.Anchor, ScrollRatio: body.ScrollRatio, ObservedAt: body.ObservedAt}); err != nil {
		studentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *StudentHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	user, _ := auth.UserFromContext(r.Context())
	addr, err := httpx.ClientIP(r, h.trustedProxyCIDRs)
	if err != nil {
		studentBad(w, r)
		return Principal{}, false
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}, true
}

func studentQueryUUID(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	values := r.URL.Query()[key]
	if len(values) == 0 {
		return uuid.Nil, true
	}
	if len(values) != 1 {
		studentBad(w, r)
		return uuid.Nil, false
	}
	return studentUUID(w, r, values[0])
}
func studentUUID(w http.ResponseWriter, r *http.Request, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		studentBad(w, r)
		return uuid.Nil, false
	}
	return id, true
}
func encodeStudentCursor(cursor SearchCursor) string {
	if cursor.ID == uuid.Nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(cursor.SortKey, 10) + ":" + cursor.ID.String()))
}
func decodeStudentCursor(w http.ResponseWriter, r *http.Request, raw string) (SearchCursor, bool) {
	if raw == "" {
		return SearchCursor{}, true
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		studentBad(w, r)
		return SearchCursor{}, false
	}
	sortRaw, idRaw, ok := strings.Cut(string(decoded), ":")
	if !ok {
		studentBad(w, r)
		return SearchCursor{}, false
	}
	sortKey, err := strconv.ParseInt(sortRaw, 10, 64)
	if err != nil {
		studentBad(w, r)
		return SearchCursor{}, false
	}
	id, err := uuid.Parse(idRaw)
	if err != nil || id == uuid.Nil {
		studentBad(w, r)
		return SearchCursor{}, false
	}
	return SearchCursor{SortKey: sortKey, ID: id}, true
}
func decodeStudentJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		studentUnsupported(w, r)
		return false
	}
	media, _, err := mime.ParseMediaType(types[0])
	if err != nil || media != "application/json" {
		studentUnsupported(w, r)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxStudentRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		studentDecodeError(w, r, err)
		return false
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		studentBad(w, r)
		return false
	}
	return true
}
func studentDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求内容过大")
		return
	}
	studentBad(w, r)
}
func studentBad(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusBadRequest, "invalid_request", "请求参数无效")
}
func studentUnsupported(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 JSON 请求")
}
func studentError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrInvalid):
		studentBad(w, r)
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}
