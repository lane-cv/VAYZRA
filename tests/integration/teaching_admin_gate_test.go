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

var errReadinessBlocked = errors.New("readiness blocked")

type lockProbeCheck struct {
	pool   *pgxpool.Pool
	locked bool
}

func (c *lockProbeCheck) Check(ctx context.Context, _ teaching.PublicationReader, draft teaching.Draft) error {
	probe, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	_, err := c.pool.Exec(probe, `UPDATE lesson_drafts SET summary='raced' WHERE lesson_id=$1`, draft.LessonID)
	c.locked = errors.Is(err, context.DeadlineExceeded)
	return errReadinessBlocked
}

func TestPublicationCheckRunsUnderDraftLockAndFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "atomic_check_admin", "admin")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences(lesson_id,mode) VALUES($1,'all')`, lessonID); err != nil {
		t.Fatal(err)
	}
	check := &lockProbeCheck{pool: pool}
	svc := teaching.NewService(teaching.NewPostgresStore(pool), check, time.Now)
	actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "atomic-check", IP: net.ParseIP("192.0.2.60")}
	_, err := svc.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: 1})
	if !errors.Is(err, teaching.ErrNotPublishable) || !check.locked {
		t.Fatalf("err=%v locked=%t", err, check.locked)
	}
	var revisions, finalizations, outbox, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM lesson_revisions WHERE lesson_id=$1`, lessonID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM lesson_revision_finalizations f JOIN lesson_revisions r ON r.id=f.revision_id WHERE r.lesson_id=$1`, lessonID).Scan(&finalizations); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE payload->>'lesson_id'=$1`, lessonID.String()).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_id=$1 AND action='lesson.published'`, lessonID.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if revisions+finalizations+outbox+audits != 0 {
		t.Fatalf("revisions=%d finalizations=%d outbox=%d audits=%d", revisions, finalizations, outbox, audits)
	}
}

func TestPublicationRejectsPersistedUnsafeMarkdown(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "unsafe_snapshot_admin", "admin")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
	if _, err := pool.Exec(ctx, `UPDATE lesson_drafts SET body_markdown='<script>alert(1)</script>' WHERE lesson_id=$1`, lessonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences(lesson_id,mode) VALUES($1,'all')`, lessonID); err != nil {
		t.Fatal(err)
	}
	actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "unsafe-snapshot", IP: net.ParseIP("192.0.2.61")}
	_, err := teaching.NewService(teaching.NewPostgresStore(pool), nil, time.Now).Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: 1})
	if !errors.Is(err, teaching.ErrNotPublishable) {
		t.Fatalf("publish err=%v", err)
	}
}

var _ = uuid.Nil

func TestAdminReadStoreReturnsCatalogDraftAndSourceVersionHistory(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "admin_read_store", "admin")
	studentID := insertTeachingUser(t, pool, "admin_read_student", "student")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
	revisionID := publishFixtureRevision(t, pool, lessonID, adminID, studentID)
	store := teaching.NewPostgresStore(pool)
	items, _, err := store.ListAdminCatalog(ctx, teaching.AdminCatalogInput{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.ID == lessonID && item.Kind == "lesson" && item.Published {
			found = true
		}
	}
	if !found {
		t.Fatalf("lesson absent from catalog: %#v", items)
	}
	detail, err := store.GetAdminLesson(ctx, lessonID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Draft.LessonID != lessonID || detail.Published == nil || detail.Published.ID != revisionID || detail.Published.SourceDraftVersion != 1 {
		t.Fatalf("detail=%#v", detail)
	}
	history, _, err := store.ListAdminRevisions(ctx, lessonID, 20, teaching.RevisionCursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].SourceDraftVersion != 1 {
		t.Fatalf("history=%#v", history)
	}
}
