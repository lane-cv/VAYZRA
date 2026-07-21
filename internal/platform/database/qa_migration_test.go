package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestQAMigrationDownRemovesSchema(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var applied bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM goose_db_version WHERE version_id=9 AND is_applied)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("qa migration was not applied")
	}
	provider, closeProvider := migrationProvider(t, pool.Config().ConnString())
	t.Cleanup(closeProvider)
	t.Cleanup(func() {
		if _, err := provider.UpTo(context.Background(), 12); err != nil {
			t.Errorf("restore latest migration: %v", err)
		}
	})

	if _, err := provider.DownTo(ctx, 8); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name IN
		('qa_threads','qa_messages','qa_message_files','teacher_notes')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("remaining qa tables=%d", tables)
	}
}

func TestQAMessageKindMigrationBackfillsAndReversesIncrementally(t *testing.T) {
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
	if _, err := provider.DownTo(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}

	student := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,role,status,password_hash) VALUES($1,$2,'Migration student','student','active','hash')`, student, "qa_kind_student_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var admin uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE role='admin' AND deleted_at IS NULL LIMIT 1`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	threadID := uuid.New()
	stamp := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at,created_at,updated_at) VALUES($1,$2,'Legacy','waiting_student',$3,$3,$3)`, threadID, student, stamp.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, row := range []struct {
		sender uuid.UUID
		role   string
		at     time.Time
	}{
		{student, "student", stamp},
		{admin, "admin", stamp.Add(time.Second)},
		{student, "student", stamp.Add(2 * time.Second)},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,body_text,idempotency_key,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, ids[i], threadID, row.sender, row.role, "legacy", uuid.NewString(), row.at); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := provider.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	rows, err := pool.Query(ctx, `SELECT message_kind FROM qa_messages WHERE thread_id=$1 ORDER BY created_at,id`, threadID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	rows.Close()
	if len(kinds) != 3 || kinds[0] != "initial" || kinds[1] != "admin_reply" || kinds[2] != "student_follow_up" {
		t.Fatalf("backfilled kinds=%v", kinds)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','initial','duplicate',$4)`, uuid.New(), threadID, student, uuid.NewString()); err == nil {
		t.Fatal("second initial message succeeded")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'admin','student_follow_up','mismatch',$4)`, uuid.New(), threadID, admin, uuid.NewString()); err == nil {
		t.Fatal("sender-role/message-kind mismatch succeeded")
	}

	if _, err := provider.DownTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	var columns, indexes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='qa_messages' AND column_name='message_kind'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND indexname='qa_messages_one_initial_per_thread_idx'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || indexes != 0 {
		t.Fatalf("down left message_kind columns=%d indexes=%d", columns, indexes)
	}
}
