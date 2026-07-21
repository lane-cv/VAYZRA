package files

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/teaching"
)

type readinessReader struct {
	blockers []string
	err      error
}

func (r readinessReader) PublicationBlockers(context.Context, uuid.UUID, int64) ([]string, error) {
	return r.blockers, r.err
}

func TestReadinessRejectsFileBlockers(t *testing.T) {
	check := NewReadinessChecker()
	d := teaching.Draft{LessonID: uuid.New(), LockVersion: 3}
	if err := check.Check(context.Background(), readinessReader{}, d); err != nil {
		t.Fatal(err)
	}
	if err := check.Check(context.Background(), readinessReader{blockers: []string{"file_not_ready"}}, d); !errors.Is(err, teaching.ErrNotPublishable) {
		t.Fatalf("err=%v", err)
	}
	boom := errors.New("database unavailable")
	if err := check.Check(context.Background(), readinessReader{err: boom}, d); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
}
