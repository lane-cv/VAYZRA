package integration_test

import (
	"context"
	"errors"
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

func TestSelectedAudienceUsersStayLockedThroughPublication(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		update string
	}{
		{name: "disable", update: `UPDATE users SET status='disabled' WHERE id=$1`},
		{name: "soft_delete", update: `UPDATE users SET deleted_at=now() WHERE id=$1`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resetTeachingTables(t, pool)
			adminID := insertTeachingUser(t, pool, "audience_lock_admin_"+tt.name, "admin")
			studentID := insertTeachingUser(t, pool, "audience_lock_student_"+tt.name, "student")
			ids := insertCatalogPath(t, pool)
			lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
			if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences(lesson_id,mode) VALUES($1,'selected')`, lessonID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audience_users(lesson_id,user_id) VALUES($1,$2)`, lessonID, studentID); err != nil {
				t.Fatal(err)
			}

			barrier := &publicationBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			service := teaching.NewService(teaching.NewPostgresStore(pool), barrier, time.Now)
			actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "audience-lock", IP: net.ParseIP("192.0.2.71")}
			publishDone := make(chan publishResult, 1)
			go func() {
				revision, err := service.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: 1})
				publishDone <- publishResult{revision: revision, err: err}
			}()
			select {
			case <-barrier.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("publication checker was not reached")
			}

			updateDone := make(chan error, 1)
			go func() {
				_, err := pool.Exec(ctx, tt.update, studentID)
				updateDone <- err
			}()
			select {
			case err := <-updateDone:
				close(barrier.release)
				<-publishDone
				t.Fatalf("audience user update completed before publication commit: %v", err)
			case <-time.After(150 * time.Millisecond):
			}
			close(barrier.release)
			var result publishResult
			select {
			case result = <-publishDone:
			case <-time.After(3 * time.Second):
				t.Fatal("publication did not complete after checker release")
			}
			if result.err != nil {
				t.Fatal(result.err)
			}
			select {
			case err := <-updateDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("audience user update remained blocked after publication commit")
			}
			var members int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM lesson_revision_audience_users WHERE revision_id=$1 AND user_id=$2`, result.revision.ID, studentID).Scan(&members); err != nil {
				t.Fatal(err)
			}
			if members != 1 {
				t.Fatalf("historical revision audience members = %d, want 1", members)
			}
		})
	}
}

type publicationBarrier struct {
	entered chan struct{}
	release chan struct{}
}

func (b *publicationBarrier) Check(context.Context, teaching.PublicationReader, teaching.Draft) error {
	close(b.entered)
	<-b.release
	return nil
}

type publishResult struct {
	revision teaching.Revision
	err      error
}

func TestPublishRejectsNonCanonicalPersistedURLThenAcceptsSavedCanonicalURL(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "canonical_url_admin", "admin")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences(lesson_id,mode) VALUES($1,'all')`, lessonID); err != nil {
		t.Fatal(err)
	}
	videoID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_external_videos(id,lesson_id,url,title) VALUES($1,$2,'https://VIDEO.EXAMPLE.TEST:443/watch#','Video')`, videoID, lessonID); err != nil {
		t.Fatal(err)
	}
	service := teaching.NewService(teaching.NewPostgresStore(pool), nil, time.Now)
	actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "canonical-url", IP: net.ParseIP("192.0.2.72")}
	if _, err := service.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: 1}); !errors.Is(err, teaching.ErrNotPublishable) {
		t.Fatalf("tampered URL publish error = %v, want not publishable", err)
	}
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM lesson_revisions WHERE lesson_id=$1`, lessonID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 {
		t.Fatalf("tampered URL created %d revisions", revisions)
	}
	draft, err := service.SaveDraft(ctx, actor, teaching.SaveDraftInput{
		LessonID: lessonID, ExpectedVersion: 1, Title: "Lesson 1", BodyMarkdown: "Body",
		Audience:       teaching.Audience{Mode: teaching.AudienceAll},
		ExternalVideos: []teaching.ExternalVideo{{ID: videoID, URL: "HTTPS://VIDEO.EXAMPLE.TEST:443/watch#", Title: "Video"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: draft.LockVersion}); err != nil {
		t.Fatalf("canonical SaveDraft publication: %v", err)
	}
}

func TestGetAdminLessonReportsEffectiveAncestorArchive(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "effective_archive_admin", "admin")
	path := insertFullCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, path.chapterID, adminID)
	store := teaching.NewPostgresStore(pool)
	for _, tt := range []struct {
		name  string
		table string
		id    uuid.UUID
	}{
		{name: "grade", table: "grades", id: path.gradeID},
		{name: "term", table: "terms", id: path.termID},
		{name: "subject", table: "subjects", id: path.subjectID},
		{name: "chapter", table: "chapters", id: path.chapterID},
		{name: "lesson", table: "lessons", id: lessonID},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, "UPDATE "+tt.table+" SET archived_at=now() WHERE id=$1", tt.id); err != nil {
				t.Fatal(err)
			}
			detail, err := store.GetAdminLesson(ctx, lessonID)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Lesson.ArchivedAt == nil {
				t.Fatal("effective archive was not reported")
			}
			if _, err := pool.Exec(ctx, "UPDATE "+tt.table+" SET archived_at=NULL WHERE id=$1", tt.id); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type fullCatalogPath struct {
	gradeID   uuid.UUID
	termID    uuid.UUID
	subjectID uuid.UUID
	chapterID uuid.UUID
}

func insertFullCatalogPath(t *testing.T, pool *pgxpool.Pool) fullCatalogPath {
	t.Helper()
	ctx := context.Background()
	var path fullCatalogPath
	if err := pool.QueryRow(ctx, `INSERT INTO grades(name) VALUES('Archive Grade') RETURNING id`).Scan(&path.gradeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO terms(grade_id,name) VALUES($1,'Archive Term') RETURNING id`, path.gradeID).Scan(&path.termID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO subjects(term_id,name) VALUES($1,'Archive Subject') RETURNING id`, path.termID).Scan(&path.subjectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO chapters(subject_id,name) VALUES($1,'Archive Chapter') RETURNING id`, path.subjectID).Scan(&path.chapterID); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPublishRejectsPersistedControlsInNonBodyFields(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		tamper func(context.Context, *pgxpool.Pool, uuid.UUID) error
	}{
		{name: "lesson_title", tamper: func(ctx context.Context, pool *pgxpool.Pool, lessonID uuid.UUID) error {
			_, err := pool.Exec(ctx, `UPDATE lesson_drafts SET title=$2 WHERE lesson_id=$1`, lessonID, "Les\x01son")
			return err
		}},
		{name: "summary", tamper: func(ctx context.Context, pool *pgxpool.Pool, lessonID uuid.UUID) error {
			_, err := pool.Exec(ctx, `UPDATE lesson_drafts SET summary=$2 WHERE lesson_id=$1`, lessonID, "Summary\x01")
			return err
		}},
		{name: "video_title", tamper: func(ctx context.Context, pool *pgxpool.Pool, lessonID uuid.UUID) error {
			_, err := pool.Exec(ctx, `INSERT INTO lesson_draft_external_videos(lesson_id,url,title) VALUES($1,'https://video.example.test/watch',$2)`, lessonID, "Vid\x01eo")
			return err
		}},
		{name: "video_description", tamper: func(ctx context.Context, pool *pgxpool.Pool, lessonID uuid.UUID) error {
			_, err := pool.Exec(ctx, `INSERT INTO lesson_draft_external_videos(lesson_id,url,title,description) VALUES($1,'https://video.example.test/watch','Video',$2)`, lessonID, "Description\x01")
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTeachingTables(t, pool)
			adminID := insertTeachingUser(t, pool, "persisted_control_admin_"+tt.name, "admin")
			ids := insertCatalogPath(t, pool)
			lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
			if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences(lesson_id,mode) VALUES($1,'all')`, lessonID); err != nil {
				t.Fatal(err)
			}
			if err := tt.tamper(ctx, pool, lessonID); err != nil {
				t.Fatalf("persisted tamper setup returned a database error: %v", err)
			}
			service := teaching.NewService(teaching.NewPostgresStore(pool), nil, time.Now)
			actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "persisted-control", IP: net.ParseIP("192.0.2.73")}
			if _, err := service.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: 1}); !errors.Is(err, teaching.ErrNotPublishable) {
				t.Fatalf("Publish error = %v, want not publishable", err)
			}
			var revisions int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM lesson_revisions WHERE lesson_id=$1`, lessonID).Scan(&revisions); err != nil {
				t.Fatal(err)
			}
			if revisions != 0 {
				t.Fatalf("persisted control created %d revisions", revisions)
			}
		})
	}
}
