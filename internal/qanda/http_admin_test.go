package qanda

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

var _ struct {
	Thread            threadDTO    `json:"thread"`
	Messages          []messageDTO `json:"messages"`
	NextMessageCursor string       `json:"nextMessageCursor,omitempty"`
} = threadDetailDTO{}

type fakeAdminHTTPService struct {
	reply  AddAdminMessageInput
	change ChangeStatusInput
	note   AddTeacherNoteInput
	err    error
	lists  int
}

func (s *fakeAdminHTTPService) ListAdminThreads(context.Context, Principal, AdminThreadFilter, ThreadCursor) ([]Thread, ThreadCursor, error) {
	s.lists++
	return []Thread{studentHTTPThreadValue()}, ThreadCursor{}, s.err
}
func (s *fakeAdminHTTPService) GetAdminThread(context.Context, Principal, uuid.UUID) (AdminThreadDetail, error) {
	return AdminThreadDetail{Thread: studentHTTPThreadValue(), Notes: []TeacherNote{{ID: uuid.New(), Body: "private note"}}}, s.err
}
func (s *fakeAdminHTTPService) AddAdminMessage(_ context.Context, _ Principal, in AddAdminMessageInput) (Thread, Message, error) {
	s.reply = in
	return studentHTTPThreadValue(), studentHTTPMessageValue(), s.err
}
func (s *fakeAdminHTTPService) ChangeStatus(_ context.Context, _ Principal, in ChangeStatusInput) (Thread, error) {
	s.change = in
	return studentHTTPThreadValue(), s.err
}
func (s *fakeAdminHTTPService) AddTeacherNote(_ context.Context, _ Principal, in AddTeacherNoteInput) (TeacherNote, error) {
	s.note = in
	return TeacherNote{ID: uuid.New(), ThreadID: in.ThreadID, Body: in.Body}, s.err
}

func TestAdminHTTPStrictWritesAndConflictCodes(t *testing.T) {
	admin := auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}
	for _, tc := range []struct {
		name, method, path, body, key string
		svcErr                        error
		want                          int
		code                          string
	}{
		{"reply", http.MethodPost, "/" + studentHTTPThread.String() + "/messages", `{"expectedVersion":4,"body":"answer"}`, strings.Repeat("r", 16), nil, http.StatusCreated, ""},
		{"status", http.MethodPost, "/" + studentHTTPThread.String() + "/status", `{"expectedVersion":4,"status":"completed"}`, "", nil, http.StatusOK, ""},
		{"note", http.MethodPost, "/" + studentHTTPThread.String() + "/notes", `{"body":"private"}`, "", nil, http.StatusCreated, ""},
		{"unknown", http.MethodPost, "/" + studentHTTPThread.String() + "/notes", `{"body":"private","extra":1}`, "", nil, http.StatusBadRequest, "invalid_request"},
		{"conflict", http.MethodPost, "/" + studentHTTPThread.String() + "/status", `{"expectedVersion":4,"status":"completed"}`, "", ErrThreadConflict, http.StatusConflict, "thread_conflict"},
		{"transition", http.MethodPost, "/" + studentHTTPThread.String() + "/status", `{"expectedVersion":4,"status":"pending"}`, "", ErrInvalidStatusTransition, http.StatusConflict, "invalid_status_transition"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeAdminHTTPService{err: tc.svcErr}
			r := studentHTTPRequest(tc.method, tc.path, tc.body, admin)
			r.Header.Set("Content-Type", "application/json")
			if tc.key != "" {
				r.Header.Set("Idempotency-Key", tc.key)
			}
			w := httptest.NewRecorder()
			NewAdminHandler(svc).Routes().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("body=%s", w.Body.String())
			}
		})
	}
}

func TestAdminHTTPRoleAndStudentDTOSeparation(t *testing.T) {
	svc := &fakeAdminHTTPService{}
	w := httptest.NewRecorder()
	NewAdminHandler(svc).Routes().ServeHTTP(w, studentHTTPRequest(http.MethodGet, "/", "", auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}))
	if w.Code != http.StatusForbidden || svc.lists != 0 {
		t.Fatalf("status=%d lists=%d", w.Code, svc.lists)
	}
	raw := httptest.NewRecorder()
	httpx.JSON(raw, http.StatusOK, detailView(ThreadDetail{Thread: studentHTTPThreadValue()}))
	for _, forbidden := range []string{"notes", "noteCount", "studentId"} {
		if strings.Contains(raw.Body.String(), forbidden) {
			t.Fatalf("student dto leaked %s: %s", forbidden, raw.Body.String())
		}
	}
}
