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
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
	"happylearn.local/app/internal/platform/redisx"
)

const (
	maxStudentRequestBody         = 16 * 1024
	maxStudentCursorDecodedLength = 57
	maxStudentCursorEncodedLength = 76
	maxCatalogCursorDecodedLength = 64
	maxCatalogCursorEncodedLength = 86
)

type StudentHTTPConfig struct {
	TrustedProxyCIDRs []netip.Prefix
	ProgressLimiter   redisx.ProgressWriteLimiter
	SearchLimiter     redisx.SearchRateLimiter
}

type StudentHandler struct {
	service           StudentHTTPService
	trustedProxyCIDRs []netip.Prefix
	progressLimiter   redisx.ProgressWriteLimiter
	searchLimiter     redisx.SearchRateLimiter
}

func NewStudentHandler(service StudentHTTPService) *StudentHandler {
	return NewStudentHandlerWithConfig(service, StudentHTTPConfig{})
}
func NewStudentHandlerWithConfig(service StudentHTTPService, cfg StudentHTTPConfig) *StudentHandler {
	return &StudentHandler{
		service:           service,
		trustedProxyCIDRs: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...),
		progressLimiter:   cfg.ProgressLimiter,
		searchLimiter:     cfg.SearchLimiter,
	}
}
func (h *StudentHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore)
	r.Use(auth.RequireRole(auth.RoleStudent))
	r.Get("/catalog", h.Browse)
	r.Get("/lessons/recent", h.Recent)
	r.Get("/lessons/{id}/position", h.GetPosition)
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
	kind := CatalogKind(r.URL.Query().Get("kind"))
	limit, ok := studentOptionalLimit(w, r)
	if !ok {
		return
	}
	after, ok := decodeCatalogCursor(w, r, r.URL.Query().Get("cursor"))
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	nodes, next, err := h.service.Browse(r.Context(), actor, BrowseInput{
		GradeID: grade, TermID: term, SubjectID: subject, ChapterID: chapter, Kind: kind, Limit: limit, After: after,
	})
	if err != nil {
		studentError(w, r, err)
		return
	}
	data := make([]studentCatalogDTO, len(nodes))
	for i := range nodes {
		data[i] = catalogDTO(nodes[i])
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data       []studentCatalogDTO `json:"data"`
		NextCursor string              `json:"nextCursor,omitempty"`
	}{Data: data, NextCursor: encodeCatalogCursor(next)})
}

func (h *StudentHandler) Recent(w http.ResponseWriter, r *http.Request) {
	limit, ok := studentOptionalLimit(w, r)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	items, err := h.service.Recent(r.Context(), actor, limit)
	if err != nil {
		studentError(w, r, err)
		return
	}
	data := make([]studentRecentDTO, len(items))
	for i := range items {
		data[i] = recentDTO(items[i])
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []studentRecentDTO `json:"data"`
	}{Data: data})
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
		Data studentLessonDTO `json:"data"`
	}{Data: lessonDTO(lesson)})
}

func (h *StudentHandler) GetPosition(w http.ResponseWriter, r *http.Request) {
	id, ok := studentUUID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	position, err := h.service.GetPosition(r.Context(), actor, id)
	if err != nil {
		studentError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data studentProgressDTO `json:"data"`
	}{Data: progressDTO(position)})
}

func (h *StudentHandler) Search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if len(query["q"]) != 1 {
		studentBad(w, r)
		return
	}
	if runes := utf8.RuneCountInString(strings.TrimSpace(query.Get("q"))); runes < 2 || runes > 64 {
		studentBad(w, r)
		return
	}
	limit, ok := studentOptionalLimit(w, r)
	if !ok {
		return
	}
	after, ok := decodeStudentCursor(w, r, query.Get("cursor"))
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if h.searchLimiter != nil {
		decision, _ := h.searchLimiter.AllowSearch(r.Context(), actor.User.ID)
		if !decision.Allowed {
			studentRateLimited(w, r, decision.RetryAfter)
			return
		}
	}
	results, next, err := h.service.Search(r.Context(), actor, SearchInput{Query: query.Get("q"), Limit: limit, After: after})
	if err != nil {
		studentError(w, r, err)
		return
	}
	data := make([]studentSearchDTO, len(results))
	for i := range results {
		data[i] = searchDTO(results[i])
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data       []studentSearchDTO `json:"data"`
		NextCursor string             `json:"nextCursor,omitempty"`
	}{Data: data, NextCursor: encodeStudentCursor(next)})
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
	if !ok || sessionID == uuid.Nil {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "请先登录")
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if h.progressLimiter != nil {
		decision, _ := h.progressLimiter.AllowProgressWrite(r.Context(), sessionID, actor.User.ID)
		if !decision.Allowed {
			studentRateLimited(w, r, decision.RetryAfter)
			return
		}
	}
	if err := h.service.UpdateProgress(r.Context(), actor, ProgressInput{RevisionID: revisionID, Viewed: body.Viewed, Anchor: body.Anchor, ScrollRatio: body.ScrollRatio, ObservedAt: body.ObservedAt}); err != nil {
		studentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type studentCatalogDTO struct {
	ID                uuid.UUID   `json:"id"`
	ParentID          *uuid.UUID  `json:"parentId,omitempty"`
	Kind              CatalogKind `json:"kind"`
	Name              string      `json:"name"`
	Description       string      `json:"description,omitempty"`
	SortKey           int64       `json:"sortKey"`
	LessonID          *uuid.UUID  `json:"lessonId,omitempty"`
	CurrentRevisionID *uuid.UUID  `json:"currentRevisionId,omitempty"`
	RevisionStatus    string      `json:"revisionStatus,omitempty"`
}
type studentProgressDTO struct {
	Viewed        bool      `json:"viewed"`
	Anchor        string    `json:"anchor"`
	ScrollRatio   float64   `json:"scrollRatio"`
	ObservedAt    time.Time `json:"observedAt"`
	FirstViewedAt time.Time `json:"firstViewedAt"`
	LastViewedAt  time.Time `json:"lastViewedAt"`
}
type studentVideoDTO struct {
	ID          uuid.UUID `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	SortKey     int64     `json:"sortKey"`
}
type studentLessonDTO struct {
	LessonID       uuid.UUID           `json:"lessonId"`
	RevisionID     uuid.UUID           `json:"revisionId"`
	Version        int64               `json:"version"`
	Title          string              `json:"title"`
	Summary        string              `json:"summary"`
	BodyMarkdown   string              `json:"bodyMarkdown"`
	SortKey        int64               `json:"sortKey"`
	PublishedAt    time.Time           `json:"publishedAt"`
	ExternalVideos []studentVideoDTO   `json:"externalVideos"`
	Files          []studentFileDTO    `json:"files"`
	Progress       *studentProgressDTO `json:"progress,omitempty"`
}
type studentFileDTO struct {
	FileVersionID    uuid.UUID `json:"fileVersionId"`
	Policy           string    `json:"policy"`
	DisplayName      string    `json:"displayName"`
	Description      string    `json:"description"`
	SortPosition     int64     `json:"sortPosition"`
	DetectedMIME     string    `json:"detectedMime"`
	BrowserPlayable  bool      `json:"browserPlayable"`
	PreviewAvailable bool      `json:"previewAvailable"`
}
type studentSearchDTO struct {
	LessonID       uuid.UUID `json:"lessonId"`
	RevisionID     uuid.UUID `json:"revisionId"`
	Title          string    `json:"title"`
	Summary        string    `json:"summary"`
	Snippet        string    `json:"snippet"`
	GradeID        uuid.UUID `json:"gradeId"`
	GradeName      string    `json:"gradeName"`
	TermID         uuid.UUID `json:"termId"`
	TermName       string    `json:"termName"`
	SubjectID      uuid.UUID `json:"subjectId"`
	SubjectName    string    `json:"subjectName"`
	ChapterID      uuid.UUID `json:"chapterId"`
	ChapterName    string    `json:"chapterName"`
	RevisionStatus string    `json:"revisionStatus"`
}
type studentRecentDTO struct {
	studentSearchDTO
	Position studentProgressDTO `json:"position"`
}

func catalogDTO(node StudentCatalogNode) studentCatalogDTO {
	dto := studentCatalogDTO{ID: node.ID, Kind: node.Kind, Name: node.Name, Description: node.Description, SortKey: node.SortKey, RevisionStatus: node.RevisionStatus}
	if node.ParentID != uuid.Nil {
		id := node.ParentID
		dto.ParentID = &id
	}
	if node.LessonID != uuid.Nil {
		id := node.LessonID
		dto.LessonID = &id
	}
	if node.CurrentRevisionID != uuid.Nil {
		id := node.CurrentRevisionID
		dto.CurrentRevisionID = &id
	}
	return dto
}
func progressDTO(p LessonProgress) studentProgressDTO {
	return studentProgressDTO{Viewed: p.Viewed, Anchor: p.Anchor, ScrollRatio: p.ScrollRatio, ObservedAt: p.ObservedAt, FirstViewedAt: p.FirstViewedAt, LastViewedAt: p.LastViewedAt}
}
func lessonDTO(lesson StudentLesson) studentLessonDTO {
	r := lesson.Revision
	dto := studentLessonDTO{LessonID: r.LessonID, RevisionID: r.ID, Version: r.Version, Title: r.Title, Summary: r.Summary, BodyMarkdown: r.BodyMarkdown, SortKey: r.SortKey, PublishedAt: r.PublishedAt, ExternalVideos: make([]studentVideoDTO, len(r.ExternalVideos)), Files: make([]studentFileDTO, len(lesson.Files))}
	for i, v := range r.ExternalVideos {
		dto.ExternalVideos[i] = studentVideoDTO{ID: v.ID, URL: v.URL, Title: v.Title, Description: v.Description, SortKey: v.SortKey}
	}
	for i, f := range lesson.Files {
		dto.Files[i] = studentFileDTO{FileVersionID: f.FileVersionID, Policy: f.Policy, DisplayName: f.DisplayName, Description: f.Description, SortPosition: f.SortPosition, DetectedMIME: f.DetectedMIME, BrowserPlayable: f.BrowserPlayable, PreviewAvailable: f.PreviewAvailable}
	}
	if lesson.Progress != nil {
		p := progressDTO(*lesson.Progress)
		dto.Progress = &p
	}
	return dto
}
func searchDTO(result SearchResult) studentSearchDTO {
	return studentSearchDTO{
		LessonID: result.LessonID, RevisionID: result.RevisionID, Title: result.Title, Summary: result.Summary, Snippet: result.Snippet,
		GradeID: result.GradeID, GradeName: result.GradeName, TermID: result.TermID, TermName: result.TermName,
		SubjectID: result.SubjectID, SubjectName: result.SubjectName, ChapterID: result.ChapterID, ChapterName: result.ChapterName,
		RevisionStatus: result.RevisionStatus,
	}
}
func recentDTO(item RecentLesson) studentRecentDTO {
	return studentRecentDTO{studentSearchDTO: searchDTO(item.SearchResult), Position: progressDTO(item.Position)}
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
func studentOptionalLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	values := r.URL.Query()["limit"]
	if len(values) == 0 {
		return 0, true
	}
	if len(values) != 1 {
		studentBad(w, r)
		return 0, false
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil {
		studentBad(w, r)
		return 0, false
	}
	return limit, true
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
	decoded, ok := decodeCursorBytes(w, r, raw, maxStudentCursorDecodedLength, maxStudentCursorEncodedLength)
	if !ok {
		return SearchCursor{}, false
	}
	sortRaw, idRaw, ok := strings.Cut(string(decoded), ":")
	if !ok || len(sortRaw) > 20 || len(idRaw) != 36 {
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
func encodeCatalogCursor(cursor CatalogCursor) string {
	if cursor.ID == uuid.Nil {
		return ""
	}
	raw := strconv.Itoa(cursor.KindRank) + ":" + strconv.FormatInt(cursor.SortKey, 10) + ":" + cursor.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
func decodeCatalogCursor(w http.ResponseWriter, r *http.Request, raw string) (CatalogCursor, bool) {
	if raw == "" {
		return CatalogCursor{}, true
	}
	decoded, ok := decodeCursorBytes(w, r, raw, maxCatalogCursorDecodedLength, maxCatalogCursorEncodedLength)
	if !ok {
		return CatalogCursor{}, false
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 3 {
		studentBad(w, r)
		return CatalogCursor{}, false
	}
	rank, err := strconv.Atoi(parts[0])
	if err != nil || rank < 1 || rank > 5 {
		studentBad(w, r)
		return CatalogCursor{}, false
	}
	sortKey, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		studentBad(w, r)
		return CatalogCursor{}, false
	}
	id, err := uuid.Parse(parts[2])
	if err != nil || id == uuid.Nil {
		studentBad(w, r)
		return CatalogCursor{}, false
	}
	return CatalogCursor{KindRank: rank, SortKey: sortKey, ID: id}, true
}
func decodeCursorBytes(w http.ResponseWriter, r *http.Request, raw string, maxDecoded, maxEncoded int) ([]byte, bool) {
	if len(raw) > maxEncoded || base64.RawURLEncoding.DecodedLen(len(raw)) > maxDecoded {
		studentBad(w, r)
		return nil, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) > maxDecoded {
		studentBad(w, r)
		return nil, false
	}
	return decoded, true
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
func studentRateLimited(w http.ResponseWriter, r *http.Request, retry time.Duration) {
	seconds := int64(retry.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	httpx.Error(w, r, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后重试")
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
