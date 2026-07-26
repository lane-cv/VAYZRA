package database_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestAIRuntimeMigrationDownRemovesSchema(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var applied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=16 AND is_applied)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("ai runtime migration was not applied")
	}

	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	t.Cleanup(closeProvider)
	if _, err := provider.DownTo(ctx, 15); err != nil {
		t.Fatal(err)
	}

	var tables, aiAccessColumn int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM information_schema.tables
			 WHERE table_schema='public' AND table_name IN
			 ('ai_threads','ai_messages','ai_message_files','ai_runs','ai_run_events','ai_usage_ledger')),
			(SELECT count(*) FROM information_schema.columns
			 WHERE table_schema='public' AND table_name='file_access_logs' AND column_name='ai_message_id')`).Scan(&tables, &aiAccessColumn); err != nil {
		t.Fatal(err)
	}
	if tables != 0 || aiAccessColumn != 0 {
		t.Fatalf("runtime schema remained after down: tables=%d ai_access_column=%d", tables, aiAccessColumn)
	}

	student := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'AI runtime down student','student','active','hash')`, student, "ai_runtime_down_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO upload_sessions(id,actor_user_id,object_key,minio_upload_id,display_name,declared_mime,expected_size,expected_sha256,state,expires_at,purpose) VALUES($1,$2,$3,$4,'ai.pdf','application/pdf',1,$5,'open',$6,'ai_attachment')`, uuid.New(), student, "ai-runtime-down/"+uuid.NewString(), uuid.NewString(), strings.Repeat("a", 64), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("down migration still accepted ai_attachment upload purpose")
	}

	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, student).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,'qa_attachment',$2,'qa.pdf','application/pdf',1,$3,'ready',$4) RETURNING id`, fileID, "ai-runtime-down/"+uuid.NewString(), strings.Repeat("b", 64), student).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO file_previews(file_version_id,preview_kind,object_key,content_type,size_bytes,sha256,processing_state) VALUES($1,'ai_text',$2,'text/plain',1,$3,'ready')`, versionID, "ai-runtime-down/"+uuid.NewString(), strings.Repeat("c", 64)); err == nil {
		t.Fatal("down migration still accepted ai_text preview kind")
	}

	threadID, messageID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at) VALUES($1,$2,'Down target','pending',now())`, threadID, student); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','initial','question',$4)`, messageID, threadID, student, "ai-runtime-down-message-key"); err != nil {
		t.Fatal(err)
	}
	lessonRevisionID := aiRuntimeDownLessonRevision(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO file_access_logs(actor_user_id,file_version_id,requested_file_version_id,result,reason_code,ip,access_policy,request_id,playback_session_hash,lesson_revision_id,qa_message_id) VALUES($1,$2,$2,'allow','','192.0.2.1','download',$3,'',$4,$5)`, student, versionID, "ai-runtime-down-access", lessonRevisionID, messageID); err == nil {
		t.Fatal("down migration still accepted multiple business targets")
	}
}

func aiRuntimeDownLessonRevision(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var admin, grade, term, subject, chapter, lesson, revision uuid.UUID
	if err := pool.QueryRow(ctx, `
		WITH existing AS (SELECT id FROM users WHERE role='admin' AND deleted_at IS NULL LIMIT 1),
		inserted AS (
			INSERT INTO users(username,display_name,role,status,password_hash)
			SELECT $1,'AI runtime down admin','admin','active','hash' WHERE NOT EXISTS(SELECT 1 FROM existing)
			RETURNING id
		)
		SELECT id FROM existing UNION ALL SELECT id FROM inserted LIMIT 1`, "ai_runtime_down_admin_"+uuid.NewString()).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO grades(name) VALUES($1) RETURNING id`, "AI runtime down grade "+uuid.NewString()).Scan(&grade); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO terms(grade_id,name) VALUES($1,$2) RETURNING id`, grade, "AI runtime down term "+uuid.NewString()).Scan(&term); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO subjects(term_id,name) VALUES($1,$2) RETURNING id`, term, "AI runtime down subject "+uuid.NewString()).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO chapters(subject_id,name) VALUES($1,$2) RETURNING id`, subject, "AI runtime down chapter "+uuid.NewString()).Scan(&chapter); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO lessons(chapter_id) VALUES($1) RETURNING id`, chapter).Scan(&lesson); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO lesson_revisions(lesson_id,version,source_draft_version,title,published_by) VALUES($1,1,1,'AI runtime down lesson',$2) RETURNING id`, lesson, admin).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}
