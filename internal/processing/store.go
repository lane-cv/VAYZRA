package processing

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Store interface {
	LeaseNext(context.Context, string, time.Time, time.Duration) (Job, error)
	Heartbeat(context.Context, uuid.UUID, string, time.Time) error
	Complete(context.Context, Job, Result) error
	Fail(context.Context, Job, Failure) error
}

type Processor interface {
	Process(context.Context, Job) (Result, error)
}
