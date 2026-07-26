package aiqa

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/objectstore"
)

func TestAttachmentTextReadCapsTwoMiBAndClosesObject(t *testing.T) {
	closer := &trackedReadCloser{Reader: bytes.NewReader([]byte(strings.Repeat("x", MaxAttachmentTextBytes+1)))}
	store := &PostgresAttachmentStore{previews: &attachmentBlobStub{reader: closer, info: objectstore.ObjectInfo{Size: MaxAttachmentTextBytes}}}
	if _, err := store.readTextObject(context.Background(), "private/key", MaxAttachmentTextBytes); err == nil {
		t.Fatal("oversized text accepted")
	}
	if !closer.closed {
		t.Fatal("preview reader was not closed")
	}
}

func TestAttachmentImageOpenClosesReaderWhenMetadataMismatch(t *testing.T) {
	closer := &trackedReadCloser{Reader: bytes.NewReader([]byte("image"))}
	store := &PostgresAttachmentStore{originals: &attachmentBlobStub{reader: closer, info: objectstore.ObjectInfo{Size: 999}}}
	if _, err := store.openVerifiedImage(context.Background(), "private/image", 5); err == nil {
		t.Fatal("mismatched image accepted")
	}
	if !closer.closed {
		t.Fatal("image reader was not closed on failure")
	}
}

type attachmentBlobStub struct {
	reader io.ReadCloser
	info   objectstore.ObjectInfo
	err    error
}

func (s *attachmentBlobStub) Get(context.Context, string, *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	return s.reader, s.info, s.err
}

type trackedReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackedReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestAttachmentInputValidationRejectsDuplicateAndInvalidPositions(t *testing.T) {
	id := uuid.New()
	for _, in := range [][]AttachmentInput{
		{{FileVersionID: uuid.Nil, SortPosition: 0}},
		{{FileVersionID: id, SortPosition: -1}},
		{{FileVersionID: id, SortPosition: 0}, {FileVersionID: id, SortPosition: 1}},
		{{FileVersionID: uuid.New(), SortPosition: 0}, {FileVersionID: uuid.New(), SortPosition: 0}},
	} {
		if err := validateAttachmentInputs(in); err == nil {
			t.Fatalf("accepted %#v", in)
		}
	}
}
