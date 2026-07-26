package files

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/tests/integration"
)

func TestPostgresProcessingArtifactCleanupRetriesAndPreservesPublishedPreview(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	username := "artifact_cleanup_" + uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		statements := []string{
			`DELETE FROM file_processing_artifacts a USING file_versions fv,files f,users u WHERE a.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username=$1`,
			`DELETE FROM file_previews fp USING file_versions fv,files f,users u WHERE fp.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username=$1`,
			`DELETE FROM file_processing_jobs j USING file_versions fv,files f,users u WHERE j.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username=$1`,
			`DELETE FROM file_versions fv USING files f,users u WHERE fv.file_id=f.id AND f.created_by=u.id AND u.username=$1`,
			`DELETE FROM files f USING users u WHERE f.created_by=u.id AND u.username=$1`,
		}
		for _, statement := range statements {
			if _, err := pool.Exec(cleanupCtx, statement, username); err != nil {
				t.Errorf("cleanup fixture: %v", err)
				return
			}
		}
	})

	var actorID, fileID, versionID, jobID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,display_name,role,status,password_hash) VALUES($1,'Artifact cleanup','student','active','hash') RETURNING id`, username).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actorID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,'ai_attachment',$2,'question.txt','text/plain',1,repeat('a',64),'failed',$3) RETURNING id`, fileID, "test-processing/artifact-cleanup/"+uuid.NewString(), actorID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_processing_jobs(file_version_id,kind,state,attempts,last_failure_category) VALUES($1,'process_file','failed',1,'storage_unavailable') RETURNING id`, versionID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	publishedKey := "previews/" + versionID.String() + "/published/ai_text.txt"
	if _, err := pool.Exec(ctx, `INSERT INTO file_previews(file_version_id,preview_kind,object_key,content_type,size_bytes,sha256,processing_state) VALUES($1,'ai_text',$2,'text/plain; charset=utf-8',1,repeat('b',64),'ready')`, versionID, publishedKey); err != nil {
		t.Fatal(err)
	}
	attemptKey := "previews/" + versionID.String() + "/" + jobID.String() + "/1/ai_text.txt"
	if _, err := pool.Exec(ctx, `INSERT INTO file_processing_artifacts(file_version_id,processing_job_id,attempt_no,artifact_kind,object_key,content_type,size_bytes,sha256,state,created_at,updated_at) VALUES($1,$2,1,'ai_text',$3,'text/plain; charset=utf-8',1,repeat('c',64),'delete_pending','2000-01-01','2000-01-01')`, versionID, jobID, attemptKey); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	previews := newFakeObjects()
	previews.deleteErr = objectstore.ErrUnavailable
	service := NewFileCleanupService(NewPostgresStore(pool), newFakeObjects(), previews, func() time.Time { return now })
	firstCleanupErr := service.Cleanup(ctx, 1)
	if firstCleanupErr == nil {
		t.Fatal("cleanup unexpectedly succeeded while object deletion failed")
	}
	var state string
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT state,cleanup_attempts FROM file_processing_artifacts WHERE object_key=$1`, attemptKey).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "delete_pending" || attempts != 1 {
		t.Fatalf("state=%q attempts=%d cleanup_error=%v", state, attempts, firstCleanupErr)
	}

	previews.deleteErr = nil
	now = now.Add(fileCleanupLease + time.Second)
	if err := service.Cleanup(ctx, 1); err != nil {
		t.Fatal(err)
	}
	var registryRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_processing_artifacts WHERE object_key=$1`, attemptKey).Scan(&registryRows); err != nil {
		t.Fatal(err)
	}
	var retainedKey string
	if err := pool.QueryRow(ctx, `SELECT object_key FROM file_previews WHERE file_version_id=$1 AND preview_kind='ai_text'`, versionID).Scan(&retainedKey); err != nil {
		t.Fatal(err)
	}
	var auditEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_type='file_version' AND target_id=$1 AND action IN ('file.processing_artifact_cleanup_scheduled','file.processing_artifact_cleanup_completed')`, versionID.String()).Scan(&auditEvents); err != nil {
		t.Fatal(err)
	}
	if registryRows != 0 || retainedKey != publishedKey || previews.deleteCalls.Load() != 2 || auditEvents != 3 {
		t.Fatalf("registry=%d retained=%q deletes=%d audit_events=%d", registryRows, retainedKey, previews.deleteCalls.Load(), auditEvents)
	}
}
