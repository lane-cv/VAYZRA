package operations

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresDashboardReaderRejectsInvalidReceiverContextAndClock(t *testing.T) {
	now := dashboardPostgresClock()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	var nilReader *PostgresDashboardReader
	if _, err := nilReader.ReadStudentSummary(context.Background(), now); !errors.Is(err, errStoreClosed) {
		t.Fatalf("nil reader error=%v want errStoreClosed", err)
	}

	reader := newPostgresDashboardReaderDB(&dashboardDBStub{})
	calls := []struct {
		name string
		call func(context.Context, time.Time) error
	}{
		{"students", func(ctx context.Context, at time.Time) error {
			_, err := reader.ReadStudentSummary(ctx, at)
			return err
		}},
		{"questions", func(ctx context.Context, at time.Time) error {
			_, err := reader.ReadQuestionSummary(ctx, at)
			return err
		}},
		{"ai", func(ctx context.Context, at time.Time) error {
			_, err := reader.ReadAISummary(ctx, at)
			return err
		}},
		{"queues", func(ctx context.Context, at time.Time) error {
			_, err := reader.ReadQueueSummaries(ctx, at)
			return err
		}},
		{"backup", func(ctx context.Context, at time.Time) error {
			_, err := reader.ReadBackupSummary(ctx, at)
			return err
		}},
		{"alerts", func(ctx context.Context, at time.Time) error {
			_, err := reader.ReadAlertSummary(ctx, at)
			return err
		}},
		{"audit", func(ctx context.Context, at time.Time) error {
			_, err := reader.ReadRecentAudit(ctx, at, MaxRecentAudit)
			return err
		}},
	}
	for _, test := range calls {
		t.Run(test.name+"/nil_context", func(t *testing.T) {
			if err := test.call(nil, now); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v want ErrInvalid", err)
			}
		})
		t.Run(test.name+"/canceled_context", func(t *testing.T) {
			if err := test.call(canceled, now); !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v want context.Canceled", err)
			}
		})
		t.Run(test.name+"/invalid_clock", func(t *testing.T) {
			if err := test.call(context.Background(), time.Time{}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v want ErrInvalid", err)
			}
		})
	}
	if reader.db.(*dashboardDBStub).calls != 0 {
		t.Fatalf("invalid requests reached database calls=%d", reader.db.(*dashboardDBStub).calls)
	}

	closedPool, err := pgxpool.New(
		context.Background(),
		"postgres://dashboard:dashboard@127.0.0.1:1/dashboard?sslmode=disable",
	)
	if err != nil {
		t.Fatal(err)
	}
	closedPool.Close()
	if _, err := NewPostgresDashboardReader(closedPool).
		ReadStudentSummary(context.Background(), now); err == nil {
		t.Fatal("closed pool returned no error")
	}

	for _, limit := range []int{0, MaxRecentAudit + 1} {
		if _, err := reader.ReadRecentAudit(context.Background(), now, limit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("limit=%d error=%v want ErrInvalid", limit, err)
		}
	}
}

func TestPostgresDashboardReaderStudentsAreAggregateAndFailClosed(t *testing.T) {
	now := dashboardPostgresClock()
	db := &dashboardDBStub{
		row: dashboardRowStub{values: []any{int64(7), int64(2), int64(0), int64(0)}},
	}
	got, err := newPostgresDashboardReaderDB(db).
		ReadStudentSummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != DataStateHealthy || got.Active != 7 || got.Disabled != 2 ||
		got.ObservedAt == nil || !got.ObservedAt.Equal(now) {
		t.Fatalf("students=%+v", got)
	}
	assertDashboardQuery(t, db.lastQuery, db.lastArgs, now,
		"count(*) FILTER", "role='student'", "deleted_at IS NULL",
		"status NOT IN", "created_at>$1", "updated_at>$1")

	for _, values := range [][]any{
		{int64(-1), int64(2), int64(0), int64(0)},
		{maxDashboardInteger + 1, int64(0), int64(0), int64(0)},
		{int64(1), int64(0), int64(1), int64(0)},
		{int64(1), int64(0), int64(0), int64(1)},
	} {
		db.row = dashboardRowStub{values: values}
		if _, err := newPostgresDashboardReaderDB(db).
			ReadStudentSummary(context.Background(), now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("values=%v error=%v want ErrInvalid", values, err)
		}
	}
}

func TestPostgresDashboardReaderQuestionsUseTeacherWaitStates(t *testing.T) {
	now := dashboardPostgresClock()
	db := &dashboardDBStub{
		row: dashboardRowStub{values: []any{int64(3), int64(91), int64(0), int64(0)}},
	}
	got, err := newPostgresDashboardReaderDB(db).
		ReadQuestionSummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != DataStateHealthy || got.Waiting != 3 ||
		got.OldestWaitSeconds != 91 || got.ObservedAt == nil ||
		!got.ObservedAt.Equal(now) {
		t.Fatalf("questions=%+v", got)
	}
	assertDashboardQuery(t, db.lastQuery, db.lastArgs, now,
		"status IN ('pending','in_progress')",
		"min(q.last_message_at)",
		"JOIN users AS u",
		"u.role='student'",
		"u.deleted_at IS NULL")

	db.row = dashboardRowStub{values: []any{int64(0), int64(0), int64(0), int64(0)}}
	empty, err := newPostgresDashboardReaderDB(db).
		ReadQuestionSummary(context.Background(), now)
	if err != nil || empty.State != DataStateEmpty || empty.Waiting != 0 ||
		empty.OldestWaitSeconds != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}

	for _, values := range [][]any{
		{int64(0), int64(1), int64(0), int64(0)},
		{int64(1), int64(-1), int64(0), int64(0)},
		{int64(1), int64(1), int64(1), int64(0)},
		{int64(1), int64(1), int64(0), int64(1)},
	} {
		db.row = dashboardRowStub{values: values}
		if _, err := newPostgresDashboardReaderDB(db).
			ReadQuestionSummary(context.Background(), now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("values=%v error=%v want ErrInvalid", values, err)
		}
	}
}

func TestPostgresDashboardReaderAIDailyWindowAndSafeAggregates(t *testing.T) {
	now := dashboardPostgresClock()
	db := &dashboardDBStub{
		row: dashboardRowStub{values: []any{
			int64(5), int64(3), int64(4), int64(125), int64(900),
			int64(1234), int64(0), int64(0),
		}},
	}
	got, err := newPostgresDashboardReaderDB(db).
		ReadAISummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != DataStateHealthy || got.Requests != 5 ||
		got.SuccessRatePercent != 75 ||
		got.FirstByteLatencyMilliseconds != 125 ||
		got.TotalLatencyMilliseconds != 900 ||
		got.DailyCostMicroUSD != 1234 ||
		got.ObservedAt == nil || !got.ObservedAt.Equal(now) {
		t.Fatalf("ai=%+v", got)
	}
	if len(db.lastArgs) != 2 {
		t.Fatalf("AI args=%#v", db.lastArgs)
	}
	start, ok := db.lastArgs[1].(time.Time)
	if !ok || start != time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC) {
		t.Fatalf("AI day start=%#v", db.lastArgs[1])
	}
	assertDashboardQuery(t, db.lastQuery, db.lastArgs, now,
		"status='succeeded'", "status IN ('succeeded','failed','cancelled')",
		"avg(first_byte_ms)", "sum(cost_micro_usd)",
		"created_at >= $2", "created_at <= $1")
	if strings.Contains(db.lastQuery, "$1 + interval") {
		t.Fatalf("AI query hides far-future rows: %s", db.lastQuery)
	}

	db.row = dashboardRowStub{values: []any{
		int64(0), int64(0), int64(0), int64(0), int64(0),
		int64(0), int64(0), int64(0),
	}}
	empty, err := newPostgresDashboardReaderDB(db).
		ReadAISummary(context.Background(), now)
	if err != nil || empty.State != DataStateEmpty {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}

	db.row = dashboardRowStub{values: []any{
		int64(2), int64(0), int64(0), int64(0), int64(0),
		int64(0), int64(0), int64(0),
	}}
	activeOnly, err := newPostgresDashboardReaderDB(db).
		ReadAISummary(context.Background(), now)
	if err != nil || activeOnly.State != DataStateHealthy ||
		activeOnly.Requests != 2 || activeOnly.SuccessRatePercent != 0 {
		t.Fatalf("active-only=%+v err=%v", activeOnly, err)
	}

	for _, values := range [][]any{
		{int64(1), int64(2), int64(1), int64(1), int64(1), int64(1), int64(0), int64(0)},
		{int64(1), int64(1), int64(2), int64(1), int64(1), int64(1), int64(0), int64(0)},
		{int64(1), int64(1), int64(1), int64(-1), int64(1), int64(1), int64(0), int64(0)},
		{int64(1), int64(1), int64(1), int64(1), int64(1), int64(-1), int64(0), int64(0)},
		{int64(1), int64(1), int64(1), int64(1), int64(1), int64(1), int64(1), int64(0)},
		{int64(1), int64(1), int64(1), int64(1), int64(1), int64(1), int64(0), int64(1)},
		{maxDashboardInteger + 1, int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
	} {
		db.row = dashboardRowStub{values: values}
		if _, err := newPostgresDashboardReaderDB(db).
			ReadAISummary(context.Background(), now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("values=%v error=%v want ErrInvalid", values, err)
		}
	}
}

func TestPostgresDashboardReaderQueuesHaveFixedOrderAndStates(t *testing.T) {
	now := dashboardPostgresClock()
	db := &dashboardDBStub{
		rows: &dashboardRowsStub{values: [][]any{
			{"processing", int64(4), int64(2), int64(1), int64(0), int64(0), int64(0)},
			{"ai", int64(3), int64(1), int64(2), int64(1), int64(0), int64(0)},
			{"outbox", int64(5), int64(1), int64(1), int64(0), int64(0), int64(0)},
		}},
	}
	got, err := newPostgresDashboardReaderDB(db).
		ReadQueueSummaries(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 ||
		got[0].Queue != QueueProcessing || got[0].State != DataStateDegraded ||
		got[1].Queue != QueueAI || got[1].Expired != 1 ||
		got[2].Queue != QueueOutbox || got[2].Queued != 5 {
		t.Fatalf("queues=%+v", got)
	}
	for _, item := range got {
		if item.ObservedAt == nil || !item.ObservedAt.Equal(now) {
			t.Fatalf("queue observation=%+v", item)
		}
	}
	assertDashboardQuery(t, db.lastQuery, db.lastArgs, now,
		"UNION ALL", "'processing'", "'ai'", "'outbox'",
		"ORDER BY queue_order", "last_error_category")

	pollution := [][][]any{
		{
			{"processing", int64(-1), int64(0), int64(0), int64(0), int64(0), int64(0)},
			{"ai", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
			{"outbox", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
		},
		{
			{"processing", int64(0), int64(0), int64(0), int64(0), int64(1), int64(0)},
			{"ai", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
			{"outbox", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
		},
		{
			{"processing", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
			{"secret/student", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
			{"outbox", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
		},
		{
			{"processing", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
			{"processing", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
			{"outbox", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
		},
	}
	for _, values := range pollution {
		db.rows = &dashboardRowsStub{values: values}
		if _, err := newPostgresDashboardReaderDB(db).
			ReadQueueSummaries(context.Background(), now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("values=%v error=%v want ErrInvalid", values, err)
		}
	}
}

func TestPostgresDashboardReaderBackupMapsLocalRemoteAndRestoreEvidence(t *testing.T) {
	now := dashboardPostgresClock()
	localAt := now.Add(-2 * time.Hour)
	remoteAt := now.Add(-3 * time.Hour)
	restoreAt := now.Add(-time.Hour)
	db := &dashboardDBStub{
		row: dashboardRowStub{values: []any{
			stringPointer("succeeded"), &localAt,
			stringPointer("degraded"), &remoteAt,
			stringPointer("succeeded"), &restoreAt, int64(73),
			int64(0), int64(0),
		}},
	}
	got, err := newPostgresDashboardReaderDB(db).
		ReadBackupSummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != DataStateDegraded || got.ObservedAt == nil ||
		got.Local.State != RecoveryStateSucceeded ||
		got.Remote.State != RecoveryStateDegraded ||
		got.Restore.State != RecoveryStateSucceeded ||
		got.Restore.RTOSeconds != 73 ||
		got.Local.CompletedAt == nil || !got.Local.CompletedAt.Equal(localAt) ||
		got.Remote.CompletedAt == nil || !got.Remote.CompletedAt.Equal(remoteAt) ||
		got.Restore.CompletedAt == nil || !got.Restore.CompletedAt.Equal(restoreAt) {
		t.Fatalf("backup=%+v", got)
	}
	assertDashboardQuery(t, db.lastQuery, db.lastArgs, now,
		"LEFT JOIN LATERAL", "backup_runs",
		"restore_verifications", "ORDER BY finished_at DESC,id DESC",
		"state IN ('succeeded','degraded','failed')",
		"state IN ('succeeded','failed')",
		"local_snapshot_id", "remote_snapshot_id",
		"state='degraded'")

	db.row = dashboardRowStub{values: []any{
		stringPointer("succeeded"), &localAt,
		(*string)(nil), (*time.Time)(nil),
		(*string)(nil), (*time.Time)(nil), int64(0),
		int64(0), int64(0),
	}}
	localOnly, err := newPostgresDashboardReaderDB(db).
		ReadBackupSummary(context.Background(), now)
	if err != nil ||
		localOnly.Local.State != RecoveryStateSucceeded ||
		localOnly.Remote.State != RecoveryStateEmpty {
		t.Fatalf("local-only=%+v err=%v", localOnly, err)
	}

	db.row = dashboardRowStub{values: []any{
		(*string)(nil), (*time.Time)(nil),
		(*string)(nil), (*time.Time)(nil),
		(*string)(nil), (*time.Time)(nil), int64(0),
		int64(0), int64(0),
	}}
	empty, err := newPostgresDashboardReaderDB(db).
		ReadBackupSummary(context.Background(), now)
	if err != nil || empty.State != DataStateEmpty ||
		empty.Local.State != RecoveryStateEmpty ||
		empty.Remote.State != RecoveryStateEmpty ||
		empty.Restore.State != RecoveryStateEmpty {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}

	for _, values := range [][]any{
		{stringPointer("unknown"), &localAt, (*string)(nil), (*time.Time)(nil), (*string)(nil), (*time.Time)(nil), int64(0), int64(0), int64(0)},
		{stringPointer("degraded"), &localAt, (*string)(nil), (*time.Time)(nil), (*string)(nil), (*time.Time)(nil), int64(0), int64(0), int64(0)},
		{stringPointer("succeeded"), &localAt, (*string)(nil), (*time.Time)(nil), stringPointer("degraded"), &restoreAt, int64(1), int64(0), int64(0)},
		{stringPointer("succeeded"), &now, (*string)(nil), (*time.Time)(nil), stringPointer("failed"), &restoreAt, int64(1), int64(0), int64(0)},
		{stringPointer("succeeded"), &localAt, (*string)(nil), (*time.Time)(nil), stringPointer("succeeded"), &restoreAt, int64(-1), int64(0), int64(0)},
		{stringPointer("succeeded"), &localAt, (*string)(nil), (*time.Time)(nil), (*string)(nil), (*time.Time)(nil), int64(0), int64(1), int64(0)},
		{stringPointer("succeeded"), &localAt, (*string)(nil), (*time.Time)(nil), (*string)(nil), (*time.Time)(nil), int64(0), int64(0), int64(1)},
	} {
		db.row = dashboardRowStub{values: values}
		if _, err := newPostgresDashboardReaderDB(db).
			ReadBackupSummary(context.Background(), now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("values=%v error=%v want ErrInvalid", values, err)
		}
	}
}

func TestPostgresDashboardReaderAlertsCountOnlyOpenSafeSeverities(t *testing.T) {
	now := dashboardPostgresClock()
	db := &dashboardDBStub{
		row: dashboardRowStub{values: []any{int64(4), int64(2), int64(0), int64(0)}},
	}
	got, err := newPostgresDashboardReaderDB(db).
		ReadAlertSummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != DataStateHealthy || got.OpenWarning != 4 ||
		got.OpenCritical != 2 || got.ObservedAt == nil ||
		!got.ObservedAt.Equal(now) {
		t.Fatalf("alerts=%+v", got)
	}
	assertDashboardQuery(t, db.lastQuery, db.lastArgs, now,
		"state IN ('open','acknowledged')",
		"severity='warning'", "severity='critical'",
		"last_observed_at>$1")

	for _, values := range [][]any{
		{int64(-1), int64(0), int64(0), int64(0)},
		{int64(0), int64(0), int64(1), int64(0)},
		{int64(0), int64(0), int64(0), int64(1)},
	} {
		db.row = dashboardRowStub{values: values}
		if _, err := newPostgresDashboardReaderDB(db).
			ReadAlertSummary(context.Background(), now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("values=%v error=%v want ErrInvalid", values, err)
		}
	}
}

func TestPostgresDashboardReaderRecentAuditReturnsOnlyBroadSafeFields(t *testing.T) {
	now := dashboardPostgresClock()
	db := &dashboardDBStub{
		rows: &dashboardRowsStub{values: [][]any{
			{"operations", "rejected", now.Add(-time.Minute), false},
			{"teaching", "succeeded", now.Add(-2 * time.Minute), false},
			{"files", "denied", now.Add(-3 * time.Minute), false},
		}},
	}
	got, err := newPostgresDashboardReaderDB(db).
		ReadRecentAudit(context.Background(), now, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 ||
		got[0].Category != AuditCategoryOperations ||
		got[0].Outcome != AuditOutcomeRejected ||
		got[2].Category != AuditCategoryFiles ||
		got[2].Outcome != AuditOutcomeDenied {
		t.Fatalf("audit=%+v", got)
	}
	if len(db.lastArgs) != 3 || db.lastArgs[2] != 3 {
		t.Fatalf("audit args=%#v", db.lastArgs)
	}
	assertDashboardQuery(t, db.lastQuery, db.lastArgs, now,
		"CASE", "metadata->>'outcome'",
		"ORDER BY occurred_at DESC,id DESC", "LIMIT $3")
	for _, forbidden := range []string{
		"actor_user_id", "target_id", "request_id", " ip", "username",
		"display_name", "path", "object_key", "prompt", "url", "trace",
	} {
		if strings.Contains(strings.ToLower(db.lastQuery), forbidden) {
			t.Fatalf("audit query exposes forbidden field %q: %s", forbidden, db.lastQuery)
		}
	}

	for _, values := range [][][]any{
		{{"private/student", "succeeded", now.Add(-time.Minute), false}},
		{{"operations", "private", now.Add(-time.Minute), false}},
		{{"operations", "succeeded", now.Add(365 * 24 * time.Hour), true}},
		{
			{"operations", "succeeded", now.Add(-2 * time.Minute), false},
			{"operations", "succeeded", now.Add(-time.Minute), false},
		},
	} {
		db.rows = &dashboardRowsStub{values: values}
		if _, err := newPostgresDashboardReaderDB(db).
			ReadRecentAudit(context.Background(), now, 1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("values=%v error=%v want ErrInvalid", values, err)
		}
	}
}

func TestPostgresDashboardReaderPropagatesDatabaseAndIterationFailures(t *testing.T) {
	now := dashboardPostgresClock()
	databaseErr := errors.New("database closed")
	rowDB := &dashboardDBStub{row: dashboardRowStub{err: databaseErr}}
	if _, err := newPostgresDashboardReaderDB(rowDB).
		ReadStudentSummary(context.Background(), now); !errors.Is(err, databaseErr) {
		t.Fatalf("row error=%v", err)
	}

	rowsDB := &dashboardDBStub{
		rows: &dashboardRowsStub{
			values: [][]any{
				{"operations", "succeeded", now.Add(-time.Minute), false},
			},
			err: databaseErr,
		},
	}
	if _, err := newPostgresDashboardReaderDB(rowsDB).
		ReadRecentAudit(context.Background(), now, 1); !errors.Is(err, databaseErr) {
		t.Fatalf("rows error=%v", err)
	}
	if !rowsDB.rows.(*dashboardRowsStub).closed {
		t.Fatal("audit rows were not closed")
	}

	queryDB := &dashboardDBStub{queryErr: databaseErr}
	if _, err := newPostgresDashboardReaderDB(queryDB).
		ReadQueueSummaries(context.Background(), now); !errors.Is(err, databaseErr) {
		t.Fatalf("query error=%v", err)
	}
}

func TestPostgresDashboardReaderRejectsNonFiniteDerivedRate(t *testing.T) {
	if validDashboardRate(math.NaN()) || validDashboardRate(math.Inf(1)) {
		t.Fatal("dashboard rate validator accepted non-finite values")
	}
}

func TestPostgresDashboardReaderSQLSmoke(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	for _, statement := range []string{
		"TRUNCATE users CASCADE",
		"TRUNCATE operational_samples",
		"TRUNCATE operational_alerts CASCADE",
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	now := dashboardPostgresClock()
	reader := newPostgresDashboardReaderDB(dashboardPGXTxDB{tx: tx})
	students, err := reader.ReadStudentSummary(ctx, now)
	if err != nil || students.State != DataStateEmpty {
		t.Fatalf("students=%+v err=%v", students, err)
	}
	questions, err := reader.ReadQuestionSummary(ctx, now)
	if err != nil || questions.State != DataStateEmpty {
		t.Fatalf("questions=%+v err=%v", questions, err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO users(
  id,username,display_name,role,status,password_hash,must_change_password,
  created_at,updated_at
) VALUES(
  '10000000-0000-4000-8000-000000000001',
  'dashboard-student','Dashboard Student','student','active','hash',false,
  $1,$1
)`, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO qa_threads(
  id,student_id,title,status,last_message_at,created_at,updated_at
) VALUES(
  '20000000-0000-4000-8000-000000000001',
  '10000000-0000-4000-8000-000000000001',
  'safe aggregate fixture','pending',$1,$1,$1
)`, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	questions, err = reader.ReadQuestionSummary(ctx, now)
	if err != nil || questions.Waiting != 1 {
		t.Fatalf("active student questions=%+v err=%v", questions, err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE users
SET deleted_at=$2,updated_at=$2
WHERE id=$1`,
		"10000000-0000-4000-8000-000000000001", now,
	); err != nil {
		t.Fatal(err)
	}
	questions, err = reader.ReadQuestionSummary(ctx, now)
	if err != nil || questions.State != DataStateEmpty || questions.Waiting != 0 {
		t.Fatalf("soft-deleted student questions=%+v err=%v", questions, err)
	}
	ai, err := reader.ReadAISummary(ctx, now)
	if err != nil || ai.State != DataStateEmpty {
		t.Fatalf("ai=%+v err=%v", ai, err)
	}
	queues, err := reader.ReadQueueSummaries(ctx, now)
	if err != nil || len(queues) != len(DashboardQueueOrder()) {
		t.Fatalf("queues=%+v err=%v", queues, err)
	}
	for _, queue := range queues {
		if queue.State != DataStateHealthy ||
			queue.Queued != 0 || queue.Streaming != 0 ||
			queue.Failed != 0 || queue.Expired != 0 {
			t.Fatalf("queue=%+v", queue)
		}
	}
	backup, err := reader.ReadBackupSummary(ctx, now)
	if err != nil || backup.State != DataStateEmpty {
		t.Fatalf("backup=%+v err=%v", backup, err)
	}
	alerts, err := reader.ReadAlertSummary(ctx, now)
	if err != nil || alerts.State != DataStateHealthy ||
		alerts.OpenWarning != 0 || alerts.OpenCritical != 0 {
		t.Fatalf("alerts=%+v err=%v", alerts, err)
	}
	recent, err := reader.ReadRecentAudit(ctx, now, MaxRecentAudit)
	if err != nil || len(recent) != 0 {
		t.Fatalf("recent=%+v err=%v", recent, err)
	}
}

func dashboardPostgresClock() time.Time {
	return time.Date(2026, 7, 30, 5, 6, 7, 0, time.UTC)
}

func assertDashboardQuery(
	t *testing.T,
	query string,
	args []any,
	now time.Time,
	fragments ...string,
) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("query missing %q: %s", fragment, query)
		}
	}
	if len(args) == 0 || args[0] != now {
		t.Fatalf("query does not use supplied UTC clock first: %#v", args)
	}
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}

type dashboardDBStub struct {
	row       dashboardRow
	rows      dashboardRows
	queryErr  error
	lastQuery string
	lastArgs  []any
	calls     int
}

type dashboardPGXTxDB struct {
	tx pgx.Tx
}

func (database dashboardPGXTxDB) QueryRow(
	ctx context.Context,
	query string,
	args ...any,
) dashboardRow {
	return database.tx.QueryRow(ctx, query, args...)
}

func (database dashboardPGXTxDB) Query(
	ctx context.Context,
	query string,
	args ...any,
) (dashboardRows, error) {
	return database.tx.Query(ctx, query, args...)
}

func (db *dashboardDBStub) QueryRow(
	_ context.Context,
	query string,
	args ...any,
) dashboardRow {
	db.calls++
	db.lastQuery = query
	db.lastArgs = append([]any(nil), args...)
	if db.row == nil {
		return dashboardRowStub{err: errors.New("unexpected QueryRow")}
	}
	return db.row
}

func (db *dashboardDBStub) Query(
	_ context.Context,
	query string,
	args ...any,
) (dashboardRows, error) {
	db.calls++
	db.lastQuery = query
	db.lastArgs = append([]any(nil), args...)
	if db.queryErr != nil {
		return nil, db.queryErr
	}
	if db.rows == nil {
		return nil, errors.New("unexpected Query")
	}
	return db.rows, nil
}

type dashboardRowStub struct {
	values []any
	err    error
}

func (row dashboardRowStub) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	return assignDashboardValues(row.values, destinations)
}

type dashboardRowsStub struct {
	values [][]any
	index  int
	err    error
	closed bool
}

func (rows *dashboardRowsStub) Next() bool {
	if rows.closed || rows.index >= len(rows.values) {
		return false
	}
	rows.index++
	return true
}

func (rows *dashboardRowsStub) Scan(destinations ...any) error {
	if rows.index < 1 || rows.index > len(rows.values) {
		return errors.New("Scan called without current row")
	}
	return assignDashboardValues(rows.values[rows.index-1], destinations)
}

func (rows *dashboardRowsStub) Err() error {
	if rows.index >= len(rows.values) {
		return rows.err
	}
	return nil
}

func (rows *dashboardRowsStub) Close() {
	rows.closed = true
}

func assignDashboardValues(values []any, destinations []any) error {
	if len(values) != len(destinations) {
		return errors.New("scan destination count mismatch")
	}
	for index := range values {
		destination := reflect.ValueOf(destinations[index])
		if destination.Kind() != reflect.Pointer || destination.IsNil() {
			return errors.New("invalid scan destination")
		}
		value := values[index]
		if value == nil {
			destination.Elem().Set(reflect.Zero(destination.Elem().Type()))
			continue
		}
		source := reflect.ValueOf(value)
		if source.Type().AssignableTo(destination.Elem().Type()) {
			destination.Elem().Set(source)
			continue
		}
		return errors.New("scan type mismatch")
	}
	return nil
}
