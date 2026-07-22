package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresOutboxStore struct {
	pool            *pgxpool.Pool
	afterLessonLock func()
}

func NewPostgresOutboxStore(pool *pgxpool.Pool) *PostgresOutboxStore {
	return &PostgresOutboxStore{pool: pool}
}

func (s *PostgresOutboxStore) Claim(ctx context.Context, owner string) ([]OutboxEvent, error) {
	if s == nil || s.pool == nil || strings.TrimSpace(owner) == "" || len(owner) > 160 {
		return nil, ErrInvalidInput
	}
	// A process may die after taking its final allowed lease. Once that lease
	// expires, make the terminal state explicit instead of stranding the row.
	if _, err := s.pool.Exec(ctx, `WITH terminal AS (
 SELECT id FROM outbox_events
 WHERE published_at IS NULL AND attempts>=$1 AND (lease_until IS NULL OR lease_until<=clock_timestamp())
 ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT $2
) UPDATE outbox_events e SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,last_error_category='max_attempts'
 FROM terminal WHERE e.id=terminal.id`, OutboxMaxAttempts, OutboxBatchLimit); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `WITH ready AS (
 SELECT id FROM outbox_events
 WHERE published_at IS NULL AND next_attempt_at<=clock_timestamp()
   AND attempts<$2 AND (lease_until IS NULL OR lease_until<=clock_timestamp())
 ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT $3
) UPDATE outbox_events e SET lease_owner=$1,lease_until=clock_timestamp()+interval '30 seconds',attempts=e.attempts+1
FROM ready WHERE e.id=ready.id
RETURNING e.id,e.kind,e.payload,e.attempts,e.lease_owner,e.lease_until`, owner, OutboxMaxAttempts, OutboxBatchLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]OutboxEvent, 0, OutboxBatchLimit)
	for rows.Next() {
		var event OutboxEvent
		if err = rows.Scan(&event.ID, &event.Kind, &event.Payload, &event.Attempts, &event.LeaseOwner, &event.LeaseUntil); err != nil {
			return nil, err
		}
		event.LeaseUntil = event.LeaseUntil.UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *PostgresOutboxStore) DeliverLessonPublication(ctx context.Context, event OutboxEvent, owner string) error {
	if event.Kind != "lesson.published" {
		return permanentOutboxError("kind_unsupported")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var raw json.RawMessage
	var kind string
	var active bool
	err = tx.QueryRow(ctx, `SELECT kind,payload,published_at IS NULL AND lease_owner=$2 AND lease_until>clock_timestamp() FROM outbox_events WHERE id=$1 FOR UPDATE`, event.ID, owner).Scan(&kind, &raw, &active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if kind != "lesson.published" {
		return permanentOutboxError("kind_unsupported")
	}
	payload, err := decodeLessonPublicationPayload(raw)
	if err != nil {
		return err
	}
	var mode string
	err = tx.QueryRow(ctx, `SELECT ra.mode FROM lessons l
 JOIN chapters c ON c.id=l.chapter_id AND c.archived_at IS NULL
 JOIN subjects s ON s.id=c.subject_id AND s.archived_at IS NULL
 JOIN terms t ON t.id=s.term_id AND t.archived_at IS NULL
 JOIN grades g ON g.id=t.grade_id AND g.archived_at IS NULL
 JOIN lesson_revision_finalizations rf ON rf.lesson_id=l.id AND rf.revision_id=l.published_revision_id
 JOIN lesson_revisions r ON r.id=rf.revision_id AND r.lesson_id=l.id
 JOIN lesson_revision_audiences ra ON ra.revision_id=r.id
 WHERE l.id=$1 AND l.published_revision_id=$2 AND l.archived_at IS NULL
 FOR UPDATE OF l`, payload.LessonID, payload.RevisionID).Scan(&mode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		if s.afterLessonLock != nil {
			s.afterLessonLock()
		}
		var insert string
		switch mode {
		case "all":
			insert = `INSERT INTO notifications(recipient_user_id,kind,title,summary,target_type,target_id,target_path,dedupe_key)
 SELECT u.id,'lesson_published','New lesson','A new lesson is available.','lesson',$1::uuid,'/student/learning/'||($1::uuid)::text,'lesson-published:'||($2::uuid)::text
 FROM users u WHERE u.role='student' AND u.status='active' AND u.deleted_at IS NULL
 ON CONFLICT(recipient_user_id,dedupe_key) DO NOTHING`
		case "selected":
			insert = `INSERT INTO notifications(recipient_user_id,kind,title,summary,target_type,target_id,target_path,dedupe_key)
 SELECT u.id,'lesson_published','New lesson','A new lesson is available.','lesson',$1::uuid,'/student/learning/'||($1::uuid)::text,'lesson-published:'||($2::uuid)::text
 FROM lesson_revision_audience_users rau JOIN users u ON u.id=rau.user_id
 WHERE rau.revision_id=$2 AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
 ON CONFLICT(recipient_user_id,dedupe_key) DO NOTHING`
		default:
			return permanentOutboxError("audience_invalid")
		}
		if _, err = tx.Exec(ctx, insert, payload.LessonID, payload.RevisionID); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE outbox_events SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,last_error_category=NULL WHERE id=$1 AND published_at IS NULL AND lease_owner=$2 AND lease_until>clock_timestamp()`, event.ID, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return tx.Commit(ctx)
}

func (s *PostgresOutboxStore) Complete(ctx context.Context, id uuid.UUID, owner string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,last_error_category=NULL WHERE id=$1 AND published_at IS NULL AND lease_owner=$2 AND lease_until>clock_timestamp()`, id, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

var stableOutboxCategories = map[string]bool{"payload_invalid": true, "kind_unsupported": true, "audience_invalid": true, "delivery_failed": true, "lease_lost": true, "max_attempts": true}

func (s *PostgresOutboxStore) Fail(ctx context.Context, id uuid.UUID, owner, category string, permanent bool) error {
	if !stableOutboxCategories[category] {
		category = "delivery_failed"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var attempts int
	if err = tx.QueryRow(ctx, `SELECT attempts FROM outbox_events WHERE id=$1 AND published_at IS NULL AND lease_owner=$2 AND lease_until>clock_timestamp() FOR UPDATE`, id, owner).Scan(&attempts); errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	} else if err != nil {
		return err
	}
	terminal := permanent || attempts >= OutboxMaxAttempts
	if terminal {
		_, err = tx.Exec(ctx, `UPDATE outbox_events SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,last_error_category=$3 WHERE id=$1 AND lease_owner=$2`, id, owner, category)
	} else {
		delay := retryDelay(attempts)
		_, err = tx.Exec(ctx, `UPDATE outbox_events SET lease_owner=NULL,lease_until=NULL,next_attempt_at=clock_timestamp()+$3::interval,last_error_category=$4 WHERE id=$1 AND lease_owner=$2`, id, owner, fmt.Sprintf("%f seconds", delay.Seconds()), category)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
