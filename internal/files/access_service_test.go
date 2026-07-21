package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/objectstore"
)

type accessStoreStub struct {
	delivery Delivery
	err      error
	logs     []AccessLog
	logErr   error
}

func (s *accessStoreStub) ResolveAccess(context.Context, uuid.UUID, uuid.UUID, AccessAction) (Delivery, error) {
	return s.delivery, s.err
}
func (s *accessStoreStub) WriteAccessLog(_ context.Context, l AccessLog) error {
	s.logs = append(s.logs, l)
	return s.logErr
}

type accessObjectsStub struct {
	data      []byte
	requested *objectstore.ByteRange
}

func (s *accessObjectsStub) CreateMultipart(context.Context, string, objectstore.ObjectMeta) (string, error) {
	panic("unused")
}
func (s *accessObjectsStub) PutPart(context.Context, string, string, int, io.Reader, int64, string) (objectstore.Part, error) {
	panic("unused")
}
func (s *accessObjectsStub) CompleteMultipart(context.Context, string, string, []objectstore.Part) (objectstore.ObjectInfo, error) {
	panic("unused")
}
func (s *accessObjectsStub) AbortMultipart(context.Context, string, string) error { panic("unused") }
func (s *accessObjectsStub) Stat(context.Context, string) (objectstore.ObjectInfo, error) {
	panic("unused")
}
func (s *accessObjectsStub) Put(context.Context, string, io.Reader, int64, objectstore.ObjectMeta) (objectstore.ObjectInfo, error) {
	panic("unused")
}
func (s *accessObjectsStub) Delete(context.Context, string) error { panic("unused") }
func (s *accessObjectsStub) Get(_ context.Context, _ string, r *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	s.requested = r
	b := s.data
	if r != nil {
		b = b[r.Offset : r.Offset+r.Length]
	}
	return io.NopCloser(bytes.NewReader(b)), objectstore.ObjectInfo{Size: int64(len(s.data)), ContentType: "video/mp4"}, nil
}

func activeStudent() Principal {
	return Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "request_123", IP: net.ParseIP("192.0.2.1")}
}

func TestAccessPolicyIsolationAndLogging(t *testing.T) {
	version, revision := uuid.New(), uuid.New()
	store := &accessStoreStub{delivery: Delivery{VersionID: version, RevisionID: revision, ObjectKey: "secret-key", DisplayName: "课件.pdf", ContentType: "application/pdf", Size: 4, Policy: PolicyPreview}}
	objects := &accessObjectsStub{data: []byte("data")}
	svc := NewAccessService(store, objects, objects)
	actor := activeStudent()
	got, err := svc.Open(context.Background(), actor, OpenInput{VersionID: version, Action: ActionPreview})
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if len(store.logs) != 1 || store.logs[0].Result != AccessAllowed {
		t.Fatalf("logs=%+v", store.logs)
	}

	_, err = svc.Open(context.Background(), actor, OpenInput{VersionID: version, Action: ActionDownload})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("download err=%v", err)
	}
	if len(store.logs) != 2 || store.logs[1].Result != AccessDenied {
		t.Fatalf("logs=%+v", store.logs)
	}

	store.err = ErrNotFound
	_, err = svc.Open(context.Background(), actor, OpenInput{VersionID: version, Action: ActionPreview})
	if !errors.Is(err, ErrNotFound) || len(store.logs) != 3 || store.logs[2].Result != AccessDenied {
		t.Fatalf("err=%v logs=%+v", err, store.logs)
	}
}

func TestSingleRangeAndMalformedRangesAreBoundedAndLogged(t *testing.T) {
	data := bytes.Repeat([]byte{'x'}, 1024)
	store := &accessStoreStub{delivery: Delivery{VersionID: uuid.New(), RevisionID: uuid.New(), ObjectKey: "opaque", DisplayName: "v.mp4", ContentType: "video/mp4", Size: int64(len(data)), Policy: PolicyDownload, Playable: true}}
	objects := &accessObjectsStub{data: data}
	svc := NewAccessService(store, objects, objects)
	out, err := svc.Open(context.Background(), activeStudent(), OpenInput{VersionID: store.delivery.VersionID, Action: ActionPreview, Range: "bytes=100-199"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(out.Body)
	out.Body.Close()
	if len(b) != 100 || out.Range.Start != 100 || out.Range.End != 199 || objects.requested.Length != 100 {
		t.Fatalf("len=%d range=%+v requested=%+v", len(b), out.Range, objects.requested)
	}
	for _, raw := range []string{"bytes=0-1,3-4", "bytes=-999999999999", "bytes=9999-"} {
		_, err := svc.Open(context.Background(), activeStudent(), OpenInput{VersionID: store.delivery.VersionID, Action: ActionPreview, Range: raw})
		if !errors.Is(err, ErrRangeNotSatisfiable) {
			t.Fatalf("%q err=%v", raw, err)
		}
	}
	if got := store.logs[len(store.logs)-1].Result; got != AccessMalformed {
		t.Fatalf("last result=%s", got)
	}
}

func TestCopyUsesFixed64KiBBuffer(t *testing.T) {
	var dst bytes.Buffer
	src := &maxReadReader{remaining: 200000, max: 64 * 1024}
	if _, err := copyDelivery(&dst, src); err != nil {
		t.Fatal(err)
	}
	if dst.Len() != 200000 {
		t.Fatalf("len=%d", dst.Len())
	}
}

type maxReadReader struct{ remaining, max int }

func (r *maxReadReader) Read(p []byte) (int, error) {
	if len(p) > r.max {
		return 0, errors.New("oversized read")
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	strings.NewReader(strings.Repeat("x", n)).Read(p[:n])
	r.remaining -= n
	return n, nil
}
func TestAccessLogFailureClosesBodyBeforeDelivery(t *testing.T) {
	version := uuid.New()
	body := &trackingReadCloser{Reader: strings.NewReader("secret")}
	store := &accessStoreStub{delivery: Delivery{VersionID: version, RevisionID: uuid.New(), ObjectKey: "opaque", DisplayName: "x", ContentType: "application/pdf", Size: 6, Policy: PolicyDownload}, logErr: errors.New("log unavailable")}
	objects := &bodyObjectStore{body: body}
	svc := NewAccessService(store, objects, objects)
	if _, err := svc.Open(context.Background(), activeStudent(), OpenInput{VersionID: version, Action: ActionDownload}); err == nil || !body.closed {
		t.Fatalf("err=%v closed=%t", err, body.closed)
	}
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error { r.closed = true; return nil }

type bodyObjectStore struct{ body io.ReadCloser }

func (*bodyObjectStore) CreateMultipart(context.Context, string, objectstore.ObjectMeta) (string, error) {
	panic("unused")
}
func (*bodyObjectStore) PutPart(context.Context, string, string, int, io.Reader, int64, string) (objectstore.Part, error) {
	panic("unused")
}
func (*bodyObjectStore) CompleteMultipart(context.Context, string, string, []objectstore.Part) (objectstore.ObjectInfo, error) {
	panic("unused")
}
func (*bodyObjectStore) AbortMultipart(context.Context, string, string) error { panic("unused") }
func (*bodyObjectStore) Stat(context.Context, string) (objectstore.ObjectInfo, error) {
	panic("unused")
}
func (s *bodyObjectStore) Get(context.Context, string, *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	return s.body, objectstore.ObjectInfo{Size: 6, ContentType: "application/pdf"}, nil
}
func (*bodyObjectStore) Put(context.Context, string, io.Reader, int64, objectstore.ObjectMeta) (objectstore.ObjectInfo, error) {
	panic("unused")
}
func (*bodyObjectStore) Delete(context.Context, string) error { panic("unused") }
func TestRangeRejectsSignedAndNonASCIIDigits(t *testing.T) {
	for _, raw := range []string{"bytes=+1-2", "bytes=1-+2", "bytes=-+1", "bytes=１-2", "bytes=1-２"} {
		if _, err := parseByteRange(raw, 100, true); !errors.Is(err, ErrRangeNotSatisfiable) {
			t.Fatalf("range %q err=%v", raw, err)
		}
	}
}

func TestPlaybackAggregationKeyCannotBeClientRotated(t *testing.T) {
	data := bytes.Repeat([]byte{'x'}, 1024)
	store := &accessStoreStub{delivery: Delivery{VersionID: uuid.New(), RevisionID: uuid.New(), ObjectKey: "opaque", DisplayName: "v.mp4", ContentType: "video/mp4", Size: int64(len(data)), Policy: PolicyDownload, Playable: true}}
	objects := &accessObjectsStub{data: data}
	svc := NewAccessService(store, objects, objects)
	svc.now = func() time.Time { return time.Date(2026, 7, 19, 1, 2, 3, 0, time.UTC) }
	actor := activeStudent()
	for _, clientValue := range []string{"", "forged-a", "forged-b", "forged-c"} {
		out, err := svc.Open(context.Background(), actor, OpenInput{VersionID: store.delivery.VersionID, Action: ActionPreview, Range: "bytes=0-9", PlaybackSession: clientValue})
		if err != nil {
			t.Fatal(err)
		}
		out.Body.Close()
	}
	if len(store.logs) != 4 {
		t.Fatalf("logs=%d", len(store.logs))
	}
	want := store.logs[0].PlaybackSessionHash
	if want == "" {
		t.Fatal("missing server aggregation key")
	}
	for _, l := range store.logs[1:] {
		if l.PlaybackSessionHash != want {
			t.Fatalf("client rotated aggregation key: %q vs %q", l.PlaybackSessionHash, want)
		}
	}
}
