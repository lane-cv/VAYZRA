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
	Browse(context.Context, BrowseInput) ([]CatalogNode, error)
	Search(context.Context, SearchInput) ([]Revision, SearchCursor, error)
	UpdateProgress(context.Context, uuid.UUID, ProgressInput) error
}

type TxStore interface {
	CatalogStore
}

type UnitOfWork interface {
	WithinTx(context.Context, func(TxStore, audit.Writer) error) error
}
