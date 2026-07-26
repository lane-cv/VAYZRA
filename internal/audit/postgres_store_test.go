package audit

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresWriterSanitizesAndAuditRowsAreImmutable(t *testing.T) {
	pool := integration.StartPostgres(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), "TRUNCATE TABLE users CASCADE"); err != nil {
		t.Fatal(err)
	}
	actorID := uuid.New()
	if _, err := pool.Exec(context.Background(), "INSERT INTO users (id, username, display_name, role, password_hash, must_change_password) VALUES ($1, 'audit_actor', 'Audit Actor', 'admin', 'hash', false)", actorID); err != nil {
		t.Fatal(err)
	}
	w := NewPostgresWriter(pool)
	event := Event{ActorUserID: actorID, Action: "student.created", TargetType: "student", TargetID: uuid.NewString(), Metadata: map[string]any{"username": "student01", "display_name": "林同学"}, RequestID: "request-123", IP: net.ParseIP("192.0.2.4")}
	if err := w.Write(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(context.Background(), Event{Action: "student.created", TargetType: "student", TargetID: "target", Metadata: map[string]any{"password": "secret"}, RequestID: "request-123"}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unsafe event error=%v", err)
	}
	var id int64
	if err := pool.QueryRow(context.Background(), "SELECT id FROM audit_logs LIMIT 1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), "UPDATE audit_logs SET action = 'changed' WHERE id = $1", id); err == nil {
		t.Fatal("audit update unexpectedly succeeded")
	}
	if _, err := pool.Exec(context.Background(), "DELETE FROM audit_logs WHERE id = $1", id); err == nil {
		t.Fatal("audit delete unexpectedly succeeded")
	}
}

func TestQandaStudentAuditEventsAcceptOnlySafeCounts(t *testing.T) {
	base := Event{
		ActorUserID: uuid.New(), TargetType: "qa_thread", TargetID: uuid.NewString(),
		Metadata:  map[string]any{"messageCount": "1", "attachmentCount": "0"},
		RequestID: "request-qa", IP: net.ParseIP("192.0.2.5"),
	}
	for _, action := range []string{"qa.thread_created", "qa.student_followed_up"} {
		event := base
		event.Action = action
		if _, err := validateAndMarshal(event); err != nil {
			t.Fatalf("approved action %q rejected: %v", action, err)
		}
	}
	for name, mutate := range map[string]func(Event) Event{
		"title metadata": func(event Event) Event { event.Metadata = map[string]any{"title": "private"}; return event },
		"body metadata":  func(event Event) Event { event.Metadata = map[string]any{"body": "private"}; return event },
		"wrong target":   func(event Event) Event { event.TargetType = "student"; return event },
		"numeric count":  func(event Event) Event { event.Metadata = map[string]any{"messageCount": 1}; return event },
		"body under count key": func(event Event) Event {
			event.Metadata = map[string]any{"messageCount": "private question body", "attachmentCount": "0"}
			return event
		},
	} {
		t.Run(name, func(t *testing.T) {
			event := mutate(base)
			event.Action = "qa.thread_created"
			if _, err := validateAndMarshal(event); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("unsafe event error=%v", err)
			}
		})
	}
}

func TestQandaTeacherAuditEventsRejectPrivateText(t *testing.T) {
	base := Event{ActorUserID: uuid.New(), TargetType: "qa_thread", TargetID: uuid.NewString(), RequestID: "request-admin", IP: net.ParseIP("192.0.2.6")}
	for action, metadata := range map[string]map[string]any{"qa.admin_replied": {"messageCount": "1", "attachmentCount": "0", "oldStatus": "pending", "newStatus": "waiting_student"}, "qa.status_changed": {"oldStatus": "pending", "newStatus": "completed"}, "qa.teacher_note_added": {"noteCount": "1"}} {
		event := base
		event.Action = action
		event.Metadata = metadata
		if _, err := validateAndMarshal(event); err != nil {
			t.Fatalf("approved %s rejected: %v", action, err)
		}
		event.Metadata = map[string]any{"body": "private"}
		if _, err := validateAndMarshal(event); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("private %s accepted", action)
		}
	}
}

func TestAIConfigurationAuditEventsAllowOnlyRedactedMetadata(t *testing.T) {
	base := Event{ActorUserID: uuid.New(), RequestID: "request-ai", IP: net.ParseIP("192.0.2.8")}
	approved := []Event{
		{Action: "ai.provider_created", TargetType: "ai_provider", TargetID: uuid.NewString(), Metadata: map[string]any{}},
		{Action: "ai.provider_updated", TargetType: "ai_provider", TargetID: uuid.NewString(), Metadata: map[string]any{"keyChanged": "true"}},
		{Action: "ai.provider_tested", TargetType: "ai_provider", TargetID: uuid.NewString(), Metadata: map[string]any{"providerId": uuid.NewString(), "protocol": "responses", "ok": "false", "errorCategory": "auth", "latencyMs": "12"}},
		{Action: "ai.model_put", TargetType: "ai_model", TargetID: uuid.NewString(), Metadata: map[string]any{"providerId": uuid.NewString(), "modality": "vision"}},
		{Action: "ai.prompt_put", TargetType: "ai_prompt", TargetID: uuid.NewString(), Metadata: map[string]any{"subject": "math", "version": "1"}},
		{Action: "ai.limits_student_put", TargetType: "ai_limits", TargetID: uuid.NewString(), Metadata: map[string]any{"studentId": uuid.NewString()}},
	}
	for _, event := range approved {
		event.ActorUserID = base.ActorUserID
		event.RequestID = base.RequestID
		event.IP = base.IP
		if _, err := validateAndMarshal(event); err != nil {
			t.Fatalf("%s rejected: %v", event.Action, err)
		}
	}
	unsafe := approved[1]
	unsafe.Metadata = map[string]any{"keyChanged": "true", "apiKey": "very-secret"}
	if _, err := validateAndMarshal(unsafe); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("secret metadata accepted: %v", err)
	}
	tested := approved[2]
	for _, metadata := range []map[string]any{
		{"providerId": uuid.NewString(), "protocol": "responses", "ok": "false", "errorCategory": "raw-upstream-body", "latencyMs": "12"},
		{"providerId": uuid.NewString(), "protocol": "responses", "ok": "true", "errorCategory": "auth", "latencyMs": "12"},
		{"providerId": uuid.NewString(), "protocol": "responses", "ok": "false", "errorCategory": "auth", "latencyMs": "-1"},
		{"providerId": uuid.NewString(), "protocol": "responses", "ok": "false", "errorCategory": "auth", "latencyMs": "12", "authorization": "secret"},
	} {
		tested.Metadata = metadata
		if _, err := validateAndMarshal(tested); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("unsafe provider test metadata accepted: %#v", metadata)
		}
	}
}
