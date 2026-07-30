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

type QAAccessHTTPService interface {
	Status(context.Context, Principal, uuid.UUID) (QAFileStatus, error)
	Open(context.Context, Principal, QAOpenInput) (OpenedFile, error)
}

type QAAccessHandler struct {
	service     QAAccessHTTPService
	trusted     []netip.Prefix
	logCategory func(string)
}

func NewQAAccessHandler(service QAAccessHTTPService, trusted []netip.Prefix) *QAAccessHandler {
	return NewQAAccessHandlerWithLog(service, trusted, nil)
}

func NewQAAccessHandlerWithLog(
	service QAAccessHTTPService,
	trusted []netip.Prefix,
	logCategory func(string),
) *QAAccessHandler {
	return &QAAccessHandler{
		service:     service,
		trusted:     append([]netip.Prefix(nil), trusted...),
		logCategory: logCategory,
	}
}

func (h *QAAccessHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.NoStore, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	}, h.requireQAActor)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, "not_found", "资源不存在")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不允许")
	})
	r.Get("/{version}/status", h.Status)
	r.Get("/{version}/preview", func(w http.ResponseWriter, r *http.Request) { h.open(w, r, ActionPreview) })
	r.Get("/{version}/download", func(w http.ResponseWriter, r *http.Request) { h.open(w, r, ActionDownload) })
	return r
}

func (h *QAAccessHandler) requireQAActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFromContext(r.Context())
		if !ok {
			httpx.Error(w, r, 401, "unauthenticated", "请先登录")
			return
		}
		if u.Status != auth.StatusActive || (u.Role != auth.RoleStudent && u.Role != auth.RoleAdmin) {
			httpx.Error(w, r, 403, "forbidden", "无权访问")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *QAAccessHandler) actor(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if r.URL.RawQuery != "" {
		fileError(w, r, ErrNotFound)
		return Principal{}, false
	}
	id := httpx.RequestIDFromContext(r.Context())
	u, _ := auth.UserFromContext(r.Context())
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		fileError(w, r, ErrNotFound)
		return Principal{}, false
	}
	return Principal{User: u, RequestID: id, IP: net.IP(addr.AsSlice())}, true
}

func qaVersion(r *http.Request) (uuid.UUID, error) {
	raw := chi.URLParam(r, "version")
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

func (h *QAAccessHandler) Status(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, err := qaVersion(r)
	if err != nil {
		fileError(w, r, ErrNotFound)
		return
	}
	out, err := h.service.Status(r.Context(), actor, id)
	if err != nil {
		fileError(w, r, err)
		return
	}
	httpx.JSON(w, 200, out)
}

func (h *QAAccessHandler) open(w http.ResponseWriter, r *http.Request, action AccessAction) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	id, err := qaVersion(r)
	if err != nil {
		fileError(w, r, ErrNotFound)
		return
	}
	ranges := r.Header.Values("Range")
	raw := ""
	if len(ranges) > 1 {
		raw = "invalid-multiple"
	} else if len(ranges) == 1 {
		raw = ranges[0]
	}
	opened, err := h.service.Open(r.Context(), actor, QAOpenInput{VersionID: id, Action: action, Range: raw})
	if err != nil {
		fileError(w, r, err)
		return
	}
	disposition := "inline"
	if action == ActionDownload {
		disposition = "attachment"
	}
	cd := mime.FormatMediaType(disposition, map[string]string{"filename": opened.DisplayName})
	if cd == "" {
		cd = disposition
	}
	w.Header().Set("Content-Disposition", cd)
	w.Header().Set("Content-Type", opened.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(opened.Size, 10))
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if opened.Playable {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	status := 200
	if opened.Partial {
		status = 206
		w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(opened.Range.Start, 10)+"-"+strconv.FormatInt(opened.Range.End, 10)+"/"+strconv.FormatInt(opened.Range.Total, 10))
	}
	w.WriteHeader(status)
	dw := newIdleDeadlineWriter(w, defaultWriteIdleTimeout)
	defer dw.finish()
	_ = deliverOpenedFileWithLog(r.Context(), dw, opened, h.logCategory)
}
