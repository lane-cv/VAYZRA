package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"happylearn.local/app/internal/platform/objectstore"
)

func TestLoadFixtureInputRequiresOwnerOnlyFixedFiles(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "fixture.json")
	original := filepath.Join(root, "original.bin")
	preview := filepath.Join(root, "preview.bin")
	writeFixtureFile(t, config, []byte(`{"schemaVersion":1,"endpoint":"minio:9000","accessKey":"fixture-access","secretKey":"fixture-secret","originalKey":"phase5-restore-live/111111111111/original.bin","previewKey":"phase5-restore-live/111111111111/preview.bin"}`))
	writeFixtureFile(t, original, []byte("fixed original bytes"))
	writeFixtureFile(t, preview, []byte("fixed preview bytes"))

	input, err := loadFixtureInput(config, original, preview)
	if err != nil {
		t.Fatal(err)
	}
	if input.config.accessKey != "fixture-access" ||
		input.config.secretKey != "fixture-secret" ||
		string(input.original.bytes) != "fixed original bytes" ||
		string(input.preview.bytes) != "fixed preview bytes" {
		t.Fatalf("unexpected fixture input: %#v", input)
	}

	if err := os.Chmod(config, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFixtureInput(config, original, preview); err == nil {
		t.Fatal("accepted config mode other than 0400")
	}
	if err := os.Chmod(config, 0o400); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "bad.json")
	writeFixtureFile(t, bad, []byte(`{"schemaVersion":1,"endpoint":"minio:9000","accessKey":"fixture-access","secretKey":"fixture-secret","originalKey":"phase5-restore-live/111111111111/original.bin","previewKey":"phase5-restore-live/111111111111/preview.bin","unknown":"forbidden"}`))
	if _, err := loadFixtureInput(bad, original, preview); err == nil {
		t.Fatal("accepted unknown config field")
	}
	link := filepath.Join(root, "fixture.link")
	if err := os.Symlink(config, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFixtureInput(link, original, preview); err == nil {
		t.Fatal("accepted symlink config")
	}
	hardlink := filepath.Join(root, "original.hardlink")
	if err := os.Link(original, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFixtureInput(config, original, preview); err == nil {
		t.Fatal("accepted hardlinked payload")
	}
}

func TestEffectiveUIDMustBeNonRoot(t *testing.T) {
	if err := validateEffectiveUID(501); err != nil {
		t.Fatalf("rejected non-root effective uid: %v", err)
	}
	if err := validateEffectiveUID(0); err == nil {
		t.Fatal("accepted root effective uid")
	}
}

func TestReadOwnerOnlyFixtureFileRejectsHardlinkCreatedAfterOpen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture")
	hardlink := filepath.Join(root, "fixture.hardlink")
	writeFixtureFile(t, path, []byte("fixed bytes"))

	_, err := readOwnerOnlyFixtureFileAt(path, func() {
		if linkErr := os.Link(path, hardlink); linkErr != nil {
			t.Fatal(linkErr)
		}
	})
	if err == nil {
		t.Fatal("accepted descriptor whose link count changed after open")
	}
}

func TestWriteFixtureObjectsPutsStatsAndReadsExactBytes(t *testing.T) {
	original := []byte("fixed original bytes")
	preview := []byte("fixed preview bytes")
	input := fixtureInput{
		config: fixtureConfig{
			endpoint:    "minio:9000",
			accessKey:   "fixture-access",
			secretKey:   "fixture-secret",
			originalKey: "phase5-restore-live/111111111111/original.bin",
			previewKey:  "phase5-restore-live/111111111111/preview.bin",
		},
		original: newFixturePayload(original),
		preview:  newFixturePayload(preview),
	}
	originals := newFixtureStore()
	previews := newFixtureStore()
	var output bytes.Buffer
	if err := writeFixtureObjects(
		context.Background(),
		input,
		originals,
		previews,
		&output,
	); err != nil {
		t.Fatal(err)
	}
	if originals.puts != 1 || originals.stats != 1 || originals.gets != 1 ||
		previews.puts != 1 || previews.stats != 1 || previews.gets != 1 {
		t.Fatalf(
			"original calls=%d/%d/%d preview calls=%d/%d/%d",
			originals.puts, originals.stats, originals.gets,
			previews.puts, previews.stats, previews.gets,
		)
	}
	want := "phase5_restore_fixture: PASS originalBytes=20 previewBytes=19\n"
	if output.String() != want {
		t.Fatalf("output=%q want=%q", output.String(), want)
	}
	for _, forbidden := range []string{
		input.config.endpoint,
		input.config.accessKey,
		input.config.secretKey,
		input.config.originalKey,
		input.config.previewKey,
	} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("output exposed forbidden value %q", forbidden)
		}
	}
}

func TestWriteFixtureObjectsRejectsStatOrReadMismatchWithoutSuccess(t *testing.T) {
	input := fixtureInput{
		config: fixtureConfig{
			endpoint:    "minio:9000",
			accessKey:   "fixture-access",
			secretKey:   "fixture-secret",
			originalKey: "phase5-restore-live/111111111111/original.bin",
			previewKey:  "phase5-restore-live/111111111111/preview.bin",
		},
		original: newFixturePayload([]byte("fixed original bytes")),
		preview:  newFixturePayload([]byte("fixed preview bytes")),
	}
	for _, mutate := range []func(*fixtureStoreStub){
		func(store *fixtureStoreStub) { store.statDelta = 1 },
		func(store *fixtureStoreStub) { store.getSuffix = []byte("corrupt") },
	} {
		originals := newFixtureStore()
		previews := newFixtureStore()
		mutate(previews)
		var output bytes.Buffer
		if err := writeFixtureObjects(
			context.Background(),
			input,
			originals,
			previews,
			&output,
		); err == nil {
			t.Fatal("accepted object evidence mismatch")
		}
		if strings.Contains(output.String(), "PASS") {
			t.Fatalf("failure published success: %q", output.String())
		}
	}
}

func writeFixtureFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
}

func newFixturePayload(content []byte) fixturePayload {
	sum := sha256.Sum256(content)
	return fixturePayload{
		bytes:  append([]byte(nil), content...),
		size:   int64(len(content)),
		sha256: hex.EncodeToString(sum[:]),
	}
}

type fixtureStoreStub struct {
	objects   map[string][]byte
	puts      int
	stats     int
	gets      int
	statDelta int64
	getSuffix []byte
}

func newFixtureStore() *fixtureStoreStub {
	return &fixtureStoreStub{objects: make(map[string][]byte)}
}

func (store *fixtureStoreStub) Put(
	_ context.Context,
	key string,
	reader io.Reader,
	size int64,
	meta objectstore.ObjectMeta,
) (objectstore.ObjectInfo, error) {
	store.puts++
	content, err := io.ReadAll(reader)
	if err != nil {
		return objectstore.ObjectInfo{}, err
	}
	sum := sha256.Sum256(content)
	if int64(len(content)) != size ||
		hex.EncodeToString(sum[:]) != meta.SHA256 {
		return objectstore.ObjectInfo{}, objectstore.ErrConflict
	}
	store.objects[key] = append([]byte(nil), content...)
	return objectstore.ObjectInfo{Size: int64(len(content))}, nil
}

func (store *fixtureStoreStub) Stat(
	_ context.Context,
	key string,
) (objectstore.ObjectInfo, error) {
	store.stats++
	content, ok := store.objects[key]
	if !ok {
		return objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	return objectstore.ObjectInfo{
		Size: int64(len(content)) + store.statDelta,
	}, nil
}

func (store *fixtureStoreStub) Get(
	_ context.Context,
	key string,
	_ *objectstore.ByteRange,
) (io.ReadCloser, objectstore.ObjectInfo, error) {
	store.gets++
	content, ok := store.objects[key]
	if !ok {
		return nil, objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	content = append(append([]byte(nil), content...), store.getSuffix...)
	return io.NopCloser(bytes.NewReader(content)),
		objectstore.ObjectInfo{Size: int64(len(content))}, nil
}
