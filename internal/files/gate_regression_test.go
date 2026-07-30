package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/internal/platform/safelog"
)

type gateAccessObjects struct {
	objectstore.Store
	body io.ReadCloser
	info objectstore.ObjectInfo
	err  error
}

func (s gateAccessObjects) Get(context.Context, string, *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	return s.body, s.info, s.err
}

func TestAccessNegativePathsFailClosedWhenAuditWriteFails(t *testing.T) {
	version := uuid.New()
	baseDelivery := Delivery{VersionID: version, RevisionID: uuid.New(), ObjectKey: "must-not-leak", DisplayName: "x", ContentType: "application/pdf", Size: 4, Policy: PolicyDownload}
	active := activeStudent()
	in := OpenInput{VersionID: version, Action: ActionPreview}
	cases := []struct {
		name    string
		actor   Principal
		input   OpenInput
		store   *accessStoreStub
		objects objectstore.Store
	}{
		{"invalid actor", Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}, in, &accessStoreStub{}, gateAccessObjects{}},
		{"resolve deny", active, in, &accessStoreStub{err: ErrNotFound}, gateAccessObjects{}},
		{"policy deny", active, OpenInput{VersionID: version, Action: ActionDownload}, &accessStoreStub{delivery: func() Delivery { d := baseDelivery; d.Policy = PolicyPreview; return d }()}, gateAccessObjects{}},
		{"malformed range", active, OpenInput{VersionID: version, Action: ActionPreview, Range: "bytes=0-1,2-3"}, &accessStoreStub{delivery: func() Delivery { d := baseDelivery; d.Playable = true; return d }()}, gateAccessObjects{}},
		{"object failure", active, in, &accessStoreStub{delivery: baseDelivery}, gateAccessObjects{err: errors.New("backend secret")}},
		{"object mismatch", active, in, &accessStoreStub{delivery: baseDelivery}, gateAccessObjects{body: io.NopCloser(strings.NewReader("bad")), info: objectstore.ObjectInfo{Size: 3}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.store.logErr = errors.New("audit database secret")
			_, err := NewAccessService(tc.store, tc.objects, tc.objects).Open(context.Background(), tc.actor, tc.input)
			if !errors.Is(err, ErrAccessUnavailable) {
				t.Fatalf("error=%v, want opaque access unavailable", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("leaked infrastructure detail: %v", err)
			}
			if len(tc.store.logs) != 1 || tc.store.logs[0].Result == AccessAllowed {
				t.Fatalf("negative audit=%+v", tc.store.logs)
			}
		})
	}
}

func TestTransferAuditFailureEmitsFixedRedactedStructuredEvent(t *testing.T) {
	var logs bytes.Buffer
	logger, err := safelog.New(&logs, time.Now, "backend secret", "originals/private-key")
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}
	opened := OpenedFile{
		Body: io.NopCloser(&failingGateReader{}),
		ReportFailure: func(context.Context, string) error {
			return errors.New("backend secret at originals/private-key")
		},
	}
	logCategory := func(category string) {
		logger.Error("file.transfer.audit", safelog.Field{
			Name:  "category",
			Value: category,
		})
	}
	if err := deliverOpenedFileWithLog(context.Background(), io.Discard, opened, logCategory); err == nil {
		t.Fatal("expected stream failure")
	}
	got := logs.String()
	if !strings.Contains(got, `"event":"file.transfer.audit"`) ||
		!strings.Contains(got, `"category":"write_failed"`) {
		t.Fatalf("missing fixed event: %q", got)
	}
	for _, secret := range []string{"backend secret", "originals/private-key", "stream_read"} {
		if strings.Contains(got, secret) {
			t.Fatalf("log leaked detail %q: %q", secret, got)
		}
	}
}

type failingGateReader struct{}

func (*failingGateReader) Read([]byte) (int, error) { return 0, errors.New("object backend secret") }

type streamZeroReader struct {
	remaining int64
	maxRead   int
}

func (r *streamZeroReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		return 0, errors.New("oversized read")
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	clear(p[:int(n)])
	r.remaining -= n
	return int(n), nil
}

type boundedDiscardWriter struct {
	maxWrite int
	total    int64
}

func (w *boundedDiscardWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		return 0, errors.New("oversized write")
	}
	w.total += int64(len(p))
	return len(p), nil
}

func TestCopyDeliveryStreamsActual500MiBWithBoundedMemory(t *testing.T) {
	const size = int64(500 * 1024 * 1024)
	src := &streamZeroReader{remaining: size, maxRead: 64 * 1024}
	dst := &boundedDiscardWriter{maxWrite: 64 * 1024}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	n, err := copyDelivery(dst, src)
	runtime.ReadMemStats(&after)
	if err != nil || n != size || dst.total != size {
		t.Fatalf("copied=%d written=%d err=%v", n, dst.total, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2*1024*1024 {
		t.Fatalf("allocated=%d bytes while streaming 500 MiB", allocated)
	}
}

func TestOpenEndedRangeEOFExceptionRemainsBoundedByMaximumFileSize(t *testing.T) {
	rng, err := parseByteRange("bytes=0-", MaxUploadSize, true)
	if err != nil || rng.Start != 0 || rng.End != MaxUploadSize-1 {
		t.Fatalf("range=%+v err=%v", rng, err)
	}
	if _, err := parseByteRange("bytes=0-67108864", MaxUploadSize, true); !errors.Is(err, ErrRangeNotSatisfiable) {
		t.Fatalf("non-EOF oversized range err=%v", err)
	}
}
