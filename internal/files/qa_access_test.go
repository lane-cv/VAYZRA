package files

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/objectstore"
)

type qaAccessStoreStub struct {
	delivery QADelivery
	status   QAFileStatus
	err      error
	logs     []AccessLog
}

func (s *qaAccessStoreStub) ResolveQAAccess(context.Context, Principal, uuid.UUID, AccessAction) (QADelivery, error) {
	return s.delivery, s.err
}
func (s *qaAccessStoreStub) ResolveQAStatus(context.Context, Principal, uuid.UUID) (QAFileStatus, error) {
	return s.status, s.err
}
func (s *qaAccessStoreStub) WriteAccessLog(_ context.Context, l AccessLog) error {
	s.logs = append(s.logs, l)
	return nil
}

func TestQAAccessFailClosedAndLogsMessageTarget(t *testing.T) {
	version, message := uuid.New(), uuid.New()
	store := &qaAccessStoreStub{delivery: QADelivery{Delivery: Delivery{
		VersionID: version, ObjectKey: "opaque", DisplayName: "answer.pdf", ContentType: "application/pdf", Size: 4,
	}, MessageID: message}}
	objects := &accessObjectsStub{data: []byte("data")}
	svc := NewQAAccessService(store, objects, objects)
	actor := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "req_qa", IP: net.ParseIP("192.0.2.9")}

	opened, err := svc.Open(context.Background(), actor, QAOpenInput{VersionID: version, Action: ActionDownload})
	if err != nil {
		t.Fatal(err)
	}
	opened.Body.Close()
	if len(store.logs) != 1 || store.logs[0].QAMessageID != message || store.logs[0].Result != AccessAllowed {
		t.Fatalf("logs=%+v", store.logs)
	}

	store.err = ErrNotFound
	_, err = svc.Open(context.Background(), actor, QAOpenInput{VersionID: uuid.New(), Action: ActionDownload})
	if !errors.Is(err, ErrNotFound) || store.logs[len(store.logs)-1].Result != AccessDenied {
		t.Fatalf("err=%v logs=%+v", err, store.logs)
	}
}

func TestQAStatusDTOContainsOnlySafeFields(t *testing.T) {
	version := uuid.New()
	store := &qaAccessStoreStub{status: QAFileStatus{FileVersionID: version, ProcessingState: "ready", DetectedMIME: "image/png", Size: 12, PreviewAvailable: true}}
	svc := NewQAAccessService(store, &accessObjectsStub{}, &accessObjectsStub{})
	got, err := svc.Status(context.Background(), activeStudent(), version)
	if err != nil || got.FileVersionID != version || !got.PreviewAvailable {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

var _ objectstore.Store = (*accessObjectsStub)(nil)
