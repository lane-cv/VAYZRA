package aiqa

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresSummaryStore struct{ pool *pgxpool.Pool }

func NewPostgresSummaryStore(pool *pgxpool.Pool) *PostgresSummaryStore {
	return &PostgresSummaryStore{pool: pool}
}

func (s *PostgresSummaryStore) ListQuestionSummaries(ctx context.Context, studentID uuid.UUID, filter SummaryFilter) ([]QuestionSummary, SummaryCursor, error) {
	if studentID == uuid.Nil || validateSummaryFilter(filter) != nil {
		return nil, SummaryCursor{}, ErrInvalidInput
	}
	cursorTime, cursorChannel, cursorID := filter.Cursor.LastMessageAt, filter.Cursor.Channel, filter.Cursor.ID
	if cursorTime.IsZero() {
		cursorTime = time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)
		cursorChannel = "zzzz"
		cursorID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	}
	pattern := ""
	if filter.Search != "" {
		pattern = "%" + escapeLike(strings.ToLower(filter.Search)) + "%"
	}
	rows, err := s.pool.Query(ctx, `
WITH unified AS (
  SELECT q.id,'teacher'::text AS channel,q.title,q.status AS raw_status,q.last_message_at,q.created_at
  FROM qa_threads q
  JOIN users u ON u.id=q.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
  WHERE q.student_id=$1
  UNION ALL
  SELECT a.id,'ai'::text AS channel,a.title,
         COALESCE((SELECT r.status FROM ai_runs r WHERE r.thread_id=a.id ORDER BY r.created_at DESC,r.id DESC LIMIT 1),'idle') AS raw_status,
         a.last_message_at,a.created_at
  FROM ai_threads a
  JOIN users u ON u.id=a.student_id AND u.role='student' AND u.status='active' AND u.deleted_at IS NULL
  WHERE a.student_id=$1
)
SELECT id,channel,title,raw_status,last_message_at,created_at
FROM unified
WHERE ($2='' OR channel=$2)
  AND ($3='' OR lower(title) LIKE $3 ESCAPE '\')
  AND (last_message_at,channel,id)<($4,$5,$6)
ORDER BY last_message_at DESC,channel DESC,id DESC
LIMIT $7`, studentID, filter.Channel, pattern, cursorTime, cursorChannel, cursorID, filter.Limit)
	if err != nil {
		return nil, SummaryCursor{}, runtimeDBError(err)
	}
	defer rows.Close()
	out := make([]QuestionSummary, 0, filter.Limit)
	for rows.Next() {
		var item QuestionSummary
		if err = rows.Scan(&item.ID, &item.Channel, &item.Title, &item.RawStatus, &item.LastMessageAt, &item.CreatedAt); err != nil {
			return nil, SummaryCursor{}, runtimeDBError(err)
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, SummaryCursor{}, runtimeDBError(err)
	}
	if len(out) == 0 {
		var active bool
		if err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='student' AND status='active' AND deleted_at IS NULL)`, studentID).Scan(&active); err != nil {
			return nil, SummaryCursor{}, runtimeDBError(err)
		}
		if !active {
			return nil, SummaryCursor{}, ErrNotFound
		}
	}
	var next SummaryCursor
	if len(out) == filter.Limit {
		last := out[len(out)-1]
		next = SummaryCursor{LastMessageAt: last.LastMessageAt, Channel: last.Channel, ID: last.ID}
	}
	return out, next, nil
}

func escapeLike(raw string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(raw)
}
