package files

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/qanda"
	"happylearn.local/app/tests/integration"
)

func TestPostgresQAUploadCompletionSetsOrphanRetention(t *testing.T) {
	ctx, pool := qaCleanupFixture(t)
	actor := qaCleanupUser(t, pool)
	now := time.Now().UTC()
	session := UploadSession{ID: uuid.New(), ActorUserID: actor, Purpose: UploadPurposeQA, ObjectKey: "qa-orphan/" + uuid.NewString(), MinIOUploadID: uuid.NewString(), DisplayName: "orphan.pdf", DeclaredMIME: "application/pdf", ExpectedSize: 1, ExpectedSHA256: digestOf([]byte("x")), State: UploadOpen, ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	store := NewPostgresStore(pool)
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	completing, _, _, err := store.BeginCompletion(ctx, session.ID, actor, UploadPurposeQA, now)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.FinishCompletion(ctx, completing, Principal{User: auth.User{ID: actor, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "qa-retention", IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	var createdAt time.Time
	var retention *time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at,retention_until FROM file_versions WHERE id=$1`, completed.FileVersionID).Scan(&createdAt, &retention); err != nil {
		t.Fatal(err)
	}
	if retention == nil || retention.Sub(createdAt) < 24*time.Hour-time.Second || retention.Sub(createdAt) > 24*time.Hour+time.Second {
		t.Fatalf("created=%s retention=%v", createdAt, retention)
	}
}

func TestPostgresQAOrphanCleanupAndBindingSerializeBothOrders(t *testing.T) {
	ctx, pool := qaCleanupFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	admin := qaCleanupAdmin(t, pool)
	notifications := qaCleanupNotifications{}
	qaService := qanda.NewService(qanda.NewPostgresStore(pool), qanda.NewPostgresUnitOfWork(pool, func(pgx.Tx) qanda.NotificationWriter { return notifications }), time.Now)

	t.Run("binding commits first", func(t *testing.T) {
		student := qaCleanupUser(t, pool)
		fileID, versionID, _ := seedQAOrphanForActor(t, pool, student, time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC))
		_, _, err := qaService.CreateThread(ctx, qaCleanupPrincipal(student), qanda.CreateThreadInput{Title: "bound first", Body: "body", IdempotencyKey: uuid.NewString(), Attachments: []qanda.AttachmentInput{{FileVersionID: versionID, SortPosition: 0}}})
		if err != nil {
			t.Fatal(err)
		}
		candidate, ok, err := NewPostgresStore(pool).ClaimFileCleanup(ctx, now, "binding-first", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if ok && candidate.FileID == fileID {
			t.Fatalf("bound version claimed: %+v", candidate)
		}
	})

	t.Run("cleanup commits first", func(t *testing.T) {
		student := qaCleanupUser(t, pool)
		_, versionID, _ := seedQAOrphanForActor(t, pool, student, time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC))
		candidate, ok, err := NewPostgresStore(pool).ClaimFileCleanup(ctx, now, "cleanup-first", time.Minute)
		if err != nil || !ok || candidate.VersionID != versionID {
			t.Fatalf("candidate=%+v ok=%v err=%v", candidate, ok, err)
		}
		_, _, err = qaService.CreateThread(ctx, qaCleanupPrincipal(student), qanda.CreateThreadInput{Title: "cleanup first", Body: "body", IdempotencyKey: uuid.NewString(), Attachments: []qanda.AttachmentInput{{FileVersionID: versionID, SortPosition: 0}}})
		if !errors.Is(err, qanda.ErrForbidden) {
			t.Fatalf("bind after cleanup error=%v", err)
		}
	})
	_ = admin
}

type qaCleanupNotifications struct{}

func (qaCleanupNotifications) Notify(context.Context, qanda.NotificationIntent) error { return nil }

func qaCleanupPrincipal(id uuid.UUID) qanda.Principal {
	return qanda.Principal{User: auth.User{ID: id, Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "qa-cleanup", IP: net.ParseIP("127.0.0.1")}
}

func qaCleanupAdmin(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `WITH existing AS (SELECT id FROM users WHERE role='admin' AND deleted_at IS NULL LIMIT 1), inserted AS (INSERT INTO users(username,display_name,role,status,password_hash) SELECT $1,'QA cleanup admin','admin','active','hash' WHERE NOT EXISTS(SELECT 1 FROM existing) RETURNING id) SELECT id FROM existing UNION ALL SELECT id FROM inserted LIMIT 1`, "qa_cleanup_admin_"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE users SET status='active' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPostgresQAOrphanCleanupClaimsDueUnboundAndSkipsBound(t *testing.T) {
	ctx, pool := qaCleanupFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	dueFile, dueVersion, dueKey := seedQAOrphan(t, pool, time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), false)
	_, _, _ = seedQAOrphan(t, pool, now.Add(time.Hour), false)
	_, boundVersion, _ := seedQAOrphan(t, pool, now.Add(-time.Hour), true)
	store := NewPostgresStore(pool)
	candidate, ok, err := store.ClaimFileCleanup(ctx, now, "qa-cleaner", time.Minute)
	if err != nil || !ok || candidate.FileID != dueFile || candidate.VersionID != dueVersion || candidate.OriginalKey != dueKey {
		t.Fatalf("candidate=%+v ok=%v err=%v", candidate, ok, err)
	}
	if err := store.CompleteFileCleanup(ctx, candidate, "qa-cleaner", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var deletedAt, purgedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT f.deleted_at,fv.purged_at FROM files f JOIN file_versions fv ON fv.file_id=f.id WHERE fv.id=$1`, dueVersion).Scan(&deletedAt, &purgedAt); err != nil {
		t.Fatal(err)
	}
	if deletedAt == nil || purgedAt == nil {
		t.Fatalf("deleted=%v purged=%v", deletedAt, purgedAt)
	}
	var state *string
	if err := pool.QueryRow(ctx, `SELECT cleanup_state FROM file_versions WHERE id=$1`, boundVersion).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != nil {
		t.Fatalf("bound cleanup state=%v", *state)
	}
}

func qaCleanupFixture(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

func qaCleanupUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `INSERT INTO users(username,display_name,role,status,password_hash) VALUES($1,'QA cleanup','student','active','hash') RETURNING id`, "qa_cleanup_"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedQAOrphan(t *testing.T, pool *pgxpool.Pool, retention time.Time, bound bool) (uuid.UUID, uuid.UUID, string) {
	t.Helper()
	actor := qaCleanupUser(t, pool)
	fileID, versionID, key := seedQAOrphanForActor(t, pool, actor, retention)
	ctx := context.Background()
	if bound {
		threadID, messageID := uuid.New(), uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at) VALUES($1,$2,'bound','pending',now())`, threadID, actor); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key) VALUES($1,$2,$3,'student','initial','bound',$4)`, messageID, threadID, actor, uuid.NewString()); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO qa_message_files(message_id,file_version_id,sort_position,display_name) VALUES($1,$2,0,'qa.pdf')`, messageID, versionID); err != nil {
			t.Fatal(err)
		}
	}
	return fileID, versionID, key
}

func seedQAOrphanForActor(t *testing.T, pool *pgxpool.Pool, actor uuid.UUID, retention time.Time) (uuid.UUID, uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	var fileID, versionID uuid.UUID
	key := "qa-cleanup/" + uuid.NewString()
	if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, actor).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,detected_mime,size_bytes,sha256,processing_state,created_by,retention_until) VALUES($1,1,'qa_attachment',$2,'qa.pdf','application/pdf','application/pdf',1,$3,'ready',$4,$5) RETURNING id`, fileID, key, digestOf([]byte("x")), actor, retention).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	return fileID, versionID, key
}
