package integration_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/teaching"
	"happylearn.local/app/tests/integration"
)

func TestTeachingPublicationSnapshotsExternalVideoWithFreshRevisionIDs(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "video_snapshot_admin", "admin")
	studentID := insertTeachingUser(t, pool, "video_snapshot_student", "student")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
	actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "video-snapshot", IP: net.ParseIP("192.0.2.50")}
	svc := teaching.NewService(teaching.NewPostgresStore(pool), nil, time.Now)
	videoID := uuid.New()
	draft, err := svc.SaveDraft(ctx, actor, teaching.SaveDraftInput{LessonID: lessonID, ExpectedVersion: 1, Title: "Lesson", BodyMarkdown: "Body", Audience: teaching.Audience{Mode: teaching.AudienceSelected, UserIDs: []uuid.UUID{studentID}}, ExternalVideos: []teaching.ExternalVideo{{ID: videoID, URL: "https://video.example.test/watch", Title: "Video"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: draft.LockVersion})
	if err != nil {
		t.Fatal(err)
	}
	draft, err = svc.SaveDraft(ctx, actor, teaching.SaveDraftInput{LessonID: lessonID, ExpectedVersion: draft.LockVersion, Title: "Lesson", BodyMarkdown: "Body v2", Audience: teaching.Audience{Mode: teaching.AudienceSelected, UserIDs: []uuid.UUID{studentID}}, ExternalVideos: []teaching.ExternalVideo{{ID: videoID, URL: "https://video.example.test/watch", Title: "Video"}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: draft.LockVersion})
	if err != nil {
		t.Fatal(err)
	}
	var firstVideo, secondVideo uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM lesson_revision_external_videos WHERE revision_id=$1`, first.ID).Scan(&firstVideo); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM lesson_revision_external_videos WHERE revision_id=$1`, second.ID).Scan(&secondVideo); err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || firstVideo == secondVideo || firstVideo == videoID || secondVideo == videoID {
		t.Fatalf("revisions/videos were not independently snapshotted: revisions=%s/%s videos=%s/%s draft=%s", first.ID, second.ID, firstVideo, secondVideo, videoID)
	}
}

func TestTeachingPublicationRejectsArchivedHierarchy(t *testing.T) {
	for _, tc := range []struct{ name, table string }{{"grade", "grades"}, {"term", "terms"}, {"subject", "subjects"}, {"chapter", "chapters"}, {"lesson", "lessons"}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := integration.StartPostgres(t)
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			resetTeachingTables(t, pool)
			adminID := insertTeachingUser(t, pool, "archive_"+tc.name+"_admin", "admin")
			studentID := insertTeachingUser(t, pool, "archive_"+tc.name+"_student", "student")
			ids := insertCatalogPath(t, pool)
			lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
			if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences (lesson_id,mode) VALUES ($1,'selected')`, lessonID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audience_users (lesson_id,user_id) VALUES ($1,$2)`, lessonID, studentID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE `+tc.table+` SET archived_at=now()`); err != nil {
				t.Fatal(err)
			}
			actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "hierarchy-archive", IP: net.ParseIP("192.0.2.51")}
			_, err := teaching.NewService(teaching.NewPostgresStore(pool), nil, time.Now).Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: 1})
			if !errors.Is(err, teaching.ErrNotPublishable) {
				t.Fatalf("publish error=%v", err)
			}
		})
	}
}
