package database_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestQAFilePurposeMigrationBackfillsConstrainsAndReverses(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	t.Cleanup(closeProvider)
	t.Cleanup(func() {
		if _, err := provider.UpTo(context.Background(), 12); err != nil {
			t.Errorf("restore latest migration: %v", err)
		}
	})
	if _, err := provider.DownTo(ctx, 10); err != nil {
		t.Fatal(err)
	}

	actor := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'Purpose fixture','student','active','hash')`, actor, "purpose_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actor).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	versionID, sessionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO file_versions(id,file_id,version,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,$2,1,$3,'legacy.pdf','application/pdf',1,$4,'ready',$5)`, versionID, fileID, "purpose-version/"+uuid.NewString(), strings.Repeat("a", 64), actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO upload_sessions(id,actor_user_id,object_key,minio_upload_id,display_name,declared_mime,expected_size,expected_sha256,state,expires_at) VALUES($1,$2,$3,$4,'legacy.pdf','application/pdf',1,$5,'open',$6)`, sessionID, actor, "purpose-session/"+uuid.NewString(), uuid.NewString(), strings.Repeat("b", 64), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 11); err != nil {
		t.Fatal(err)
	}
	var versionPurpose, sessionPurpose string
	if err := pool.QueryRow(ctx, `SELECT purpose FROM file_versions WHERE id=$1`, versionID).Scan(&versionPurpose); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT purpose FROM upload_sessions WHERE id=$1`, sessionID).Scan(&sessionPurpose); err != nil {
		t.Fatal(err)
	}
	if versionPurpose != "teaching" || sessionPurpose != "teaching" {
		t.Fatalf("version=%q session=%q", versionPurpose, sessionPurpose)
	}
	if _, err := pool.Exec(ctx, `UPDATE upload_sessions SET purpose='archive' WHERE id=$1`, sessionID); err == nil {
		t.Fatal("invalid purpose accepted")
	}
	if _, err := provider.DownTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var columns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND column_name='purpose' AND table_name IN ('upload_sessions','file_versions')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatalf("purpose columns after down=%d", columns)
	}
}

func TestQAFilePurposeDownMigrationFailsClosedForQAHistory(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	t.Cleanup(closeProvider)
	actor, threadID, messageID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'QA history fixture','student','active','hash')`, actor, "qa_history_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at) VALUES($1,$2,'History','pending',now())`, threadID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','initial','history',$4)`, messageID, threadID, actor, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actor).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,'qa_attachment',$2,'history.pdf','application/pdf',1,$3,'ready',$4) RETURNING id`, fileID, "qa-history/"+uuid.NewString(), strings.Repeat("c", 64), actor).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	requestID := "qa-history-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO file_access_logs(actor_user_id,file_version_id,requested_file_version_id,result,reason_code,ip,access_policy,request_id,playback_session_hash,qa_message_id) VALUES($1,$2,$2,'allow','','192.0.2.9','download',$3,'',$4)`, actor, versionID, requestID, messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 10); err == nil {
		t.Fatal("down migration discarded Q&A access history")
	}
	var stillPresent bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='file_access_logs' AND column_name='qa_message_id')`).Scan(&stillPresent); err != nil {
		t.Fatal(err)
	}
	if !stillPresent {
		t.Fatal("failed down migration partially removed Q&A provenance")
	}

	if _, err := pool.Exec(ctx, `ALTER TABLE file_access_logs DISABLE TRIGGER file_access_logs_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM file_access_logs WHERE request_id=$1`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE file_access_logs ENABLE TRIGGER file_access_logs_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE qa_messages DISABLE TRIGGER qa_messages_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM qa_messages WHERE id=$1`, messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE qa_messages ENABLE TRIGGER qa_messages_immutable`); err != nil {
		t.Fatal(err)
	}
	for _, cleanup := range []struct {
		query string
		id    uuid.UUID
	}{
		{`DELETE FROM qa_threads WHERE id=$1`, threadID},
		{`DELETE FROM file_versions WHERE id=$1`, versionID},
		{`DELETE FROM files WHERE id=$1`, fileID},
		{`DELETE FROM users WHERE id=$1`, actor},
	} {
		if _, err := pool.Exec(ctx, cleanup.query, cleanup.id); err != nil {
			t.Fatal(err)
		}
	}
}
