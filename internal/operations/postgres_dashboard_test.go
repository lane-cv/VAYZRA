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
			int64(1234), int64(0), int64(0), int64(0),
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
	if strings.Contains(db.lastQuery, "FROM ai_runs AS lifecycle") {
		t.Fatalf("AI query scans lifecycle outside daily source: %s", db.lastQuery)
	}

	db.row = dashboardRowStub{values: []any{
		int64(0), int64(0), int64(0), int64(0), int64(0),
		int64(0), int64(0), int64(0), int64(0),
	}}
	empty, err := newPostgresDashboardReaderDB(db).
		ReadAISummary(context.Background(), now)
	if err != nil || empty.State != DataStateEmpty {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}

	db.row = dashboardRowStub{values: []any{
		int64(2), int64(0), int64(0), int64(0), int64(0),
		int64(0), int64(0), int64(0), int64(0),
	}}
	activeOnly, err := newPostgresDashboardReaderDB(db).
		ReadAISummary(context.Background(), now)
	if err != nil || activeOnly.State != DataStateHealthy ||
		activeOnly.Requests != 2 || activeOnly.SuccessRatePercent != 0 {
		t.Fatalf("active-only=%+v err=%v", activeOnly, err)
	}

	for _, values := range [][]any{
		{int64(1), int64(2), int64(1), int64(1), int64(1), int64(1), int64(0), int64(0), int64(0)},
		{int64(1), int64(1), int64(2), int64(1), int64(1), int64(1), int64(0), int64(0), int64(0)},
		{int64(1), int64(1), int64(1), int64(-1), int64(1), int64(1), int64(0), int64(0), int64(0)},
		{int64(1), int64(1), int64(1), int64(1), int64(1), int64(-1), int64(0), int64(0), int64(0)},
		{int64(1), int64(0), int64(0), int64(1), int64(1), int64(1), int64(1), int64(0), int64(0)},
		{int64(1), int64(1), int64(1), int64(1), int64(1), int64(1), int64(0), int64(1), int64(0)},
		{int64(1), int64(1), int64(1), int64(1), int64(1), int64(1), int64(0), int64(0), int64(1)},
		{maxDashboardInteger + 1, int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
	} {
		db.row = dashboardRowStub{values: values}
		if _, err := newPostgresDashboardReaderDB(db).
			ReadAISummary(context.Background(), now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("values=%v error=%v want ErrInvalid", values, err)
		}
	}
}

func TestPostgresDashboardReaderAIUnknownUsageIsDegraded(t *testing.T) {
	now := dashboardPostgresClock()
	db := &dashboardDBStub{
		row: dashboardRowStub{values: []any{
			int64(5), int64(3), int64(4), int64(125), int64(900),
			int64(1234), int64(1), int64(0), int64(0),
		}},
	}

	got, err := newPostgresDashboardReaderDB(db).
		ReadAISummary(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != DataStateDegraded ||
		got.Requests != 5 ||
		got.SuccessRatePercent != 75 ||
		got.FirstByteLatencyMilliseconds != 125 ||
		got.TotalLatencyMilliseconds != 900 ||
		got.DailyCostMicroUSD != 1234 {
		t.Fatalf("ai=%+v", got)
	}
}

func TestPostgresDashboardReaderAIChecksLifecycleFuturePollution(t *testing.T) {
	now := dashboardPostgresClock()
	db := &dashboardDBStub{
		row: dashboardRowStub{values: []any{
			int64(1), int64(1), int64(1), int64(1), int64(1),
			int64(1), int64(0), int64(0), int64(0),
		}},
	}

	if _, err := newPostgresDashboardReaderDB(db).
		ReadAISummary(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	assertDashboardQuery(
		t,
		db.lastQuery,
		db.lastArgs,
		now,
		"started_at > $1",
		"completed_at > $1",
	)
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

func TestPostgresDashboardReaderQueuesMatchClaimLeaseSemantics(t *testing.T) {
	now := dashboardPostgresClock()
	db := &dashboardDBStub{
		rows: &dashboardRowsStub{values: [][]any{
			{"processing", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
			{"ai", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
			{"outbox", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
		}},
	}

	if _, err := newPostgresDashboardReaderDB(db).
		ReadQueueSummaries(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(db.lastArgs) != 3 || db.lastArgs[2] != 10 {
		t.Fatalf("queue args=%#v want shared outbox max attempts", db.lastArgs)
	}
	assertDashboardQuery(
		t,
		db.lastQuery,
		db.lastArgs,
		now,
		"available_at <= $1",
		"state='running' AND lease_until < $1",
		"state='running' AND lease_until >= $1",
		"status='streaming' AND lease_expires_at >= $1",
		"status='streaming' AND lease_expires_at < $1",
		"next_attempt_at <= $1",
		"lease_until > $1",
		"attempts<$3",
		"attempts>=$3",
		"FROM file_processing_jobs\n  WHERE state IN ('queued','running')",
		"FROM ai_runs\n  WHERE status IN ('queued','streaming')",
		"FROM outbox_events\n  WHERE published_at IS NULL",
	)
	processingSource := dashboardQuerySource(
		t,
		db.lastQuery,
		"FROM file_processing_jobs",
		"\n\n  UNION ALL",
	)
	if strings.Contains(processingSource, "OR created_at>$1") {
		t.Fatalf("processing source scans irrelevant historical rows: %s", processingSource)
	}
	aiSource := dashboardQuerySource(
		t,
		db.lastQuery,
		"FROM ai_runs",
		"\n\n  UNION ALL",
	)
	if strings.Contains(aiSource, "OR created_at>$1") ||
		strings.Contains(aiSource, "OR started_at>$1") {
		t.Fatalf("AI queue source scans irrelevant historical rows: %s", aiSource)
	}
	outboxSource := dashboardQuerySource(
		t,
		db.lastQuery,
		"FROM outbox_events",
		"\n)",
	)
	if strings.Contains(outboxSource, "OR created_at>$1") ||
		strings.Contains(outboxSource, "OR published_at>$1") {
		t.Fatalf("outbox source scans irrelevant historical rows: %s", outboxSource)
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
	if len(db.lastArgs) != 2 || db.lastArgs[1] != 3 {
		t.Fatalf("audit args=%#v", db.lastArgs)
	}
	assertDashboardQuery(t, db.lastQuery, db.lastArgs, now,
		"CASE", "metadata->>'outcome'",
		"ORDER BY occurred_at DESC,id DESC", "LIMIT $2")
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

func TestPostgresDashboardReaderRecentAuditHasNoArbitraryAgeCutoff(t *testing.T) {
	now := dashboardPostgresClock()
	old := now.Add(-180 * 24 * time.Hour)
	db := &dashboardDBStub{
		rows: &dashboardRowsStub{values: [][]any{
			{"operations", "succeeded", old, false},
		}},
	}

	got, err := newPostgresDashboardReaderDB(db).
		ReadRecentAudit(context.Background(), now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].OccurredAt.Equal(old) {
		t.Fatalf("recent=%+v", got)
	}
	if len(db.lastArgs) != 2 || db.lastArgs[0] != now || db.lastArgs[1] != 1 {
		t.Fatalf("audit args=%#v", db.lastArgs)
	}
	if strings.Contains(db.lastQuery, "occurred_at >=") {
		t.Fatalf("audit query contains arbitrary age cutoff: %s", db.lastQuery)
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
		"TRUNCATE outbox_events",
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

	seedDashboardPostgresFixture(t, ctx, tx, now)

	students, err = reader.ReadStudentSummary(ctx, now)
	if err != nil || students.State != DataStateHealthy ||
		students.Active != 4 || students.Disabled != 0 {
		t.Fatalf("fixture students=%+v err=%v", students, err)
	}
	ai, err = reader.ReadAISummary(ctx, now)
	if err != nil || ai.State != DataStateDegraded ||
		ai.Requests != 5 || ai.SuccessRatePercent != 50 ||
		ai.FirstByteLatencyMilliseconds != 100 ||
		ai.TotalLatencyMilliseconds != 500 ||
		ai.DailyCostMicroUSD != 300 {
		t.Fatalf("fixture ai=%+v err=%v", ai, err)
	}
	queues, err = reader.ReadQueueSummaries(ctx, now)
	if err != nil || len(queues) != 3 {
		t.Fatalf("fixture queues=%+v err=%v", queues, err)
	}
	if queues[0].Queue != QueueProcessing ||
		queues[0].Queued != 2 || queues[0].Streaming != 1 ||
		queues[0].Failed != 1 || queues[0].Expired != 0 ||
		queues[0].State != DataStateDegraded {
		t.Fatalf("fixture processing queue=%+v", queues[0])
	}
	if queues[1].Queue != QueueAI ||
		queues[1].Queued != 1 || queues[1].Streaming != 1 ||
		queues[1].Failed != 1 || queues[1].Expired != 1 ||
		queues[1].State != DataStateDegraded {
		t.Fatalf("fixture AI queue=%+v", queues[1])
	}
	if queues[2].Queue != QueueOutbox ||
		queues[2].Queued != 4 || queues[2].Streaming != 1 ||
		queues[2].Failed != 2 || queues[2].Expired != 0 ||
		queues[2].State != DataStateDegraded {
		t.Fatalf("fixture outbox queue=%+v", queues[2])
	}
	backup, err = reader.ReadBackupSummary(ctx, now)
	if err != nil || backup.State != DataStateHealthy ||
		backup.Local.State != RecoveryStateSucceeded ||
		backup.Remote.State != RecoveryStateSucceeded ||
		backup.Restore.State != RecoveryStateSucceeded ||
		backup.Restore.RTOSeconds != 73 {
		t.Fatalf("fixture backup=%+v err=%v", backup, err)
	}
	alerts, err = reader.ReadAlertSummary(ctx, now)
	if err != nil || alerts.State != DataStateHealthy ||
		alerts.OpenWarning != 1 || alerts.OpenCritical != 1 {
		t.Fatalf("fixture alerts=%+v err=%v", alerts, err)
	}
	recent, err = reader.ReadRecentAudit(ctx, now, MaxRecentAudit)
	if err != nil || len(recent) != 3 ||
		recent[0].Category != AuditCategoryAI ||
		recent[0].Outcome != AuditOutcomeFailed ||
		recent[1].Category != AuditCategoryFiles ||
		recent[1].Outcome != AuditOutcomeDenied ||
		recent[2].Category != AuditCategoryOperations ||
		recent[2].Outcome != AuditOutcomeSucceeded ||
		!recent[2].OccurredAt.Equal(now.Add(-180*24*time.Hour)) {
		t.Fatalf("fixture recent=%+v err=%v", recent, err)
	}

	insertDashboardFutureAIRun(t, ctx, tx, now)
	if _, err := reader.ReadAISummary(ctx, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future AI lifecycle error=%v want ErrInvalid", err)
	}
}

func TestPostgresDashboardReaderNaturalPlansStayBounded(t *testing.T) {
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
	now := dashboardPostgresClock()
	seedDashboardPlannerHistory(t, ctx, tx, now)

	aiDB := &dashboardDBStub{row: dashboardRowStub{values: []any{
		int64(0), int64(0), int64(0), int64(0), int64(0),
		int64(0), int64(0), int64(0), int64(0),
	}}}
	if _, err := newPostgresDashboardReaderDB(aiDB).
		ReadAISummary(ctx, now); err != nil {
		t.Fatal(err)
	}
	queueDB := &dashboardDBStub{rows: &dashboardRowsStub{values: [][]any{
		{"processing", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
		{"ai", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
		{"outbox", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)},
	}}}
	if _, err := newPostgresDashboardReaderDB(queueDB).
		ReadQueueSummaries(ctx, now); err != nil {
		t.Fatal(err)
	}
	backupDB := &dashboardDBStub{row: dashboardRowStub{values: []any{
		nil, nil, nil, nil, nil, nil, int64(0), int64(0), int64(0),
	}}}
	if _, err := newPostgresDashboardReaderDB(backupDB).
		ReadBackupSummary(ctx, now); err != nil {
		t.Fatal(err)
	}
	auditDB := &dashboardDBStub{rows: &dashboardRowsStub{}}
	if _, err := newPostgresDashboardReaderDB(auditDB).
		ReadRecentAudit(ctx, now, MaxRecentAudit); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		query       string
		args        []any
		indexes     []string
		largeTables []string
	}{
		{
			name:        "AI daily",
			query:       aiDB.lastQuery,
			args:        aiDB.lastArgs,
			indexes:     []string{"ai_runs_dashboard_daily_idx"},
			largeTables: []string{"ai_runs"},
		},
		{
			name:  "queues",
			query: queueDB.lastQuery,
			args:  queueDB.lastArgs,
			indexes: []string{
				"file_processing_jobs_claim_idx",
				"file_processing_jobs_dashboard_failed_idx",
				"ai_runs_one_active_student_idx",
				"ai_runs_dashboard_failed_idx",
				"outbox_events_dashboard_pending_idx",
				"outbox_events_dashboard_terminal_failure_idx",
			},
			largeTables: []string{"file_processing_jobs", "ai_runs", "outbox_events"},
		},
		{
			name:  "backup",
			query: backupDB.lastQuery,
			args:  backupDB.lastArgs,
			indexes: []string{
				"backup_runs_dashboard_finished_idx",
				"backup_runs_dashboard_remote_finished_idx",
				"restore_verifications_dashboard_finished_idx",
				"restore_verifications_dashboard_unknown_idx",
			},
			largeTables: []string{"backup_runs", "restore_verifications"},
		},
		{
			name:        "audit",
			query:       auditDB.lastQuery,
			args:        auditDB.lastArgs,
			indexes:     []string{"audit_logs_dashboard_latest_idx"},
			largeTables: []string{"audit_logs"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := dashboardExplainPlan(t, ctx, tx, test.query, test.args...)
			t.Logf("history_rows=50000 plan:\n%s", plan)
			for _, index := range test.indexes {
				if !strings.Contains(plan, index) {
					t.Fatalf("natural plan missing %s:\n%s", index, plan)
				}
			}
			for _, table := range test.largeTables {
				if strings.Contains(plan, "Seq Scan on "+table) {
					t.Fatalf("natural plan scans historical %s:\n%s", table, plan)
				}
			}
		})
	}
}

func seedDashboardPlannerHistory(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	now time.Time,
) {
	t.Helper()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`TRUNCATE audit_logs,ai_runs,file_processing_jobs,outbox_events,
		  restore_verifications,backup_runs CASCADE`,
		`INSERT INTO audit_logs(
		   action,target_type,target_id,metadata,request_id,occurred_at
		 )
		 SELECT 'operations.history','operations','dashboard','{}',
		   'dashboard-plan-'||value,
		   $1::timestamptz-interval '90 days'-(value*interval '1 second')
		 FROM generate_series(1,50000) AS value`,
		`INSERT INTO ai_runs(
		   id,thread_id,student_id,trigger_message_id,attempt_no,idempotency_key,
		   status,provider_id,provider_key_version,provider_base_url,protocol_mode,
		   model_id,upstream_model_id,modality,context_window_tokens,
		   max_output_tokens,image_quota_tokens,
		   input_price_micro_usd_per_million_tokens,
		   output_price_micro_usd_per_million_tokens,
		   prompt_id,prompt_subject,prompt_version,prompt_sha256,
		   connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,
		   total_timeout_ms,reserved_request_count,reserved_token_count,
		   quota_day_key,quota_month_key,estimator_version,
		   input_tokens,output_tokens,cost_micro_usd,usage_source,
		   first_byte_ms,total_ms,created_at,updated_at,started_at,completed_at
		 )
		 SELECT
		   md5('dashboard-ai-id-'||value)::uuid,
		   md5('dashboard-ai-thread-'||value)::uuid,
		   '61000000-0000-4000-8000-000000000001'::uuid,
		   md5('dashboard-ai-trigger-'||value)::uuid,
		   1,'dashboard-plan-'||lpad(value::text,10,'0'),
		   'succeeded','62000000-0000-4000-8000-000000000001'::uuid,1,
		   'https://dashboard.invalid/v1','chat_completions',
		   '63000000-0000-4000-8000-000000000001'::uuid,
		   'dashboard-model','text',8192,1024,1024,0,0,
		   '64000000-0000-4000-8000-000000000001'::uuid,
		   'math',1,repeat('a',64),1000,30000,30000,120000,
		   1,1024,'2026-07-30','2026-07',1,
		   1,1,0,'upstream',1,1,
		   $1::timestamptz-interval '90 days'-(value*interval '1 second'),
		   $1::timestamptz-interval '90 days'-(value*interval '1 second'),
		   $1::timestamptz-interval '90 days'-(value*interval '1 second'),
		   $1::timestamptz-interval '90 days'-(value*interval '1 second')
		 FROM generate_series(1,50000) AS value`,
		`INSERT INTO file_processing_jobs(
		   id,file_version_id,kind,state,attempts,available_at,created_at,updated_at
		 )
		 SELECT md5('dashboard-job-id-'||value)::uuid,
		   md5('dashboard-version-id-'||value)::uuid,
		   'process_file','completed',1,
		   $1::timestamptz-interval '90 days',
		   $1::timestamptz-interval '90 days'-(value*interval '1 second'),
		   $1::timestamptz-interval '90 days'-(value*interval '1 second')
		 FROM generate_series(1,50000) AS value`,
		`INSERT INTO outbox_events(
		   id,kind,payload,created_at,published_at,attempts,next_attempt_at
		 )
		 SELECT md5('dashboard-outbox-id-'||value)::uuid,
		   'dashboard.history','{}',
		   $1::timestamptz-interval '90 days'-(value*interval '1 second'),
		   $1::timestamptz-interval '90 days'-(value*interval '1 second'),
		   1,$1::timestamptz-interval '90 days'
		 FROM generate_series(1,50000) AS value`,
		`INSERT INTO backup_runs(
		   id,idempotency_key,trigger_kind,state,requested_at,started_at,finished_at
		 )
		 SELECT md5('dashboard-backup-id-'||value)::uuid,
		   'dashboard-plan-'||lpad(value::text,10,'0'),
		   'manual','failed',
		   $1::timestamptz-interval '90 days'-(value*interval '1 second'),
		   $1::timestamptz-interval '90 days'-(value*interval '1 second'),
		   $1::timestamptz-interval '90 days'-(value*interval '1 second')
		 FROM generate_series(1,50000) AS value`,
		`INSERT INTO restore_verifications(
		   id,backup_run_id,state,started_at,finished_at
		 )
		 SELECT md5('dashboard-restore-id-'||value)::uuid,
		   md5('dashboard-backup-id-'||value)::uuid,
		   'failed',
		   $1::timestamptz-interval '90 days'-(value*interval '1 second'),
		   $1::timestamptz-interval '90 days'-(value*interval '1 second')
		 FROM generate_series(1,50000) AS value`,
		`ANALYZE audit_logs`,
		`ANALYZE ai_runs`,
		`ANALYZE file_processing_jobs`,
		`ANALYZE outbox_events`,
		`ANALYZE backup_runs`,
		`ANALYZE restore_verifications`,
	} {
		var err error
		if strings.Contains(statement, "$1") {
			_, err = tx.Exec(ctx, statement, now)
		} else {
			_, err = tx.Exec(ctx, statement)
		}
		if err != nil {
			t.Fatalf("planner fixture: %v", err)
		}
	}
}

func dashboardExplainPlan(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	query string,
	args ...any,
) string {
	t.Helper()
	rows, err := tx.Query(
		ctx,
		"EXPLAIN (ANALYZE,BUFFERS,COSTS OFF,TIMING OFF,SUMMARY OFF) "+query,
		args...,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan.String()
}

func seedDashboardPostgresFixture(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	now time.Time,
) {
	t.Helper()
	for index, statement := range []struct {
		query string
		args  []any
	}{
		{
			`INSERT INTO users(
  id,username,display_name,role,status,password_hash,must_change_password,
  created_at,updated_at
) VALUES
  ('30000000-0000-4000-8000-000000000001','dashboard-admin','Dashboard Admin','admin','active','hash',false,$1,$1),
  ('30000000-0000-4000-8000-000000000101','dashboard-ai-1','Dashboard AI 1','student','active','hash',false,$1,$1),
  ('30000000-0000-4000-8000-000000000102','dashboard-ai-2','Dashboard AI 2','student','active','hash',false,$1,$1),
  ('30000000-0000-4000-8000-000000000103','dashboard-ai-3','Dashboard AI 3','student','active','hash',false,$1,$1),
  ('30000000-0000-4000-8000-000000000104','dashboard-ai-4','Dashboard AI 4','student','active','hash',false,$1,$1)`,
			[]any{now.Add(-2 * time.Hour)},
		},
		{
			`INSERT INTO ai_providers(
  id,name,base_url,protocol_mode,encrypted_api_key,key_version,
  key_updated_at,active,created_by,created_at,updated_at
) VALUES(
  '40000000-0000-4000-8000-000000000001',
  'Dashboard Provider','https://dashboard.invalid/v1','chat_completions',
  decode(repeat('aa',29),'hex'),1,$1,true,
  '30000000-0000-4000-8000-000000000001',$1,$1
)`,
			[]any{now.Add(-2 * time.Hour)},
		},
		{
			`INSERT INTO ai_models(
  id,provider_id,upstream_model_id,modality,
  context_window_tokens,max_output_tokens,
  connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,
  image_quota_tokens,input_price_micro_usd_per_million_tokens,
  output_price_micro_usd_per_million_tokens,
  created_by,updated_by,created_at,updated_at
) VALUES(
  '41000000-0000-4000-8000-000000000001',
  '40000000-0000-4000-8000-000000000001',
  'dashboard-model','text',8192,1024,1000,30000,30000,120000,
  1024,1000000,2000000,
  '30000000-0000-4000-8000-000000000001',
  '30000000-0000-4000-8000-000000000001',$1,$1
)`,
			[]any{now.Add(-2 * time.Hour)},
		},
		{
			`INSERT INTO prompt_templates(
  id,subject,version,system_prompt,active,created_by,created_at
) VALUES(
  '42000000-0000-4000-8000-000000000001',
  'math',1,'Dashboard prompt',true,
  '30000000-0000-4000-8000-000000000001',$1
)`,
			[]any{now.Add(-2 * time.Hour)},
		},
		{
			`INSERT INTO ai_threads(
  id,student_id,title,subject,last_message_at,created_at
) VALUES
  ('43000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000101','Dashboard 1','math',$1,$1),
  ('43000000-0000-4000-8000-000000000002','30000000-0000-4000-8000-000000000101','Dashboard 2','math',$1,$1),
  ('43000000-0000-4000-8000-000000000003','30000000-0000-4000-8000-000000000102','Dashboard 3','math',$1,$1),
  ('43000000-0000-4000-8000-000000000004','30000000-0000-4000-8000-000000000103','Dashboard 4','math',$1,$1),
  ('43000000-0000-4000-8000-000000000005','30000000-0000-4000-8000-000000000104','Dashboard 5','math',$1,$1),
  ('43000000-0000-4000-8000-000000000006','30000000-0000-4000-8000-000000000101','Dashboard future','math',$1,$1)`,
			[]any{now.Add(-time.Minute)},
		},
		{
			`INSERT INTO ai_messages(
  id,thread_id,role,sender_user_id,body_text,idempotency_key,created_at
) VALUES
  ('44000000-0000-4000-8000-000000000001','43000000-0000-4000-8000-000000000001','student','30000000-0000-4000-8000-000000000101','Dashboard request 1','dashboard-msg-0001',$1),
  ('44000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000002','student','30000000-0000-4000-8000-000000000101','Dashboard request 2','dashboard-msg-0002',$1),
  ('44000000-0000-4000-8000-000000000003','43000000-0000-4000-8000-000000000003','student','30000000-0000-4000-8000-000000000102','Dashboard request 3','dashboard-msg-0003',$1),
  ('44000000-0000-4000-8000-000000000004','43000000-0000-4000-8000-000000000004','student','30000000-0000-4000-8000-000000000103','Dashboard request 4','dashboard-msg-0004',$1),
  ('44000000-0000-4000-8000-000000000005','43000000-0000-4000-8000-000000000005','student','30000000-0000-4000-8000-000000000104','Dashboard request 5','dashboard-msg-0005',$1),
  ('44000000-0000-4000-8000-000000000006','43000000-0000-4000-8000-000000000006','student','30000000-0000-4000-8000-000000000101','Dashboard future request','dashboard-msg-0006',$1)`,
			[]any{now.Add(-time.Hour)},
		},
		{
			`INSERT INTO ai_runs(
  id,thread_id,student_id,trigger_message_id,attempt_no,idempotency_key,status,
  provider_id,provider_key_version,provider_base_url,protocol_mode,
  model_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,
  image_quota_tokens,input_price_micro_usd_per_million_tokens,
  output_price_micro_usd_per_million_tokens,
  prompt_id,prompt_subject,prompt_version,prompt_sha256,
  connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,
  reserved_request_count,reserved_token_count,quota_day_key,quota_month_key,estimator_version,
  lease_owner,lease_expires_at,heartbeat_at,
  input_tokens,output_tokens,cost_micro_usd,usage_source,
  first_byte_ms,total_ms,error_code,
  created_at,updated_at,started_at,completed_at
)
SELECT
  fixture.id::uuid,fixture.thread_id::uuid,fixture.student_id::uuid,
  fixture.message_id::uuid,1,fixture.idempotency_key,fixture.status,
  '40000000-0000-4000-8000-000000000001'::uuid,1,
  'https://dashboard.invalid/v1','chat_completions',
  '41000000-0000-4000-8000-000000000001'::uuid,
  'dashboard-model','text',8192,1024,1024,1000000,2000000,
  '42000000-0000-4000-8000-000000000001'::uuid,
  'math',1,encode(digest('Dashboard prompt','sha256'),'hex'),
  1000,30000,30000,120000,1,1024,'2026-07-30','2026-07',1,
  fixture.lease_owner,fixture.lease_expires_at,fixture.heartbeat_at,
  fixture.input_tokens,fixture.output_tokens,fixture.cost_micro_usd,
  fixture.usage_source,fixture.first_byte_ms,fixture.total_ms,fixture.error_code,
  fixture.created_at,fixture.updated_at,fixture.started_at,fixture.completed_at
FROM (VALUES
  ('45000000-0000-4000-8000-000000000001','43000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000101','44000000-0000-4000-8000-000000000001','dashboard-run-0001','succeeded',NULL::text,NULL::timestamptz,NULL::timestamptz,10::bigint,20::bigint,300::bigint,'upstream',100::bigint,500::bigint,NULL::text,$1::timestamptz-interval '40 minutes',$1::timestamptz-interval '30 minutes',$1::timestamptz-interval '35 minutes',$1::timestamptz-interval '30 minutes'),
  ('45000000-0000-4000-8000-000000000002','43000000-0000-4000-8000-000000000002','30000000-0000-4000-8000-000000000101','44000000-0000-4000-8000-000000000002','dashboard-run-0002','failed',NULL,NULL,NULL,NULL,NULL,NULL,'unknown',NULL,NULL,'upstream_error',$1::timestamptz-interval '10 minutes',$1::timestamptz-interval '5 minutes',$1::timestamptz-interval '7 minutes',$1::timestamptz-interval '5 minutes'),
  ('45000000-0000-4000-8000-000000000003','43000000-0000-4000-8000-000000000003','30000000-0000-4000-8000-000000000102','44000000-0000-4000-8000-000000000003','dashboard-run-0003','queued',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,$1::timestamptz-interval '20 minutes',$1::timestamptz-interval '20 minutes',NULL,NULL),
  ('45000000-0000-4000-8000-000000000004','43000000-0000-4000-8000-000000000004','30000000-0000-4000-8000-000000000103','44000000-0000-4000-8000-000000000004','dashboard-run-0004','streaming','dashboard-active',$1::timestamptz+interval '5 minutes',$1::timestamptz-interval '1 minute',NULL,NULL,NULL,NULL,NULL,NULL,NULL,$1::timestamptz-interval '15 minutes',$1::timestamptz-interval '1 minute',$1::timestamptz-interval '10 minutes',NULL),
  ('45000000-0000-4000-8000-000000000005','43000000-0000-4000-8000-000000000005','30000000-0000-4000-8000-000000000104','44000000-0000-4000-8000-000000000005','dashboard-run-0005','streaming','dashboard-expired',$1::timestamptz-interval '1 minute',$1::timestamptz-interval '2 minutes',NULL,NULL,NULL,NULL,NULL,NULL,NULL,$1::timestamptz-interval '10 minutes',$1::timestamptz-interval '2 minutes',$1::timestamptz-interval '8 minutes',NULL)
) AS fixture(
  id,thread_id,student_id,message_id,idempotency_key,status,
  lease_owner,lease_expires_at,heartbeat_at,
  input_tokens,output_tokens,cost_micro_usd,usage_source,
  first_byte_ms,total_ms,error_code,
  created_at,updated_at,started_at,completed_at
)`,
			[]any{now},
		},
		{
			`INSERT INTO files(id,created_by,created_at) VALUES
  ('50000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001',$1),
  ('50000000-0000-4000-8000-000000000002','30000000-0000-4000-8000-000000000001',$1),
  ('50000000-0000-4000-8000-000000000003','30000000-0000-4000-8000-000000000001',$1),
  ('50000000-0000-4000-8000-000000000004','30000000-0000-4000-8000-000000000001',$1)`,
			[]any{now.Add(-time.Hour)},
		},
		{
			`INSERT INTO file_versions(
  id,file_id,version,purpose,object_key,display_name,declared_mime,
  size_bytes,sha256,processing_state,created_by,created_at
) VALUES
  ('50100000-0000-4000-8000-000000000001','50000000-0000-4000-8000-000000000001',1,'teaching','dashboard/file-1','file-1.pdf','application/pdf',10,repeat('a',64),'processing','30000000-0000-4000-8000-000000000001',$1),
  ('50100000-0000-4000-8000-000000000002','50000000-0000-4000-8000-000000000002',1,'teaching','dashboard/file-2','file-2.pdf','application/pdf',10,repeat('b',64),'processing','30000000-0000-4000-8000-000000000001',$1),
  ('50100000-0000-4000-8000-000000000003','50000000-0000-4000-8000-000000000003',1,'teaching','dashboard/file-3','file-3.pdf','application/pdf',10,repeat('c',64),'processing','30000000-0000-4000-8000-000000000001',$1),
  ('50100000-0000-4000-8000-000000000004','50000000-0000-4000-8000-000000000004',1,'teaching','dashboard/file-4','file-4.pdf','application/pdf',10,repeat('d',64),'processing','30000000-0000-4000-8000-000000000001',$1)`,
			[]any{now.Add(-time.Hour)},
		},
		{
			`INSERT INTO file_processing_jobs(
  id,file_version_id,kind,state,attempts,available_at,
  lease_owner,lease_until,created_at,updated_at
) VALUES
  ('51000000-0000-4000-8000-000000000001','50100000-0000-4000-8000-000000000001','process_file','queued',0,$1::timestamptz-interval '20 minutes',NULL,NULL,$1::timestamptz-interval '30 minutes',$1::timestamptz-interval '20 minutes'),
  ('51000000-0000-4000-8000-000000000002','50100000-0000-4000-8000-000000000002','process_file','running',1,$1::timestamptz-interval '20 minutes','dashboard-active',$1::timestamptz+interval '5 minutes',$1::timestamptz-interval '30 minutes',$1::timestamptz-interval '1 minute'),
  ('51000000-0000-4000-8000-000000000003','50100000-0000-4000-8000-000000000003','process_file','running',1,$1::timestamptz-interval '20 minutes','dashboard-expired',$1::timestamptz-interval '1 minute',$1::timestamptz-interval '30 minutes',$1::timestamptz-interval '2 minutes'),
  ('51000000-0000-4000-8000-000000000004','50100000-0000-4000-8000-000000000004','process_file','running',4,$1::timestamptz-interval '20 minutes','dashboard-exhausted',$1::timestamptz-interval '1 minute',$1::timestamptz-interval '30 minutes',$1::timestamptz-interval '2 minutes')`,
			[]any{now},
		},
		{
			`INSERT INTO outbox_events(
  id,kind,payload,created_at,published_at,dedupe_key,
  lease_owner,lease_until,attempts,next_attempt_at,last_error_category
) VALUES
  ('52000000-0000-4000-8000-000000000001','dashboard.test','{}',$1::timestamptz-interval '30 minutes',NULL,'dashboard-outbox-1',NULL,NULL,0,$1::timestamptz-interval '20 minutes',NULL),
  ('52000000-0000-4000-8000-000000000002','dashboard.test','{}',$1::timestamptz-interval '30 minutes',NULL,'dashboard-outbox-2','dashboard-expired',$1::timestamptz-interval '1 minute',1,$1::timestamptz-interval '20 minutes',NULL),
  ('52000000-0000-4000-8000-000000000003','dashboard.test','{}',$1::timestamptz-interval '30 minutes',NULL,'dashboard-outbox-3','dashboard-active',$1::timestamptz+interval '5 minutes',1,$1::timestamptz-interval '20 minutes',NULL),
  ('52000000-0000-4000-8000-000000000004','dashboard.test','{}',$1::timestamptz-interval '30 minutes',NULL,'dashboard-outbox-4',NULL,NULL,10,$1::timestamptz-interval '20 minutes','attempts_exhausted'),
  ('52000000-0000-4000-8000-000000000005','dashboard.test','{}',$1::timestamptz-interval '30 minutes',$1::timestamptz-interval '5 minutes','dashboard-outbox-5',NULL,NULL,1,$1::timestamptz-interval '20 minutes','delivery_failed'),
  ('52000000-0000-4000-8000-000000000006','dashboard.test','{}',$1::timestamptz-interval '30 minutes',NULL,'dashboard-outbox-6',NULL,NULL,4,$1::timestamptz-interval '20 minutes',NULL),
  ('52000000-0000-4000-8000-000000000007','dashboard.test','{}',$1::timestamptz-interval '30 minutes',NULL,'dashboard-outbox-7','dashboard-expired-9',$1::timestamptz-interval '1 minute',9,$1::timestamptz-interval '20 minutes',NULL)`,
			[]any{now},
		},
		{
			`INSERT INTO backup_runs(
  id,idempotency_key,trigger_kind,state,requested_by,
  requested_at,started_at,finished_at,database_migration_version,
  encryption_key_id,local_snapshot_id,remote_snapshot_id,manifest_sha256,
  local_expires_at,remote_expires_at
) VALUES(
  '53000000-0000-4000-8000-000000000001','dashboard-backup','manual','succeeded',
  '30000000-0000-4000-8000-000000000001',
  $1::timestamptz-interval '30 minutes',$1::timestamptz-interval '25 minutes',$1::timestamptz-interval '20 minutes',
  22,'dashboard-key','dashboard-local','dashboard-remote',
  decode(repeat('ab',32),'hex'),$1::timestamptz+interval '7 days',$1::timestamptz+interval '30 days'
)`,
			[]any{now},
		},
		{
			`INSERT INTO restore_verifications(
  id,backup_run_id,state,started_at,finished_at,restored_migration_version,
  database_row_counts,checked_object_count,missing_object_count,
  unexpected_object_count,session_revocation_verified,rto_seconds,report_sha256
) VALUES(
  '53100000-0000-4000-8000-000000000001',
  '53000000-0000-4000-8000-000000000001','succeeded',
  $1::timestamptz-interval '10 minutes',$1::timestamptz-interval '5 minutes',22,
  '{"users":4}',4,0,0,true,73,decode(repeat('cd',32),'hex')
)`,
			[]any{now},
		},
		{
			`INSERT INTO operational_alerts(
  id,dedupe_key,category,severity,state,
  first_observed_at,last_observed_at,acknowledged_by,acknowledged_at,
  resolved_at,current_value,threshold_value,summary
) VALUES
  ('54000000-0000-4000-8000-000000000001','dashboard-warning','queue','warning','open',$1::timestamptz-interval '20 minutes',$1::timestamptz-interval '2 minutes',NULL,NULL,NULL,2,1,'Dashboard warning'),
  ('54000000-0000-4000-8000-000000000002','dashboard-critical','backup','critical','acknowledged',$1::timestamptz-interval '20 minutes',$1::timestamptz-interval '2 minutes','30000000-0000-4000-8000-000000000001',$1::timestamptz-interval '1 minute',NULL,2,1,'Dashboard critical'),
  ('54000000-0000-4000-8000-000000000003','dashboard-resolved','queue','warning','resolved',$1::timestamptz-interval '20 minutes',$1::timestamptz-interval '2 minutes',NULL,NULL,$1::timestamptz-interval '1 minute',0,1,'Dashboard resolved')`,
			[]any{now},
		},
		{
			`INSERT INTO audit_logs(
  actor_user_id,action,target_type,target_id,metadata,request_id,occurred_at
) VALUES
  ('30000000-0000-4000-8000-000000000001','operations.dashboard_checked','operations','dashboard','{"outcome":"succeeded"}','dashboard-audit-old',$1::timestamptz-interval '180 days'),
  ('30000000-0000-4000-8000-000000000001','file.download','file','fixture','{"outcome":"denied"}','dashboard-audit-file',$1::timestamptz-interval '1 minute'),
  ('30000000-0000-4000-8000-000000000001','ai.run','ai_run','fixture','{"outcome":"failed"}','dashboard-audit-ai',$1::timestamptz-interval '1 minute')`,
			[]any{now},
		},
	} {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("fixture statement %d: %v", index, err)
		}
	}
}

func insertDashboardFutureAIRun(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	now time.Time,
) {
	t.Helper()
	if _, err := tx.Exec(ctx, `
INSERT INTO ai_runs(
  id,thread_id,student_id,trigger_message_id,attempt_no,idempotency_key,status,
  provider_id,provider_key_version,provider_base_url,protocol_mode,
  model_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,
  image_quota_tokens,input_price_micro_usd_per_million_tokens,
  output_price_micro_usd_per_million_tokens,
  prompt_id,prompt_subject,prompt_version,prompt_sha256,
  connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,
  reserved_request_count,reserved_token_count,quota_day_key,quota_month_key,estimator_version,
  usage_source,error_code,created_at,updated_at,started_at,completed_at
) VALUES(
  '45000000-0000-4000-8000-000000000006',
  '43000000-0000-4000-8000-000000000006',
  '30000000-0000-4000-8000-000000000101',
  '44000000-0000-4000-8000-000000000006',
  1,'dashboard-run-0006','failed',
  '40000000-0000-4000-8000-000000000001',1,
  'https://dashboard.invalid/v1','chat_completions',
  '41000000-0000-4000-8000-000000000001',
  'dashboard-model','text',8192,1024,1024,1000000,2000000,
  '42000000-0000-4000-8000-000000000001',
  'math',1,encode(digest('Dashboard prompt','sha256'),'hex'),
  1000,30000,30000,120000,1,1024,'2026-07-30','2026-07',1,
  'unknown','upstream_error',
  $1::timestamptz-interval '40 minutes',$1::timestamptz-interval '10 minutes',
  $1::timestamptz-interval '30 minutes',$1::timestamptz+interval '1 minute'
)`, now); err != nil {
		t.Fatal(err)
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

func dashboardQuerySource(
	t *testing.T,
	query string,
	start string,
	end string,
) string {
	t.Helper()
	startIndex := strings.Index(query, start)
	if startIndex < 0 {
		t.Fatalf("query missing source %q: %s", start, query)
	}
	source := query[startIndex:]
	endIndex := strings.Index(source, end)
	if endIndex < 0 {
		t.Fatalf("query source %q missing terminator %q: %s", start, end, query)
	}
	return source[:endIndex]
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
