package files

import (
	"context"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/objectstore"
)

type AIAccessStore interface {
	ResolveAIAccess(context.Context, Principal, uuid.UUID) (AIDelivery, error)
	ResolveAIStatus(context.Context, Principal, uuid.UUID) (AIFileStatus, error)
	WriteAccessLog(context.Context, AccessLog) error
}

type AIAccessService struct {
	store               AIAccessStore
	originals, previews objectstore.Store
}

func NewAIAccessService(store AIAccessStore, originals, previews objectstore.Store) *AIAccessService {
	return &AIAccessService{store: store, originals: originals, previews: previews}
}

func validAIActor(actor Principal) bool {
	return actor.User.ID != uuid.Nil && actor.User.Role == auth.RoleStudent && actor.User.Status == auth.StatusActive
}

func (s *AIAccessService) Status(ctx context.Context, actor Principal, version uuid.UUID) (AIFileStatus, error) {
	if s == nil || s.store == nil || !validAIActor(actor) || version == uuid.Nil {
		return AIFileStatus{}, ErrNotFound
	}
	out, err := s.store.ResolveAIStatus(ctx, actor, version)
	if err != nil {
		return AIFileStatus{}, ErrNotFound
	}
	return out, nil
}

func (s *AIAccessService) Open(ctx context.Context, actor Principal, in AIOpenInput) (OpenedFile, error) {
	log := AccessLog{
		ActorUserID: actor.User.ID, RequestedVersionID: in.VersionID, Action: ActionPreview,
		RequestID: actor.RequestID, IP: append([]byte(nil), actor.IP...),
	}
	fail := func(result AccessResult, reason string, fallback error) (OpenedFile, error) {
		log.Result, log.Reason = result, reason
		if s == nil || s.store == nil || s.store.WriteAccessLog(ctx, log) != nil {
			return OpenedFile{}, ErrAccessUnavailable
		}
		return OpenedFile{}, fallback
	}
	if s == nil || s.store == nil || !validAIActor(actor) || in.VersionID == uuid.Nil {
		return fail(AccessDenied, "not_found", ErrNotFound)
	}
	delivery, err := s.store.ResolveAIAccess(ctx, actor, in.VersionID)
	log.VersionID, log.AIMessageID = delivery.VersionID, delivery.MessageID
	if err != nil {
		return fail(AccessDenied, "not_found", ErrNotFound)
	}
	rng, err := parseByteRange(in.Range, delivery.Size, delivery.Playable)
	if err != nil {
		return fail(AccessMalformed, "invalid_range", err)
	}
	var requested *objectstore.ByteRange
	if rng != nil {
		requested = &objectstore.ByteRange{Offset: rng.Start, Length: rng.End - rng.Start + 1}
		log.RangeStart, log.RangeEnd = &rng.Start, &rng.End
	}
	objects := s.originals
	if delivery.Preview {
		objects = s.previews
	}
	if objects == nil {
		return fail(AccessFailed, "storage", ErrAccessUnavailable)
	}
	body, info, err := objects.Get(ctx, delivery.ObjectKey, requested)
	if err != nil || info.Size != delivery.Size {
		if body != nil {
			_ = body.Close()
		}
		return fail(AccessFailed, "storage", ErrAccessUnavailable)
	}
	log.Result = AccessAllowed
	if err = s.store.WriteAccessLog(ctx, log); err != nil {
		_ = body.Close()
		return OpenedFile{}, ErrAccessUnavailable
	}
	size := delivery.Size
	if rng != nil {
		size = rng.End - rng.Start + 1
	}
	failure := log
	return OpenedFile{
		Body: body, DisplayName: delivery.DisplayName, ContentType: delivery.ContentType, Size: size,
		Partial: rng != nil, Range: rangeValue(rng, delivery.Size), Playable: delivery.Playable,
		ReportFailure: func(logCtx context.Context, reason string) error {
			failure.Result, failure.Reason = AccessFailed, reason
			return s.store.WriteAccessLog(logCtx, failure)
		},
	}, nil
}
