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

func TestStudentCatalogDoesNotEnumerateOtherStudentsLessons(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)

	teacher := insertTeachingUser(t, pool, "catalog_teacher", "admin")
	studentA := insertTeachingUser(t, pool, "catalog_student_a", "student")
	studentB := insertTeachingUser(t, pool, "catalog_student_b", "student")
	ids := insertCatalogPath(t, pool)
	allLesson := insertLessonDraft(t, pool, ids.chapterID, teacher)
	forA := insertLessonDraft(t, pool, ids.chapterID, teacher)
	forB := insertLessonDraft(t, pool, ids.chapterID, teacher)
	allRevision := publishRevisionForAudience(t, pool, allLesson, teacher, "all", nil, "All students", "shared body")
	publishRevisionForAudience(t, pool, forA, teacher, "selected", []uuid.UUID{studentA}, "A only", "alpha body")
	forBRevision := publishRevisionForAudience(t, pool, forB, teacher, "selected", []uuid.UUID{studentB}, "B only", "bravo private body")

	svc := teaching.NewStudentService(teaching.NewPostgresStore(pool), time.Now)
	principalA := teaching.Principal{User: auth.User{ID: studentA, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "catalog-a", IP: net.ParseIP("192.0.2.60")}
	if _, err := svc.GetLesson(ctx, principalA, forB); !errors.Is(err, teaching.ErrNotFound) {
		t.Fatalf("read of B-only lesson error = %v, want not found", err)
	}
	if _, err := svc.GetLesson(ctx, principalA, forA); err != nil {
		t.Fatalf("read of A-only lesson: %v", err)
	}
	if nodes, err := svc.Browse(ctx, principalA, teaching.BrowseInput{}); err != nil || len(nodes) != 4 {
		t.Fatalf("browse nodes=%d err=%v, want visible active path", len(nodes), err)
	}
	results, _, err := svc.Search(ctx, principalA, teaching.SearchInput{Query: "alpha", Limit: 10})
	if err != nil || len(results) != 1 || results[0].LessonID != forA {
		t.Fatalf("search alpha results=%#v err=%v", results, err)
	}
	results, _, err = svc.Search(ctx, principalA, teaching.SearchInput{Query: "bravo", Limit: 10})
	if err != nil || len(results) != 0 {
		t.Fatalf("private search results=%#v err=%v", results, err)
	}

	draftOnly := insertLessonDraft(t, pool, ids.chapterID, teacher)
	if _, err := pool.Exec(ctx, `UPDATE lesson_drafts SET body_markdown = 'draft only text' WHERE lesson_id = $1`, draftOnly); err != nil {
		t.Fatal(err)
	}
	results, _, err = svc.Search(ctx, principalA, teaching.SearchInput{Query: "draft only", Limit: 10})
	if err != nil || len(results) != 0 {
		t.Fatalf("draft search results=%#v err=%v", results, err)
	}

	first := time.Now().UTC().Add(-2 * time.Minute)
	if err := svc.UpdateProgress(ctx, principalA, teaching.ProgressInput{RevisionID: allRevision, Anchor: "first", ScrollRatio: 0.25, ObservedAt: first}); err != nil {
		t.Fatalf("initial progress: %v", err)
	}
	second := first.Add(time.Minute)
	if err := svc.UpdateProgress(ctx, principalA, teaching.ProgressInput{RevisionID: allRevision, Viewed: true, Anchor: "second", ScrollRatio: 0.75, ObservedAt: second}); err != nil {
		t.Fatalf("newer progress: %v", err)
	}
	if err := svc.UpdateProgress(ctx, principalA, teaching.ProgressInput{RevisionID: allRevision, Anchor: "stale", ScrollRatio: 0.1, ObservedAt: first}); err != nil {
		t.Fatalf("stale progress: %v", err)
	}
	var viewed bool
	var anchor string
	var ratio float64
	if err := pool.QueryRow(ctx, `SELECT viewed,anchor,scroll_ratio FROM lesson_progress WHERE user_id=$1 AND revision_id=$2`, studentA, allRevision).Scan(&viewed, &anchor, &ratio); err != nil {
		t.Fatal(err)
	}
	if !viewed || anchor != "second" || ratio != 0.75 {
		t.Fatalf("progress=%t %q %v, want monotonic second update", viewed, anchor, ratio)
	}
	if err := svc.UpdateProgress(ctx, principalA, teaching.ProgressInput{RevisionID: forBRevision, Anchor: "blocked", ObservedAt: second}); !errors.Is(err, teaching.ErrNotFound) {
		t.Fatalf("other student's progress error=%v, want not found", err)
	}
	replacement := publishRevisionVersion(t, pool, allLesson, teacher, 2, "all")
	if err := svc.UpdateProgress(ctx, principalA, teaching.ProgressInput{RevisionID: allRevision, Anchor: "stale revision", ObservedAt: second}); !errors.Is(err, teaching.ErrNotFound) {
		t.Fatalf("old revision progress error=%v, want not found", err)
	}
	if err := svc.UpdateProgress(ctx, principalA, teaching.ProgressInput{RevisionID: replacement, Anchor: "current revision", ObservedAt: second}); err != nil {
		t.Fatalf("current revision progress: %v", err)
	}
}

func TestTeachingSchemaEnforcesHierarchyAudienceAndImmutableRevisions(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)

	teacher := insertTeachingUser(t, pool, "teacher", "admin")
	student := insertTeachingUser(t, pool, "student_1", "student")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)

	if _, err := pool.Exec(ctx, `INSERT INTO terms (grade_id, name) VALUES ($1, 'orphan term')`, uuid.New()); err == nil {
		t.Fatal("term without grade succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO grades (name) VALUES (' grade 1 ')`); err == nil {
		t.Fatal("duplicate normalized grade name succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences (lesson_id, mode) VALUES ($1, 'selected')`, lessonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audience_users (lesson_id, user_id) VALUES ($1, $2)`, lessonID, teacher); err == nil {
		t.Fatal("selected audience accepted an admin")
	}

	revisionID := publishFixtureRevision(t, pool, lessonID, teacher, student)
	if _, err := pool.Exec(ctx, `UPDATE lesson_revisions SET title = 'mutated' WHERE id = $1`, revisionID); err == nil {
		t.Fatal("published revision update succeeded")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM lesson_revisions WHERE id = $1`, revisionID); err == nil {
		t.Fatal("published revision deletion succeeded")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM lesson_revision_audience_users WHERE revision_id = $1 AND user_id = $2`, revisionID, student); err == nil {
		t.Fatal("frozen revision audience deletion succeeded")
	}

	if _, err := pool.Exec(ctx, `INSERT INTO lesson_progress (user_id, revision_id) VALUES ($1, $2)`, student, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_progress (user_id, revision_id) VALUES ($1, $2)`, student, revisionID); err == nil {
		t.Fatal("duplicate lesson progress succeeded")
	}
}

type catalogIDs struct{ chapterID uuid.UUID }

func insertCatalogPath(t *testing.T, pool *pgxpool.Pool) catalogIDs {
	t.Helper()
	ctx := context.Background()
	var gradeID, termID, subjectID, chapterID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO grades (name) VALUES ('Grade 1') RETURNING id`).Scan(&gradeID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO terms (grade_id, name) VALUES ($1, 'Term 1') RETURNING id`, gradeID).Scan(&termID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO subjects (term_id, name) VALUES ($1, 'Math') RETURNING id`, termID).Scan(&subjectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO chapters (subject_id, name) VALUES ($1, 'Chapter 1') RETURNING id`, subjectID).Scan(&chapterID); err != nil {
		t.Fatal(err)
	}
	return catalogIDs{chapterID: chapterID}
}

func insertLessonDraft(t *testing.T, pool *pgxpool.Pool, chapterID, teacherID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var lessonID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO lessons (chapter_id) VALUES ($1) RETURNING id`, chapterID).Scan(&lessonID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_drafts (lesson_id, title, body_markdown, updated_by) VALUES ($1, 'Lesson 1', 'Body', $2)`, lessonID, teacherID); err != nil {
		t.Fatal(err)
	}
	return lessonID
}

func publishFixtureRevision(t *testing.T, pool *pgxpool.Pool, lessonID, teacherID, studentID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var revisionID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_revisions (lesson_id, version, title, summary, body_markdown, sort_key, published_by)
		VALUES ($1, 1, 'Lesson 1', '', 'Body', 1024, $2) RETURNING id`, lessonID, teacherID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audiences (revision_id, mode) VALUES ($1, 'selected')`, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audience_users (revision_id, user_id) VALUES ($1, $2)`, revisionID, studentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT finalize_lesson_revision($1)`, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lessons SET published_revision_id = $2 WHERE id = $1`, lessonID, revisionID); err != nil {
		t.Fatal(err)
	}
	return revisionID
}

func publishRevisionForAudience(t *testing.T, pool *pgxpool.Pool, lessonID, teacherID uuid.UUID, mode string, users []uuid.UUID, title, body string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var revisionID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_revisions (lesson_id, version, title, summary, body_markdown, sort_key, published_by)
		VALUES ($1, 1, $2, '', $3, 1024, $4) RETURNING id`, lessonID, title, body, teacherID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audiences (revision_id, mode) VALUES ($1, $2)`, revisionID, mode); err != nil {
		t.Fatal(err)
	}
	for _, studentID := range users {
		if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audience_users (revision_id, user_id) VALUES ($1, $2)`, revisionID, studentID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `SELECT finalize_lesson_revision($1)`, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lessons SET published_revision_id = $2 WHERE id = $1`, lessonID, revisionID); err != nil {
		t.Fatal(err)
	}
	return revisionID
}
func publishRevisionVersion(t *testing.T, pool *pgxpool.Pool, lessonID, teacherID uuid.UUID, version int64, mode string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var revisionID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_revisions (lesson_id, version, title, summary, body_markdown, sort_key, published_by)
		VALUES ($1, $2, 'Replacement', '', 'replacement body', 1024, $3) RETURNING id`, lessonID, version, teacherID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audiences (revision_id, mode) VALUES ($1, $2)`, revisionID, mode); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT finalize_lesson_revision($1)`, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lessons SET published_revision_id = $2 WHERE id = $1`, lessonID, revisionID); err != nil {
		t.Fatal(err)
	}
	return revisionID
}
func insertTeachingUser(t *testing.T, pool *pgxpool.Pool, username, role string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO users (username, display_name, role, status, password_hash)
		VALUES ($1, $1, $2, 'active', 'hash') RETURNING id`, username, role).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func resetTeachingTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `TRUNCATE TABLE grades, users CASCADE`); err != nil {
		t.Fatalf("reset teaching tables: %v", err)
	}
}

func TestTeachingSchemaRejectsCrossLessonPublishedRevision(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)

	teacher := insertTeachingUser(t, pool, "cross_teacher", "admin")
	student := insertTeachingUser(t, pool, "cross_student", "student")
	ids := insertCatalogPath(t, pool)
	firstLessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)
	secondLessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)
	revisionID := publishFixtureRevision(t, pool, firstLessonID, teacher, student)

	if _, err := pool.Exec(ctx, `UPDATE lessons SET published_revision_id = $2 WHERE id = $1`, secondLessonID, revisionID); err == nil {
		t.Fatal("lesson accepted a published revision owned by another lesson")
	}
}

func TestTeachingSchemaFinalizesRevisionChildren(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)

	teacher := insertTeachingUser(t, pool, "freeze_teacher", "admin")
	student := insertTeachingUser(t, pool, "freeze_student", "student")
	secondStudent := insertTeachingUser(t, pool, "freeze_student_2", "student")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)
	revisionID := publishFixtureRevision(t, pool, lessonID, teacher, student)

	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audience_users (revision_id, user_id) VALUES ($1, $2)`, revisionID, secondStudent); err == nil {
		t.Fatal("finalized revision accepted a new audience member")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_external_videos (revision_id, url, title) VALUES ($1, 'https://video.example.test', 'Video')`, revisionID); err == nil {
		t.Fatal("finalized revision accepted a new external video")
	}

	secondLessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)
	secondRevisionID := insertUnfinalizedRevision(t, pool, secondLessonID, teacher)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audiences (revision_id, mode) VALUES ($1, 'all')`, secondRevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT finalize_lesson_revision($1)`, secondRevisionID); err != nil {
		t.Fatalf("finalize revision: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lessons SET published_revision_id = $2 WHERE id = $1`, secondLessonID, secondRevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_revision_audiences SET mode = 'selected' WHERE revision_id = $1`, secondRevisionID); err == nil {
		t.Fatal("finalized revision accepted an audience header mutation")
	}
}

func TestTeachingSchemaRequiresActiveAudienceStudents(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)

	editor := insertTeachingUser(t, pool, "audience_editor", "student")
	inactiveStudent := insertTeachingUserWithStatus(t, pool, "inactive_student", "student", "disabled")
	deletedStudent := insertTeachingUser(t, pool, "deleted_student", "student")
	if _, err := pool.Exec(ctx, `UPDATE users SET deleted_at = now() WHERE id = $1`, deletedStudent); err != nil {
		t.Fatal(err)
	}
	activeStudent := insertTeachingUser(t, pool, "active_student", "student")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, editor)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audiences (lesson_id, mode) VALUES ($1, 'selected')`, lessonID); err != nil {
		t.Fatal(err)
	}

	for _, studentID := range []uuid.UUID{inactiveStudent, deletedStudent} {
		if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audience_users (lesson_id, user_id) VALUES ($1, $2)`, lessonID, studentID); err == nil {
			t.Fatal("selected audience accepted an inactive or deleted student")
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_draft_audience_users (lesson_id, user_id) VALUES ($1, $2)`, lessonID, activeStudent); err != nil {
		t.Fatal(err)
	}
	for _, update := range []string{
		`UPDATE users SET status = 'disabled' WHERE id = $1`,
		`UPDATE users SET role = 'admin' WHERE id = $1`,
		`UPDATE users SET deleted_at = now() WHERE id = $1`,
	} {
		if _, err := pool.Exec(ctx, update, activeStudent); err == nil {
			t.Fatal("referenced audience student was allowed to become ineligible")
		}
	}
}

func insertUnfinalizedRevision(t *testing.T, pool *pgxpool.Pool, lessonID, teacherID uuid.UUID) uuid.UUID {
	t.Helper()
	var revisionID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO lesson_revisions (lesson_id, version, title, summary, body_markdown, sort_key, published_by)
		VALUES ($1, 1, 'Lesson 1', '', 'Body', 1024, $2) RETURNING id`, lessonID, teacherID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	return revisionID
}

func insertTeachingUserWithStatus(t *testing.T, pool *pgxpool.Pool, username, role, status string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO users (username, display_name, role, status, password_hash)
		VALUES ($1, $1, $2, $3, 'hash') RETURNING id`, username, role, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTeachingSchemaPreventsFrozenChildMoves(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)

	teacher := insertTeachingUser(t, pool, "move_teacher", "admin")
	student := insertTeachingUser(t, pool, "move_student", "student")
	ids := insertCatalogPath(t, pool)
	sourceLessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)
	targetLessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)
	sourceRevisionID := insertUnfinalizedRevision(t, pool, sourceLessonID, teacher)
	targetRevisionID := insertUnfinalizedRevision(t, pool, targetLessonID, teacher)
	for _, revisionID := range []uuid.UUID{sourceRevisionID, targetRevisionID} {
		if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audiences (revision_id, mode) VALUES ($1, 'selected')`, revisionID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audience_users (revision_id, user_id) VALUES ($1, $2)`, sourceRevisionID, student); err != nil {
		t.Fatal(err)
	}
	var videoID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO lesson_revision_external_videos (revision_id, url, title)
		VALUES ($1, 'https://video.example.test', 'Video') RETURNING id`, sourceRevisionID).Scan(&videoID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT finalize_lesson_revision($1)`, sourceRevisionID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `UPDATE lesson_revision_audience_users SET revision_id = $2 WHERE revision_id = $1 AND user_id = $3`, sourceRevisionID, targetRevisionID, student); err == nil {
		t.Fatal("frozen audience member moved to an unfinalized revision")
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_revision_external_videos SET revision_id = $2 WHERE id = $1`, videoID, targetRevisionID); err == nil {
		t.Fatal("frozen external video moved to an unfinalized revision")
	}
}

func TestTeachingSchemaRequiresAudienceHeaderBeforeFinalization(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)

	teacher := insertTeachingUser(t, pool, "header_teacher", "admin")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)
	revisionID := insertUnfinalizedRevision(t, pool, lessonID, teacher)
	if _, err := pool.Exec(ctx, `SELECT finalize_lesson_revision($1)`, revisionID); err == nil {
		t.Fatal("revision without audience header finalized")
	}
}

func TestTeachingSchemaRequiresSelectedAudienceMemberBeforeFinalization(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)

	teacher := insertTeachingUser(t, pool, "selected_teacher", "admin")
	ids := insertCatalogPath(t, pool)
	lessonID := insertLessonDraft(t, pool, ids.chapterID, teacher)
	revisionID := insertUnfinalizedRevision(t, pool, lessonID, teacher)
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_revision_audiences (revision_id, mode) VALUES ($1, 'selected')`, revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT finalize_lesson_revision($1)`, revisionID); err == nil {
		t.Fatal("revision with empty selected audience finalized")
	}
}
