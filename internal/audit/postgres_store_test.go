package audit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresAuditFilteredReadsUseStableKeysetAndExactFilters(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE audit_logs, users CASCADE"); err != nil {
		t.Fatal(err)
	}
	actorID := uuid.MustParse("60000000-0000-4000-8000-000000000001")
	otherActorID := uuid.MustParse("60000000-0000-4000-8000-000000000002")
	if _, err := pool.Exec(ctx, `
INSERT INTO users(id,username,display_name,role,password_hash,must_change_password)
VALUES
  ($1,'audit_filter_admin','Audit Filter Admin','admin','hash',false),
  ($2,'audit_filter_other','Audit Filter Other','student','hash',false)`,
		actorID, otherActorID); err != nil {
		t.Fatal(err)
	}

	baseEvent := Event{
		ActorUserID: actorID, TargetID: "global",
		RequestID: "audit-filter", IP: net.ParseIP("192.0.2.80"),
	}
	events := []Event{
		baseEvent,
		baseEvent,
		baseEvent,
		baseEvent,
		baseEvent,
	}
	events[0].Action = "operations.settings_rejected"
	events[0].TargetType = "system_settings"
	events[0].Metadata = map[string]any{"category": "high_risk", "reason": "retention"}
	events[0].RequestID = "audit-filter-0001"
	events[1].Action = "operations.settings_rejected"
	events[1].TargetType = "system_settings"
	events[1].Metadata = map[string]any{"category": "high_risk", "reason": "threshold"}
	events[1].RequestID = "audit-filter-0002"
	events[2].Action = "operations.settings_updated"
	events[2].TargetType = "system_settings"
	events[2].Metadata = map[string]any{}
	events[2].RequestID = "audit-filter-0003"
	events[3].ActorUserID = otherActorID
	events[3].Action = "operations.settings_rejected"
	events[3].TargetType = "system_settings"
	events[3].Metadata = map[string]any{"category": "high_risk", "reason": "backup_schedule"}
	events[3].RequestID = "audit-filter-0004"
	events[4].Action = "ai.provider_tested"
	events[4].TargetType = "ai_provider"
	events[4].TargetID = uuid.NewString()
	events[4].Metadata = map[string]any{
		"providerId": uuid.NewString(), "protocol": "responses",
		"ok": "false", "errorCategory": "auth", "latencyMs": "12",
	}
	events[4].RequestID = "audit-filter-0005"

	from := time.Now().UTC().Add(-time.Second)
	writer := NewPostgresWriter(pool)
	forgedOutcome := "succeeded"
	for i, event := range events {
		eventWriter := writer
		if i == 0 {
			eventWriter = NewPostgresWriter(historicalOutcomeDB{pool: pool})
		} else if i == 1 {
			eventWriter = NewPostgresWriter(historicalOutcomeDB{
				pool: pool, storedOutcome: &forgedOutcome,
			})
		}
		if err := eventWriter.Write(ctx, event); err != nil {
			t.Fatalf("write %s: %v", event.RequestID, err)
		}
	}
	to := time.Now().UTC().Add(time.Second)

	filter := AuditFilter{
		Action: "operations.settings_rejected", TargetType: "system_settings",
		Outcome: "rejected", ActorID: actorID,
		From: from, To: to,
		Limit: 1,
	}
	first, err := writer.ListFiltered(ctx, filter)
	if err != nil || len(first.Items) != 1 || first.NextBeforeID == 0 ||
		first.Items[0].RequestID != "audit-filter-0002" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	filter.BeforeID = first.NextBeforeID
	second, err := writer.ListFiltered(ctx, filter)
	if err != nil || len(second.Items) != 1 || second.NextBeforeID != 0 ||
		second.Items[0].RequestID != "audit-filter-0001" {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	for _, tc := range []struct {
		outcome string
		action  string
		request string
	}{
		{"succeeded", "operations.settings_updated", "audit-filter-0003"},
		{"failed", "ai.provider_tested", "audit-filter-0005"},
	} {
		page, listErr := writer.ListFiltered(ctx, AuditFilter{
			Action: tc.action, Outcome: tc.outcome, From: from, To: to, Limit: 10,
		})
		if listErr != nil || len(page.Items) != 1 || page.Items[0].RequestID != tc.request {
			t.Fatalf("%s page=%#v err=%v", tc.outcome, page, listErr)
		}
	}
	if _, err := writer.ListFiltered(ctx, AuditFilter{
		Action: "' OR TRUE --", Limit: 20,
	}); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("unsafe filter error=%v", err)
	}
	if _, err := writer.ListFiltered(ctx, AuditFilter{
		Outcome: "unknown", Limit: 20,
	}); !errors.Is(err, ErrInvalidFilter) {
		t.Fatalf("unknown outcome error=%v", err)
	}

	compatibility, err := writer.List(ctx, 1000, 0)
	if err != nil || len(compatibility) != len(events) {
		t.Fatalf("compatibility records=%d err=%v", len(compatibility), err)
	}
	negativeCursor, err := writer.List(ctx, 10, -1)
	if err != nil || len(negativeCursor) != 0 {
		t.Fatalf("negative compatibility records=%d err=%v", len(negativeCursor), err)
	}
}

type historicalOutcomeDB struct {
	pool          *pgxpool.Pool
	storedOutcome *string
}

func (db historicalOutcomeDB) Exec(
	ctx context.Context,
	query string,
	args ...any,
) (pgconn.CommandTag, error) {
	if strings.HasPrefix(query, "INSERT INTO audit_logs ") && len(args) > 4 {
		var metadata map[string]any
		if raw, ok := args[4].([]byte); ok && json.Unmarshal(raw, &metadata) == nil {
			if db.storedOutcome == nil {
				delete(metadata, "outcome")
			} else {
				metadata["outcome"] = *db.storedOutcome
			}
			args[4], _ = json.Marshal(metadata)
		}
	}
	return db.pool.Exec(ctx, query, args...)
}

func (db historicalOutcomeDB) Query(
	ctx context.Context,
	query string,
	args ...any,
) (pgx.Rows, error) {
	return db.pool.Query(ctx, query, args...)
}

func TestAuditOutcomeClassificationIsServerOwnedAndComplete(t *testing.T) {
	base := Event{
		ActorUserID: uuid.New(), Action: "operations.settings_updated",
		TargetType: "system_settings", TargetID: "global",
		Metadata: map[string]any{}, RequestID: "outcome-owned",
		IP: net.ParseIP("192.0.2.81"),
	}
	encoded, err := validateAndMarshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"outcome":"succeeded"`) {
		t.Fatalf("writer did not classify outcome: %s", encoded)
	}
	clientSupplied := base
	clientSupplied.Metadata = map[string]any{"outcome": "failed"}
	if _, err := validateAndMarshal(clientSupplied); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("client-owned outcome error=%v", err)
	}

	if len(auditOutcomeRules) != len(allowedTargetTypes) {
		t.Fatalf("outcome rules=%d allowed actions=%d", len(auditOutcomeRules), len(allowedTargetTypes))
	}
	for action := range allowedTargetTypes {
		if _, ok := auditOutcomeRules[action]; !ok {
			t.Errorf("allowed action %q has no outcome rule", action)
		}
	}
	for action := range auditOutcomeRules {
		if _, ok := allowedTargetTypes[action]; !ok {
			t.Errorf("outcome rule %q is not an allowed action", action)
		}
	}
	for _, outcome := range []string{"succeeded", "rejected", "failed", "attempted"} {
		if !IsValidOutcome(outcome) {
			t.Errorf("classified outcome %q rejected by filter", outcome)
		}
	}
	for _, outcome := range []string{"", "unknown", "success", "error"} {
		if IsValidOutcome(outcome) {
			t.Errorf("unclassified outcome %q accepted by filter", outcome)
		}
	}

	query, args := auditOutcomeSQL(9)
	for range 20 {
		repeatedQuery, repeatedArgs := auditOutcomeSQL(9)
		if repeatedQuery != query || !reflect.DeepEqual(repeatedArgs, args) {
			t.Fatal("outcome SQL generation is not stable")
		}
	}
	for action, rule := range auditOutcomeRules {
		for _, raw := range []string{action, rule.outcome, rule.metadataKey} {
			if raw != "" && strings.Contains(query, raw) {
				t.Errorf("outcome SQL contains unbound rule value %q", raw)
			}
		}
		for value, outcome := range rule.metadataOutcomes {
			for _, raw := range []string{value, outcome} {
				if strings.Contains(query, raw) {
					t.Errorf("outcome SQL contains unbound dynamic value %q", raw)
				}
			}
		}
	}
}

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

func TestOperationsSystemAuditActorExceptionIsExact(t *testing.T) {
	event := Event{
		Action: "operations.lease_taken_over", TargetType: "operational_mode",
		TargetID: "global", Metadata: map[string]any{},
		RequestID: "operations-lease-takeover", IP: net.ParseIP("127.0.0.1"),
	}
	if _, err := validateAndMarshal(event); err != nil {
		t.Fatalf("exact system event rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Event){
		"action":      func(e *Event) { e.Action = "operations.settings_updated" },
		"target type": func(e *Event) { e.TargetType = "system_settings" },
		"target ID":   func(e *Event) { e.TargetID = "other" },
		"request ID":  func(e *Event) { e.RequestID = "other-system-request" },
		"IP":          func(e *Event) { e.IP = net.ParseIP("127.0.0.2") },
		"metadata":    func(e *Event) { e.Metadata = map[string]any{"token": "secret"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := event
			mutate(&changed)
			if _, err := validateAndMarshal(changed); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("inexact system event error=%v", err)
			}
		})
	}
	for action, targetType := range allowedTargetTypes {
		if action == event.Action {
			continue
		}
		candidate := event
		candidate.Action = action
		candidate.TargetType = targetType
		if systemActorAllowed(candidate) {
			t.Fatalf("nil actor allowed for non-system action %q", action)
		}
	}
}

func TestOperationsRetentionSystemAuditActorExceptionIsExact(t *testing.T) {
	event := Event{
		Action: "operations.retention_completed", TargetType: "metadata_retention",
		TargetID: "global",
		Metadata: map[string]any{
			"samples":              "1",
			"alertDeliveries":      "2",
			"alerts":               "3",
			"restoreVerifications": "4",
			"backupRuns":           "5",
		},
		RequestID: "operations-retention", IP: net.ParseIP("127.0.0.1"),
	}
	if _, err := validateAndMarshal(event); err != nil {
		t.Fatalf("exact retention system event rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Event){
		"actor user ID": func(e *Event) { e.ActorUserID = uuid.New() },
		"action":        func(e *Event) { e.Action = "operations.lease_taken_over" },
		"target type":   func(e *Event) { e.TargetType = "operational_mode" },
		"target ID":     func(e *Event) { e.TargetID = "other" },
		"request ID":    func(e *Event) { e.RequestID = "other-system-request" },
		"IP":            func(e *Event) { e.IP = net.ParseIP("127.0.0.2") },
		"missing key":   func(e *Event) { delete(e.Metadata, "samples") },
		"extra key":     func(e *Event) { e.Metadata["reason"] = "retention" },
		"negative":      func(e *Event) { e.Metadata["alerts"] = "-1" },
		"non-decimal":   func(e *Event) { e.Metadata["backupRuns"] = "01" },
		"wrong type":    func(e *Event) { e.Metadata["samples"] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := event
			changed.Metadata = make(map[string]any, len(event.Metadata))
			for key, value := range event.Metadata {
				changed.Metadata[key] = value
			}
			mutate(&changed)
			if _, err := validateAndMarshal(changed); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("inexact retention system event error=%v", err)
			}
		})
	}
}

func TestOperationsSettingsRejectionAuditAllowsOnlyRedactedClassification(t *testing.T) {
	event := Event{
		ActorUserID: uuid.New(), Action: "operations.settings_rejected",
		TargetType: "system_settings", TargetID: "global",
		Metadata:  map[string]any{"category": "high_risk", "reason": "retention"},
		RequestID: "operations-rejection", IP: net.ParseIP("192.0.2.41"),
	}
	if _, err := validateAndMarshal(event); err != nil {
		t.Fatalf("redacted rejection event rejected: %v", err)
	}
	for name, metadata := range map[string]map[string]any{
		"submitted value":  {"category": "high_risk", "reason": "retention", "value": "0"},
		"prior value":      {"category": "high_risk", "reason": "retention", "prior": "365"},
		"unknown reason":   {"category": "high_risk", "reason": "timezone"},
		"unknown category": {"category": "invalid", "reason": "retention"},
	} {
		t.Run(name, func(t *testing.T) {
			changed := event
			changed.Metadata = metadata
			if _, err := validateAndMarshal(changed); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("unsafe rejection metadata error=%v", err)
			}
		})
	}
	for _, reason := range []string{"retention", "backup_schedule", "threshold"} {
		changed := event
		changed.Metadata = map[string]any{"category": "high_risk", "reason": reason}
		if _, err := validateAndMarshal(changed); err != nil {
			t.Fatalf("approved reason %q rejected: %v", reason, err)
		}
	}
}

func TestApplicationUpdateAuditAllowsOnlyRedactedRequests(t *testing.T) {
	for _, action := range []string{"operations.update_requested", "operations.rollback_requested"} {
		t.Run(action, func(t *testing.T) {
			event := Event{
				ActorUserID: uuid.New(), Action: action,
				TargetType: "application_update", TargetID: "global",
				Metadata:  map[string]any{"status": "requested"},
				RequestID: "application-update-request", IP: net.ParseIP("192.0.2.42"),
			}
			encoded, err := validateAndMarshal(event)
			if err != nil {
				t.Fatalf("redacted update request rejected: %v", err)
			}
			if !strings.Contains(string(encoded), `"outcome":"attempted"`) {
				t.Fatalf("request outcome not classified: %s", encoded)
			}
			for name, metadata := range map[string]map[string]any{
				"unexpected status": {"status": "completed"},
				"version detail":    {"status": "requested", "version": "1.2.3"},
				"repository detail": {"status": "requested", "repository": "private"},
			} {
				t.Run(name, func(t *testing.T) {
					changed := event
					changed.Metadata = metadata
					if _, err := validateAndMarshal(changed); !errors.Is(err, ErrInvalidEvent) {
						t.Fatalf("unsafe update event error=%v", err)
					}
				})
			}
		})
	}
}

func TestPostgresWriterListsSystemEventWithNilActor(t *testing.T) {
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE audit_logs"); err != nil {
		t.Fatal(err)
	}
	event := Event{
		Action: "operations.lease_taken_over", TargetType: "operational_mode",
		TargetID: "global", Metadata: map[string]any{},
		RequestID: "operations-lease-takeover", IP: net.ParseIP("127.0.0.1"),
	}
	writer := NewPostgresWriter(pool)
	if err := writer.Write(ctx, event); err != nil {
		t.Fatal(err)
	}
	records, err := writer.List(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ActorUserID != uuid.Nil ||
		records[0].Action != event.Action {
		t.Fatalf("records=%#v", records)
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

func TestAIFileRequestAuditAllowsOnlyStableRejectionMetadata(t *testing.T) {
	base := Event{
		ActorUserID: uuid.New(), Action: "ai.file_access_rejected", TargetType: "ai_file_request", TargetID: "unresolved",
		Metadata: map[string]any{"reason": "malformed_id"}, RequestID: "request-ai-file", IP: net.ParseIP("192.0.2.9"),
	}
	for _, reason := range []string{"malformed_id", "unexpected_query", "invalid_actor", "invalid_ip"} {
		event := base
		event.Metadata = map[string]any{"reason": reason}
		if _, err := validateAndMarshal(event); err != nil {
			t.Fatalf("approved reason %q rejected: %v", reason, err)
		}
	}
	for name, mutate := range map[string]func(Event) Event{
		"raw identifier": func(event Event) Event {
			event.Metadata = map[string]any{"reason": "malformed_id", "fileVersionId": uuid.NewString()}
			return event
		},
		"raw query": func(event Event) Event {
			event.Metadata = map[string]any{"reason": "unexpected_query", "query": "secret=value"}
			return event
		},
		"unknown reason": func(event Event) Event {
			event.Metadata = map[string]any{"reason": "foreign_object"}
			return event
		},
		"resolved target": func(event Event) Event {
			event.TargetID = uuid.NewString()
			return event
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateAndMarshal(mutate(base)); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("unsafe event accepted: %v", err)
			}
		})
	}
}
