package aiqa

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

type StudentHTTPConfig struct {
	TrustedProxyCIDRs []netip.Prefix
}

type StudentHandler struct {
	service           StudentService
	events            StudentEventStore
	trusted           []netip.Prefix
	now               func() time.Time
	waiter            EventWaiter
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	writeTimeout      time.Duration
	sessionID         func(context.Context) (uuid.UUID, bool)
	connections       *streamConnections
}

func NewStudentHandler(service StudentService, events StudentEventStore) *StudentHandler {
	return NewStudentHandlerWithConfig(service, events, StudentHTTPConfig{})
}

func NewStudentHandlerWithConfig(service StudentService, events StudentEventStore, cfg StudentHTTPConfig) *StudentHandler {
	return &StudentHandler{
		service: service, events: events, trusted: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...),
		now: time.Now, waiter: timerEventWaiter{}, pollInterval: 250 * time.Millisecond,
		heartbeatInterval: 10 * time.Second, writeTimeout: 30 * time.Second,
		sessionID: auth.SessionIDFromContext, connections: newStreamConnections(),
	}
}

func (h *StudentHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore, auth.RequireRole(auth.RoleStudent))
	r.Post("/threads", h.create)
	r.Get("/threads", h.list)
	r.Get("/threads/{threadId}", h.get)
	r.Post("/threads/{threadId}/messages", h.addMessage)
	r.Get("/runs/{runId}/events", h.eventsStream)
	r.Post("/runs/{runId}/cancel", h.cancel)
	r.Post("/runs/{runId}/retries", h.retry)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { studentAIError(w, r, ErrNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不被允许")
	})
	return r
}

func (h *StudentHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.ID == uuid.Nil || user.Role != auth.RoleStudent || user.Status != auth.StatusActive {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
		return Principal{}, false
	}
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		studentAIInvalid(w, r)
		return Principal{}, false
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}, true
}

type studentAttachmentWire struct {
	FileVersionID string `json:"fileVersionId"`
	SortPosition  int    `json:"sortPosition"`
}

func parseStudentAttachments(wire []studentAttachmentWire) ([]AttachmentInput, error) {
	if len(wire) > MaxAIAttachments {
		return nil, ErrInvalidInput
	}
	out := make([]AttachmentInput, len(wire))
	for i := range wire {
		id, err := studentAICanonicalUUID(wire[i].FileVersionID)
		if err != nil {
			return nil, ErrInvalidInput
		}
		out[i] = AttachmentInput{FileVersionID: id, SortPosition: wire[i].SortPosition}
	}
	if err := validateAttachmentInputs(out); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *StudentHandler) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       string                  `json:"title"`
		Subject     Subject                 `json:"subject"`
		Body        string                  `json:"body"`
		Attachments []studentAttachmentWire `json:"attachments"`
	}
	if !decodeStudentAIJSON(w, r, &body) {
		return
	}
	key, ok := studentAIIdempotencyKey(w, r)
	if !ok {
		return
	}
	attachments, err := parseStudentAttachments(body.Attachments)
	if err != nil || !validStudentAIText(body.Body) {
		studentAIInvalid(w, r)
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	detail, run, err := h.service.CreateThread(r.Context(), actor, CreateThreadInput{
		Title: body.Title, Subject: body.Subject, Body: body.Body, IdempotencyKey: key, Attachments: attachments,
	})
	if err != nil {
		studentAIError(w, r, err)
		return
	}
	message := Message{}
	for i := range detail.Messages {
		if detail.Messages[i].ID == run.TriggerMessageID {
			message = detail.Messages[i]
			break
		}
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data struct {
			Thread    studentThreadDTO  `json:"thread"`
			Message   studentMessageDTO `json:"message"`
			Run       studentRunDTO     `json:"run"`
			EventsURL string            `json:"eventsUrl"`
		} `json:"data"`
	}{Data: struct {
		Thread    studentThreadDTO  `json:"thread"`
		Message   studentMessageDTO `json:"message"`
		Run       studentRunDTO     `json:"run"`
		EventsURL string            `json:"eventsUrl"`
	}{
		Thread: studentThreadView(detail.Thread), Message: studentMessageView(message), Run: studentRunView(run),
		EventsURL: "/api/v1/student/ai/runs/" + run.ID.String() + "/events",
	}})
}

func (h *StudentHandler) list(w http.ResponseWriter, r *http.Request) {
	query, err := studentAIQuery(r, "cursor", "limit")
	if err != nil {
		studentAIInvalid(w, r)
		return
	}
	if explicitEmptyAIQuery(query, "cursor", "limit") {
		studentAIInvalid(w, r)
		return
	}
	limit, err := studentAILimit(query.Get("limit"))
	if err != nil {
		studentAIInvalid(w, r)
		return
	}
	cursor, err := decodeAIThreadCursor(query.Get("cursor"), h.now())
	if err != nil {
		studentAIInvalid(w, r)
		return
	}
	cursor.Limit = limit
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	threads, next, err := h.service.ListThreads(r.Context(), actor, cursor)
	if err != nil {
		studentAIError(w, r, err)
		return
	}
	data := make([]studentThreadDTO, len(threads))
	for i := range threads {
		data[i] = studentThreadView(threads[i])
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []studentThreadDTO `json:"data"`
		Meta struct {
			NextCursor string `json:"nextCursor,omitempty"`
		} `json:"meta"`
	}{Data: data, Meta: struct {
		NextCursor string `json:"nextCursor,omitempty"`
	}{NextCursor: encodeAIThreadCursor(next)}})
}

func (h *StudentHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := studentAICanonicalUUID(chi.URLParam(r, "threadId"))
	if err != nil {
		studentAIError(w, r, ErrNotFound)
		return
	}
	query, err := studentAIQuery(r, "cursor", "limit")
	if err != nil {
		studentAIInvalid(w, r)
		return
	}
	if explicitEmptyAIQuery(query, "cursor", "limit") {
		studentAIInvalid(w, r)
		return
	}
	limit, err := studentAILimit(query.Get("limit"))
	if err != nil {
		studentAIInvalid(w, r)
		return
	}
	cursor, err := decodeAIMessageCursor(query.Get("cursor"), h.now())
	if err != nil {
		studentAIInvalid(w, r)
		return
	}
	cursor.Limit = limit
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	detail, err := h.service.GetThread(r.Context(), actor, id, cursor)
	if err != nil {
		studentAIError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data studentThreadDetailDTO `json:"data"`
	}{Data: studentDetailView(detail)})
}

func (h *StudentHandler) addMessage(w http.ResponseWriter, r *http.Request) {
	threadID, err := studentAICanonicalUUID(chi.URLParam(r, "threadId"))
	if err != nil {
		studentAIError(w, r, ErrNotFound)
		return
	}
	var body struct {
		Body        string                  `json:"body"`
		Attachments []studentAttachmentWire `json:"attachments"`
	}
	if !decodeStudentAIJSON(w, r, &body) {
		return
	}
	key, ok := studentAIIdempotencyKey(w, r)
	if !ok {
		return
	}
	attachments, err := parseStudentAttachments(body.Attachments)
	if err != nil || !validStudentAIText(body.Body) {
		studentAIInvalid(w, r)
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	detail, run, err := h.service.AddMessage(r.Context(), actor, AddMessageInput{
		ThreadID: threadID, Body: body.Body, IdempotencyKey: key, Attachments: attachments,
	})
	if err != nil {
		studentAIError(w, r, err)
		return
	}
	message := Message{}
	for i := range detail.Messages {
		if detail.Messages[i].ID == run.TriggerMessageID {
			message = detail.Messages[i]
		}
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data struct {
			Message   studentMessageDTO `json:"message"`
			Run       studentRunDTO     `json:"run"`
			EventsURL string            `json:"eventsUrl"`
		} `json:"data"`
	}{Data: struct {
		Message   studentMessageDTO `json:"message"`
		Run       studentRunDTO     `json:"run"`
		EventsURL string            `json:"eventsUrl"`
	}{studentMessageView(message), studentRunView(run), "/api/v1/student/ai/runs/" + run.ID.String() + "/events"}})
}

func (h *StudentHandler) cancel(w http.ResponseWriter, r *http.Request) {
	id, err := studentAICanonicalUUID(chi.URLParam(r, "runId"))
	if err != nil {
		studentAIError(w, r, ErrNotFound)
		return
	}
	var body struct{}
	if !decodeStudentAIJSON(w, r, &body) {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	run, err := h.service.CancelRun(r.Context(), actor, id)
	if err != nil {
		studentAIError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data studentRunDTO `json:"data"`
	}{Data: studentRunView(run)})
}

func (h *StudentHandler) retry(w http.ResponseWriter, r *http.Request) {
	id, err := studentAICanonicalUUID(chi.URLParam(r, "runId"))
	if err != nil {
		studentAIError(w, r, ErrNotFound)
		return
	}
	var body struct{}
	if !decodeStudentAIJSON(w, r, &body) {
		return
	}
	key, ok := studentAIIdempotencyKey(w, r)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	run, err := h.service.RetryRun(r.Context(), actor, id, key)
	if err != nil {
		studentAIError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data struct {
			Run       studentRunDTO `json:"run"`
			EventsURL string        `json:"eventsUrl"`
		} `json:"data"`
	}{Data: struct {
		Run       studentRunDTO `json:"run"`
		EventsURL string        `json:"eventsUrl"`
	}{studentRunView(run), "/api/v1/student/ai/runs/" + run.ID.String() + "/events"}})
}
