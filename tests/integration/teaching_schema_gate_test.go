package integration_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/students"
	"happylearn.local/app/internal/teaching"
	"happylearn.local/app/tests/integration"
)

func TestTeachingDisableRevokesSessionsAndPreservesRevisionAudience(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	users := auth.NewPostgresUserStore(pool)
	admin, err := users.Create(ctx, auth.CreateUserParams{Username: "disable_admin", DisplayName: "Admin", Role: auth.RoleAdmin, Status: auth.StatusActive, PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	student, err := users.Create(ctx, auth.CreateUserParams{Username: "disable_student", DisplayName: "Student", Role: auth.RoleStudent, Status: auth.StatusActive, PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, admin.ID)
	revisionID := publishFixtureRevision(t, pool, lessonID, admin.ID, student.ID)
	now := time.Now().UTC()
	hash := sha256.Sum256([]byte("teaching-disable-session"))
	if err := auth.NewPostgresSessionStore(pool).Create(ctx, auth.CreateSessionParams{UserID: student.ID, TokenHash: hash, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	svc := students.NewService(users, students.NewPostgresUnitOfWork(pool), auth.PasswordHasher{}, time.Now)
	actor := students.Principal{User: admin, RequestID: "disable-with-history", IP: net.ParseIP("192.0.2.60")}
	if err := svc.SetStatus(ctx, actor, student.ID, auth.StatusDisabled); err != nil {
		t.Fatalf("disable referenced student: %v", err)
	}
	var status, revokedReason string
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT u.status,s.revoked_at,s.revoke_reason FROM users u JOIN sessions s ON s.user_id=u.id WHERE u.id=$1`, student.ID).Scan(&status, &revokedAt, &revokedReason); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" || revokedAt == nil || revokedReason != "student disabled" {
		t.Fatalf("status=%q revoked_at=%v reason=%q", status, revokedAt, revokedReason)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET deleted_at=now() WHERE id=$1`, student.ID); err != nil {
		t.Fatalf("soft-delete referenced student: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM lesson_revision_audience_users WHERE revision_id=$1 AND user_id=$2`, revisionID, student.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("historical revision audience rows=%d err=%v", rows, err)
	}
}

func TestTeachingPublicationRecordsSourceDraftVersionAndRejectsReplay(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	adminID := insertTeachingUser(t, pool, "source_version_admin", "admin")
	studentID := insertTeachingUser(t, pool, "source_version_student", "student")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, adminID)
	actor := teaching.Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "source-version", IP: net.ParseIP("192.0.2.61")}
	svc := teaching.NewService(teaching.NewPostgresStore(pool), nil, time.Now)
	draft, err := svc.SaveDraft(ctx, actor, teaching.SaveDraftInput{LessonID: lessonID, ExpectedVersion: 1, Title: "Lesson", BodyMarkdown: "Body", Audience: teaching.Audience{Mode: teaching.AudienceSelected, UserIDs: []uuid.UUID{studentID}}})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := svc.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: draft.LockVersion})
	if err != nil {
		t.Fatal(err)
	}
	var sourceVersion int64
	if err := pool.QueryRow(ctx, `SELECT source_draft_version FROM lesson_revisions WHERE id=$1`, revision.ID).Scan(&sourceVersion); err != nil || sourceVersion != draft.LockVersion {
		t.Fatalf("source draft version=%d want=%d err=%v", sourceVersion, draft.LockVersion, err)
	}
	if _, err := svc.Publish(ctx, actor, teaching.PublishInput{LessonID: lessonID, ExpectedVersion: draft.LockVersion}); !errors.Is(err, teaching.ErrConflict) {
		t.Fatalf("duplicate publish error=%v, want conflict", err)
	}
	var revisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM lesson_revisions WHERE lesson_id=$1`, lessonID).Scan(&revisions); err != nil || revisions != 1 {
		t.Fatalf("revision count=%d err=%v", revisions, err)
	}
	if _, ok := reflect.TypeOf(teaching.Revision{}).FieldByName("SourceDraftVersion"); !ok {
		t.Fatal("Revision contract lacks SourceDraftVersion")
	}
}

func TestTeachingAudienceModeOwnsSelectedUsers(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	teacher := insertTeachingUser(t, pool, "mode_teacher", "admin")
	student := insertTeachingUser(t, pool, "mode_student", "student")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences (lesson_id,mode) VALUES ($1,'all')`, lessonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audience_users (lesson_id,user_id) VALUES ($1,$2)`, lessonID, student); err == nil {
		t.Fatal("all-mode draft accepted a selected user")
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_draft_audiences SET mode='selected' WHERE lesson_id=$1`, lessonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audience_users (lesson_id,user_id) VALUES ($1,$2)`, lessonID, student); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_draft_audiences SET mode='all' WHERE lesson_id=$1`, lessonID); err == nil {
		t.Fatal("draft changed to all while retaining selected users")
	}
	revisionID := insertUnfinalizedRevision(t, pool, lessonID, teacher)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audiences (revision_id,mode) VALUES ($1,'all')`, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audience_users (revision_id,user_id) VALUES ($1,$2)`, revisionID, student); err == nil {
		t.Fatal("all-mode revision accepted a selected user")
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_revision_audiences SET mode='selected' WHERE revision_id=$1`, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audience_users (revision_id,user_id) VALUES ($1,$2)`, revisionID, student); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_revision_audiences SET mode='all' WHERE revision_id=$1`, revisionID); err == nil {
		t.Fatal("revision changed to all while retaining selected users")
	}
}

func TestTeachingFinalizationSerializesWithEveryChildMutation(t *testing.T) {
	scenarios := []struct {
		name string
		sql  string
		args func(serializationFixture) []any
	}{
		{"audience-header-insert", `INSERT INTO lesson_revision_audiences (revision_id,mode) VALUES ($1,'all')`, func(f serializationFixture) []any { return []any{f.sourceRevision} }},
		{"audience-header-update", `UPDATE lesson_revision_audiences SET mode='all' WHERE revision_id=$1`, func(f serializationFixture) []any { return []any{f.sourceRevision} }},
		{"audience-header-delete", `DELETE FROM lesson_revision_audiences WHERE revision_id=$1`, func(f serializationFixture) []any { return []any{f.sourceRevision} }},
		{"audience-user-insert", `INSERT INTO lesson_revision_audience_users (revision_id,user_id) VALUES ($1,$2)`, func(f serializationFixture) []any { return []any{f.sourceRevision, f.secondStudent} }},
		{"audience-user-move", `UPDATE lesson_revision_audience_users SET revision_id=$2 WHERE revision_id=$1 AND user_id=$3`, func(f serializationFixture) []any { return []any{f.sourceRevision, f.targetRevision, f.student} }},
		{"audience-user-delete", `DELETE FROM lesson_revision_audience_users WHERE revision_id=$1 AND user_id=$2`, func(f serializationFixture) []any { return []any{f.sourceRevision, f.student} }},
		{"external-video-insert", `INSERT INTO lesson_revision_external_videos (revision_id,url,title) VALUES ($1,'https://new.example.test','New')`, func(f serializationFixture) []any { return []any{f.sourceRevision} }},
		{"external-video-move", `UPDATE lesson_revision_external_videos SET revision_id=$2 WHERE id=$1`, func(f serializationFixture) []any { return []any{f.video, f.targetRevision} }},
		{"external-video-delete", `DELETE FROM lesson_revision_external_videos WHERE id=$1`, func(f serializationFixture) []any { return []any{f.video} }},
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := integration.StartPostgres(t)
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			resetTeachingTables(t, pool)
			fixture := insertSerializationFixture(t, pool)
			finalizeTx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer finalizeTx.Rollback(context.Background())
			if _, err := finalizeTx.Exec(ctx, `SELECT finalize_lesson_revision($1)`, fixture.sourceRevision); err != nil {
				t.Fatal(err)
			}
			mutationDone := make(chan error, 1)
			go func() {
				mutationCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				tx, err := pool.Begin(mutationCtx)
				if err == nil {
					_, err = tx.Exec(mutationCtx, tc.sql, tc.args(fixture)...)
				}
				if err == nil {
					err = tx.Commit(mutationCtx)
				} else if tx != nil {
					_ = tx.Rollback(context.Background())
				}
				mutationDone <- err
			}()
			select {
			case err := <-mutationDone:
				t.Fatalf("mutation completed before finalization committed: %v", err)
			case <-time.After(150 * time.Millisecond):
			}
			if err := finalizeTx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-mutationDone:
				if err == nil {
					t.Fatal("after-freeze mutation committed")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("mutation did not finish after finalization committed")
			}
		})
	}
}

type serializationFixture struct {
	sourceRevision uuid.UUID
	targetRevision uuid.UUID
	student        uuid.UUID
	secondStudent  uuid.UUID
	video          uuid.UUID
}

func insertSerializationFixture(t *testing.T, pool *pgxpool.Pool) serializationFixture {
	t.Helper()
	ctx := context.Background()
	teacher := insertTeachingUser(t, pool, "lock_teacher", "admin")
	student := insertTeachingUser(t, pool, "lock_student", "student")
	secondStudent := insertTeachingUser(t, pool, "lock_student_2", "student")
	ids := insertCatalogPath(t, pool)
	sourceLesson := insertLessonDraft(t, pool, ids.chapterID, teacher)
	targetLesson := insertLessonDraft(t, pool, ids.chapterID, teacher)
	sourceRevision := insertUnfinalizedRevision(t, pool, sourceLesson, teacher)
	targetRevision := insertUnfinalizedRevision(t, pool, targetLesson, teacher)
	for _, revisionID := range []uuid.UUID{sourceRevision, targetRevision} {
		if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audiences (revision_id,mode) VALUES ($1,'selected')`, revisionID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audience_users (revision_id,user_id) VALUES ($1,$2)`, sourceRevision, student); err != nil {
		t.Fatal(err)
	}
	var video uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO lesson_revision_external_videos (revision_id,url,title) VALUES ($1,'https://video.example.test','Video') RETURNING id`, sourceRevision).Scan(&video); err != nil {
		t.Fatal(err)
	}
	return serializationFixture{sourceRevision: sourceRevision, targetRevision: targetRevision, student: student, secondStudent: secondStudent, video: video}
}
