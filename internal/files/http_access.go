package files

import (
	"context"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

type AccessHTTPService interface {
	Open(context.Context, Principal, OpenInput) (OpenedFile, error)
}
type AccessHandler struct {
	service          AccessHTTPService
	trusted          []netip.Prefix
	writeIdleTimeout time.Duration
	logger           *slog.Logger
}

const defaultWriteIdleTimeout = 30 * time.Second

func NewAccessHandler(service AccessHTTPService, trusted []netip.Prefix) *AccessHandler {
	return &AccessHandler{service: service, trusted: append([]netip.Prefix(nil), trusted...), writeIdleTimeout: defaultWriteIdleTimeout, logger: slog.Default()}
}
func (h *AccessHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireRole(auth.RoleStudent))
	r.Get("/{id}/preview", h.Preview)
	r.Get("/{id}/download", h.Download)
	return r
}
func (h *AccessHandler) Preview(w http.ResponseWriter, r *http.Request) { h.open(w, r, ActionPreview) }
func (h *AccessHandler) Download(w http.ResponseWriter, r *http.Request) {
	h.open(w, r, ActionDownload)
}
func (h *AccessHandler) open(w http.ResponseWriter, r *http.Request, action AccessAction) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || id == uuid.Nil {
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
	user, _ := auth.UserFromContext(r.Context())
	addr, err := httpx.ClientIP(r, h.trusted)
	if err != nil {
		fileError(w, r, ErrNotFound)
		return
	}
	actor := Principal{User: user, RequestID: httpx.RequestIDFromContext(r.Context()), IP: net.IP(addr.AsSlice())}
	opened, err := h.service.Open(r.Context(), actor, OpenInput{VersionID: id, Action: action, Range: raw})
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
	deliveryWriter := newIdleDeadlineWriter(w, h.writeIdleTimeout)
	defer deliveryWriter.finish()
	_ = deliverOpenedFileWithLogger(r.Context(), deliveryWriter, opened, h.logger)
}

type idleDeadlineWriter struct {
	dst       io.Writer
	control   *http.ResponseController
	timeout   time.Duration
	supported bool
	armed     bool
}

func newIdleDeadlineWriter(w http.ResponseWriter, timeout time.Duration) *idleDeadlineWriter {
	if timeout <= 0 {
		timeout = defaultWriteIdleTimeout
	}
	return &idleDeadlineWriter{dst: w, control: http.NewResponseController(w), timeout: timeout, supported: true}
}

func (w *idleDeadlineWriter) Write(p []byte) (int, error) {
	if w.supported {
		if err := w.control.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil {
			if !errors.Is(err, http.ErrNotSupported) {
				return 0, err
			}
			w.supported = false
		} else {
			w.armed = true
		}
	}
	return w.dst.Write(p)
}

func (w *idleDeadlineWriter) finish() {
	if w.armed {
		_ = w.control.SetWriteDeadline(time.Time{})
	}
}

func fileError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, r, 404, "not_found", "资源不存在")
	case errors.Is(err, ErrRangeNotSatisfiable):
		var rangeErr *RangeError
		if errors.As(err, &rangeErr) && rangeErr.Size > 0 {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(rangeErr.Size, 10))
		} else {
			w.Header().Set("Content-Range", "bytes */*")
		}
		httpx.Error(w, r, 416, "range_not_satisfiable", "请求范围无效")
	default:
		httpx.Error(w, r, 500, "internal_error", "服务暂不可用")
	}
}

const transferFailureLogTimeout = 2 * time.Second

func deliverOpenedFile(ctx context.Context, dst io.Writer, opened OpenedFile) error {
	return deliverOpenedFileWithLogger(ctx, dst, opened, slog.Default())
}

func deliverOpenedFileWithLogger(ctx context.Context, dst io.Writer, opened OpenedFile, logger *slog.Logger) error {
	_, copyErr := copyDeliveryContext(ctx, dst, opened.Body)
	closeErr := opened.Body.Close()
	if copyErr != nil {
		reason := "stream_read"
		if errors.Is(copyErr, errDeliveryWrite) {
			reason = "stream_write"
		}
		if errors.Is(copyErr, errDeliveryCancelled) || ctx.Err() != nil {
			reason = "cancelled"
		}
		reportTransferFailure(ctx, opened, reason, logger)
		return copyErr
	}
	if closeErr != nil {
		reportTransferFailure(ctx, opened, "stream_close", logger)
		return errors.New("delivery close failed")
	}
	return nil
}

func reportTransferFailure(requestCtx context.Context, opened OpenedFile, reason string, logger *slog.Logger) {
	if opened.ReportFailure == nil {
		return
	}
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), transferFailureLogTimeout)
	defer cancel()
	if err := opened.ReportFailure(logCtx, reason); err != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.ErrorContext(logCtx, "file_transfer_audit_failed")
	}
}
