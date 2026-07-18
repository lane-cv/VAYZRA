package teaching

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

type Principal struct {
	User      auth.User
	RequestID string
	IP        net.IP
}

type AdminHTTPService interface {
	CreateCatalog(context.Context, Principal, CatalogCreateInput) (CatalogNode, error)
	RenameCatalog(context.Context, Principal, CatalogRenameInput) (CatalogNode, error)
	ReorderCatalog(context.Context, Principal, CatalogReorderInput) error
	ArchiveCatalog(context.Context, Principal, CatalogArchiveInput) error
	CreateLesson(context.Context, Principal, CreateLessonInput) (Draft, error)
	SaveDraft(context.Context, Principal, SaveDraftInput) (Draft, error)
	Publish(context.Context, Principal, PublishInput) (Revision, error)
	Withdraw(context.Context, Principal, uuid.UUID) error
	ArchiveLesson(context.Context, Principal, uuid.UUID) error
	ListAdminCatalog(context.Context, Principal, AdminCatalogInput) ([]AdminCatalogItem, AdminCatalogCursor, error)
	GetAdminLesson(context.Context, Principal, uuid.UUID) (AdminLessonDetail, error)
	ListAdminRevisions(context.Context, Principal, uuid.UUID, int, RevisionCursor) ([]Revision, RevisionCursor, error)
}

type Service struct {
	store       CatalogStore
	publication PublicationCheck
	now         func() time.Time
}

// allowPublication is intentionally private: production only injects the no-op
// catalog gate until file readiness is implemented in the next phase.
type allowPublication struct{}

func (allowPublication) Check(context.Context, PublicationReader, Draft) error { return nil }

func NewService(store CatalogStore, publication PublicationCheck, now func() time.Time) *Service {
	if publication == nil {
		publication = allowPublication{}
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, publication: publication, now: now}
}

func (s *Service) CreateCatalog(ctx context.Context, actor Principal, in CatalogCreateInput) (CatalogNode, error) {
	if err := authorize(actor); err != nil {
		return CatalogNode{}, err
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if !validCatalogCreate(in) {
		return CatalogNode{}, ErrInvalid
	}
	var node CatalogNode
	err := s.withTx(ctx, func(store TxStore, writer audit.Writer) error {
		var err error
		node, err = store.CreateCatalog(ctx, in)
		if err != nil {
			return err
		}
		return writer.Write(ctx, teachingEvent(actor, "catalog.created", "catalog", node.ID, map[string]any{"kind": string(node.Kind)}))
	})
	return node, err
}
func (s *Service) RenameCatalog(ctx context.Context, actor Principal, in CatalogRenameInput) (CatalogNode, error) {
	if err := authorize(actor); err != nil {
		return CatalogNode{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if !validKind(in.Kind) || in.ID == uuid.Nil || !validText(in.Name, 80) {
		return CatalogNode{}, ErrInvalid
	}
	var node CatalogNode
	err := s.withTx(ctx, func(store TxStore, writer audit.Writer) error {
		var err error
		node, err = store.RenameCatalog(ctx, in)
		if err != nil {
			return err
		}
		return writer.Write(ctx, teachingEvent(actor, "catalog.renamed", "catalog", node.ID, map[string]any{"kind": string(node.Kind)}))
	})
	return node, err
}
func (s *Service) ReorderCatalog(ctx context.Context, actor Principal, in CatalogReorderInput) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if !validKind(in.Kind) || in.ID == uuid.Nil {
		return ErrInvalid
	}
	return s.withTx(ctx, func(store TxStore, writer audit.Writer) error {
		if err := store.ReorderCatalog(ctx, in); err != nil {
			return err
		}
		return writer.Write(ctx, teachingEvent(actor, "catalog.reordered", "catalog", in.ID, map[string]any{"kind": string(in.Kind)}))
	})
}
func (s *Service) ArchiveCatalog(ctx context.Context, actor Principal, in CatalogArchiveInput) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if !validKind(in.Kind) || in.ID == uuid.Nil {
		return ErrInvalid
	}
	return s.withTx(ctx, func(store TxStore, writer audit.Writer) error {
		if err := store.ArchiveCatalog(ctx, in); err != nil {
			return err
		}
		action := "catalog.archived"
		if !in.Archived {
			action = "catalog.restored"
		}
		return writer.Write(ctx, teachingEvent(actor, action, "catalog", in.ID, map[string]any{"kind": string(in.Kind)}))
	})
}
func (s *Service) CreateLesson(ctx context.Context, actor Principal, in CreateLessonInput) (Draft, error) {
	if err := authorize(actor); err != nil {
		return Draft{}, err
	}
	in.Title, in.ActorID = strings.TrimSpace(in.Title), actor.User.ID
	if in.ChapterID == uuid.Nil || !validText(in.Title, 160) {
		return Draft{}, ErrInvalid
	}
	var draft Draft
	err := s.withTx(ctx, func(store TxStore, writer audit.Writer) error {
		var err error
		draft, err = store.CreateLesson(ctx, in)
		if err != nil {
			return err
		}
		return writer.Write(ctx, teachingEvent(actor, "lesson.draft_saved", "lesson", draft.LessonID, map[string]any{}))
	})
	return draft, err
}
func (s *Service) SaveDraft(ctx context.Context, actor Principal, in SaveDraftInput) (Draft, error) {
	if err := authorize(actor); err != nil {
		return Draft{}, err
	}
	in = normalizedDraftInput(in, actor.User.ID)
	if !validDraft(in) {
		return Draft{}, ErrInvalid
	}
	var draft Draft
	err := s.withTx(ctx, func(store TxStore, writer audit.Writer) error {
		var err error
		draft, err = store.SaveDraft(ctx, in)
		if err != nil {
			return err
		}
		return writer.Write(ctx, teachingEvent(actor, "lesson.draft_saved", "lesson", draft.LessonID, map[string]any{}))
	})
	return draft, err
}
func (s *Service) Publish(ctx context.Context, actor Principal, in PublishInput) (Revision, error) {
	if err := authorize(actor); err != nil {
		return Revision{}, err
	}
	in.ActorID = actor.User.ID
	if in.LessonID == uuid.Nil || in.ExpectedVersion < 1 {
		return Revision{}, ErrInvalid
	}
	var revision Revision
	err := s.withTx(ctx, func(store TxStore, writer audit.Writer) error {
		draft, err := store.LockDraftForPublication(ctx, in.LessonID)
		if err != nil {
			return err
		}
		if draft.LockVersion != in.ExpectedVersion {
			return ErrConflict
		}
		persisted := SaveDraftInput{LessonID: draft.LessonID, ExpectedVersion: draft.LockVersion, Title: draft.Title, Summary: draft.Summary, BodyMarkdown: draft.BodyMarkdown, SortKey: draft.SortKey, Audience: draft.Audience, ExternalVideos: draft.ExternalVideos, ActorID: actor.User.ID}
		if !validPersistedDraft(persisted) {
			return ErrNotPublishable
		}
		if draft.Audience.Mode == AudienceSelected {
			eligible, err := store.EligibleAudienceUsers(ctx, draft.Audience.UserIDs)
			if err != nil {
				return err
			}
			if eligible != len(draft.Audience.UserIDs) {
				return ErrNotPublishable
			}
		}
		if err := s.publication.Check(ctx, store, draft); err != nil {
			return ErrNotPublishable
		}
		revision, err = store.PublishSnapshot(ctx, in, draft)
		if err != nil {
			return err
		}
		return writer.Write(ctx, teachingEvent(actor, "lesson.published", "lesson", in.LessonID, map[string]any{"revision_id": revision.ID.String()}))
	})
	return revision, err
}
func (s *Service) ListAdminCatalog(ctx context.Context, actor Principal, in AdminCatalogInput) ([]AdminCatalogItem, AdminCatalogCursor, error) {
	if err := authorize(actor); err != nil {
		return nil, AdminCatalogCursor{}, err
	}
	if in.Limit < 1 || in.Limit > 200 {
		return nil, AdminCatalogCursor{}, ErrInvalid
	}
	if in.Kind != "" && in.Kind != "grade" && in.Kind != "term" && in.Kind != "subject" && in.Kind != "chapter" && in.Kind != "lesson" {
		return nil, AdminCatalogCursor{}, ErrInvalid
	}
	return s.store.ListAdminCatalog(ctx, in)
}
func (s *Service) GetAdminLesson(ctx context.Context, actor Principal, id uuid.UUID) (AdminLessonDetail, error) {
	if err := authorize(actor); err != nil {
		return AdminLessonDetail{}, err
	}
	if id == uuid.Nil {
		return AdminLessonDetail{}, ErrInvalid
	}
	return s.store.GetAdminLesson(ctx, id)
}
func (s *Service) ListAdminRevisions(ctx context.Context, actor Principal, lessonID uuid.UUID, limit int, after RevisionCursor) ([]Revision, RevisionCursor, error) {
	if err := authorize(actor); err != nil {
		return nil, RevisionCursor{}, err
	}
	if lessonID == uuid.Nil || limit < 1 || limit > 100 {
		return nil, RevisionCursor{}, ErrInvalid
	}
	return s.store.ListAdminRevisions(ctx, lessonID, limit, after)
}

func (s *Service) ArchiveLesson(ctx context.Context, actor Principal, lessonID uuid.UUID) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if lessonID == uuid.Nil {
		return ErrInvalid
	}
	return s.withTx(ctx, func(store TxStore, writer audit.Writer) error {
		if err := store.ArchiveLesson(ctx, lessonID); err != nil {
			return err
		}
		return writer.Write(ctx, teachingEvent(actor, "lesson.archived", "lesson", lessonID, map[string]any{}))
	})
}

func (s *Service) Withdraw(ctx context.Context, actor Principal, lessonID uuid.UUID) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if lessonID == uuid.Nil {
		return ErrInvalid
	}
	return s.withTx(ctx, func(store TxStore, writer audit.Writer) error {
		if err := store.Withdraw(ctx, WithdrawInput{LessonID: lessonID, ActorID: actor.User.ID}); err != nil {
			return err
		}
		return writer.Write(ctx, teachingEvent(actor, "lesson.withdrawn", "lesson", lessonID, map[string]any{}))
	})
}

func (s *Service) withTx(ctx context.Context, fn func(TxStore, audit.Writer) error) error {
	if uow, ok := s.store.(UnitOfWork); ok {
		return uow.WithinTx(ctx, fn)
	}
	store, ok := s.store.(TxStore)
	if !ok {
		return ErrInvalid
	}
	return fn(store, discardTeachingAudit{})
}

type discardTeachingAudit struct{}

func (discardTeachingAudit) Write(context.Context, audit.Event) error { return nil }

func authorize(actor Principal) error {
	if actor.User.ID == uuid.Nil || actor.User.Role != auth.RoleAdmin || actor.User.Status != auth.StatusActive || strings.TrimSpace(actor.RequestID) == "" || actor.IP == nil {
		return ErrForbidden
	}
	return nil
}
func teachingEvent(actor Principal, action, targetType string, target uuid.UUID, metadata map[string]any) audit.Event {
	return audit.Event{ActorUserID: actor.User.ID, Action: action, TargetType: targetType, TargetID: target.String(), Metadata: metadata, RequestID: actor.RequestID, IP: append(net.IP(nil), actor.IP...)}
}
func validKind(k CatalogKind) bool {
	return k == CatalogGrade || k == CatalogTerm || k == CatalogSubject || k == CatalogChapter
}
func validCatalogCreate(in CatalogCreateInput) bool {
	if !validKind(in.Kind) || !validText(in.Name, 80) || utf8.RuneCountInString(in.Description) > 500 {
		return false
	}
	return (in.Kind == CatalogGrade && in.ParentID == uuid.Nil) || (in.Kind != CatalogGrade && in.ParentID != uuid.Nil)
}
func validText(v string, max int) bool {
	return utf8.ValidString(v) && strings.TrimSpace(v) != "" && utf8.RuneCountInString(v) <= max
}
func normalizedDraftInput(in SaveDraftInput, actorID uuid.UUID) SaveDraftInput {
	in.ActorID, in.Title, in.Summary = actorID, strings.TrimSpace(in.Title), strings.TrimSpace(in.Summary)
	for i := range in.ExternalVideos {
		normalized, err := normalizeExternalURL(in.ExternalVideos[i].URL)
		if err != nil {
			normalized = ""
		}
		in.ExternalVideos[i].URL = normalized
		in.ExternalVideos[i].Title = strings.TrimSpace(in.ExternalVideos[i].Title)
		in.ExternalVideos[i].Description = strings.TrimSpace(in.ExternalVideos[i].Description)
	}
	return in
}
func validDraft(in SaveDraftInput) bool {
	if in.LessonID == uuid.Nil || in.ExpectedVersion < 1 || !validText(in.Title, 160) || !utf8.ValidString(in.Summary) || utf8.RuneCountInString(in.Summary) > 500 || !utf8.ValidString(in.BodyMarkdown) || utf8.RuneCountInString(in.BodyMarkdown) > 200000 {
		return false
	}
	if in.Audience.Mode != AudienceAll && in.Audience.Mode != AudienceSelected {
		return false
	}
	if in.Audience.Mode == AudienceAll && len(in.Audience.UserIDs) != 0 {
		return false
	}
	if in.Audience.Mode == AudienceSelected && len(in.Audience.UserIDs) == 0 {
		return false
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range in.Audience.UserIDs {
		if id == uuid.Nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, video := range in.ExternalVideos {
		if video.ID == uuid.Nil || !validText(video.Title, 160) || !utf8.ValidString(video.Description) || utf8.RuneCountInString(video.Description) > 500 || !validExternalURL(video.URL) {
			return false
		}
	}
	return true
}
func validPersistedDraft(in SaveDraftInput) bool {
	if !validDraft(in) {
		return false
	}
	lower := strings.ToLower(in.BodyMarkdown)
	for _, bad := range []string{"<script", "javascript:", "\\write18", "\\input{", "\\include{"} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	if strings.Count(in.BodyMarkdown, "```")%2 != 0 || strings.ContainsRune(in.BodyMarkdown, '\x00') {
		return false
	}
	return true
}
func normalizeExternalURL(raw string) (string, error) {
	if strings.ContainsAny(raw, "\r\n\t") {
		return "", ErrInvalid
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || !u.IsAbs() || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" {
		return "", ErrInvalid
	}
	u.Scheme = "https"
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && port != "443" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Host = host
	if u.Fragment == "" {
		u.RawFragment = ""
	}
	return u.String(), nil
}
func validExternalURL(raw string) bool { _, err := normalizeExternalURL(raw); return err == nil }

var _ AdminHTTPService = (*Service)(nil)
var _ = errors.Is
