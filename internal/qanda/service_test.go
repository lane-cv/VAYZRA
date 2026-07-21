package qanda

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

func TestStateTransitions(t *testing.T) {
	statuses := []Status{StatusPending, StatusInProgress, StatusWaitingStudent, StatusCompleted}
	actions := []Action{ActionCreate, ActionClaim, ActionAdminReply, ActionStudentFollowUp, ActionComplete, ActionReopen}
	roles := []auth.Role{auth.RoleAdmin, auth.RoleStudent}
	want := map[struct {
		status Status
		action Action
		role   auth.Role
	}]Status{
		{"", ActionCreate, auth.RoleStudent}:                            StatusPending,
		{StatusPending, ActionClaim, auth.RoleAdmin}:                    StatusInProgress,
		{StatusPending, ActionAdminReply, auth.RoleAdmin}:               StatusWaitingStudent,
		{StatusInProgress, ActionAdminReply, auth.RoleAdmin}:            StatusWaitingStudent,
		{StatusWaitingStudent, ActionAdminReply, auth.RoleAdmin}:        StatusWaitingStudent,
		{StatusWaitingStudent, ActionStudentFollowUp, auth.RoleStudent}: StatusPending,
		{StatusInProgress, ActionStudentFollowUp, auth.RoleStudent}:     StatusPending,
		{StatusCompleted, ActionStudentFollowUp, auth.RoleStudent}:      StatusPending,
		{StatusPending, ActionComplete, auth.RoleAdmin}:                 StatusCompleted,
		{StatusInProgress, ActionComplete, auth.RoleAdmin}:              StatusCompleted,
		{StatusWaitingStudent, ActionComplete, auth.RoleAdmin}:          StatusCompleted,
		{StatusCompleted, ActionReopen, auth.RoleAdmin}:                 StatusInProgress,
	}
	for _, status := range statuses {
		for _, action := range actions {
			for _, role := range roles {
				name := string(status) + "/" + string(action) + "/" + string(role)
				t.Run(name, func(t *testing.T) {
					got, err := NextStatus(status, action, role)
					expected, ok := want[struct {
						status Status
						action Action
						role   auth.Role
					}{status, action, role}]
					if !ok {
						if !errors.Is(err, ErrInvalidStatusTransition) {
							t.Fatalf("NextStatus() error = %v, want ErrInvalidStatusTransition", err)
						}
						return
					}
					if err != nil || got != expected {
						t.Fatalf("NextStatus() = %q, %v; want %q, nil", got, err, expected)
					}
				})
			}
		}
	}
	if got, err := NextStatus("", ActionCreate, auth.RoleStudent); err != nil || got != StatusPending {
		t.Fatalf("create transition = %q, %v", got, err)
	}
	for _, tc := range []struct {
		status Status
		action Action
		role   auth.Role
	}{{"unknown", ActionClaim, auth.RoleAdmin}, {StatusPending, "unknown", auth.RoleAdmin}, {StatusPending, ActionClaim, "unknown"}} {
		if _, err := NextStatus(tc.status, tc.action, tc.role); !errors.Is(err, ErrInvalidStatusTransition) {
			t.Fatalf("NextStatus(%q,%q,%q) error = %v", tc.status, tc.action, tc.role, err)
		}
	}
}

func TestValidateNormalizedCreateInputBounds(t *testing.T) {
	valid := CreateThreadInput{Title: "  标题  ", Body: "\n 正文 \t", IdempotencyKey: strings.Repeat("k", 16)}
	got, err := normalizeCreateInput(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "标题" || got.Body != "正文" {
		t.Fatalf("normalized input = %#v", got)
	}

	tests := []struct {
		name string
		in   CreateThreadInput
	}{
		{"empty title", withCreateTitle(valid, " \n ")},
		{"long unicode title", withCreateTitle(valid, strings.Repeat("界", 161))},
		{"invalid title utf8", withCreateTitle(valid, string([]byte{0xff}))},
		{"empty body", withCreateBody(valid, "\t")},
		{"long unicode body", withCreateBody(valid, strings.Repeat("界", 20001))},
		{"invalid body utf8", withCreateBody(valid, string([]byte{0xff}))},
		{"short key", withCreateKey(valid, strings.Repeat("k", 15))},
		{"long key", withCreateKey(valid, strings.Repeat("k", 129))},
		{"trimmed key differs", withCreateKey(valid, " "+strings.Repeat("k", 16))},
		{"too many attachments", withCreateAttachments(valid, make([]AttachmentInput, 21))},
		{"nil file version", withCreateAttachments(valid, []AttachmentInput{{SortPosition: 0}})},
		{"negative attachment sort", withCreateAttachments(valid, []AttachmentInput{{FileVersionID: uuid.New(), SortPosition: -1}})},
		{"duplicate attachment sort", withCreateAttachments(valid, []AttachmentInput{{FileVersionID: uuid.New(), SortPosition: 0}, {FileVersionID: uuid.New(), SortPosition: 0}})},
		{"duplicate attachment file", func() CreateThreadInput {
			id := uuid.New()
			return withCreateAttachments(valid, []AttachmentInput{{FileVersionID: id, SortPosition: 0}, {FileVersionID: id, SortPosition: 1}})
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeCreateInput(tc.in); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeCreateInput() error = %v, want ErrInvalidInput", err)
			}
		})
	}
	for _, n := range []int{16, 128} {
		if _, err := normalizeCreateInput(withCreateKey(valid, strings.Repeat("k", n))); err != nil {
			t.Fatalf("key length %d rejected: %v", n, err)
		}
	}
	twenty := make([]AttachmentInput, 20)
	for i := range twenty {
		twenty[i] = AttachmentInput{FileVersionID: uuid.New(), SortPosition: i}
	}
	if _, err := normalizeCreateInput(withCreateAttachments(valid, twenty)); err != nil {
		t.Fatalf("20 attachment records rejected: %v", err)
	}
}

func TestValidateStudentPrincipalAndCursors(t *testing.T) {
	valid := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "req-1", IP: net.ParseIP("127.0.0.1")}
	invalid := []Principal{
		{},
		{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusDisabled}, RequestID: "req", IP: net.ParseIP("127.0.0.1")},
		{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "req", IP: net.ParseIP("127.0.0.1")},
		{User: auth.User{Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "req", IP: net.ParseIP("127.0.0.1")},
		{User: valid.User, IP: valid.IP},
		{User: valid.User, RequestID: "req"},
	}
	for i, actor := range invalid {
		if err := authorizeStudent(actor); !errors.Is(err, ErrForbidden) {
			t.Fatalf("invalid principal %d error = %v", i, err)
		}
	}
	if err := authorizeStudent(valid); err != nil {
		t.Fatal(err)
	}

	svc := NewService(nil, nil, time.Now)
	if _, _, err := svc.ListStudentThreads(context.Background(), valid, "", ThreadCursor{Limit: 51}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("thread limit error = %v", err)
	}
	if _, _, err := svc.ListStudentThreads(context.Background(), valid, "unknown", ThreadCursor{Limit: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("thread status error = %v", err)
	}
	if _, _, err := svc.ListStudentMessages(context.Background(), valid, uuid.Nil, MessageCursor{Limit: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil thread error = %v", err)
	}
	if _, _, err := svc.ListStudentMessages(context.Background(), valid, uuid.New(), MessageCursor{Limit: 101}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("message limit error = %v", err)
	}
	if _, err := svc.GetStudentThread(context.Background(), valid, uuid.Nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil thread error = %v", err)
	}
}

func withCreateTitle(in CreateThreadInput, value string) CreateThreadInput {
	in.Title = value
	return in
}
func withCreateBody(in CreateThreadInput, value string) CreateThreadInput { in.Body = value; return in }
func withCreateKey(in CreateThreadInput, value string) CreateThreadInput {
	in.IdempotencyKey = value
	return in
}
func withCreateAttachments(in CreateThreadInput, value []AttachmentInput) CreateThreadInput {
	in.Attachments = value
	return in
}

func TestServiceCreateThreadIsTransactionalAndIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	studentID, adminID := uuid.New(), uuid.New()
	uow := newFakeUOW(adminID)
	svc := NewService(uow.readStore(), uow, func() time.Time { return now })
	actor := studentPrincipal(studentID)
	in := CreateThreadInput{Title: "  Algebra  ", Body: "  Why?  ", IdempotencyKey: strings.Repeat("c", 16)}

	thread, message, err := svc.CreateThread(context.Background(), actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if thread.StudentID != studentID || thread.Title != "Algebra" || thread.Status != StatusPending || thread.Version != 1 || message.Body != "Why?" {
		t.Fatalf("thread=%#v message=%#v", thread, message)
	}
	if len(uow.audits) != 1 || len(uow.notifications) != 1 {
		t.Fatalf("audits=%d notifications=%d", len(uow.audits), len(uow.notifications))
	}
	if got := uow.audits[0]; got.Action != "qa.thread_created" || got.TargetType != "qa_thread" || len(got.Metadata) != 2 || got.Metadata["messageCount"] != "1" || got.Metadata["attachmentCount"] != "0" {
		t.Fatalf("audit = %#v", got)
	}
	if got := uow.notifications[0]; got.RecipientUserID != adminID || got.TargetID != thread.ID || got.DedupeKey != "qa-created:"+message.ID.String() {
		t.Fatalf("notification = %#v", got)
	}

	againThread, againMessage, err := svc.CreateThread(context.Background(), actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if againThread.ID != thread.ID || againMessage.ID != message.ID || len(uow.state.threads) != 1 || len(uow.state.messages) != 1 || len(uow.audits) != 1 || len(uow.notifications) != 1 {
		t.Fatalf("duplicate thread=%s message=%s counts=%d/%d/%d/%d", againThread.ID, againMessage.ID, len(uow.state.threads), len(uow.state.messages), len(uow.audits), len(uow.notifications))
	}
}

func TestServiceStudentFollowUpReopensAndIsIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC)
	studentID := uuid.New()
	uow := newFakeUOW(uuid.New())
	completed := now.Add(-time.Hour)
	thread := Thread{ID: uuid.New(), StudentID: studentID, Title: "Question", Status: StatusCompleted, Version: 4, LastMessageAt: completed, CreatedAt: completed, UpdatedAt: completed, CompletedAt: &completed}
	uow.state.threads[thread.ID] = thread
	svc := NewService(uow.readStore(), uow, func() time.Time { return now })
	in := AddMessageInput{ThreadID: thread.ID, Body: "  more detail  ", IdempotencyKey: strings.Repeat("f", 16)}

	updated, message, err := svc.AddStudentMessage(context.Background(), studentPrincipal(studentID), in)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusPending || updated.Version != 5 || updated.CompletedAt != nil || !updated.LastMessageAt.Equal(now) || message.Body != "more detail" {
		t.Fatalf("thread=%#v message=%#v", updated, message)
	}
	if len(uow.audits) != 1 || len(uow.notifications) != 1 {
		t.Fatalf("audits=%d notifications=%d", len(uow.audits), len(uow.notifications))
	}

	againThread, againMessage, err := svc.AddStudentMessage(context.Background(), studentPrincipal(studentID), in)
	if err != nil {
		t.Fatal(err)
	}
	if againThread.ID != updated.ID || againMessage.ID != message.ID || len(uow.state.messages) != 1 || len(uow.audits) != 1 || len(uow.notifications) != 1 {
		t.Fatalf("duplicate result=%#v/%#v counts=%d/%d/%d", againThread, againMessage, len(uow.state.messages), len(uow.audits), len(uow.notifications))
	}
}

func TestServiceRollbackOnAuditOrNotificationError(t *testing.T) {
	for _, failure := range []string{"audit", "notification"} {
		t.Run(failure, func(t *testing.T) {
			uow := newFakeUOW(uuid.New())
			if failure == "audit" {
				uow.auditErr = errors.New("audit failed")
			} else {
				uow.notificationErr = errors.New("notification failed")
			}
			svc := NewService(uow.readStore(), uow, time.Now)
			_, _, err := svc.CreateThread(context.Background(), studentPrincipal(uuid.New()), CreateThreadInput{Title: "Title", Body: "Body", IdempotencyKey: strings.Repeat("r", 16)})
			if err == nil {
				t.Fatal("CreateThread succeeded")
			}
			if len(uow.state.threads) != 0 || len(uow.state.messages) != 0 || len(uow.audits) != 0 || len(uow.notifications) != 0 {
				t.Fatalf("rollback left state: threads=%d messages=%d audits=%d notifications=%d", len(uow.state.threads), len(uow.state.messages), len(uow.audits), len(uow.notifications))
			}
		})
	}
}

func TestServiceRejectsIdempotencyKeyReusedAcrossStudentThreads(t *testing.T) {
	studentID := uuid.New()
	uow := newFakeUOW(uuid.New())
	first := Thread{ID: uuid.New(), StudentID: studentID, Status: StatusWaitingStudent, Version: 1}
	second := Thread{ID: uuid.New(), StudentID: studentID, Status: StatusWaitingStudent, Version: 1}
	uow.state.threads[first.ID], uow.state.threads[second.ID] = first, second
	svc := NewService(uow.readStore(), uow, time.Now)
	key := strings.Repeat("x", 16)
	if _, _, err := svc.AddStudentMessage(context.Background(), studentPrincipal(studentID), AddMessageInput{ThreadID: first.ID, Body: "first", IdempotencyKey: key}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AddStudentMessage(context.Background(), studentPrincipal(studentID), AddMessageInput{ThreadID: second.ID, Body: "second", IdempotencyKey: key}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceRejectsIdempotencyKeyReusedAcrossOperations(t *testing.T) {
	studentID := uuid.New()
	uow := newFakeUOW(uuid.New())
	svc := NewService(uow.readStore(), uow, time.Now)
	actor := studentPrincipal(studentID)
	createKey := strings.Repeat("o", 16)
	thread, _, err := svc.CreateThread(context.Background(), actor, CreateThreadInput{Title: "Title", Body: "Initial", IdempotencyKey: createKey})
	if err != nil {
		t.Fatal(err)
	}
	ready := uow.state.threads[thread.ID]
	ready.Status = StatusWaitingStudent
	uow.state.threads[thread.ID] = ready
	if _, _, err := svc.AddStudentMessage(context.Background(), actor, AddMessageInput{ThreadID: thread.ID, Body: "Follow", IdempotencyKey: createKey}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("create key reused for follow-up error=%v", err)
	}
	followKey := strings.Repeat("p", 16)
	if _, _, err := svc.AddStudentMessage(context.Background(), actor, AddMessageInput{ThreadID: thread.ID, Body: "Follow", IdempotencyKey: followKey}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateThread(context.Background(), actor, CreateThreadInput{Title: "Another", Body: "Another", IdempotencyKey: followKey}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("follow-up key reused for create error=%v", err)
	}
}

type fakeState struct {
	threads     map[uuid.UUID]Thread
	messages    map[uuid.UUID]Message
	idempotency map[string]uuid.UUID
	first       map[uuid.UUID]uuid.UUID
}

func newFakeState() *fakeState {
	return &fakeState{threads: map[uuid.UUID]Thread{}, messages: map[uuid.UUID]Message{}, idempotency: map[string]uuid.UUID{}, first: map[uuid.UUID]uuid.UUID{}}
}

func (s *fakeState) clone() *fakeState {
	copy := newFakeState()
	for id, thread := range s.threads {
		copy.threads[id] = thread
	}
	for id, message := range s.messages {
		copy.messages[id] = message
	}
	for key, id := range s.idempotency {
		copy.idempotency[key] = id
	}
	for threadID, messageID := range s.first {
		copy.first[threadID] = messageID
	}
	return copy
}

type fakeUOW struct {
	state                     *fakeState
	adminID                   uuid.UUID
	auditErr, notificationErr error
	audits                    []audit.Event
	notifications             []NotificationIntent
}

func newFakeUOW(adminID uuid.UUID) *fakeUOW { return &fakeUOW{state: newFakeState(), adminID: adminID} }
func (u *fakeUOW) readStore() Store         { return &fakeStore{state: u.state, adminID: u.adminID} }
func (u *fakeUOW) WithinTx(ctx context.Context, fn func(TxStore, audit.Writer, NotificationWriter) error) error {
	state := u.state.clone()
	writer := &fakeAuditWriter{err: u.auditErr}
	notifier := &fakeNotificationWriter{err: u.notificationErr}
	if err := fn(&fakeStore{state: state, adminID: u.adminID}, writer, notifier); err != nil {
		return err
	}
	u.state = state
	u.audits = append(u.audits, writer.events...)
	u.notifications = append(u.notifications, notifier.intents...)
	return nil
}

type fakeAuditWriter struct {
	events []audit.Event
	err    error
}

func (w *fakeAuditWriter) Write(_ context.Context, event audit.Event) error {
	if w.err != nil {
		return w.err
	}
	w.events = append(w.events, event)
	return nil
}

type fakeNotificationWriter struct {
	intents []NotificationIntent
	err     error
}

func (w *fakeNotificationWriter) Notify(_ context.Context, intent NotificationIntent) error {
	if w.err != nil {
		return w.err
	}
	w.intents = append(w.intents, intent)
	return nil
}

type fakeStore struct {
	state   *fakeState
	adminID uuid.UUID
}

func (s *fakeStore) ListStudentThreads(context.Context, uuid.UUID, Status, ThreadCursor) ([]Thread, ThreadCursor, error) {
	return nil, ThreadCursor{}, nil
}
func (s *fakeStore) GetStudentThread(_ context.Context, studentID, threadID uuid.UUID) (Thread, error) {
	thread, ok := s.state.threads[threadID]
	if !ok || thread.StudentID != studentID {
		return Thread{}, ErrNotFound
	}
	return thread, nil
}
func (s *fakeStore) ListStudentMessages(context.Context, uuid.UUID, uuid.UUID, MessageCursor) ([]Message, MessageCursor, error) {
	return nil, MessageCursor{}, nil
}
func (s *fakeStore) FindMessageByIdempotency(_ context.Context, senderID uuid.UUID, key string) (Thread, Message, bool, error) {
	messageID, ok := s.state.idempotency[senderID.String()+"/"+key]
	if !ok {
		return Thread{}, Message{}, false, ErrNotFound
	}
	message := s.state.messages[messageID]
	return s.state.threads[message.ThreadID], message, s.state.first[message.ThreadID] == message.ID, nil
}
func (s *fakeStore) CreateThreadWithFirstMessage(_ context.Context, studentID uuid.UUID, in CreateThreadInput, now time.Time) (Thread, Message, bool, error) {
	if thread, message, first, err := s.FindMessageByIdempotency(context.Background(), studentID, in.IdempotencyKey); err == nil {
		if !first {
			return Thread{}, Message{}, false, ErrIdempotencyConflict
		}
		return thread, message, false, nil
	}
	thread := Thread{ID: uuid.New(), StudentID: studentID, Title: in.Title, Status: StatusPending, Version: 1, LastMessageAt: now, CreatedAt: now, UpdatedAt: now}
	message := Message{ID: uuid.New(), ThreadID: thread.ID, SenderUserID: studentID, SenderRole: auth.RoleStudent, Body: in.Body, CreatedAt: now}
	s.state.threads[thread.ID], s.state.messages[message.ID] = thread, message
	s.state.idempotency[studentID.String()+"/"+in.IdempotencyKey] = message.ID
	s.state.first[thread.ID] = message.ID
	return thread, message, true, nil
}
func (s *fakeStore) LockStudentThread(_ context.Context, studentID, threadID uuid.UUID) (Thread, error) {
	return s.GetStudentThread(context.Background(), studentID, threadID)
}
func (s *fakeStore) AppendStudentMessage(_ context.Context, thread Thread, studentID uuid.UUID, in AddMessageInput, next Status, now time.Time) (Thread, Message, error) {
	if !now.After(thread.LastMessageAt) {
		now = thread.LastMessageAt.Add(time.Microsecond)
	}
	thread.Status, thread.Version, thread.LastMessageAt, thread.UpdatedAt, thread.CompletedAt = next, thread.Version+1, now, now, nil
	message := Message{ID: uuid.New(), ThreadID: thread.ID, SenderUserID: studentID, SenderRole: auth.RoleStudent, Body: in.Body, CreatedAt: now}
	s.state.threads[thread.ID], s.state.messages[message.ID] = thread, message
	s.state.idempotency[studentID.String()+"/"+in.IdempotencyKey] = message.ID
	return thread, message, nil
}
func (s *fakeStore) ActiveAdminID(context.Context) (uuid.UUID, error) { return s.adminID, nil }

func studentPrincipal(id uuid.UUID) Principal {
	return Principal{User: auth.User{ID: id, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "req-1", IP: net.ParseIP("127.0.0.1")}
}
