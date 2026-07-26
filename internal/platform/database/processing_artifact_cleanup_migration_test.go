package database_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestProcessingArtifactCleanupMigrationUpDownAndLatestCompatibility(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var applied, tablePresent bool
	var latest int64
	if err := pool.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=17 AND is_applied),
       to_regclass('public.file_processing_artifacts') IS NOT NULL,
       COALESCE(max(version_id) FILTER (WHERE is_applied),0)
FROM goose_db_version`).Scan(&applied, &tablePresent, &latest); err != nil {
		t.Fatal(err)
	}
	if !applied || !tablePresent || latest != 17 {
		t.Fatalf("applied=%t table_present=%t latest=%d", applied, tablePresent, latest)
	}

	username := "pam_" + uuid.NewString()
	objectKey := "test-processing/artifact-migration/" + uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		statements := []struct {
			query string
			args  []any
		}{
			{`DELETE FROM file_processing_artifacts a USING file_versions fv WHERE a.file_version_id=fv.id AND fv.object_key=$1`, []any{objectKey}},
			{`DELETE FROM file_processing_jobs j USING file_versions fv WHERE j.file_version_id=fv.id AND fv.object_key=$1`, []any{objectKey}},
			{`DELETE FROM file_versions WHERE object_key=$1`, []any{objectKey}},
			{`DELETE FROM files f USING users u WHERE f.created_by=u.id AND u.username=$1 AND NOT EXISTS(SELECT 1 FROM file_versions fv WHERE fv.file_id=f.id)`, []any{username}},
			{`DELETE FROM users u WHERE u.username=$1 AND NOT EXISTS(SELECT 1 FROM files f WHERE f.created_by=u.id)`, []any{username}},
		}
		for _, statement := range statements {
			if _, err := pool.Exec(cleanupCtx, statement.query, statement.args...); err != nil {
				t.Errorf("cleanup migration fixture: %v", err)
				return
			}
		}
	})

	var actorID, fileID, versionID, jobID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,display_name,role,status,password_hash) VALUES($1,'Artifact migration','student','active','hash') RETURNING id`, username).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actorID).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,'ai_attachment',$2,'question.txt','text/plain',1,repeat('a',64),'processing',$3) RETURNING id`, fileID, objectKey, actorID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_processing_jobs(file_version_id,kind,state,attempts,lease_owner,lease_until) VALUES($1,'process_file','running',1,'migration-worker',now()+interval '5 minutes') RETURNING id`, versionID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO file_processing_artifacts(file_version_id,processing_job_id,attempt_no,artifact_kind,object_key,content_type,size_bytes,sha256,state) VALUES($1,$2,1,'ai_text',$3,'text/plain; charset=utf-8',1,repeat('b',64),'delete_pending')`, versionID, jobID, "previews/"+versionID.String()+"/"+jobID.String()+"/1/ai_text.txt"); err != nil {
		t.Fatal(err)
	}

	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	registerMigrationProviderCleanup(t, provider, closeProvider)
	if _, err := provider.DownTo(ctx, 16); err == nil {
		t.Fatal("down migration accepted unreclaimed processing artifact")
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.file_processing_artifacts') IS NOT NULL`).Scan(&tablePresent); err != nil {
		t.Fatal(err)
	}
	if !tablePresent {
		t.Fatal("failed down migration was not atomic")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM file_processing_artifacts WHERE file_version_id=$1`, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 16); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.file_processing_artifacts') IS NOT NULL`).Scan(&tablePresent); err != nil {
		t.Fatal(err)
	}
	if tablePresent {
		t.Fatal("processing artifact table remained after down migration")
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.file_processing_artifacts') IS NOT NULL`).Scan(&tablePresent); err != nil {
		t.Fatal(err)
	}
	if !tablePresent {
		t.Fatal("latest migration did not recreate processing artifact table")
	}
}
