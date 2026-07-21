package qanda

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

func TestAdminReplyStatusAndPrivateNoteWorkflow(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	studentID, adminID := uuid.New(), uuid.New()
	uow := newFakeUOW(adminID)
	thread := Thread{ID: uuid.New(), StudentID: studentID, Title: "Private title", Status: StatusPending, Version: 1, LastMessageAt: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}
	uow.state.threads[thread.ID] = thread
	svc := NewService(uow.readStore(), uow, func() time.Time { return now })
	actor := adminPrincipal(adminID)

	updated, message, err := svc.AddAdminMessage(context.Background(), actor, AddAdminMessageInput{ThreadID: thread.ID, ExpectedVersion: 1, Body: "  Answer  ", IdempotencyKey: strings.Repeat("a", 16)})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusWaitingStudent || updated.Version != 2 || message.Kind != MessageKindAdminReply || message.Body != "Answer" {
		t.Fatalf("thread=%#v message=%#v", updated, message)
	}
	if len(uow.notifications) != 1 || uow.notifications[0].RecipientUserID != studentID {
		t.Fatalf("notifications=%#v", uow.notifications)
	}
	if got := uow.audits[0]; got.Metadata["oldStatus"] != string(StatusPending) || got.Metadata["newStatus"] != string(StatusWaitingStudent) || strings.Contains(strings.ToLower(got.Metadata["messageCount"].(string)), "answer") {
		t.Fatalf("audit=%#v", got)
	}

	againThread, againMessage, err := svc.AddAdminMessage(context.Background(), actor, AddAdminMessageInput{ThreadID: thread.ID, ExpectedVersion: 1, Body: "changed retry body", IdempotencyKey: strings.Repeat("a", 16)})
	if err != nil || againThread.ID != updated.ID || againMessage.ID != message.ID || len(uow.notifications) != 1 {
		t.Fatalf("retry=%#v/%#v err=%v notifications=%d", againThread, againMessage, err, len(uow.notifications))
	}
	if _, _, err := svc.AddAdminMessage(context.Background(), actor, AddAdminMessageInput{ThreadID: thread.ID, ExpectedVersion: 1, Body: "new", IdempotencyKey: strings.Repeat("b", 16)}); !errors.Is(err, ErrThreadConflict) {
		t.Fatalf("stale reply error=%v", err)
	}

	completed, err := svc.ChangeStatus(context.Background(), actor, ChangeStatusInput{ThreadID: thread.ID, ExpectedVersion: 2, Status: StatusCompleted})
	if err != nil || completed.Status != StatusCompleted || completed.Version != 3 || completed.CompletedAt == nil {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	reopened, err := svc.ChangeStatus(context.Background(), actor, ChangeStatusInput{ThreadID: thread.ID, ExpectedVersion: 3, Status: StatusInProgress})
	if err != nil || reopened.Status != StatusInProgress || reopened.Version != 4 || reopened.CompletedAt != nil {
		t.Fatalf("reopened=%#v err=%v", reopened, err)
	}
	if _, err := svc.ChangeStatus(context.Background(), actor, ChangeStatusInput{ThreadID: thread.ID, ExpectedVersion: 4, Status: StatusPending}); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("invalid transition=%v", err)
	}

	note, err := svc.AddTeacherNote(context.Background(), actor, AddTeacherNoteInput{ThreadID: thread.ID, Body: "  internal only  "})
	if err != nil || note.Body != "internal only" || note.AuthorUserID != adminID {
		t.Fatalf("note=%#v err=%v", note, err)
	}
	if len(uow.notifications) != 3 {
		t.Fatalf("notes emitted notification: %#v", uow.notifications)
	}
	if strings.Contains(strings.ToLower(uow.audits[len(uow.audits)-1].Action), "internal only") {
		t.Fatalf("note leaked in audit=%#v", uow.audits[len(uow.audits)-1])
	}
}

func TestAdminPrincipalAndInputs(t *testing.T) {
	admin := adminPrincipal(uuid.New())
	student := studentPrincipal(uuid.New())
	svc := NewService(nil, nil, time.Now)
	if _, _, err := svc.ListAdminThreads(context.Background(), student, AdminThreadFilter{}, ThreadCursor{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("student list=%v", err)
	}
	if _, err := svc.AddTeacherNote(context.Background(), admin, AddTeacherNoteInput{ThreadID: uuid.New(), Body: strings.Repeat("界", 20001)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("long note=%v", err)
	}
	if _, err := svc.ChangeStatus(context.Background(), admin, ChangeStatusInput{ThreadID: uuid.Nil, ExpectedVersion: 1, Status: StatusCompleted}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil status=%v", err)
	}
}

func TestAdminReplyRollsBackOnAuditOrNotificationFailure(t *testing.T) {
	for _, failure := range []string{"audit", "notification"} {
		t.Run(failure, func(t *testing.T) {
			adminID, studentID := uuid.New(), uuid.New()
			uow := newFakeUOW(adminID)
			thread := Thread{ID: uuid.New(), StudentID: studentID, Status: StatusPending, Version: 1, LastMessageAt: time.Now().Add(-time.Hour)}
			uow.state.threads[thread.ID] = thread
			if failure == "audit" {
				uow.auditErr = errors.New("audit failed")
			} else {
				uow.notificationErr = errors.New("notification failed")
			}
			svc := NewService(uow.readStore(), uow, time.Now)
			_, _, err := svc.AddAdminMessage(context.Background(), adminPrincipal(adminID), AddAdminMessageInput{ThreadID: thread.ID, ExpectedVersion: 1, Body: "private reply", IdempotencyKey: uuid.NewString()})
			if err == nil {
				t.Fatal("reply succeeded")
			}
			if len(uow.state.messages) != 0 || uow.state.threads[thread.ID].Version != 1 || len(uow.audits) != 0 || len(uow.notifications) != 0 {
				t.Fatalf("rollback failed state=%#v", uow.state)
			}
		})
	}
}

func adminPrincipal(id uuid.UUID) Principal {
	return Principal{User: auth.User{ID: id, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "req-admin", IP: net.ParseIP("127.0.0.1")}
}
