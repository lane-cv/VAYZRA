package files

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
)

type BindingHTTPService interface {
	List(context.Context, Principal, uuid.UUID) ([]DraftBinding, error)
	Replace(context.Context, Principal, uuid.UUID, int64, []DraftBindingInput) ([]DraftBinding, error)
}
type BindingHandler struct {
	service BindingHTTPService
	trusted []netip.Prefix
}

func NewBindingHandler(service BindingHTTPService, trusted []netip.Prefix) *BindingHandler {
	return &BindingHandler{service: service, trusted: append([]netip.Prefix(nil), trusted...)}
}
func (h *BindingHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireRole(auth.RoleAdmin))
	r.Get("/{id}/files", h.List)
	r.Put("/{id}/files", h.Replace)
	return r
}
func (h *BindingHandler) List(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || id == uuid.Nil {
		uploadBad(w, r)
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		uploadBad(w, r)
		return
	}
	out, err := h.service.List(r.Context(), Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}, id)
	if err != nil {
		uploadHTTPError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Data []DraftBinding `json:"data"`
	}{out})
}
func (h *BindingHandler) Replace(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || id == uuid.Nil {
		uploadBad(w, r)
		return
	}
	vals := r.Header.Values("Content-Type")
	if len(vals) != 1 {
		httpx.Error(w, r, 415, "unsupported_media_type", "仅支持 JSON 请求")
		return
	}
	mt, p, err := mime.ParseMediaType(vals[0])
	if err != nil || mt != "application/json" || len(p) != 0 {
		httpx.Error(w, r, 415, "unsupported_media_type", "仅支持 JSON 请求")
		return
	}
	var body struct {
		ExpectedVersion int64               `json:"expectedVersion"`
		Files           []DraftBindingInput `json:"files"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil {
		uploadBad(w, r)
		return
	}
	if err = d.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		uploadBad(w, r)
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		uploadBad(w, r)
		return
	}
	actor := Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}
	out, err := h.service.Replace(r.Context(), actor, id, body.ExpectedVersion, body.Files)
	if err != nil {
		uploadHTTPError(w, r, err)
		return
	}
	httpx.JSON(w, 200, struct {
		Data []DraftBinding `json:"data"`
	}{out})
}
