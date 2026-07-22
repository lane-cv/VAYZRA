package notifications

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/qanda"
	"happylearn.local/app/tests/integration"
)

func TestWriterRejectsUnsafeIntentBeforeSQL(t *testing.T) {
	w := NewWriter(nil)
	bad := qanda.NotificationIntent{RecipientUserID: uuid.New(), Kind: "qa_created", Title: "unsafe body", Summary: "body leak", TargetType: "qa_thread", TargetID: uuid.New(), TargetPath: "/student/questions/" + uuid.NewString(), DedupeKey: "qa-created:" + uuid.NewString()}
	if err := w.Notify(context.Background(), bad); err == nil {
		t.Fatal("expected unsafe template rejection")
	}
}

func TestPostgresRecipientIsolationDedupeKeysetAndReadTransitions(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	first, second := insertNotificationUser(t, pool, "student"), insertNotificationUser(t, pool, "student")
	target := uuid.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(tx)
	in := qanda.NotificationIntent{RecipientUserID: first, Kind: "qa_replied", Title: "Teacher reply", Summary: "Your teacher replied to a question.", TargetType: "qa_thread", TargetID: target, TargetPath: "/student/questions/" + target.String(), DedupeKey: "qa-replied:" + uuid.NewString()}
	if err = w.Notify(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err = w.Notify(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.RecipientUserID = second
	if err = w.Notify(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(pool)
	items, _, err := store.List(ctx, first, Cursor{Limit: 1})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if count, err := store.UnreadCount(ctx, first); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err = store.MarkRead(ctx, second, items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign mark err=%v", err)
	}
	if err = store.MarkRead(ctx, first, items[0].ID); err != nil {
		t.Fatal(err)
	}
	var readAt time.Time
	if err = pool.QueryRow(ctx, `SELECT read_at FROM notifications WHERE id=$1`, items[0].ID).Scan(&readAt); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkRead(ctx, first, items[0].ID); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	if err = pool.QueryRow(ctx, `SELECT read_at FROM notifications WHERE id=$1`, items[0].ID).Scan(&after); err != nil || !after.Equal(readAt) {
		t.Fatalf("read changed: %v %v err=%v", readAt, after, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE notifications SET read_at=NULL WHERE id=$1`, items[0].ID); err == nil {
		t.Fatal("read_at reverted")
	}
	if _, err = pool.Exec(ctx, `UPDATE notifications SET title='changed' WHERE id=$1`, items[0].ID); err == nil {
		t.Fatal("content changed")
	}
	nowID, futureID := uuid.New(), uuid.New()
	for _, candidate := range []struct {
		id  uuid.UUID
		at  time.Time
		key string
	}{{nowID, time.Now().Add(-time.Minute), "cutoff-now:" + uuid.NewString()}, {futureID, time.Now().Add(time.Hour), "cutoff-future:" + uuid.NewString()}} {
		if _, err = pool.Exec(ctx, `INSERT INTO notifications(id,recipient_user_id,kind,title,summary,target_type,target_id,target_path,dedupe_key,created_at) VALUES($1,$2,'qa_replied','Teacher reply','Your teacher replied to a question.','qa_thread',$3,$4,$5,$6)`, candidate.id, first, target, "/student/questions/"+target.String(), candidate.key, candidate.at); err != nil {
			t.Fatal(err)
		}
	}
	if changed, err := store.MarkAllRead(ctx, first); err != nil || changed != 1 {
		t.Fatalf("mark all changed=%d err=%v", changed, err)
	}
	var nowRead, futureRead bool
	if err = pool.QueryRow(ctx, `SELECT read_at IS NOT NULL FROM notifications WHERE id=$1`, nowID).Scan(&nowRead); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT read_at IS NOT NULL FROM notifications WHERE id=$1`, futureID).Scan(&futureRead); err != nil {
		t.Fatal(err)
	}
	if !nowRead || futureRead {
		t.Fatalf("nowRead=%v futureRead=%v", nowRead, futureRead)
	}
}

func TestRealWriterFailureRollsBackQACreationAndAudit(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	student := insertNotificationUser(t, pool, "student")
	var admin uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL LIMIT 1`).Scan(&admin); err != nil {
		admin = insertNotificationUser(t, pool, "admin")
	}
	if _, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION notification_test_failure() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'forced notification failure'; END; $$ LANGUAGE plpgsql; CREATE TRIGGER notification_test_failure BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION notification_test_failure()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS notification_test_failure ON notifications; DROP FUNCTION IF EXISTS notification_test_failure()`)
	})
	store := qanda.NewPostgresStore(pool)
	uow := qanda.NewPostgresUnitOfWork(pool, func(tx pgx.Tx) qanda.NotificationWriter { return NewWriter(tx) })
	svc := qanda.NewService(store, uow, time.Now)
	actor := qanda.Principal{User: auth.User{ID: student, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "notification-rollback-" + uuid.NewString(), IP: net.ParseIP("192.0.2.5")}
	if _, _, err := svc.CreateThread(ctx, actor, qanda.CreateThreadInput{Title: "Rollback", Body: "Private body", IdempotencyKey: "rollback-" + uuid.NewString()}); err == nil {
		t.Fatal("expected notification failure")
	}
	var threads, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_threads WHERE student_id=$1`, student).Scan(&threads); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE actor_user_id=$1`, student).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if threads != 0 || audits != 0 {
		t.Fatalf("threads=%d audits=%d", threads, audits)
	}
}

func TestRealWriterFailureRollsBackEveryQAMutation(t *testing.T) {
	for _, operation := range []string{"create", "admin_reply", "student_follow_up", "status_change"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			pool := integration.StartPostgres(t)
			if err := database.Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			student := insertNotificationUser(t, pool, "student")
			var admin uuid.UUID
			if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL LIMIT 1`).Scan(&admin); err != nil {
				admin = insertNotificationUser(t, pool, "admin")
			}
			svc := qanda.NewService(qanda.NewPostgresStore(pool), qanda.NewPostgresUnitOfWork(pool, func(tx pgx.Tx) qanda.NotificationWriter { return NewWriter(tx) }), time.Now)
			studentActor := qanda.Principal{User: auth.User{ID: student, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "rollback-student-" + uuid.NewString(), IP: net.ParseIP("192.0.2.20")}
			adminActor := qanda.Principal{User: auth.User{ID: admin, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "rollback-admin-" + uuid.NewString(), IP: net.ParseIP("192.0.2.21")}
			var thread qanda.Thread
			if operation != "create" {
				var err error
				thread, _, err = svc.CreateThread(ctx, studentActor, qanda.CreateThreadInput{Title: "Rollback setup", Body: "setup", IdempotencyKey: "setup-create-" + uuid.NewString()})
				if err != nil {
					t.Fatal(err)
				}
			}
			if operation == "student_follow_up" {
				var err error
				thread, _, err = svc.AddAdminMessage(ctx, adminActor, qanda.AddAdminMessageInput{ThreadID: thread.ID, ExpectedVersion: thread.Version, Body: "setup reply", IdempotencyKey: "setup-reply-" + uuid.NewString()})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := qaRollbackSnapshot(t, pool, student, thread.ID)
			if _, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION notification_test_failure_all() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'forced notification failure'; END; $$ LANGUAGE plpgsql; CREATE TRIGGER notification_test_failure_all BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION notification_test_failure_all()`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS notification_test_failure_all ON notifications; DROP FUNCTION IF EXISTS notification_test_failure_all()`)
			})
			var err error
			switch operation {
			case "create":
				_, _, err = svc.CreateThread(ctx, studentActor, qanda.CreateThreadInput{Title: "Must rollback", Body: "body", IdempotencyKey: "fail-create-" + uuid.NewString()})
			case "admin_reply":
				_, _, err = svc.AddAdminMessage(ctx, adminActor, qanda.AddAdminMessageInput{ThreadID: thread.ID, ExpectedVersion: thread.Version, Body: "must rollback", IdempotencyKey: "fail-reply-" + uuid.NewString()})
			case "student_follow_up":
				_, _, err = svc.AddStudentMessage(ctx, studentActor, qanda.AddMessageInput{ThreadID: thread.ID, ExpectedVersion: thread.Version, Body: "must rollback", IdempotencyKey: "fail-follow-" + uuid.NewString()})
			case "status_change":
				_, err = svc.ChangeStatus(ctx, adminActor, qanda.ChangeStatusInput{ThreadID: thread.ID, ExpectedVersion: thread.Version, Status: qanda.StatusInProgress})
			}
			if err == nil {
				t.Fatal("expected notification failure")
			}
			after := qaRollbackSnapshot(t, pool, student, thread.ID)
			if before != after {
				t.Fatalf("before=%+v after=%+v", before, after)
			}
		})
	}
}

type qaSnapshot struct {
	Threads, Messages, Audits, Notifications int
	Status                                   string
	Version                                  int64
	LastActivity, Updated                    string
}

func qaRollbackSnapshot(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, student, threadID uuid.UUID) qaSnapshot {
	t.Helper()
	ctx := context.Background()
	var s qaSnapshot
	if threadID == uuid.Nil {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_threads WHERE student_id=$1`, student).Scan(&s.Threads); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_messages WHERE sender_user_id=$1`, student).Scan(&s.Messages); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE actor_user_id=$1`, student).Scan(&s.Audits); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&s.Notifications); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if err := pool.QueryRow(ctx, `SELECT status,version,last_message_at::text,updated_at::text FROM qa_threads WHERE id=$1`, threadID).Scan(&s.Status, &s.Version, &s.LastActivity, &s.Updated); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_threads WHERE id=$1`, threadID).Scan(&s.Threads); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_messages WHERE thread_id=$1`, threadID).Scan(&s.Messages); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_id=$1`, threadID.String()).Scan(&s.Audits); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1`, threadID).Scan(&s.Notifications); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRealWriterPersistsEveryQANotificationAndSkipsPrivateNotes(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	student := insertNotificationUser(t, pool, "student")
	var admin uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL LIMIT 1`).Scan(&admin); err != nil {
		admin = insertNotificationUser(t, pool, "admin")
	}
	store := qanda.NewPostgresStore(pool)
	svc := qanda.NewService(store, qanda.NewPostgresUnitOfWork(pool, func(tx pgx.Tx) qanda.NotificationWriter { return NewWriter(tx) }), time.Now)
	studentActor := qanda.Principal{User: auth.User{ID: student, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "qa-notify-student-" + uuid.NewString(), IP: net.ParseIP("192.0.2.10")}
	adminActor := qanda.Principal{User: auth.User{ID: admin, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "qa-notify-admin-" + uuid.NewString(), IP: net.ParseIP("192.0.2.11")}
	thread, createdMessage, err := svc.CreateThread(ctx, studentActor, qanda.CreateThreadInput{Title: "Notification flow", Body: "private create body", IdempotencyKey: "created-" + uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	thread, repliedMessage, err := svc.AddAdminMessage(ctx, adminActor, qanda.AddAdminMessageInput{ThreadID: thread.ID, ExpectedVersion: thread.Version, Body: "private reply body", IdempotencyKey: "replied-" + uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	thread, followedMessage, err := svc.AddStudentMessage(ctx, studentActor, qanda.AddMessageInput{ThreadID: thread.ID, ExpectedVersion: thread.Version, Body: "private follow-up body", IdempotencyKey: "followed-" + uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	thread, err = svc.ChangeStatus(ctx, adminActor, qanda.ChangeStatusInput{ThreadID: thread.ID, ExpectedVersion: thread.Version, Status: qanda.StatusInProgress})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AddTeacherNote(ctx, adminActor, qanda.AddTeacherNoteInput{ThreadID: thread.ID, Body: "private teacher note"}); err != nil {
		t.Fatal(err)
	}
	type row struct {
		recipient                          uuid.UUID
		kind, path, dedupe, title, summary string
	}
	rows, err := pool.Query(ctx, `SELECT recipient_user_id,kind,target_path,dedupe_key,title,summary FROM notifications WHERE target_id=$1`, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]row{}
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.recipient, &r.kind, &r.path, &r.dedupe, &r.title, &r.summary); err != nil {
			t.Fatal(err)
		}
		got[r.kind] = r
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]row{
		"qa_created":        {recipient: admin, path: "/admin/questions/" + thread.ID.String(), dedupe: "qa-created:" + createdMessage.ID.String(), title: "New student question", summary: "A student created a question."},
		"qa_replied":        {recipient: student, path: "/student/questions/" + thread.ID.String(), dedupe: "qa-replied:" + repliedMessage.ID.String(), title: "Teacher reply", summary: "Your teacher replied to a question."},
		"qa_followed_up":    {recipient: admin, path: "/admin/questions/" + thread.ID.String(), dedupe: "qa-followed-up:" + followedMessage.ID.String(), title: "Student follow-up", summary: "A student followed up on a question."},
		"qa_status_changed": {recipient: student, path: "/student/questions/" + thread.ID.String(), dedupe: "qa-status:" + thread.ID.String() + ":" + strconv.FormatInt(thread.Version, 10), title: "Question status changed", summary: "Your question status changed."},
	}
	if len(got) != len(want) {
		t.Fatalf("notification rows=%#v", got)
	}
	for kind, w := range want {
		g, ok := got[kind]
		if !ok || g.recipient != w.recipient || g.path != w.path || g.dedupe != w.dedupe || g.title != w.title || g.summary != w.summary {
			t.Fatalf("kind=%s got=%#v want=%#v", kind, g, w)
		}
	}
	for _, secret := range []string{"private create body", "private reply body", "private follow-up body", "private teacher note"} {
		var leaks int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE target_id=$1 AND (title LIKE '%'||$2||'%' OR summary LIKE '%'||$2||'%' OR target_path LIKE '%'||$2||'%' OR dedupe_key LIKE '%'||$2||'%')`, thread.ID, secret).Scan(&leaks); err != nil || leaks != 0 {
			t.Fatalf("secret=%q leaks=%d err=%v", secret, leaks, err)
		}
	}
}

func insertNotificationUser(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, role string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	name := "notify_" + role + "_" + uuid.NewString()
	if err := pool.QueryRow(context.Background(), `INSERT INTO users(username,display_name,role,status,password_hash) VALUES($1,'Notification fixture',$2,'active','hash') RETURNING id`, name, role).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWriterRejectsKindPathMismatch(t *testing.T) {
	w := NewWriter(nil)
	in := qanda.NotificationIntent{RecipientUserID: uuid.New(), Kind: "qa_created", Title: "New student question", Summary: "A student created a question.", TargetType: "qa_thread", TargetID: uuid.New(), TargetPath: "/student/questions/" + uuid.NewString(), DedupeKey: "qa-created:" + uuid.NewString()}
	if err := w.Notify(context.Background(), in); err == nil {
		t.Fatal("expected admin path rejection")
	}
}

func TestWriterRejectsDedupeKeyThatDoesNotMatchKind(t *testing.T) {
	target := uuid.New()
	in := qanda.NotificationIntent{RecipientUserID: uuid.New(), Kind: "qa_replied", Title: "Teacher reply", Summary: "Your teacher replied to a question.", TargetType: "qa_thread", TargetID: target, TargetPath: "/student/questions/" + target.String(), DedupeKey: "qa-created:" + uuid.NewString()}
	if err := NewWriter(nil).Notify(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err=%v", err)
	}
}
