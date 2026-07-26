package aiqa

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

type AdminUsageHandler struct {
	service AdminUsageService
	trusted []netip.Prefix
	now     func() time.Time
}

func NewAdminUsageHandler(service AdminUsageService, trusted []netip.Prefix) *AdminUsageHandler {
	return &AdminUsageHandler{service: service, trusted: append([]netip.Prefix(nil), trusted...), now: time.Now}
}

func (h *AdminUsageHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore, auth.RequireRole(auth.RoleAdmin))
	r.Get("/summary", h.summary)
	r.Get("/runs", h.runs)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不被允许")
	})
	return r
}

func (h *AdminUsageHandler) summary(w http.ResponseWriter, r *http.Request) {
	p, filter, ok := h.request(w, r)
	if !ok {
		return
	}
	result, err := h.service.UsageSummary(r.Context(), p, filter)
	if err != nil {
		adminUsageError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data usageSummaryDTO `json:"data"`
	}{Data: usageSummaryView(result)})
}

func (h *AdminUsageHandler) runs(w http.ResponseWriter, r *http.Request) {
	p, filter, ok := h.request(w, r)
	if !ok {
		return
	}
	items, next, err := h.service.UsageRuns(r.Context(), p, filter)
	if err != nil {
		adminUsageError(w, r, err)
		return
	}
	data := make([]usageRunDTO, len(items))
	for i := range items {
		data[i] = usageRunView(items[i])
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []usageRunDTO `json:"data"`
		Meta struct {
			NextCursor string `json:"nextCursor,omitempty"`
		} `json:"meta"`
	}{Data: data, Meta: struct {
		NextCursor string `json:"nextCursor,omitempty"`
	}{NextCursor: encodeUsageCursor(next)}})
}

func (h *AdminUsageHandler) request(w http.ResponseWriter, r *http.Request) (Principal, UsageFilter, bool) {
	query, err := studentAIQuery(r, "studentId", "modelId", "status", "from", "to", "cursor", "limit")
	if err != nil || explicitEmptyAIQuery(query, "studentId", "modelId", "status", "from", "to", "cursor", "limit") {
		studentAIInvalid(w, r)
		return Principal{}, UsageFilter{}, false
	}
	limit, err := studentAILimit(query.Get("limit"))
	if err != nil {
		studentAIInvalid(w, r)
		return Principal{}, UsageFilter{}, false
	}
	var studentID, modelID uuid.UUID
	if raw := query.Get("studentId"); raw != "" {
		studentID, err = studentAICanonicalUUID(raw)
		if err != nil {
			studentAIInvalid(w, r)
			return Principal{}, UsageFilter{}, false
		}
	}
	if raw := query.Get("modelId"); raw != "" {
		modelID, err = studentAICanonicalUUID(raw)
		if err != nil {
			studentAIInvalid(w, r)
			return Principal{}, UsageFilter{}, false
		}
	}
	from, err := validUsageTime(query.Get("from"))
	if err != nil {
		studentAIInvalid(w, r)
		return Principal{}, UsageFilter{}, false
	}
	to, err := validUsageTime(query.Get("to"))
	if err != nil {
		studentAIInvalid(w, r)
		return Principal{}, UsageFilter{}, false
	}
	cursor, err := decodeUsageCursor(query.Get("cursor"), h.now())
	if err != nil {
		studentAIInvalid(w, r)
		return Principal{}, UsageFilter{}, false
	}
	filter := UsageFilter{
		StudentID: studentID, ModelID: modelID, Status: RunStatus(query.Get("status")),
		From: from, To: to, Cursor: cursor, Limit: limit,
	}
	if err = validateUsageFilter(filter); err != nil {
		studentAIInvalid(w, r)
		return Principal{}, UsageFilter{}, false
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.ID == uuid.Nil || user.Role != auth.RoleAdmin || user.Status != auth.StatusActive {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
		return Principal{}, UsageFilter{}, false
	}
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		studentAIInvalid(w, r)
		return Principal{}, UsageFilter{}, false
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}, filter, true
}

func adminUsageError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrInvalidInput) {
		studentAIInvalid(w, r)
		return
	}
	if errors.Is(err, ErrForbidden) {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
		return
	}
	httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "服务暂不可用")
}

type usageSummaryDTO struct {
	Requests           int64  `json:"requests"`
	Succeeded          int64  `json:"succeeded"`
	Failed             int64  `json:"failed"`
	InputTokens        int64  `json:"inputTokens"`
	OutputTokens       int64  `json:"outputTokens"`
	CostMicroUSD       string `json:"costMicroUSD"`
	UnknownUsage       int64  `json:"unknownUsage"`
	AverageFirstByteMS int64  `json:"averageFirstByteMs"`
	AverageTotalMS     int64  `json:"averageTotalMs"`
}

func usageSummaryView(v UsageSummary) usageSummaryDTO {
	return usageSummaryDTO{
		Requests: v.Requests, Succeeded: v.Succeeded, Failed: v.Failed,
		InputTokens: v.InputTokens, OutputTokens: v.OutputTokens,
		CostMicroUSD: strconv.FormatInt(v.CostMicroUSD, 10), UnknownUsage: v.UnknownUsage,
		AverageFirstByteMS: v.AverageFirstByteMS, AverageTotalMS: v.AverageTotalMS,
	}
}

type usageRunDTO struct {
	ID                 uuid.UUID  `json:"id"`
	StudentID          uuid.UUID  `json:"studentId"`
	StudentUsername    string     `json:"studentUsername"`
	StudentDisplayName string     `json:"studentDisplayName"`
	ModelID            uuid.UUID  `json:"modelId"`
	ModelLabel         string     `json:"modelLabel"`
	Status             RunStatus  `json:"status"`
	InputTokens        int64      `json:"inputTokens"`
	OutputTokens       int64      `json:"outputTokens"`
	UsageSource        string     `json:"usageSource"`
	CostMicroUSD       string     `json:"costMicroUSD"`
	FirstByteMS        *int64     `json:"firstByteMs,omitempty"`
	TotalMS            *int64     `json:"totalMs,omitempty"`
	ErrorCategory      string     `json:"errorCategory,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	StartedAt          *time.Time `json:"startedAt,omitempty"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
}

func usageRunView(v UsageRun) usageRunDTO {
	return usageRunDTO{
		ID: v.ID, StudentID: v.StudentID, StudentUsername: v.StudentUsername, StudentDisplayName: v.StudentDisplayName,
		ModelID: v.ModelID, ModelLabel: v.ModelLabel, Status: v.Status,
		InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, UsageSource: v.UsageSource,
		CostMicroUSD: strconv.FormatInt(v.CostMicroUSD, 10), FirstByteMS: v.FirstByteMS,
		TotalMS: v.TotalMS, ErrorCategory: v.ErrorCategory, CreatedAt: v.CreatedAt,
		StartedAt: v.StartedAt, CompletedAt: v.CompletedAt,
	}
}
