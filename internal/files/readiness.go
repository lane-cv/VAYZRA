package files

import (
	"context"
	"happylearn.local/app/internal/teaching"
)

type ReadinessChecker struct{}

func NewReadinessChecker() *ReadinessChecker { return &ReadinessChecker{} }
func (*ReadinessChecker) Check(ctx context.Context, reader teaching.PublicationReader, d teaching.Draft) error {
	blockers, err := reader.PublicationBlockers(ctx, d.LessonID, d.LockVersion)
	if err != nil {
		return err
	}
	if len(blockers) > 0 {
		return teaching.ErrNotPublishable
	}
	return nil
}

var _ teaching.PublicationCheck = (*ReadinessChecker)(nil)
