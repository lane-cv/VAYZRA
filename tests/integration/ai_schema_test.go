package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestAIRuntimeDoesNotWeakenTeacherQASchema(t *testing.T) {
	pool := integration.StartPostgres(t)
	if err := database.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	assertQAMessageStillRejectsAssistantRole(t, pool)
	assertQAMessageStillRequiresSenderUser(t, pool)
	assertAIMessageAcceptsAssistantWithoutUserID(t, pool)
}

func TestAISchemaHistoryAndRunConstraints(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	student, other := aiStudent(t, pool), aiStudent(t, pool)
	threadID := aiThread(t, pool, student)
	studentMessage := aiStudentMessage(t, pool, threadID, student, "ai-message-key-0001")
	config := aiRuntimeConfig(t, pool)

	for _, statement := range []struct {
		name string
		sql  string
		args []any
	}{
		{"message update", `UPDATE ai_messages SET body_text='changed' WHERE id=$1`, []any{studentMessage}},
		{"message delete", `DELETE FROM ai_messages WHERE id=$1`, []any{studentMessage}},
	} {
		t.Run(statement.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, statement.sql, statement.args...); err == nil {
				t.Fatal("immutable AI message mutation succeeded")
			}
		})
	}

	runID := aiRun(t, pool, threadID, student, studentMessage, 1, "ai-run-key-00000001", config)
	if _, err := pool.Exec(ctx, aiRunInsertSQL, uuid.New(), threadID, student, studentMessage, 2, "ai-run-key-00000002", "queued", config.providerID, config.modelID, config.promptID, config.promptSHA256); err == nil {
		t.Fatal("second active run for student succeeded")
	}
	if _, err := pool.Exec(ctx, aiRunTerminalInsertSQL, uuid.New(), threadID, student, studentMessage, 2, "ai-run-key-00000001", "failed", config.providerID, config.modelID, config.promptID, config.promptSHA256); err == nil {
		t.Fatal("duplicate student idempotency key succeeded")
	} else {
		assertUniqueConstraint(t, err, "ai_runs_student_id_idempotency_key_key")
	}
	if _, err := pool.Exec(ctx, aiRunTerminalInsertSQL, uuid.New(), threadID, student, studentMessage, 1, "ai-run-key-00000003", "failed", config.providerID, config.modelID, config.promptID, config.promptSHA256); err == nil {
		t.Fatal("duplicate trigger attempt succeeded")
	} else {
		assertUniqueConstraint(t, err, "ai_runs_trigger_message_id_attempt_no_key")
	}

	if _, err := pool.Exec(ctx, `INSERT INTO ai_run_events(run_id,sequence,kind,payload_text) VALUES($1,0,'delta','x')`, runID); err == nil {
		t.Fatal("zero event sequence succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_run_events(run_id,sequence,kind,payload_text) VALUES($1,1,'delta','x')`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_run_events SET payload_text='changed' WHERE run_id=$1 AND sequence=1`, runID); err == nil {
		t.Fatal("event update succeeded")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ai_run_events WHERE run_id=$1 AND sequence=1`, runID); err == nil {
		t.Fatal("event delete succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_run_events(run_id,sequence,kind,payload_text) VALUES($1,2,'delta',$2)`, runID, strings.Repeat("x", 16*1024+1)); err == nil {
		t.Fatal("oversized event payload succeeded")
	}

	if err := aiSucceedRunWithAssistant(ctx, pool, runID, threadID, "answer"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_runs SET status='failed' WHERE id=$1`, runID); err == nil {
		t.Fatal("terminal run transitioned again")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_messages(id,thread_id,role,body_text,trigger_run_id) VALUES($1,$2,'assistant','second answer',$3)`, uuid.New(), threadID, runID); err == nil {
		t.Fatal("second final assistant message succeeded")
	}

	var ledgerID int64
	if err := pool.QueryRow(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,period_kind,period_key,action,request_delta,token_delta) VALUES($1,$2,'day','2026-07-26','reserve',1,100) RETURNING id`, student, runID).Scan(&ledgerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_usage_ledger SET token_delta=99 WHERE id=$1`, ledgerID); err == nil {
		t.Fatal("ledger update succeeded")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ai_usage_ledger WHERE id=$1`, ledgerID); err == nil {
		t.Fatal("ledger delete succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,period_kind,period_key,action,request_delta,token_delta) VALUES($1,$2,'day','2026-07-26','settle',1,20)`, student, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,period_kind,period_key,action,request_delta,token_delta) VALUES($1,$2,'day','2026-07-26','settle',0,1)`, student, runID); err == nil {
		t.Fatal("duplicate terminal ledger action succeeded")
	}

	if _, err := pool.Exec(ctx, `UPDATE ai_threads SET student_id=$2 WHERE id=$1`, threadID, other); err == nil {
		t.Fatal("AI thread ownership mutation succeeded")
	}
}

func TestAISchemaCommitTimeFinalAssistantAndTerminalUsage(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	student := aiStudent(t, pool)
	threadID := aiThread(t, pool, student)
	messageID := aiStudentMessage(t, pool, threadID, student, "ai-message-key-0004")
	config := aiRuntimeConfig(t, pool)

	missingFinalRun := aiRun(t, pool, threadID, student, messageID, 1, "ai-run-key-00000006", config)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_runs SET status='streaming',lease_owner='worker-1',lease_expires_at=now()+interval '1 minute',heartbeat_at=now(),started_at=now() WHERE id=$1`, missingFinalRun); err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_runs SET status='succeeded',lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,completed_at=now(),usage_source='upstream',input_tokens=1,output_tokens=1,cost_micro_usd=0 WHERE id=$1`, missingFinalRun); err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("succeeded run without a final assistant message committed")
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_runs SET status='failed',completed_at=now(),error_code='test_cleanup',usage_source='unknown' WHERE id=$1`, missingFinalRun); err != nil {
		t.Fatal(err)
	}

	validRun := aiRun(t, pool, threadID, student, messageID, 2, "ai-run-key-00000007", config)
	if err := aiSucceedRunWithAssistant(ctx, pool, validRun, threadID, "final answer"); err != nil {
		t.Fatalf("valid succeeded run and final assistant message transaction: %v", err)
	}

	for _, status := range []string{"failed", "cancelled"} {
		runID := aiRun(t, pool, threadID, student, messageID, map[string]int{"failed": 3, "cancelled": 4}[status], "ai-run-key-0000000"+map[string]string{"failed": "8", "cancelled": "9"}[status], config)
		if _, err := pool.Exec(ctx, `UPDATE ai_runs SET status=$2,completed_at=now(),error_code='upstream_failed' WHERE id=$1`, runID, status); err == nil {
			t.Fatalf("%s terminal run without usage provenance succeeded", status)
		}
		if _, err := pool.Exec(ctx, `UPDATE ai_runs SET status=$2,completed_at=now(),error_code='upstream_failed',usage_source='unknown' WHERE id=$1`, runID, status); err != nil {
			t.Fatalf("%s unknown usage representation rejected: %v", status, err)
		}
	}

	incompleteUpstreamRun := aiRun(t, pool, threadID, student, messageID, 5, "ai-run-key-00000010", config)
	if _, err := pool.Exec(ctx, `UPDATE ai_runs SET status='failed',completed_at=now(),error_code='upstream_failed',usage_source='upstream' WHERE id=$1`, incompleteUpstreamRun); err == nil {
		t.Fatal("upstream terminal usage without token and cost fields succeeded")
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_runs SET status='failed',completed_at=now(),error_code='upstream_failed',usage_source='unknown' WHERE id=$1`, incompleteUpstreamRun); err != nil {
		t.Fatal(err)
	}
	completeEstimatedRun := aiRun(t, pool, threadID, student, messageID, 6, "ai-run-key-00000011", config)
	if _, err := pool.Exec(ctx, `UPDATE ai_runs SET status='cancelled',completed_at=now(),error_code='cancelled',usage_source='estimated',input_tokens=1,output_tokens=2,cost_micro_usd=3 WHERE id=$1`, completeEstimatedRun); err != nil {
		t.Fatalf("complete estimated terminal usage rejected: %v", err)
	}
}

func TestAISchemaSnapshotAndLedgerIntegrity(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	config := aiRuntimeConfig(t, pool)
	for _, tc := range []struct {
		name, statement, hash string
	}{
		{"provider base URL", strings.Replace(aiRunInsertSQL, "https://provider.invalid", "https://wrong.invalid", 1), config.promptSHA256},
		{"provider protocol", strings.Replace(aiRunInsertSQL, "chat_completions", "responses", 1), config.promptSHA256},
		{"model token limits", strings.Replace(aiRunInsertSQL, "'model','text',1000,100,10,0,0", "'model','text',2000,100,10,0,0", 1), config.promptSHA256},
		{"model prices", strings.Replace(aiRunInsertSQL, "'model','text',1000,100,10,0,0", "'model','text',1000,100,10,1,0", 1), config.promptSHA256},
		{"prompt hash", aiRunInsertSQL, strings.Repeat("f", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			student := aiStudent(t, pool)
			threadID := aiThread(t, pool, student)
			messageID := aiStudentMessage(t, pool, threadID, student, "ai-message-key-"+uuid.NewString()[:16])
			if _, err := pool.Exec(ctx, tc.statement, uuid.New(), threadID, student, messageID, 1, "ai-run-key-"+uuid.NewString()[:16], "queued", config.providerID, config.modelID, config.promptID, tc.hash); err == nil {
				t.Fatal("mismatched immutable configuration snapshot succeeded")
			}
		})
	}

	student, other := aiStudent(t, pool), aiStudent(t, pool)
	threadID := aiThread(t, pool, student)
	messageID := aiStudentMessage(t, pool, threadID, student, "ai-message-key-0005")
	runID := aiRun(t, pool, threadID, student, messageID, 1, "ai-run-key-00000012", config)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,period_kind,period_key,action,request_delta,token_delta) VALUES($1,$2,'day','2026-07-26','reserve',1,1)`, other, runID); err == nil {
		t.Fatal("cross-student AI ledger row succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_usage_ledger(student_id,run_id,period_kind,period_key,action,request_delta,token_delta) VALUES($1,$2,'day','2026-07-27','reserve',1,1)`, student, runID); err == nil {
		t.Fatal("wrong AI ledger period succeeded")
	}
}

func TestAISchemaAttachmentPurposeOwnershipAndAccessTarget(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	student, other := aiStudent(t, pool), aiStudent(t, pool)
	threadID := aiThread(t, pool, student)
	messageID := aiStudentMessage(t, pool, threadID, student, "ai-message-key-0002")
	fileVersionID := aiFileVersion(t, pool, student, "ai_attachment")
	wrongOwner := aiFileVersion(t, pool, other, "ai_attachment")
	wrongPurpose := aiFileVersion(t, pool, student, "qa_attachment")
	requestID := "ai-access-" + uuid.NewString()
	t.Cleanup(func() {
		aiSchemaAttachmentCleanup(t, pool, requestID, fileVersionID, wrongOwner, wrongPurpose)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO ai_message_files(message_id,file_version_id,sort_position,display_name) VALUES($1,$2,0,'ai.pdf')`, messageID, fileVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_message_files SET display_name='changed.pdf' WHERE message_id=$1 AND file_version_id=$2`, messageID, fileVersionID); err == nil {
		t.Fatal("AI attachment update succeeded")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ai_message_files WHERE message_id=$1 AND file_version_id=$2`, messageID, fileVersionID); err == nil {
		t.Fatal("AI attachment delete succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_message_files(message_id,file_version_id,sort_position,display_name) VALUES($1,$2,1,'ai.pdf')`, uuid.New(), fileVersionID); err == nil {
		t.Fatal("AI file version reused across messages")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_message_files(message_id,file_version_id,sort_position,display_name) VALUES($1,$2,1,'wrong.pdf')`, messageID, wrongOwner); err == nil {
		t.Fatal("cross-owner AI attachment succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_message_files(message_id,file_version_id,sort_position,display_name) VALUES($1,$2,1,'wrong-purpose.pdf')`, messageID, wrongPurpose); err == nil {
		t.Fatal("non-AI attachment succeeded")
	}

	if _, err := pool.Exec(ctx, `INSERT INTO file_previews(file_version_id,preview_kind,object_key,content_type,size_bytes,sha256,processing_state) VALUES($1,'ai_text',$2,'text/plain; charset=utf-8',1,$3,'ready')`, fileVersionID, "ai-preview/"+uuid.NewString(), strings.Repeat("a", 64)); err != nil {
		t.Fatalf("ai_text preview rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO file_access_logs(actor_user_id,file_version_id,requested_file_version_id,result,reason_code,ip,access_policy,request_id,playback_session_hash,ai_message_id) VALUES($1,$2,$2,'allow','','192.0.2.1','download',$3,'',$4)`, student, fileVersionID, requestID, messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO file_access_logs(actor_user_id,file_version_id,requested_file_version_id,result,reason_code,ip,access_policy,request_id,playback_session_hash,qa_message_id,ai_message_id) VALUES($1,$2,$2,'allow','','192.0.2.1','download',$3,'',$4,$4)`, student, fileVersionID, "ai-access-"+uuid.NewString(), uuid.New()); err == nil {
		t.Fatal("multiple file access business targets succeeded")
	}
}

func aiSchemaAttachmentCleanup(t *testing.T, pool *pgxpool.Pool, requestID string, fileVersionIDs ...uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE file_access_logs DISABLE TRIGGER file_access_logs_immutable`); err != nil {
		t.Errorf("disable file access immutability: %v", err)
		return
	}
	defer func() {
		if _, err := pool.Exec(ctx, `ALTER TABLE file_access_logs ENABLE TRIGGER file_access_logs_immutable`); err != nil {
			t.Errorf("enable file access immutability: %v", err)
		}
	}()
	if _, err := pool.Exec(ctx, `DELETE FROM file_access_logs WHERE request_id=$1`, requestID); err != nil {
		t.Errorf("delete AI access fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE ai_message_files DISABLE TRIGGER ai_message_files_immutable`); err != nil {
		t.Errorf("disable AI attachment immutability: %v", err)
		return
	}
	defer func() {
		if _, err := pool.Exec(ctx, `ALTER TABLE ai_message_files ENABLE TRIGGER ai_message_files_immutable`); err != nil {
			t.Errorf("enable AI attachment immutability: %v", err)
		}
	}()
	for _, fileVersionID := range fileVersionIDs {
		if fileVersionID == uuid.Nil {
			continue
		}
		if _, err := pool.Exec(ctx, `DELETE FROM ai_message_files WHERE file_version_id=$1`, fileVersionID); err != nil {
			t.Errorf("delete AI attachment binding: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM file_previews WHERE file_version_id=$1`, fileVersionID); err != nil {
			t.Errorf("delete AI preview fixture: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM file_versions WHERE id=$1`, fileVersionID); err != nil {
			t.Errorf("delete AI file version fixture: %v", err)
		}
	}
}

func assertQAMessageStillRejectsAssistantRole(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	student := aiStudent(t, pool)
	threadID := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO qa_threads(id,student_id,title,status,last_message_at) VALUES($1,$2,'QA isolation','pending',now())`, threadID, student); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'assistant','initial','no',$4)`, uuid.New(), threadID, student, "qa-isolation-key-0001"); err == nil {
		t.Fatal("teacher QA accepted assistant role")
	}
}

func assertQAMessageStillRequiresSenderUser(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	student := aiStudent(t, pool)
	threadID := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO qa_threads(id,student_id,title,status,last_message_at) VALUES($1,$2,'QA isolation','pending',now())`, threadID, student); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,NULL,'student','initial','no',$3)`, uuid.New(), threadID, "qa-isolation-key-0002"); err == nil {
		t.Fatal("teacher QA accepted a missing sender")
	}
}

func assertAIMessageAcceptsAssistantWithoutUserID(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	student := aiStudent(t, pool)
	threadID := aiThread(t, pool, student)
	messageID := aiStudentMessage(t, pool, threadID, student, "ai-message-key-0003")
	config := aiRuntimeConfig(t, pool)
	runID := aiRun(t, pool, threadID, student, messageID, 1, "ai-run-key-00000004", config)
	if _, err := pool.Exec(context.Background(), `UPDATE ai_runs SET status='failed',completed_at=now(),error_code='test_failure',usage_source='unknown' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE ai_runs SET status='streaming',lease_owner='worker-1',lease_expires_at=now()+interval '1 minute',heartbeat_at=now(),started_at=now() WHERE id=$1`, runID); err == nil {
		t.Fatal("terminal run transition unexpectedly succeeded")
	}
	// A separate succeeded run demonstrates that an assistant role has no user sender.
	runID = aiRun(t, pool, threadID, student, messageID, 2, "ai-run-key-00000005", config)
	if err := aiSucceedRunWithAssistant(context.Background(), pool, runID, threadID, "completed answer"); err != nil {
		t.Fatalf("AI assistant without sender user ID rejected: %v", err)
	}
}

func aiSucceedRunWithAssistant(ctx context.Context, pool *pgxpool.Pool, runID, threadID uuid.UUID, body string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE ai_runs SET status='streaming',lease_owner='worker-1',lease_expires_at=now()+interval '1 minute',heartbeat_at=now(),started_at=now() WHERE id=$1`, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_runs SET status='succeeded',lease_owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,completed_at=now(),usage_source='upstream',input_tokens=10,output_tokens=20,cost_micro_usd=3 WHERE id=$1`, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai_messages(id,thread_id,role,body_text,trigger_run_id) VALUES($1,$2,'assistant',$3,$4)`, uuid.New(), threadID, body, runID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func aiStudent(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `INSERT INTO users(username,display_name,role,status,password_hash) VALUES($1,'AI student','student','active','hash') RETURNING id`, "ai_schema_"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertUniqueConstraint(t *testing.T, err error, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected PostgreSQL unique violation %q, got %T: %v", constraint, err, err)
	}
	if pgErr.Code != "23505" || pgErr.ConstraintName != constraint {
		t.Fatalf("expected unique constraint %q, got code=%q constraint=%q", constraint, pgErr.Code, pgErr.ConstraintName)
	}
}

func aiThread(t *testing.T, pool *pgxpool.Pool, student uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO ai_threads(id,student_id,title,subject,last_message_at) VALUES($1,$2,'AI question','math',now())`, id, student); err != nil {
		t.Fatal(err)
	}
	return id
}

func aiStudentMessage(t *testing.T, pool *pgxpool.Pool, threadID, student uuid.UUID, key string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO ai_messages(id,thread_id,role,sender_user_id,body_text,idempotency_key) VALUES($1,$2,'student',$3,'question',$4)`, id, threadID, student, key); err != nil {
		t.Fatal(err)
	}
	return id
}

func aiRun(t *testing.T, pool *pgxpool.Pool, threadID, student, messageID uuid.UUID, attempt int, key string, config aiConfigIDs) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), aiRunInsertSQL, id, threadID, student, messageID, attempt, key, "queued", config.providerID, config.modelID, config.promptID, config.promptSHA256); err != nil {
		t.Fatal(err)
	}
	return id
}

const aiRunInsertSQL = `
INSERT INTO ai_runs(
  id,thread_id,student_id,trigger_message_id,attempt_no,idempotency_key,status,
  provider_id,provider_base_url,protocol_mode,model_id,upstream_model_id,modality,
  context_window_tokens,max_output_tokens,image_quota_tokens,
  input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,
  prompt_id,prompt_subject,prompt_version,prompt_sha256,
  connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,
  reserved_request_count,reserved_token_count,quota_day_key,quota_month_key,estimator_version
) VALUES(
  $1,$2,$3,$4,$5,$6,$7,
  $8,'https://provider.invalid','chat_completions',$9,'model','text',1000,100,10,0,0,
	$10,'math',1,$11,
  1000,2000,2000,3000,1,100,'2026-07-26','2026-07',1
)`

const aiRunTerminalInsertSQL = `
INSERT INTO ai_runs(
  id,thread_id,student_id,trigger_message_id,attempt_no,idempotency_key,status,
  provider_id,provider_base_url,protocol_mode,model_id,upstream_model_id,modality,
  context_window_tokens,max_output_tokens,image_quota_tokens,
  input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,
  prompt_id,prompt_subject,prompt_version,prompt_sha256,
  connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,
  reserved_request_count,reserved_token_count,quota_day_key,quota_month_key,estimator_version,
  completed_at,error_code,usage_source
) VALUES(
  $1,$2,$3,$4,$5,$6,$7,
  $8,'https://provider.invalid','chat_completions',$9,'model','text',1000,100,10,0,0,
	$10,'math',1,$11,
  1000,2000,2000,3000,1,100,'2026-07-26','2026-07',1,
  now(),'upstream_failed','unknown'
)`

type aiConfigIDs struct {
	providerID   uuid.UUID
	modelID      uuid.UUID
	promptID     uuid.UUID
	promptSHA256 string
}

func aiRuntimeConfig(t *testing.T, pool *pgxpool.Pool) aiConfigIDs {
	t.Helper()
	ctx := context.Background()
	var adminID uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH existing AS (SELECT id FROM users WHERE role='admin' AND deleted_at IS NULL LIMIT 1),
		inserted AS (
			INSERT INTO users(username,display_name,role,status,password_hash)
			SELECT $1,'AI admin','admin','active','hash' WHERE NOT EXISTS(SELECT 1 FROM existing)
			RETURNING id
		)
		SELECT id FROM existing UNION ALL SELECT id FROM inserted LIMIT 1`, "ai_schema_admin_"+uuid.NewString()).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	ids := aiConfigIDs{providerID: uuid.New(), modelID: uuid.New()}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_providers(id,name,base_url,protocol_mode,encrypted_api_key,key_version,key_updated_at,created_by) VALUES($1,$2,'https://provider.invalid','chat_completions',$3,1,now(),$4)`, ids.providerID, "AI provider "+uuid.NewString(), make([]byte, 29), adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_models(id,provider_id,upstream_model_id,modality,context_window_tokens,max_output_tokens,connect_timeout_ms,response_header_timeout_ms,idle_stream_timeout_ms,total_timeout_ms,image_quota_tokens,input_price_micro_usd_per_million_tokens,output_price_micro_usd_per_million_tokens,created_by,updated_by) VALUES($1,$2,'model','text',1000,100,1000,2000,2000,3000,10,0,0,$3,$3)`, ids.modelID, ids.providerID, adminID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		WITH existing AS (SELECT id FROM prompt_templates WHERE subject='math' AND version=1 LIMIT 1),
		inserted AS (
			INSERT INTO prompt_templates(subject,version,system_prompt,created_by)
			SELECT 'math',1,'prompt',$1 WHERE NOT EXISTS(SELECT 1 FROM existing)
			RETURNING id
		)
		SELECT id FROM existing UNION ALL SELECT id FROM inserted LIMIT 1`, adminID).Scan(&ids.promptID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT encode(digest(system_prompt,'sha256'),'hex') FROM prompt_templates WHERE id=$1`, ids.promptID).Scan(&ids.promptSHA256); err != nil {
		t.Fatal(err)
	}
	return ids
}

func aiFileVersion(t *testing.T, pool *pgxpool.Pool, owner uuid.UUID, purpose string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, owner).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,$3,'ai.pdf','application/pdf',1,$4,'ready',$5) RETURNING id`, fileID, purpose, "ai-file/"+uuid.NewString(), strings.Repeat("b", 64), owner).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	return versionID
}
