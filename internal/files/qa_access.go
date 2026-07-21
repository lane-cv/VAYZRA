package files

import (
	"context"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/objectstore"
)

type QAAccessStore interface {
	ResolveQAAccess(context.Context, Principal, uuid.UUID, AccessAction) (QADelivery, error)
	ResolveQAStatus(context.Context, Principal, uuid.UUID) (QAFileStatus, error)
	WriteAccessLog(context.Context, AccessLog) error
}

type QAAccessService struct {
	store               QAAccessStore
	originals, previews objectstore.Store
	now                 func() time.Time
}

func NewQAAccessService(store QAAccessStore, originals, previews objectstore.Store) *QAAccessService {
	return &QAAccessService{store: store, originals: originals, previews: previews, now: time.Now}
}

func validQAActor(actor Principal) bool {
	return actor.User.ID != uuid.Nil && actor.User.Status == auth.StatusActive &&
		(actor.User.Role == auth.RoleStudent || actor.User.Role == auth.RoleAdmin)
}

func (s *QAAccessService) Status(ctx context.Context, actor Principal, version uuid.UUID) (QAFileStatus, error) {
	if s == nil || s.store == nil || !validQAActor(actor) || version == uuid.Nil {
		return QAFileStatus{}, ErrNotFound
	}
	status, err := s.store.ResolveQAStatus(ctx, actor, version)
	if err != nil {
		return QAFileStatus{}, ErrNotFound
	}
	return status, nil
}

func (s *QAAccessService) Open(ctx context.Context, actor Principal, in QAOpenInput) (OpenedFile, error) {
	log := AccessLog{ActorUserID: actor.User.ID, RequestedVersionID: in.VersionID, Action: in.Action, RequestID: actor.RequestID, IP: append([]byte(nil), actor.IP...)}
	fail := func(result AccessResult, reason string, fallback error) (OpenedFile, error) {
		log.Result, log.Reason = result, reason
		if s == nil || s.store == nil || s.store.WriteAccessLog(ctx, log) != nil {
			return OpenedFile{}, ErrAccessUnavailable
		}
		return OpenedFile{}, fallback
	}
	if s == nil || s.store == nil || !validQAActor(actor) || in.VersionID == uuid.Nil || (in.Action != ActionPreview && in.Action != ActionDownload) {
		return fail(AccessDenied, "not_found", ErrNotFound)
	}
	d, err := s.store.ResolveQAAccess(ctx, actor, in.VersionID, in.Action)
	log.VersionID, log.QAMessageID = d.VersionID, d.MessageID
	if err != nil {
		return fail(AccessDenied, "not_found", ErrNotFound)
	}
	rangeCapable := d.Playable || in.Action == ActionDownload
	rng, err := parseByteRange(in.Range, d.Size, rangeCapable)
	if err != nil {
		return fail(AccessMalformed, "invalid_range", err)
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
	if err != nil || info.Size != d.Size {
		if body != nil {
			_ = body.Close()
		}
		return fail(AccessFailed, "storage", ErrAccessUnavailable)
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
	failure := log
	return OpenedFile{Body: body, DisplayName: d.DisplayName, ContentType: d.ContentType, Size: size, Partial: rng != nil, Range: rangeValue(rng, d.Size), Playable: rangeCapable, ReportFailure: func(c context.Context, reason string) error {
		failure.Result, failure.Reason = AccessFailed, reason
		return s.store.WriteAccessLog(c, failure)
	}}, nil
}
