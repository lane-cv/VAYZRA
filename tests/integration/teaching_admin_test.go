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

func TestTeachingAdminPublicationWritesFrozenRevisionAuditAndOutbox(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "publication_admin", "admin")
	studentID := insertTeachingUser(t, pool, "publication_student", "student")
	svc := teaching.NewService(teaching.NewPostgresStore(pool), nil, time.Now)
	actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "publication-request", IP: net.ParseIP("192.0.2.40")}
	grade, err := svc.CreateCatalog(ctx, actor, teaching.CatalogCreateInput{Kind: teaching.CatalogGrade, Name: "Grade 1"})
	if err != nil {
		t.Fatal(err)
	}
	term, err := svc.CreateCatalog(ctx, actor, teaching.CatalogCreateInput{Kind: teaching.CatalogTerm, ParentID: grade.ID, Name: "Term 1"})
	if err != nil {
		t.Fatal(err)
	}
	subject, err := svc.CreateCatalog(ctx, actor, teaching.CatalogCreateInput{Kind: teaching.CatalogSubject, ParentID: term.ID, Name: "Math"})
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := svc.CreateCatalog(ctx, actor, teaching.CatalogCreateInput{Kind: teaching.CatalogChapter, ParentID: subject.ID, Name: "Chapter 1"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := svc.CreateLesson(ctx, actor, teaching.CreateLessonInput{ChapterID: chapter.ID, Title: "Lesson 1"})
	if err != nil {
		t.Fatal(err)
	}
	if draft.Audience.Mode != teaching.AudienceAll {
		t.Fatalf("new lesson audience mode=%q, want all", draft.Audience.Mode)
	}
	draft, err = svc.SaveDraft(ctx, actor, teaching.SaveDraftInput{LessonID: draft.LessonID, ExpectedVersion: draft.LockVersion, Title: "Lesson 1", BodyMarkdown: "Published body", Audience: teaching.Audience{Mode: teaching.AudienceSelected, UserIDs: []uuid.UUID{studentID}}})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := svc.Publish(ctx, actor, teaching.PublishInput{LessonID: draft.LessonID, ExpectedVersion: draft.LockVersion})
	if err != nil {
		t.Fatal(err)
	}
	var published uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT published_revision_id FROM lessons WHERE id=$1`, draft.LessonID).Scan(&published); err != nil || published != revision.ID {
		t.Fatalf("published=%s revision=%s err=%v", published, revision.ID, err)
	}
	var finalizations, audits, outbox int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM lesson_revision_finalizations WHERE revision_id=$1`, revision.ID).Scan(&finalizations); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='lesson.published' AND target_id=$1`, draft.LessonID.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE kind='lesson.published' AND payload->>'lesson_id'=$1`, draft.LessonID.String()).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if finalizations != 1 || audits != 1 || outbox != 1 {
		t.Fatalf("finalizations=%d audits=%d outbox=%d", finalizations, audits, outbox)
	}
}

func TestTeachingPublicationFailureLeavesCurrentRevisionUntouched(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "publication_fail_admin", "admin")
	studentID := insertTeachingUser(t, pool, "publication_fail_student", "student")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
	current := publishFixtureRevision(t, pool, lessonID, adminID, studentID)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences(lesson_id,mode) VALUES($1,'all')`, lessonID); err != nil {
		t.Fatal(err)
	}
	checkerCalled := false
	svc := teaching.NewService(teaching.NewPostgresStore(pool), rejectPublication{called: &checkerCalled}, time.Now)
	actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "reject-request", IP: net.ParseIP("192.0.2.41")}
	_, err := svc.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: 1})
	if !errors.Is(err, teaching.ErrNotPublishable) || !checkerCalled {
		t.Fatalf("publish error=%v checkerCalled=%t", err, checkerCalled)
	}
	var got uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT published_revision_id FROM lessons WHERE id=$1`, lessonID).Scan(&got); err != nil || got != current {
		t.Fatalf("published=%s want=%s err=%v", got, current, err)
	}
}

type rejectPublication struct{ called *bool }

func (r rejectPublication) Check(context.Context, teaching.PublicationReader, teaching.Draft) error {
	*r.called = true
	return teaching.ErrNotPublishable
}
