package objectstore

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound    = errors.New("object not found")
	ErrConflict    = errors.New("object state conflict")
	ErrUnavailable = errors.New("object store unavailable")
)

type ObjectMeta struct {
	ContentType  string
	SHA256       string
	UserMetadata map[string]string
}

type Part struct {
	Number int
	ETag   string
	Size   int64
}

type ObjectInfo struct {
	Size         int64
	ETag         string
	ContentType  string
	LastModified time.Time
}

type ByteRange struct {
	Offset int64
	Length int64
}

type Store interface {
	CreateMultipart(context.Context, string, ObjectMeta) (uploadID string, err error)
	PutPart(context.Context, string, string, int, io.Reader, int64, string) (Part, error)
	CompleteMultipart(context.Context, string, string, []Part) (ObjectInfo, error)
	AbortMultipart(context.Context, string, string) error
	Stat(context.Context, string) (ObjectInfo, error)
	Get(context.Context, string, *ByteRange) (io.ReadCloser, ObjectInfo, error)
	Put(context.Context, string, io.Reader, int64, ObjectMeta) (ObjectInfo, error)
	Delete(context.Context, string) error
}
