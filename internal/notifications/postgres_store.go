package notifications

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/qanda"
)

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type PostgresStore struct {
	pool *pgxpool.Pool
	q    querier
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool, q: pool} }

type Writer struct{ q querier }

func NewWriter(q querier) *Writer { return &Writer{q: q} }

var safeTemplates = map[string][2]string{
	"qa_created":        {"New student question", "A student created a question."},
	"qa_replied":        {"Teacher reply", "Your teacher replied to a question."},
	"qa_followed_up":    {"Student follow-up", "A student followed up on a question."},
	"qa_status_changed": {"Question status changed", "Your question status changed."},
	"lesson_published":  {"New lesson", "A new lesson is available."},
}

func (w *Writer) Notify(ctx context.Context, in qanda.NotificationIntent) error {
	template, ok := safeTemplates[in.Kind]
	if !ok || in.RecipientUserID == uuid.Nil || in.TargetID == uuid.Nil || in.Title != template[0] || in.Summary != template[1] || len(in.DedupeKey) < 16 || len(in.DedupeKey) > 200 || !validTarget(in) || !validDedupe(in) {
		return ErrInvalidInput
	}
	if w == nil || w.q == nil {
		return errors.New("notification transaction is not configured")
	}
	_, err := w.q.Exec(ctx, `INSERT INTO notifications(id,recipient_user_id,kind,title,summary,target_type,target_id,target_path,dedupe_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(recipient_user_id,dedupe_key) DO NOTHING`, uuid.New(), in.RecipientUserID, in.Kind, in.Title, in.Summary, in.TargetType, in.TargetID, in.TargetPath, in.DedupeKey)
	return err
}

func validDedupe(in qanda.NotificationIntent) bool {
	var prefix string
	switch in.Kind {
	case "qa_created":
		prefix = "qa-created:"
	case "qa_replied":
		prefix = "qa-replied:"
	case "qa_followed_up":
		prefix = "qa-followed-up:"
	case "lesson_published":
		prefix = "lesson-published:"
	case "qa_status_changed":
		prefix = "qa-status:" + in.TargetID.String() + ":"
		version, err := strconv.ParseInt(strings.TrimPrefix(in.DedupeKey, prefix), 10, 64)
		return strings.HasPrefix(in.DedupeKey, prefix) && version > 0 && err == nil && in.DedupeKey == prefix+strconv.FormatInt(version, 10)
	default:
		return false
	}
	raw := strings.TrimPrefix(in.DedupeKey, prefix)
	id, err := uuid.Parse(raw)
	return strings.HasPrefix(in.DedupeKey, prefix) && err == nil && id != uuid.Nil && raw == id.String()
}
func validTarget(in qanda.NotificationIntent) bool {
	switch in.Kind {
	case "qa_created", "qa_followed_up":
		return in.TargetType == "qa_thread" && strings.HasPrefix(in.TargetPath, "/admin/questions/") && strings.TrimPrefix(in.TargetPath, "/admin/questions/") == in.TargetID.String()
	case "qa_replied", "qa_status_changed":
		return in.TargetType == "qa_thread" && strings.HasPrefix(in.TargetPath, "/student/questions/") && strings.TrimPrefix(in.TargetPath, "/student/questions/") == in.TargetID.String()
	case "lesson_published":
		return in.TargetType == "lesson" && strings.HasPrefix(in.TargetPath, "/student/learning/") && strings.TrimPrefix(in.TargetPath, "/student/learning/") == in.TargetID.String()
	}
	return false
}

func (s *PostgresStore) List(ctx context.Context, recipient uuid.UUID, c Cursor) ([]Notification, Cursor, error) {
	at, id := c.CreatedAt, c.ID
	if at.IsZero() {
		at = time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)
		id = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	}
	rows, err := s.q.Query(ctx, `SELECT id,kind,title,summary,target_type,target_id,target_path,read_at,created_at FROM notifications WHERE recipient_user_id=$1 AND (created_at,id)<($2,$3) ORDER BY created_at DESC,id DESC LIMIT $4`, recipient, at, id, c.Limit)
	if err != nil {
		return nil, Cursor{}, err
	}
	defer rows.Close()
	items := make([]Notification, 0, c.Limit)
	for rows.Next() {
		var n Notification
		if err = rows.Scan(&n.ID, &n.Kind, &n.Title, &n.Summary, &n.TargetType, &n.TargetID, &n.TargetPath, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, Cursor{}, err
		}
		n.CreatedAt = n.CreatedAt.UTC()
		if n.ReadAt != nil {
			x := n.ReadAt.UTC()
			n.ReadAt = &x
		}
		items = append(items, n)
	}
	if err = rows.Err(); err != nil {
		return nil, Cursor{}, err
	}
	var next Cursor
	if len(items) == c.Limit {
		last := items[len(items)-1]
		next = Cursor{CreatedAt: last.CreatedAt, ID: last.ID, Limit: c.Limit}
	}
	return items, next, nil
}
func (s *PostgresStore) UnreadCount(ctx context.Context, r uuid.UUID) (int64, error) {
	var n int64
	err := s.q.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_user_id=$1 AND read_at IS NULL`, r).Scan(&n)
	return n, err
}
func (s *PostgresStore) MarkRead(ctx context.Context, r, id uuid.UUID) error {
	tag, err := s.q.Exec(ctx, `UPDATE notifications SET read_at=clock_timestamp() WHERE id=$1 AND recipient_user_id=$2 AND read_at IS NULL`, id, r)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err = s.q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notifications WHERE id=$1 AND recipient_user_id=$2)`, id, r).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}
func (s *PostgresStore) MarkAllRead(ctx context.Context, r uuid.UUID) (int64, error) {
	if s.pool == nil {
		return 0, ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background())
	var cutoff time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&cutoff); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE notifications SET read_at=$2 WHERE recipient_user_id=$1 AND read_at IS NULL AND created_at<=$2`, r, cutoff)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
