package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/auth"
	securefiles "happylearn.local/app/internal/files"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/internal/teaching"
	"happylearn.local/app/tests/integration"
)

func TestFileSchemaCreatesDurableTablesAndConstraints(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var version int
	if err := pool.QueryRow(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 8 {
		t.Fatalf("migration version=%d err=%v", version, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('files','file_versions','file_previews','lesson_draft_files','lesson_revision_files','upload_sessions','upload_parts','file_access_logs')`).Scan(&count); err != nil || count != 8 {
		t.Fatalf("secure file table count=%d err=%v", count, err)
	}

	actorID := insertFileSchemaUser(t, pool, "file_schema_actor")
	suffix := uuid.NewString()
	insertUpload := func(size int64, state, objectKey, uploadID string) error {
		objectKey += "/" + suffix
		uploadID += "-" + suffix
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
	if err := pool.QueryRow(ctx, `SELECT id FROM upload_sessions WHERE object_key=$1`, "schema/valid/"+suffix).Scan(&sessionID); err != nil {
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
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_files (revision_id,file_version_id,access_policy,sort_position,display_name,description) VALUES ($1,$2,'stream',0,'notes.pdf','')`, revisionID, versionID); err == nil {
		t.Fatal("revision file accepted policy outside preview|download")
	}
	var bindingID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO lesson_revision_files (revision_id,file_version_id,access_policy,sort_position,display_name,description) VALUES ($1,$2,'download',0,'notes.pdf','') RETURNING id`, revisionID, versionID).Scan(&bindingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_revision_files SET sort_position=1 WHERE id=$1`, bindingID); err == nil {
		t.Fatal("immutable revision binding was updated")
	}
	var accessID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO file_access_logs (actor_user_id,file_version_id,access_policy,request_id,requested_file_version_id,result,reason_code,ip,playback_session_hash) VALUES ($1,$2,'download','file-schema-request',$2,'allow','','192.0.2.1','') RETURNING id`, actorID, versionID).Scan(&accessID); err != nil {
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
		OperationTimeout: 10 * time.Second, SkipLifecycleBootstrap: true,
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

func TestReplaceFileMovesReadyUploadIntoExistingFile(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actorID := insertFileSchemaUser(t, pool, "replace_actor")
	revisionID := insertFileSchemaRevision(t, pool, actorID)
	var lessonID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT lesson_id FROM lesson_revisions WHERE id=$1`, revisionID).Scan(&lessonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_drafts(lesson_id,title,body_markdown,lock_version,updated_by) VALUES($1,'Replace lesson','body',1,$2)`, lessonID, actorID); err != nil {
		t.Fatal(err)
	}
	var originalFileID, originalVersionID, uploadedFileID, uploadedVersionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actorID).Scan(&originalFileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,detected_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,'original.docx','application/vnd.openxmlformats-officedocument.wordprocessingml.document','application/vnd.openxmlformats-officedocument.wordprocessingml.document',4,$3,'ready',$4) RETURNING id`, originalFileID, "original/"+uuid.NewString(), fmt.Sprintf("%064x", 81), actorID).Scan(&originalVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_files(lesson_id,file_version_id,access_policy,sort_position,display_name,description) VALUES($1,$2,'download',10,'original.docx','')`, lessonID, originalVersionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actorID).Scan(&uploadedFileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,detected_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,'replacement.docx','application/vnd.openxmlformats-officedocument.wordprocessingml.document','application/vnd.openxmlformats-officedocument.wordprocessingml.document',4,$3,'ready',$4) RETURNING id`, uploadedFileID, "original/"+uuid.NewString(), fmt.Sprintf("%064x", 82), actorID).Scan(&uploadedVersionID); err != nil {
		t.Fatal(err)
	}
	actor := securefiles.Principal{User: auth.User{ID: actorID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "replace-request", IP: net.ParseIP("192.0.2.82")}
	if err := securefiles.NewPostgresStore(pool).ReplaceFile(ctx, actor, originalFileID, uploadedVersionID, time.Now().Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var currentFileID uuid.UUID
	var currentVersion int
	if err := pool.QueryRow(ctx, `SELECT file_id,version FROM file_versions WHERE id=$1`, uploadedVersionID).Scan(&currentFileID, &currentVersion); err != nil || currentFileID != originalFileID || currentVersion != 2 {
		t.Fatalf("file=%s version=%d err=%v", currentFileID, currentVersion, err)
	}
}

func TestReplaceFileRejectsUndeliverableUpload(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actorID := insertFileSchemaUser(t, pool, "replace_reject_actor")
	actor := securefiles.Principal{User: auth.User{ID: actorID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "replace-reject-request", IP: net.ParseIP("192.0.2.83")}

	for _, tc := range []struct {
		name   string
		state  string
		policy string
	}{
		{name: "pending download", state: "pending_scan", policy: "download"},
		{name: "rejected download", state: "rejected", policy: "download"},
		{name: "preview without deliverable representation", state: "ready", policy: "preview"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			revisionID := insertFileSchemaRevision(t, pool, actorID)
			var lessonID uuid.UUID
			if err := pool.QueryRow(ctx, `SELECT lesson_id FROM lesson_revisions WHERE id=$1`, revisionID).Scan(&lessonID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO lesson_drafts(lesson_id,title,body_markdown,lock_version,updated_by) VALUES($1,'Replace rejection','body',1,$2)`, lessonID, actorID); err != nil {
				t.Fatal(err)
			}
			var originalFileID, originalVersionID, uploadedFileID, uploadedVersionID uuid.UUID
			if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actorID).Scan(&originalFileID); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,detected_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,'original.pdf','application/pdf','application/pdf',4,$3,'ready',$4) RETURNING id`, originalFileID, "original/"+uuid.NewString(), fmt.Sprintf("%064x", 91), actorID).Scan(&originalVersionID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_files(lesson_id,file_version_id,access_policy,sort_position,display_name,description) VALUES($1,$2,$3,10,'original.pdf','')`, lessonID, originalVersionID, tc.policy); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actorID).Scan(&uploadedFileID); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,detected_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,'replacement.pdf','application/pdf','application/pdf',4,$3,$4,$5) RETURNING id`, uploadedFileID, "original/"+uuid.NewString(), fmt.Sprintf("%064x", 92), tc.state, actorID).Scan(&uploadedVersionID); err != nil {
				t.Fatal(err)
			}

			err := securefiles.NewPostgresStore(pool).ReplaceFile(ctx, actor, originalFileID, uploadedVersionID, time.Now().Add(30*24*time.Hour))
			if !errors.Is(err, securefiles.ErrAccessUnavailable) {
				t.Fatalf("ReplaceFile() err=%v, want ErrAccessUnavailable", err)
			}
			var currentFileID uuid.UUID
			var currentVersion int
			if err := pool.QueryRow(ctx, `SELECT file_id,version FROM file_versions WHERE id=$1`, uploadedVersionID).Scan(&currentFileID, &currentVersion); err != nil || currentFileID != uploadedFileID || currentVersion != 1 {
				t.Fatalf("replacement moved after rejection: file=%s version=%d err=%v", currentFileID, currentVersion, err)
			}
			var boundVersionID uuid.UUID
			if err := pool.QueryRow(ctx, `SELECT file_version_id FROM lesson_draft_files WHERE lesson_id=$1`, lessonID).Scan(&boundVersionID); err != nil || boundVersionID != originalVersionID {
				t.Fatalf("binding changed after rejection: version=%s err=%v", boundVersionID, err)
			}
		})
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func TestUploadServiceResumesPersistedPartsAfterRestart(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id,username,display_name,role,status,password_hash) VALUES ($1,$2,'Upload integration admin','student','active','hash')`, actorID, "upload_integration_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	payload := []byte("restart-persisted-upload")
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	objects := newIntegrationUploadObjects()
	actor := securefiles.Principal{User: auth.User{ID: actorID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "upload-integration-request", IP: net.ParseIP("192.0.2.90")}
	first := securefiles.NewUploadService(securefiles.NewPostgresStore(pool), objects, time.Now)
	created, err := first.Create(ctx, actor, securefiles.CreateUploadInput{DisplayName: "restart.pdf", DeclaredMIME: "application/pdf", ExpectedSize: int64(len(payload)), ExpectedSHA256: hash})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.PutPart(ctx, actor, securefiles.PutPartInput{SessionID: created.ID, Number: 1, Size: int64(len(payload)), SHA256: hash, Body: bytes.NewReader(payload)}); err != nil {
		t.Fatal(err)
	}
	restarted := securefiles.NewUploadService(securefiles.NewPostgresStore(pool), objects, time.Now)
	status, err := restarted.Status(ctx, actor, created.ID)
	if err != nil || len(status.Parts) != 1 || status.Parts[0].SHA256 != hash {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	completed, err := restarted.Complete(ctx, actor, created.ID)
	if err != nil || completed.FileVersionID == uuid.Nil || completed.ProcessingState != "pending_scan" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	duplicate, err := restarted.Complete(ctx, actor, created.ID)
	if err != nil || duplicate.FileVersionID != completed.FileVersionID {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
}

type integrationUploadObjects struct {
	parts map[string][]byte
	data  map[string][]byte
}

func newIntegrationUploadObjects() *integrationUploadObjects {
	return &integrationUploadObjects{parts: make(map[string][]byte), data: make(map[string][]byte)}
}
func (*integrationUploadObjects) CreateMultipart(context.Context, string, objectstore.ObjectMeta) (string, error) {
	return uuid.NewString(), nil
}
func (s *integrationUploadObjects) PutPart(_ context.Context, key, _ string, number int, reader io.Reader, size int64, _ string) (objectstore.Part, error) {
	var buffer bytes.Buffer
	if _, err := io.CopyN(&buffer, reader, size); err != nil {
		return objectstore.Part{}, err
	}
	s.parts[key] = buffer.Bytes()
	return objectstore.Part{Number: number, ETag: "bounded-etag", Size: size}, nil
}
func (s *integrationUploadObjects) CompleteMultipart(_ context.Context, key, _ string, _ []objectstore.Part) (objectstore.ObjectInfo, error) {
	s.data[key] = append([]byte(nil), s.parts[key]...)
	return objectstore.ObjectInfo{Size: int64(len(s.data[key]))}, nil
}
func (*integrationUploadObjects) AbortMultipart(context.Context, string, string) error { return nil }
func (s *integrationUploadObjects) Stat(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	data, ok := s.data[key]
	if !ok {
		return objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	return objectstore.ObjectInfo{Size: int64(len(data))}, nil
}
func (s *integrationUploadObjects) Get(_ context.Context, key string, _ *objectstore.ByteRange) (io.ReadCloser, objectstore.ObjectInfo, error) {
	data, ok := s.data[key]
	if !ok {
		return nil, objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), objectstore.ObjectInfo{Size: int64(len(data))}, nil
}
func (*integrationUploadObjects) Put(context.Context, string, io.Reader, int64, objectstore.ObjectMeta) (objectstore.ObjectInfo, error) {
	return objectstore.ObjectInfo{}, errors.New("not implemented")
}
func (s *integrationUploadObjects) Delete(_ context.Context, key string) error {
	delete(s.data, key)
	return nil
}
func TestFileAccessPostgresAuthorizationAndDenyLog(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	allowed := insertFileSchemaUser(t, pool, "file_access_allowed")
	other := insertFileSchemaUser(t, pool, "file_access_other")
	revision := insertFileSchemaRevision(t, pool, allowed)
	var lesson uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT lesson_id FROM lesson_revisions WHERE id=$1`, revision).Scan(&lesson); err != nil {
		t.Fatal(err)
	}
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, allowed).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,'secret.pdf','application/pdf',4,$3,'ready',$4) RETURNING id`, fileID, "private/"+uuid.NewString(), fmt.Sprintf("%064x", 7), allowed).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO file_previews(file_version_id,preview_kind,object_key,content_type,size_bytes,sha256,processing_state) VALUES($1,'pdf',$2,'application/pdf',4,$3,'ready')`, versionID, "preview/"+uuid.NewString(), fmt.Sprintf("%064x", 8)); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO lesson_revision_audiences(revision_id,mode) VALUES($1,'selected')`, []any{revision}},
		{`INSERT INTO lesson_revision_audience_users(revision_id,user_id) VALUES($1,$2)`, []any{revision, allowed}},
		{`INSERT INTO lesson_revision_files(revision_id,file_version_id,access_policy,sort_position,display_name,description) VALUES($1,$2,'preview',0,'授权讲义.pdf','')`, []any{revision, versionID}},
		{`SELECT finalize_lesson_revision($1)`, []any{revision}},
		{`UPDATE lessons SET published_revision_id=$1 WHERE id=$2`, []any{revision, lesson}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	store := securefiles.NewPostgresStore(pool)
	delivery, err := store.ResolveAccess(ctx, allowed, versionID, securefiles.ActionPreview)
	if err != nil || delivery.VersionID != versionID || !delivery.Preview || delivery.ObjectKey == "" {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	if _, err := store.ResolveAccess(ctx, other, versionID, securefiles.ActionPreview); !errors.Is(err, securefiles.ErrNotFound) {
		t.Fatalf("other err=%v", err)
	}
	requested := uuid.New()
	if err := store.WriteAccessLog(ctx, securefiles.AccessLog{ActorUserID: other, RequestedVersionID: requested, Action: securefiles.ActionPreview, Result: securefiles.AccessDenied, Reason: "not_found", RequestID: "access-deny-request", IP: net.ParseIP("192.0.2.9")}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT result FROM file_access_logs WHERE requested_file_version_id=$1`, requested).Scan(&got); err != nil || got != "deny" {
		t.Fatalf("result=%q err=%v", got, err)
	}
	allow := securefiles.AccessLog{ActorUserID: allowed, RequestedVersionID: versionID, VersionID: versionID, RevisionID: delivery.RevisionID, Action: securefiles.ActionPreview, Result: securefiles.AccessAllowed, RequestID: "playback-allow-request", IP: net.ParseIP("192.0.2.10"), PlaybackSessionHash: strings.Repeat("a", 64)}
	if err := store.WriteAccessLog(ctx, allow); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAccessLog(ctx, allow); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_access_logs WHERE playback_session_hash=$1 AND result='allow'`, allow.PlaybackSessionHash).Scan(&count); err != nil || count != 1 {
		t.Fatalf("sampled allow count=%d err=%v", count, err)
	}
	deny := securefiles.AccessLog{ActorUserID: other, RequestedVersionID: requested, Action: securefiles.ActionPreview, Result: securefiles.AccessDenied, Reason: "not_found", RequestID: "playback-deny-request", IP: net.ParseIP("192.0.2.11"), PlaybackSessionHash: strings.Repeat("b", 64)}
	if err := store.WriteAccessLog(ctx, deny); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAccessLog(ctx, deny); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_access_logs WHERE playback_session_hash=$1 AND result='deny'`, deny.PlaybackSessionHash).Scan(&count); err != nil || count != 2 {
		t.Fatalf("unsampled deny count=%d err=%v", count, err)
	}
}

func TestPublishSnapshotsFilesAndAttachmentSearchDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var admin, student uuid.UUID
	if err := pool.QueryRow(ctx, `WITH existing AS (SELECT id FROM users WHERE role='admin' LIMIT 1), inserted AS (INSERT INTO users(username,display_name,role,status,password_hash) SELECT $1,'Teacher','admin','active','hash' WHERE NOT EXISTS(SELECT 1 FROM existing) RETURNING id) SELECT id FROM existing UNION ALL SELECT id FROM inserted LIMIT 1`, "teacher_"+uuid.NewString()).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	student = insertFileSchemaUser(t, pool, "snapshot_student")
	var grade, term, subject, chapter, lesson uuid.UUID
	n := uuid.NewString()
	if err := pool.QueryRow(ctx, `INSERT INTO grades(name) VALUES($1) RETURNING id`, `G`+n).Scan(&grade); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO terms(grade_id,name) VALUES($1,$2) RETURNING id`, grade, `T`+n).Scan(&term); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO subjects(term_id,name) VALUES($1,$2) RETURNING id`, term, `S`+n).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO chapters(subject_id,name) VALUES($1,$2) RETURNING id`, subject, `C`+n).Scan(&chapter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO lessons(chapter_id) VALUES($1) RETURNING id`, chapter).Scan(&lesson); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO lesson_drafts(lesson_id,title,body_markdown,lock_version,updated_by) VALUES($1,'牛顿课程','正文',1,$2)`, []any{lesson, admin}},
		{`INSERT INTO lesson_draft_audiences(lesson_id,mode) VALUES($1,'selected')`, []any{lesson}},
		{`INSERT INTO lesson_draft_audience_users(lesson_id,user_id) VALUES($1,$2)`, []any{lesson, student}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	versions := make([]uuid.UUID, 0, 2)
	for i := 0; i < 2; i++ {
		var fileID, versionID uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, admin).Scan(&fileID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,$3,'application/pdf',4,$4,'ready',$5) RETURNING id`, fileID, "original/"+uuid.NewString(), fmt.Sprintf("牛顿资料%d.pdf", i+1), fmt.Sprintf("%064x", 20+i), admin).Scan(&versionID); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, versionID)
		if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_files(lesson_id,file_version_id,access_policy,sort_position,display_name,description) VALUES($1,$2,'download',$3,$4,'')`, lesson, versionID, i, fmt.Sprintf("牛顿资料%d.pdf", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	store := teaching.NewPostgresStore(pool)
	svc := teaching.NewService(store, securefiles.NewReadinessChecker(), time.Now)
	actor := teaching.Principal{User: auth.User{ID: admin, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "publish-files-request", IP: net.ParseIP("192.0.2.20")}
	revision, err := svc.Publish(ctx, actor, teaching.PublishInput{LessonID: lesson, ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_draft_files SET display_name='changed.pdf' WHERE lesson_id=$1`, lesson); err != nil {
		t.Fatal(err)
	}
	var names []string
	rows, err := pool.Query(ctx, `SELECT display_name FROM lesson_revision_files WHERE revision_id=$1 ORDER BY sort_position`, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		names = append(names, v)
	}
	rows.Close()
	if len(names) != 2 || names[0] != "牛顿资料1.pdf" {
		t.Fatalf("snapshot names=%v", names)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_files(revision_id,file_version_id,access_policy,sort_position,display_name,description) VALUES($1,$2,'download',99,'late.pdf','')`, revision.ID, versions[0]); err == nil {
		t.Fatal("finalized revision accepted file insert")
	}
	studentSvc := teaching.NewStudentService(store, time.Now)
	studentActor := teaching.Principal{User: auth.User{ID: student, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "search-files-request", IP: net.ParseIP("192.0.2.21")}
	results, _, err := studentSvc.Search(ctx, studentActor, teaching.SearchInput{Query: "牛顿", Limit: 10})
	if err != nil || len(results) != 1 {
		t.Fatalf("search len=%d err=%v results=%+v", len(results), err, results)
	}
}
func TestFinalizationAndRevisionFileInsertSerialize(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actor := insertFileSchemaUser(t, pool, "finalize_race_actor")
	revision := insertFileSchemaRevision(t, pool, actor)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audiences(revision_id,mode) VALUES($1,'all')`, revision); err != nil {
		t.Fatal(err)
	}
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actor).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,'race.pdf','application/pdf',4,$3,'ready',$4) RETURNING id`, fileID, "race/"+uuid.NewString(), fmt.Sprintf("%064x", 91), actor).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT finalize_lesson_revision($1)`, revision); err != nil {
		t.Fatal(err)
	}
	insertTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer insertTx.Rollback(context.Background())
	var insertPID int
	if err := insertTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&insertPID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := insertTx.Exec(context.Background(), `INSERT INTO lesson_revision_files(revision_id,file_version_id,access_policy,sort_position,display_name,description) VALUES($1,$2,'download',0,'race.pdf','')`, revision, versionID)
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for !blocked && time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("concurrent insert crossed uncommitted finalization: %v", err)
		default:
		}
		var waitType *string
		if err := pool.QueryRow(ctx, `SELECT wait_event_type FROM pg_stat_activity WHERE pid=$1`, insertPID).Scan(&waitType); err != nil {
			t.Fatal(err)
		}
		blocked = waitType != nil && *waitType == "Lock"
		if !blocked {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !blocked {
		t.Fatal("concurrent insert never reached revision lock wait")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("insert succeeded after finalization committed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent insert remained blocked")
	}
}
