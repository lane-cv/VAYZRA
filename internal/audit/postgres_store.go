package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type postgresDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
}
type PostgresWriter struct{ db postgresDB }

func NewPostgresWriter(db postgresDB) *PostgresWriter { return &PostgresWriter{db: db} }

var identifier = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

func (s *PostgresWriter) Write(ctx context.Context, event Event) error {
	metadata, err := validateAndMarshal(event)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO audit_logs (actor_user_id, action, target_type, target_id, metadata, request_id, ip) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)`, event.ActorUserID, event.Action, event.TargetType, event.TargetID, metadata, event.RequestID, event.IP)
	if err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

func (s *PostgresWriter) List(ctx context.Context, limit int, beforeID int64) ([]Record, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	var cursor any
	if beforeID != 0 {
		cursor = beforeID
	}
	rows, err := s.db.Query(ctx, `SELECT id, actor_user_id, action, target_type, target_id, metadata, request_id, ip, occurred_at FROM audit_logs WHERE ($2::bigint IS NULL OR id < $2) ORDER BY id DESC LIMIT $1`, limit, cursor)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	result := make([]Record, 0, limit)
	for rows.Next() {
		var record Record
		var metadata []byte
		var ip net.IP
		if err := rows.Scan(&record.ID, &record.ActorUserID, &record.Action, &record.TargetType, &record.TargetID, &metadata, &record.RequestID, &ip, &record.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		if err := json.Unmarshal(metadata, &record.Metadata); err != nil {
			return nil, fmt.Errorf("decode audit metadata: %w", err)
		}
		record.IP = append(net.IP(nil), ip...)
		record.OccurredAt = record.OccurredAt.UTC()
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return result, nil
}

func validateAndMarshal(event Event) ([]byte, error) {
	if event.ActorUserID == [16]byte{} || !identifier.MatchString(event.Action) || !identifier.MatchString(event.TargetType) || strings.TrimSpace(event.TargetID) == "" || len(event.TargetID) > 128 || strings.TrimSpace(event.RequestID) == "" || len(event.RequestID) > 64 || event.IP == nil {
		return nil, ErrInvalidEvent
	}
	allowed, ok := allowedMetadata[event.Action]
	if !ok || allowedTargetTypes[event.Action] != event.TargetType {
		return nil, ErrInvalidEvent
	}
	for key, value := range event.Metadata {
		if !allowed[key] {
			return nil, ErrInvalidEvent
		}
		if _, ok := value.(string); !ok {
			return nil, ErrInvalidEvent
		}
	}
	if event.Action == "qa.thread_created" || event.Action == "qa.student_followed_up" {
		messageCount, messageOK := event.Metadata["messageCount"].(string)
		attachmentCount, attachmentOK := event.Metadata["attachmentCount"].(string)
		attachments, countErr := strconv.Atoi(attachmentCount)
		if len(event.Metadata) != 2 || !messageOK || messageCount != "1" || !attachmentOK || countErr != nil || attachments < 0 || attachments > 20 {
			return nil, ErrInvalidEvent
		}
	}
	if event.Action == "qa.admin_replied" {
		messageCount, messageOK := event.Metadata["messageCount"].(string)
		attachmentCount, attachmentOK := event.Metadata["attachmentCount"].(string)
		oldStatus, oldOK := event.Metadata["oldStatus"].(string)
		newStatus, newOK := event.Metadata["newStatus"].(string)
		attachments, countErr := strconv.Atoi(attachmentCount)
		if len(event.Metadata) != 4 || !messageOK || messageCount != "1" || !attachmentOK || countErr != nil || attachments < 0 || attachments > 20 || !oldOK || !safeQAStatus(oldStatus) || !newOK || !safeQAStatus(newStatus) {
			return nil, ErrInvalidEvent
		}
	}
	if event.Action == "qa.status_changed" {
		oldStatus, oldOK := event.Metadata["oldStatus"].(string)
		newStatus, newOK := event.Metadata["newStatus"].(string)
		if len(event.Metadata) != 2 || !oldOK || !safeQAStatus(oldStatus) || !newOK || !safeQAStatus(newStatus) {
			return nil, ErrInvalidEvent
		}
	}
	if event.Action == "qa.teacher_note_added" {
		noteCount, ok := event.Metadata["noteCount"].(string)
		if len(event.Metadata) != 1 || !ok || noteCount != "1" {
			return nil, ErrInvalidEvent
		}
	}
	if strings.HasPrefix(event.Action, "ai.") {
		if !validAIEvent(event) {
			return nil, ErrInvalidEvent
		}
	}
	return json.Marshal(event.Metadata)
}

func safeQAStatus(status string) bool {
	switch status {
	case "pending", "in_progress", "waiting_student", "completed":
		return true
	default:
		return false
	}
}

var allowedMetadata = map[string]map[string]bool{
	"student.created": {"username": true, "display_name": true}, "student.disabled": {"status": true}, "student.enabled": {"status": true}, "student.password_reset": {},
	"catalog.created": {"kind": true}, "catalog.renamed": {"kind": true}, "catalog.reordered": {"kind": true}, "catalog.archived": {"kind": true}, "catalog.restored": {"kind": true},
	"lesson.draft_saved": {}, "lesson.published": {"revision_id": true}, "lesson.withdrawn": {}, "lesson.archived": {},
	"file.uploaded": {}, "file.policy_changed": {}, "file.processing_retried": {}, "file.replaced": {}, "file.draft_rolled_back": {}, "file.delete_requested": {},
	"file.cleanup_scheduled": {"previewCount": true}, "file.cleanup_completed": {},
	"qa.thread_created": {"messageCount": true, "attachmentCount": true}, "qa.student_followed_up": {"messageCount": true, "attachmentCount": true},
	"qa.admin_replied": {"messageCount": true, "attachmentCount": true, "oldStatus": true, "newStatus": true}, "qa.status_changed": {"oldStatus": true, "newStatus": true}, "qa.teacher_note_added": {"noteCount": true},
	"ai.provider_created": {}, "ai.provider_updated": {"keyChanged": true}, "ai.provider_activated": {}, "ai.model_put": {"providerId": true, "modality": true}, "ai.prompt_put": {"subject": true, "version": true}, "ai.limits_global_put": {}, "ai.limits_student_put": {"studentId": true},
}

var allowedTargetTypes = map[string]string{
	"student.created": "student", "student.disabled": "student", "student.enabled": "student", "student.password_reset": "student",
	"catalog.created": "catalog", "catalog.renamed": "catalog", "catalog.reordered": "catalog", "catalog.archived": "catalog", "catalog.restored": "catalog",
	"lesson.draft_saved": "lesson", "lesson.published": "lesson", "lesson.withdrawn": "lesson", "lesson.archived": "lesson",
	"file.uploaded": "file_version", "file.policy_changed": "lesson", "file.processing_retried": "file_version", "file.replaced": "file", "file.draft_rolled_back": "file_version", "file.delete_requested": "file",
	"file.cleanup_scheduled": "file_version", "file.cleanup_completed": "file_version",
	"qa.thread_created": "qa_thread", "qa.student_followed_up": "qa_thread", "qa.admin_replied": "qa_thread", "qa.status_changed": "qa_thread", "qa.teacher_note_added": "qa_thread",
	"ai.provider_created": "ai_provider", "ai.provider_updated": "ai_provider", "ai.provider_activated": "ai_provider", "ai.model_put": "ai_model", "ai.prompt_put": "ai_prompt", "ai.limits_global_put": "ai_limits", "ai.limits_student_put": "ai_limits",
}

func validAIEvent(e Event) bool {
	v := func(k string) string { x, _ := e.Metadata[k].(string); return x }
	switch e.Action {
	case "ai.provider_created", "ai.provider_activated", "ai.limits_global_put":
		return len(e.Metadata) == 0
	case "ai.provider_updated":
		return len(e.Metadata) == 1 && (v("keyChanged") == "true" || v("keyChanged") == "false")
	case "ai.model_put":
		_, x := uuid.Parse(v("providerId"))
		m := v("modality")
		return len(e.Metadata) == 2 && x == nil && (m == "text" || m == "vision")
	case "ai.prompt_put":
		n, err := strconv.ParseInt(v("version"), 10, 64)
		s := v("subject")
		return len(e.Metadata) == 2 && err == nil && n > 0 && (s == "math" || s == "physics")
	case "ai.limits_student_put":
		_, x := uuid.Parse(v("studentId"))
		return len(e.Metadata) == 1 && x == nil
	}
	return false
}
