package aiqa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/platform/objectstore"
)

type attachmentBlobStore interface {
	Get(context.Context, string, *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error)
}

type PostgresAttachmentStore struct {
	pool                *pgxpool.Pool
	originals, previews attachmentBlobStore
}

func NewPostgresAttachmentStore(pool *pgxpool.Pool, originals, previews attachmentBlobStore) *PostgresAttachmentStore {
	return &PostgresAttachmentStore{pool: pool, originals: originals, previews: previews}
}

func (s *PostgresAttachmentStore) ValidateForAI(ctx context.Context, studentID, threadID uuid.UUID, inputs []AttachmentInput) ([]AttachmentMetadata, error) {
	if s == nil || s.pool == nil || studentID == uuid.Nil || validateAttachmentInputs(inputs) != nil {
		return nil, ErrInvalidInput
	}
	if threadID != uuid.Nil {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_threads WHERE id=$1 AND student_id=$2)`, threadID, studentID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrNotFound
		}
	}
	type orderedMetadata struct {
		position int
		value    AttachmentMetadata
	}
	ordered := make([]orderedMetadata, 0, len(inputs))
	for _, input := range inputs {
		var metadata AttachmentMetadata
		var state, scanResult string
		var textReady bool
		err := s.pool.QueryRow(ctx, `
SELECT fv.id,fv.display_name,COALESCE(fv.detected_mime,''),fv.size_bytes,fv.processing_state,COALESCE(fv.scan_result,''),
 EXISTS(SELECT 1 FROM file_previews fp WHERE fp.file_version_id=fv.id AND fp.preview_kind='ai_text' AND fp.processing_state='ready')
FROM users u
JOIN files f ON f.created_by=u.id AND f.deleted_at IS NULL
JOIN file_versions fv ON fv.file_id=f.id AND fv.created_by=u.id
WHERE u.id=$1 AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
 AND fv.id=$2 AND fv.purpose='ai_attachment' AND fv.purged_at IS NULL`, studentID, input.FileVersionID).Scan(
			&metadata.FileVersionID, &metadata.DisplayName, &metadata.DetectedMIME, &metadata.Size, &state, &scanResult, &textReady)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if state != "ready" || scanResult != "clean" || metadata.DetectedMIME == "" {
			return nil, ErrAttachmentNotReady
		}
		if isAIImageMIME(metadata.DetectedMIME) {
			metadata.Modality = ModalityVision
		} else {
			metadata.Modality = ModalityText
			if !isAITextMIME(metadata.DetectedMIME) || !textReady {
				return nil, ErrAttachmentNotReady
			}
		}
		ordered = append(ordered, orderedMetadata{position: input.SortPosition, value: metadata})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].position < ordered[j].position })
	out := make([]AttachmentMetadata, len(ordered))
	for i := range ordered {
		out[i] = ordered[i].value
	}
	return out, nil
}

func (s *PostgresAttachmentStore) LoadAIText(ctx context.Context, studentID, versionID uuid.UUID) (string, error) {
	if s == nil || s.pool == nil || s.previews == nil || studentID == uuid.Nil || versionID == uuid.Nil {
		return "", ErrInvalidInput
	}
	var state, scanResult, mime string
	var objectKey *string
	var expectedSize *int64
	var expectedSHA256 *string
	err := s.pool.QueryRow(ctx, `
SELECT fv.processing_state,COALESCE(fv.scan_result,''),COALESCE(fv.detected_mime,''),fp.object_key,fp.size_bytes,fp.sha256
FROM users u JOIN files f ON f.created_by=u.id AND f.deleted_at IS NULL
JOIN file_versions fv ON fv.file_id=f.id AND fv.created_by=u.id
LEFT JOIN file_previews fp ON fp.file_version_id=fv.id AND fp.preview_kind='ai_text' AND fp.processing_state='ready'
WHERE u.id=$1 AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
 AND fv.id=$2 AND fv.purpose='ai_attachment' AND fv.purged_at IS NULL`, studentID, versionID).Scan(&state, &scanResult, &mime, &objectKey, &expectedSize, &expectedSHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if state != "ready" || scanResult != "clean" || !isAITextMIME(mime) || objectKey == nil || expectedSize == nil || expectedSHA256 == nil {
		return "", ErrAttachmentNotReady
	}
	return s.readTextObject(ctx, *objectKey, *expectedSize, *expectedSHA256)
}

func (s *PostgresAttachmentStore) readTextObject(ctx context.Context, objectKey string, expectedSize int64, expectedSHA256 string) (string, error) {
	if s == nil || s.previews == nil || objectKey == "" || expectedSize < 1 || expectedSize > MaxAttachmentTextBytes || len(expectedSHA256) != sha256.Size*2 {
		return "", ErrAttachmentNotReady
	}
	body, info, err := s.previews.Get(ctx, objectKey, nil)
	if err != nil {
		return "", ErrAttachmentNotReady
	}
	if info.Size != expectedSize {
		_ = body.Close()
		return "", ErrAttachmentNotReady
	}
	raw, err := io.ReadAll(io.LimitReader(body, MaxAttachmentTextBytes+1))
	closeErr := body.Close()
	sum := sha256.Sum256(raw)
	if err != nil || closeErr != nil || int64(len(raw)) != expectedSize || len(raw) > MaxAttachmentTextBytes ||
		hex.EncodeToString(sum[:]) != expectedSHA256 || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return "", ErrAttachmentNotReady
	}
	return string(raw), nil
}

func (s *PostgresAttachmentStore) OpenAIImage(ctx context.Context, studentID, versionID uuid.UUID) (io.ReadCloser, string, int64, error) {
	if s == nil || s.pool == nil || s.originals == nil || studentID == uuid.Nil || versionID == uuid.Nil {
		return nil, "", 0, ErrInvalidInput
	}
	var objectKey, state, scanResult, mime string
	var size int64
	err := s.pool.QueryRow(ctx, `
SELECT fv.object_key,fv.processing_state,COALESCE(fv.scan_result,''),COALESCE(fv.detected_mime,''),fv.size_bytes
FROM users u JOIN files f ON f.created_by=u.id AND f.deleted_at IS NULL
JOIN file_versions fv ON fv.file_id=f.id AND fv.created_by=u.id
WHERE u.id=$1 AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
 AND fv.id=$2 AND fv.purpose='ai_attachment' AND fv.purged_at IS NULL`, studentID, versionID).Scan(&objectKey, &state, &scanResult, &mime, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", 0, ErrNotFound
	}
	if err != nil {
		return nil, "", 0, err
	}
	if state != "ready" || scanResult != "clean" || !isAIImageMIME(mime) || size < 1 {
		return nil, "", 0, ErrAttachmentNotReady
	}
	body, err := s.openVerifiedImage(ctx, objectKey, size)
	if err != nil {
		return nil, "", 0, err
	}
	return body, mime, size, nil
}

func (s *PostgresAttachmentStore) openVerifiedImage(ctx context.Context, objectKey string, expectedSize int64) (io.ReadCloser, error) {
	if s == nil || s.originals == nil || objectKey == "" || expectedSize < 1 {
		return nil, ErrAttachmentNotReady
	}
	body, info, err := s.originals.Get(ctx, objectKey, nil)
	if err != nil {
		return nil, ErrAttachmentNotReady
	}
	if info.Size != expectedSize {
		_ = body.Close()
		return nil, ErrAttachmentNotReady
	}
	return body, nil
}

func isAIImageMIME(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return true
	default:
		return false
	}
}

func isAITextMIME(mime string) bool {
	switch mime {
	case "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "text/plain", "text/markdown":
		return true
	default:
		return false
	}
}

var _ AttachmentContextStore = (*PostgresAttachmentStore)(nil)
