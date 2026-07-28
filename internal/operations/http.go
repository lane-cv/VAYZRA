package operations

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
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
	router.Get("/settings", h.getSettings)
	router.Put("/settings", h.updateSettings)
	router.Get("/audit", h.listAudit)
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不被允许")
	})
	return router
}

func (h *AdminHandler) getSettings(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) {
		operationsInvalid(w, r, "settings_invalid")
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	settings, err := h.service.GetSettings(r.Context(), principal)
	if err != nil {
		operationsError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data settingsDTO `json:"data"`
	}{Data: settingsView(settings)})
}

func (h *AdminHandler) updateSettings(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) {
		operationsInvalid(w, r, "settings_invalid")
		return
	}
	input, ok := decodeSettings(w, r)
	if !ok {
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	updated, err := h.service.UpdateSettings(r.Context(), principal, input)
	if err != nil {
		operationsError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data settingsDTO `json:"data"`
	}{Data: settingsView(updated)})
}

func (h *AdminHandler) listAudit(w http.ResponseWriter, r *http.Request) {
	filter, ok := auditFilterFromRequest(w, r)
	if !ok {
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	page, err := h.service.ListAudit(r.Context(), principal, filter)
	if err != nil {
		operationsError(w, r, err)
		return
	}
	items := make([]auditRecordDTO, len(page.Items))
	for i := range page.Items {
		items[i] = auditRecordView(page.Items[i])
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []auditRecordDTO `json:"data"`
		Meta struct {
			NextBeforeID int64 `json:"nextBeforeId,omitempty"`
		} `json:"meta"`
	}{
		Data: items,
		Meta: struct {
			NextBeforeID int64 `json:"nextBeforeId,omitempty"`
		}{NextBeforeID: page.NextBeforeID},
	})
}

func (h *AdminHandler) principal(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.ID == uuid.Nil || user.Role != auth.RoleAdmin || user.Status != auth.StatusActive {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
		return Principal{}, false
	}
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		operationsInvalid(w, r, "invalid_request")
		return Principal{}, false
	}
	return Principal{
		User: user, RequestID: httpx.RequestIDFromContext(r.Context()),
		IP: net.IP(addr.AsSlice()),
	}, true
}

type settingsDTO struct {
	Version                        int64     `json:"version"`
	SiteName                       string    `json:"siteName"`
	SiteAnnouncement               string    `json:"siteAnnouncement"`
	SoftDeleteRetentionDays        int       `json:"softDeleteRetentionDays"`
	AuditRetentionDays             int       `json:"auditRetentionDays"`
	OperationalSampleRetentionDays int       `json:"operationalSampleRetentionDays"`
	BackupHour                     int       `json:"backupHour"`
	BackupMinute                   int       `json:"backupMinute"`
	BackupTimezone                 string    `json:"backupTimezone"`
	DiskWarningPercent             int       `json:"diskWarningPercent"`
	DiskCriticalPercent            int       `json:"diskCriticalPercent"`
	AIErrorWarningPercent          int       `json:"aiErrorWarningPercent"`
	AIErrorCriticalPercent         int       `json:"aiErrorCriticalPercent"`
	ProcessingQueueWarning         int       `json:"processingQueueWarning"`
	ProcessingQueueCritical        int       `json:"processingQueueCritical"`
	UpdatedAt                      time.Time `json:"updatedAt"`
}

func settingsView(settings Settings) settingsDTO {
	return settingsDTO{
		Version:                        settings.Version,
		SiteName:                       settings.SiteName,
		SiteAnnouncement:               settings.SiteAnnouncement,
		SoftDeleteRetentionDays:        settings.SoftDeleteRetentionDays,
		AuditRetentionDays:             settings.AuditRetentionDays,
		OperationalSampleRetentionDays: settings.OperationalSampleRetentionDays,
		BackupHour:                     settings.BackupHour,
		BackupMinute:                   settings.BackupMinute,
		BackupTimezone:                 settings.BackupTimezone,
		DiskWarningPercent:             settings.DiskWarningPercent,
		DiskCriticalPercent:            settings.DiskCriticalPercent,
		AIErrorWarningPercent:          settings.AIErrorWarningPercent,
		AIErrorCriticalPercent:         settings.AIErrorCriticalPercent,
		ProcessingQueueWarning:         settings.ProcessingQueueWarning,
		ProcessingQueueCritical:        settings.ProcessingQueueCritical,
		UpdatedAt:                      settings.UpdatedAt.UTC(),
	}
}

type settingsRequest struct {
	Version                        *int64     `json:"version"`
	SiteName                       *string    `json:"siteName"`
	SiteAnnouncement               *string    `json:"siteAnnouncement"`
	SoftDeleteRetentionDays        *int       `json:"softDeleteRetentionDays"`
	AuditRetentionDays             *int       `json:"auditRetentionDays"`
	OperationalSampleRetentionDays *int       `json:"operationalSampleRetentionDays"`
	BackupHour                     *int       `json:"backupHour"`
	BackupMinute                   *int       `json:"backupMinute"`
	BackupTimezone                 *string    `json:"backupTimezone"`
	DiskWarningPercent             *int       `json:"diskWarningPercent"`
	DiskCriticalPercent            *int       `json:"diskCriticalPercent"`
	AIErrorWarningPercent          *int       `json:"aiErrorWarningPercent"`
	AIErrorCriticalPercent         *int       `json:"aiErrorCriticalPercent"`
	ProcessingQueueWarning         *int       `json:"processingQueueWarning"`
	ProcessingQueueCritical        *int       `json:"processingQueueCritical"`
	UpdatedAt                      *time.Time `json:"updatedAt"`
}

func decodeSettings(w http.ResponseWriter, r *http.Request) (Settings, bool) {
	var input settingsRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		operationsInvalid(w, r, "settings_invalid")
		return Settings{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		operationsInvalid(w, r, "settings_invalid")
		return Settings{}, false
	}
	if input.Version == nil || input.SiteName == nil || input.SiteAnnouncement == nil ||
		input.SoftDeleteRetentionDays == nil || input.AuditRetentionDays == nil ||
		input.OperationalSampleRetentionDays == nil || input.BackupHour == nil ||
		input.BackupMinute == nil || input.BackupTimezone == nil ||
		input.DiskWarningPercent == nil || input.DiskCriticalPercent == nil ||
		input.AIErrorWarningPercent == nil || input.AIErrorCriticalPercent == nil ||
		input.ProcessingQueueWarning == nil || input.ProcessingQueueCritical == nil {
		operationsInvalid(w, r, "settings_invalid")
		return Settings{}, false
	}
	return Settings{
		Version:                        *input.Version,
		SiteName:                       *input.SiteName,
		SiteAnnouncement:               *input.SiteAnnouncement,
		SoftDeleteRetentionDays:        *input.SoftDeleteRetentionDays,
		AuditRetentionDays:             *input.AuditRetentionDays,
		OperationalSampleRetentionDays: *input.OperationalSampleRetentionDays,
		BackupHour:                     *input.BackupHour,
		BackupMinute:                   *input.BackupMinute,
		BackupTimezone:                 *input.BackupTimezone,
		DiskWarningPercent:             *input.DiskWarningPercent,
		DiskCriticalPercent:            *input.DiskCriticalPercent,
		AIErrorWarningPercent:          *input.AIErrorWarningPercent,
		AIErrorCriticalPercent:         *input.AIErrorCriticalPercent,
		ProcessingQueueWarning:         *input.ProcessingQueueWarning,
		ProcessingQueueCritical:        *input.ProcessingQueueCritical,
	}, true
}

var auditQueryValue = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

func auditFilterFromRequest(w http.ResponseWriter, r *http.Request) (audit.AuditFilter, bool) {
	query, err := exactQuery(r, "action", "targetType", "outcome", "actorId", "from", "to", "beforeId", "limit")
	if err != nil {
		operationsInvalid(w, r, "invalid_request")
		return audit.AuditFilter{}, false
	}
	for _, key := range []string{"action", "targetType", "outcome"} {
		if value := query.Get(key); value != "" && !auditQueryValue.MatchString(value) {
			operationsInvalid(w, r, "invalid_request")
			return audit.AuditFilter{}, false
		}
	}
	filter := audit.AuditFilter{
		Action: query.Get("action"), TargetType: query.Get("targetType"),
		Outcome: query.Get("outcome"), Limit: 50,
	}
	if raw := query.Get("actorId"); raw != "" {
		filter.ActorID, err = uuid.Parse(raw)
		if err != nil || filter.ActorID == uuid.Nil || filter.ActorID.String() != raw {
			operationsInvalid(w, r, "invalid_request")
			return audit.AuditFilter{}, false
		}
	}
	if raw := query.Get("from"); raw != "" {
		filter.From, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			operationsInvalid(w, r, "invalid_request")
			return audit.AuditFilter{}, false
		}
	}
	if raw := query.Get("to"); raw != "" {
		filter.To, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			operationsInvalid(w, r, "invalid_request")
			return audit.AuditFilter{}, false
		}
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To) {
		operationsInvalid(w, r, "invalid_request")
		return audit.AuditFilter{}, false
	}
	if raw := query.Get("beforeId"); raw != "" {
		filter.BeforeID, err = canonicalPositiveInt64(raw)
		if err != nil {
			operationsInvalid(w, r, "invalid_request")
			return audit.AuditFilter{}, false
		}
	}
	if raw := query.Get("limit"); raw != "" {
		value, parseErr := canonicalPositiveInt64(raw)
		if parseErr != nil || value > 100 {
			operationsInvalid(w, r, "invalid_request")
			return audit.AuditFilter{}, false
		}
		filter.Limit = int(value)
	}
	return filter, true
}

func exactQuery(r *http.Request, allowed ...string) (url.Values, error) {
	if r.URL.ForceQuery {
		return nil, errors.New("invalid query")
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
			return nil, errors.New("invalid query")
		}
	}
	return query, nil
}

func canonicalPositiveInt64(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 || strconv.FormatInt(value, 10) != raw {
		return 0, errors.New("invalid positive integer")
	}
	return value, nil
}

func noQuery(r *http.Request) bool {
	query, err := url.ParseQuery(r.URL.RawQuery)
	return err == nil && !r.URL.ForceQuery && len(query) == 0 && r.URL.RawQuery == ""
}

var publicAuditMetadata = map[string]struct{}{
	"status": {}, "reason": {}, "version": {}, "count": {},
	"provider_id": {}, "model_id": {}, "file_purpose": {},
}

type auditRecordDTO struct {
	ID         int64          `json:"id"`
	ActorID    string         `json:"actorId,omitempty"`
	Action     string         `json:"action"`
	TargetType string         `json:"targetType"`
	TargetID   string         `json:"targetId,omitempty"`
	Metadata   map[string]any `json:"metadata"`
	OccurredAt time.Time      `json:"occurredAt"`
}

func auditRecordView(record audit.Record) auditRecordDTO {
	metadata := make(map[string]any)
	for key, value := range record.Metadata {
		if _, ok := publicAuditMetadata[key]; !ok || !publicAuditScalar(value) {
			continue
		}
		metadata[key] = value
	}
	actorID := ""
	if record.ActorUserID != uuid.Nil {
		actorID = record.ActorUserID.String()
	}
	return auditRecordDTO{
		ID: record.ID, ActorID: actorID, Action: record.Action,
		TargetType: record.TargetType, TargetID: publicAuditTargetID(record.TargetID),
		Metadata: metadata, OccurredAt: record.OccurredAt.UTC(),
	}
}

func publicAuditTargetID(raw string) string {
	if raw == "global" || raw == "unresolved" {
		return raw
	}
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		return ""
	}
	return raw
}

func publicAuditScalar(value any) bool {
	switch value.(type) {
	case string, bool, float64, json.Number:
		return true
	default:
		return false
	}
}

func operationsInvalid(w http.ResponseWriter, r *http.Request, code string) {
	httpx.Error(w, r, http.StatusBadRequest, code, "请求参数无效")
}

func operationsError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrInvalid):
		operationsInvalid(w, r, "settings_invalid")
	case errors.Is(err, audit.ErrInvalidFilter):
		operationsInvalid(w, r, "invalid_request")
	case errors.Is(err, ErrConflict):
		httpx.Error(w, r, http.StatusConflict, "settings_conflict", "设置已被其他管理员更新")
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}
