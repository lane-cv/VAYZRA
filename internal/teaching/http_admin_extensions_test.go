package teaching

import (
	"context"

	"github.com/google/uuid"
)

func (*fakeAdminHTTPService) ListAdminCatalog(context.Context, Principal, AdminCatalogInput) ([]AdminCatalogItem, AdminCatalogCursor, error) {
	return nil, AdminCatalogCursor{}, nil
}
func (*fakeAdminHTTPService) GetAdminLesson(context.Context, Principal, uuid.UUID) (AdminLessonDetail, error) {
	return AdminLessonDetail{}, nil
}
func (*fakeAdminHTTPService) ListAdminRevisions(context.Context, Principal, uuid.UUID, int, RevisionCursor) ([]Revision, RevisionCursor, error) {
	return nil, RevisionCursor{}, nil
}
