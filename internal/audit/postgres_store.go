package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
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
	if !ok || !validTargetType(event.TargetType) {
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
	return json.Marshal(event.Metadata)
}

var allowedMetadata = map[string]map[string]bool{
	"student.created": {"username": true, "display_name": true}, "student.disabled": {"status": true}, "student.enabled": {"status": true}, "student.password_reset": {},
	"catalog.created": {"kind": true}, "catalog.renamed": {"kind": true}, "catalog.reordered": {"kind": true}, "catalog.archived": {"kind": true}, "catalog.restored": {"kind": true},
	"lesson.draft_saved": {}, "lesson.published": {"revision_id": true}, "lesson.withdrawn": {}, "lesson.archived": {},
}

func validTargetType(target string) bool {
	return target == "student" || target == "catalog" || target == "lesson"
}
