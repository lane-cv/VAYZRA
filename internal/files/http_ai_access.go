package files

import (
	"context"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

type AIAccessHTTPService interface {
	Status(context.Context, Principal, uuid.UUID) (AIFileStatus, error)
	Open(context.Context, Principal, AIOpenInput) (OpenedFile, error)
}

type AIAccessHandler struct {
	service AIAccessHTTPService
	trusted []netip.Prefix
}

func NewAIAccessHandler(service AIAccessHTTPService, trusted []netip.Prefix) *AIAccessHandler {
	return &AIAccessHandler{service: service, trusted: append([]netip.Prefix(nil), trusted...)}
}

func (h *AIAccessHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	}, auth.RequireRole(auth.RoleStudent))
	r.Get("/{fileVersionId}/status", h.status)
	r.Get("/{fileVersionId}/preview", h.preview)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { fileError(w, r, ErrNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不被允许")
	})
	return r
}

func (h *AIAccessHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if r.URL.RawQuery != "" {
		fileError(w, r, ErrNotFound)
		return Principal{}, false
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user.ID == uuid.Nil || user.Role != auth.RoleStudent || user.Status != auth.StatusActive {
		httpx.Error(w, r, http.StatusForbidden, "forbidden", "无权访问")
		return Principal{}, false
	}
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		fileError(w, r, ErrNotFound)
		return Principal{}, false
	}
	return Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}, true
}

func aiFileVersionID(r *http.Request) (uuid.UUID, error) {
	raw := chi.URLParam(r, "fileVersionId")
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

func (h *AIAccessHandler) status(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, err := aiFileVersionID(r)
	if err != nil {
		fileError(w, r, ErrNotFound)
		return
	}
	out, err := h.service.Status(r.Context(), actor, id)
	if err != nil {
		fileError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *AIAccessHandler) preview(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, err := aiFileVersionID(r)
	if err != nil {
		fileError(w, r, ErrNotFound)
		return
	}
	ranges := r.Header.Values("Range")
	rawRange := ""
	if len(ranges) > 1 {
		rawRange = "invalid-multiple"
	} else if len(ranges) == 1 {
		rawRange = ranges[0]
	}
	opened, err := h.service.Open(r.Context(), actor, AIOpenInput{VersionID: id, Range: rawRange})
	if err != nil {
		fileError(w, r, err)
		return
	}
	disposition := mime.FormatMediaType("inline", map[string]string{"filename": opened.DisplayName})
	if disposition == "" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Type", opened.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(opened.Size, 10))
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if opened.Playable {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	status := http.StatusOK
	if opened.Partial {
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(opened.Range.Start, 10)+"-"+strconv.FormatInt(opened.Range.End, 10)+"/"+strconv.FormatInt(opened.Range.Total, 10))
	}
	w.WriteHeader(status)
	writer := newIdleDeadlineWriter(w, defaultWriteIdleTimeout)
	defer writer.finish()
	_ = deliverOpenedFile(r.Context(), writer, opened)
}
