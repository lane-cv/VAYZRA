package qanda

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresStudentIsolationDisabledDenialAndStableCursor(t *testing.T) {
	ctx, pool := postgresFixture(t)
	first, second := insertQAStudent(t, pool), insertQAStudent(t, pool)
	stamp := time.Date(2026, 7, 22, 6, 0, 0, 0, time.UTC)
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() > ids[j].String() })
	for i, id := range ids {
		if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at,created_at,updated_at) VALUES($1,$2,$3,'pending',$4,$4,$4)`, id, first, fmt.Sprintf("thread-%d", i), stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE qa_threads SET status='completed',completed_at=$2 WHERE id=$1`, ids[0], stamp); err != nil {
		t.Fatal(err)
	}
	otherThread := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at) VALUES($1,$2,'private','pending',$3)`, otherThread, second, stamp); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(pool)
	page1, next, err := store.ListStudentThreads(ctx, first, "", ThreadCursor{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || page1[0].ID != ids[0] || page1[1].ID != ids[1] || next.ID != ids[1] || !next.LastMessageAt.Equal(stamp) {
		t.Fatalf("page1=%#v next=%#v", page1, next)
	}
	page2, _, err := store.ListStudentThreads(ctx, first, "", next)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].ID != ids[2] {
		t.Fatalf("page2=%#v", page2)
	}
	pending, _, err := store.ListStudentThreads(ctx, first, StatusPending, ThreadCursor{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].ID != ids[1] || pending[1].ID != ids[2] {
		t.Fatalf("pending=%#v", pending)
	}
	if _, err := store.GetStudentThread(ctx, first, otherThread); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-student get error=%v", err)
	}
	if _, err := store.GetStudentThread(ctx, second, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reverse cross-student get error=%v", err)
	}
	malformedKey := strings.Repeat("m", 16)
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key,created_at) VALUES($1,$2,$3,'student','student_follow_up','must remain private',$4,$5)`, uuid.New(), otherThread, first, malformedKey, stamp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FindMessageByIdempotency(ctx, first, malformedKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner idempotency lookup error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetStudentThread(ctx, first, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled get error=%v", err)
	}
	if threads, _, err := store.ListStudentThreads(ctx, first, "", ThreadCursor{Limit: 10}); !errors.Is(err, ErrNotFound) || threads != nil {
		t.Fatalf("disabled list=%#v error=%v", threads, err)
	}
}

func TestPostgresStudentHistoryRejectsMalformedMessagesAndAttachments(t *testing.T) {
	ctx, pool := postgresFixture(t)
	owner, other, admin := insertQAStudent(t, pool), insertQAStudent(t, pool), activeQAAdmin(t, pool)
	threadID := uuid.New()
	stamp := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at,created_at,updated_at) VALUES($1,$2,'History','waiting_student',$3,$3,$3)`, threadID, owner, stamp); err != nil {
		t.Fatal(err)
	}
	studentMessage, adminMessage := uuid.New(), uuid.New()
	malformedStudent, malformedAdmin := uuid.New(), uuid.New()
	for _, row := range []struct {
		id, sender            uuid.UUID
		role, kind, body, key string
		at                    time.Time
	}{
		{studentMessage, owner, "student", "initial", "visible student", uuid.NewString(), stamp},
		{malformedStudent, other, "student", "student_follow_up", "cross student secret", uuid.NewString(), stamp.Add(time.Second)},
		{adminMessage, admin, "admin", "admin_reply", "visible admin", uuid.NewString(), stamp.Add(2 * time.Second)},
		{malformedAdmin, other, "admin", "admin_reply", "non-admin sender secret", uuid.NewString(), stamp.Add(3 * time.Second)},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, row.id, threadID, row.sender, row.role, row.kind, row.body, row.key, row.at); err != nil {
			t.Fatal(err)
		}
	}
	ownerFile := insertQATestFileVersion(t, pool, owner, "visible-student.pdf")
	otherFile := insertQATestFileVersion(t, pool, other, "cross-student-secret.pdf")
	adminFile := insertQATestFileVersion(t, pool, admin, "visible-admin.pdf")
	studentOwnedFile := insertQATestFileVersion(t, pool, owner, "admin-foreign-secret.pdf")
	for _, binding := range []struct {
		message, version uuid.UUID
		position         int
		name             string
	}{
		{studentMessage, ownerFile, 0, "visible-student.pdf"},
		{studentMessage, otherFile, 1, "cross-student-secret.pdf"},
		{adminMessage, adminFile, 0, "visible-admin.pdf"},
		{adminMessage, studentOwnedFile, 1, "admin-foreign-secret.pdf"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO qa_message_files(message_id,file_version_id,sort_position,display_name) VALUES($1,$2,$3,$4)`, binding.message, binding.version, binding.position, binding.name); err != nil {
			t.Fatal(err)
		}
	}
	messages, _, err := NewPostgresStore(pool).ListStudentMessages(ctx, owner, threadID, MessageCursor{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].ID != studentMessage || messages[1].ID != adminMessage {
		t.Fatalf("messages=%#v", messages)
	}
	if len(messages[0].Attachments) != 1 || messages[0].Attachments[0].FileVersionID != ownerFile || len(messages[1].Attachments) != 1 || messages[1].Attachments[0].FileVersionID != adminFile {
		t.Fatalf("attachments=%#v/%#v", messages[0].Attachments, messages[1].Attachments)
	}
	dump := fmt.Sprintf("%#v", messages)
	for _, secret := range []string{"cross student secret", "non-admin sender secret", "cross-student-secret.pdf", "admin-foreign-secret.pdf", otherFile.String(), studentOwnedFile.String()} {
		if strings.Contains(dump, secret) {
			t.Fatalf("history leaked %q in %s", secret, dump)
		}
	}
}

func TestPostgresIdempotencyScopeAndNotificationRollback(t *testing.T) {
	ctx, pool := postgresFixture(t)
	admin := activeQAAdmin(t, pool)
	first, second := insertQAStudent(t, pool), insertQAStudent(t, pool)
	clock := func() time.Time { return time.Date(2026, 7, 22, 7, 0, 0, 0, time.UTC) }
	key := strings.Repeat("i", 16)

	notifications := &capturingNotifications{}
	service := NewService(NewPostgresStore(pool), NewPostgresUnitOfWork(pool, func(pgx.Tx) NotificationWriter { return notifications }), clock)
	firstThread, firstMessage, err := service.CreateThread(ctx, postgresPrincipal(first), CreateThreadInput{Title: "First", Body: "Body", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	againThread, againMessage, err := service.CreateThread(ctx, postgresPrincipal(first), CreateThreadInput{Title: "Changed", Body: "Changed", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if againThread.ID != firstThread.ID || againMessage.ID != firstMessage.ID {
		t.Fatalf("same-user retry created different records")
	}
	secondThread, _, err := service.CreateThread(ctx, postgresPrincipal(second), CreateThreadInput{Title: "Second", Body: "Body", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if secondThread.ID == firstThread.ID {
		t.Fatal("different students shared an idempotency result")
	}
	if notifications.count() != 2 {
		t.Fatalf("notifications=%d", notifications.count())
	}
	for _, intent := range notifications.snapshot() {
		if intent.RecipientUserID != admin {
			t.Fatalf("recipient=%s admin=%s", intent.RecipientUserID, admin)
		}
	}

	failing := NewService(NewPostgresStore(pool), NewPostgresUnitOfWork(pool, func(tx pgx.Tx) NotificationWriter { return failingTxNotification{tx: tx} }), clock)
	rollbackKey := strings.Repeat("z", 16)
	if _, _, err := failing.CreateThread(ctx, postgresPrincipal(first), CreateThreadInput{Title: "Rollback", Body: "Body", IdempotencyKey: rollbackKey}); err == nil {
		t.Fatal("notification failure did not fail transaction")
	}
	var threads, messages, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_messages WHERE sender_user_id=$1 AND idempotency_key=$2`, first, rollbackKey).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_threads WHERE student_id=$1 AND title='Rollback'`, first).Scan(&threads); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE actor_user_id=$1 AND action='qa.thread_created' AND metadata->>'messageCount'='1'`, first).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if threads != 0 || messages != 0 || audits != 1 { // one audit belongs to the committed first create
		t.Fatalf("threads=%d messages=%d audits=%d", threads, messages, audits)
	}
}

func TestPostgresConcurrentStudentFollowUpsAppendOnce(t *testing.T) {
	ctx, pool := postgresFixture(t)
	activeQAAdmin(t, pool)
	student := insertQAStudent(t, pool)
	notifications := &capturingNotifications{}
	clock := func() time.Time { return time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC) }
	service := NewService(NewPostgresStore(pool), NewPostgresUnitOfWork(pool, func(pgx.Tx) NotificationWriter { return notifications }), clock)
	thread, _, err := service.CreateThread(ctx, postgresPrincipal(student), CreateThreadInput{Title: "Concurrent", Body: "Initial", IdempotencyKey: strings.Repeat("a", 16)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE qa_threads SET status='waiting_student',version=2 WHERE id=$1`, thread.ID); err != nil {
		t.Fatal(err)
	}
	in := AddMessageInput{ThreadID: thread.ID, Body: "Follow up", IdempotencyKey: strings.Repeat("b", 16)}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, _, err := service.AddStudentMessage(context.Background(), postgresPrincipal(student), in)
			results <- err
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var status Status
	var version int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_messages WHERE sender_user_id=$1 AND idempotency_key=$2`, student, in.IdempotencyKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status,version FROM qa_threads WHERE id=$1`, thread.ID).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != StatusPending || version != 3 || notifications.count() != 2 { // create + one follow-up
		t.Fatalf("messages=%d status=%s version=%d notifications=%d", count, status, version, notifications.count())
	}
}

func TestPostgresStudentFollowUpUsesMonotonicActivityTime(t *testing.T) {
	ctx, pool := postgresFixture(t)
	activeQAAdmin(t, pool)
	student := insertQAStudent(t, pool)
	base := time.Date(2026, 7, 22, 11, 0, 0, 123456000, time.UTC)
	clock := base
	service := NewService(
		NewPostgresStore(pool),
		NewPostgresUnitOfWork(pool, func(pgx.Tx) NotificationWriter { return &capturingNotifications{} }),
		func() time.Time { return clock },
	)
	thread, initial, err := service.CreateThread(ctx, postgresPrincipal(student), CreateThreadInput{Title: "Clock", Body: "Initial", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE qa_threads SET status='waiting_student',version=2 WHERE id=$1`, thread.ID); err != nil {
		t.Fatal(err)
	}
	equalThread, equalMessage, err := service.AddStudentMessage(ctx, postgresPrincipal(student), AddMessageInput{ThreadID: thread.ID, Body: "Equal", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if !equalMessage.CreatedAt.Equal(base.Add(time.Microsecond)) || !equalThread.LastMessageAt.Equal(equalMessage.CreatedAt) || !equalThread.UpdatedAt.Equal(equalMessage.CreatedAt) {
		t.Fatalf("equal clock message=%s last=%s updated=%s", equalMessage.CreatedAt, equalThread.LastMessageAt, equalThread.UpdatedAt)
	}
	if _, err := pool.Exec(ctx, `UPDATE qa_threads SET status='waiting_student' WHERE id=$1`, thread.ID); err != nil {
		t.Fatal(err)
	}
	clock = base.Add(-time.Hour)
	backwardThread, backwardMessage, err := service.AddStudentMessage(ctx, postgresPrincipal(student), AddMessageInput{ThreadID: thread.ID, Body: "Backward", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if !backwardMessage.CreatedAt.Equal(base.Add(2*time.Microsecond)) || !backwardThread.LastMessageAt.Equal(backwardMessage.CreatedAt) || !backwardThread.UpdatedAt.Equal(backwardMessage.CreatedAt) {
		t.Fatalf("backward clock message=%s last=%s updated=%s", backwardMessage.CreatedAt, backwardThread.LastMessageAt, backwardThread.UpdatedAt)
	}

	store := NewPostgresStore(pool)
	cursor := MessageCursor{Limit: 1}
	var got []Message
	for range 3 {
		page, next, err := store.ListStudentMessages(ctx, student, thread.ID, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 1 {
			t.Fatalf("page=%#v cursor=%#v", page, cursor)
		}
		got = append(got, page[0])
		cursor = next
	}
	if got[0].ID != initial.ID || got[1].ID != equalMessage.ID || got[2].ID != backwardMessage.ID || !got[0].CreatedAt.Before(got[1].CreatedAt) || !got[1].CreatedAt.Before(got[2].CreatedAt) {
		t.Fatalf("forward history=%#v", got)
	}
}

func TestPostgresAdminQueueFiltersStableCursorAndWorkflow(t *testing.T) {
	ctx, pool := postgresFixture(t)
	admin := activeQAAdmin(t, pool)
	first, second := insertQAStudent(t, pool), insertQAStudent(t, pool)
	stamp := time.Date(2026, 7, 22, 13, 0, 0, 0, time.UTC)
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() > ids[j].String() })
	for i, id := range ids {
		student := first
		if i == 2 {
			student = second
		}
		status := "pending"
		if i == 1 {
			status = "in_progress"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,version,last_message_at,created_at,updated_at) VALUES($1,$2,$3,$4,1,$5,$6,$6)`, id, student, fmt.Sprintf("admin-%d", i), status, stamp, stamp.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	store := NewPostgresStore(pool)
	page, next, err := store.ListAdminThreads(ctx, AdminThreadFilter{StudentID: first, From: stamp.Add(-time.Minute), To: stamp.Add(2 * time.Hour)}, ThreadCursor{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != ids[0] || next.ID != ids[0] {
		t.Fatalf("page=%#v next=%#v", page, next)
	}
	rest, _, err := store.ListAdminThreads(ctx, AdminThreadFilter{StudentID: first}, next)
	if err != nil || len(rest) != 1 || rest[0].ID != ids[1] {
		t.Fatalf("rest=%#v err=%v", rest, err)
	}
	pending, _, err := store.ListAdminThreads(ctx, AdminThreadFilter{Status: StatusPending, StudentID: first, From: stamp.Add(-time.Minute)}, ThreadCursor{Limit: 10})
	if err != nil || len(pending) != 1 || pending[0].ID != ids[0] {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	notifications := &capturingNotifications{}
	svc := NewService(store, NewPostgresUnitOfWork(pool, func(pgx.Tx) NotificationWriter { return notifications }), func() time.Time { return stamp.Add(4 * time.Hour) })
	reply, _, err := svc.AddAdminMessage(ctx, adminPrincipal(admin), AddAdminMessageInput{ThreadID: ids[0], ExpectedVersion: 1, Body: "answer", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Status != StatusWaitingStudent || reply.Version != 2 {
		t.Fatalf("reply=%#v", reply)
	}
	if _, _, err := svc.AddAdminMessage(ctx, adminPrincipal(admin), AddAdminMessageInput{ThreadID: ids[0], ExpectedVersion: 1, Body: "stale", IdempotencyKey: uuid.NewString()}); !errors.Is(err, ErrThreadConflict) {
		t.Fatalf("stale reply error=%v", err)
	}
	note, err := svc.AddTeacherNote(ctx, adminPrincipal(admin), AddTeacherNoteInput{ThreadID: ids[0], Body: "private"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := svc.GetAdminThread(ctx, adminPrincipal(admin), ids[0], MessageCursor{})
	if err != nil || len(detail.Notes) != 1 || detail.Notes[0].ID != note.ID {
		t.Fatalf("detail=%#v err=%v", detail, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE teacher_notes SET body_text='changed' WHERE id=$1`, note.ID); err == nil {
		t.Fatal("teacher note update unexpectedly succeeded")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM teacher_notes WHERE id=$1`, note.ID); err == nil {
		t.Fatal("teacher note delete unexpectedly succeeded")
	}
	studentDetail, err := svc.GetStudentThread(ctx, postgresPrincipal(first), ids[0])
	if err != nil || len(studentDetail.Messages) != 1 {
		t.Fatalf("student detail=%#v err=%v", studentDetail, err)
	}
}

func TestPostgresAdminNotificationFailureRollsBack(t *testing.T) {
	ctx, pool := postgresFixture(t)
	admin := activeQAAdmin(t, pool)
	student := insertQAStudent(t, pool)
	stamp := time.Date(2026, 7, 22, 14, 0, 0, 0, time.UTC)
	thread := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,version,last_message_at,created_at,updated_at) VALUES($1,$2,'rollback','pending',1,$3,$3,$3)`, thread, student, stamp); err != nil {
		t.Fatal(err)
	}
	svc := NewService(NewPostgresStore(pool), NewPostgresUnitOfWork(pool, func(tx pgx.Tx) NotificationWriter { return failingTxNotification{tx: tx} }), func() time.Time { return stamp.Add(time.Hour) })
	if _, _, err := svc.AddAdminMessage(ctx, adminPrincipal(admin), AddAdminMessageInput{ThreadID: thread, ExpectedVersion: 1, Body: "secret reply", IdempotencyKey: uuid.NewString()}); err == nil {
		t.Fatal("reply unexpectedly committed")
	}
	var messages, audits int
	var version int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_messages WHERE thread_id=$1`, thread).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE target_id=$1 AND action='qa.admin_replied'`, thread.String()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT version FROM qa_threads WHERE id=$1`, thread).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || audits != 0 || version != 1 {
		t.Fatalf("messages=%d audits=%d version=%d", messages, audits, version)
	}
}

func TestPostgresConcurrentAdminRepliesAppendOnce(t *testing.T) {
	ctx, pool := postgresFixture(t)
	admin, student := activeQAAdmin(t, pool), insertQAStudent(t, pool)
	stamp := time.Date(2026, 7, 22, 16, 0, 0, 0, time.UTC)
	threadID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,version,last_message_at,created_at,updated_at) VALUES($1,$2,'concurrent admin','pending',1,$3,$3,$3)`, threadID, student, stamp); err != nil {
		t.Fatal(err)
	}
	notifications := &capturingNotifications{}
	svc := NewService(NewPostgresStore(pool), NewPostgresUnitOfWork(pool, func(pgx.Tx) NotificationWriter { return notifications }), func() time.Time { return stamp.Add(time.Hour) })
	in := AddAdminMessageInput{ThreadID: threadID, ExpectedVersion: 1, Body: "one answer", IdempotencyKey: uuid.NewString()}
	start, results := make(chan struct{}), make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, _, err := svc.AddAdminMessage(context.Background(), adminPrincipal(admin), in)
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var messages int
	var version int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM qa_messages WHERE sender_user_id=$1 AND idempotency_key=$2`, admin, in.IdempotencyKey).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT version FROM qa_threads WHERE id=$1`, threadID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || version != 2 || notifications.count() != 1 {
		t.Fatalf("messages=%d version=%d notifications=%d", messages, version, notifications.count())
	}
}

func TestPostgresAdminDetailContinuesAfterOneHundredMessages(t *testing.T) {
	ctx, pool := postgresFixture(t)
	admin, student := activeQAAdmin(t, pool), insertQAStudent(t, pool)
	base := time.Date(2026, 7, 22, 18, 0, 0, 0, time.UTC)
	threadID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,version,last_message_at,created_at,updated_at) VALUES($1,$2,'long history','waiting_student',102,$3,$4,$3)`, threadID, student, base.Add(100*time.Second), base); err != nil {
		t.Fatal(err)
	}
	want := make([]uuid.UUID, 101)
	for i := range want {
		want[i] = uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key,created_at) VALUES($1,$2,$3,'admin','admin_reply',$4,$5,$6)`, want[i], threadID, admin, "answer", uuid.NewString(), base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewService(NewPostgresStore(pool), nil, time.Now)
	first, err := svc.GetAdminThread(ctx, adminPrincipal(admin), threadID, MessageCursor{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) != 100 || first.Messages[0].ID != want[0] || first.Messages[99].ID != want[99] || first.NextMessageCursor.ID != want[99] {
		t.Fatalf("first len=%d next=%#v", len(first.Messages), first.NextMessageCursor)
	}
	second, err := svc.GetAdminThread(ctx, adminPrincipal(admin), threadID, first.NextMessageCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 1 || second.Messages[0].ID != want[100] || second.NextMessageCursor.ID != uuid.Nil {
		t.Fatalf("second=%#v", second)
	}
}

func postgresFixture(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

func insertQAStudent(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `INSERT INTO users(username,display_name,role,status,password_hash) VALUES($1,'Q&A student','student','active','hash') RETURNING id`, "qanda_student_"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertQATestFileVersion(t *testing.T, pool *pgxpool.Pool, creator uuid.UUID, displayName string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, creator).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,$3,'application/pdf',1,$4,'ready',$5) RETURNING id`, fileID, "qa-test/"+uuid.NewString(), displayName, strings.Repeat("a", 64), creator).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	return versionID
}

func activeQAAdmin(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL LIMIT 1`).Scan(&id); err == nil {
		return id
	}
	if err := pool.QueryRow(context.Background(), `INSERT INTO users(username,display_name,role,status,password_hash) VALUES($1,'Q&A teacher','admin','active','hash') RETURNING id`, "qanda_admin_"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func postgresPrincipal(id uuid.UUID) Principal {
	return Principal{User: auth.User{ID: id, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "postgres-req-" + id.String(), IP: net.ParseIP("127.0.0.1")}
}

type capturingNotifications struct {
	mu      sync.Mutex
	intents []NotificationIntent
}

func (w *capturingNotifications) Notify(_ context.Context, intent NotificationIntent) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.intents = append(w.intents, intent)
	return nil
}
func (w *capturingNotifications) count() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.intents) }
func (w *capturingNotifications) snapshot() []NotificationIntent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]NotificationIntent(nil), w.intents...)
}

type failingTxNotification struct{ tx pgx.Tx }

func (w failingTxNotification) Notify(ctx context.Context, _ NotificationIntent) error {
	_, err := w.tx.Exec(ctx, `SELECT 1/0`)
	return err
}
