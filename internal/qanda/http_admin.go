package qanda

import (
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

type AdminHTTPConfig struct{ TrustedProxyCIDRs []netip.Prefix }
type AdminHandler struct {
	service           AdminHTTPService
	trustedProxyCIDRs []netip.Prefix
	now               func() time.Time
}

func NewAdminHandler(service AdminHTTPService) *AdminHandler {
	return NewAdminHandlerWithConfig(service, AdminHTTPConfig{})
}
func NewAdminHandlerWithConfig(service AdminHTTPService, cfg AdminHTTPConfig) *AdminHandler {
	return &AdminHandler{service: service, trustedProxyCIDRs: append([]netip.Prefix(nil), cfg.TrustedProxyCIDRs...), now: time.Now}
}
func (h *AdminHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore)
	r.Use(auth.RequireRole(auth.RoleAdmin))
	r.Get("/", h.List)
	r.Get("/{id}", h.Get)
	r.Post("/{id}/messages", h.AddMessage)
	r.Post("/{id}/status", h.ChangeStatus)
	r.Post("/{id}/notes", h.AddNote)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不被允许")
	})
	return r
}

type adminThreadDTO struct {
	threadDTO
	StudentID uuid.UUID `json:"studentId"`
}
type teacherNoteDTO struct {
	ID           uuid.UUID `json:"id"`
	AuthorUserID uuid.UUID `json:"authorUserId"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"createdAt"`
}

func adminThreadView(t Thread) adminThreadDTO {
	return adminThreadDTO{threadDTO: threadView(t), StudentID: t.StudentID}
}

func (h *AdminHandler) List(w http.ResponseWriter, r *http.Request) {
	q, ok := qandaQuery(w, r, "status", "studentId", "from", "to", "cursor", "limit")
	if !ok {
		return
	}
	status, _, ok := qandaSingleQuery(w, r, q, "status")
	if !ok || !validStatusFilter(Status(status)) {
		if ok {
			qandaBad(w, r)
		}
		return
	}
	studentRaw, present, ok := qandaSingleQuery(w, r, q, "studentId")
	if !ok {
		return
	}
	var student uuid.UUID
	if present {
		var err error
		student, err = uuid.Parse(studentRaw)
		if err != nil || student == uuid.Nil || student.String() != studentRaw {
			qandaBad(w, r)
			return
		}
	}
	from, ok := adminDateQuery(w, r, q, "from", h.now())
	if !ok {
		return
	}
	to, ok := adminDateQuery(w, r, q, "to", h.now())
	if !ok {
		return
	}
	limit, ok := qandaLimit(w, r, q)
	if !ok {
		return
	}
	cursorRaw, _, ok := qandaSingleQuery(w, r, q, "cursor")
	if !ok {
		return
	}
	cursor, err := decodeThreadCursor(cursorRaw, h.now())
	if err != nil {
		qandaBad(w, r)
		return
	}
	cursor.Limit = limit
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	threads, next, err := h.service.ListAdminThreads(r.Context(), actor, AdminThreadFilter{Status: Status(status), StudentID: student, From: from, To: to}, cursor)
	if err != nil {
		qandaError(w, r, err)
		return
	}
	data := make([]adminThreadDTO, len(threads))
	for i := range threads {
		data[i] = adminThreadView(threads[i])
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []adminThreadDTO `json:"data"`
		Meta struct {
			NextCursor string `json:"nextCursor,omitempty"`
		} `json:"meta"`
	}{Data: data, Meta: struct {
		NextCursor string `json:"nextCursor,omitempty"`
	}{encodeThreadCursor(next)}})
}

func (h *AdminHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := qandaRouteID(w, r)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	d, err := h.service.GetAdminThread(r.Context(), actor, id)
	if err != nil {
		qandaError(w, r, err)
		return
	}
	messages := make([]messageDTO, len(d.Messages))
	for i := range d.Messages {
		messages[i] = messageView(d.Messages[i])
	}
	notes := make([]teacherNoteDTO, len(d.Notes))
	for i, n := range d.Notes {
		notes[i] = teacherNoteDTO{ID: n.ID, AuthorUserID: n.AuthorUserID, Body: n.Body, CreatedAt: n.CreatedAt}
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data struct {
			Thread            adminThreadDTO   `json:"thread"`
			Messages          []messageDTO     `json:"messages"`
			Notes             []teacherNoteDTO `json:"notes"`
			NextMessageCursor string           `json:"nextMessageCursor,omitempty"`
		} `json:"data"`
	}{Data: struct {
		Thread            adminThreadDTO   `json:"thread"`
		Messages          []messageDTO     `json:"messages"`
		Notes             []teacherNoteDTO `json:"notes"`
		NextMessageCursor string           `json:"nextMessageCursor,omitempty"`
	}{adminThreadView(d.Thread), messages, notes, encodeMessageCursor(d.NextMessageCursor)}})
}

func (h *AdminHandler) AddMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := qandaRouteID(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64                 `json:"expectedVersion"`
		Body            string                `json:"body"`
		Attachments     []attachmentInputWire `json:"attachments"`
	}
	if !decodeQANDAJSON(w, r, &body) {
		return
	}
	key, ok := qandaIdempotencyKey(w, r)
	if !ok {
		return
	}
	attachments, ok := qandaAttachments(w, r, body.Attachments)
	if !ok {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	thread, message, err := h.service.AddAdminMessage(r.Context(), actor, AddAdminMessageInput{ThreadID: id, ExpectedVersion: body.ExpectedVersion, Body: body.Body, IdempotencyKey: key, Attachments: attachments})
	if err != nil {
		qandaError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data struct {
			Thread  adminThreadDTO `json:"thread"`
			Message messageDTO     `json:"message"`
		} `json:"data"`
	}{Data: struct {
		Thread  adminThreadDTO `json:"thread"`
		Message messageDTO     `json:"message"`
	}{adminThreadView(thread), messageView(message)}})
}

func (h *AdminHandler) ChangeStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := qandaRouteID(w, r)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int64  `json:"expectedVersion"`
		Status          Status `json:"status"`
	}
	if !decodeQANDAJSON(w, r, &body) {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	thread, err := h.service.ChangeStatus(r.Context(), actor, ChangeStatusInput{ThreadID: id, ExpectedVersion: body.ExpectedVersion, Status: body.Status})
	if err != nil {
		qandaError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data adminThreadDTO `json:"data"`
	}{adminThreadView(thread)})
}
func (h *AdminHandler) AddNote(w http.ResponseWriter, r *http.Request) {
	id, ok := qandaRouteID(w, r)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if !decodeQANDAJSON(w, r, &body) {
		return
	}
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	note, err := h.service.AddTeacherNote(r.Context(), actor, AddTeacherNoteInput{ThreadID: id, Body: body.Body})
	if err != nil {
		qandaError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, struct {
		Data teacherNoteDTO `json:"data"`
	}{teacherNoteDTO{ID: note.ID, AuthorUserID: note.AuthorUserID, Body: note.Body, CreatedAt: note.CreatedAt}})
}

func (h *AdminHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.ID == uuid.Nil || user.Role != auth.RoleAdmin || user.Status != auth.StatusActive {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
		return Principal{}, false
	}
	addr, err := httpx.ClientIP(r, h.trustedProxyCIDRs)
	if err != nil {
		qandaBad(w, r)
		return Principal{}, false
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}, true
}
func adminDateQuery(w http.ResponseWriter, r *http.Request, q map[string][]string, key string, now time.Time) (time.Time, bool) {
	raw, present, ok := qandaSingleQuery(w, r, q, key)
	if !ok || !present {
		return time.Time{}, ok
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil || at.Location() != time.UTC || at.Format(time.RFC3339) != raw || at.After(now.UTC()) {
		qandaBad(w, r)
		return time.Time{}, false
	}
	return at.UTC(), true
}
