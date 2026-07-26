package aiqa

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestAdminUsageFilterIsStrict(t *testing.T) {
	now := time.Now().UTC()
	for _, filter := range []UsageFilter{
		{Status: "other", Limit: 20},
		{Limit: 0},
		{Limit: 101},
		{From: now, To: now.Add(-time.Second), Limit: 20},
		{StudentID: uuid.Nil, ModelID: uuid.Nil, Status: RunSucceeded, From: now.Add(-time.Hour), To: now, Limit: 20},
	} {
		err := validateUsageFilter(filter)
		if filter.Status == RunSucceeded && filter.Limit == 20 {
			if err != nil {
				t.Fatalf("valid filter rejected: %v", err)
			}
		} else if err == nil {
			t.Fatalf("invalid filter accepted: %+v", filter)
		}
	}
}

func TestUsageCursorCanonicalRoundTrip(t *testing.T) {
	cursor := UsageCursor{CreatedAt: time.Date(2026, 7, 27, 2, 3, 4, 5, time.UTC), ID: uuid.MustParse("20000000-0000-4000-8000-000000000001")}
	raw := encodeUsageCursor(cursor)
	got, err := decodeUsageCursor(raw, time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC))
	if err != nil || got != cursor {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestPostgresAdminUsageAggregatesSourcesFiltersAndStablePagination(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := newRuntimeFixture(t, ctx, pool, 20)
	var adminID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE role='admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 7, 27, 5, 0, 0, 0, time.UTC)
	store := NewPostgresRuntimeStore(pool)
	makeRun := func(key string) Run {
		t.Helper()
		in := fixture.admission()
		in.IdempotencyKey, in.Now = key, stamp
		_, run, err := store.AdmitRun(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	finishSuccess := func(run Run, source string, input, output, cost, first, total int64) {
		t.Helper()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err = tx.Exec(ctx, `UPDATE ai_runs SET status='streaming',started_at=$2::timestamptz,updated_at=$2::timestamptz,lease_owner='usage-test',lease_expires_at=$2::timestamptz+interval '1 minute',heartbeat_at=$2::timestamptz WHERE id=$1`, run.ID, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE ai_runs SET status='succeeded',lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,
completed_at=$2,updated_at=$2,input_tokens=$3,output_tokens=$4,cost_micro_usd=$5,usage_source=$6,
first_byte_ms=$7,total_ms=$8 WHERE id=$1`, run.ID, stamp.Add(time.Second), input, output, cost, source, first, total); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO ai_messages(id,thread_id,role,body_text,trigger_run_id,created_at)
VALUES($1,$2,'assistant','safe answer',$3,$4)`, uuid.New(), run.ThreadID, run.ID, stamp.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	finishUnknown := func(run Run, status RunStatus, total int64) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE ai_runs SET status=$2,completed_at=$3,updated_at=$3,usage_source='unknown',
error_code=$4,total_ms=$5 WHERE id=$1`, run.ID, status, stamp.Add(time.Second), string(status)+"_safe", total); err != nil {
			t.Fatal(err)
		}
	}
	finishSuccess(makeRun("usage-upstream-0001"), "upstream", 10, 5, 99, 11, 21)
	finishSuccess(makeRun("usage-estimated-001"), "estimated", 20, 10, 101, 13, 23)
	finishUnknown(makeRun("usage-failed-000001"), RunFailed, 31)
	finishUnknown(makeRun("usage-cancelled-001"), RunCancelled, 41)

	service := NewPostgresAdminUsageService(pool)
	principal := Principal{User: auth.User{ID: adminID, Role: auth.RoleAdmin, Status: auth.StatusActive}}
	summary, err := service.UsageSummary(ctx, principal, UsageFilter{StudentID: fixture.student, ModelID: fixture.model, From: stamp, To: stamp, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Requests != 4 || summary.Succeeded != 2 || summary.Failed != 2 ||
		summary.InputTokens != 30 || summary.OutputTokens != 15 || summary.CostMicroUSD != 200 ||
		summary.UnknownUsage != 2 || summary.AverageFirstByteMS != 12 || summary.AverageTotalMS != 29 {
		t.Fatalf("summary=%+v", summary)
	}
	first, cursor, err := service.UsageRuns(ctx, principal, UsageFilter{StudentID: fixture.student, Limit: 2})
	if err != nil || len(first) != 2 || cursor.ID == uuid.Nil {
		t.Fatalf("first=%+v cursor=%+v err=%v", first, cursor, err)
	}
	second, _, err := service.UsageRuns(ctx, principal, UsageFilter{StudentID: fixture.student, Cursor: cursor, Limit: 2})
	if err != nil || len(second) != 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	seen := map[uuid.UUID]bool{}
	for _, run := range append(first, second...) {
		if seen[run.ID] {
			t.Fatalf("duplicate run %s", run.ID)
		}
		seen[run.ID] = true
		if run.ModelLabel != "runtime-model" || run.StudentDisplayName != "runtime-student" {
			t.Fatalf("metadata=%+v", run)
		}
	}
	succeeded, err := service.UsageSummary(ctx, principal, UsageFilter{Status: RunSucceeded, Limit: 20})
	if err != nil || succeeded.Requests != 2 || succeeded.CostMicroUSD != 200 || succeeded.UnknownUsage != 0 {
		t.Fatalf("succeeded=%+v err=%v", succeeded, err)
	}
	if _, _, err = service.UsageRuns(ctx, Principal{User: auth.User{ID: fixture.student, Role: auth.RoleStudent, Status: auth.StatusActive}}, UsageFilter{Limit: 20}); err != ErrForbidden {
		t.Fatalf("student usage error=%v", err)
	}
}
