package qanda

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/httpx"
)

var (
	studentHTTPUserID  = uuid.MustParse("10000000-0000-4000-8000-000000000001")
	studentHTTPThread  = uuid.MustParse("20000000-0000-4000-8000-000000000002")
	studentHTTPMessage = uuid.MustParse("30000000-0000-4000-8000-000000000003")
	studentHTTPFile    = uuid.MustParse("40000000-0000-4000-8000-000000000004")
	studentHTTPTime    = time.Date(2025, 7, 22, 3, 4, 5, 678901234, time.UTC)
)

type fakeStudentHTTPService struct {
	actor        Principal
	createInput  CreateThreadInput
	addInput     AddMessageInput
	status       Status
	threadCursor ThreadCursor
	messageAfter MessageCursor
	err          error
}

func (s *fakeStudentHTTPService) CreateThread(_ context.Context, actor Principal, in CreateThreadInput) (Thread, Message, error) {
	s.actor, s.createInput = actor, in
	return studentHTTPThreadValue(), studentHTTPMessageValue(), s.err
}
func (s *fakeStudentHTTPService) ListStudentThreads(_ context.Context, actor Principal, status Status, cursor ThreadCursor) ([]Thread, ThreadCursor, error) {
	s.actor, s.status, s.threadCursor = actor, status, cursor
	return []Thread{studentHTTPThreadValue()}, ThreadCursor{LastMessageAt: studentHTTPTime, ID: studentHTTPThread, Limit: cursor.Limit}, s.err
}
func (s *fakeStudentHTTPService) GetStudentThread(_ context.Context, actor Principal, _ uuid.UUID) (ThreadDetail, error) {
	s.actor = actor
	return ThreadDetail{
		Thread: studentHTTPThreadValue(), Messages: []Message{studentHTTPMessageValue()},
		NextMessageCursor: MessageCursor{CreatedAt: studentHTTPTime, ID: studentHTTPMessage, Limit: 50},
	}, s.err
}
func (s *fakeStudentHTTPService) ListStudentMessages(_ context.Context, actor Principal, _ uuid.UUID, cursor MessageCursor) ([]Message, MessageCursor, error) {
	s.actor, s.messageAfter = actor, cursor
	return []Message{studentHTTPMessageValue()}, MessageCursor{CreatedAt: studentHTTPTime, ID: studentHTTPMessage, Limit: cursor.Limit}, s.err
}
func (s *fakeStudentHTTPService) AddStudentMessage(_ context.Context, actor Principal, in AddMessageInput) (Thread, Message, error) {
	s.actor, s.addInput = actor, in
	return studentHTTPThreadValue(), studentHTTPMessageValue(), s.err
}

func studentHTTPThreadValue() Thread {
	return Thread{ID: studentHTTPThread, StudentID: studentHTTPUserID, Title: "Private title", Status: StatusPending, Version: 2, LastMessageAt: studentHTTPTime, CreatedAt: studentHTTPTime.Add(-time.Hour), UpdatedAt: studentHTTPTime}
}
func studentHTTPMessageValue() Message {
	return Message{ID: studentHTTPMessage, ThreadID: studentHTTPThread, SenderUserID: studentHTTPUserID, SenderRole: auth.RoleStudent, Kind: MessageKindInitial, Body: "Private body", CreatedAt: studentHTTPTime, Attachments: []Attachment{{FileVersionID: studentHTTPFile, SortPosition: 0, DisplayName: "notes.txt"}}}
}

func TestStudentHTTPCreateStrictJSONAndIdempotencyContract(t *testing.T) {
	validBody := `{"title":"Question","body":"Please help","attachments":[{"fileVersionId":"` + studentHTTPFile.String() + `","sortPosition":0}]}`
	tests := []struct {
		name        string
		body        string
		contentType []string
		keys        []string
		wantStatus  int
		wantCode    string
	}{
		{name: "missing content type", body: validBody, keys: []string{"1234567890abcdef"}, wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type"},
		{name: "duplicate content type", body: validBody, contentType: []string{"application/json", "application/json"}, keys: []string{"1234567890abcdef"}, wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type"},
		{name: "wrong content type", body: validBody, contentType: []string{"text/plain"}, keys: []string{"1234567890abcdef"}, wantStatus: http.StatusUnsupportedMediaType, wantCode: "unsupported_media_type"},
		{name: "unknown field", body: `{"title":"Question","body":"Please help","unknown":true}`, contentType: []string{"application/json"}, keys: []string{"1234567890abcdef"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "two objects", body: validBody + `{}`, contentType: []string{"application/json"}, keys: []string{"1234567890abcdef"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "body cap", body: `{"title":"Question","body":"` + strings.Repeat("x", 64*1024) + `"}`, contentType: []string{"application/json"}, keys: []string{"1234567890abcdef"}, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "request_too_large"},
		{name: "body cap after valid object", body: validBody + strings.Repeat(" ", 64*1024), contentType: []string{"application/json"}, keys: []string{"1234567890abcdef"}, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "request_too_large"},
		{name: "missing idempotency", body: validBody, contentType: []string{"application/json"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "duplicate idempotency", body: validBody, contentType: []string{"application/json"}, keys: []string{"1234567890abcdef", "fedcba0987654321"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "valid", body: validBody, contentType: []string{"application/json; charset=utf-8"}, keys: []string{"1234567890abcdef"}, wantStatus: http.StatusCreated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeStudentHTTPService{}
			r := studentHTTPRequest(http.MethodPost, "/", tc.body, auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive})
			for _, value := range tc.contentType {
				r.Header.Add("Content-Type", value)
			}
			for _, value := range tc.keys {
				r.Header.Add("Idempotency-Key", value)
			}
			w := httptest.NewRecorder()
			studentHTTPHandler(svc).ServeHTTP(w, r)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store, private" {
				t.Fatalf("cache=%q", w.Header().Get("Cache-Control"))
			}
			if tc.wantCode != "" && (!strings.Contains(w.Body.String(), `"code":"`+tc.wantCode+`"`) || !strings.Contains(w.Body.String(), `"requestId":"request-test-1"`)) {
				t.Fatalf("body=%s", w.Body.String())
			}
			if tc.wantStatus == http.StatusCreated {
				if svc.createInput.IdempotencyKey != "1234567890abcdef" || len(svc.createInput.Attachments) != 1 || svc.createInput.Attachments[0].FileVersionID != studentHTTPFile {
					t.Fatalf("input=%#v", svc.createInput)
				}
				for _, forbidden := range []string{"studentId", "senderUserId", "threadId", "objectKey", "bucket"} {
					if strings.Contains(w.Body.String(), forbidden) {
						t.Fatalf("response leaks %q: %s", forbidden, w.Body.String())
					}
				}
			}
		})
	}
}

func TestStudentHTTPListValidatesFiltersLimitsAndCanonicalCursor(t *testing.T) {
	validCursor := encodeThreadCursor(ThreadCursor{LastMessageAt: studentHTTPTime, ID: studentHTTPThread, Limit: 50})
	for _, tc := range []struct {
		name, query string
		wantStatus  int
	}{
		{"valid", "?status=waiting_student&limit=50&cursor=" + validCursor, http.StatusOK},
		{"invalid status", "?status=unknown", http.StatusBadRequest},
		{"duplicate status", "?status=pending&status=completed", http.StatusBadRequest},
		{"empty status", "?status=", http.StatusBadRequest},
		{"empty cursor", "?cursor=", http.StatusBadRequest},
		{"empty limit", "?limit=", http.StatusBadRequest},
		{"zero limit", "?limit=0", http.StatusBadRequest},
		{"high limit", "?limit=51", http.StatusBadRequest},
		{"unknown query", "?studentId=" + studentHTTPUserID.String(), http.StatusBadRequest},
		{"padded cursor", "?cursor=" + validCursor + "%3D", http.StatusBadRequest},
		{"cursor unknown field", "?cursor=" + rawCursor(`{"lastMessageAt":"2026-07-22T03:04:05Z","id":"`+studentHTTPThread.String()+`","extra":true}`), http.StatusBadRequest},
		{"cursor zero uuid", "?cursor=" + rawCursor(`{"lastMessageAt":"2026-07-22T03:04:05Z","id":"00000000-0000-0000-0000-000000000000"}`), http.StatusBadRequest},
		{"cursor future time", "?cursor=" + rawCursor(`{"lastMessageAt":"2999-07-22T03:04:05Z","id":"`+studentHTTPThread.String()+`"}`), http.StatusBadRequest},
		{"cursor non canonical time", "?cursor=" + rawCursor(`{"lastMessageAt":"2026-07-22T03:04:05.000000000Z","id":"`+studentHTTPThread.String()+`"}`), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeStudentHTTPService{}
			w := httptest.NewRecorder()
			studentHTTPHandler(svc).ServeHTTP(w, studentHTTPRequest(http.MethodGet, "/"+tc.query, "", auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				if svc.status != StatusWaitingStudent || svc.threadCursor.Limit != 50 || svc.threadCursor.ID != studentHTTPThread || !svc.threadCursor.LastMessageAt.Equal(studentHTTPTime) {
					t.Fatalf("status=%q cursor=%#v", svc.status, svc.threadCursor)
				}
				if !strings.Contains(w.Body.String(), `"nextCursor":"`) {
					t.Fatalf("missing next cursor: %s", w.Body.String())
				}
			}
		})
	}
}

func TestStudentHTTPDetailRepresentsMessageContinuationWithoutPrivateIdentifiers(t *testing.T) {
	svc := &fakeStudentHTTPService{}
	w := httptest.NewRecorder()
	studentHTTPHandler(svc).ServeHTTP(w, studentHTTPRequest(http.MethodGet, "/"+studentHTTPThread.String(), "", auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"nextMessageCursor":"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"fileVersionId":"`+studentHTTPFile.String()+`"`) || !strings.Contains(w.Body.String(), `"displayName":"notes.txt"`) {
		t.Fatalf("missing safe attachment view: %s", w.Body.String())
	}
	for _, forbidden := range []string{"studentId", "senderUserId", "threadId", "objectKey", "bucket"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("response leaks %q: %s", forbidden, w.Body.String())
		}
	}
}

func TestStudentHTTPMessagesAndPathValidation(t *testing.T) {
	validCursor := encodeMessageCursor(MessageCursor{CreatedAt: studentHTTPTime, ID: studentHTTPMessage, Limit: 50})
	for _, tc := range []struct {
		name, path string
		wantStatus int
	}{
		{"valid", "/" + studentHTTPThread.String() + "/messages?cursor=" + validCursor + "&limit=50", http.StatusOK},
		{"malformed thread", "/not-a-uuid/messages", http.StatusBadRequest},
		{"zero thread", "/00000000-0000-0000-0000-000000000000/messages", http.StatusBadRequest},
		{"high limit", "/" + studentHTTPThread.String() + "/messages?limit=51", http.StatusBadRequest},
		{"empty cursor", "/" + studentHTTPThread.String() + "/messages?cursor=", http.StatusBadRequest},
		{"empty limit", "/" + studentHTTPThread.String() + "/messages?limit=", http.StatusBadRequest},
		{"duplicate cursor", "/" + studentHTTPThread.String() + "/messages?cursor=" + validCursor + "&cursor=" + validCursor, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeStudentHTTPService{}
			w := httptest.NewRecorder()
			studentHTTPHandler(svc).ServeHTTP(w, studentHTTPRequest(http.MethodGet, tc.path, "", auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.wantStatus == http.StatusOK && (svc.messageAfter.ID != studentHTTPMessage || svc.messageAfter.Limit != 50) {
				t.Fatalf("cursor=%#v", svc.messageAfter)
			}
		})
	}
}

func TestStudentHTTPRouterErrorsUseStableJSONContract(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, code string
		status                   int
	}{
		{"unknown path", http.MethodGet, "/unknown/path", "not_found", http.StatusNotFound},
		{"unsupported method", http.MethodPut, "/" + studentHTTPThread.String(), "method_not_allowed", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			studentHTTPHandler(&fakeStudentHTTPService{}).ServeHTTP(w, studentHTTPRequest(tc.method, tc.path, "", auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) || !strings.Contains(w.Body.String(), `"requestId":"request-test-1"`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store, private" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("cache=%q content-type=%q", w.Header().Get("Cache-Control"), w.Header().Get("Content-Type"))
			}
		})
	}
}

func TestStudentHTTPPreservesServiceSpecificDefaultLimits(t *testing.T) {
	svc := &fakeStudentHTTPService{}
	w := httptest.NewRecorder()
	studentHTTPHandler(svc).ServeHTTP(w, studentHTTPRequest(http.MethodGet, "/", "", auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	if w.Code != http.StatusOK || svc.threadCursor.Limit != 0 {
		t.Fatalf("thread status=%d cursor=%#v body=%s", w.Code, svc.threadCursor, w.Body.String())
	}
	w = httptest.NewRecorder()
	studentHTTPHandler(svc).ServeHTTP(w, studentHTTPRequest(http.MethodGet, "/"+studentHTTPThread.String()+"/messages", "", auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}))
	if w.Code != http.StatusOK || svc.messageAfter.Limit != 0 {
		t.Fatalf("message status=%d cursor=%#v body=%s", w.Code, svc.messageAfter, w.Body.String())
	}
}

func TestStudentHTTPAddMessageStrictBodyAndMapsServiceErrors(t *testing.T) {
	valid := `{"body":"Follow up","attachments":[]}`
	for _, tc := range []struct {
		name, body string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"valid", valid, nil, http.StatusCreated, ""},
		{"unknown", `{"body":"Follow up","extra":true}`, nil, http.StatusBadRequest, "invalid_request"},
		{"invalid input", valid, ErrInvalidInput, http.StatusBadRequest, "invalid_request"},
		{"idempotency conflict", valid, ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict"},
		{"not found", valid, ErrNotFound, http.StatusNotFound, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeStudentHTTPService{err: tc.err}
			r := studentHTTPRequest(http.MethodPost, "/"+studentHTTPThread.String()+"/messages", tc.body, auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive})
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "1234567890abcdef")
			w := httptest.NewRecorder()
			studentHTTPHandler(svc).ServeHTTP(w, r)
			if w.Code != tc.wantStatus || (tc.wantCode != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.wantCode+`"`)) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.wantStatus == http.StatusCreated && (svc.addInput.ThreadID != studentHTTPThread || svc.addInput.IdempotencyKey != "1234567890abcdef") {
				t.Fatalf("input=%#v", svc.addInput)
			}
		})
	}
}

func TestStudentHTTPRoleDisabledAndMissingThreadAreIndistinguishable(t *testing.T) {
	for _, tc := range []struct {
		name string
		user auth.User
		err  error
		want int
	}{
		{"admin denied", auth.User{ID: studentHTTPUserID, Role: auth.RoleAdmin, Status: auth.StatusActive}, nil, http.StatusForbidden},
		{"disabled student denied", auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusDisabled}, ErrForbidden, http.StatusForbidden},
		{"cross student hidden", auth.User{ID: studentHTTPUserID, Role: auth.RoleStudent, Status: auth.StatusActive}, ErrNotFound, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeStudentHTTPService{err: tc.err}
			w := httptest.NewRecorder()
			studentHTTPHandler(svc).ServeHTTP(w, studentHTTPRequest(http.MethodGet, "/"+studentHTTPThread.String(), "", tc.user))
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.want == http.StatusNotFound && !strings.Contains(w.Body.String(), `"code":"not_found"`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"requestId":"request-test-1"`) {
				t.Fatalf("missing request id: %s", w.Body.String())
			}
		})
	}
}

func studentHTTPHandler(service StudentHTTPService) http.Handler {
	return httpx.RequestID(NewStudentHandler(service).Routes())
}

func studentHTTPRequest(method, target, body string, user auth.User) *http.Request {
	var reader *strings.Reader
	reader = strings.NewReader(body)
	r := httptest.NewRequest(method, target, reader)
	r.RemoteAddr = "192.0.2.10:4321"
	r.Header.Set("X-Request-ID", "request-test-1")
	r = r.WithContext(auth.ContextWithUser(r.Context(), user))
	return r
}

func rawCursor(value string) string { return base64.RawURLEncoding.EncodeToString([]byte(value)) }

func TestStudentHTTPCursorEncodingUsesExactTimestampAndUUIDJSON(t *testing.T) {
	threadCursor := encodeThreadCursor(ThreadCursor{LastMessageAt: studentHTTPTime, ID: studentHTTPThread})
	decoded, err := base64.RawURLEncoding.DecodeString(threadCursor)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"lastMessageAt":%q,"id":%q}`, studentHTTPTime.Format(time.RFC3339Nano), studentHTTPThread.String())
	if string(decoded) != want {
		t.Fatalf("cursor=%s want=%s", decoded, want)
	}
}
