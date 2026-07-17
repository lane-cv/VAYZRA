package audit

import "context"

type Writer interface {
	Write(context.Context, Event) error
}
type Store interface {
	Writer
	List(context.Context, int, int64) ([]Record, error)
}
