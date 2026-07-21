package integration_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/teaching"
	"happylearn.local/app/tests/integration"
)

type fileReadinessLockBarrier struct {
	entered chan struct{}
	release chan struct{}
}

func (b *fileReadinessLockBarrier) Check(ctx context.Context, reader teaching.PublicationReader, draft teaching.Draft) error {
	blockers, err := reader.PublicationBlockers(ctx, draft.LessonID, draft.LockVersion)
	if err != nil {
		return err
	}
	if len(blockers) != 0 {
		return teaching.ErrNotPublishable
	}
	close(b.entered)
	<-b.release
	return nil
}

func TestPublicationReadinessLocksEveryFileDecisionRow(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		update string
		target func(fileID, versionID, bindingID uuid.UUID) uuid.UUID
	}{
		{"binding delete", `DELETE FROM lesson_draft_files WHERE id=$1`, func(_, _, binding uuid.UUID) uuid.UUID { return binding }},
		{"file delete marker", `UPDATE files SET deleted_at=now() WHERE id=$1`, func(file, _, _ uuid.UUID) uuid.UUID { return file }},
		{"version state", `UPDATE file_versions SET processing_state='rejected' WHERE id=$1`, func(_, version, _ uuid.UUID) uuid.UUID { return version }},
		{"preview state", `UPDATE file_previews SET processing_state='failed' WHERE file_version_id=$1`, func(_, version, _ uuid.UUID) uuid.UUID { return version }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetTeachingTables(t, pool)
			adminID := insertTeachingUser(t, pool, "file_lock_admin_"+stringsForDBName(tc.name), "admin")
			lessonID := insertLessonDraft(t, pool, insertCatalogPath(t, pool).chapterID, adminID)
			if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences(lesson_id,mode) VALUES($1,'all')`, lessonID); err != nil {
				t.Fatal(err)
			}
			var fileID, versionID, bindingID uuid.UUID
			if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, adminID).Scan(&fileID); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,'teaching',$2,'x.pdf','application/pdf',4,$3,'ready',$4) RETURNING id`, fileID, "lock/"+uuid.NewString(), fmt.Sprintf("%064x", 71), adminID).Scan(&versionID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO file_previews(file_version_id,preview_kind,object_key,content_type,size_bytes,sha256,processing_state) VALUES($1,'pdf',$2,'application/pdf',4,$3,'ready')`, versionID, "preview/"+uuid.NewString(), fmt.Sprintf("%064x", 72)); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `INSERT INTO lesson_draft_files(lesson_id,file_version_id,access_policy,sort_position,display_name,description) VALUES($1,$2,'preview',0,'x.pdf','') RETURNING id`, lessonID, versionID).Scan(&bindingID); err != nil {
				t.Fatal(err)
			}

			barrier := &fileReadinessLockBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			service := teaching.NewService(teaching.NewPostgresStore(pool), barrier, time.Now)
			actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "file-lock", IP: net.ParseIP("192.0.2.90")}
			publishDone := make(chan error, 1)
			go func() {
				_, err := service.Publish(context.Background(), actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: 1})
				publishDone <- err
			}()
			select {
			case <-barrier.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("publication readiness did not acquire locks")
			}

			updateTx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer updateTx.Rollback(context.Background())
			var pid int
			if err := updateTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			updateDone := make(chan error, 1)
			go func() {
				_, err := updateTx.Exec(context.Background(), tc.update, tc.target(fileID, versionID, bindingID))
				updateDone <- err
			}()
			waitForPostgresLock(t, pool, pid, updateDone)
			close(barrier.release)
			select {
			case err := <-publishDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("publication did not finish")
			}
			select {
			case err := <-updateDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("writer remained blocked after publication commit")
			}
		})
	}
}

func waitForPostgresLock(t *testing.T, pool *pgxpool.Pool, pid int, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("writer completed before publication commit: %v", err)
		default:
		}
		var waitType *string
		if err := pool.QueryRow(context.Background(), `SELECT wait_event_type FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waitType); err != nil {
			t.Fatal(err)
		}
		if waitType != nil && *waitType == "Lock" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("writer never entered a PostgreSQL Lock wait")
}

func stringsForDBName(value string) string {
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
