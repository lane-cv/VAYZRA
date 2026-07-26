package files

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresAIAccessRequiresBoundCleanOwnerFileAndLogsAITarget(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	addStudent := func() uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users(username,display_name,role,status,password_hash)
VALUES($1,'AI file student','student','active','hash') RETURNING id`, "ai_file_"+uuid.NewString()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner, other := addStudent(), addStudent()
	now := time.Now().UTC()
	thread, message := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO ai_threads(id,student_id,title,subject,last_message_at,created_at)
VALUES($1,$2,'Private AI','math',$3,$3)`, thread, owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_messages(id,thread_id,role,sender_user_id,body_text,idempotency_key,created_at)
VALUES($1,$2,'student',$3,'question',$4,$5)`, message, thread, owner, "ai-file-message-0001", now); err != nil {
		t.Fatal(err)
	}
	makeVersion := func(purpose, state, scan string, playable bool) uuid.UUID {
		t.Helper()
		var fileID, versionID uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, owner).Scan(&fileID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO file_versions(
file_id,version,purpose,object_key,display_name,declared_mime,detected_mime,size_bytes,sha256,processing_state,scan_result,browser_playable,created_by)
VALUES($1,1,$2,$3,'question.txt','text/plain','text/plain',5,$4,$5,$6,$7,$8) RETURNING id`,
			fileID, purpose, "ai-file/"+uuid.NewString(), digestOf([]byte("hello")), state, scan, playable, owner).Scan(&versionID); err != nil {
			t.Fatal(err)
		}
		return versionID
	}
	bound := makeVersion("ai_attachment", "ready", "clean", true)
	unbound := makeVersion("ai_attachment", "ready", "clean", true)
	qaPurpose := makeVersion("qa_attachment", "ready", "clean", true)
	notClean := makeVersion("ai_attachment", "ready", "rejected", true)
	if _, err := pool.Exec(ctx, `INSERT INTO ai_message_files(message_id,file_version_id,sort_position,display_name)
VALUES($1,$2,0,'question.txt')`, message, bound); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `ALTER TABLE file_access_logs DISABLE TRIGGER file_access_logs_immutable`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM file_access_logs WHERE ai_message_id=$1 OR requested_file_version_id=ANY($2)`,
			message, []uuid.UUID{bound, unbound, qaPurpose, notClean})
		_, _ = pool.Exec(cleanupCtx, `ALTER TABLE file_access_logs ENABLE TRIGGER file_access_logs_immutable`)
		_, _ = pool.Exec(cleanupCtx, `ALTER TABLE ai_message_files DISABLE TRIGGER ai_message_files_immutable`)
		_, _ = pool.Exec(cleanupCtx, `ALTER TABLE ai_messages DISABLE TRIGGER ai_messages_immutable`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM ai_message_files WHERE message_id=$1`, message)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM ai_messages WHERE id=$1`, message)
		_, _ = pool.Exec(cleanupCtx, `ALTER TABLE ai_messages ENABLE TRIGGER ai_messages_immutable`)
		_, _ = pool.Exec(cleanupCtx, `ALTER TABLE ai_message_files ENABLE TRIGGER ai_message_files_immutable`)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM ai_threads WHERE id=$1`, thread)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM file_versions WHERE id=ANY($1)`, []uuid.UUID{bound, unbound, qaPurpose, notClean})
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM files WHERE created_by=$1`, owner)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=ANY($1)`, []uuid.UUID{owner, other})
	})
	store := NewPostgresStore(pool)
	principal := func(id uuid.UUID) Principal {
		return Principal{User: auth.User{ID: id, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "ai-file-request", IP: net.ParseIP("192.0.2.44")}
	}
	delivery, err := store.ResolveAIAccess(ctx, principal(owner), bound)
	if err != nil || delivery.MessageID != message || delivery.VersionID != bound || delivery.ObjectKey == "" {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	status, err := store.ResolveAIStatus(ctx, principal(owner), bound)
	if err != nil || status.FileVersionID != bound || status.ProcessingState != "ready" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	for name, tc := range map[string]struct {
		actor   uuid.UUID
		version uuid.UUID
	}{
		"foreign":    {other, bound},
		"unbound":    {owner, unbound},
		"qa purpose": {owner, qaPurpose},
		"not clean":  {owner, notClean},
	} {
		t.Run(name, func(t *testing.T) {
			denied, err := store.ResolveAIAccess(ctx, principal(tc.actor), tc.version)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("access err=%v", err)
			}
			if name == "foreign" && (denied.MessageID != message || denied.ObjectKey != "") {
				t.Fatalf("denied audit target=%+v", denied)
			}
			if _, err := store.ResolveAIStatus(ctx, principal(tc.actor), tc.version); !errors.Is(err, ErrNotFound) {
				t.Fatalf("status err=%v", err)
			}
		})
	}
	logRequestID := "ai-file-access-" + uuid.NewString()
	if err := store.WriteAccessLog(ctx, AccessLog{
		ActorUserID: owner, RequestedVersionID: bound, VersionID: bound, AIMessageID: message,
		Action: ActionPreview, Result: AccessAllowed, RequestID: logRequestID, IP: net.ParseIP("192.0.2.44"),
	}); err != nil {
		t.Fatal(err)
	}
	var loggedMessage uuid.UUID
	var lessonNull, qaNull bool
	if err := pool.QueryRow(ctx, `SELECT ai_message_id,lesson_revision_id IS NULL,qa_message_id IS NULL
FROM file_access_logs WHERE request_id=$1`, logRequestID).Scan(&loggedMessage, &lessonNull, &qaNull); err != nil {
		t.Fatal(err)
	}
	if loggedMessage != message || !lessonNull || !qaNull {
		t.Fatalf("message=%s lessonNull=%v qaNull=%v", loggedMessage, lessonNull, qaNull)
	}
}
