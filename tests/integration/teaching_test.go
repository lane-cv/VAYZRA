package integration_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

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
