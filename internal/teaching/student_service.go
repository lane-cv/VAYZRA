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

// StudentStore stays deliberately student-scoped: authorization is enforced in
// every SQL statement rather than by filtering loaded domain objects.
type StudentStore interface {
	BrowseStudent(context.Context, BrowseInput) ([]StudentCatalogNode, CatalogCursor, error)
	Recent(context.Context, uuid.UUID, int) ([]RecentLesson, error)
	Search(context.Context, SearchInput) ([]SearchResult, SearchCursor, error)
	GetLesson(context.Context, uuid.UUID, uuid.UUID) (StudentLesson, error)
	GetPosition(context.Context, uuid.UUID, uuid.UUID) (LessonProgress, error)
	UpdateProgress(context.Context, uuid.UUID, ProgressInput) error
}

type StudentHTTPService interface {
	Browse(context.Context, Principal, BrowseInput) ([]StudentCatalogNode, CatalogCursor, error)
	Recent(context.Context, Principal, int) ([]RecentLesson, error)
	GetLesson(context.Context, Principal, uuid.UUID) (StudentLesson, error)
	GetPosition(context.Context, Principal, uuid.UUID) (LessonProgress, error)
	Search(context.Context, Principal, SearchInput) ([]SearchResult, SearchCursor, error)
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

func (s *StudentService) Browse(ctx context.Context, actor Principal, in BrowseInput) ([]StudentCatalogNode, CatalogCursor, error) {
	if err := authorizeStudent(actor); err != nil {
		return nil, CatalogCursor{}, err
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.Limit < 1 || in.Limit > 50 || !validCatalogKind(in.Kind) || !validCatalogCursor(in.After) {
		return nil, CatalogCursor{}, ErrInvalid
	}
	in.StudentID = actor.User.ID
	return s.store.BrowseStudent(ctx, in)
}

func (s *StudentService) Recent(ctx context.Context, actor Principal, limit int) ([]RecentLesson, error) {
	if err := authorizeStudent(actor); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 20 {
		return nil, ErrInvalid
	}
	return s.store.Recent(ctx, actor.User.ID, limit)
}

func (s *StudentService) GetLesson(ctx context.Context, actor Principal, lessonID uuid.UUID) (StudentLesson, error) {
	if err := authorizeStudent(actor); err != nil {
		return StudentLesson{}, err
	}
	if lessonID == uuid.Nil {
		return StudentLesson{}, ErrInvalid
	}
	return s.store.GetLesson(ctx, actor.User.ID, lessonID)
}

func (s *StudentService) GetPosition(ctx context.Context, actor Principal, lessonID uuid.UUID) (LessonProgress, error) {
	if err := authorizeStudent(actor); err != nil {
		return LessonProgress{}, err
	}
	if lessonID == uuid.Nil {
		return LessonProgress{}, ErrInvalid
	}
	return s.store.GetPosition(ctx, actor.User.ID, lessonID)
}

func (s *StudentService) Search(ctx context.Context, actor Principal, in SearchInput) ([]SearchResult, SearchCursor, error) {
	if err := authorizeStudent(actor); err != nil {
		return nil, SearchCursor{}, err
	}
	in.StudentID, in.Query = actor.User.ID, strings.TrimSpace(in.Query)
	runes := utf8.RuneCountInString(in.Query)
	if !utf8.ValidString(in.Query) || runes < 2 || runes > 64 || (in.After.ID == uuid.Nil) != (in.After == (SearchCursor{})) {
		return nil, SearchCursor{}, ErrInvalid
	}
	if runes == 2 {
		in.IncludeBody = false
		if in.Limit == 0 {
			in.Limit = 10
		}
		if in.Limit < 1 || in.Limit > 10 {
			return nil, SearchCursor{}, ErrInvalid
		}
	} else {
		in.IncludeBody = true
		if in.Limit == 0 {
			in.Limit = 20
		}
		if in.Limit < 1 || in.Limit > 20 {
			return nil, SearchCursor{}, ErrInvalid
		}
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

func validCatalogKind(kind CatalogKind) bool {
	switch kind {
	case "", CatalogGrade, CatalogTerm, CatalogSubject, CatalogChapter, CatalogLesson:
		return true
	default:
		return false
	}
}

func validCatalogCursor(cursor CatalogCursor) bool {
	if cursor == (CatalogCursor{}) {
		return true
	}
	return cursor.KindRank >= 1 && cursor.KindRank <= 5 && cursor.ID != uuid.Nil
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
