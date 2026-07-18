package teaching

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *fakeCatalogStore) ListAdminCatalog(context.Context, AdminCatalogInput) ([]AdminCatalogItem, AdminCatalogCursor, error) {
	return nil, AdminCatalogCursor{}, nil
}
func (s *fakeCatalogStore) GetAdminLesson(context.Context, uuid.UUID) (AdminLessonDetail, error) {
	return AdminLessonDetail{}, nil
}
func (s *fakeCatalogStore) ListAdminRevisions(context.Context, uuid.UUID, int, RevisionCursor) ([]Revision, RevisionCursor, error) {
	return nil, RevisionCursor{}, nil
}
func (s *fakeCatalogStore) LockDraftForPublication(ctx context.Context, id uuid.UUID) (Draft, error) {
	return s.GetDraft(ctx, id)
}
func (s *fakeCatalogStore) PublishSnapshot(ctx context.Context, in PublishInput, _ Draft) (Revision, error) {
	return s.Publish(ctx, in)
}
func (s *fakeCatalogStore) EligibleAudienceUsers(_ context.Context, ids []uuid.UUID) (int, error) {
	return len(ids), nil
}
func (s *fakeCatalogStore) PublicationQuery(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (s *fakeCatalogStore) PublicationQueryRow(context.Context, string, ...any) pgx.Row { return nil }
