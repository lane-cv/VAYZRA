package processing

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/objectstore"
)

func TestPipelineStreamsVerifiesScansAndCreatesRandomPDFPreview(t *testing.T) {
	body := []byte("%PDF-1.7\nlesson\n%%EOF\n")
	versionID := uuid.New()
	sources := sourceStub{source: SourceFile{VersionID: versionID, ObjectKey: "originals/private-key", DisplayName: "lesson.pdf", DeclaredMIME: "application/pdf", Size: int64(len(body)), SHA256: hashHex(body)}}
	originals := &blobStub{body: body}
	previews := &blobStub{}
	runner := &runnerStub{exit: 0}
	result, err := (&Pipeline{Sources: sources, Originals: originals, Previews: previews, Runner: runner, WorkRoot: t.TempDir()}).Process(context.Background(), Job{FileVersionID: versionID, Kind: KindProcessFile})
	if err != nil {
		t.Fatal(err)
	}
	if result.DetectedMIME != "application/pdf" || result.ScanResult != "clean" || result.Preview == nil || result.Preview.Kind != "pdf" {
		t.Fatalf("result=%+v", result)
	}
	if previews.putKey == "" || previews.putKey == sources.source.ObjectKey || !strings.Contains(previews.putKey, versionID.String()) || !bytes.Equal(previews.putBody, body) {
		t.Fatalf("preview key=%q body=%q", previews.putKey, previews.putBody)
	}
	if runner.args[len(runner.args)-1] == sources.source.DisplayName || strings.Contains(runner.args[len(runner.args)-1], sources.source.ObjectKey) {
		t.Fatalf("unsafe scan path=%q", runner.args[len(runner.args)-1])
	}
}

func TestPipelineRejectsObjectSizeAndHashMismatchBeforeScanner(t *testing.T) {
	body := []byte("%PDF-1.7\nlesson\n%%EOF\n")
	for _, source := range []SourceFile{
		{VersionID: uuid.New(), ObjectKey: "one", DisplayName: "lesson.pdf", DeclaredMIME: "application/pdf", Size: int64(len(body) - 1), SHA256: hashHex(body)},
		{VersionID: uuid.New(), ObjectKey: "two", DisplayName: "lesson.pdf", DeclaredMIME: "application/pdf", Size: int64(len(body)), SHA256: strings.Repeat("0", 64)},
	} {
		runner := &runnerStub{}
		_, err := (&Pipeline{Sources: sourceStub{source: source}, Originals: &blobStub{body: body}, Previews: &blobStub{}, Runner: runner, WorkRoot: t.TempDir()}).Process(context.Background(), Job{FileVersionID: source.VersionID, Kind: KindProcessFile})
		if category(err) != "object_mismatch" || runner.executable != "" {
			t.Fatalf("source=%+v err=%v scanner=%q", source, err, runner.executable)
		}
	}
}

func TestPipelineFailsClosedBeforeScanningWithStaleDefinitions(t *testing.T) {
	body := []byte("plain text")
	versionID := uuid.New()
	source := SourceFile{VersionID: versionID, ObjectKey: "text", DisplayName: "lesson.txt", DeclaredMIME: "text/plain", Size: int64(len(body)), SHA256: hashHex(body)}
	runner := &runnerStub{}
	_, err := (&Pipeline{
		Sources: sourceStub{source: source}, Originals: &blobStub{body: body}, Previews: &blobStub{}, Runner: runner,
		WorkRoot: t.TempDir(), ClamDefinitionsDir: t.TempDir(), Now: func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) },
	}).Process(context.Background(), Job{FileVersionID: versionID, Kind: KindProcessFile})
	if category(err) != "scanner_definitions_stale" || runner.executable != "" {
		t.Fatalf("err=%v executable=%q", err, runner.executable)
	}
}

func TestPipelineConvertsOfficeAndProbesVideoWithoutTranscoding(t *testing.T) {
	t.Run("office", func(t *testing.T) {
		body := officeFixture(t)
		source := SourceFile{VersionID: uuid.New(), ObjectKey: "office", DisplayName: "lesson.docx", DeclaredMIME: allowedTypes[".docx"].mime, Size: int64(len(body)), SHA256: hashHex(body)}
		calls := 0
		runner := &runnerStub{hook: func(args []string) {
			calls++
			if len(args) > 0 && args[0] == "--headless" {
				out := argAfter(args, "--outdir")
				input := args[len(args)-1]
				base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
				_ = os.WriteFile(filepath.Join(out, base+".pdf"), []byte("%PDF-1.7\npreview\n%%EOF\n"), 0600)
			}
		}}
		result, err := (&Pipeline{Sources: sourceStub{source: source}, Originals: &blobStub{body: body}, Previews: &blobStub{}, Runner: runner, WorkRoot: t.TempDir()}).Process(context.Background(), Job{FileVersionID: source.VersionID, Kind: KindProcessFile})
		if err != nil || result.Preview == nil || result.Preview.ContentType != "application/pdf" || calls != 2 {
			t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
		}
	})
	t.Run("video", func(t *testing.T) {
		body := append([]byte{0, 0, 0, 24}, []byte("ftypisom00000000")...)
		source := SourceFile{VersionID: uuid.New(), ObjectKey: "video", DisplayName: "lesson.mp4", DeclaredMIME: "video/mp4", Size: int64(len(body)), SHA256: hashHex(body)}
		runner := &runnerStub{stdout: []byte(`{"format":{"format_name":"mp4","duration":"1.5"},"streams":[{"codec_type":"video","codec_name":"h264","width":1280,"height":720}]}`)}
		result, err := (&Pipeline{Sources: sourceStub{source: source}, Originals: &blobStub{body: body}, Previews: &blobStub{}, Runner: runner, WorkRoot: t.TempDir()}).Process(context.Background(), Job{FileVersionID: source.VersionID, Kind: KindProcessFile})
		if err != nil || !result.BrowserPlayable || result.VideoDurationMS == nil || *result.VideoDurationMS != 1500 || result.Preview == nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

type sourceStub struct {
	source SourceFile
	err    error
}

func (s sourceStub) LoadSource(context.Context, uuid.UUID) (SourceFile, error) {
	return s.source, s.err
}

type blobStub struct {
	body, putBody  []byte
	putKey         string
	getErr, putErr error
}

func (b *blobStub) Get(context.Context, string, *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	return io.NopCloser(bytes.NewReader(b.body)), objectstore.ObjectInfo{Size: int64(len(b.body))}, b.getErr
}
func (b *blobStub) Put(_ context.Context, key string, reader io.Reader, size int64, _ objectstore.ObjectMeta) (objectstore.ObjectInfo, error) {
	b.putKey = key
	b.putBody, _ = io.ReadAll(reader)
	return objectstore.ObjectInfo{Size: size}, b.putErr
}
func hashHex(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
func officeFixture(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for name, body := range map[string]string{"[Content_Types].xml": `<Types/>`, "word/document.xml": `<document/>`} {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
