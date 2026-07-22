package integration_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestQAHistoryIsImmutable(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	teacher := qaTeacher(t, pool)
	student := qaStudent(t, pool)
	var fileID, fileVersionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, teacher).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by)
		VALUES($1,1,'teaching',$2,'qa.pdf','application/pdf',1,$3,'ready',$4) RETURNING id`,
		fileID, "qa/"+uuid.NewString(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", teacher).Scan(&fileVersionID); err != nil {
		t.Fatal(err)
	}
	threadID := uuid.New()
	messageID := uuid.New()
	noteID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO qa_threads(id,student_id,title,status,last_message_at)
		VALUES($1,$2,'Need help','pending',now())`, threadID, student); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key)
		VALUES($1,$2,$3,'student','initial','Question', $4)`, messageID, threadID, student, "qa-message-key-0001"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO qa_message_files(message_id,file_version_id,sort_position,display_name)
		VALUES($1,$2,0,'qa.pdf')`, messageID, fileVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO teacher_notes(id,thread_id,author_user_id,body_text)
		VALUES($1,$2,$3,'Private note')`, noteID, threadID, teacher); err != nil {
		t.Fatal(err)
	}

	for _, statement := range []struct {
		name string
		sql  string
		args []any
	}{
		{"message update", `UPDATE qa_messages SET body_text='Changed' WHERE id=$1`, []any{messageID}},
		{"message delete", `DELETE FROM qa_messages WHERE id=$1`, []any{messageID}},
		{"file binding update", `UPDATE qa_message_files SET display_name='changed.pdf' WHERE message_id=$1 AND file_version_id=$2`, []any{messageID, fileVersionID}},
		{"file binding delete", `DELETE FROM qa_message_files WHERE message_id=$1 AND file_version_id=$2`, []any{messageID, fileVersionID}},
		{"teacher note update", `UPDATE teacher_notes SET body_text='Changed' WHERE id=$1`, []any{noteID}},
		{"teacher note delete", `DELETE FROM teacher_notes WHERE id=$1`, []any{noteID}},
	} {
		t.Run(statement.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, statement.sql, statement.args...); err == nil {
				t.Fatal("immutable history mutation succeeded")
			}
		})
	}
}

func TestQAStatusAndMessageIdempotencyConstraints(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	student := qaStudent(t, pool)
	for _, status := range []string{"pending", "in_progress", "waiting_student", "completed"} {
		threadID := uuid.New()
		completedAt := "NULL"
		if status == "completed" {
			completedAt = "now()"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at,completed_at) VALUES($1,$2,'Status test',$3,now(),`+completedAt+`)`, threadID, student, status); err != nil {
			t.Fatalf("insert status %q: %v", status, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at) VALUES($1,$2,'Invalid status','not_a_status',now())`, uuid.New(), student); err == nil {
		t.Fatal("invalid qa status succeeded")
	}

	threadID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at) VALUES($1,$2,'Idempotency','pending',now())`, threadID, student); err != nil {
		t.Fatal(err)
	}
	key := "qa-message-key-0002"
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','initial','One',$4)`, uuid.New(), threadID, student, key); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','student_follow_up','Two',$4)`, uuid.New(), threadID, student, key); err == nil {
		t.Fatal("duplicate sender idempotency key succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','initial','Another initial',$4)`, uuid.New(), threadID, student, "qa-message-key-0003"); err == nil {
		t.Fatal("second initial message succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','invalid','Invalid kind',$4)`, uuid.New(), threadID, student, "qa-message-key-0004"); err == nil {
		t.Fatal("invalid message kind succeeded")
	}
}

func TestQAOwnershipSenderAndAttachmentIntegrity(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	student, other, admin := qaStudent(t, pool), qaStudent(t, pool), qaTeacher(t, pool)
	threadID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at) VALUES($1,$2,'Integrity','pending',now())`, threadID, student); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE qa_threads SET student_id=$2 WHERE id=$1`, threadID, other); err == nil {
		t.Fatal("qa thread ownership mutation succeeded")
	}
	for _, tc := range []struct {
		name, role, kind string
		sender           uuid.UUID
	}{
		{"cross student", "student", "student_follow_up", other},
		{"non admin", "admin", "admin_reply", other},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,$4,$5,'invalid sender',$6)`, uuid.New(), threadID, tc.sender, tc.role, tc.kind, uuid.NewString()); err == nil {
			t.Fatalf("%s sender insert succeeded", tc.name)
		}
	}
	studentMessage, adminMessage := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','initial','valid',$4)`, studentMessage, threadID, student, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'admin','admin_reply','valid',$4)`, adminMessage, threadID, admin, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var fileID, versionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, student).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,'qa_attachment',$2,'one.pdf','application/pdf',1,$3,'ready',$4) RETURNING id`, fileID, "qa/"+uuid.NewString(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", student).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_message_files(message_id,file_version_id,sort_position,display_name) VALUES($1,$2,0,'one.pdf')`, studentMessage, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_message_files(message_id,file_version_id,sort_position,display_name) VALUES($1,$2,0,'one.pdf')`, adminMessage, versionID); err == nil {
		t.Fatal("file version reused across messages")
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, student); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','student_follow_up','disabled sender',$4)`, uuid.New(), threadID, student, uuid.NewString()); err == nil {
		t.Fatal("disabled student sender insert succeeded")
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET deleted_at=now() WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'admin','admin_reply','deleted sender',$4)`, uuid.New(), threadID, admin, uuid.NewString()); err == nil {
		t.Fatal("deleted admin sender insert succeeded")
	}
}

func qaTeacher(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var teacher uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		WITH existing AS (SELECT id FROM users WHERE role='admin' AND deleted_at IS NULL LIMIT 1),
		inserted AS (
			INSERT INTO users(username,display_name,role,status,password_hash)
			SELECT $1,'QA Teacher','admin','active','hash' WHERE NOT EXISTS(SELECT 1 FROM existing)
			RETURNING id
		)
		SELECT id FROM existing UNION ALL SELECT id FROM inserted LIMIT 1`, "qa_teacher_"+uuid.NewString()).Scan(&teacher); err != nil {
		t.Fatal(err)
	}
	return teacher
}

func qaStudent(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var student uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO users(username,display_name,role,status,password_hash)
		VALUES($1,'QA Student','student','active','hash') RETURNING id`, "qa_student_"+uuid.NewString()).Scan(&student); err != nil {
		t.Fatal(err)
	}
	return student
}
