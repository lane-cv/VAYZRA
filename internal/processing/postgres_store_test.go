package processing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresStoreImplementsLeaseContract(t *testing.T) {
	var _ Store = NewPostgresStore(nil)
}

func TestPostgresProcessingClaimCannotPassQueuedMaintenance(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actor := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users(id,username,display_name,role,status,password_hash)
VALUES($1,$2,'Processing admission','student','active','hash')`,
		actor, "processing_admission_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var fileID, versionID, jobID uuid.UUID
	if err := pool.QueryRow(
		ctx,
		`INSERT INTO files(created_by) VALUES($1) RETURNING id`,
		actor,
	).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO file_versions(
 file_id,version,purpose,object_key,display_name,declared_mime,
 size_bytes,sha256,processing_state,created_by
) VALUES($1,1,'teaching',$2,'admission.pdf','application/pdf',1,$3,'pending_scan',$4)
RETURNING id`,
		fileID,
		"test-processing/admission/"+uuid.NewString(),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		actor,
	).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
INSERT INTO file_processing_jobs(file_version_id,kind,available_at)
VALUES($1,'process_file','-infinity') RETURNING id`, versionID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	finishMaintenance := integration.QueueOperationsExclusiveBehindShared(t, pool)
	started := time.Now()
	job, err := NewPostgresStore(pool).LeaseNext(
		ctx,
		"maintenance-race",
		time.Now().UTC(),
		DefaultLeaseDuration,
	)
	if !errors.Is(err, ErrNoJob) || job.ID != uuid.Nil ||
		time.Since(started) > time.Second {
		t.Fatalf("job=%+v err=%v elapsed=%s", job, err, time.Since(started))
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM file_processing_jobs WHERE id=$1`, jobID).
		Scan(&state); err != nil || state != StateQueued {
		t.Fatalf("state=%s err=%v", state, err)
	}
	finishMaintenance()
	job, err = NewPostgresStore(pool).LeaseNext(
		ctx,
		"after-maintenance",
		time.Now().UTC(),
		DefaultLeaseDuration,
	)
	if err != nil || job.ID != jobID {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

func TestPostgresAITextCompletionRequiresAndPersistsPrivateArtifact(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cleanup := func(cleanupCtx context.Context) {
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM file_previews fp USING file_versions fv,files f,users u WHERE fp.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'processing_ai_text_%'`); err != nil {
			t.Errorf("cleanup previews: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM file_processing_artifacts a USING file_versions fv,files f,users u WHERE a.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'processing_ai_text_%'`); err != nil {
			t.Errorf("cleanup processing artifacts: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM file_processing_jobs j USING file_versions fv,files f,users u WHERE j.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'processing_ai_text_%'`); err != nil {
			t.Errorf("cleanup jobs: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM file_versions fv USING files f,users u WHERE fv.file_id=f.id AND f.created_by=u.id AND u.username LIKE 'processing_ai_text_%'`); err != nil {
			t.Errorf("cleanup versions: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM files f USING users u WHERE f.created_by=u.id AND u.username LIKE 'processing_ai_text_%'`); err != nil {
			t.Errorf("cleanup files: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM users WHERE username LIKE 'processing_ai_text_%'`); err != nil {
			t.Errorf("cleanup users: %v", err)
		}
	}
	cleanup(ctx)
	t.Cleanup(func() { cleanup(context.Background()) })
	actor := uuid.New()
	username := "processing_ai_text_" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'AI text student','student','active','hash')`, actor, username); err != nil {
		t.Fatal(err)
	}
	var fileID, versionID, jobID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actor).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,'ai_attachment',$2,'question.txt','text/plain',1,repeat('a',64),'processing',$3) RETURNING id`, fileID, "test-processing/ai-text/"+uuid.NewString(), actor).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	owner := "ai-text-worker"
	if err := pool.QueryRow(ctx, `INSERT INTO file_processing_jobs(file_version_id,kind,state,attempts,lease_owner,lease_until) VALUES($1,'process_file','running',1,$2,now()+interval '5 minutes') RETURNING id`, versionID, owner).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	job := Job{ID: jobID, FileVersionID: versionID, Kind: KindProcessFile, State: StateRunning, Attempts: 1, LeaseOwner: owner}
	result := Result{DetectedMIME: "text/plain", ScanResult: "clean"}
	if err := NewPostgresStore(pool).Complete(ctx, job, result); err == nil {
		t.Fatal("AI text attachment completed without extracted artifact")
	}
	result.AIText = &PreviewResult{
		Kind: "ai_text", ObjectKey: "test-processing/ai-text-preview/" + uuid.NewString(),
		ContentType: "text/plain; charset=utf-8", Size: 4, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	store := NewPostgresStore(pool)
	if err := store.ReserveArtifact(ctx, ProcessingArtifact{
		FileVersionID: versionID, ProcessingJobID: jobID, AttemptNo: 1, Kind: "ai_text",
		ObjectKey: result.AIText.ObjectKey, ContentType: result.AIText.ContentType,
		Size: result.AIText.Size, SHA256: result.AIText.SHA256,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkArtifactStored(ctx, result.AIText.ObjectKey); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE file_processing_artifacts SET content_type='application/octet-stream' WHERE object_key=$1`, result.AIText.ObjectKey); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, job, result); err == nil {
		t.Fatal("completion accepted processing artifact registry metadata mismatch")
	}
	if _, err := pool.Exec(ctx, `UPDATE file_processing_artifacts SET content_type=$2 WHERE object_key=$1`, result.AIText.ObjectKey, result.AIText.ContentType); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, job, result); err != nil {
		t.Fatal(err)
	}
	var state, previewKey string
	if err := pool.QueryRow(ctx, `SELECT fv.processing_state,fp.object_key FROM file_versions fv JOIN file_previews fp ON fp.file_version_id=fv.id AND fp.preview_kind='ai_text' WHERE fv.id=$1`, versionID).Scan(&state, &previewKey); err != nil {
		t.Fatal(err)
	}
	if state != "ready" || previewKey != result.AIText.ObjectKey {
		t.Fatalf("state=%q preview=%q", state, previewKey)
	}
}

func TestPostgresLeaseIsExclusiveReclaimsExpiredAndRejectsStaleOwner(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	cleanupFixtures := func(cleanupCtx context.Context) error {
		tx, err := pool.Begin(cleanupCtx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(cleanupCtx) }()
		marker := `(fv.object_key LIKE 'test-processing/lease/%' OR u.username ~ '^processing_[0-9a-f-]{36}$' OR u.username LIKE 'processing_lease_fixture_%')`
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM file_processing_jobs j USING file_versions fv,files f,users u WHERE j.file_version_id=fv.id AND fv.file_id=f.id AND f.created_by=u.id AND `+marker); err != nil {
			return err
		}
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM file_versions fv USING files f,users u WHERE fv.file_id=f.id AND f.created_by=u.id AND `+marker); err != nil {
			return err
		}
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM files f USING users u WHERE f.created_by=u.id AND (u.username ~ '^processing_[0-9a-f-]{36}$' OR u.username LIKE 'processing_lease_fixture_%') AND NOT EXISTS(SELECT 1 FROM file_versions fv WHERE fv.file_id=f.id)`); err != nil {
			return err
		}
		if _, err := tx.Exec(cleanupCtx, `DELETE FROM users u WHERE (u.username ~ '^processing_[0-9a-f-]{36}$' OR u.username LIKE 'processing_lease_fixture_%') AND NOT EXISTS(SELECT 1 FROM files f WHERE f.created_by=u.id)`); err != nil {
			return err
		}
		return tx.Commit(cleanupCtx)
	}
	if err := cleanupFixtures(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanupFixtures(context.Background()) })
	actor := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'Processing admin','student','active','hash')`, actor, "processing_lease_fixture_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actor).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,'teaching',$2,'lease.pdf','application/pdf',1,$3,'pending_scan',$4) RETURNING id`, fileID, "test-processing/lease/"+uuid.NewString(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", actor).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO file_processing_jobs(file_version_id,kind,available_at) VALUES($1,'process_file','-infinity')`, versionID); err != nil {
		t.Fatal(err)
	}
	// The shared PostgreSQL fixture intentionally retains rows from unrelated
	// packages. Lock every other eligible row without changing it so both
	// workers use SKIP LOCKED and compete only for this test's job.
	hold, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	if _, err := hold.Exec(ctx, `UPDATE file_processing_jobs SET updated_at=updated_at WHERE id<>(SELECT id FROM file_processing_jobs WHERE file_version_id=$1 AND kind='process_file') AND state IN ('queued','running')`, versionID); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	start := make(chan struct{})
	type leaseResult struct {
		job Job
		err error
	}
	results := make(chan leaseResult, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, owner := range []string{"worker-a", "worker-b"} {
		go func(owner string) {
			ready.Done()
			<-start
			job, err := NewPostgresStore(pool).LeaseNext(ctx, owner, now, DefaultLeaseDuration)
			results <- leaseResult{job, err}
		}(owner)
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	if first.err != nil && second.err == nil {
		first, second = second, first
	}
	if first.err != nil || !errors.Is(second.err, ErrNoJob) || first.job.Attempts != 1 {
		t.Fatalf("first=%+v/%v second=%+v/%v", first.job, first.err, second.job, second.err)
	}
	oldOwner := first.job.LeaseOwner
	if _, err := pool.Exec(ctx, `UPDATE file_processing_jobs SET lease_until=$2 WHERE id=$1`, first.job.ID, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := NewPostgresStore(pool).LeaseNext(ctx, "worker-c", now, DefaultLeaseDuration)
	if err != nil || reclaimed.ID != first.job.ID || reclaimed.Attempts != 2 || reclaimed.LeaseOwner != "worker-c" {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
	if err := NewPostgresStore(pool).Heartbeat(ctx, reclaimed.ID, oldOwner, now.Add(time.Minute)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale heartbeat err=%v", err)
	}
	stale := reclaimed
	stale.LeaseOwner = oldOwner
	if err := NewPostgresStore(pool).Complete(ctx, stale, Result{DetectedMIME: "application/pdf", ScanResult: "clean"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale complete err=%v", err)
	}
	if err := NewPostgresStore(pool).Fail(ctx, stale, Failure{Category: "storage_unavailable", RetryAt: now.Add(time.Minute)}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale fail err=%v", err)
	}
}
