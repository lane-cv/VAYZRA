package audit

import "context"

type Writer interface {
	Write(context.Context, Event) error
}

type FilteredReader interface {
	ListFiltered(context.Context, AuditFilter) (AuditPage, error)
}

type Store interface {
	Writer
	FilteredReader
}
