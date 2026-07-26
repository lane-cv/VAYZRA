package processing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/objectstore"
)

type SourceStore interface {
	LoadSource(context.Context, uuid.UUID) (SourceFile, error)
}
type BlobStore interface {
	Get(context.Context, string, *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error)
	Put(context.Context, string, io.Reader, int64, objectstore.ObjectMeta) (objectstore.ObjectInfo, error)
	Delete(context.Context, string) error
}
type Pipeline struct {
	Sources             SourceStore
	Originals, Previews BlobStore
	Runner              Runner
	WorkRoot            string
	ClamDefinitionsDir  string
	Now                 func() time.Time
}

func (p *Pipeline) Process(ctx context.Context, job Job) (Result, error) {
	if p == nil || p.Sources == nil || p.Originals == nil || p.Previews == nil || p.Runner == nil || p.WorkRoot == "" || job.Kind != KindProcessFile || job.FileVersionID == uuid.Nil {
		return Result{}, transient("pipeline_unavailable")
	}
	source, err := p.Sources.LoadSource(ctx, job.FileVersionID)
	if err != nil || source.VersionID != job.FileVersionID {
		return Result{}, transient("database_unavailable")
	}
	if source.Size < 1 || source.Size > MaxProcessFileSize || len(source.SHA256) != sha256.Size*2 {
		return Result{}, reject("object_mismatch")
	}
	jobDir, err := os.MkdirTemp(p.WorkRoot, "job-")
	if err != nil {
		return Result{}, transient("workspace_unavailable")
	}
	defer os.RemoveAll(jobDir)
	if err = os.Chmod(jobDir, 0700); err != nil {
		return Result{}, transient("workspace_unavailable")
	}
	ext := strings.ToLower(filepath.Ext(source.DisplayName))
	file, err := os.CreateTemp(jobDir, "input-*"+ext)
	if err != nil {
		return Result{}, transient("workspace_unavailable")
	}
	inputPath := file.Name()
	body, info, err := p.Originals.Get(ctx, source.ObjectKey, nil)
	if err != nil {
		file.Close()
		return Result{}, transient("storage_unavailable")
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(body, source.Size+1))
	closeBodyErr := body.Close()
	closeFileErr := file.Close()
	if copyErr != nil || closeBodyErr != nil || closeFileErr != nil {
		return Result{}, transient("storage_unavailable")
	}
	if written != source.Size || (info.Size > 0 && info.Size != source.Size) || hex.EncodeToString(hasher.Sum(nil)) != source.SHA256 {
		return Result{}, reject("object_mismatch")
	}
	detected, err := DetectFile(inputPath, source.DisplayName, source.DeclaredMIME, source.Size)
	if err != nil {
		return Result{}, err
	}
	if p.ClamDefinitionsDir != "" {
		now := time.Now()
		if p.Now != nil {
			now = p.Now()
		}
		if !ClamDefinitionsFresh(p.ClamDefinitionsDir, MaxClamDefinitionAge, now) {
			return Result{}, transient("scanner_definitions_stale")
		}
	}
	if err = (Scanner{Runner: p.Runner}).Scan(ctx, inputPath); err != nil {
		return Result{}, err
	}
	if detected.Kind == KindDocument {
		if err = (PDFValidator{Runner: p.Runner}).Validate(ctx, inputPath); err != nil {
			return Result{}, err
		}
	}
	result := Result{DetectedMIME: detected.MIME, ScanResult: "clean"}
	previewPath, previewKind, previewType := "", "", ""
	switch detected.Kind {
	case KindOffice:
		previewPath, err = (OfficeConverter{Runner: p.Runner}).Convert(ctx, inputPath, jobDir)
		previewKind, previewType = "pdf", "application/pdf"
	case KindDocument:
		previewPath, previewKind, previewType = inputPath, "pdf", detected.MIME
	case KindImage, KindText:
		previewPath, previewKind, previewType = inputPath, "page", detected.MIME
	case KindVideo:
		var probe VideoProbe
		probe, err = (VideoProber{Runner: p.Runner}).Probe(ctx, inputPath)
		if err == nil {
			duration, width, height := probe.DurationMS, probe.Width, probe.Height
			result.BrowserPlayable = probe.BrowserPlayable
			result.VideoContainer = probe.Container
			result.VideoCodec = probe.Codec
			result.VideoDurationMS = &duration
			result.VideoWidth = &width
			result.VideoHeight = &height
			if probe.BrowserPlayable {
				previewPath, previewKind, previewType = inputPath, "poster", detected.MIME
			}
		}
	}
	if err != nil {
		return Result{}, err
	}
	if source.Purpose == "ai_attachment" {
		var text string
		switch detected.Kind {
		case KindImage:
		case KindText:
			text, err = readAITextFile(inputPath)
		case KindDocument, KindOffice:
			extractInput := inputPath
			if detected.Kind == KindOffice {
				extractInput = previewPath
			}
			text, err = ExtractPDFText(ctx, p.Runner, extractInput, filepath.Join(jobDir, "ai-extracted.txt"))
		default:
			err = reject("type_rejected")
		}
		if err != nil {
			return Result{}, err
		}
		if detected.Kind != KindImage && text == "" {
			return Result{}, reject("text_extraction_failed")
		}
		if text != "" {
			normalizedPath := filepath.Join(jobDir, "ai-text.txt")
			if err = os.WriteFile(normalizedPath, []byte(text), 0600); err != nil {
				return Result{}, transient("workspace_unavailable")
			}
			aiText, storeErr := p.storePreview(ctx, source.VersionID, normalizedPath, "ai_text", "text/plain; charset=utf-8")
			if storeErr != nil {
				return Result{}, storeErr
			}
			result.AIText = &aiText
		}
	}
	if previewPath != "" {
		preview, previewErr := p.storePreview(ctx, source.VersionID, previewPath, previewKind, previewType)
		if previewErr != nil {
			if result.AIText != nil {
				p.deletePreview(ctx, result.AIText.ObjectKey)
			}
			return Result{}, previewErr
		}
		result.Preview = &preview
	}
	return result, nil
}

func (p *Pipeline) storePreview(ctx context.Context, versionID uuid.UUID, path, kind, contentType string) (PreviewResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return PreviewResult{}, transient("preview_unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() < 1 || info.Size() > MaxProcessFileSize {
		return PreviewResult{}, transient("preview_unavailable")
	}
	hasher := sha256.New()
	if _, err = io.Copy(hasher, file); err != nil {
		return PreviewResult{}, transient("preview_unavailable")
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return PreviewResult{}, transient("preview_unavailable")
	}
	ext := filepath.Ext(path)
	if ext == "" {
		ext = map[string]string{"pdf": ".pdf", "page": ".bin", "poster": ".bin", "ai_text": ".txt"}[kind]
	}
	key := "previews/" + versionID.String() + "/" + kind + ext
	stored, err := p.Previews.Put(ctx, key, file, info.Size(), objectstore.ObjectMeta{ContentType: contentType, SHA256: sum})
	if err != nil || stored.Size != 0 && stored.Size != info.Size() {
		p.deletePreview(ctx, key)
		return PreviewResult{}, transient("storage_unavailable")
	}
	return PreviewResult{Kind: kind, ObjectKey: key, ContentType: contentType, Size: info.Size(), SHA256: sum}, nil
}

func (p *Pipeline) deletePreview(ctx context.Context, key string) {
	if p == nil || p.Previews == nil || key == "" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = p.Previews.Delete(cleanupCtx, key)
}

var _ Processor = (*Pipeline)(nil)
