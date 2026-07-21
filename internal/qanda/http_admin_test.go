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
	reply        AddAdminMessageInput
	change       ChangeStatusInput
	note         AddTeacherNoteInput
	detailCursor MessageCursor
	err          error
	lists        int
}

func (s *fakeAdminHTTPService) ListAdminThreads(context.Context, Principal, AdminThreadFilter, ThreadCursor) ([]Thread, ThreadCursor, error) {
	s.lists++
	return []Thread{studentHTTPThreadValue()}, ThreadCursor{}, s.err
}
func (s *fakeAdminHTTPService) GetAdminThread(_ context.Context, _ Principal, _ uuid.UUID, cursor MessageCursor) (AdminThreadDetail, error) {
	s.detailCursor = cursor
	return AdminThreadDetail{Thread: studentHTTPThreadValue(), Notes: []TeacherNote{{ID: uuid.New(), Body: "private note"}}, NextMessageCursor: MessageCursor{CreatedAt: studentHTTPTime, ID: studentHTTPMessage, Limit: cursor.Limit}}, s.err
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

func TestAdminHTTPDetailAcceptsOnlyCanonicalMessagePagination(t *testing.T) {
	admin := auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}
	cursor := encodeMessageCursor(MessageCursor{CreatedAt: studentHTTPTime, ID: studentHTTPMessage})
	for _, tc := range []struct {
		name, query string
		want        int
	}{
		{"default", "", http.StatusOK}, {"valid", "?cursor=" + cursor + "&limit=100", http.StatusOK},
		{"empty cursor", "?cursor=", http.StatusBadRequest}, {"duplicate cursor", "?cursor=" + cursor + "&cursor=" + cursor, http.StatusBadRequest},
		{"non canonical cursor", "?cursor=" + cursor + "%3D", http.StatusBadRequest}, {"empty limit", "?limit=", http.StatusBadRequest},
		{"limit zero", "?limit=0", http.StatusBadRequest}, {"limit high", "?limit=101", http.StatusBadRequest}, {"unknown", "?status=pending", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeAdminHTTPService{}
			w := httptest.NewRecorder()
			NewAdminHandler(svc).Routes().ServeHTTP(w, studentHTTPRequest(http.MethodGet, "/"+studentHTTPThread.String()+tc.query, "", admin))
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.name == "default" && svc.detailCursor.Limit != 100 {
				t.Fatalf("default cursor=%#v", svc.detailCursor)
			}
			if tc.name == "valid" && (svc.detailCursor.Limit != 100 || svc.detailCursor.ID != studentHTTPMessage) {
				t.Fatalf("cursor=%#v", svc.detailCursor)
			}
			if tc.want == http.StatusOK && !strings.Contains(w.Body.String(), `"nextMessageCursor":"`) {
				t.Fatalf("missing continuation: %s", w.Body.String())
			}
		})
	}
}

func TestAdminHTTPWritesRejectQueriesAndUseStableIdempotencyCodes(t *testing.T) {
	admin := auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}
	for _, tc := range []struct {
		name, path, body string
		headers          []string
		want             int
		code             string
	}{
		{"missing key", "/" + studentHTTPThread.String() + "/messages", `{"expectedVersion":4,"body":"answer"}`, nil, http.StatusBadRequest, "idempotency_key_required"},
		{"invalid key", "/" + studentHTTPThread.String() + "/messages", `{"expectedVersion":4,"body":"answer"}`, []string{"short"}, http.StatusBadRequest, "invalid_idempotency_key"},
		{"duplicate key", "/" + studentHTTPThread.String() + "/messages", `{"expectedVersion":4,"body":"answer"}`, []string{strings.Repeat("a", 16), strings.Repeat("b", 16)}, http.StatusBadRequest, "invalid_idempotency_key"},
		{"reply query", "/" + studentHTTPThread.String() + "/messages?x=1", `{"expectedVersion":4,"body":"answer"}`, []string{strings.Repeat("a", 16)}, http.StatusBadRequest, "invalid_request"},
		{"status query", "/" + studentHTTPThread.String() + "/status?x=1", `{"expectedVersion":4,"status":"completed"}`, nil, http.StatusBadRequest, "invalid_request"},
		{"note query", "/" + studentHTTPThread.String() + "/notes?x=1", `{"body":"private"}`, nil, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeAdminHTTPService{}
			r := studentHTTPRequest(http.MethodPost, tc.path, tc.body, admin)
			r.Header.Set("Content-Type", "application/json")
			for _, h := range tc.headers {
				r.Header.Add("Idempotency-Key", h)
			}
			w := httptest.NewRecorder()
			NewAdminHandler(svc).Routes().ServeHTTP(w, r)
			if w.Code != tc.want || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
