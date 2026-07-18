package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/tests/integration"
)

func TestFileSchemaCreatesDurableTablesAndConstraints(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var version int
	if err := pool.QueryRow(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("migration version=%d err=%v", version, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('files','file_versions','file_previews','lesson_draft_files','lesson_revision_files','upload_sessions','upload_parts','file_access_logs')`).Scan(&count); err != nil || count != 8 {
		t.Fatalf("secure file table count=%d err=%v", count, err)
	}

	actorID := insertFileSchemaUser(t, pool, "file_schema_actor")
	insertUpload := func(size int64, state, objectKey, uploadID string) error {
		_, err := pool.Exec(ctx, `INSERT INTO upload_sessions (actor_user_id,object_key,minio_upload_id,display_name,declared_mime,expected_size,expected_sha256,state,expires_at) VALUES ($1,$2,$3,'notes.pdf','application/pdf',$4,$5,$6,now()+interval '1 hour')`, actorID, objectKey, uploadID, size, fmt.Sprintf("%064x", 1), state)
		return err
	}
	if err := insertUpload(0, "open", "schema/zero", "upload-zero"); err == nil {
		t.Fatal("upload session accepted zero-byte expected size")
	}
	if err := insertUpload(524288001, "open", "schema/large", "upload-large"); err == nil {
		t.Fatal("upload session accepted expected size above 500 MiB")
	}
	if err := insertUpload(1, "unknown", "schema/state", "upload-state"); err == nil {
		t.Fatal("upload session accepted unknown state")
	}
	if err := insertUpload(1, "open", "schema/valid", "upload-valid"); err != nil {
		t.Fatal(err)
	}
	var sessionID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM upload_sessions WHERE object_key='schema/valid'`).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO upload_parts (upload_session_id,part_number,size_bytes,sha256,etag) VALUES ($1,1,1,$2,'etag-one'),($1,1,1,$2,'etag-two')`, sessionID, fmt.Sprintf("%064x", 2)); err == nil {
		t.Fatal("upload parts accepted duplicate session part number")
	}
}

func TestFileSchemaRejectsInvalidPoliciesAndImmutableHistoryMutation(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actorID := insertFileSchemaUser(t, pool, "file_history_actor")
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files (created_by) VALUES ($1) RETURNING id`, actorID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions (file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES ($1,1,$2,'notes.pdf','application/pdf',4,$3,'pending_scan',$4) RETURNING id`, fileID, "history/"+uuid.NewString(), fmt.Sprintf("%064x", 3), actorID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	revisionID := insertFileSchemaRevision(t, pool, actorID)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_files (revision_id,file_version_id,access_policy,sort_position) VALUES ($1,$2,'stream',0)`, revisionID, versionID); err == nil {
		t.Fatal("revision file accepted policy outside preview|download")
	}
	var bindingID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO lesson_revision_files (revision_id,file_version_id,access_policy,sort_position) VALUES ($1,$2,'download',0) RETURNING id`, revisionID, versionID).Scan(&bindingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_revision_files SET sort_position=1 WHERE id=$1`, bindingID); err == nil {
		t.Fatal("immutable revision binding was updated")
	}
	var accessID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO file_access_logs (actor_user_id,file_version_id,access_policy,request_id) VALUES ($1,$2,'download','file-schema-request') RETURNING id`, actorID, versionID).Scan(&accessID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM file_access_logs WHERE id=$1`, accessID); err == nil {
		t.Fatal("immutable file access log was deleted")
	}
}

func TestMinIOObjectStoreMultipartRangeAndAbort(t *testing.T) {
	ctx := context.Background()
	endpoint := envOr("HAPPYLEARN_TEST_MINIO_ENDPOINT", "127.0.0.1:59000")
	accessKey := envOr("HAPPYLEARN_TEST_MINIO_ACCESS_KEY", "happylearn_dev")
	secretKey := envOr("HAPPYLEARN_TEST_MINIO_SECRET_KEY", "happylearn_minio_dev_secret")
	suffix := uuid.NewString()
	stores, err := objectstore.NewMinIO(ctx, objectstore.MinIOConfig{
		Endpoint: endpoint, AccessKey: accessKey, SecretKey: secretKey,
		OriginalsBucket: "test-originals-" + suffix, PreviewsBucket: "test-previews-" + suffix,
		OperationTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.DeleteBuckets(context.Background()) })

	payload := []byte("0123456789-range-payload")
	key := uuid.NewString()
	uploadID, err := stores.Originals.CreateMultipart(ctx, key, objectstore.ObjectMeta{ContentType: "application/octet-stream"})
	if err != nil {
		t.Fatal(err)
	}
	part, err := stores.Originals.PutPart(ctx, key, uploadID, 1, bytes.NewReader(payload), int64(len(payload)), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := stores.Originals.CompleteMultipart(ctx, key, uploadID, []objectstore.Part{part})
	if err != nil || completed.Size != int64(len(payload)) {
		t.Fatalf("complete=%+v err=%v", completed, err)
	}
	stat, err := stores.Originals.Stat(ctx, key)
	if err != nil || stat.Size != int64(len(payload)) {
		t.Fatalf("stat=%+v err=%v", stat, err)
	}
	r, info, err := stores.Originals.Get(ctx, key, &objectstore.ByteRange{Offset: 3, Length: 7})
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if readErr != nil || closeErr != nil || string(got) != string(payload[3:10]) || info.Size != int64(len(payload)) {
		t.Fatalf("range=%q info=%+v readErr=%v closeErr=%v", got, info, readErr, closeErr)
	}

	abortedKey := uuid.NewString()
	abortedID, err := stores.Originals.CreateMultipart(ctx, abortedKey, objectstore.ObjectMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Originals.PutPart(ctx, abortedKey, abortedID, 1, bytes.NewReader(payload), int64(len(payload)), ""); err != nil {
		t.Fatal(err)
	}
	if err := stores.Originals.AbortMultipart(ctx, abortedKey, abortedID); err != nil {
		t.Fatal(err)
	}
	if _, err := stores.Originals.Stat(ctx, abortedKey); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("aborted object stat error=%v, want ErrNotFound", err)
	}
	if err := stores.Originals.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
}

func insertFileSchemaUser(t *testing.T, pool *pgxpool.Pool, prefix string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	username := prefix + "_" + uuid.NewString()
	if err := pool.QueryRow(context.Background(), `INSERT INTO users (username,display_name,role,status,password_hash) VALUES ($1,'File schema actor','student','active','hash') RETURNING id`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertFileSchemaRevision(t *testing.T, pool *pgxpool.Pool, actorID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var gradeID, termID, subjectID, chapterID, lessonID, revisionID uuid.UUID
	name := uuid.NewString()
	if err := pool.QueryRow(ctx, `INSERT INTO grades (name) VALUES ($1) RETURNING id`, "Grade "+name).Scan(&gradeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO terms (grade_id,name) VALUES ($1,$2) RETURNING id`, gradeID, "Term "+name).Scan(&termID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO subjects (term_id,name) VALUES ($1,$2) RETURNING id`, termID, "Subject "+name).Scan(&subjectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO chapters (subject_id,name) VALUES ($1,$2) RETURNING id`, subjectID, "Chapter "+name).Scan(&chapterID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO lessons (chapter_id) VALUES ($1) RETURNING id`, chapterID).Scan(&lessonID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO lesson_revisions (lesson_id,version,source_draft_version,title,published_by) VALUES ($1,1,1,'File lesson',$2) RETURNING id`, lessonID, actorID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	return revisionID
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
