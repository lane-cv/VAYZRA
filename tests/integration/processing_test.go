package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/processing"
)

func TestProcessingLeaseAndResultAreDurableAndAtomic(t *testing.T) {
	ctx := context.Background()
	pool := StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	cleanupFixtures := func(cleanupCtx context.Context) error {
		tx, err := pool.Begin(cleanupCtx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(cleanupCtx) }()
		predicate := `(fv.object_key LIKE 'test-processing/integration/%' OR u.username LIKE 'processing_integration_%')`
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM file_processing_jobs j USING file_versions fv,files f,users u WHERE j.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND `+predicate); err != nil {
			return err
		}
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM file_versions fv USING files f,users u WHERE fv.file_id=f.id AND f.created_by=u.id AND `+predicate); err != nil {
			return err
		}
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM files f USING users u WHERE f.created_by=u.id AND u.username LIKE 'processing_integration_%' AND NOT EXISTS(SELECT 1 FROM file_versions fv WHERE fv.file_id=f.id)`); err != nil {
			return err
		}
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM users u WHERE u.username LIKE 'processing_integration_%' AND NOT EXISTS(SELECT 1 FROM files f WHERE f.created_by=u.id)`); err != nil {
			return err
		}
		return tx.Commit(cleanupCtx)
	}
	if err := cleanupFixtures(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanupFixtures(context.Background()) })
	actor := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'Processing integration','student','active','hash')`, actor, "processing_integration_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actor).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,'result.pdf','application/pdf',1,$3,'pending_scan',$4) RETURNING id`, fileID, "test-processing/integration/"+uuid.NewString(), "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", actor).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO file_processing_jobs(file_version_id,kind,available_at) VALUES($1,'process_file','-infinity')`, versionID); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	store := processing.NewPostgresStore(pool)
	job, err := store.LeaseNext(ctx, "integration-worker", now, processing.DefaultLeaseDuration)
	if err != nil || job.FileVersionID != versionID || job.Attempts != 1 {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if err := store.Complete(ctx, job, processing.Result{DetectedMIME: "application/pdf", ScanResult: "clean"}); err != nil {
		t.Fatal(err)
	}
	var jobState, fileState, detected, scan string
	var owner *string
	if err := pool.QueryRow(ctx, `SELECT j.state,j.lease_owner,fv.processing_state,fv.detected_mime,fv.scan_result FROM file_processing_jobs j JOIN file_versions fv ON fv.id=j.file_version_id WHERE j.id=$1`, job.ID).Scan(&jobState, &owner, &fileState, &detected, &scan); err != nil {
		t.Fatal(err)
	}
	if jobState != "completed" || owner != nil || fileState != "ready" || detected != "application/pdf" || scan != "clean" {
		t.Fatalf("job=%s owner=%v file=%s detected=%s scan=%s", jobState, owner, fileState, detected, scan)
	}
}
