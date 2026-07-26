package aiqa

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type studentHTTPStub struct {
	principal Principal
	create    CreateThreadInput
	add       AddMessageInput
	retryKey  string
	cancelled int
	err       error
}

func (s *studentHTTPStub) CreateThread(_ context.Context, p Principal, in CreateThreadInput) (ThreadDetail, Run, error) {
	s.principal, s.create = p, in
	return studentHTTPDetail(), studentHTTPRun(), s.err
}
func (s *studentHTTPStub) ListThreads(context.Context, Principal, ThreadCursor) ([]Thread, ThreadCursor, error) {
	return []Thread{studentHTTPDetail().Thread}, ThreadCursor{}, s.err
}
func (s *studentHTTPStub) GetThread(context.Context, Principal, uuid.UUID, MessageCursor) (ThreadDetail, error) {
	return studentHTTPDetail(), s.err
}
func (s *studentHTTPStub) AddMessage(_ context.Context, p Principal, in AddMessageInput) (ThreadDetail, Run, error) {
	s.principal, s.add = p, in
	return studentHTTPDetail(), studentHTTPRun(), s.err
}
func (s *studentHTTPStub) CancelRun(context.Context, Principal, uuid.UUID) (Run, error) {
	s.cancelled++
	return studentHTTPRun(), s.err
}
func (s *studentHTTPStub) RetryRun(_ context.Context, _ Principal, _ uuid.UUID, key string) (Run, error) {
	s.retryKey = key
	return studentHTTPRun(), s.err
}

var (
	studentHTTPThreadID  = uuid.MustParse("10000000-0000-4000-8000-000000000001")
	studentHTTPMessageID = uuid.MustParse("10000000-0000-4000-8000-000000000002")
	studentHTTPRunID     = uuid.MustParse("10000000-0000-4000-8000-000000000003")
	studentHTTPFileID    = uuid.MustParse("10000000-0000-4000-8000-000000000004")
	studentHTTPUserID    = uuid.MustParse("10000000-0000-4000-8000-000000000005")
	studentHTTPTime      = time.Date(2026, 7, 27, 1, 2, 3, 0, time.UTC)
)

func studentHTTPDetail() ThreadDetail {
	return ThreadDetail{
		Thread: Thread{ID: studentHTTPThreadID, StudentID: studentHTTPUserID, Title: "代数", Subject: SubjectMath, LastMessageAt: studentHTTPTime, CreatedAt: studentHTTPTime},
		Messages: []Message{{ID: studentHTTPMessageID, ThreadID: studentHTTPThreadID, Role: "student", Body: "解方程", Attachments: []AttachmentMetadata{{
			FileVersionID: studentHTTPFileID, DisplayName: "question.png", DetectedMIME: "image/png", Size: 42, Modality: ModalityVision,
		}}, CreatedAt: studentHTTPTime}},
	}
}
func studentHTTPRun() Run {
	return Run{ID: studentHTTPRunID, ThreadID: studentHTTPThreadID, TriggerMessageID: studentHTTPMessageID, Status: RunQueued, AttemptNo: 1, CreatedAt: studentHTTPTime, UpdatedAt: studentHTTPTime}
}

func studentHTTPRequest(t *testing.T, h http.Handler, method, path, body string, headers map[string]string, role auth.Role) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: studentHTTPUserID, Role: role, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestStudentHTTPCreateUsesStrictPrivateDTO(t *testing.T) {
	service := &studentHTTPStub{}
	h := NewStudentHandler(service, nil).Routes()
	body := `{"title":"代数","subject":"math","body":"解方程","attachments":[{"fileVersionId":"10000000-0000-4000-8000-000000000004","sortPosition":0}]}`
	w := studentHTTPRequest(t, h, http.MethodPost, "/threads", body, map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": "1234567890abcdef",
	}, auth.RoleStudent)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if service.create.Subject != SubjectMath || service.create.IdempotencyKey != "1234567890abcdef" || len(service.create.Attachments) != 1 {
		t.Fatalf("input=%+v", service.create)
	}
	var decoded map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	raw := w.Body.String()
	for _, forbidden := range []string{"studentId", "provider", "baseUrl", "model", "upstream", "objectKey", "reservedTokenCount", "triggerBody"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("private field %q leaked: %s", forbidden, raw)
		}
	}
	for _, required := range []string{`"thread"`, `"message"`, `"run"`, `"eventsUrl":"/api/v1/student/ai/runs/10000000-0000-4000-8000-000000000003/events"`} {
		if !strings.Contains(raw, required) {
			t.Fatalf("missing %s in %s", required, raw)
		}
	}
}

func TestStudentHTTPRejectsMalformedMutationsBeforeService(t *testing.T) {
	tests := []struct {
		name, path, body string
		headers          map[string]string
		want             int
	}{
		{"content type missing", "/threads", `{}`, map[string]string{"Idempotency-Key": "1234567890abcdef"}, http.StatusUnsupportedMediaType},
		{"duplicate content type", "/threads", `{}`, map[string]string{"Content-Type": "application/json, application/json", "Idempotency-Key": "1234567890abcdef"}, http.StatusUnsupportedMediaType},
		{"unknown field", "/threads", `{"title":"t","subject":"math","body":"b","attachments":[],"secret":"x"}`, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "1234567890abcdef"}, http.StatusBadRequest},
		{"invalid utf8", "/threads", string([]byte("{\"title\":\"t\",\"subject\":\"math\",\"body\":\"\xff\",\"attachments\":[]}")), map[string]string{"Content-Type": "application/json", "Idempotency-Key": "1234567890abcdef"}, http.StatusBadRequest},
		{"missing idempotency", "/threads", `{"title":"t","subject":"math","body":"b","attachments":[]}`, map[string]string{"Content-Type": "application/json"}, http.StatusBadRequest},
		{"duplicate idempotency", "/threads", `{"title":"t","subject":"math","body":"b","attachments":[]}`, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "a, b"}, http.StatusBadRequest},
		{"noncanonical attachment UUID", "/threads", `{"title":"t","subject":"math","body":"b","attachments":[{"fileVersionId":"10000000-0000-4000-8000-00000000000A","sortPosition":0}]}`, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "1234567890abcdef"}, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := &studentHTTPStub{}
			h := NewStudentHandler(service, nil).Routes()
			w := studentHTTPRequest(t, h, http.MethodPost, tc.path, tc.body, tc.headers, auth.RoleStudent)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if service.create.IdempotencyKey != "" {
				t.Fatal("service called")
			}
			if !strings.Contains(w.Body.String(), `"requestId"`) {
				t.Fatalf("missing request ID: %s", w.Body)
			}
		})
	}
}

func TestStudentHTTPEnforcesTwentyThousandRuneBodyAndRole(t *testing.T) {
	for _, tc := range []struct {
		name string
		role auth.Role
		body string
		want int
	}{
		{"maximum accepted", auth.RoleStudent, strings.Repeat("界", 20000), http.StatusCreated},
		{"over maximum", auth.RoleStudent, strings.Repeat("界", 20001), http.StatusBadRequest},
		{"Unicode whitespace counts toward maximum", auth.RoleStudent, "\u3000" + strings.Repeat("界", 20000), http.StatusBadRequest},
		{"Unicode whitespace within maximum", auth.RoleStudent, "\u3000" + strings.Repeat("界", 19998) + "\u2003", http.StatusCreated},
		{"Unicode whitespace only is empty", auth.RoleStudent, "\u3000\u2003", http.StatusBadRequest},
		{"admin isolated", auth.RoleAdmin, "ok", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &studentHTTPStub{}
			h := NewStudentHandler(service, nil).Routes()
			payload, _ := json.Marshal(map[string]any{"title": "t", "subject": "math", "body": tc.body, "attachments": []any{}})
			w := studentHTTPRequest(t, h, http.MethodPost, "/threads", string(payload), map[string]string{
				"Content-Type": "application/json", "Idempotency-Key": "1234567890abcdef",
			}, tc.role)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestStudentHTTPRejectsMoreThanTwentyAttachmentsBeforeService(t *testing.T) {
	attachments := make([]map[string]any, MaxAIAttachments+1)
	for i := range attachments {
		attachments[i] = map[string]any{"fileVersionId": uuid.NewString(), "sortPosition": i}
	}
	payload, _ := json.Marshal(map[string]any{"title": "t", "subject": "math", "body": "b", "attachments": attachments})
	service := &studentHTTPStub{}
	w := studentHTTPRequest(t, NewStudentHandler(service, nil).Routes(), http.MethodPost, "/threads", string(payload), map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": "1234567890abcdef",
	}, auth.RoleStudent)
	if w.Code != http.StatusBadRequest || service.create.IdempotencyKey != "" {
		t.Fatalf("status=%d called=%+v body=%s", w.Code, service.create, w.Body)
	}
}

func TestStudentHTTPCancelAndRetryRequireExactEmptyJSON(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		headers          map[string]string
		want             int
	}{
		{"cancel", "/runs/" + studentHTTPRunID.String() + "/cancel", `{}`, map[string]string{"Content-Type": "application/json"}, http.StatusOK},
		{"cancel unknown", "/runs/" + studentHTTPRunID.String() + "/cancel", `{"reason":"x"}`, map[string]string{"Content-Type": "application/json"}, http.StatusBadRequest},
		{"retry", "/runs/" + studentHTTPRunID.String() + "/retries", `{}`, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "1234567890abcdef"}, http.StatusCreated},
		{"retry body missing", "/runs/" + studentHTTPRunID.String() + "/retries", ``, map[string]string{"Content-Type": "application/json", "Idempotency-Key": "1234567890abcdef"}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &studentHTTPStub{}
			w := studentHTTPRequest(t, NewStudentHandler(service, nil).Routes(), http.MethodPost, tc.path, tc.body, tc.headers, auth.RoleStudent)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestStudentHTTPRejectsExplicitEmptyOrNoncanonicalListParameters(t *testing.T) {
	for _, path := range []string{
		"/threads?limit=",
		"/threads?cursor=",
		"/threads?limit=01",
		"/threads/" + studentHTTPThreadID.String() + "?cursor=",
	} {
		service := &studentHTTPStub{}
		w := studentHTTPRequest(t, NewStudentHandler(service, nil).Routes(), http.MethodGet, path, "", nil, auth.RoleStudent)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body)
		}
	}
}

func TestStudentHTTPMapsStableServiceErrors(t *testing.T) {
	for err, code := range map[error]string{
		ErrAIDisabled: "AI_DISABLED", ErrQuotaExceeded: "QUOTA_EXCEEDED", ErrAIBusy: "AI_BUSY",
		ErrAttachmentNotReady: "ATTACHMENT_NOT_READY", ErrContextTooLarge: "CONTEXT_TOO_LARGE",
		ErrProviderUnavailable: "PROVIDER_UNAVAILABLE", ErrRunConflict: "RUN_CONFLICT", ErrNotFound: "not_found",
	} {
		service := &studentHTTPStub{err: err}
		h := NewStudentHandler(service, nil).Routes()
		w := studentHTTPRequest(t, h, http.MethodGet, "/threads/"+studentHTTPThreadID.String(), "", nil, auth.RoleStudent)
		if !strings.Contains(w.Body.String(), `"code":"`+code+`"`) || !strings.Contains(w.Body.String(), `"requestId"`) {
			t.Fatalf("err=%v status=%d body=%s", err, w.Code, w.Body)
		}
	}
}
