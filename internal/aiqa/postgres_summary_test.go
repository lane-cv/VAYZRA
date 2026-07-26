package aiqa

import (
	"context"
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
	student, other := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES
($1,'summary-owner','Owner','student','active','x'),($2,'summary-other','Other','student','active','x')`, student, other); err != nil {
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
}
