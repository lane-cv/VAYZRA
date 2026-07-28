package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/httpx"
)

type AdminHandler struct {
	service HTTPService
	trusted []netip.Prefix
}

func NewAdminHandler(service HTTPService, trusted []netip.Prefix) *AdminHandler {
	return &AdminHandler{
		service: service,
		trusted: append([]netip.Prefix(nil), trusted...),
	}
}

func (h *AdminHandler) Routes() http.Handler {
	router := chi.NewRouter()
	router.Use(httpx.NoStore, auth.RequireRole(auth.RoleAdmin))
	router.Get("/", h.list)
	router.Post("/", h.create)
	router.Get("/{id}", h.detail)
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不被允许")
	})
	return router
}

func (h *AdminHandler) create(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) {
		backupInvalid(w, r, "invalid_request")
		return
	}
	if !validCreateBody(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	run, err := h.service.RequestManual(r.Context(), principal, key)
	if err != nil {
		backupError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, struct {
		Data runSummaryDTO `json:"data"`
	}{Data: runSummaryView(run.Summary())})
}

func (h *AdminHandler) list(w http.ResponseWriter, r *http.Request) {
	filter, ok := listFilter(w, r)
	if !ok {
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	page, err := h.service.List(r.Context(), principal, filter)
	if err != nil {
		backupError(w, r, err)
		return
	}
	items := make([]runSummaryDTO, len(page.Items))
	for i := range page.Items {
		items[i] = runSummaryView(page.Items[i])
	}
	var meta listMetaDTO
	if !page.Next.IsZero() {
		meta.NextBeforeRequestedAt = page.Next.RequestedAt.UTC().Format(time.RFC3339Nano)
		meta.NextBeforeID = page.Next.ID.String()
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []runSummaryDTO `json:"data"`
		Meta listMetaDTO     `json:"meta"`
	}{Data: items, Meta: meta})
}

func (h *AdminHandler) detail(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) {
		backupInvalid(w, r, "invalid_request")
		return
	}
	id, err := canonicalUUID(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	detail, err := h.service.Get(r.Context(), principal, id)
	if err != nil {
		backupError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data runDetailDTO `json:"data"`
	}{Data: runDetailView(detail)})
}

func (h *AdminHandler) principal(
	w http.ResponseWriter,
	r *http.Request,
) (operations.Principal, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.ID == uuid.Nil ||
		user.Role != auth.RoleAdmin || user.Status != auth.StatusActive {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
		return operations.Principal{}, false
	}
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		backupInvalid(w, r, "invalid_request")
		return operations.Principal{}, false
	}
	return operations.Principal{
		User: user, RequestID: httpx.RequestIDFromContext(r.Context()),
		IP: net.IP(addr.AsSlice()),
	}, true
}

var publicIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || !publicIdempotencyKey.MatchString(values[0]) {
		backupInvalid(w, r, "invalid_idempotency_key")
		return "", false
	}
	return values[0], true
}

func listFilter(w http.ResponseWriter, r *http.Request) (Filter, bool) {
	query, err := exactQuery(r, "beforeRequestedAt", "beforeId", "limit")
	if err != nil {
		backupInvalid(w, r, "invalid_request")
		return Filter{}, false
	}
	filter := Filter{Limit: 50}
	rawAt, hasAt := query["beforeRequestedAt"]
	rawID, hasID := query["beforeId"]
	if hasAt != hasID {
		backupInvalid(w, r, "invalid_request")
		return Filter{}, false
	}
	if hasAt {
		filter.Before.RequestedAt, err = time.Parse(time.RFC3339Nano, rawAt[0])
		if err != nil ||
			filter.Before.RequestedAt.Location() != time.UTC ||
			filter.Before.RequestedAt.Format(time.RFC3339Nano) != rawAt[0] {
			backupInvalid(w, r, "invalid_request")
			return Filter{}, false
		}
		filter.Before.ID, err = canonicalUUID(rawID[0])
		if err != nil {
			backupInvalid(w, r, "invalid_request")
			return Filter{}, false
		}
	}
	if raw, ok := query["limit"]; ok {
		parsed, parseErr := strconv.Atoi(raw[0])
		if parseErr != nil || parsed < 1 || parsed > 100 ||
			strconv.Itoa(parsed) != raw[0] {
			backupInvalid(w, r, "invalid_request")
			return Filter{}, false
		}
		filter.Limit = parsed
	}
	return filter, true
}

func exactQuery(r *http.Request, allowed ...string) (url.Values, error) {
	if r.URL.ForceQuery {
		return nil, ErrInvalid
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	approved := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		approved[key] = struct{}{}
	}
	for key, values := range query {
		if _, ok := approved[key]; !ok || len(values) != 1 || values[0] == "" {
			return nil, ErrInvalid
		}
	}
	return query, nil
}

func canonicalUUID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

func noQuery(r *http.Request) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	return err == nil && !r.URL.ForceQuery && len(query) == 0 && r.URL.RawQuery == ""
}

func validCreateBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return true
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求体过大")
		} else {
			backupInvalid(w, r, "invalid_request")
		}
		return false
	}
	if len(raw) == 0 {
		return true
	}
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 application/json")
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" ||
		len(parameters) > 1 ||
		(len(parameters) == 1 && !strings.EqualFold(parameters["charset"], "utf-8")) ||
		!utf8.Valid(raw) {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 application/json")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	open, ok := token.(json.Delim)
	if err != nil || !ok || open != '{' || decoder.More() {
		backupInvalid(w, r, "invalid_request")
		return false
	}
	token, err = decoder.Token()
	close, ok := token.(json.Delim)
	if err != nil || !ok || close != '}' {
		backupInvalid(w, r, "invalid_request")
		return false
	}
	if !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		backupInvalid(w, r, "invalid_request")
		return false
	}
	return true
}

type runSummaryDTO struct {
	ID              string     `json:"id"`
	Trigger         Trigger    `json:"trigger"`
	State           State      `json:"state"`
	RequestedAt     time.Time  `json:"requestedAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	LogicalBytes    *int64     `json:"logicalBytes,omitempty"`
	StoredBytes     *int64     `json:"storedBytes,omitempty"`
	LocalExpiresAt  *time.Time `json:"localExpiresAt,omitempty"`
	RemoteExpiresAt *time.Time `json:"remoteExpiresAt,omitempty"`
	ErrorCategory   string     `json:"errorCategory,omitempty"`
}

func runSummaryView(summary RunSummary) runSummaryDTO {
	return runSummaryDTO{
		ID: summary.ID.String(), Trigger: summary.Trigger, State: summary.State,
		RequestedAt: summary.RequestedAt.UTC(), StartedAt: cloneTime(summary.StartedAt),
		FinishedAt:   cloneTime(summary.FinishedAt),
		LogicalBytes: cloneInt64(summary.LogicalBytes), StoredBytes: cloneInt64(summary.StoredBytes),
		LocalExpiresAt: cloneTime(summary.LocalExpiresAt), RemoteExpiresAt: cloneTime(summary.RemoteExpiresAt),
		ErrorCategory: safeText(summary.ErrorCategory),
	}
}

type artifactDTO struct {
	Kind       ArtifactKind `json:"kind"`
	Repository Repository   `json:"repository"`
	SizeBytes  int64        `json:"sizeBytes"`
	VerifiedAt time.Time    `json:"verifiedAt"`
	ExpiresAt  time.Time    `json:"expiresAt"`
}

type restoreVerificationDTO struct {
	ID                        string           `json:"id"`
	State                     RestoreState     `json:"state"`
	StartedAt                 *time.Time       `json:"startedAt,omitempty"`
	FinishedAt                *time.Time       `json:"finishedAt,omitempty"`
	RestoredMigrationVersion  *int64           `json:"restoredMigrationVersion,omitempty"`
	DatabaseRowCounts         map[string]int64 `json:"databaseRowCounts"`
	CheckedObjectCount        int64            `json:"checkedObjectCount"`
	MissingObjectCount        int64            `json:"missingObjectCount"`
	UnexpectedObjectCount     int64            `json:"unexpectedObjectCount"`
	SessionRevocationVerified bool             `json:"sessionRevocationVerified"`
	RTOSeconds                *int64           `json:"rtoSeconds,omitempty"`
	ErrorCategory             string           `json:"errorCategory,omitempty"`
}

type runDetailDTO struct {
	runSummaryDTO
	Artifacts            []artifactDTO            `json:"artifacts"`
	RestoreVerifications []restoreVerificationDTO `json:"restoreVerifications"`
}

func runDetailView(detail RunDetail) runDetailDTO {
	view := runDetailDTO{runSummaryDTO: runSummaryView(detail.Run.Summary())}
	view.Artifacts = make([]artifactDTO, len(detail.Artifacts))
	for i, artifact := range detail.Artifacts {
		view.Artifacts[i] = artifactDTO{
			Kind: artifact.Kind, Repository: artifact.Repository,
			SizeBytes: artifact.SizeBytes, VerifiedAt: artifact.VerifiedAt.UTC(),
			ExpiresAt: artifact.ExpiresAt.UTC(),
		}
	}
	view.RestoreVerifications = make([]restoreVerificationDTO, len(detail.RestoreVerifications))
	for i, verification := range detail.RestoreVerifications {
		view.RestoreVerifications[i] = restoreVerificationDTO{
			ID: verification.ID.String(), State: verification.State,
			StartedAt: cloneTime(verification.StartedAt), FinishedAt: cloneTime(verification.FinishedAt),
			RestoredMigrationVersion:  cloneInt64(verification.RestoredMigrationVersion),
			DatabaseRowCounts:         safeRowCounts(verification.DatabaseRowCounts),
			CheckedObjectCount:        verification.CheckedObjectCount,
			MissingObjectCount:        verification.MissingObjectCount,
			UnexpectedObjectCount:     verification.UnexpectedObjectCount,
			SessionRevocationVerified: verification.SessionRevocationVerified,
			RTOSeconds:                cloneInt64(verification.RTOSeconds),
			ErrorCategory:             safeText(verification.ErrorCategory),
		}
	}
	return view
}

type listMetaDTO struct {
	NextBeforeRequestedAt string `json:"nextBeforeRequestedAt,omitempty"`
	NextBeforeID          string `json:"nextBeforeId,omitempty"`
}

func backupInvalid(w http.ResponseWriter, r *http.Request, code string) {
	httpx.Error(w, r, http.StatusBadRequest, code, "请求参数无效")
}

func backupError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrInvalidTransition):
		backupInvalid(w, r, "invalid_request")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, ErrAlreadyQueued), errors.Is(err, ErrActiveClaim):
		httpx.Error(w, r, http.StatusConflict, "backup_already_queued", "已有备份正在等待或执行")
	default:
		httpx.Error(w, r, http.StatusServiceUnavailable, "backup_unavailable", "备份服务暂不可用")
	}
}
