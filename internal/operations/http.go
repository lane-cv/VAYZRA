package operations

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
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
	router.Get("/dashboard", h.dashboard)
	router.Get("/alerts", h.listAlerts)
	router.Post("/alerts/{id}/acknowledge", h.acknowledgeAlert)
	router.Post("/webhook-test", h.testWebhook)
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不被允许")
	})
	return router
}

func (h *AdminHandler) testWebhook(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) || !emptyRequestBody(w, r) {
		operationsInvalid(w, r, "invalid_request")
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	service, ok := h.service.(WebhookTestHTTPService)
	if !ok {
		httpx.Error(
			w,
			r,
			http.StatusInternalServerError,
			"internal_error",
			"服务暂不可用",
		)
		return
	}
	if err := service.TestWebhook(r.Context(), principal); err != nil {
		webhookTestOperationsError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}{
		Data: struct {
			Status string `json:"status"`
		}{Status: "delivered"},
	})
}

func webhookTestOperationsError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrInvalid):
		operationsInvalid(w, r, "invalid_request")
	case errors.Is(err, ErrWebhookNotConfigured):
		httpx.Error(
			w,
			r,
			http.StatusConflict,
			"webhook_not_configured",
			"Webhook 尚未配置",
		)
	case errors.Is(err, ErrWebhookDeliveryFailed):
		httpx.Error(
			w,
			r,
			http.StatusBadGateway,
			"webhook_delivery_failed",
			"Webhook 投递失败",
		)
	default:
		httpx.Error(
			w,
			r,
			http.StatusInternalServerError,
			"internal_error",
			"服务暂不可用",
		)
	}
}

func (h *AdminHandler) listAlerts(w http.ResponseWriter, r *http.Request) {
	filter, ok := alertFilterFromRequest(w, r)
	if !ok {
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	service, ok := h.service.(AlertHTTPService)
	if !ok {
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
		return
	}
	page, err := service.ListAlerts(r.Context(), principal, filter)
	if err != nil {
		alertOperationsError(w, r, err)
		return
	}
	items := make([]alertDTO, len(page.Items))
	for index := range page.Items {
		items[index] = alertView(page.Items[index])
	}
	next := ""
	if page.Next != nil {
		next = encodeAlertCursor(*page.Next)
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []alertDTO `json:"data"`
		Meta struct {
			Next string `json:"next,omitempty"`
		} `json:"meta"`
	}{
		Data: items,
		Meta: struct {
			Next string `json:"next,omitempty"`
		}{Next: next},
	})
}

func (h *AdminHandler) acknowledgeAlert(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) || !emptyRequestBody(w, r) {
		operationsInvalid(w, r, "invalid_request")
		return
	}
	rawID := chi.URLParam(r, "id")
	id, err := uuid.Parse(rawID)
	if err != nil || id == uuid.Nil || id.String() != rawID {
		operationsInvalid(w, r, "invalid_request")
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	service, ok := h.service.(AlertHTTPService)
	if !ok {
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
		return
	}
	alert, err := service.AcknowledgeAlert(r.Context(), principal, id)
	if err != nil {
		alertOperationsError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data alertDTO `json:"data"`
	}{Data: alertView(alert)})
}

func emptyRequestBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength > 0 {
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	return err == nil && len(raw) == 0
}

type alertDTO struct {
	ID              string        `json:"id"`
	DedupeKey       string        `json:"dedupeKey"`
	Category        string        `json:"category"`
	Severity        AlertSeverity `json:"severity"`
	State           AlertState    `json:"state"`
	FirstObservedAt time.Time     `json:"firstObservedAt"`
	LastObservedAt  time.Time     `json:"lastObservedAt"`
	AcknowledgedBy  string        `json:"acknowledgedBy,omitempty"`
	AcknowledgedAt  *time.Time    `json:"acknowledgedAt,omitempty"`
	ResolvedAt      *time.Time    `json:"resolvedAt,omitempty"`
	CurrentValue    float64       `json:"currentValue"`
	ThresholdValue  float64       `json:"thresholdValue"`
	Summary         string        `json:"summary"`
}

func alertView(alert Alert) alertDTO {
	acknowledgedBy := ""
	if alert.AcknowledgedBy != uuid.Nil {
		acknowledgedBy = alert.AcknowledgedBy.String()
	}
	return alertDTO{
		ID: alert.ID.String(), DedupeKey: alert.DedupeKey,
		Category: alert.Category, Severity: alert.Severity, State: alert.State,
		FirstObservedAt: alert.FirstObservedAt.UTC(),
		LastObservedAt:  alert.LastObservedAt.UTC(),
		AcknowledgedBy:  acknowledgedBy,
		AcknowledgedAt:  utcAlertTime(alert.AcknowledgedAt),
		ResolvedAt:      utcAlertTime(alert.ResolvedAt),
		CurrentValue:    alert.CurrentValue,
		ThresholdValue:  alert.ThresholdValue,
		Summary:         alert.Summary,
	}
}

func alertFilterFromRequest(w http.ResponseWriter, r *http.Request) (AlertFilter, bool) {
	query, err := exactQuery(r, "state", "severity", "category", "before", "limit")
	if err != nil {
		operationsInvalid(w, r, "invalid_request")
		return AlertFilter{}, false
	}
	filter := AlertFilter{
		State:    AlertState(query.Get("state")),
		Severity: AlertSeverity(query.Get("severity")),
		Category: query.Get("category"),
		Limit:    50,
	}
	if raw := query.Get("limit"); raw != "" {
		value, parseErr := canonicalPositiveInt64(raw)
		if parseErr != nil || value > 100 {
			operationsInvalid(w, r, "invalid_request")
			return AlertFilter{}, false
		}
		filter.Limit = int(value)
	}
	if raw := query.Get("before"); raw != "" {
		cursor, parseErr := decodeAlertCursor(raw)
		if parseErr != nil {
			operationsInvalid(w, r, "invalid_request")
			return AlertFilter{}, false
		}
		filter.Before = &cursor
	}
	if validateAlertFilter(filter) != nil {
		operationsInvalid(w, r, "invalid_request")
		return AlertFilter{}, false
	}
	return filter, true
}

func encodeAlertCursor(cursor AlertCursor) string {
	payload := cursor.LastObservedAt.UTC().Format(time.RFC3339Nano) +
		"\n" + cursor.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func decodeAlertCursor(raw string) (AlertCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) > 128 {
		return AlertCursor{}, ErrInvalid
	}
	parts := strings.Split(string(decoded), "\n")
	if len(parts) != 2 {
		return AlertCursor{}, ErrInvalid
	}
	observedAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil || observedAt.IsZero() {
		return AlertCursor{}, ErrInvalid
	}
	id, err := uuid.Parse(parts[1])
	if err != nil || id == uuid.Nil || id.String() != parts[1] {
		return AlertCursor{}, ErrInvalid
	}
	cursor := AlertCursor{LastObservedAt: observedAt.UTC(), ID: id}
	if encodeAlertCursor(cursor) != raw {
		return AlertCursor{}, ErrInvalid
	}
	return cursor, nil
}

func alertOperationsError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
	case errors.Is(err, ErrInvalid):
		operationsInvalid(w, r, "invalid_request")
	case errors.Is(err, ErrAlertNotFound):
		httpx.Error(w, r, http.StatusNotFound, "alert_not_found", "告警不存在")
	case errors.Is(err, ErrAlertAlreadyResolved):
		httpx.Error(w, r, http.StatusConflict, "alert_already_resolved", "告警已解决")
	default:
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
	}
}

func (h *AdminHandler) dashboard(w http.ResponseWriter, r *http.Request) {
	if !noQuery(r) {
		operationsInvalid(w, r, "invalid_request")
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	dashboard, err := h.service.GetDashboard(r.Context(), principal)
	if err != nil {
		operationsError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data Dashboard `json:"data"`
	}{Data: dashboard})
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
	Version                          int64     `json:"version"`
	SiteName                         string    `json:"siteName"`
	SiteAnnouncement                 string    `json:"siteAnnouncement"`
	SoftDeleteRetentionDays          int       `json:"softDeleteRetentionDays"`
	AuditRetentionDays               int       `json:"auditRetentionDays"`
	OperationalSampleRetentionDays   int       `json:"operationalSampleRetentionDays"`
	BackupHour                       int       `json:"backupHour"`
	BackupMinute                     int       `json:"backupMinute"`
	BackupTimezone                   string    `json:"backupTimezone"`
	DiskWarningPercent               int       `json:"diskWarningPercent"`
	DiskCriticalPercent              int       `json:"diskCriticalPercent"`
	BackupFilesystemWarningPercent   int       `json:"backupFilesystemWarningPercent"`
	BackupFilesystemCriticalPercent  int       `json:"backupFilesystemCriticalPercent"`
	LocalBackupAgeWarningHours       int       `json:"localBackupAgeWarningHours"`
	LocalBackupAgeCriticalHours      int       `json:"localBackupAgeCriticalHours"`
	AIErrorWarningPercent            int       `json:"aiErrorWarningPercent"`
	AIErrorCriticalPercent           int       `json:"aiErrorCriticalPercent"`
	ProcessingQueueWarning           int       `json:"processingQueueWarning"`
	ProcessingQueueCritical          int       `json:"processingQueueCritical"`
	ProcessingFailureWarningCount    int       `json:"processingFailureWarningCount"`
	ProcessingFailureCriticalCount   int       `json:"processingFailureCriticalCount"`
	LoginFailureWarningCount         int       `json:"loginFailureWarningCount"`
	LoginFailureCriticalCount        int       `json:"loginFailureCriticalCount"`
	AuthorizationDenialWarningCount  int       `json:"authorizationDenialWarningCount"`
	AuthorizationDenialCriticalCount int       `json:"authorizationDenialCriticalCount"`
	Infrastructure                   []infrastructureStatusDTO `json:"infrastructure"`
	UpdatedAt                        time.Time                 `json:"updatedAt"`
}

type infrastructureStatusDTO struct {
	Key             string     `json:"key"`
	Configured      bool       `json:"configured"`
	LastValidatedAt *time.Time `json:"lastValidatedAt"`
}

func settingsView(settings Settings) settingsDTO {
	statuses := NormalizeInfrastructureStatuses(settings.Infrastructure)
	infrastructure := make([]infrastructureStatusDTO, len(statuses))
	for index, status := range statuses {
		infrastructure[index] = infrastructureStatusDTO{
			Key:             infrastructureStorageKey(status.Key),
			Configured:      status.Configured,
			LastValidatedAt: status.LastValidatedAt,
		}
	}
	return settingsDTO{
		Version:                          settings.Version,
		SiteName:                         settings.SiteName,
		SiteAnnouncement:                 settings.SiteAnnouncement,
		SoftDeleteRetentionDays:          settings.SoftDeleteRetentionDays,
		AuditRetentionDays:               settings.AuditRetentionDays,
		OperationalSampleRetentionDays:   settings.OperationalSampleRetentionDays,
		BackupHour:                       settings.BackupHour,
		BackupMinute:                     settings.BackupMinute,
		BackupTimezone:                   settings.BackupTimezone,
		DiskWarningPercent:               settings.DiskWarningPercent,
		DiskCriticalPercent:              settings.DiskCriticalPercent,
		BackupFilesystemWarningPercent:   settings.BackupFilesystemWarningPercent,
		BackupFilesystemCriticalPercent:  settings.BackupFilesystemCriticalPercent,
		LocalBackupAgeWarningHours:       settings.LocalBackupAgeWarningHours,
		LocalBackupAgeCriticalHours:      settings.LocalBackupAgeCriticalHours,
		AIErrorWarningPercent:            settings.AIErrorWarningPercent,
		AIErrorCriticalPercent:           settings.AIErrorCriticalPercent,
		ProcessingQueueWarning:           settings.ProcessingQueueWarning,
		ProcessingQueueCritical:          settings.ProcessingQueueCritical,
		ProcessingFailureWarningCount:    settings.ProcessingFailureWarningCount,
		ProcessingFailureCriticalCount:   settings.ProcessingFailureCriticalCount,
		LoginFailureWarningCount:         settings.LoginFailureWarningCount,
		LoginFailureCriticalCount:        settings.LoginFailureCriticalCount,
		AuthorizationDenialWarningCount:  settings.AuthorizationDenialWarningCount,
		AuthorizationDenialCriticalCount: settings.AuthorizationDenialCriticalCount,
		Infrastructure:                   infrastructure,
		UpdatedAt:                        settings.UpdatedAt.UTC(),
	}
}

type settingsRequest struct {
	Version                          *int64          `json:"version"`
	SiteName                         *string         `json:"siteName"`
	SiteAnnouncement                 *string         `json:"siteAnnouncement"`
	SoftDeleteRetentionDays          *int            `json:"softDeleteRetentionDays"`
	AuditRetentionDays               *int            `json:"auditRetentionDays"`
	OperationalSampleRetentionDays   *int            `json:"operationalSampleRetentionDays"`
	BackupHour                       *int            `json:"backupHour"`
	BackupMinute                     *int            `json:"backupMinute"`
	BackupTimezone                   *string         `json:"backupTimezone"`
	DiskWarningPercent               *int            `json:"diskWarningPercent"`
	DiskCriticalPercent              *int            `json:"diskCriticalPercent"`
	BackupFilesystemWarningPercent   *int            `json:"backupFilesystemWarningPercent"`
	BackupFilesystemCriticalPercent  *int            `json:"backupFilesystemCriticalPercent"`
	LocalBackupAgeWarningHours       *int            `json:"localBackupAgeWarningHours"`
	LocalBackupAgeCriticalHours      *int            `json:"localBackupAgeCriticalHours"`
	AIErrorWarningPercent            *int            `json:"aiErrorWarningPercent"`
	AIErrorCriticalPercent           *int            `json:"aiErrorCriticalPercent"`
	ProcessingQueueWarning           *int            `json:"processingQueueWarning"`
	ProcessingQueueCritical          *int            `json:"processingQueueCritical"`
	ProcessingFailureWarningCount    *int            `json:"processingFailureWarningCount"`
	ProcessingFailureCriticalCount   *int            `json:"processingFailureCriticalCount"`
	LoginFailureWarningCount         *int            `json:"loginFailureWarningCount"`
	LoginFailureCriticalCount        *int            `json:"loginFailureCriticalCount"`
	AuthorizationDenialWarningCount  *int            `json:"authorizationDenialWarningCount"`
	AuthorizationDenialCriticalCount *int            `json:"authorizationDenialCriticalCount"`
}

var settingsJSONFields = map[string]struct{}{
	"version":                          {},
	"siteName":                         {},
	"siteAnnouncement":                 {},
	"softDeleteRetentionDays":          {},
	"auditRetentionDays":               {},
	"operationalSampleRetentionDays":   {},
	"backupHour":                       {},
	"backupMinute":                     {},
	"backupTimezone":                   {},
	"diskWarningPercent":               {},
	"diskCriticalPercent":              {},
	"backupFilesystemWarningPercent":   {},
	"backupFilesystemCriticalPercent":  {},
	"localBackupAgeWarningHours":       {},
	"localBackupAgeCriticalHours":      {},
	"aiErrorWarningPercent":            {},
	"aiErrorCriticalPercent":           {},
	"processingQueueWarning":           {},
	"processingQueueCritical":          {},
	"processingFailureWarningCount":    {},
	"processingFailureCriticalCount":   {},
	"loginFailureWarningCount":         {},
	"loginFailureCriticalCount":        {},
	"authorizationDenialWarningCount":  {},
	"authorizationDenialCriticalCount": {},
}

func decodeSettings(w http.ResponseWriter, r *http.Request) (Settings, bool) {
	if !settingsJSONContentType(w, r) {
		return Settings{}, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.Error(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "请求体过大")
		} else {
			operationsInvalid(w, r, "settings_invalid")
		}
		return Settings{}, false
	}
	if !utf8.Valid(raw) || uniqueJSONObject(raw) != nil {
		operationsInvalid(w, r, "settings_invalid")
		return Settings{}, false
	}
	var input settingsRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		operationsInvalid(w, r, "settings_invalid")
		return Settings{}, false
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		operationsInvalid(w, r, "settings_invalid")
		return Settings{}, false
	}
	if input.Version == nil || input.SiteName == nil || input.SiteAnnouncement == nil ||
		input.SoftDeleteRetentionDays == nil || input.AuditRetentionDays == nil ||
		input.OperationalSampleRetentionDays == nil || input.BackupHour == nil ||
		input.BackupMinute == nil || input.BackupTimezone == nil ||
		input.DiskWarningPercent == nil || input.DiskCriticalPercent == nil ||
		input.BackupFilesystemWarningPercent == nil || input.BackupFilesystemCriticalPercent == nil ||
		input.LocalBackupAgeWarningHours == nil || input.LocalBackupAgeCriticalHours == nil ||
		input.AIErrorWarningPercent == nil || input.AIErrorCriticalPercent == nil ||
		input.ProcessingQueueWarning == nil || input.ProcessingQueueCritical == nil ||
		input.ProcessingFailureWarningCount == nil || input.ProcessingFailureCriticalCount == nil ||
		input.LoginFailureWarningCount == nil || input.LoginFailureCriticalCount == nil ||
		input.AuthorizationDenialWarningCount == nil || input.AuthorizationDenialCriticalCount == nil {
		operationsInvalid(w, r, "settings_invalid")
		return Settings{}, false
	}
	return Settings{
		Version:                          *input.Version,
		SiteName:                         *input.SiteName,
		SiteAnnouncement:                 *input.SiteAnnouncement,
		SoftDeleteRetentionDays:          *input.SoftDeleteRetentionDays,
		AuditRetentionDays:               *input.AuditRetentionDays,
		OperationalSampleRetentionDays:   *input.OperationalSampleRetentionDays,
		BackupHour:                       *input.BackupHour,
		BackupMinute:                     *input.BackupMinute,
		BackupTimezone:                   *input.BackupTimezone,
		DiskWarningPercent:               *input.DiskWarningPercent,
		DiskCriticalPercent:              *input.DiskCriticalPercent,
		BackupFilesystemWarningPercent:   *input.BackupFilesystemWarningPercent,
		BackupFilesystemCriticalPercent:  *input.BackupFilesystemCriticalPercent,
		LocalBackupAgeWarningHours:       *input.LocalBackupAgeWarningHours,
		LocalBackupAgeCriticalHours:      *input.LocalBackupAgeCriticalHours,
		AIErrorWarningPercent:            *input.AIErrorWarningPercent,
		AIErrorCriticalPercent:           *input.AIErrorCriticalPercent,
		ProcessingQueueWarning:           *input.ProcessingQueueWarning,
		ProcessingQueueCritical:          *input.ProcessingQueueCritical,
		ProcessingFailureWarningCount:    *input.ProcessingFailureWarningCount,
		ProcessingFailureCriticalCount:   *input.ProcessingFailureCriticalCount,
		LoginFailureWarningCount:         *input.LoginFailureWarningCount,
		LoginFailureCriticalCount:        *input.LoginFailureCriticalCount,
		AuthorizationDenialWarningCount:  *input.AuthorizationDenialWarningCount,
		AuthorizationDenialCriticalCount: *input.AuthorizationDenialCriticalCount,
	}, true
}

func settingsJSONContentType(w http.ResponseWriter, r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 application/json")
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" ||
		len(parameters) > 1 ||
		(len(parameters) == 1 && !strings.EqualFold(parameters["charset"], "utf-8")) {
		httpx.Error(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "仅支持 application/json")
		return false
	}
	return true
}

func uniqueJSONObject(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	open, ok := token.(json.Delim)
	if !ok || open != '{' {
		return errors.New("settings body must be an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("settings member name must be a string")
		}
		if _, allowed := settingsJSONFields[key]; !allowed {
			return errors.New("unknown settings member")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate settings member")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err = decoder.Decode(&value); err != nil {
			return err
		}
	}
	token, err = decoder.Token()
	if err != nil {
		return err
	}
	close, ok := token.(json.Delim)
	if !ok || close != '}' {
		return errors.New("settings body must end with an object")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("settings body has trailing data")
		}
		return err
	}
	return nil
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
	if outcome := query.Get("outcome"); outcome != "" && !audit.IsValidOutcome(outcome) {
		operationsInvalid(w, r, "invalid_request")
		return audit.AuditFilter{}, false
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
		if err != nil || filter.From.IsZero() {
			operationsInvalid(w, r, "invalid_request")
			return audit.AuditFilter{}, false
		}
	}
	if raw := query.Get("to"); raw != "" {
		filter.To, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil || filter.To.IsZero() {
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
		if _, ok := publicAuditMetadata[key]; !ok {
			continue
		}
		normalized, ok := publicAuditMetadataValue(key, value)
		if ok {
			metadata[key] = normalized
		}
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

var publicAuditStatuses = map[string]struct{}{
	"active": {}, "disabled": {},
	"normal": {}, "draining": {}, "backup": {}, "release": {},
	"queued": {}, "snapshotting": {}, "encrypting": {}, "verifying": {},
	"syncing": {}, "succeeded": {}, "degraded": {}, "failed": {},
	"restoring": {}, "checking": {},
	"open": {}, "acknowledged": {}, "resolved": {},
}

var publicAuditReasons = map[string]struct{}{
	"retention": {}, "backup_schedule": {}, "threshold": {},
	"malformed_id": {}, "unexpected_query": {},
	"invalid_actor": {}, "invalid_ip": {},
}

var publicAuditFilePurposes = map[string]struct{}{
	"teaching": {}, "qa_attachment": {}, "ai_attachment": {},
}

const (
	maxPublicAuditCount   = uint64(1_000_000_000)
	maxSafeJSONInteger    = float64(9_007_199_254_740_991)
	maxPublicAuditVersion = uint64(math.MaxInt64)
)

func publicAuditMetadataValue(key string, value any) (any, bool) {
	switch key {
	case "status":
		return publicAuditEnum(value, publicAuditStatuses)
	case "reason":
		return publicAuditEnum(value, publicAuditReasons)
	case "version":
		return publicAuditInteger(value, 1, maxPublicAuditVersion)
	case "count":
		return publicAuditInteger(value, 0, maxPublicAuditCount)
	case "provider_id", "model_id":
		raw, ok := value.(string)
		if !ok {
			return nil, false
		}
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil || id.String() != raw {
			return nil, false
		}
		return raw, true
	case "file_purpose":
		return publicAuditEnum(value, publicAuditFilePurposes)
	default:
		return nil, false
	}
}

func publicAuditEnum(value any, allowed map[string]struct{}) (any, bool) {
	raw, ok := value.(string)
	if !ok {
		return nil, false
	}
	if _, ok := allowed[raw]; !ok {
		return nil, false
	}
	return raw, true
}

func publicAuditInteger(value any, minimum, maximum uint64) (any, bool) {
	switch typed := value.(type) {
	case string:
		parsed, ok := canonicalPublicAuditInteger(typed, minimum, maximum)
		if !ok {
			return nil, false
		}
		return strconv.FormatUint(parsed, 10), true
	case json.Number:
		parsed, ok := canonicalPublicAuditInteger(string(typed), minimum, maximum)
		if !ok {
			return nil, false
		}
		return json.Number(strconv.FormatUint(parsed, 10)), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) ||
			typed < float64(minimum) || typed > float64(maximum) ||
			(typed == 0 && math.Signbit(typed)) ||
			typed > maxSafeJSONInteger || math.Trunc(typed) != typed {
			return nil, false
		}
		return typed, true
	default:
		return nil, false
	}
}

func canonicalPublicAuditInteger(raw string, minimum, maximum uint64) (uint64, bool) {
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum ||
		strconv.FormatUint(parsed, 10) != raw {
		return 0, false
	}
	return parsed, true
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
