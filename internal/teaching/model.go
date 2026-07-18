package teaching

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

type AudienceMode string

const (
	AudienceAll      AudienceMode = "all"
	AudienceSelected AudienceMode = "selected"
)

type CatalogKind string

const (
	CatalogGrade   CatalogKind = "grade"
	CatalogTerm    CatalogKind = "term"
	CatalogSubject CatalogKind = "subject"
	CatalogChapter CatalogKind = "chapter"
)

var (
	ErrNotFound                 = errors.New("teaching resource not found")
	ErrForbidden                = errors.New("teaching operation forbidden")
	ErrInvalid                  = errors.New("invalid teaching input")
	ErrConflict                 = errors.New("teaching draft conflict")
	ErrNotPublishable           = errors.New("lesson not publishable")
	ErrPublicationReaderExpired = errors.New("publication reader expired")
)

type Audience struct {
	Mode    AudienceMode
	UserIDs []uuid.UUID
}

type ExternalVideo struct {
	ID          uuid.UUID
	URL         string
	Title       string
	Description string
	SortKey     int64
}

type CatalogNode struct {
	ID          uuid.UUID
	ParentID    uuid.UUID
	Kind        CatalogKind
	Name        string
	Description string
	SortKey     int64
	ArchivedAt  *time.Time
}

type Draft struct {
	LessonID       uuid.UUID
	ChapterID      uuid.UUID
	Title          string
	Summary        string
	BodyMarkdown   string
	SortKey        int64
	LockVersion    int64
	Audience       Audience
	ExternalVideos []ExternalVideo
	UpdatedAt      time.Time
}

type Revision struct {
	ID                 uuid.UUID
	LessonID           uuid.UUID
	Version            int64
	SourceDraftVersion int64
	Title              string
	Summary            string
	BodyMarkdown       string
	SortKey            int64
	Audience           Audience
	ExternalVideos     []ExternalVideo
	PublishedBy        uuid.UUID
	PublishedAt        time.Time
}

type Lesson struct {
	ID                  uuid.UUID
	ChapterID           uuid.UUID
	PublishedRevisionID uuid.UUID
	ArchivedAt          *time.Time
}

type AdminCatalogItem struct {
	ID          uuid.UUID
	ParentID    uuid.UUID
	Kind        string
	Name        string
	Description string
	SortKey     int64
	ArchivedAt  *time.Time
	// Published is a compatibility shorthand for a current revision pointer.
	Published    bool
	HasRevisions bool
}
type AdminCatalogCursor struct {
	Rank    int
	SortKey int64
	ID      uuid.UUID
}
type AdminCatalogInput struct {
	Kind            string
	ParentID        uuid.UUID
	IncludeArchived bool
	Limit           int
	After           AdminCatalogCursor
}
type AdminLessonDetail struct {
	Lesson       Lesson
	Draft        Draft
	Published    *Revision
	HasRevisions bool
}
type RevisionCursor struct {
	Version int64
	ID      uuid.UUID
}

type CatalogCreateInput struct {
	Kind        CatalogKind
	ParentID    uuid.UUID
	Name        string
	Description string
	SortKey     int64
}

type CatalogRenameInput struct {
	Kind CatalogKind
	ID   uuid.UUID
	Name string
}

type CatalogReorderInput struct {
	Kind    CatalogKind
	ID      uuid.UUID
	SortKey int64
}

type CatalogArchiveInput struct {
	Kind     CatalogKind
	ID       uuid.UUID
	Archived bool
}

type CreateLessonInput struct {
	ChapterID uuid.UUID
	Title     string
	ActorID   uuid.UUID
}

type SaveDraftInput struct {
	LessonID        uuid.UUID
	ExpectedVersion int64
	Title           string
	Summary         string
	BodyMarkdown    string
	SortKey         int64
	Audience        Audience
	ExternalVideos  []ExternalVideo
	ActorID         uuid.UUID
}

type PublishInput struct {
	LessonID        uuid.UUID
	ExpectedVersion int64
	ActorID         uuid.UUID
}

type WithdrawInput struct {
	LessonID uuid.UUID
	ActorID  uuid.UUID
}

type BrowseInput struct {
	StudentID uuid.UUID
	GradeID   uuid.UUID
	TermID    uuid.UUID
	SubjectID uuid.UUID
	ChapterID uuid.UUID
	Kind      CatalogKind
	Limit     int
	After     CatalogCursor
}

type SearchCursor struct {
	SortKey int64
	ID      uuid.UUID
}

type SearchInput struct {
	StudentID   uuid.UUID
	Query       string
	Limit       int
	After       SearchCursor
	IncludeBody bool
}

type ProgressInput struct {
	RevisionID  uuid.UUID
	Viewed      bool
	Anchor      string
	ScrollRatio float64
	ObservedAt  time.Time
}

type PublicationReader interface {
	PublicationBlockers(context.Context, uuid.UUID, int64) ([]string, error)
}

// PublicationCheck returns ErrNotPublishable for a semantic readiness blocker.
// Every other error is an infrastructure failure and is preserved by Publish.
type PublicationCheck interface {
	Check(context.Context, PublicationReader, Draft) error
}
