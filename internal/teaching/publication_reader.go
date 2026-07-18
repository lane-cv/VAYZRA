package teaching

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// publicationReadScope deliberately exposes only the narrow publication read
// capability and is valid only during the synchronous checker invocation.
type publicationReadScope struct {
	mu     sync.RWMutex
	active bool
	reader PublicationReader
}

func newPublicationReadScope(reader PublicationReader) *publicationReadScope {
	return &publicationReadScope{active: true, reader: reader}
}

func (r *publicationReadScope) PublicationBlockers(ctx context.Context, lessonID uuid.UUID, sourceDraftVersion int64) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.active {
		return nil, ErrPublicationReaderExpired
	}
	return r.reader.PublicationBlockers(ctx, lessonID, sourceDraftVersion)
}

func (r *publicationReadScope) invalidate() {
	r.mu.Lock()
	r.active = false
	r.reader = nil
	r.mu.Unlock()
}

var _ PublicationReader = (*publicationReadScope)(nil)
