package files

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type aiAccessStoreStub struct {
	delivery AIDelivery
	status   AIFileStatus
	err      error
	logs     []AccessLog
}

func (s *aiAccessStoreStub) ResolveAIAccess(context.Context, Principal, uuid.UUID) (AIDelivery, error) {
	return s.delivery, s.err
}
func (s *aiAccessStoreStub) ResolveAIStatus(context.Context, Principal, uuid.UUID) (AIFileStatus, error) {
	return s.status, s.err
}
func (s *aiAccessStoreStub) WriteAccessLog(_ context.Context, log AccessLog) error {
	s.logs = append(s.logs, log)
	return nil
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

func TestAIAccessDeniedAndFailureAre404AndAudited(t *testing.T) {
	version := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	user := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	store := &aiAccessStoreStub{err: ErrNotFound}
	h := NewAIAccessHandler(NewAIAccessService(store, &accessObjectsStub{}, &accessObjectsStub{}), nil).Routes()
	r := httptest.NewRequest(http.MethodGet, "/"+version.String()+"/preview", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), user))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || len(store.logs) != 1 || store.logs[0].Result != AccessDenied {
		t.Fatalf("status=%d logs=%+v body=%s", w.Code, store.logs, w.Body)
	}
}

func TestAIAccessStatusNeverLeaksStorageCoordinates(t *testing.T) {
	version := uuid.MustParse("30000000-0000-4000-8000-000000000001")
	user := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	store := &aiAccessStoreStub{status: AIFileStatus{FileVersionID: version, ProcessingState: "ready", DetectedMIME: "image/png", Size: 10, PreviewAvailable: true}}
	h := NewAIAccessHandler(NewAIAccessService(store, &accessObjectsStub{}, &accessObjectsStub{}), nil).Routes()
	r := httptest.NewRequest(http.MethodGet, "/"+version.String()+"/status", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), user))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "object") || strings.Contains(w.Body.String(), "owner") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}
