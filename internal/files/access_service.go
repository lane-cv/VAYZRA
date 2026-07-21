package files

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/objectstore"
)

const maxRangeLength int64 = 64 * 1024 * 1024

type AccessService struct {
	store     AccessStore
	originals objectstore.Store
	previews  objectstore.Store
	now       func() time.Time
}

func NewAccessService(store AccessStore, originals, previews objectstore.Store) *AccessService {
	return &AccessService{store: store, originals: originals, previews: previews, now: time.Now}
}

func (s *AccessService) failClosedLog(ctx context.Context, log AccessLog, fallback error) error {
	if err := s.store.WriteAccessLog(ctx, log); err != nil {
		return ErrAccessUnavailable
	}
	return fallback
}

func (s *AccessService) Open(ctx context.Context, actor Principal, in OpenInput) (OpenedFile, error) {
	log := AccessLog{ActorUserID: actor.User.ID, RequestedVersionID: in.VersionID, Action: in.Action, RequestID: actor.RequestID, IP: append([]byte(nil), actor.IP...)}
	if actor.User.Role != auth.RoleStudent || actor.User.Status != auth.StatusActive || in.VersionID == uuid.Nil || (in.Action != ActionPreview && in.Action != ActionDownload) {
		log.Result, log.Reason = AccessDenied, "not_found"
		return OpenedFile{}, s.failClosedLog(ctx, log, ErrNotFound)
	}
	d, err := s.store.ResolveAccess(ctx, actor.User.ID, in.VersionID, in.Action)
	log.VersionID, log.RevisionID = d.VersionID, d.RevisionID
	if err != nil {
		log.Result, log.Reason = AccessDenied, "not_found"
		return OpenedFile{}, s.failClosedLog(ctx, log, ErrNotFound)
	}
	if in.Action == ActionDownload && d.Policy != PolicyDownload {
		log.Result, log.Reason = AccessDenied, "policy"
		return OpenedFile{}, s.failClosedLog(ctx, log, ErrNotFound)
	}
	rng, err := parseByteRange(in.Range, d.Size, d.Playable)
	if err != nil {
		log.Result, log.Reason = AccessMalformed, "invalid_range"
		return OpenedFile{}, s.failClosedLog(ctx, log, err)
	}
	var requested *objectstore.ByteRange
	if rng != nil {
		requested = &objectstore.ByteRange{Offset: rng.Start, Length: rng.End - rng.Start + 1}
		log.RangeStart, log.RangeEnd = &rng.Start, &rng.End
	}
	objects := s.originals
	if d.Preview {
		objects = s.previews
	}
	body, info, err := objects.Get(ctx, d.ObjectKey, requested)
	if err != nil {
		log.Result, log.Reason = AccessFailed, "storage"
		return OpenedFile{}, s.failClosedLog(ctx, log, ErrAccessUnavailable)
	}
	if info.Size != d.Size {
		_ = body.Close()
		log.Result, log.Reason = AccessFailed, "storage"
		return OpenedFile{}, s.failClosedLog(ctx, log, ErrAccessUnavailable)
	}
	if rng != nil && d.Playable {
		log.PlaybackSessionHash = s.playbackAggregationKey(actor.User.ID, d.VersionID, d.RevisionID, in.Action)
	}
	log.Result = AccessAllowed
	if err := s.store.WriteAccessLog(ctx, log); err != nil {
		_ = body.Close()
		return OpenedFile{}, ErrAccessUnavailable
	}
	size := d.Size
	if rng != nil {
		size = rng.End - rng.Start + 1
	}
	failureLog := log
	reportFailure := func(logCtx context.Context, reason string) error {
		failureLog.Result, failureLog.Reason = AccessFailed, reason
		return s.store.WriteAccessLog(logCtx, failureLog)
	}
	return OpenedFile{Body: body, DisplayName: d.DisplayName, ContentType: d.ContentType, Size: size, Partial: rng != nil, Range: rangeValue(rng, d.Size), Playable: d.Playable, ReportFailure: reportFailure}, nil
}

func rangeValue(r *ResponseRange, total int64) ResponseRange {
	if r == nil {
		return ResponseRange{Total: total}
	}
	return *r
}

func parseByteRange(raw string, size int64, playable bool) (*ResponseRange, error) {
	if raw == "" {
		return nil, nil
	}
	if !playable || size < 1 || !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
		return nil, &RangeError{Size: size}
	}
	v := strings.TrimPrefix(raw, "bytes=")
	dash := strings.IndexByte(v, '-')
	if dash < 0 || strings.Count(v, "-") != 1 {
		return nil, &RangeError{Size: size}
	}
	left, right := v[:dash], v[dash+1:]
	var start, end int64
	if left == "" {
		n, ok := parseASCIIDecimal(right)
		if !ok || n < 1 || n > maxRangeLength {
			return nil, &RangeError{Size: size}
		}
		if n > size {
			n = size
		}
		start = size - n
		end = size - 1
	} else {
		var ok bool
		start, ok = parseASCIIDecimal(left)
		if !ok || start < 0 || start >= size {
			return nil, &RangeError{Size: size}
		}
		if right == "" {
			end = size - 1
		} else {
			end, ok = parseASCIIDecimal(right)
			if !ok || end < start {
				return nil, &RangeError{Size: size}
			}
			if end >= size {
				end = size - 1
			}
		}
		// An explicit or implicit range ending at EOF may represent resume playback.
		// It is intentionally exempt from the 64 MiB slice cap; file size is still
		// bounded by the global 500 MiB upload limit and delivery remains streaming.
		if end-start+1 > maxRangeLength && end != size-1 {
			return nil, &RangeError{Size: size}
		}
	}
	return &ResponseRange{Start: start, End: end, Total: size}, nil
}

func parseASCIIDecimal(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseUint(raw, 10, 63)
	return int64(value), err == nil
}

var (
	errDeliveryRead      = errors.New("delivery read failed")
	errDeliveryWrite     = errors.New("delivery write failed")
	errDeliveryCancelled = errors.New("delivery cancelled")
)

func copyDelivery(dst io.Writer, src io.Reader) (int64, error) {
	return copyDeliveryContext(context.Background(), dst, src)
}

func copyDeliveryContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 64*1024)
	var total int64
	empty := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, errDeliveryCancelled
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			empty = 0
			written, writeErr := dst.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil || written != n {
				return total, errDeliveryWrite
			}
		} else {
			empty++
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			if ctx.Err() != nil {
				return total, errDeliveryCancelled
			}
			return total, errDeliveryRead
		}
		if empty >= 100 {
			return total, errDeliveryRead
		}
	}
}

const playbackAggregationWindow = 10 * time.Minute

func (s *AccessService) playbackAggregationKey(actor, version, revision uuid.UUID, action AccessAction) string {
	h := sha256.New()
	_, _ = h.Write(actor[:])
	_, _ = h.Write(version[:])
	_, _ = h.Write(revision[:])
	_, _ = h.Write([]byte(action))
	var bucket [8]byte
	binary.BigEndian.PutUint64(bucket[:], uint64(s.now().UTC().Unix()/int64(playbackAggregationWindow/time.Second)))
	_, _ = h.Write(bucket[:])
	return hex.EncodeToString(h.Sum(nil))
}
