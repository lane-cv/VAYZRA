package aiqa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRuntimeStore struct{ pool *pgxpool.Pool }

func NewPostgresRuntimeStore(pool *pgxpool.Pool) *PostgresRuntimeStore {
	return &PostgresRuntimeStore{pool: pool}
}

func (s *PostgresRuntimeStore) AdmitRun(ctx context.Context, in RuntimeAdmission) (detail ThreadDetail, run Run, err error) {
	if !validAdmission(in) {
		return detail, run, ErrInvalidInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return detail, run, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockStudent(ctx, tx, in.StudentID); err != nil {
		return detail, run, err
	}
	run, found, err := findRunByKey(ctx, tx, in.StudentID, in.IdempotencyKey)
	if err != nil {
		return detail, Run{}, err
	}
	if found {
		detail, err = getThreadTx(ctx, tx, in.StudentID, run.ThreadID, MessageCursor{})
		if err != nil {
			return detail, Run{}, err
		}
		run.TriggerAttachments, err = loadMessageAttachments(ctx, tx, in.StudentID, run.TriggerMessageID)
		if err != nil {
			return detail, Run{}, err
		}
		sameThread := (!in.CreateThread && run.ThreadID == in.ThreadID) ||
			(in.CreateThread && detail.Thread.Title == in.ThreadTitle && detail.Thread.Subject == in.Subject)
		if !sameThread || run.TriggerBody != in.MessageBody || !sameMetadataBindings(in.Attachments, run.TriggerAttachments) {
			return ThreadDetail{}, Run{}, ErrRunConflict
		}
		err = tx.Commit(ctx)
		return detail, run, err
	}
	if err = validateActiveStudent(ctx, tx, in.StudentID); err != nil {
		return detail, run, err
	}
	if !in.CreateThread {
		var subject Subject
		err = tx.QueryRow(ctx, `SELECT subject FROM ai_threads WHERE id=$1 AND student_id=$2 FOR UPDATE`, in.ThreadID, in.StudentID).Scan(&subject)
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrNotFound
		} else if err == nil && subject != in.Subject {
			err = ErrNotFound
		}
	}
	if err != nil {
		return detail, run, err
	}
	if err = validateAndLockSnapshot(ctx, tx, in.Snapshot); err != nil {
		return detail, run, err
	}
	if err = reserveQuota(ctx, tx, in.StudentID, uuid.Nil, in.Reservation, false); err != nil {
		return detail, run, err
	}
	if in.CreateThread {
		_, err = tx.Exec(ctx, `INSERT INTO ai_threads(id,student_id,title,subject,last_message_at,created_at) VALUES($1,$2,$3,$4,$5,$5)`,
			in.ThreadID, in.StudentID, in.ThreadTitle, in.Subject, in.Now)
		if err != nil {
			return detail, run, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO ai_messages(id,thread_id,role,sender_user_id,body_text,idempotency_key,created_at)
VALUES($1,$2,'student',$3,$4,$5,$6)`, in.MessageID, in.ThreadID, in.StudentID, in.MessageBody, in.IdempotencyKey, in.Now)
	if err != nil {
		return detail, run, runtimeDBError(err)
	}
	for position, attachment := range in.Attachments {
		_, err = tx.Exec(ctx, `INSERT INTO ai_message_files(message_id,file_version_id,sort_position,display_name,created_at) VALUES($1,$2,$3,$4,$5)`,
			in.MessageID, attachment.FileVersionID, position, attachment.DisplayName, in.Now)
		if err != nil {
			return detail, run, runtimeDBError(err)
		}
	}
	runID := uuid.New()
	if err = insertRun(ctx, tx, runID, in.StudentID, in.ThreadID, in.MessageID, in.AttemptNo, in.IdempotencyKey, in.Snapshot, in.Reservation, in.Now); err != nil {
		return detail, run, runtimeDBError(err)
	}
	if err = insertReserveRows(ctx, tx, in.StudentID, runID, in.Reservation, in.Now); err != nil {
		return detail, run, err
	}
	if _, err = tx.Exec(ctx, `UPDATE ai_threads SET last_message_at=$3 WHERE id=$1 AND student_id=$2`, in.ThreadID, in.StudentID, in.Now); err != nil {
		return detail, run, err
	}
	run, err = scanRun(tx.QueryRow(ctx, runSelect+` WHERE r.id=$1 AND r.student_id=$2`, runID, in.StudentID))
	if err != nil {
		return detail, Run{}, err
	}
	detail, err = getThreadTx(ctx, tx, in.StudentID, in.ThreadID, MessageCursor{})
	if err != nil {
		return detail, Run{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ThreadDetail{}, Run{}, runtimeDBError(err)
	}
	return detail, run, nil
}

func (s *PostgresRuntimeStore) RetryRun(ctx context.Context, in RuntimeRetryAdmission) (detail ThreadDetail, run Run, err error) {
	if in.StudentID == uuid.Nil || in.SourceRunID == uuid.Nil || in.RunID == uuid.Nil || !validIdempotency(in.IdempotencyKey) {
		return detail, run, ErrInvalidInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return detail, run, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockStudent(ctx, tx, in.StudentID); err != nil {
		return detail, run, err
	}
	var threadID, messageID uuid.UUID
	var status RunStatus
	err = tx.QueryRow(ctx, `SELECT thread_id,trigger_message_id,status FROM ai_runs WHERE id=$1 AND student_id=$2 FOR UPDATE`, in.SourceRunID, in.StudentID).
		Scan(&threadID, &messageID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return detail, run, ErrNotFound
	}
	if err != nil {
		return detail, run, err
	}
	if status != RunFailed && status != RunCancelled {
		return detail, run, ErrRunConflict
	}
	if run, found, e := findRunByKey(ctx, tx, in.StudentID, in.IdempotencyKey); e != nil {
		return detail, Run{}, e
	} else if found {
		if run.TriggerMessageID != messageID {
			return ThreadDetail{}, Run{}, ErrRunConflict
		}
		detail, e = getThreadTx(ctx, tx, in.StudentID, run.ThreadID, MessageCursor{})
		if e == nil {
			e = tx.Commit(ctx)
		}
		return detail, run, e
	}
	var attempt int
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(attempt_no),0)+1 FROM ai_runs WHERE trigger_message_id=$1`, messageID).Scan(&attempt); err != nil {
		return detail, run, err
	}
	if err = validateAndLockSnapshot(ctx, tx, in.Snapshot); err != nil {
		return detail, run, err
	}
	if err = reserveQuota(ctx, tx, in.StudentID, uuid.Nil, in.Reservation, false); err != nil {
		return detail, run, err
	}
	if err = insertRun(ctx, tx, in.RunID, in.StudentID, threadID, messageID, attempt, in.IdempotencyKey, in.Snapshot, in.Reservation, in.Now); err != nil {
		return detail, run, runtimeDBError(err)
	}
	if err = insertReserveRows(ctx, tx, in.StudentID, in.RunID, in.Reservation, in.Now); err != nil {
		return detail, run, err
	}
	run, err = scanRun(tx.QueryRow(ctx, runSelect+` WHERE r.id=$1 AND r.student_id=$2`, in.RunID, in.StudentID))
	if err != nil {
		return detail, Run{}, err
	}
	detail, err = getThreadTx(ctx, tx, in.StudentID, threadID, MessageCursor{})
	if err == nil {
		err = tx.Commit(ctx)
	}
	return detail, run, runtimeDBError(err)
}

func (s *PostgresRuntimeStore) ListThreads(ctx context.Context, studentID uuid.UUID, cursor ThreadCursor) ([]Thread, ThreadCursor, error) {
	limit := cursor.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT id,student_id,title,subject,last_message_at,created_at FROM ai_threads
WHERE student_id=$1 AND ($2::timestamptz IS NULL OR (last_message_at,id)<($2,$3))
ORDER BY last_message_at DESC,id DESC LIMIT $4`, studentID, nullableTime(cursor.LastMessageAt), cursor.ID, limit+1)
	if err != nil {
		return nil, ThreadCursor{}, err
	}
	defer rows.Close()
	out := make([]Thread, 0, limit)
	for rows.Next() {
		var v Thread
		if err = rows.Scan(&v.ID, &v.StudentID, &v.Title, &v.Subject, &v.LastMessageAt, &v.CreatedAt); err != nil {
			return nil, ThreadCursor{}, err
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		return nil, ThreadCursor{}, err
	}
	var next ThreadCursor
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = ThreadCursor{LastMessageAt: last.LastMessageAt, ID: last.ID, Limit: limit}
	}
	return out, next, nil
}

func (s *PostgresRuntimeStore) GetThread(ctx context.Context, studentID, threadID uuid.UUID, cursor MessageCursor) (ThreadDetail, error) {
	return getThreadTx(ctx, s.pool, studentID, threadID, cursor)
}

func (s *PostgresRuntimeStore) LoadContext(ctx context.Context, studentID, threadID uuid.UUID) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.id,m.thread_id,m.role,m.body_text,coalesce(m.trigger_run_id,'00000000-0000-0000-0000-000000000000'::uuid),m.created_at
FROM ai_threads t JOIN ai_messages m ON m.thread_id=t.id WHERE t.id=$1 AND t.student_id=$2 ORDER BY m.created_at,m.id`, threadID, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Body, &m.RunID, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		var exists bool
		if err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_threads WHERE id=$1 AND student_id=$2)`, threadID, studentID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrNotFound
		}
	}
	return out, nil
}

func (s *PostgresRuntimeStore) GetRun(ctx context.Context, studentID, runID uuid.UUID) (Run, error) {
	run, err := scanRun(s.pool.QueryRow(ctx, runSelect+` WHERE r.id=$1 AND r.student_id=$2`, runID, studentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err == nil {
		run.TriggerAttachments, err = loadMessageAttachments(ctx, s.pool, studentID, run.TriggerMessageID)
	}
	return run, err
}

func (s *PostgresRuntimeStore) GetRunByIdempotency(ctx context.Context, studentID uuid.UUID, key string) (ThreadDetail, Run, error) {
	if studentID == uuid.Nil || !validIdempotency(key) {
		return ThreadDetail{}, Run{}, ErrNotFound
	}
	run, err := scanRun(s.pool.QueryRow(ctx, runSelect+` WHERE r.student_id=$1 AND r.idempotency_key=$2`, studentID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return ThreadDetail{}, Run{}, ErrNotFound
	}
	if err != nil {
		return ThreadDetail{}, Run{}, err
	}
	run.TriggerAttachments, err = loadMessageAttachments(ctx, s.pool, studentID, run.TriggerMessageID)
	if err != nil {
		return ThreadDetail{}, Run{}, err
	}
	detail, err := s.GetThread(ctx, studentID, run.ThreadID, MessageCursor{})
	return detail, run, err
}

func (s *PostgresRuntimeStore) CancelRun(ctx context.Context, studentID, runID uuid.UUID, now time.Time) (Run, error) {
	return s.releaseTerminal(ctx, studentID, runID, RunCancelled, "cancelled", now)
}

func (s *PostgresRuntimeStore) FailRun(ctx context.Context, studentID, runID uuid.UUID, errorCode string, now time.Time) (Run, error) {
	if errorCode == "" {
		errorCode = "run_failed"
	}
	return s.releaseTerminal(ctx, studentID, runID, RunFailed, errorCode, now)
}

func (s *PostgresRuntimeStore) releaseTerminal(ctx context.Context, studentID, runID uuid.UUID, target RunStatus, errorCode string, now time.Time) (run Run, err error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return run, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockStudent(ctx, tx, studentID); err != nil {
		return run, err
	}
	var status RunStatus
	var requestCount, tokenCount int64
	var day, month string
	err = tx.QueryRow(ctx, `SELECT status,reserved_request_count,reserved_token_count,quota_day_key,quota_month_key FROM ai_runs WHERE id=$1 AND student_id=$2 FOR UPDATE`, runID, studentID).
		Scan(&status, &requestCount, &tokenCount, &day, &month)
	if errors.Is(err, pgx.ErrNoRows) {
		return run, ErrNotFound
	}
	if err != nil {
		return run, err
	}
	if status == target {
		run, err = scanRun(tx.QueryRow(ctx, runSelect+` WHERE r.id=$1 AND r.student_id=$2`, runID, studentID))
		if err == nil {
			err = tx.Commit(ctx)
		}
		return run, err
	}
	if status == RunSucceeded || status == RunFailed || status == RunCancelled {
		return run, ErrRunConflict
	}
	if target == RunCancelled && status == RunStreaming {
		_, err = tx.Exec(ctx, `UPDATE ai_runs SET cancel_requested_at=COALESCE(cancel_requested_at,$3),updated_at=$3
WHERE id=$1 AND student_id=$2 AND status='streaming'`, runID, studentID, now)
		if err != nil {
			return run, runtimeDBError(err)
		}
		run, err = scanRun(tx.QueryRow(ctx, runSelect+` WHERE r.id=$1 AND r.student_id=$2`, runID, studentID))
		if err == nil {
			err = tx.Commit(ctx)
		}
		return run, runtimeDBError(err)
	}
	_, err = tx.Exec(ctx, `UPDATE ai_runs SET status=$3,lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,completed_at=$4,updated_at=$4,usage_source='unknown',error_code=$5 WHERE id=$1 AND student_id=$2`,
		runID, studentID, target, now, errorCode)
	if err != nil {
		return run, runtimeDBError(err)
	}
	for _, p := range []struct{ kind, key string }{{"day", day}, {"month", month}} {
		_, err = tx.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,action,period_kind,period_key,request_delta,token_delta,created_at)
VALUES($1,$2,'release',$3,$4,$5,$6,$7) ON CONFLICT(run_id,period_kind,action) DO NOTHING`,
			studentID, runID, p.kind, p.key, -requestCount, -tokenCount, now)
		if err != nil {
			return run, err
		}
	}
	run, err = scanRun(tx.QueryRow(ctx, runSelect+` WHERE r.id=$1 AND r.student_id=$2`, runID, studentID))
	if err == nil {
		err = tx.Commit(ctx)
	}
	return run, err
}

func (s *PostgresRuntimeStore) SucceedRun(ctx context.Context, studentID, runID uuid.UUID, assistantBody string, usage TerminalUsage, now time.Time) (run Run, err error) {
	if !validBody(assistantBody) || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CostMicroUSD < 0 || (usage.UsageSource != "upstream" && usage.UsageSource != "estimated") {
		return run, ErrInvalidInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return run, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = lockStudent(ctx, tx, studentID); err != nil {
		return run, err
	}
	var status RunStatus
	var threadID uuid.UUID
	var requestCount, reservedTokens int64
	var day, month string
	var modelID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT status,thread_id,reserved_request_count,reserved_token_count,quota_day_key,quota_month_key,model_id FROM ai_runs WHERE id=$1 AND student_id=$2 FOR UPDATE`, runID, studentID).
		Scan(&status, &threadID, &requestCount, &reservedTokens, &day, &month, &modelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return run, ErrNotFound
	}
	if err != nil {
		return run, err
	}
	if status == RunSucceeded {
		run, err = scanRun(tx.QueryRow(ctx, runSelect+` WHERE r.id=$1 AND r.student_id=$2`, runID, studentID))
		if err == nil {
			err = tx.Commit(ctx)
		}
		return run, err
	}
	if status != RunStreaming {
		return run, ErrRunConflict
	}
	actual := usage.InputTokens + usage.OutputTokens
	charged := actual
	if charged > reservedTokens {
		charged = reservedTokens
		if _, err = tx.Exec(ctx, `UPDATE ai_models SET quota_blocked_at=$2,quota_block_reason='quota_estimation_anomaly',updated_at=$2,version=version+1 WHERE id=$1 AND quota_blocked_at IS NULL`, modelID, now); err != nil {
			return run, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE ai_runs SET status='succeeded',lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,completed_at=$3,updated_at=$3,input_tokens=$4,output_tokens=$5,cost_micro_usd=$6,usage_source=$7,finish_reason=$8,error_code=NULL WHERE id=$1 AND student_id=$2`,
		runID, studentID, now, usage.InputTokens, usage.OutputTokens, usage.CostMicroUSD, usage.UsageSource, nilIfEmpty(usage.FinishReason))
	if err != nil {
		return run, runtimeDBError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO ai_messages(id,thread_id,role,body_text,trigger_run_id,created_at) VALUES($1,$2,'assistant',$3,$4,$5)`,
		uuid.New(), threadID, assistantBody, runID, now)
	if err != nil {
		return run, runtimeDBError(err)
	}
	for _, p := range []struct{ kind, key string }{{"day", day}, {"month", month}} {
		_, err = tx.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,action,period_kind,period_key,request_delta,token_delta,created_at)
VALUES($1,$2,'settle',$3,$4,$5,$6,$7) ON CONFLICT(run_id,period_kind,action) DO NOTHING`,
			studentID, runID, p.kind, p.key, requestCount, charged, now)
		if err != nil {
			return run, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,action,period_kind,period_key,request_delta,token_delta,created_at)
VALUES($1,$2,'release',$3,$4,$5,$6,$7) ON CONFLICT(run_id,period_kind,action) DO NOTHING`,
			studentID, runID, p.kind, p.key, -requestCount, -reservedTokens, now)
		if err != nil {
			return run, err
		}
	}
	run, err = scanRun(tx.QueryRow(ctx, runSelect+` WHERE r.id=$1 AND r.student_id=$2`, runID, studentID))
	if err == nil {
		err = tx.Commit(ctx)
	}
	return run, runtimeDBError(err)
}

func reserveQuota(ctx context.Context, tx pgx.Tx, studentID, _ uuid.UUID, reservation QuotaReservation, _ bool) error {
	var global QuotaLimits
	if err := tx.QueryRow(ctx, `SELECT daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit FROM ai_global_limits WHERE singleton FOR UPDATE`).
		Scan(&global.DailyRequests, &global.MonthlyRequests, &global.DailyTokens, &global.MonthlyTokens); err != nil {
		return err
	}
	var student StudentQuotaLimits
	err := tx.QueryRow(ctx, `SELECT daily_request_limit,monthly_request_limit,daily_token_limit,monthly_token_limit FROM student_ai_limits WHERE student_id=$1 FOR UPDATE`, studentID).
		Scan(&student.DailyRequests, &student.MonthlyRequests, &student.DailyTokens, &student.MonthlyTokens)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	limits, err := ResolveQuotaLimits(global, student)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT period_kind,request_delta,token_delta FROM ai_usage_ledger
WHERE student_id=$1 AND ((period_kind='day' AND period_key=$2) OR (period_kind='month' AND period_key=$3)) FOR UPDATE`, studentID, reservation.DayKey, reservation.MonthKey)
	if err != nil {
		return err
	}
	var used QuotaUsage
	for rows.Next() {
		var kind string
		var requests, tokens int64
		if err = rows.Scan(&kind, &requests, &tokens); err != nil {
			rows.Close()
			return err
		}
		if kind == "day" {
			used.DailyRequests += requests
			used.DailyTokens += tokens
		} else {
			used.MonthlyRequests += requests
			used.MonthlyTokens += tokens
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	return CheckQuota(limits, used, reservation)
}

func validateAndLockSnapshot(ctx context.Context, tx pgx.Tx, snapshot RuntimeSnapshot) error {
	p := snapshot.Provider
	if p.ProviderID == uuid.Nil || p.BaseURL == nil || p.Model.ID == uuid.Nil || p.Prompt.ID == uuid.Nil || len(snapshot.PromptSHA256) != 64 {
		return ErrAIDisabled
	}
	var providerID uuid.UUID
	var upstream string
	var modality Modality
	var contextTokens, maxOutput, imageQuota, inputPrice, outputPrice int64
	var enabled bool
	var blockedAt *time.Time
	var connectMS, headerMS, idleMS, totalMS int64
	err := tx.QueryRow(ctx, `SELECT provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,
input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,enabled,quota_blocked_at,
connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms
FROM ai_models WHERE id=$1 FOR UPDATE`, p.Model.ID).Scan(
		&providerID, &upstream, &modality, &contextTokens, &maxOutput, &imageQuota, &inputPrice, &outputPrice, &enabled, &blockedAt,
		&connectMS, &headerMS, &idleMS, &totalMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAIDisabled
	}
	if err != nil {
		return err
	}
	if providerID != p.ProviderID || upstream != p.Model.UpstreamModelID || modality != p.Model.Modality ||
		contextTokens != p.Model.ContextTokens || maxOutput != p.Model.MaxOutputTokens || imageQuota != p.Model.ImageQuotaTokens ||
		inputPrice != p.Model.InputPriceMicroUSD || outputPrice != p.Model.OutputPriceMicroUSD ||
		enabled != p.Model.Enabled || !enabled || blockedAt != nil || p.Model.QuotaBlockedAt != nil ||
		connectMS != p.Timeouts.Connect.Milliseconds() || headerMS != p.Timeouts.ResponseHeader.Milliseconds() ||
		idleMS != p.Timeouts.IdleStream.Milliseconds() || totalMS != p.Timeouts.Total.Milliseconds() {
		return ErrAIDisabled
	}
	var baseURL string
	var protocol ProtocolMode
	var providerActive bool
	err = tx.QueryRow(ctx, `SELECT base_url,protocol_mode,active FROM ai_providers WHERE id=$1`, p.ProviderID).
		Scan(&baseURL, &protocol, &providerActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAIDisabled
	}
	if err != nil {
		return err
	}
	if !providerActive || baseURL != p.BaseURL.String() || protocol != p.ProtocolMode {
		return ErrAIDisabled
	}
	var subject Subject
	var version int64
	var promptSHA []byte
	var promptActive bool
	err = tx.QueryRow(ctx, `SELECT subject,version,digest(system_prompt,'sha256'),active FROM prompt_templates WHERE id=$1`, p.Prompt.ID).
		Scan(&subject, &version, &promptSHA, &promptActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAIDisabled
	}
	if err != nil {
		return err
	}
	expectedSHA := sha256.Sum256([]byte(p.Prompt.Body))
	if !promptActive || subject != p.Prompt.Subject || version != p.Prompt.Version ||
		hex.EncodeToString(promptSHA) != snapshot.PromptSHA256 || snapshot.PromptSHA256 != hex.EncodeToString(expectedSHA[:]) {
		return ErrAIDisabled
	}
	return nil
}

func insertRun(ctx context.Context, tx pgx.Tx, runID, studentID, threadID, messageID uuid.UUID, attempt int, key string, snapshot RuntimeSnapshot, reservation QuotaReservation, now time.Time) error {
	p := snapshot.Provider
	_, err := tx.Exec(ctx, `INSERT INTO ai_runs(
id,thread_id,student_id,trigger_message_id,attempt_no,idempotency_key,status,
provider_id,provider_base_url,protocol_mode,model_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,image_quota_tokens,
input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,prompt_id,prompt_subject,prompt_version,prompt_sha256,
connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,
reserved_request_count,reserved_token_count,quota_day_key,quota_month_key,estimator_version,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,'queued',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$31)`,
		runID, threadID, studentID, messageID, attempt, key,
		p.ProviderID, p.BaseURL.String(), p.ProtocolMode, p.Model.ID, p.Model.UpstreamModelID, p.Model.Modality, p.Model.ContextTokens, p.Model.MaxOutputTokens, p.Model.ImageQuotaTokens,
		p.Model.InputPriceMicroUSD, p.Model.OutputPriceMicroUSD, p.Prompt.ID, p.Prompt.Subject, p.Prompt.Version, snapshot.PromptSHA256,
		p.Timeouts.Connect.Milliseconds(), p.Timeouts.ResponseHeader.Milliseconds(), p.Timeouts.IdleStream.Milliseconds(), p.Timeouts.Total.Milliseconds(),
		reservation.RequestCount, reservation.TokenCount, reservation.DayKey, reservation.MonthKey, reservation.EstimatorVersion, now)
	return err
}

func insertReserveRows(ctx context.Context, tx pgx.Tx, studentID, runID uuid.UUID, reservation QuotaReservation, now time.Time) error {
	for _, p := range []struct{ kind, key string }{{"day", reservation.DayKey}, {"month", reservation.MonthKey}} {
		if _, err := tx.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,action,period_kind,period_key,request_delta,token_delta,created_at)
VALUES($1,$2,'reserve',$3,$4,$5,$6,$7)`, studentID, runID, p.kind, p.key, reservation.RequestCount, reservation.TokenCount, now); err != nil {
			return err
		}
	}
	return nil
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const runSelect = `SELECT r.id,r.thread_id,r.trigger_message_id,m.body_text,r.status,r.attempt_no,r.last_sequence,coalesce(r.error_code,''),r.modality,r.reserved_token_count,r.created_at,r.updated_at FROM ai_runs r JOIN ai_messages m ON m.id=r.trigger_message_id`

func scanRun(row pgx.Row) (Run, error) {
	var run Run
	err := row.Scan(&run.ID, &run.ThreadID, &run.TriggerMessageID, &run.TriggerBody, &run.Status, &run.AttemptNo, &run.LastSequence, &run.ErrorCode, &run.Modality, &run.ReservedTokenCount, &run.CreatedAt, &run.UpdatedAt)
	return run, err
}

func findRunByKey(ctx context.Context, q queryRower, studentID uuid.UUID, key string) (Run, bool, error) {
	run, err := scanRun(q.QueryRow(ctx, runSelect+` WHERE r.student_id=$1 AND r.idempotency_key=$2`, studentID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, nil
	}
	return run, err == nil, err
}

func getThreadTx(ctx context.Context, q interface {
	queryRower
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, studentID, threadID uuid.UUID, cursor MessageCursor) (ThreadDetail, error) {
	var detail ThreadDetail
	err := q.QueryRow(ctx, `SELECT id,student_id,title,subject,last_message_at,created_at FROM ai_threads WHERE id=$1 AND student_id=$2`, threadID, studentID).
		Scan(&detail.Thread.ID, &detail.Thread.StudentID, &detail.Thread.Title, &detail.Thread.Subject, &detail.Thread.LastMessageAt, &detail.Thread.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return detail, ErrNotFound
	}
	if err != nil {
		return detail, err
	}
	limit := cursor.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := q.Query(ctx, `SELECT m.id,m.thread_id,m.role,m.body_text,coalesce(m.trigger_run_id,'00000000-0000-0000-0000-000000000000'::uuid),m.created_at
FROM ai_messages m JOIN ai_threads t ON t.id=m.thread_id
WHERE m.thread_id=$1 AND t.student_id=$2 AND ($3::timestamptz IS NULL OR (m.created_at,m.id)>($3,$4))
ORDER BY m.created_at,m.id LIMIT $5`, threadID, studentID, nullableTime(cursor.CreatedAt), cursor.ID, limit+1)
	if err != nil {
		return detail, err
	}
	for rows.Next() {
		var m Message
		if err = rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Body, &m.RunID, &m.CreatedAt); err != nil {
			rows.Close()
			return detail, err
		}
		detail.Messages = append(detail.Messages, m)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return detail, err
	}
	if len(detail.Messages) > limit {
		detail.Messages = detail.Messages[:limit]
		last := detail.Messages[len(detail.Messages)-1]
		detail.NextMessageCursor = MessageCursor{CreatedAt: last.CreatedAt, ID: last.ID, Limit: limit}
	}
	for i := range detail.Messages {
		attachments, e := loadMessageAttachments(ctx, q, studentID, detail.Messages[i].ID)
		if e != nil {
			return detail, e
		}
		detail.Messages[i].Attachments = attachments
	}
	active, e := scanRun(q.QueryRow(ctx, runSelect+` WHERE r.thread_id=$1 AND r.student_id=$2 AND r.status IN ('queued','streaming')`, threadID, studentID))
	if e == nil {
		detail.ActiveRun = &active
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return detail, e
	}
	return detail, nil
}

func loadMessageAttachments(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, studentID, messageID uuid.UUID) ([]AttachmentMetadata, error) {
	rows, err := q.Query(ctx, `SELECT mf.file_version_id,mf.display_name,coalesce(fv.detected_mime,''),fv.size_bytes,
CASE WHEN fv.detected_mime IN ('image/jpeg','image/png','image/webp','image/gif') THEN 'vision' ELSE 'text' END
FROM ai_message_files mf JOIN ai_messages m ON m.id=mf.message_id JOIN ai_threads t ON t.id=m.thread_id
JOIN file_versions fv ON fv.id=mf.file_version_id WHERE mf.message_id=$1 AND t.student_id=$2 ORDER BY mf.sort_position`, messageID, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttachmentMetadata
	for rows.Next() {
		var v AttachmentMetadata
		if err = rows.Scan(&v.FileVersionID, &v.DisplayName, &v.DetectedMIME, &v.Size, &v.Modality); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func lockStudent(ctx context.Context, tx pgx.Tx, studentID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, "ai-quota:"+studentID.String())
	return err
}

func validateActiveStudent(ctx context.Context, q queryRower, studentID uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='student' AND status='active' AND deleted_at IS NULL)`, studentID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func validAdmission(in RuntimeAdmission) bool {
	return in.StudentID != uuid.Nil && in.ThreadID != uuid.Nil && in.MessageID != uuid.Nil && validBody(in.MessageBody) &&
		validIdempotency(in.IdempotencyKey) && in.AttemptNo > 0 && (!in.CreateThread || (validTitle(in.ThreadTitle) && subjectOK(in.Subject))) &&
		in.Reservation.RequestCount > 0 && in.Reservation.TokenCount >= 0 && in.Reservation.DayKey != "" && in.Reservation.MonthKey != ""
}

func sameMetadataBindings(left, right []AttachmentMetadata) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].FileVersionID != right[i].FileVersionID {
			return false
		}
	}
	return true
}

func nullableTime(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v
}

func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func runtimeDBError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "23505" {
			switch pgErr.ConstraintName {
			case "ai_runs_one_active_student_idx":
				return ErrAIBusy
			case "ai_runs_student_id_idempotency_key_key", "ai_messages_sender_user_id_idempotency_key_key":
				return ErrRunConflict
			}
		}
	}
	return err
}

var _ RuntimeStore = (*PostgresRuntimeStore)(nil)
var _ RunTerminalStore = (*PostgresRuntimeStore)(nil)
