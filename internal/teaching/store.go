package teaching

import (
	"context"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
)

type CatalogStore interface {
	CreateCatalog(context.Context, CatalogCreateInput) (CatalogNode, error)
	RenameCatalog(context.Context, CatalogRenameInput) (CatalogNode, error)
	ReorderCatalog(context.Context, CatalogReorderInput) error
	ArchiveCatalog(context.Context, CatalogArchiveInput) error
	CreateLesson(context.Context, CreateLessonInput) (Draft, error)
	GetDraft(context.Context, uuid.UUID) (Draft, error)
	SaveDraft(context.Context, SaveDraftInput) (Draft, error)
	Publish(context.Context, PublishInput) (Revision, error)
	Withdraw(context.Context, WithdrawInput) error
	ArchiveLesson(context.Context, uuid.UUID) error
	ListAdminCatalog(context.Context, AdminCatalogInput) ([]AdminCatalogItem, AdminCatalogCursor, error)
	GetAdminLesson(context.Context, uuid.UUID) (AdminLessonDetail, error)
	ListAdminRevisions(context.Context, uuid.UUID, int, RevisionCursor) ([]Revision, RevisionCursor, error)
	LockDraftForPublication(context.Context, uuid.UUID) (Draft, error)
	PublishSnapshot(context.Context, PublishInput, Draft) (Revision, error)
	EligibleAudienceUsers(context.Context, []uuid.UUID) (int, error)
	Browse(context.Context, BrowseInput) ([]CatalogNode, error)
	Search(context.Context, SearchInput) ([]Revision, SearchCursor, error)
	UpdateProgress(context.Context, uuid.UUID, ProgressInput) error
}

type TxStore interface {
	CatalogStore
	PublicationReader
}

type UnitOfWork interface {
	WithinTx(context.Context, func(TxStore, audit.Writer) error) error
}
