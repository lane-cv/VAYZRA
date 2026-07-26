package aiqa

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UsageCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type UsageFilter struct {
	StudentID uuid.UUID
	ModelID   uuid.UUID
	Status    RunStatus
	From      time.Time
	To        time.Time
	Cursor    UsageCursor
	Limit     int
}

type UsageSummary struct {
	Requests, Succeeded, Failed int64
	InputTokens, OutputTokens   int64
	CostMicroUSD                int64
	UnknownUsage                int64
	AverageFirstByteMS          int64
	AverageTotalMS              int64
}

type UsageRun struct {
	ID                 uuid.UUID
	StudentID          uuid.UUID
	StudentUsername    string
	StudentDisplayName string
	ModelID            uuid.UUID
	ModelLabel         string
	Status             RunStatus
	InputTokens        int64
	OutputTokens       int64
	UsageSource        string
	CostMicroUSD       int64
	FirstByteMS        *int64
	TotalMS            *int64
	ErrorCategory      string
	CreatedAt          time.Time
	StartedAt          *time.Time
	CompletedAt        *time.Time
}

type AdminUsageService interface {
	UsageSummary(context.Context, Principal, UsageFilter) (UsageSummary, error)
	UsageRuns(context.Context, Principal, UsageFilter) ([]UsageRun, UsageCursor, error)
}

type PostgresAdminUsageService struct{ pool *pgxpool.Pool }

func NewPostgresAdminUsageService(pool *pgxpool.Pool) *PostgresAdminUsageService {
	return &PostgresAdminUsageService{pool: pool}
}

func validateUsageFilter(filter UsageFilter) error {
	if filter.Limit < 1 || filter.Limit > 100 {
		return ErrInvalidInput
	}
	if filter.Status != "" && filter.Status != RunQueued && filter.Status != RunStreaming &&
		filter.Status != RunSucceeded && filter.Status != RunFailed && filter.Status != RunCancelled {
		return ErrInvalidInput
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.From.After(filter.To) {
		return ErrInvalidInput
	}
	c := filter.Cursor
	empty := c.CreatedAt.IsZero() && c.ID == uuid.Nil
	complete := !c.CreatedAt.IsZero() && c.ID != uuid.Nil
	if !empty && !complete {
		return ErrInvalidInput
	}
	return nil
}

func (s *PostgresAdminUsageService) UsageSummary(ctx context.Context, p Principal, filter UsageFilter) (UsageSummary, error) {
	if err := admin(p); err != nil {
		return UsageSummary{}, err
	}
	if err := validateUsageFilter(filter); err != nil {
		return UsageSummary{}, err
	}
	var out UsageSummary
	err := s.pool.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE r.status='succeeded'),
       count(*) FILTER (WHERE r.status IN ('failed','cancelled')),
       COALESCE(sum(r.input_tokens),0),
       COALESCE(sum(r.output_tokens),0),
       COALESCE(sum(r.cost_micro_usd),0),
       count(*) FILTER (WHERE r.usage_source='unknown'),
       COALESCE(floor(avg(r.first_byte_ms)),0)::bigint,
       COALESCE(floor(avg(r.total_ms)),0)::bigint
FROM ai_runs r
JOIN users u ON u.id=r.student_id AND u.role='student' AND u.deleted_at IS NULL
WHERE ($1::uuid IS NULL OR r.student_id=$1)
  AND ($2::uuid IS NULL OR r.model_id=$2)
  AND ($3='' OR r.status=$3)
  AND ($4::timestamptz IS NULL OR r.created_at >= $4)
  AND ($5::timestamptz IS NULL OR r.created_at <= $5)`,
		nullableUUIDUsage(filter.StudentID), nullableUUIDUsage(filter.ModelID), filter.Status,
		nullableTimeUsage(filter.From), nullableTimeUsage(filter.To)).
		Scan(&out.Requests, &out.Succeeded, &out.Failed, &out.InputTokens, &out.OutputTokens,
			&out.CostMicroUSD, &out.UnknownUsage, &out.AverageFirstByteMS, &out.AverageTotalMS)
	if err != nil {
		return UsageSummary{}, runtimeDBError(err)
	}
	return out, nil
}

func (s *PostgresAdminUsageService) UsageRuns(ctx context.Context, p Principal, filter UsageFilter) ([]UsageRun, UsageCursor, error) {
	if err := admin(p); err != nil {
		return nil, UsageCursor{}, err
	}
	if err := validateUsageFilter(filter); err != nil {
		return nil, UsageCursor{}, err
	}
	cursorAt, cursorID := filter.Cursor.CreatedAt, filter.Cursor.ID
	if cursorAt.IsZero() {
		cursorAt = time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)
		cursorID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	}
	rows, err := s.pool.Query(ctx, `
SELECT r.id,r.student_id,u.username,u.display_name,r.model_id,r.upstream_model_id,r.status,
       COALESCE(r.input_tokens,0),COALESCE(r.output_tokens,0),COALESCE(r.usage_source,''),
       COALESCE(r.cost_micro_usd,0),r.first_byte_ms,r.total_ms,COALESCE(r.error_code,''),
       r.created_at,r.started_at,r.completed_at
FROM ai_runs r
JOIN users u ON u.id=r.student_id AND u.role='student' AND u.deleted_at IS NULL
WHERE ($1::uuid IS NULL OR r.student_id=$1)
  AND ($2::uuid IS NULL OR r.model_id=$2)
  AND ($3='' OR r.status=$3)
  AND ($4::timestamptz IS NULL OR r.created_at >= $4)
  AND ($5::timestamptz IS NULL OR r.created_at <= $5)
  AND (r.created_at,r.id)<($6,$7)
ORDER BY r.created_at DESC,r.id DESC
LIMIT $8`, nullableUUIDUsage(filter.StudentID), nullableUUIDUsage(filter.ModelID), filter.Status,
		nullableTimeUsage(filter.From), nullableTimeUsage(filter.To), cursorAt, cursorID, filter.Limit)
	if err != nil {
		return nil, UsageCursor{}, runtimeDBError(err)
	}
	defer rows.Close()
	out := make([]UsageRun, 0, filter.Limit)
	for rows.Next() {
		var item UsageRun
		if err = rows.Scan(&item.ID, &item.StudentID, &item.StudentUsername, &item.StudentDisplayName,
			&item.ModelID, &item.ModelLabel, &item.Status, &item.InputTokens, &item.OutputTokens,
			&item.UsageSource, &item.CostMicroUSD, &item.FirstByteMS, &item.TotalMS, &item.ErrorCategory,
			&item.CreatedAt, &item.StartedAt, &item.CompletedAt); err != nil {
			return nil, UsageCursor{}, runtimeDBError(err)
		}
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, UsageCursor{}, runtimeDBError(err)
	}
	var next UsageCursor
	if len(out) == filter.Limit {
		last := out[len(out)-1]
		next = UsageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return out, next, nil
}

type usageCursorWire struct {
	CreatedAt string `json:"createdAt"`
	ID        string `json:"id"`
}

func encodeUsageCursor(v UsageCursor) string {
	if v.CreatedAt.IsZero() || v.ID == uuid.Nil {
		return ""
	}
	return encodeAICursor(usageCursorWire{CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339Nano), ID: v.ID.String()})
}

func decodeUsageCursor(raw string, now time.Time) (UsageCursor, error) {
	if raw == "" {
		return UsageCursor{}, nil
	}
	var wire usageCursorWire
	if err := decodeAICursor(raw, &wire); err != nil {
		return UsageCursor{}, ErrInvalidInput
	}
	at, id, err := aiCursorParts(wire.CreatedAt, wire.ID, now)
	if err != nil {
		return UsageCursor{}, ErrInvalidInput
	}
	out := UsageCursor{CreatedAt: at, ID: id}
	if encodeUsageCursor(out) != raw {
		return UsageCursor{}, ErrInvalidInput
	}
	return out, nil
}

func nullableUUIDUsage(v uuid.UUID) any {
	if v == uuid.Nil {
		return nil
	}
	return v
}

func nullableTimeUsage(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v
}

func validUsageTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || at.Location() != time.UTC || at.Format(time.RFC3339Nano) != strings.TrimSpace(raw) {
		return time.Time{}, ErrInvalidInput
	}
	return at, nil
}
