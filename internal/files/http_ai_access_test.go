package files

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

type aiAccessStoreStub struct {
	delivery  AIDelivery
	status    AIFileStatus
	err       error
	statusErr error
	logErr    error
	logs      []AccessLog
}

func (s *aiAccessStoreStub) ResolveAIAccess(context.Context, Principal, uuid.UUID) (AIDelivery, error) {
	return s.delivery, s.err
}
func (s *aiAccessStoreStub) ResolveAIStatus(context.Context, Principal, uuid.UUID) (AIFileStatus, error) {
	if s.statusErr != nil {
		return AIFileStatus{}, s.statusErr
	}
	return s.status, s.err
}
func (s *aiAccessStoreStub) WriteAccessLog(_ context.Context, log AccessLog) error {
	s.logs = append(s.logs, log)
	return s.logErr
}

type aiSecurityAuditStub struct {
	events []audit.Event
	err    error
}

func (s *aiSecurityAuditStub) Write(_ context.Context, event audit.Event) error {
	s.events = append(s.events, event)
	return s.err
}

func TestAIAccessPreviewIsControlledAndAudited(t *testing.T) {
	version := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	message := uuid.MustParse("30000000-0000-4000-8000-000000000002")
	user := auth.User{ID: uuid.MustParse("30000000-0000-4000-8000-000000000003"), Role: auth.RoleStudent, Status: auth.StatusActive}
	store := &aiAccessStoreStub{
		delivery: AIDelivery{Delivery: Delivery{VersionID: version, ObjectKey: "private/object", DisplayName: "answer.txt", ContentType: "text/plain", Size: 5, Playable: true}, MessageID: message},
		status:   AIFileStatus{FileVersionID: version, ProcessingState: "ready", DetectedMIME: "text/plain", Size: 5, PreviewAvailable: true},
	}
	service := NewAIAccessService(store, &accessObjectsStub{data: []byte("hello")}, &accessObjectsStub{})
	h := NewAIAccessHandler(service, nil).Routes()

	r := httptest.NewRequest(http.MethodGet, "/"+version.String()+"/preview", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), user))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "hello" || w.Header().Get("Location") != "" {
		t.Fatalf("status=%d location=%q body=%q", w.Code, w.Header().Get("Location"), w.Body)
	}
	if len(store.logs) != 1 || store.logs[0].AIMessageID != message || store.logs[0].Result != AccessAllowed {
		t.Fatalf("logs=%+v", store.logs)
	}
	if strings.Contains(w.Body.String(), "private/object") {
		t.Fatal("object key leaked")
	}
}

func TestAIAccessPreviewAuthorizationErrorsAreExactlyClassifiedAndAudited(t *testing.T) {
	version := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	user := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	for _, tc := range []struct {
		name       string
		resolveErr error
		logErr     error
		wantStatus int
		wantResult AccessResult
		wantReason string
	}{
		{"owner scoped miss", ErrNotFound, nil, http.StatusNotFound, AccessDenied, "not_found"},
		{"authorization store failure", errors.New("database unavailable"), nil, http.StatusInternalServerError, AccessFailed, "storage"},
		{"audit failure fails closed", ErrNotFound, errors.New("audit unavailable"), http.StatusInternalServerError, AccessDenied, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &aiAccessStoreStub{
				delivery: AIDelivery{
					Delivery:  Delivery{VersionID: uuid.New(), ObjectKey: "must-not-persist"},
					MessageID: uuid.New(),
				},
				err: tc.resolveErr, logErr: tc.logErr,
			}
			h := NewAIAccessHandler(NewAIAccessService(store, &accessObjectsStub{}, &accessObjectsStub{}), nil).Routes()
			r := httptest.NewRequest(http.MethodGet, "/"+version.String()+"/preview", nil)
			r = r.WithContext(auth.ContextWithUser(r.Context(), user))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantStatus || len(store.logs) != 1 || store.logs[0].Result != tc.wantResult ||
				store.logs[0].Reason != tc.wantReason || store.logs[0].VersionID != uuid.Nil ||
				store.logs[0].AIMessageID != uuid.Nil {
				t.Fatalf("status=%d logs=%+v body=%s", w.Code, store.logs, w.Body)
			}
		})
	}
}

func TestAIAccessStatusNeverLeaksStorageCoordinates(t *testing.T) {
	version := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	message := uuid.MustParse("30000000-0000-4000-8000-000000000002")
	user := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	store := &aiAccessStoreStub{status: AIFileStatus{FileVersionID: version, MessageID: message, ProcessingState: "ready", DetectedMIME: "image/png", Size: 10, PreviewAvailable: true}}
	h := NewAIAccessHandler(NewAIAccessService(store, &accessObjectsStub{}, &accessObjectsStub{}), nil).Routes()
	r := httptest.NewRequest(http.MethodGet, "/"+version.String()+"/status", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), user))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "object") || strings.Contains(w.Body.String(), "owner") || strings.Contains(w.Body.String(), message.String()) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if len(store.logs) != 1 || store.logs[0].Result != AccessAllowed || store.logs[0].VersionID != version || store.logs[0].AIMessageID != message {
		t.Fatalf("status audit=%+v", store.logs)
	}
}

func TestAIAccessStatusDenialFailureAndAuditFailureAreDurablyClassified(t *testing.T) {
	version := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	user := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	for _, tc := range []struct {
		name       string
		resolveErr error
		logErr     error
		wantStatus int
		wantResult AccessResult
	}{
		{"owner scoped miss", ErrNotFound, nil, http.StatusNotFound, AccessDenied},
		{"authorization store failure", errors.New("database unavailable"), nil, http.StatusInternalServerError, AccessFailed},
		{"audit write failure", ErrNotFound, errors.New("audit unavailable"), http.StatusInternalServerError, AccessDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &aiAccessStoreStub{statusErr: tc.resolveErr, logErr: tc.logErr}
			h := NewAIAccessHandler(NewAIAccessService(store, &accessObjectsStub{}, &accessObjectsStub{}), nil).Routes()
			r := httptest.NewRequest(http.MethodGet, "/"+version.String()+"/status", nil)
			r = r.WithContext(auth.ContextWithUser(r.Context(), user))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantStatus || len(store.logs) != 1 || store.logs[0].Result != tc.wantResult ||
				store.logs[0].VersionID != uuid.Nil || store.logs[0].AIMessageID != uuid.Nil {
				t.Fatalf("status=%d logs=%+v body=%s", w.Code, store.logs, w.Body)
			}
		})
	}
}

func TestAIAccessHandlerDurablyAuditsEarlyDenialsWithoutRawInput(t *testing.T) {
	version := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	user := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	for _, tc := range []struct {
		name, path, forwarded string
		wantSecurity          bool
	}{
		{"malformed UUID", "/not-a-private-id/status", "", true},
		{"unexpected query", "/" + version.String() + "/status?secret=value", "", false},
		{"invalid forwarded IP", "/" + version.String() + "/status", "bad forwarded value", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, security := &aiAccessStoreStub{}, &aiSecurityAuditStub{}
			service := NewAIAccessService(store, &accessObjectsStub{}, &accessObjectsStub{}, security)
			handler := NewAIAccessHandler(service, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}).Routes()
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.RemoteAddr = "192.0.2.10:1234"
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			r = r.WithContext(auth.ContextWithUser(r.Context(), user))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if tc.wantSecurity {
				if len(security.events) != 1 || len(store.logs) != 0 {
					t.Fatalf("security=%+v fileLogs=%+v", security.events, store.logs)
				}
				event := security.events[0]
				encoded := event.TargetID + event.RequestID
				for key, value := range event.Metadata {
					encoded += key + value.(string)
				}
				if strings.Contains(encoded, "not-a-private-id") || strings.Contains(encoded, "secret=value") || event.TargetID != "unresolved" {
					t.Fatalf("unsafe security audit=%+v", event)
				}
			} else if len(store.logs) != 1 || store.logs[0].RequestedVersionID != version ||
				store.logs[0].VersionID != uuid.Nil || store.logs[0].AIMessageID != uuid.Nil ||
				store.logs[0].Result != AccessMalformed || store.logs[0].Reason != "policy" {
				t.Fatalf("security=%+v fileLogs=%+v", security.events, store.logs)
			}
		})
	}
}

func TestAIAccessHandlerDurablyAuditsInvalidActor(t *testing.T) {
	version := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	store, security := &aiAccessStoreStub{}, &aiSecurityAuditStub{}
	handler := NewAIAccessHandler(NewAIAccessService(store, &accessObjectsStub{}, &accessObjectsStub{}, security), nil).Routes()
	r := httptest.NewRequest(http.MethodGet, "/"+version.String()+"/status", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || len(security.events) != 0 || len(store.logs) != 1 ||
		store.logs[0].RequestedVersionID != version || store.logs[0].VersionID != uuid.Nil ||
		store.logs[0].AIMessageID != uuid.Nil || store.logs[0].Result != AccessDenied ||
		store.logs[0].Reason != "policy" {
		t.Fatalf("status=%d security=%+v fileLogs=%+v body=%s", w.Code, security.events, store.logs, w.Body)
	}
}
