package processing

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/objectstore"
)

func TestPipelineStreamsVerifiesScansAndCreatesRetrySafePDFPreview(t *testing.T) {
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
	wantKey := "previews/" + versionID.String() + "/pdf.pdf"
	if previews.putKey != wantKey || previews.putKey == sources.source.ObjectKey || !bytes.Equal(previews.putBody, body) {
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

func TestPipelineAITextStoresPrivateNormalizedArtifact(t *testing.T) {
	body := []byte("first\r\nsecond\r")
	versionID := uuid.New()
	source := SourceFile{
		VersionID: versionID, Purpose: "ai_attachment", ObjectKey: "originals/never-expose",
		DisplayName: "question.txt", DeclaredMIME: "text/plain", Size: int64(len(body)), SHA256: hashHex(body),
	}
	previews := &recordingBlobStore{}
	artifacts := newArtifactRegistryStub()
	job := Job{ID: uuid.New(), FileVersionID: versionID, Kind: KindProcessFile, Attempts: 1}
	result, err := (&Pipeline{
		Sources: sourceStub{source: source}, Originals: &blobStub{body: body}, Previews: previews,
		Artifacts: artifacts, Runner: &runnerStub{}, WorkRoot: t.TempDir(),
	}).Process(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if result.AIText == nil || result.AIText.Kind != "ai_text" || result.AIText.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("ai text=%+v", result.AIText)
	}
	artifact, ok := previews.puts["ai_text"]
	if !ok || string(artifact.body) != "first\nsecond\n" || artifact.key == source.ObjectKey {
		t.Fatalf("artifact=%+v", artifact)
	}
}

func TestPipelineAITextExtractsOfficeConvertedPDFAndImagesHaveNoText(t *testing.T) {
	t.Run("office", func(t *testing.T) {
		body := officeFixture(t)
		source := SourceFile{
			VersionID: uuid.New(), Purpose: "ai_attachment", ObjectKey: "office",
			DisplayName: "question.docx", DeclaredMIME: allowedTypes[".docx"].mime, Size: int64(len(body)), SHA256: hashHex(body),
		}
		var convertedPDF string
		runner := &runnerStub{hook: func(args []string) {
			switch {
			case len(args) > 0 && args[0] == "--headless":
				out := argAfter(args, "--outdir")
				input := args[len(args)-1]
				convertedPDF = filepath.Join(out, strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))+".pdf")
				_ = os.WriteFile(convertedPDF, []byte("%PDF-1.7\npreview\n%%EOF\n"), 0600)
			case len(args) == 4 && args[0] == "-layout":
				if args[2] != convertedPDF {
					t.Fatalf("pdftotext input=%q converted=%q", args[2], convertedPDF)
				}
				_ = os.WriteFile(args[3], []byte("converted text"), 0600)
			}
		}}
		job := Job{ID: uuid.New(), FileVersionID: source.VersionID, Kind: KindProcessFile, Attempts: 1}
		result, err := (&Pipeline{
			Sources: sourceStub{source: source}, Originals: &blobStub{body: body}, Previews: &recordingBlobStore{},
			Artifacts: newArtifactRegistryStub(), Runner: runner, WorkRoot: t.TempDir(),
		}).Process(context.Background(), job)
		if err != nil || result.AIText == nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("image", func(t *testing.T) {
		body := tinyPNG()
		source := SourceFile{
			VersionID: uuid.New(), Purpose: "ai_attachment", ObjectKey: "image",
			DisplayName: "question.png", DeclaredMIME: "image/png", Size: int64(len(body)), SHA256: hashHex(body),
		}
		job := Job{ID: uuid.New(), FileVersionID: source.VersionID, Kind: KindProcessFile, Attempts: 1}
		result, err := (&Pipeline{
			Sources: sourceStub{source: source}, Originals: &blobStub{body: body}, Previews: &recordingBlobStore{},
			Artifacts: newArtifactRegistryStub(), Runner: &runnerStub{}, WorkRoot: t.TempDir(),
		}).Process(context.Background(), job)
		if err != nil || result.AIText != nil {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

func TestPipelineAmbiguousSecondPutNeverTouchesPreexistingArtifactsAndTracksFailedDeletes(t *testing.T) {
	body := []byte("private question")
	versionID := uuid.New()
	source := SourceFile{
		VersionID: versionID, Purpose: "ai_attachment", ObjectKey: "originals/private",
		DisplayName: "question.txt", DeclaredMIME: "text/plain", Size: int64(len(body)), SHA256: hashHex(body),
	}
	priorAIText := "previews/" + versionID.String() + "/ai_text.txt"
	priorPage := "previews/" + versionID.String() + "/page.txt"
	previews := &recordingBlobStore{
		objects:   map[string][]byte{priorAIText: []byte("valid ai text"), priorPage: []byte("valid page")},
		failPutAt: 2, deleteErr: objectstore.ErrUnavailable,
	}
	artifacts := newArtifactRegistryStub()
	job := Job{ID: uuid.New(), FileVersionID: versionID, Kind: KindProcessFile, Attempts: 2}
	_, err := (&Pipeline{
		Sources: sourceStub{source: source}, Originals: &blobStub{body: body}, Previews: previews,
		Artifacts: artifacts, Runner: &runnerStub{}, WorkRoot: t.TempDir(),
	}).Process(context.Background(), job)
	if category(err) != "storage_unavailable" {
		t.Fatalf("err=%v", err)
	}
	if string(previews.objects[priorAIText]) != "valid ai text" || string(previews.objects[priorPage]) != "valid page" {
		t.Fatalf("preexisting artifacts changed: %v", previews.objects)
	}
	if len(artifacts.pendingKeys()) != 2 {
		t.Fatalf("pending cleanup=%v objects=%v", artifacts.pendingKeys(), previews.objects)
	}
	for _, key := range artifacts.pendingKeys() {
		if !strings.Contains(key, job.ID.String()) || !strings.Contains(key, "/2/") {
			t.Fatalf("key is not attempt-owned: %q", key)
		}
		if _, exists := previews.objects[key]; !exists {
			t.Fatalf("ambiguous object not retained for durable cleanup: %q", key)
		}
	}
}

func TestPipelineAmbiguousFirstPutPreservesPreexistingArtifactAndTracksAttemptObject(t *testing.T) {
	body := []byte("private question")
	versionID := uuid.New()
	source := SourceFile{
		VersionID: versionID, Purpose: "ai_attachment", ObjectKey: "originals/private",
		DisplayName: "question.txt", DeclaredMIME: "text/plain", Size: int64(len(body)), SHA256: hashHex(body),
	}
	prior := "previews/" + versionID.String() + "/ai_text.txt"
	previews := &recordingBlobStore{
		objects:   map[string][]byte{prior: []byte("valid prior")},
		failPutAt: 1, deleteErr: objectstore.ErrUnavailable,
	}
	artifacts := newArtifactRegistryStub()
	job := Job{ID: uuid.New(), FileVersionID: versionID, Kind: KindProcessFile, Attempts: 1}
	_, err := (&Pipeline{
		Sources: sourceStub{source: source}, Originals: &blobStub{body: body}, Previews: previews,
		Artifacts: artifacts, Runner: &runnerStub{}, WorkRoot: t.TempDir(),
	}).Process(context.Background(), job)
	if category(err) != "storage_unavailable" {
		t.Fatalf("err=%v", err)
	}
	if string(previews.objects[prior]) != "valid prior" {
		t.Fatalf("preexisting artifact changed: %v", previews.objects)
	}
	pending := artifacts.pendingKeys()
	if len(pending) != 1 || !strings.Contains(pending[0], job.ID.String()) {
		t.Fatalf("pending cleanup=%v", pending)
	}
}

type recordedPut struct {
	key  string
	body []byte
}

type recordingBlobStore struct {
	puts      map[string]recordedPut
	objects   map[string][]byte
	putCalls  int
	failPutAt int
	deleteErr error
}

func (b *recordingBlobStore) Get(context.Context, string, *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	return nil, objectstore.ObjectInfo{}, errors.New("not used")
}

func (b *recordingBlobStore) Put(_ context.Context, key string, reader io.Reader, size int64, meta objectstore.ObjectMeta) (objectstore.ObjectInfo, error) {
	if b.puts == nil {
		b.puts = make(map[string]recordedPut)
	}
	if b.objects == nil {
		b.objects = make(map[string][]byte)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return objectstore.ObjectInfo{}, err
	}
	kind := "preview"
	if meta.ContentType == "text/plain; charset=utf-8" {
		kind = "ai_text"
	}
	b.puts[kind] = recordedPut{key: key, body: body}
	b.objects[key] = append([]byte(nil), body...)
	b.putCalls++
	if b.putCalls == b.failPutAt {
		return objectstore.ObjectInfo{}, objectstore.ErrUnavailable
	}
	return objectstore.ObjectInfo{Size: size}, nil
}

func (b *recordingBlobStore) Delete(_ context.Context, key string) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	delete(b.objects, key)
	return nil
}

type artifactRegistryStub struct {
	records map[string]ArtifactState
}

func newArtifactRegistryStub() *artifactRegistryStub {
	return &artifactRegistryStub{records: make(map[string]ArtifactState)}
}

func (s *artifactRegistryStub) ReserveArtifact(_ context.Context, artifact ProcessingArtifact) error {
	s.records[artifact.ObjectKey] = ArtifactReserved
	return nil
}

func (s *artifactRegistryStub) MarkArtifactStored(_ context.Context, key string) error {
	s.records[key] = ArtifactStored
	return nil
}

func (s *artifactRegistryStub) MarkArtifactDeletePending(_ context.Context, key string) error {
	s.records[key] = ArtifactDeletePending
	return nil
}

func (s *artifactRegistryStub) ForgetArtifact(_ context.Context, key string) error {
	delete(s.records, key)
	return nil
}

func (s *artifactRegistryStub) pendingKeys() []string {
	var out []string
	for key, state := range s.records {
		if state == ArtifactDeletePending {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func tinyPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0, 0, 0, 13, 'I', 'H', 'D', 'R',
		0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0, 0x90, 0x77, 0x53, 0xde,
		0, 0, 0, 12, 'I', 'D', 'A', 'T', 8, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0, 0, 4, 0, 1, 0, 0, 0, 0,
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
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
func (*blobStub) Delete(context.Context, string) error { return nil }
func hashHex(body []byte) string                       { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }
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
