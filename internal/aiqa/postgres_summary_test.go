package aiqa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresSummaryStableUnionOwnershipAndTitleOnlySearch(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE TABLE users CASCADE`); err != nil {
		t.Fatal(err)
	}
	student, other, admin := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES
($1,'summary-owner','Owner','student','active','x'),($2,'summary-other','Other','student','active','x'),
($3,'summary-admin','Teacher','admin','active','x')`, student, other, admin); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 27, 4, 0, 0, 0, time.UTC)
	teacherHigh, teacherLow, aiHigh, aiLow, foreign := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at,created_at,updated_at) VALUES
($1,$4,'100% teacher high','pending',$5,$5,$5),
($2,$4,'teacher low','pending',$5,$5,$5),
($3,$6,'foreign secret','pending',$5,$5,$5)`,
		teacherHigh, teacherLow, foreign, student, at, other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_threads(id,student_id,title,subject,last_message_at,created_at) VALUES
($1,$3,'AI high','math',$4,$4),($2,$3,'AI low','math',$4,$4)`,
		aiHigh, aiLow, student, at); err != nil {
		t.Fatal(err)
	}
	const bodyOnlySentinel = "private-body-note-sentinel"
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key,created_at)
VALUES($1,$2,$3,'student','initial',$4,$5,$6)`,
		uuid.New(), teacherHigh, student, bodyOnlySentinel, uuid.NewString(), at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO teacher_notes(id,thread_id,author_user_id,body_text,created_at)
VALUES($1,$2,$3,$4,$5)`, uuid.New(), teacherHigh, admin, bodyOnlySentinel, at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_messages(id,thread_id,role,sender_user_id,body_text,idempotency_key,created_at)
VALUES($1,$2,'student',$3,$4,$5,$6)`,
		uuid.New(), aiHigh, student, bodyOnlySentinel, uuid.NewString(), at); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresSummaryStore(pool)
	rows, next, err := store.ListQuestionSummaries(ctx, student, SummaryFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Channel != "teacher" || rows[1].Channel != "teacher" || rows[0].ID.String() < rows[1].ID.String() {
		t.Fatalf("first page=%+v", rows)
	}
	rows2, _, err := store.ListQuestionSummaries(ctx, student, SummaryFilter{Cursor: next, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 2 || rows2[0].Channel != "ai" || rows2[1].Channel != "ai" || rows2[0].ID.String() < rows2[1].ID.String() {
		t.Fatalf("second page=%+v next=%+v", rows2, next)
	}
	for _, row := range append(rows, rows2...) {
		if row.ID == foreign || row.Title == "foreign secret" {
			t.Fatalf("foreign row leaked: %+v", row)
		}
	}
	literalPercent, _, err := store.ListQuestionSummaries(ctx, student, SummaryFilter{Search: "%", Limit: 10})
	if err != nil || len(literalPercent) != 1 || literalPercent[0].ID != teacherHigh {
		t.Fatalf("escaped search rows=%+v err=%v", literalPercent, err)
	}
	aiOnly, _, err := store.ListQuestionSummaries(ctx, student, SummaryFilter{Channel: "ai", Search: "ai", Limit: 10})
	if err != nil || len(aiOnly) != 2 {
		t.Fatalf("AI filter=%+v err=%v", aiOnly, err)
	}
	bodyOnly, _, err := store.ListQuestionSummaries(ctx, student, SummaryFilter{Search: bodyOnlySentinel, Limit: 10})
	if err != nil || len(bodyOnly) != 0 {
		t.Fatalf("message/note body matched title-only search rows=%+v err=%v", bodyOnly, err)
	}
}

func TestPostgresSummaryUsesSameLatestRetryOrderingAsThreadDetail(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	store := NewPostgresRuntimeStore(pool)
	in := fixture.admission()
	at := time.Date(2026, 7, 27, 5, 0, 0, 0, time.UTC)
	in.Now = at
	_, first, err := store.AdmitRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CancelRun(ctx, fixture.student, first.ID, at); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(fixture.cfg.Prompt.Body))
	_, retry, err := store.RetryRun(ctx, RuntimeRetryAdmission{
		StudentID:      fixture.student,
		SourceRunID:    first.ID,
		RunID:          uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		IdempotencyKey: "summary-retry-order-0001",
		Snapshot: RuntimeSnapshot{
			Provider:     fixture.cfg,
			PromptSHA256: hex.EncodeToString(digest[:]),
		},
		Reservation: QuotaReservation{
			RequestCount:     1,
			TokenCount:       2000,
			DayKey:           "2026-07-27",
			MonthKey:         "2026-07",
			EstimatorVersion: CurrentEstimatorVersion,
		},
		Now: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.GetThread(ctx, fixture.student, first.ThreadID, MessageCursor{})
	if err != nil {
		t.Fatal(err)
	}
	summaries, _, err := NewPostgresSummaryStore(pool).ListQuestionSummaries(ctx, fixture.student, SummaryFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if detail.ActiveRun == nil || detail.ActiveRun.ID != retry.ID || len(summaries) != 1 ||
		summaries[0].RawStatus != string(detail.ActiveRun.Status) {
		t.Fatalf("detail run=%+v summary=%+v want retry=%s", detail.ActiveRun, summaries, retry.ID)
	}
}
