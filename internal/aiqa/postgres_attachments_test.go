package aiqa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/tests/integration"
)

func TestPostgresAttachmentOwnershipPurposeAndReadiness(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cleanup := func(cleanupCtx context.Context) {
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM file_previews fp USING file_versions fv,files f,users u WHERE fp.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'attachment_%'`); err != nil {
			t.Errorf("cleanup previews: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM file_processing_jobs j USING file_versions fv,files f,users u WHERE j.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'attachment_%'`); err != nil {
			t.Errorf("cleanup jobs: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM file_versions fv USING files f,users u WHERE fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'attachment_%'`); err != nil {
			t.Errorf("cleanup versions: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM files f USING users u WHERE f.created_by=u.id AND u.username LIKE 'attachment_%'`); err != nil {
			t.Errorf("cleanup files: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM users WHERE username LIKE 'attachment_%'`); err != nil {
			t.Errorf("cleanup users: %v", err)
		}
	}
	cleanup(ctx)
	t.Cleanup(func() { cleanup(context.Background()) })
	owner := insertAttachmentStudent(t, ctx, pool, "attachment_owner_")
	other := insertAttachmentStudent(t, ctx, pool, "attachment_other_")
	objects := &attachmentObjectMap{objects: make(map[string][]byte)}
	store := NewPostgresAttachmentStore(pool, objects, objects)

	readyText := insertAttachmentVersion(t, ctx, pool, owner, "ai_attachment", "ready", "text/plain", "ready.txt")
	textKey := "test-ai-preview/" + uuid.NewString()
	objects.objects[textKey] = []byte("private extracted text")
	if _, err := pool.Exec(ctx, `INSERT INTO file_previews(file_version_id,preview_kind,object_key,content_type,size_bytes,sha256,processing_state) VALUES($1,'ai_text',$2,'text/plain; charset=utf-8',$3,$4,'ready')`, readyText, textKey, len(objects.objects[textKey]), attachmentDigest(objects.objects[textKey])); err != nil {
		t.Fatal(err)
	}
	notCleanText := insertAttachmentVersion(t, ctx, pool, owner, "ai_attachment", "ready", "text/plain", "not-clean.txt")
	if _, err := pool.Exec(ctx, `UPDATE file_versions SET scan_result='rejected' WHERE id=$1`, notCleanText); err != nil {
		t.Fatal(err)
	}
	notCleanKey := "test-ai-preview/" + uuid.NewString()
	objects.objects[notCleanKey] = []byte("must not load")
	if _, err := pool.Exec(ctx, `INSERT INTO file_previews(file_version_id,preview_kind,object_key,content_type,size_bytes,sha256,processing_state) VALUES($1,'ai_text',$2,'text/plain; charset=utf-8',$3,$4,'ready')`, notCleanText, notCleanKey, len(objects.objects[notCleanKey]), attachmentDigest(objects.objects[notCleanKey])); err != nil {
		t.Fatal(err)
	}
	pending := insertAttachmentVersion(t, ctx, pool, owner, "ai_attachment", "pending_scan", "", "pending.pdf")
	qa := insertAttachmentVersion(t, ctx, pool, owner, "qa_attachment", "ready", "text/plain", "qa.txt")
	teaching := insertAttachmentVersion(t, ctx, pool, owner, "teaching", "ready", "text/plain", "lesson.txt")
	missingText := insertAttachmentVersion(t, ctx, pool, owner, "ai_attachment", "ready", "application/pdf", "missing.pdf")

	got, err := store.ValidateForAI(ctx, owner, uuid.Nil, []AttachmentInput{{FileVersionID: readyText, SortPosition: 0}})
	if err != nil || len(got) != 1 || got[0].FileVersionID != readyText || got[0].Modality != ModalityText {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got[0].DisplayName == textKey || got[0].DetectedMIME == textKey {
		t.Fatalf("object key leaked: %+v", got[0])
	}
	if text, err := store.LoadAIText(ctx, owner, readyText); err != nil || text != "private extracted text" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	for _, id := range []uuid.UUID{qa, teaching} {
		if _, err := store.ValidateForAI(ctx, owner, uuid.Nil, []AttachmentInput{{FileVersionID: id}}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("wrong purpose id=%s err=%v", id, err)
		}
	}
	if _, err := store.ValidateForAI(ctx, other, uuid.Nil, []AttachmentInput{{FileVersionID: readyText}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner err=%v", err)
	}
	for _, id := range []uuid.UUID{pending, missingText} {
		if _, err := store.ValidateForAI(ctx, owner, uuid.Nil, []AttachmentInput{{FileVersionID: id}}); !errors.Is(err, ErrAttachmentNotReady) {
			t.Fatalf("not-ready id=%s err=%v", id, err)
		}
	}
	if _, err := store.ValidateForAI(ctx, owner, uuid.Nil, []AttachmentInput{{FileVersionID: notCleanText}}); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("not-clean validation err=%v", err)
	}
	if _, err := store.LoadAIText(ctx, owner, notCleanText); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("not-clean text load err=%v", err)
	}
}

func TestPostgresOpenAIImageEnforcesOwnerPurposeScanReadinessAndObjectSize(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cleanupAttachmentFixtures(t, pool)
	owner := insertAttachmentStudent(t, ctx, pool, "attachment_image_owner_")
	other := insertAttachmentStudent(t, ctx, pool, "attachment_image_other_")
	objects := &attachmentObjectMap{objects: make(map[string][]byte)}
	store := NewPostgresAttachmentStore(pool, objects, objects)

	ready := insertAttachmentVersion(t, ctx, pool, owner, "ai_attachment", "ready", "image/png", "ready.png")
	readyKey := attachmentVersionKey(t, ctx, pool, ready)
	objects.objects[readyKey] = []byte("x")
	body, mime, size, err := store.OpenAIImage(ctx, owner, ready)
	if err != nil || mime != "image/png" || size != 1 {
		t.Fatalf("mime=%q size=%d err=%v", mime, size, err)
	}
	got, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || string(got) != "x" {
		t.Fatalf("body=%q read=%v close=%v", got, readErr, closeErr)
	}
	if _, _, _, err := store.OpenAIImage(ctx, other, ready); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner err=%v", err)
	}

	wrongPurpose := insertAttachmentVersion(t, ctx, pool, owner, "qa_attachment", "ready", "image/png", "qa.png")
	objects.objects[attachmentVersionKey(t, ctx, pool, wrongPurpose)] = []byte("x")
	if _, _, _, err := store.OpenAIImage(ctx, owner, wrongPurpose); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong purpose err=%v", err)
	}

	notClean := insertAttachmentVersion(t, ctx, pool, owner, "ai_attachment", "ready", "image/png", "not-clean.png")
	if _, err := pool.Exec(ctx, `UPDATE file_versions SET scan_result='rejected' WHERE id=$1`, notClean); err != nil {
		t.Fatal(err)
	}
	objects.objects[attachmentVersionKey(t, ctx, pool, notClean)] = []byte("x")
	if _, _, _, err := store.OpenAIImage(ctx, owner, notClean); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("not clean err=%v", err)
	}

	notReady := insertAttachmentVersion(t, ctx, pool, owner, "ai_attachment", "pending_scan", "image/png", "pending.png")
	objects.objects[attachmentVersionKey(t, ctx, pool, notReady)] = []byte("x")
	if _, _, _, err := store.OpenAIImage(ctx, owner, notReady); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("not ready err=%v", err)
	}

	mismatch := insertAttachmentVersion(t, ctx, pool, owner, "ai_attachment", "ready", "image/png", "mismatch.png")
	objects.objects[attachmentVersionKey(t, ctx, pool, mismatch)] = []byte("xx")
	if _, _, _, err := store.OpenAIImage(ctx, owner, mismatch); !errors.Is(err, ErrAttachmentNotReady) {
		t.Fatalf("metadata size mismatch err=%v", err)
	}
}

func insertAttachmentStudent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, prefix string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'Attachment student','student','active','hash')`, id, prefix+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertAttachmentVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID, purpose, state, detectedMIME, name string) uuid.UUID {
	t.Helper()
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, owner).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	key := "test-ai-original/" + uuid.NewString()
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,detected_mime,size_bytes,sha256,processing_state,scan_result,created_by) VALUES($1,1,$2,$3,$4,CASE WHEN $5='' THEN 'application/pdf' ELSE $5 END,NULLIF($5,''),1,repeat('a',64),$6,CASE WHEN $6='ready' THEN 'clean' ELSE NULL END,$7) RETURNING id`, fileID, purpose, key, name, detectedMIME, state, owner).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	return versionID
}

func attachmentVersionKey(t *testing.T, ctx context.Context, pool *pgxpool.Pool, versionID uuid.UUID) string {
	t.Helper()
	var key string
	if err := pool.QueryRow(ctx, `SELECT object_key FROM file_versions WHERE id=$1`, versionID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

func attachmentDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func cleanupAttachmentFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	cleanup := func(cleanupCtx context.Context) {
		for _, query := range []string{
			`DELETE FROM file_previews fp USING file_versions fv,files f,users u WHERE fp.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'attachment_%'`,
			`DELETE FROM file_processing_jobs j USING file_versions fv,files f,users u WHERE j.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'attachment_%'`,
			`DELETE FROM file_versions fv USING files f,users u WHERE fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'attachment_%'`,
			`DELETE FROM files f USING users u WHERE f.created_by=u.id AND u.username LIKE 'attachment_%'`,
			`DELETE FROM users WHERE username LIKE 'attachment_%'`,
		} {
			if _, err := pool.Exec(cleanupCtx, query); err != nil {
				t.Errorf("cleanup attachments: %v", err)
			}
		}
	}
	cleanup(context.Background())
	t.Cleanup(func() { cleanup(context.Background()) })
}

type attachmentObjectMap struct {
	objects map[string][]byte
}

func (s *attachmentObjectMap) Get(_ context.Context, key string, _ *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	body, ok := s.objects[key]
	if !ok {
		return nil, objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), objectstore.ObjectInfo{Size: int64(len(body))}, nil
}
