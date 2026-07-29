package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"happylearn.local/app/internal/platform/objectstore"
)

const (
	fixtureConfigPath   = "/run/secrets/restore-live-object-fixture.json"
	fixtureOriginalPath = "/run/fixture/original.bin"
	fixturePreviewPath  = "/run/fixture/preview.bin"
	fixtureMaxFileBytes = 4096
)

var fixtureObjectKeyPattern = regexp.MustCompile(
	`^phase5-restore-live/[a-f0-9]{12}/(original|preview)\.bin$`,
)

type fixtureConfig struct {
	endpoint    string
	accessKey   string
	secretKey   string
	originalKey string
	previewKey  string
}

type fixturePayload struct {
	bytes  []byte
	size   int64
	sha256 string
}

type fixtureInput struct {
	config   fixtureConfig
	original fixturePayload
	preview  fixturePayload
}

type fixtureStore interface {
	Put(
		context.Context,
		string,
		io.Reader,
		int64,
		objectstore.ObjectMeta,
	) (objectstore.ObjectInfo, error)
	Stat(context.Context, string) (objectstore.ObjectInfo, error)
	Get(
		context.Context,
		string,
		*objectstore.ByteRange,
	) (io.ReadCloser, objectstore.ObjectInfo, error)
}

type encodedFixtureConfig struct {
	SchemaVersion int    `json:"schemaVersion"`
	Endpoint      string `json:"endpoint"`
	AccessKey     string `json:"accessKey"`
	SecretKey     string `json:"secretKey"`
	OriginalKey   string `json:"originalKey"`
	PreviewKey    string `json:"previewKey"`
}

func main() {
	if err := validateEffectiveUID(os.Geteuid()); err != nil {
		fmt.Fprintln(os.Stderr, "phase5_restore_fixture: invalid_identity")
		os.Exit(1)
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "phase5_restore_fixture: invalid_arguments")
		os.Exit(2)
	}
	input, err := loadFixtureInput(
		fixtureConfigPath,
		fixtureOriginalPath,
		fixturePreviewPath,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "phase5_restore_fixture: invalid_input")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint:               input.config.endpoint,
		AccessKey:              input.config.accessKey,
		SecretKey:              input.config.secretKey,
		UseTLS:                 false,
		OriginalsBucket:        "happylearn-originals",
		PreviewsBucket:         "happylearn-previews",
		OperationTimeout:       30 * time.Second,
		SkipLifecycleBootstrap: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "phase5_restore_fixture: object_store_unavailable")
		os.Exit(1)
	}
	if err := writeFixtureObjects(
		ctx,
		input,
		stores.Originals,
		stores.Previews,
		os.Stdout,
	); err != nil {
		fmt.Fprintln(os.Stderr, "phase5_restore_fixture: object_verification_failed")
		os.Exit(1)
	}
}

func validateEffectiveUID(euid int) error {
	if euid < 1 {
		return errors.New("fixture helper must be non-root")
	}
	return nil
}

func loadFixtureInput(
	configPath string,
	originalPath string,
	previewPath string,
) (fixtureInput, error) {
	configBytes, err := readOwnerOnlyFixtureFile(configPath)
	if err != nil {
		return fixtureInput{}, err
	}
	var encoded encodedFixtureConfig
	decoder := json.NewDecoder(bytes.NewReader(configBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return fixtureInput{}, errors.New("decode fixture config")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fixtureInput{}, errors.New("decode fixture config")
	}
	if encoded.SchemaVersion != 1 ||
		encoded.Endpoint != "minio:9000" ||
		!strictFixtureCredential(encoded.AccessKey) ||
		!strictFixtureCredential(encoded.SecretKey) ||
		!fixtureObjectKeyPattern.MatchString(encoded.OriginalKey) ||
		!fixtureObjectKeyPattern.MatchString(encoded.PreviewKey) ||
		!strings.HasSuffix(encoded.OriginalKey, "/original.bin") ||
		!strings.HasSuffix(encoded.PreviewKey, "/preview.bin") ||
		strings.TrimSuffix(encoded.OriginalKey, "original.bin") !=
			strings.TrimSuffix(encoded.PreviewKey, "preview.bin") {
		return fixtureInput{}, errors.New("invalid fixture config")
	}
	original, err := loadFixturePayload(originalPath)
	if err != nil {
		return fixtureInput{}, err
	}
	preview, err := loadFixturePayload(previewPath)
	if err != nil {
		return fixtureInput{}, err
	}
	return fixtureInput{
		config: fixtureConfig{
			endpoint:    encoded.Endpoint,
			accessKey:   encoded.AccessKey,
			secretKey:   encoded.SecretKey,
			originalKey: encoded.OriginalKey,
			previewKey:  encoded.PreviewKey,
		},
		original: original,
		preview:  preview,
	}, nil
}

func strictFixtureCredential(value string) bool {
	return len(value) >= 8 &&
		len(value) <= 128 &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func readOwnerOnlyFixtureFile(path string) ([]byte, error) {
	return readOwnerOnlyFixtureFileAt(path, nil)
}

func readOwnerOnlyFixtureFileAt(path string, afterOpen func()) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o400 ||
		info.Size() < 1 ||
		info.Size() > fixtureMaxFileBytes {
		return nil, errors.New("invalid fixture file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return nil, errors.New("invalid fixture owner")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open fixture file")
	}
	if afterOpen != nil {
		afterOpen()
	}
	opened, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, errors.New("changed fixture file")
	}
	openedStat, openedStatOK := opened.Sys().(*syscall.Stat_t)
	if !openedStatOK ||
		int(openedStat.Uid) != os.Geteuid() ||
		openedStat.Nlink != 1 ||
		!os.SameFile(info, opened) ||
		!opened.Mode().IsRegular() ||
		opened.Mode().Perm() != 0o400 ||
		opened.Size() != info.Size() {
		_ = file.Close()
		return nil, errors.New("changed fixture file")
	}
	content, readErr := io.ReadAll(io.LimitReader(
		file,
		fixtureMaxFileBytes+1,
	))
	closeErr := file.Close()
	if readErr != nil ||
		closeErr != nil ||
		len(content) < 1 ||
		len(content) > fixtureMaxFileBytes ||
		int64(len(content)) != info.Size() {
		return nil, errors.New("read fixture file")
	}
	return content, nil
}

func loadFixturePayload(path string) (fixturePayload, error) {
	content, err := readOwnerOnlyFixtureFile(path)
	if err != nil {
		return fixturePayload{}, err
	}
	sum := sha256.Sum256(content)
	return fixturePayload{
		bytes:  content,
		size:   int64(len(content)),
		sha256: hex.EncodeToString(sum[:]),
	}, nil
}

func writeFixtureObjects(
	ctx context.Context,
	input fixtureInput,
	originals fixtureStore,
	previews fixtureStore,
	output io.Writer,
) error {
	if ctx == nil || originals == nil || previews == nil || output == nil {
		return errors.New("invalid fixture writer")
	}
	if err := writeAndVerifyFixtureObject(
		ctx,
		originals,
		input.config.originalKey,
		input.original,
	); err != nil {
		return err
	}
	if err := writeAndVerifyFixtureObject(
		ctx,
		previews,
		input.config.previewKey,
		input.preview,
	); err != nil {
		return err
	}
	_, err := fmt.Fprintf(
		output,
		"phase5_restore_fixture: PASS originalBytes=%d previewBytes=%d\n",
		input.original.size,
		input.preview.size,
	)
	return err
}

func writeAndVerifyFixtureObject(
	ctx context.Context,
	store fixtureStore,
	key string,
	payload fixturePayload,
) error {
	if !fixtureObjectKeyPattern.MatchString(key) ||
		payload.size < 1 ||
		payload.size != int64(len(payload.bytes)) ||
		len(payload.sha256) != sha256.Size*2 {
		return errors.New("invalid object fixture")
	}
	info, err := store.Put(
		ctx,
		key,
		bytes.NewReader(payload.bytes),
		payload.size,
		objectstore.ObjectMeta{
			ContentType: "application/octet-stream",
			SHA256:      payload.sha256,
		},
	)
	if err != nil || info.Size != payload.size {
		return errors.New("put object fixture")
	}
	info, err = store.Stat(ctx, key)
	if err != nil || info.Size != payload.size {
		return errors.New("stat object fixture")
	}
	reader, info, err := store.Get(ctx, key, nil)
	if err != nil || reader == nil || info.Size != payload.size {
		if reader != nil {
			_ = reader.Close()
		}
		return errors.New("get object fixture")
	}
	content, readErr := io.ReadAll(io.LimitReader(reader, payload.size+1))
	closeErr := reader.Close()
	sum := sha256.Sum256(content)
	if readErr != nil ||
		closeErr != nil ||
		int64(len(content)) != payload.size ||
		!bytes.Equal(content, payload.bytes) ||
		hex.EncodeToString(sum[:]) != payload.sha256 {
		return errors.New("verify object fixture")
	}
	return nil
}
