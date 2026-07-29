package backup

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresRestoreVerificationDatabaseUsesFixedCountsSessionsAndGoose(t *testing.T) {
	pool := restoreVerificationPool(t)
	ctx := context.Background()
	adapter := NewPostgresRestoreVerificationDatabase(pool)

	for _, table := range restoreRowCountAllowlist {
		var expected int64
		if err := pool.QueryRow(
			ctx,
			`SELECT count(*) FROM `+table,
		).Scan(&expected); err != nil {
			t.Fatal(err)
		}
		got, err := adapter.CountRows(ctx, table)
		if err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != expected {
			t.Fatalf("count %s=%d want=%d", table, got, expected)
		}
	}
	if _, err := adapter.CountRows(
		ctx,
		"users; SELECT pg_sleep(10)",
	); !errors.Is(err, ErrRestoreVerifierConfiguration) {
		t.Fatalf("unknown table error=%v", err)
	}

	var expectedMigration int64
	if err := pool.QueryRow(ctx, `
SELECT COALESCE(MAX(version_id),0)
FROM goose_db_version
WHERE is_applied`).Scan(&expectedMigration); err != nil {
		t.Fatal(err)
	}
	migration, err := adapter.MigrationVersion(ctx)
	if err != nil || migration != expectedMigration {
		t.Fatalf("migration=%d want=%d err=%v", migration, expectedMigration, err)
	}

	activeBefore, err := adapter.ActiveSessionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	username := "restore_sessions_" + strings.ReplaceAll(userID.String(), "-", "")
	if _, err := pool.Exec(ctx, `
INSERT INTO users(
  id,username,display_name,role,password_hash,must_change_password
) VALUES($1,$2,'Restore Sessions','student','hash',false)`,
		userID,
		username,
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sessions WHERE user_id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	})
	activeToken := uuid.New()
	revokedToken := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO sessions(
  id,user_id,token_hash,idle_expires_at,absolute_expires_at,revoked_at
) VALUES
  ($1,$3,$4,now()+interval '1 hour',now()+interval '2 hours',NULL),
  ($2,$3,$5,now()+interval '1 hour',now()+interval '2 hours',now())`,
		uuid.New(),
		uuid.New(),
		userID,
		activeToken[:],
		revokedToken[:],
	); err != nil {
		t.Fatal(err)
	}
	active, err := adapter.ActiveSessionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active != activeBefore+1 {
		t.Fatalf(
			"active sessions=%d want=%d; revoked fixture must be excluded",
			active,
			activeBefore+1,
		)
	}
}

func TestPostgresRestoreVerificationDatabaseStreamsOnlyLiveObjectUnion(t *testing.T) {
	pool := restoreVerificationPool(t)
	ctx := context.Background()
	adapter := NewPostgresRestoreVerificationDatabase(pool)
	fixture := createRestoreObjectFixture(t, pool)

	got := make(map[string]RestoreObjectReference)
	if err := adapter.ForEachLiveObject(
		ctx,
		func(reference RestoreObjectReference) error {
			if strings.HasPrefix(reference.ObjectKey, fixture.prefix) {
				got[reference.ObjectKey] = reference
			}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, fixture.expected) {
		t.Fatalf("references=%#v want=%#v", got, fixture.expected)
	}
}

func TestPostgresRestoreVerificationDatabaseStopsStreamingOnVisitorFailure(t *testing.T) {
	pool := restoreVerificationPool(t)
	adapter := NewPostgresRestoreVerificationDatabase(pool)
	_ = createRestoreObjectFixture(t, pool)
	sentinel := errors.New("stop visitor")
	calls := 0
	err := adapter.ForEachLiveObject(
		context.Background(),
		func(RestoreObjectReference) error {
			calls++
			return sentinel
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
	if calls != 1 {
		t.Fatalf("visitor calls=%d want=1", calls)
	}
}

type restoreObjectFixture struct {
	prefix   string
	expected map[string]RestoreObjectReference
}

func createRestoreObjectFixture(
	t *testing.T,
	pool *pgxpool.Pool,
) restoreObjectFixture {
	t.Helper()
	ctx := context.Background()
	fixtureID := uuid.New()
	prefix := "restore-verifier/" + fixtureID.String() + "/"
	userID := uuid.New()
	username := "restore_objects_" + strings.ReplaceAll(userID.String(), "-", "")
	if _, err := pool.Exec(ctx, `
INSERT INTO users(
  id,username,display_name,role,password_hash,must_change_password
) VALUES($1,$2,'Restore Objects','student','hash',false)`,
		userID,
		username,
	); err != nil {
		t.Fatal(err)
	}

	type versionFixture struct {
		name         string
		cleanupState string
		purged       bool
	}
	versionIDs := make(map[string]uuid.UUID)
	fileIDs := make([]uuid.UUID, 0, 3)
	for _, input := range []versionFixture{
		{name: "live"},
		{name: "deleting", cleanupState: "deleting"},
		{name: "purged", cleanupState: "purged", purged: true},
	} {
		fileID := uuid.New()
		versionID := uuid.New()
		fileIDs = append(fileIDs, fileID)
		versionIDs[input.name] = versionID
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO files(id,created_by) VALUES($1,$2)`,
			fileID,
			userID,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO file_versions(
  id,file_id,version,purpose,object_key,display_name,declared_mime,
  size_bytes,sha256,processing_state,created_by,purged_at,cleanup_state,
  cleanup_lease_owner,cleanup_lease_until
) VALUES(
  $1,$2,1,'teaching',$3,$4,'application/pdf',
  $5,repeat('a',64),$6,$7,
  CASE WHEN $8 THEN now() ELSE NULL END,
  NULLIF($9,''),
  CASE WHEN $9='deleting' THEN 'restore-fixture' ELSE NULL END,
  CASE WHEN $9='deleting' THEN now()+interval '1 hour' ELSE NULL END
)`,
			versionID,
			fileID,
			prefix+"original-"+input.name,
			input.name+".pdf",
			int64(10+len(input.name)),
			map[bool]string{true: "failed", false: "ready"}[input.purged],
			userID,
			input.purged,
			input.cleanupState,
		); err != nil {
			t.Fatal(err)
		}
	}

	previewInputs := []struct {
		name, parent, state string
		size                int64
	}{
		{name: "preview-live", parent: "live", state: "ready", size: 31},
		{name: "preview-processing", parent: "live", state: "processing", size: 32},
		{name: "preview-deleting-parent", parent: "deleting", state: "ready", size: 33},
	}
	for _, input := range previewInputs {
		if _, err := pool.Exec(ctx, `
INSERT INTO file_previews(
  id,file_version_id,preview_kind,object_key,content_type,
  size_bytes,sha256,processing_state
) VALUES($1,$2,$3,$4,'application/pdf',$5,repeat('b',64),$6)`,
			uuid.New(),
			versionIDs[input.parent],
			map[string]string{
				"preview-live":            "pdf",
				"preview-processing":      "page",
				"preview-deleting-parent": "thumbnail",
			}[input.name],
			prefix+input.name,
			input.size,
			input.state,
		); err != nil {
			t.Fatal(err)
		}
	}

	artifactInputs := []struct {
		name, state string
		size        int64
	}{
		{name: "artifact-stored", state: "stored", size: 41},
		{name: "artifact-delete-pending", state: "delete_pending", size: 42},
	}
	jobIDs := make([]uuid.UUID, 0, len(artifactInputs))
	for index, input := range artifactInputs {
		jobID := uuid.New()
		jobIDs = append(jobIDs, jobID)
		if _, err := pool.Exec(ctx, `
INSERT INTO file_processing_jobs(
  id,file_version_id,kind,state,attempts
) VALUES($1,$2,'process_file','completed',1)`,
			jobID,
			versionIDs["deleting"],
		); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO file_processing_artifacts(
  id,file_version_id,processing_job_id,attempt_no,artifact_kind,
  object_key,content_type,size_bytes,sha256,state
) VALUES($1,$2,$3,1,$4,$5,'application/pdf',$6,repeat('c',64),$7)`,
			uuid.New(),
			versionIDs["deleting"],
			jobID,
			[]string{"pdf", "page"}[index],
			prefix+input.name,
			input.size,
			input.state,
		); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM file_processing_artifacts WHERE object_key LIKE $1`,
			prefix+"%",
		)
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM file_processing_jobs WHERE id=ANY($1)`,
			jobIDs,
		)
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM file_previews WHERE object_key LIKE $1`,
			prefix+"%",
		)
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM file_versions WHERE object_key LIKE $1`,
			prefix+"%",
		)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM files WHERE id=ANY($1)`, fileIDs)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, userID)
	})

	liveOriginal := prefix + "original-live"
	livePreview := prefix + "preview-live"
	storedArtifact := prefix + "artifact-stored"
	return restoreObjectFixture{
		prefix: prefix,
		expected: map[string]RestoreObjectReference{
			liveOriginal: {
				Source: RestoreFileVersion, Repository: RestoreOriginals,
				ObjectKey: liveOriginal, Size: 14,
			},
			livePreview: {
				Source: RestoreFilePreview, Repository: RestorePreviews,
				ObjectKey: livePreview, Size: 31,
			},
			storedArtifact: {
				Source: RestoreProcessingArtifact, Repository: RestorePreviews,
				ObjectKey: storedArtifact, Size: 41,
			},
		},
	}
}

func restoreVerificationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestRestoreRowCountAllowlistRemainsMigrationConsistent(t *testing.T) {
	if len(restoreRowCountAllowlist) != 16 {
		t.Fatalf("fixed row count table count=%d", len(restoreRowCountAllowlist))
	}
	for index, table := range restoreRowCountAllowlist {
		if table == "" {
			t.Fatalf("empty table at index %d", index)
		}
		for later := index + 1; later < len(restoreRowCountAllowlist); later++ {
			if table == restoreRowCountAllowlist[later] {
				t.Fatalf("duplicate fixed table %q", table)
			}
		}
	}
}
