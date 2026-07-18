package teaching

import (
	"context"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

// StudentStore is deliberately narrower than CatalogStore: each method is
// student-scoped in SQL, so callers cannot load another student's lesson and
// apply authorization after the fact.
type StudentStore interface {
	Browse(context.Context, BrowseInput) ([]CatalogNode, error)
	Search(context.Context, SearchInput) ([]Revision, SearchCursor, error)
	GetLesson(context.Context, uuid.UUID, uuid.UUID) (Revision, error)
	UpdateProgress(context.Context, uuid.UUID, ProgressInput) error
}

type StudentHTTPService interface {
	Browse(context.Context, Principal, BrowseInput) ([]CatalogNode, error)
	GetLesson(context.Context, Principal, uuid.UUID) (Revision, error)
	Search(context.Context, Principal, SearchInput) ([]Revision, SearchCursor, error)
	UpdateProgress(context.Context, Principal, ProgressInput) error
}

type StudentService struct {
	store StudentStore
	now   func() time.Time
}

func NewStudentService(store StudentStore, now func() time.Time) *StudentService {
	if now == nil {
		now = time.Now
	}
	return &StudentService{store: store, now: now}
}

func (s *StudentService) Browse(ctx context.Context, actor Principal, in BrowseInput) ([]CatalogNode, error) {
	if err := authorizeStudent(actor); err != nil {
		return nil, err
	}
	in.StudentID = actor.User.ID
	return s.store.Browse(ctx, in)
}

func (s *StudentService) GetLesson(ctx context.Context, actor Principal, lessonID uuid.UUID) (Revision, error) {
	if err := authorizeStudent(actor); err != nil {
		return Revision{}, err
	}
	if lessonID == uuid.Nil {
		return Revision{}, ErrInvalid
	}
	return s.store.GetLesson(ctx, actor.User.ID, lessonID)
}

func (s *StudentService) Search(ctx context.Context, actor Principal, in SearchInput) ([]Revision, SearchCursor, error) {
	if err := authorizeStudent(actor); err != nil {
		return nil, SearchCursor{}, err
	}
	in.StudentID, in.Query = actor.User.ID, strings.TrimSpace(in.Query)
	if !utf8.ValidString(in.Query) || in.Query == "" || utf8.RuneCountInString(in.Query) > 200 || in.Limit < 1 || in.Limit > 50 {
		return nil, SearchCursor{}, ErrInvalid
	}
	return s.store.Search(ctx, in)
}

func (s *StudentService) UpdateProgress(ctx context.Context, actor Principal, in ProgressInput) error {
	if err := authorizeStudent(actor); err != nil {
		return err
	}
	if !validProgress(in, s.now()) {
		return ErrInvalid
	}
	return s.store.UpdateProgress(ctx, actor.User.ID, in)
}

func authorizeStudent(actor Principal) error {
	if actor.User.ID == uuid.Nil || actor.User.Role != auth.RoleStudent || actor.User.Status != auth.StatusActive || strings.TrimSpace(actor.RequestID) == "" || actor.IP == nil {
		return ErrForbidden
	}
	return nil
}

func validProgress(in ProgressInput, now time.Time) bool {
	if in.RevisionID == uuid.Nil || !utf8.ValidString(in.Anchor) || utf8.RuneCountInString(in.Anchor) > 160 || math.IsNaN(in.ScrollRatio) || math.IsInf(in.ScrollRatio, 0) || in.ScrollRatio < 0 || in.ScrollRatio > 1 || in.ObservedAt.IsZero() {
		return false
	}
	delta := now.Sub(in.ObservedAt)
	if delta < 0 {
		delta = -delta
	}
	return delta <= 10*time.Minute
}

var _ StudentHTTPService = (*StudentService)(nil)
