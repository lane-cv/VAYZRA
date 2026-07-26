package aiqa

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRunnerStore struct {
	pool        *pgxpool.Pool
	box         SecretBox
	attachments AttachmentContextStore
}

func NewPostgresRunnerStore(pool *pgxpool.Pool, box SecretBox, attachments AttachmentContextStore) *PostgresRunnerStore {
	return &PostgresRunnerStore{pool: pool, box: box, attachments: attachments}
}

func (s *PostgresRunnerStore) LeaseNext(ctx context.Context, owner string, now time.Time, duration time.Duration) (leased LeasedRun, err error) {
	if s == nil || s.pool == nil || s.box == nil || owner == "" || len(owner) > 128 || duration <= 0 {
		return leased, ErrInvalidInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return leased, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var studentID uuid.UUID
	var rawURL string
	var encrypted []byte
	var keyVersion int16
	var connectMS, headerMS, idleMS, totalMS int64
	var promptSHA string
	err = tx.QueryRow(ctx, `
WITH candidate AS (
 SELECT id FROM ai_runs
 WHERE status='queued'
 ORDER BY created_at,id
 FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE ai_runs r SET status='streaming',lease_owner=$1,lease_expires_at=$2,heartbeat_at=$3,
 started_at=$3,updated_at=$3
FROM candidate c WHERE r.id=c.id
RETURNING r.id,r.thread_id,r.student_id,r.trigger_message_id,r.status,r.attempt_no,r.last_sequence,
 r.modality,r.reserved_token_count,r.created_at,r.updated_at,
 r.provider_id,r.provider_base_url,r.protocol_mode,r.model_id,r.upstream_model_id,
 r.context_window_tokens,r.max_output_tokens,r.image_quota_tokens,
 r.input_price_micro_usd_per_million_tokens,r.output_price_micro_usd_per_million_tokens,
 r.prompt_id,r.prompt_subject,r.prompt_version,r.prompt_sha256,
 r.connect_timeout_ms,r.response_header_timeout_ms,r.idle_stream_timeout_ms,r.total_timeout_ms`,
		owner, now.Add(duration), now).Scan(
		&leased.Run.ID, &leased.Run.ThreadID, &studentID, &leased.Run.TriggerMessageID, &leased.Run.Status,
		&leased.Run.AttemptNo, &leased.Run.LastSequence, &leased.Run.Modality, &leased.Run.ReservedTokenCount,
		&leased.Run.CreatedAt, &leased.Run.UpdatedAt,
		&leased.Config.ProviderID, &rawURL, &leased.Config.ProtocolMode, &leased.Config.Model.ID,
		&leased.Config.Model.UpstreamModelID, &leased.Config.Model.ContextTokens, &leased.Config.Model.MaxOutputTokens,
		&leased.Config.Model.ImageQuotaTokens, &leased.Config.Model.InputPriceMicroUSD,
		&leased.Config.Model.OutputPriceMicroUSD, &leased.Config.Prompt.ID, &leased.Config.Prompt.Subject,
		&leased.Config.Prompt.Version, &promptSHA, &connectMS, &headerMS, &idleMS, &totalMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return LeasedRun{}, ErrNoRunnableRun
	}
	if err != nil {
		return LeasedRun{}, err
	}
	leased.Config.Model.ProviderID = leased.Config.ProviderID
	leased.Config.Model.Modality = leased.Run.Modality
	leased.Config.Model.Enabled = true
	leased.Config.Prompt.Active = true
	leased.Config.Timeouts = GatewayTimeouts{
		Connect: time.Duration(connectMS) * time.Millisecond, ResponseHeader: time.Duration(headerMS) * time.Millisecond,
		IdleStream: time.Duration(idleMS) * time.Millisecond, Total: time.Duration(totalMS) * time.Millisecond,
	}
	leased.Config.BaseURL, err = url.Parse(rawURL)
	if err != nil || leased.Config.BaseURL.Scheme == "" || leased.Config.BaseURL.Host == "" {
		return LeasedRun{}, ErrProviderUnavailable
	}
	err = tx.QueryRow(ctx, `SELECT encrypted_api_key,key_version FROM ai_providers WHERE id=$1`, leased.Config.ProviderID).
		Scan(&encrypted, &keyVersion)
	if err != nil {
		return LeasedRun{}, err
	}
	err = tx.QueryRow(ctx, `SELECT system_prompt FROM prompt_templates
WHERE id=$1 AND subject=$2 AND version=$3 AND encode(digest(system_prompt,'sha256'),'hex')=$4`,
		leased.Config.Prompt.ID, leased.Config.Prompt.Subject, leased.Config.Prompt.Version, promptSHA).Scan(&leased.Config.Prompt.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return LeasedRun{}, ErrProviderUnavailable
	}
	if err != nil {
		return LeasedRun{}, err
	}
	err = tx.QueryRow(ctx, `SELECT body_text FROM ai_messages WHERE id=$1 AND thread_id=$2`,
		leased.Run.TriggerMessageID, leased.Run.ThreadID).Scan(&leased.Run.TriggerBody)
	if err != nil {
		return LeasedRun{}, err
	}
	rows, err := tx.Query(ctx, `SELECT role,body_text FROM ai_messages
WHERE thread_id=$1 AND (created_at,id)<(SELECT created_at,id FROM ai_messages WHERE id=$2)
ORDER BY created_at,id`, leased.Run.ThreadID, leased.Run.TriggerMessageID)
	if err != nil {
		return LeasedRun{}, err
	}
	var history []Message
	for rows.Next() {
		var message Message
		if err = rows.Scan(&message.Role, &message.Body); err != nil {
			rows.Close()
			return LeasedRun{}, err
		}
		history = append(history, message)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return LeasedRun{}, err
	}
	leased.LeaseOwner = owner
	if err = tx.Commit(ctx); err != nil {
		return LeasedRun{}, err
	}
	leased.Config.APIKey, err = s.box.Open(leased.Config.ProviderID, EncryptedSecret{KeyVersion: keyVersion, Blob: encrypted})
	if err != nil {
		s.failPreparation(ctx, leased, duration, "provider_secret_unavailable")
		return LeasedRun{}, ErrNoRunnableRun
	}
	var extracted strings.Builder
	imageCount := 0
	attachments, attachmentErr := loadMessageAttachments(ctx, s.pool, studentID, leased.Run.TriggerMessageID)
	if attachmentErr == nil {
		for _, attachment := range attachments {
			attachment := attachment
			if s.attachments == nil {
				attachmentErr = ErrAttachmentNotReady
				break
			}
			if attachment.Modality == ModalityVision {
				imageCount++
				leased.Request.Images = append(leased.Request.Images, GatewayImage{
					MediaType: attachment.DetectedMIME, Size: attachment.Size,
					Open: func(openCtx context.Context) (io.ReadCloser, error) {
						body, mediaType, size, openErr := s.attachments.OpenAIImage(openCtx, studentID, attachment.FileVersionID)
						if openErr != nil || mediaType != attachment.DetectedMIME || size != attachment.Size {
							if body != nil {
								_ = body.Close()
							}
							return nil, ErrAttachmentNotReady
						}
						return body, nil
					},
				})
				continue
			}
			var text string
			text, attachmentErr = s.attachments.LoadAIText(ctx, studentID, attachment.FileVersionID)
			if attachmentErr != nil {
				break
			}
			if extracted.Len() > 0 {
				extracted.WriteString("\n\n")
			}
			extracted.WriteString(text)
		}
	}
	if attachmentErr != nil {
		s.failPreparation(ctx, leased, duration, "attachment_unavailable")
		zeroBytes(leased.Config.APIKey)
		return LeasedRun{}, ErrNoRunnableRun
	}
	turns, err := selectContext(leased.Config.Prompt.Body, history, leased.Run.TriggerBody, extracted.String(), imageCount,
		leased.Config.Model.ImageQuotaTokens, leased.Config.Model.ContextTokens, leased.Config.Model.MaxOutputTokens)
	if err != nil {
		s.failPreparation(ctx, leased, duration, "context_too_large")
		zeroBytes(leased.Config.APIKey)
		return LeasedRun{}, ErrNoRunnableRun
	}
	current := leased.Run.TriggerBody
	if extracted.Len() > 0 {
		current += "\n\nAttachment text:\n" + extracted.String()
	}
	leased.Request.RunID = leased.Run.ID
	leased.Request.Model = leased.Config.Model.UpstreamModelID
	leased.Request.SystemPrompt = leased.Config.Prompt.Body
	leased.Request.MaxOutputTokens = int(leased.Config.Model.MaxOutputTokens)
	leased.Request.Turns = make([]GatewayTurn, 0, len(turns)+1)
	for _, turn := range turns {
		leased.Request.Turns = append(leased.Request.Turns, GatewayTurn{Role: turn.Role, Text: turn.Text})
	}
	leased.Request.Turns = append(leased.Request.Turns, GatewayTurn{Role: "student", Text: current})
	return leased, nil
}

func (s *PostgresRunnerStore) failPreparation(ctx context.Context, leased LeasedRun, leaseDuration time.Duration, code string) {
	failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runnerTerminalTimeout(leaseDuration))
	defer cancel()
	_ = s.Fail(failCtx, leased, Failure{Status: RunFailed, ErrorCode: code, UsageSource: "unknown"})
}

func (s *PostgresRunnerStore) Heartbeat(ctx context.Context, runID uuid.UUID, owner string, leaseUntil time.Time) error {
	if s == nil || s.pool == nil || runID == uuid.Nil || owner == "" {
		return ErrInvalidInput
	}
	var cancelRequested bool
	err := s.pool.QueryRow(ctx, `UPDATE ai_runs SET heartbeat_at=now(),lease_expires_at=$3,updated_at=now()
WHERE id=$1 AND status='streaming' AND lease_owner=$2
RETURNING cancel_requested_at IS NOT NULL`, runID, owner, leaseUntil).Scan(&cancelRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunnerLeaseLost
	}
	if err != nil {
		return err
	}
	if cancelRequested {
		return ErrCancelRequested
	}
	return nil
}

func (s *PostgresRunnerStore) AppendEvents(ctx context.Context, runID uuid.UUID, owner string, events []RunEvent) error {
	if s == nil || s.pool == nil || runID == uuid.Nil || owner == "" || len(events) == 0 {
		return ErrInvalidInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sequence int64
	var cancelRequested bool
	err = tx.QueryRow(ctx, `SELECT last_sequence,cancel_requested_at IS NOT NULL FROM ai_runs
WHERE id=$1 AND status='streaming' AND lease_owner=$2 FOR UPDATE`, runID, owner).Scan(&sequence, &cancelRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunnerLeaseLost
	}
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.Sequence != sequence+1 || (event.Kind != "delta" && event.Kind != "usage") || len(event.Delta) > MaxGatewayEventBytes {
			return ErrInvalidInput
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		if _, err = tx.Exec(ctx, `INSERT INTO ai_run_events(run_id,sequence,kind,payload_text,error_code,created_at)
VALUES($1,$2,$3,$4,$5,$6)`, runID, event.Sequence, event.Kind, event.Delta, nilIfEmpty(event.ErrorCode), event.CreatedAt); err != nil {
			return err
		}
		sequence = event.Sequence
	}
	tag, err := tx.Exec(ctx, `UPDATE ai_runs SET last_sequence=$3,updated_at=now()
WHERE id=$1 AND status='streaming' AND lease_owner=$2`, runID, owner, sequence)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRunnerLeaseLost
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if cancelRequested {
		return ErrCancelRequested
	}
	return nil
}

func (s *PostgresRunnerStore) Complete(ctx context.Context, leased LeasedRun, completion Completion) error {
	if s == nil || s.pool == nil || leased.Run.ID == uuid.Nil || leased.LeaseOwner == "" ||
		!validBody(completion.Answer) || completion.InputTokens < 0 || completion.OutputTokens < 0 ||
		completion.CostMicroUSD < 0 || (completion.UsageSource != "upstream" && completion.UsageSource != "estimated") {
		return ErrInvalidInput
	}
	tx, studentID, err := s.lockTerminal(ctx, leased)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var threadID, modelID uuid.UUID
	var reservedRequests, reservedTokens, sequence int64
	var day, month string
	var cancelRequested bool
	err = tx.QueryRow(ctx, `SELECT thread_id,model_id,reserved_request_count,reserved_token_count,
quota_day_key,quota_month_key,last_sequence,cancel_requested_at IS NOT NULL
FROM ai_runs WHERE id=$1 AND status='streaming' AND lease_owner=$2 FOR UPDATE`,
		leased.Run.ID, leased.LeaseOwner).Scan(&threadID, &modelID, &reservedRequests, &reservedTokens, &day, &month, &sequence, &cancelRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunnerLeaseLost
	}
	if err != nil {
		return err
	}
	if cancelRequested {
		return ErrCancelRequested
	}
	now := time.Now().UTC()
	actual := completion.InputTokens
	if completion.OutputTokens > math.MaxInt64-actual {
		actual = math.MaxInt64
	} else {
		actual += completion.OutputTokens
	}
	charged := actual
	if charged > reservedTokens {
		charged = reservedTokens
		if _, err = tx.Exec(ctx, `UPDATE ai_models SET quota_blocked_at=$2,quota_block_reason='quota_estimation_anomaly',
updated_at=$2,version=version+1 WHERE id=$1 AND quota_blocked_at IS NULL`, modelID, now); err != nil {
			return err
		}
	}
	sequence++
	if _, err = tx.Exec(ctx, `INSERT INTO ai_run_events(run_id,sequence,kind,payload_text,created_at)
VALUES($1,$2,'completed','',$3)`, leased.Run.ID, sequence, now); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE ai_runs SET status='succeeded',lease_owner=NULL,lease_expires_at=NULL,
heartbeat_at=NULL,completed_at=$3,updated_at=$3,input_tokens=$4,output_tokens=$5,cost_micro_usd=$6,
usage_source=$7,first_byte_ms=$8,total_ms=$9,finish_reason=$10,error_code=NULL,last_sequence=$11
WHERE id=$1 AND lease_owner=$2 AND status='streaming'`, leased.Run.ID, leased.LeaseOwner, now,
		completion.InputTokens, completion.OutputTokens, completion.CostMicroUSD, completion.UsageSource,
		completion.FirstByteMS, completion.TotalMS, nilIfEmpty(completion.FinishReason), sequence)
	if err != nil || tag.RowsAffected() != 1 {
		if err == nil {
			err = ErrRunnerLeaseLost
		}
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ai_messages(id,thread_id,role,body_text,trigger_run_id,created_at)
VALUES($1,$2,'assistant',$3,$4,$5)`, uuid.New(), threadID, completion.Answer, leased.Run.ID, now); err != nil {
		return err
	}
	if err = settleRunnerQuota(ctx, tx, studentID, leased.Run.ID, day, month, reservedRequests, reservedTokens, charged, now, true); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresRunnerStore) Fail(ctx context.Context, leased LeasedRun, failure Failure) error {
	if s == nil || s.pool == nil || leased.Run.ID == uuid.Nil || leased.LeaseOwner == "" ||
		(failure.Status != RunFailed && failure.Status != RunCancelled) || failure.ErrorCode == "" ||
		failure.InputTokens < 0 || failure.OutputTokens < 0 || failure.CostMicroUSD < 0 {
		return ErrInvalidInput
	}
	tx, studentID, err := s.lockTerminal(ctx, leased)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var reservedRequests, reservedTokens, sequence int64
	var day, month string
	err = tx.QueryRow(ctx, `SELECT reserved_request_count,reserved_token_count,quota_day_key,quota_month_key,last_sequence
FROM ai_runs WHERE id=$1 AND status='streaming' AND lease_owner=$2 FOR UPDATE`,
		leased.Run.ID, leased.LeaseOwner).Scan(&reservedRequests, &reservedTokens, &day, &month, &sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunnerLeaseLost
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	kind := "failed"
	if failure.Status == RunCancelled {
		kind = "cancelled"
	}
	sequence++
	if _, err = tx.Exec(ctx, `INSERT INTO ai_run_events(run_id,sequence,kind,payload_text,error_code,created_at)
VALUES($1,$2,$3,'',$4,$5)`, leased.Run.ID, sequence, kind, failure.ErrorCode, now); err != nil {
		return err
	}
	usageSource := failure.UsageSource
	if usageSource == "" {
		usageSource = "unknown"
	}
	var inputTokens, outputTokens, cost any
	if usageSource == "upstream" || usageSource == "estimated" {
		inputTokens, outputTokens, cost = failure.InputTokens, failure.OutputTokens, failure.CostMicroUSD
	} else if usageSource != "unknown" {
		return ErrInvalidInput
	}
	tag, err := tx.Exec(ctx, `UPDATE ai_runs SET status=$3,lease_owner=NULL,lease_expires_at=NULL,
heartbeat_at=NULL,completed_at=$4,updated_at=$4,usage_source=$5,error_code=$6,total_ms=$7,last_sequence=$8,
input_tokens=$9,output_tokens=$10,cost_micro_usd=$11
WHERE id=$1 AND lease_owner=$2 AND status='streaming'`, leased.Run.ID, leased.LeaseOwner, failure.Status,
		now, usageSource, failure.ErrorCode, failure.TotalMS, sequence, inputTokens, outputTokens, cost)
	if err != nil || tag.RowsAffected() != 1 {
		if err == nil {
			err = ErrRunnerLeaseLost
		}
		return err
	}
	if err = settleRunnerQuota(ctx, tx, studentID, leased.Run.ID, day, month, reservedRequests, reservedTokens, 0, now, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresRunnerStore) lockTerminal(ctx context.Context, leased LeasedRun) (pgx.Tx, uuid.UUID, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, uuid.Nil, err
	}
	var studentID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT student_id FROM ai_runs WHERE id=$1`, leased.Run.ID).Scan(&studentID)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return nil, uuid.Nil, ErrRunnerLeaseLost
	}
	if err == nil {
		err = lockStudent(ctx, tx, studentID)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, uuid.Nil, err
	}
	return tx, studentID, nil
}

func settleRunnerQuota(ctx context.Context, tx pgx.Tx, studentID, runID uuid.UUID, day, month string,
	requests, reservedTokens, charged int64, now time.Time, settle bool) error {
	for _, period := range []struct{ kind, key string }{{"day", day}, {"month", month}} {
		if settle {
			if _, err := tx.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,action,period_kind,period_key,request_delta,token_delta,created_at)
VALUES($1,$2,'settle',$3,$4,$5,$6,$7) ON CONFLICT(run_id,period_kind,action) DO NOTHING`,
				studentID, runID, period.kind, period.key, requests, charged, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,action,period_kind,period_key,request_delta,token_delta,created_at)
VALUES($1,$2,'release',$3,$4,$5,$6,$7) ON CONFLICT(run_id,period_kind,action) DO NOTHING`,
			studentID, runID, period.kind, period.key, -requests, -reservedTokens, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresRunnerStore) ReconcileExpired(ctx context.Context, now time.Time, limit int) error {
	if s == nil || s.pool == nil {
		return ErrInvalidInput
	}
	if limit <= 0 {
		limit = 100
	}
	// Current schema transitions a claim directly from queued to streaming, so
	// queued rows normally have no lease. Clearing a stale queued lease keeps
	// reconciliation forward-compatible without touching terminal rows.
	if _, err := s.pool.Exec(ctx, `UPDATE ai_runs SET lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,updated_at=$1
WHERE status='queued' AND lease_expires_at<$1`, now); err != nil {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT id,lease_owner FROM ai_runs
WHERE status='streaming' AND lease_expires_at<$1 ORDER BY lease_expires_at,id LIMIT $2`, now, limit)
	if err != nil {
		return err
	}
	type expired struct {
		id    uuid.UUID
		owner string
	}
	var runs []expired
	for rows.Next() {
		var run expired
		if err = rows.Scan(&run.id, &run.owner); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, run)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, run := range runs {
		err = s.Fail(ctx, LeasedRun{Run: Run{ID: run.id}, LeaseOwner: run.owner}, Failure{
			Status: RunFailed, ErrorCode: "runner_lost", UsageSource: "unknown",
		})
		if err != nil && !errors.Is(err, ErrRunnerLeaseLost) {
			return fmt.Errorf("reconcile AI run: %w", err)
		}
	}
	return nil
}

var _ RunnerStore = (*PostgresRunnerStore)(nil)
