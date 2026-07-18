package integration_test

import (
	"context"
	"errors"
	"math"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/teaching"
	"happylearn.local/app/tests/integration"
)

func TestStudentCatalogFiveLevelFilteredPaginationUsesOneAuthorizedHierarchy(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	var databaseName, currentUser string
	if err := pool.QueryRow(ctx, `SELECT current_database(),current_user`).Scan(&databaseName, &currentUser); err != nil {
		t.Fatal(err)
	}
	if databaseName != "happylearn" || currentUser != "happylearn" {
		t.Fatalf("unexpected disposable postgres identity db=%q user=%q", databaseName, currentUser)
	}
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	teacher := insertTeachingUser(t, pool, "page_teacher", "admin")
	student := insertTeachingUser(t, pool, "page_student", "student")
	first := insertCatalogPath(t, pool)
	firstLesson := insertLessonDraft(t, pool, first.chapterID, teacher)
	publishRevisionForAudience(t, pool, firstLesson, teacher, "all", nil, "First lesson", "first body")
	var grade2, term2, subject2, chapter2 uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO grades(name,sort_key) VALUES('Grade 2',2048) RETURNING id`).Scan(&grade2); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO terms(grade_id,name,sort_key) VALUES($1,'Term 2',2048) RETURNING id`, grade2).Scan(&term2); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO subjects(term_id,name,sort_key) VALUES($1,'Science',2048) RETURNING id`, term2).Scan(&subject2); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO chapters(subject_id,name,sort_key) VALUES($1,'Chapter 2',2048) RETURNING id`, subject2).Scan(&chapter2); err != nil {
		t.Fatal(err)
	}
	secondLesson := insertLessonDraft(t, pool, chapter2, teacher)
	publishRevisionForAudience(t, pool, secondLesson, teacher, "all", nil, "Second lesson", "second body")
	svc := teaching.NewStudentService(teaching.NewPostgresStore(pool), time.Now)
	actor := teaching.Principal{User: auth.User{ID: student, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "page", IP: net.ParseIP("192.0.2.70")}

	var all []teaching.StudentCatalogNode
	var cursor teaching.CatalogCursor
	for {
		page, next, err := svc.Browse(ctx, actor, teaching.BrowseInput{Limit: 2, After: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if cursor != (teaching.CatalogCursor{}) {
			repeated, repeatNext, err := svc.Browse(ctx, actor, teaching.BrowseInput{Limit: 2, After: cursor})
			if err != nil || len(repeated) != len(page) || repeatNext != next {
				t.Fatalf("unstable page repeated=%#v next=%#v err=%v", repeated, repeatNext, err)
			}
			for i := range page {
				if repeated[i].ID != page[i].ID {
					t.Fatalf("unstable item %d", i)
				}
			}
		}
		all = append(all, page...)
		if next == (teaching.CatalogCursor{}) {
			break
		}
		cursor = next
	}
	if len(all) != 10 {
		t.Fatalf("five-level nodes=%d want 10: %#v", len(all), all)
	}
	counts := map[teaching.CatalogKind]int{}
	for _, node := range all {
		counts[node.Kind]++
	}
	for _, kind := range []teaching.CatalogKind{teaching.CatalogGrade, teaching.CatalogTerm, teaching.CatalogSubject, teaching.CatalogChapter, teaching.CatalogLesson} {
		if counts[kind] != 2 {
			t.Fatalf("kind %s count=%d", kind, counts[kind])
		}
	}
	filtered, _, err := svc.Browse(ctx, actor, teaching.BrowseInput{GradeID: grade2, TermID: term2, SubjectID: subject2, ChapterID: chapter2, Limit: 20})
	if err != nil || len(filtered) != 5 {
		t.Fatalf("filtered=%#v err=%v", filtered, err)
	}
	for _, node := range filtered {
		if node.ID == firstLesson || node.ParentID == first.chapterID {
			t.Fatalf("cross-branch node=%#v", node)
		}
	}
	mismatch, _, err := svc.Browse(ctx, actor, teaching.BrowseInput{GradeID: grade2, TermID: filtered[1].ID, ChapterID: first.chapterID, Limit: 20})
	if !errors.Is(err, teaching.ErrNotFound) || mismatch != nil {
		t.Fatalf("mismatched hierarchy=%#v err=%v, want not found", mismatch, err)
	}
}

type filteredBrowseFixture struct {
	teacher, student, other                    uuid.UUID
	grade, term, subject, chapter, lesson      uuid.UUID
	grade2, term2, subject2, chapter2, lesson2 uuid.UUID
}

func setupFilteredBrowseFixture(t *testing.T, pool *pgxpool.Pool) filteredBrowseFixture {
	t.Helper()
	ctx := context.Background()
	resetTeachingTables(t, pool)
	f := filteredBrowseFixture{}
	f.teacher = insertTeachingUser(t, pool, "filter_teacher", "admin")
	f.student = insertTeachingUser(t, pool, "filter_student", "student")
	f.other = insertTeachingUser(t, pool, "filter_other", "student")
	path := insertCatalogPath(t, pool)
	f.chapter = path.chapterID
	if err := pool.QueryRow(ctx, `SELECT s.id,t.id,g.id FROM chapters c JOIN subjects s ON s.id=c.subject_id JOIN terms t ON t.id=s.term_id JOIN grades g ON g.id=t.grade_id WHERE c.id=$1`, f.chapter).Scan(&f.subject, &f.term, &f.grade); err != nil {
		t.Fatal(err)
	}
	f.lesson = insertLessonDraft(t, pool, f.chapter, f.teacher)
	publishRevisionForAudience(t, pool, f.lesson, f.teacher, "all", nil, "Authorized first", "body")
	if err := pool.QueryRow(ctx, `INSERT INTO grades(name,sort_key) VALUES('Grade filter 2',2048) RETURNING id`).Scan(&f.grade2); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO terms(grade_id,name,sort_key) VALUES($1,'Term filter 2',2048) RETURNING id`, f.grade2).Scan(&f.term2); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO subjects(term_id,name,sort_key) VALUES($1,'Subject filter 2',2048) RETURNING id`, f.term2).Scan(&f.subject2); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO chapters(subject_id,name,sort_key) VALUES($1,'Chapter filter 2',2048) RETURNING id`, f.subject2).Scan(&f.chapter2); err != nil {
		t.Fatal(err)
	}
	f.lesson2 = insertLessonDraft(t, pool, f.chapter2, f.teacher)
	publishRevisionForAudience(t, pool, f.lesson2, f.teacher, "all", nil, "Authorized second", "body")
	return f
}

func TestStudentCatalogFilteredBrowseValidationIsIndistinguishableAndSingleSnapshot(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	maxID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	type testCase struct {
		name       string
		emptySetup bool
		input      func(*testing.T, filteredBrowseFixture) teaching.BrowseInput
		wantCount  int
		want404    bool
	}
	cases := []testCase{
		{name: "missing grade", input: func(*testing.T, filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{GradeID: uuid.New()}
		}, want404: true},
		{name: "missing term", input: func(*testing.T, filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{TermID: uuid.New()}
		}, want404: true},
		{name: "missing subject", input: func(*testing.T, filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{SubjectID: uuid.New()}
		}, want404: true},
		{name: "missing chapter", input: func(*testing.T, filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{ChapterID: uuid.New()}
		}, want404: true},
		{name: "archived grade", input: func(t *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			if _, err := pool.Exec(ctx, `UPDATE grades SET archived_at=now() WHERE id=$1`, f.grade); err != nil {
				t.Fatal(err)
			}
			return teaching.BrowseInput{GradeID: f.grade}
		}, want404: true},
		{name: "archived term", input: func(t *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			if _, err := pool.Exec(ctx, `UPDATE terms SET archived_at=now() WHERE id=$1`, f.term); err != nil {
				t.Fatal(err)
			}
			return teaching.BrowseInput{TermID: f.term}
		}, want404: true},
		{name: "archived subject", input: func(t *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			if _, err := pool.Exec(ctx, `UPDATE subjects SET archived_at=now() WHERE id=$1`, f.subject); err != nil {
				t.Fatal(err)
			}
			return teaching.BrowseInput{SubjectID: f.subject}
		}, want404: true},
		{name: "archived chapter", input: func(t *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			if _, err := pool.Exec(ctx, `UPDATE chapters SET archived_at=now() WHERE id=$1`, f.chapter); err != nil {
				t.Fatal(err)
			}
			return teaching.BrowseInput{ChapterID: f.chapter}
		}, want404: true},
		{name: "archived lesson leaves no authorized target", input: func(t *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			if _, err := pool.Exec(ctx, `UPDATE lessons SET archived_at=now() WHERE id=$1`, f.lesson); err != nil {
				t.Fatal(err)
			}
			return teaching.BrowseInput{ChapterID: f.chapter}
		}, want404: true},
		{name: "unauthorized selected audience", input: func(t *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			if _, err := pool.Exec(ctx, `UPDATE lessons SET archived_at=now() WHERE id=$1`, f.lesson); err != nil {
				t.Fatal(err)
			}
			privateLesson := insertLessonDraft(t, pool, f.chapter, f.teacher)
			publishRevisionForAudience(t, pool, privateLesson, f.teacher, "selected", []uuid.UUID{f.other}, "Private", "body")
			return teaching.BrowseInput{ChapterID: f.chapter}
		}, want404: true},
		{name: "mismatched grade and term", input: func(_ *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{GradeID: f.grade, TermID: f.term2}
		}, want404: true},
		{name: "mismatched term and subject", input: func(_ *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{TermID: f.term, SubjectID: f.subject2}
		}, want404: true},
		{name: "mismatched subject and chapter", input: func(_ *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{SubjectID: f.subject, ChapterID: f.chapter2}
		}, want404: true},
		{name: "mismatched grade and chapter", input: func(_ *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{GradeID: f.grade2, ChapterID: f.chapter}
		}, want404: true},
		{name: "valid filtered first page", input: func(_ *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{GradeID: f.grade, TermID: f.term, SubjectID: f.subject, ChapterID: f.chapter}
		}, wantCount: 5},
		{name: "valid filtered terminal empty page", input: func(_ *testing.T, f filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{ChapterID: f.chapter, After: teaching.CatalogCursor{KindRank: 5, SortKey: math.MaxInt64, ID: maxID}}
		}},
		{name: "unfiltered empty catalog", emptySetup: true, input: func(*testing.T, filteredBrowseFixture) teaching.BrowseInput {
			return teaching.BrowseInput{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetTeachingTables(t, pool)
			var f filteredBrowseFixture
			if !tc.emptySetup {
				f = setupFilteredBrowseFixture(t, pool)
			}
			actor := teaching.Principal{User: auth.User{ID: f.student, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "filtered", IP: net.ParseIP("192.0.2.73")}
			if tc.emptySetup {
				actor.User.ID = insertTeachingUser(t, pool, "empty_catalog_student", "student")
			}
			input := tc.input(t, f)
			input.Limit = 20
			nodes, _, err := teaching.NewStudentService(teaching.NewPostgresStore(pool), time.Now).Browse(ctx, actor, input)
			if tc.want404 {
				if !errors.Is(err, teaching.ErrNotFound) || nodes != nil {
					t.Fatalf("nodes=%#v err=%v, want indistinguishable not found", nodes, err)
				}
				return
			}
			if err != nil || len(nodes) != tc.wantCount {
				t.Fatalf("nodes=%#v err=%v wantCount=%d", nodes, err, tc.wantCount)
			}
		})
	}
}
func TestStudentRecentPositionEqualTimestampAndCurrentRevisionIsolation(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	teacher := insertTeachingUser(t, pool, "recent_teacher", "admin")
	studentA := insertTeachingUser(t, pool, "recent_a", "student")
	studentB := insertTeachingUser(t, pool, "recent_b", "student")
	path := insertCatalogPath(t, pool)
	lesson1 := insertLessonDraft(t, pool, path.chapterID, teacher)
	lesson2 := insertLessonDraft(t, pool, path.chapterID, teacher)
	revision1 := publishRevisionForAudience(t, pool, lesson1, teacher, "all", nil, "Recent one", "body one")
	revision2 := publishRevisionForAudience(t, pool, lesson2, teacher, "all", nil, "Recent two", "body two")
	svc := teaching.NewStudentService(teaching.NewPostgresStore(pool), time.Now)
	actorA := teaching.Principal{User: auth.User{ID: studentA, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "recent", IP: net.ParseIP("192.0.2.71")}
	withoutProgress, err := svc.GetLesson(ctx, actorA, lesson1)
	if err != nil || withoutProgress.Progress != nil {
		t.Fatalf("lesson without progress=%#v err=%v", withoutProgress, err)
	}
	observed := time.Now().UTC().Add(-5 * time.Minute)
	if err := svc.UpdateProgress(ctx, actorA, teaching.ProgressInput{RevisionID: revision1, Anchor: "original", ScrollRatio: .2, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateProgress(ctx, actorA, teaching.ProgressInput{RevisionID: revision1, Viewed: true, Anchor: "equal-overwrite", ScrollRatio: .9, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	position, err := svc.GetPosition(ctx, actorA, lesson1)
	if err != nil || position.Anchor != "original" || position.ScrollRatio != .2 || position.Viewed {
		t.Fatalf("equal timestamp position=%#v err=%v", position, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lesson_progress(user_id,revision_id,anchor,observed_at,last_viewed_at) VALUES($1,$2,'other-student',$3,$4)`, studentB, revision1, observed, observed.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.GetLesson(ctx, actorA, lesson1)
	if err != nil || detail.Progress == nil || detail.Progress.Anchor != "original" || detail.Progress.ObservedAt.Location() != time.UTC || detail.Progress.FirstViewedAt.Location() != time.UTC || detail.Progress.LastViewedAt.Location() != time.UTC {
		t.Fatalf("student detail=%#v err=%v", detail, err)
	}
	if err := svc.UpdateProgress(ctx, actorA, teaching.ProgressInput{RevisionID: revision2, Anchor: "newest", ObservedAt: observed.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_progress SET last_viewed_at=$3 WHERE user_id=$1 AND revision_id=$2`, studentA, revision1, observed); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lesson_progress SET last_viewed_at=$3 WHERE user_id=$1 AND revision_id=$2`, studentA, revision2, observed.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	recent, err := svc.Recent(ctx, actorA, 10)
	if err != nil || len(recent) != 2 || recent[0].LessonID != lesson2 || recent[0].Position.Anchor != "newest" {
		t.Fatalf("recent=%#v err=%v", recent, err)
	}
	replacement := publishRevisionVersion(t, pool, lesson1, teacher, 2, "all")
	if _, err := svc.GetPosition(ctx, actorA, lesson1); !errors.Is(err, teaching.ErrNotFound) {
		t.Fatalf("stale position error=%v", err)
	}
	if detail, err := svc.GetLesson(ctx, actorA, lesson1); err != nil || detail.Revision.ID != replacement || detail.Progress != nil {
		t.Fatalf("replacement detail=%#v err=%v", detail, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE lessons SET archived_at=now() WHERE id=$1`, lesson1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetLesson(ctx, actorA, lesson1); !errors.Is(err, teaching.ErrNotFound) {
		t.Fatalf("archived lesson error=%v", err)
	}
}

func TestStudentSearchShortQueryTitleOnlyAndCompactSnippet(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetTeachingTables(t, pool)
	teacher := insertTeachingUser(t, pool, "search_teacher", "admin")
	student := insertTeachingUser(t, pool, "search_student", "student")
	path := insertCatalogPath(t, pool)
	bodyOnly := insertLessonDraft(t, pool, path.chapterID, teacher)
	titleMatch := insertLessonDraft(t, pool, path.chapterID, teacher)
	longBody := "# **数学** " + strings.Repeat("正文 ", 200)
	publishRevisionForAudience(t, pool, bodyOnly, teacher, "all", nil, "Other title", longBody)
	publishRevisionForAudience(t, pool, titleMatch, teacher, "all", nil, "数学课", "bodyneedle "+longBody)
	svc := teaching.NewStudentService(teaching.NewPostgresStore(pool), time.Now)
	actor := teaching.Principal{User: auth.User{ID: student, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "search", IP: net.ParseIP("192.0.2.72")}
	short, _, err := svc.Search(ctx, actor, teaching.SearchInput{Query: "数学"})
	if err != nil || len(short) != 1 || short[0].LessonID != titleMatch {
		t.Fatalf("short results=%#v err=%v", short, err)
	}
	long, _, err := svc.Search(ctx, actor, teaching.SearchInput{Query: "bodyneedle"})
	if err != nil || len(long) != 1 || long[0].LessonID != titleMatch {
		t.Fatalf("long results=%#v err=%v", long, err)
	}
	if utf8.RuneCountInString(long[0].Snippet) > 240 || strings.ContainsAny(long[0].Snippet, "#*[]()~") {
		t.Fatalf("snippet not compact/plain runes=%d %q", utf8.RuneCountInString(long[0].Snippet), long[0].Snippet)
	}
}
