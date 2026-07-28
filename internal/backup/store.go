package backup

import (
	"context"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/operations"
)

type Store interface {
	Create(context.Context, CreateInput) (Run, error)
	Claim(context.Context, uuid.UUID, time.Duration) (Run, error)
	Renew(context.Context, uuid.UUID, uuid.UUID, int64, time.Duration) (Run, error)
	Transition(context.Context, TransitionInput) (Run, error)
	AddArtifact(context.Context, Artifact) error
	List(context.Context, Filter) ([]RunSummary, Cursor, error)
	Get(context.Context, uuid.UUID) (RunDetail, error)
	RetentionCandidates(context.Context, RetentionPolicy) ([]Artifact, error)
}

type HTTPService interface {
	RequestManual(context.Context, operations.Principal, string) (Run, error)
	List(context.Context, operations.Principal, Filter) (Page, error)
	Get(context.Context, operations.Principal, uuid.UUID) (RunDetail, error)
}
