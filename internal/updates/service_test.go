package updates

import (
	"context"
	"testing"

	"happylearn.local/app/internal/audit"
)

type recordingAuditWriter struct {
	events []audit.Event
}

func (w *recordingAuditWriter) Write(_ context.Context, event audit.Event) error {
	w.events = append(w.events, event)
	return nil
}

func TestServiceApplyGuardsLegacyProtocolBeforeAudit(t *testing.T) {
	agent := &fakeAgent{status: Status{LegacyProtocol: true}}
	writer := &recordingAuditWriter{}
	service, err := NewService(agent, writer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), Principal{User: activeAdmin()}); err != ErrAgentProtocolOutdated {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(writer.events) != 0 || agent.applyCalls != 0 {
		t.Fatalf("audit events=%d apply calls=%d", len(writer.events), agent.applyCalls)
	}
}

func TestServiceRollbackGuardsCapabilityBeforeAudit(t *testing.T) {
	agent := &fakeAgent{status: Status{CanRollback: false}}
	writer := &recordingAuditWriter{}
	service, err := NewService(agent, writer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rollback(context.Background(), Principal{User: activeAdmin()}); err != ErrRollbackUnavailable {
		t.Fatalf("Rollback() error = %v", err)
	}
	if len(writer.events) != 0 || agent.rollbackCalls != 0 {
		t.Fatalf("audit events=%d rollback calls=%d", len(writer.events), agent.rollbackCalls)
	}
}
