package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
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
	var actorUserID any = event.ActorUserID
	if event.ActorUserID == uuid.Nil {
		actorUserID = nil
	}
	_, err = s.db.Exec(ctx, `INSERT INTO audit_logs (actor_user_id, action, target_type, target_id, metadata, request_id, ip) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)`, actorUserID, event.Action, event.TargetType, event.TargetID, metadata, event.RequestID, event.IP)
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
	if beforeID < 0 {
		return []Record{}, nil
	}
	page, err := s.ListFiltered(ctx, AuditFilter{BeforeID: beforeID, Limit: limit})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (s *PostgresWriter) ListFiltered(ctx context.Context, filter AuditFilter) (AuditPage, error) {
	if err := validateAuditFilter(filter); err != nil {
		return AuditPage{}, err
	}
	var actorID, from, to, cursor any
	if filter.ActorID != uuid.Nil {
		actorID = filter.ActorID
	}
	if !filter.From.IsZero() {
		from = filter.From
	}
	if !filter.To.IsZero() {
		to = filter.To
	}
	if filter.BeforeID != 0 {
		cursor = filter.BeforeID
	}
	outcomeExpression, outcomeArgs := auditOutcomeSQL(9)
	query := `
SELECT id,actor_user_id,action,target_type,target_id,metadata,request_id,ip,occurred_at
FROM audit_logs
WHERE ($1::text='' OR action=$1)
  AND ($2::text='' OR target_type=$2)
  AND ($3::text='' OR (` + outcomeExpression + `)=$3)
  AND ($4::uuid IS NULL OR actor_user_id=$4)
  AND ($5::timestamptz IS NULL OR occurred_at >= $5)
  AND ($6::timestamptz IS NULL OR occurred_at <= $6)
  AND ($7::bigint IS NULL OR id < $7)
ORDER BY id DESC
LIMIT $8`
	args := []any{
		filter.Action, filter.TargetType, filter.Outcome, actorID,
		from, to, cursor, filter.Limit + 1,
	}
	args = append(args, outcomeArgs...)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return AuditPage{}, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	result := make([]Record, 0, filter.Limit+1)
	for rows.Next() && len(result) < filter.Limit+1 {
		var record Record
		var metadata []byte
		var ip net.IP
		var actorUserID *uuid.UUID
		if err := rows.Scan(&record.ID, &actorUserID, &record.Action, &record.TargetType, &record.TargetID, &metadata, &record.RequestID, &ip, &record.OccurredAt); err != nil {
			return AuditPage{}, fmt.Errorf("scan audit event: %w", err)
		}
		if actorUserID != nil {
			record.ActorUserID = *actorUserID
		}
		if err := json.Unmarshal(metadata, &record.Metadata); err != nil {
			return AuditPage{}, fmt.Errorf("decode audit metadata: %w", err)
		}
		record.IP = append(net.IP(nil), ip...)
		record.OccurredAt = record.OccurredAt.UTC()
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, fmt.Errorf("iterate audit events: %w", err)
	}
	page := AuditPage{Items: result}
	if len(result) > filter.Limit {
		page.Items = result[:filter.Limit]
		page.NextBeforeID = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

func validateAuditFilter(filter AuditFilter) error {
	if filter.Limit < 1 || filter.Limit > 100 ||
		filter.BeforeID < 0 ||
		(filter.Action != "" && !identifier.MatchString(filter.Action)) ||
		(filter.TargetType != "" && !identifier.MatchString(filter.TargetType)) ||
		(filter.Outcome != "" &&
			(!identifier.MatchString(filter.Outcome) || !IsValidOutcome(filter.Outcome))) ||
		(!filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To)) {
		return ErrInvalidFilter
	}
	return nil
}

func validateAndMarshal(event Event) ([]byte, error) {
	if (event.ActorUserID == uuid.Nil && !systemActorAllowed(event)) || !identifier.MatchString(event.Action) || !identifier.MatchString(event.TargetType) || strings.TrimSpace(event.TargetID) == "" || len(event.TargetID) > 128 || strings.TrimSpace(event.RequestID) == "" || len(event.RequestID) > 64 || event.IP == nil {
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
	if strings.HasPrefix(event.Action, "operations.") {
		if !validOperationsEvent(event) {
			return nil, ErrInvalidEvent
		}
	}
	outcome, ok := classifyAuditOutcome(event.Action, event.Metadata)
	if !ok {
		return nil, ErrInvalidEvent
	}
	storedMetadata := make(map[string]any, len(event.Metadata)+1)
	for key, value := range event.Metadata {
		storedMetadata[key] = value
	}
	storedMetadata["outcome"] = outcome
	return json.Marshal(storedMetadata)
}

func systemActorAllowed(event Event) bool {
	return event.Action == "operations.lease_taken_over" &&
		event.TargetType == "operational_mode" &&
		event.TargetID == "global" &&
		len(event.Metadata) == 0 &&
		event.RequestID == "operations-lease-takeover" &&
		event.IP.Equal(net.ParseIP("127.0.0.1"))
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
	"file.processing_artifact_cleanup_scheduled": {}, "file.processing_artifact_cleanup_completed": {},
	"qa.thread_created": {"messageCount": true, "attachmentCount": true}, "qa.student_followed_up": {"messageCount": true, "attachmentCount": true},
	"qa.admin_replied": {"messageCount": true, "attachmentCount": true, "oldStatus": true, "newStatus": true}, "qa.status_changed": {"oldStatus": true, "newStatus": true}, "qa.teacher_note_added": {"noteCount": true},
	"ai.provider_created": {}, "ai.provider_updated": {"keyChanged": true}, "ai.provider_activated": {}, "ai.provider_tested": {"providerId": true, "protocol": true, "ok": true, "errorCategory": true, "latencyMs": true}, "ai.model_put": {"providerId": true, "modality": true}, "ai.prompt_put": {"subject": true, "version": true}, "ai.limits_global_put": {}, "ai.limits_student_put": {"studentId": true},
	"ai.file_access_rejected":     {"reason": true},
	"operations.settings_updated": {}, "operations.settings_rejected": {"category": true, "reason": true}, "operations.lease_taken_over": {},
	"operations.backup_requested":   {},
	"operations.alert_acknowledged": {"status": true},
}

var allowedTargetTypes = map[string]string{
	"student.created": "student", "student.disabled": "student", "student.enabled": "student", "student.password_reset": "student",
	"catalog.created": "catalog", "catalog.renamed": "catalog", "catalog.reordered": "catalog", "catalog.archived": "catalog", "catalog.restored": "catalog",
	"lesson.draft_saved": "lesson", "lesson.published": "lesson", "lesson.withdrawn": "lesson", "lesson.archived": "lesson",
	"file.uploaded": "file_version", "file.policy_changed": "lesson", "file.processing_retried": "file_version", "file.replaced": "file", "file.draft_rolled_back": "file_version", "file.delete_requested": "file",
	"file.cleanup_scheduled": "file_version", "file.cleanup_completed": "file_version",
	"file.processing_artifact_cleanup_scheduled": "file_version", "file.processing_artifact_cleanup_completed": "file_version",
	"qa.thread_created": "qa_thread", "qa.student_followed_up": "qa_thread", "qa.admin_replied": "qa_thread", "qa.status_changed": "qa_thread", "qa.teacher_note_added": "qa_thread",
	"ai.provider_created": "ai_provider", "ai.provider_updated": "ai_provider", "ai.provider_activated": "ai_provider", "ai.provider_tested": "ai_provider", "ai.model_put": "ai_model", "ai.prompt_put": "ai_prompt", "ai.limits_global_put": "ai_limits", "ai.limits_student_put": "ai_limits",
	"ai.file_access_rejected":     "ai_file_request",
	"operations.settings_updated": "system_settings", "operations.settings_rejected": "system_settings", "operations.lease_taken_over": "operational_mode",
	"operations.backup_requested":   "backup_run",
	"operations.alert_acknowledged": "operational_alert",
}

type auditOutcomeRule struct {
	outcome          string
	metadataKey      string
	metadataOutcomes map[string]string
}

var auditOutcomeRules = map[string]auditOutcomeRule{
	"student.created":        {outcome: "succeeded"},
	"student.disabled":       {outcome: "succeeded"},
	"student.enabled":        {outcome: "succeeded"},
	"student.password_reset": {outcome: "succeeded"},

	"catalog.created":   {outcome: "succeeded"},
	"catalog.renamed":   {outcome: "succeeded"},
	"catalog.reordered": {outcome: "succeeded"},
	"catalog.archived":  {outcome: "succeeded"},
	"catalog.restored":  {outcome: "succeeded"},

	"lesson.draft_saved": {outcome: "succeeded"},
	"lesson.published":   {outcome: "succeeded"},
	"lesson.withdrawn":   {outcome: "succeeded"},
	"lesson.archived":    {outcome: "succeeded"},

	"file.uploaded":                              {outcome: "succeeded"},
	"file.policy_changed":                        {outcome: "succeeded"},
	"file.processing_retried":                    {outcome: "succeeded"},
	"file.replaced":                              {outcome: "succeeded"},
	"file.draft_rolled_back":                     {outcome: "succeeded"},
	"file.delete_requested":                      {outcome: "succeeded"},
	"file.cleanup_scheduled":                     {outcome: "succeeded"},
	"file.cleanup_completed":                     {outcome: "succeeded"},
	"file.processing_artifact_cleanup_scheduled": {outcome: "succeeded"},
	"file.processing_artifact_cleanup_completed": {outcome: "succeeded"},

	"qa.thread_created":             {outcome: "succeeded"},
	"qa.student_followed_up":        {outcome: "succeeded"},
	"qa.admin_replied":              {outcome: "succeeded"},
	"qa.status_changed":             {outcome: "succeeded"},
	"qa.teacher_note_added":         {outcome: "succeeded"},
	"ai.provider_created":           {outcome: "succeeded"},
	"ai.provider_updated":           {outcome: "succeeded"},
	"ai.provider_activated":         {outcome: "succeeded"},
	"ai.model_put":                  {outcome: "succeeded"},
	"ai.prompt_put":                 {outcome: "succeeded"},
	"ai.limits_global_put":          {outcome: "succeeded"},
	"ai.limits_student_put":         {outcome: "succeeded"},
	"ai.file_access_rejected":       {outcome: "rejected"},
	"operations.settings_updated":   {outcome: "succeeded"},
	"operations.settings_rejected":  {outcome: "rejected"},
	"operations.lease_taken_over":   {outcome: "succeeded"},
	"operations.backup_requested":   {outcome: "succeeded"},
	"operations.alert_acknowledged": {outcome: "succeeded"},
	"ai.provider_tested": {
		metadataKey: "ok",
		metadataOutcomes: map[string]string{
			"true": "succeeded", "false": "failed",
		},
	},
}

func classifyAuditOutcome(action string, metadata map[string]any) (string, bool) {
	rule, ok := auditOutcomeRules[action]
	if !ok {
		return "", false
	}
	if rule.outcome != "" {
		return rule.outcome, true
	}
	value, ok := metadata[rule.metadataKey].(string)
	if !ok {
		return "", false
	}
	outcome, ok := rule.metadataOutcomes[value]
	return outcome, ok
}

func IsValidOutcome(outcome string) bool {
	if outcome == "" {
		return false
	}
	for _, rule := range auditOutcomeRules {
		if rule.outcome == outcome {
			return true
		}
		for _, classified := range rule.metadataOutcomes {
			if classified == outcome {
				return true
			}
		}
	}
	return false
}

func auditOutcomeSQL(firstParameter int) (string, []any) {
	staticActions := make(map[string][]string)
	dynamicActions := make([]string, 0)
	for action, rule := range auditOutcomeRules {
		if rule.outcome != "" {
			staticActions[rule.outcome] = append(staticActions[rule.outcome], action)
			continue
		}
		dynamicActions = append(dynamicActions, action)
	}
	sort.Strings(dynamicActions)
	outcomes := make([]string, 0, len(staticActions))
	for outcome := range staticActions {
		outcomes = append(outcomes, outcome)
		sort.Strings(staticActions[outcome])
	}
	sort.Strings(outcomes)

	parameter := firstParameter
	args := make([]any, 0)
	clauses := make([]string, 0, len(dynamicActions)+len(outcomes))
	bind := func(value any) string {
		placeholder := "$" + strconv.Itoa(parameter)
		parameter++
		args = append(args, value)
		return placeholder
	}
	for _, action := range dynamicActions {
		rule := auditOutcomeRules[action]
		values := make([]string, 0, len(rule.metadataOutcomes))
		for value := range rule.metadataOutcomes {
			values = append(values, value)
		}
		sort.Strings(values)
		for _, value := range values {
			clauses = append(clauses,
				"WHEN action="+bind(action)+"::text"+
					" AND metadata ->> ("+bind(rule.metadataKey)+"::text)="+bind(value)+"::text"+
					" THEN "+bind(rule.metadataOutcomes[value])+"::text",
			)
		}
	}
	for _, outcome := range outcomes {
		clauses = append(clauses,
			"WHEN action = ANY("+bind(staticActions[outcome])+"::text[])"+
				" THEN "+bind(outcome)+"::text",
		)
	}
	return "CASE " + strings.Join(clauses, " ") + " END", args
}

func validOperationsEvent(event Event) bool {
	switch event.Action {
	case "operations.settings_updated", "operations.lease_taken_over":
		return len(event.Metadata) == 0
	case "operations.backup_requested":
		id, err := uuid.Parse(event.TargetID)
		return len(event.Metadata) == 0 && err == nil &&
			id != uuid.Nil && id.String() == event.TargetID
	case "operations.alert_acknowledged":
		id, err := uuid.Parse(event.TargetID)
		status, ok := event.Metadata["status"].(string)
		return len(event.Metadata) == 1 && err == nil &&
			id != uuid.Nil && id.String() == event.TargetID &&
			ok && status == "acknowledged"
	case "operations.settings_rejected":
		category, categoryOK := event.Metadata["category"].(string)
		reason, reasonOK := event.Metadata["reason"].(string)
		return len(event.Metadata) == 2 &&
			categoryOK && category == "high_risk" &&
			reasonOK && (reason == "retention" || reason == "backup_schedule" || reason == "threshold")
	default:
		return false
	}
}

func validAIEvent(e Event) bool {
	v := func(k string) string { x, _ := e.Metadata[k].(string); return x }
	switch e.Action {
	case "ai.provider_created", "ai.provider_activated", "ai.limits_global_put":
		return len(e.Metadata) == 0
	case "ai.provider_updated":
		return len(e.Metadata) == 1 && (v("keyChanged") == "true" || v("keyChanged") == "false")
	case "ai.provider_tested":
		_, providerErr := uuid.Parse(v("providerId"))
		latency, latencyErr := strconv.ParseInt(v("latencyMs"), 10, 64)
		protocol := v("protocol")
		ok := v("ok")
		category := v("errorCategory")
		if providerErr != nil || latencyErr != nil || latency < 0 || (protocol != "chat_completions" && protocol != "responses") || (ok != "true" && ok != "false") {
			return false
		}
		if ok == "true" {
			return len(e.Metadata) == 5 && category == ""
		}
		switch category {
		case "auth", "rate_limited", "upstream_4xx", "upstream_5xx", "timeout", "stream_interrupted", "malformed_stream", "response_too_large", "cancelled", "unavailable", "busy":
			return len(e.Metadata) == 5
		default:
			return false
		}
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
	case "ai.file_access_rejected":
		if e.TargetID != "unresolved" || len(e.Metadata) != 1 {
			return false
		}
		switch v("reason") {
		case "malformed_id", "unexpected_query", "invalid_actor", "invalid_ip":
			return true
		default:
			return false
		}
	}
	return false
}
