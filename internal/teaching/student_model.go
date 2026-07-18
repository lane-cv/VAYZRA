package teaching

import (
	"time"

	"github.com/google/uuid"
)

const CatalogLesson CatalogKind = "lesson"

type CatalogCursor struct {
	KindRank int
	SortKey  int64
	ID       uuid.UUID
}

type StudentCatalogNode struct {
	ID                uuid.UUID
	ParentID          uuid.UUID
	Kind              CatalogKind
	Name              string
	Description       string
	SortKey           int64
	LessonID          uuid.UUID
	CurrentRevisionID uuid.UUID
	RevisionStatus    string
}

type LessonProgress struct {
	Viewed        bool
	Anchor        string
	ScrollRatio   float64
	ObservedAt    time.Time
	FirstViewedAt time.Time
	LastViewedAt  time.Time
}

type StudentLesson struct {
	Revision Revision
	Progress *LessonProgress
}

type SearchResult struct {
	LessonID       uuid.UUID
	RevisionID     uuid.UUID
	Title          string
	Summary        string
	Snippet        string
	GradeID        uuid.UUID
	GradeName      string
	TermID         uuid.UUID
	TermName       string
	SubjectID      uuid.UUID
	SubjectName    string
	ChapterID      uuid.UUID
	ChapterName    string
	RevisionStatus string
	SortKey        int64
}

type RecentLesson struct {
	SearchResult
	Position LessonProgress
}
