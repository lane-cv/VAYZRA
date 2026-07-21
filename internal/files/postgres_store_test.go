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

func TestPostgresUploadStorePersistsPartsAcrossRestartAndCompletesAtomically(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	actor := uuid.New()
	username := "upload_store_" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id,username,display_name,role,status,password_hash) VALUES ($1,$2,'Upload admin','student','active','hash')`, actor, username); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := UploadSession{ID: uuid.New(), ActorUserID: actor, Purpose: UploadPurposeTeaching, ObjectKey: "originals/" + uuid.NewString(), MinIOUploadID: uuid.NewString(), DisplayName: "restart.pdf", DeclaredMIME: "application/pdf", ExpectedSize: 3, ExpectedSHA256: digestOf([]byte("pdf")), State: UploadOpen, ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	first := NewPostgresStore(pool)
	if err := first.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := first.RecordPart(ctx, session.ID, UploadPart{SessionID: session.ID, Number: 1, Size: 3, SHA256: digestOf([]byte("pdf")), ETag: "etag-one", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	restarted := NewPostgresStore(pool)
	persisted, parts, err := restarted.GetSession(ctx, session.ID, actor, UploadPurposeTeaching)
	if err != nil || persisted.ID != session.ID || len(parts) != 1 || parts[0].SHA256 != digestOf([]byte("pdf")) {
		t.Fatalf("session=%+v parts=%+v err=%v", persisted, parts, err)
	}
	completing, completionParts, existing, err := restarted.BeginCompletion(ctx, session.ID, actor, UploadPurposeTeaching, now)
	if err != nil || existing != nil || completing.State != UploadCompleting || len(completionParts) != 1 {
		t.Fatalf("session=%+v parts=%+v existing=%+v err=%v", completing, completionParts, existing, err)
	}
	completed, err := restarted.FinishCompletion(ctx, completing, Principal{User: auth.User{ID: actor, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "postgres-upload-request", IP: net.ParseIP("192.0.2.91")})
	if err != nil || completed.FileVersionID == uuid.Nil || completed.ProcessingState != "pending_scan" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	_, _, duplicate, err := restarted.BeginCompletion(ctx, session.ID, actor, UploadPurposeTeaching, now)
	if err != nil || duplicate == nil || duplicate.FileVersionID != completed.FileVersionID {
		t.Fatalf("duplicate=%+v err=%v", duplicate, err)
	}
	var filesCount, versionsCount, jobsCount, auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE id=$1`, completed.FileID).Scan(&filesCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_versions WHERE id=$1 AND processing_state='pending_scan'`, completed.FileVersionID).Scan(&versionsCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_processing_jobs WHERE file_version_id=$1 AND kind='process_file' AND state='queued' AND attempts=0`, completed.FileVersionID).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='file.uploaded' AND target_type='file_version' AND target_id=$1 AND request_id='postgres-upload-request' AND metadata='{}'::jsonb`, completed.FileVersionID.String()).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if filesCount != 1 || versionsCount != 1 || jobsCount != 1 || auditCount != 1 {
		t.Fatalf("files=%d versions=%d jobs=%d audits=%d", filesCount, versionsCount, jobsCount, auditCount)
	}
}

func TestPostgresQAAccessAuthorizationIsDerivedFromBoundThread(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	insertUser := func(role auth.Role) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users(username,display_name,role,status,password_hash) VALUES($1,'QA access',$2,'active','hash') RETURNING id`, "qa_access_"+uuid.NewString(), role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	owner, other := insertUser(auth.RoleStudent), insertUser(auth.RoleStudent)
	var admin uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL LIMIT 1`).Scan(&admin); err != nil {
		admin = insertUser(auth.RoleAdmin)
	}
	thread, message := uuid.New(), uuid.New()
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO qa_threads(id,student_id,title,status,last_message_at,created_at,updated_at) VALUES($1,$2,'Private','pending',$3,$3,$3)`, thread, owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO qa_messages(id,thread_id,sender_user_id,sender_role,message_kind,body_text,idempotency_key,created_at) VALUES($1,$2,$3,'student','initial','body',$4,$5)`, message, thread, owner, uuid.NewString(), now); err != nil {
		t.Fatal(err)
	}
	makeVersion := func(purpose, state string, bound bool) uuid.UUID {
		t.Helper()
		var fileID, versionID uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO files(created_by) VALUES($1) RETURNING id`, owner).Scan(&fileID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO file_versions(file_id,version,purpose,object_key,display_name,declared_mime,detected_mime,size_bytes,sha256,processing_state,created_by) VALUES($1,1,$2,$3,'answer.txt','text/plain','text/plain',4,$4,$5,$6) RETURNING id`, fileID, purpose, "qa-access/"+uuid.NewString(), digestOf([]byte("data")), state, owner).Scan(&versionID); err != nil {
			t.Fatal(err)
		}
		if bound {
			if _, err := pool.Exec(ctx, `INSERT INTO qa_message_files(message_id,file_version_id,sort_position,display_name) VALUES($1,$2,0,'answer.txt')`, message, versionID); err != nil {
				t.Fatal(err)
			}
		}
		return versionID
	}
	bound := makeVersion("qa_attachment", "ready", true)
	unbound := makeVersion("qa_attachment", "ready", false)
	teaching := makeVersion("teaching", "ready", false)
	store := NewPostgresStore(pool)
	principal := func(id uuid.UUID, role auth.Role) Principal {
		return Principal{User: auth.User{ID: id, Role: role, Status: auth.StatusActive}, RequestID: "qa-access-request", IP: net.ParseIP("192.0.2.8")}
	}
	if d, err := store.ResolveQAAccess(ctx, principal(owner, auth.RoleStudent), bound, ActionDownload); err != nil || d.MessageID != message {
		t.Fatalf("owner delivery=%+v err=%v", d, err)
	}
	if _, err := store.ResolveQAAccess(ctx, principal(other, auth.RoleStudent), bound, ActionDownload); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross student err=%v", err)
	}
	if _, err := store.ResolveQAAccess(ctx, principal(admin, auth.RoleAdmin), bound, ActionDownload); err != nil {
		t.Fatalf("admin err=%v", err)
	}
	if _, err := store.ResolveQAStatus(ctx, principal(owner, auth.RoleStudent), unbound); err != nil {
		t.Fatalf("owner unbound status=%v", err)
	}
	if _, err := store.ResolveQAStatus(ctx, principal(other, auth.RoleStudent), unbound); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign unbound err=%v", err)
	}
	if _, err := store.ResolveQAAccess(ctx, principal(owner, auth.RoleStudent), unbound, ActionDownload); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbound delivery err=%v", err)
	}
	if _, err := store.ResolveQAStatus(ctx, principal(owner, auth.RoleStudent), teaching); !errors.Is(err, ErrNotFound) {
		t.Fatalf("teaching status err=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveQAAccess(ctx, principal(owner, auth.RoleStudent), bound, ActionDownload); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled owner err=%v", err)
	}
	if _, err := store.ResolveQAAccess(ctx, principal(admin, auth.RoleAdmin), bound, ActionDownload); err != nil {
		t.Fatalf("admin should retain thread access after student disable: %v", err)
	}
}

func TestPostgresUploadCleanupClaimsGraceRecoveryStatesAndSkipsReferences(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM upload_parts; DELETE FROM upload_sessions`); err != nil {
		t.Fatal(err)
	}
	actor := uuid.New()
	username := "upload_cleanup_" + uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id,username,display_name,role,status,password_hash) VALUES ($1,$2,'Cleanup admin','student','active','hash')`, actor, username); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(pool)
	seed := func(state UploadState, expiresAt time.Time) UploadSession {
		t.Helper()
		u := UploadSession{
			ID:             uuid.New(),
			ActorUserID:    actor,
			Purpose:        UploadPurposeTeaching,
			ObjectKey:      "originals/" + uuid.NewString(),
			MinIOUploadID:  uuid.NewString(),
			DisplayName:    "cleanup.pdf",
			DeclaredMIME:   "application/pdf",
			ExpectedSize:   1,
			ExpectedSHA256: digestOf([]byte("x")),
			State:          state,
			ExpiresAt:      expiresAt,
			CreatedAt:      now.Add(-25 * time.Hour),
		}
		if err := store.CreateSession(ctx, u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	withinGrace := seed(UploadOpen, now.Add(-cleanupGrace+time.Second))
	agedOpen := seed(UploadOpen, now.Add(-cleanupGrace-time.Second))
	staleCompleting := seed(UploadCompleting, now.Add(-2*cleanupGrace))
	cancelled := seed(UploadCancelled, now.Add(-3*cleanupGrace))
	referenced := seed(UploadOpen, now.Add(-4*cleanupGrace))

	var fileID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO files (created_by) VALUES ($1) RETURNING id`, actor).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO file_versions (file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES ($1,1,'teaching',$2,'referenced.pdf','application/pdf',1,$3,'ready',$4)`, fileID, referenced.ObjectKey, referenced.ExpectedSHA256, actor); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimCleanup(ctx, now.Add(-cleanupGrace), 100)
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[uuid.UUID]UploadState, len(claimed))
	for _, u := range claimed {
		states[u.ID] = u.State
	}
	if len(states) != 3 || states[agedOpen.ID] != UploadOpen || states[staleCompleting.ID] != UploadCompleting || states[cancelled.ID] != UploadCancelled {
		t.Fatalf("claimed states=%v", states)
	}
	if _, ok := states[withinGrace.ID]; ok {
		t.Fatal("claimed session inside cleanup grace")
	}
	if _, ok := states[referenced.ID]; ok {
		t.Fatal("claimed referenced object")
	}
	for _, id := range []uuid.UUID{agedOpen.ID, staleCompleting.ID, cancelled.ID} {
		confirmed, err := store.ConfirmCleanup(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		wantState := UploadExpired
		if id == cancelled.ID {
			wantState = UploadCancelled
		}
		if confirmed.State != wantState {
			t.Fatalf("confirmed %s state=%s want=%s", id, confirmed.State, wantState)
		}
		if err := store.FinishCleanup(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	var removed, retained int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM upload_sessions WHERE id=ANY($1)`, []uuid.UUID{agedOpen.ID, staleCompleting.ID, cancelled.ID}).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM upload_sessions WHERE id=ANY($1)`, []uuid.UUID{withinGrace.ID, referenced.ID}).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if removed != 0 || retained != 2 {
		t.Fatalf("removed rows remaining=%d retained rows=%d", removed, retained)
	}
}

func TestPostgresUploadCleanupConfirmsReferenceBeforeObjectOperations(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM upload_parts; DELETE FROM upload_sessions`); err != nil {
		t.Fatal(err)
	}
	actorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id,username,display_name,role,status,password_hash) VALUES ($1,$2,'Cleanup race admin','student','active','hash')`, actorID, "upload_cleanup_race_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	base := NewPostgresStore(pool)
	candidate := UploadSession{ID: uuid.New(), ActorUserID: actorID, Purpose: UploadPurposeTeaching, ObjectKey: "originals/" + uuid.NewString(), MinIOUploadID: uuid.NewString(), DisplayName: "race.pdf", DeclaredMIME: "application/pdf", ExpectedSize: 1, ExpectedSHA256: digestOf([]byte("x")), State: UploadOpen, ExpiresAt: now.Add(-2 * cleanupGrace), CreatedAt: now.Add(-26 * time.Hour)}
	if err := base.CreateSession(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	store := &interposingCleanupStore{PostgresStore: base}
	store.beforeConfirm = func() error {
		var fileID uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO files (created_by) VALUES ($1) RETURNING id`, actorID).Scan(&fileID); err != nil {
			return err
		}
		_, err := pool.Exec(ctx, `INSERT INTO file_versions (file_id,version,purpose,object_key,display_name,declared_mime,size_bytes,sha256,processing_state,created_by) VALUES ($1,1,'teaching',$2,'race.pdf','application/pdf',1,$3,'ready',$4)`, fileID, candidate.ObjectKey, candidate.ExpectedSHA256, actorID)
		return err
	}
	objects := newFakeObjects()
	svc := NewUploadService(store, objects, TeachingUploadPolicy{}, func() time.Time { return now })
	if err := svc.CleanupExpired(ctx, 100); err == nil || err.Error() != "cleanup expired uploads" {
		t.Fatalf("cleanup err=%v", err)
	}
	if objects.abortCalls.Load() != 0 || objects.deleteCalls.Load() != 0 {
		t.Fatalf("object operations crossed reference confirmation: abort=%d delete=%d", objects.abortCalls.Load(), objects.deleteCalls.Load())
	}
	var sessions, versions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM upload_sessions WHERE id=$1`, candidate.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_versions WHERE object_key=$1`, candidate.ObjectKey).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || versions != 1 {
		t.Fatalf("sessions=%d versions=%d", sessions, versions)
	}
}

func TestPostgresUploadCleanupFreezeRejectsConcurrentCompletion(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM upload_parts; DELETE FROM upload_sessions`); err != nil {
		t.Fatal(err)
	}
	actorID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id,username,display_name,role,status,password_hash) VALUES ($1,$2,'Freeze admin','student','active','hash')`, actorID, "upload_freeze_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	base := NewPostgresStore(pool)
	candidate := UploadSession{ID: uuid.New(), ActorUserID: actorID, Purpose: UploadPurposeTeaching, ObjectKey: "originals/" + uuid.NewString(), MinIOUploadID: uuid.NewString(), DisplayName: "freeze.pdf", DeclaredMIME: "application/pdf", ExpectedSize: 1, ExpectedSHA256: digestOf([]byte("x")), State: UploadOpen, ExpiresAt: now.Add(-2 * cleanupGrace), CreatedAt: now.Add(-26 * time.Hour)}
	if err := base.CreateSession(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	confirmed := make(chan struct{})
	release := make(chan struct{})
	cleanupStore := &interposingCleanupStore{PostgresStore: base, afterConfirm: func() {
		close(confirmed)
		<-release
	}}
	cleanupObjects := newFakeObjects()
	cleanupService := NewUploadService(cleanupStore, cleanupObjects, TeachingUploadPolicy{}, func() time.Time { return now })
	cleanupResult := make(chan error, 1)
	go func() { cleanupResult <- cleanupService.CleanupExpired(ctx, 100) }()
	select {
	case <-confirmed:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not confirm and freeze candidate")
	}
	actor := Principal{User: auth.User{ID: actorID, Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "freeze-complete", IP: net.ParseIP("192.0.2.92")}
	completionService := NewUploadService(base, newFakeObjects(), TeachingUploadPolicy{}, func() time.Time { return now })
	if _, err := completionService.Complete(ctx, actor, candidate.ID); !errors.Is(err, ErrUploadConflict) && !errors.Is(err, ErrUploadExpired) {
		close(release)
		t.Fatalf("completion after freeze err=%v", err)
	}
	if _, err := base.FinishCompletion(ctx, candidate, actor); !errors.Is(err, ErrUploadConflict) {
		close(release)
		t.Fatalf("finalization after freeze err=%v", err)
	}
	var versions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_versions WHERE object_key=$1`, candidate.ObjectKey).Scan(&versions); err != nil {
		close(release)
		t.Fatal(err)
	}
	if versions != 0 {
		close(release)
		t.Fatalf("completion created %d versions after freeze", versions)
	}
	close(release)
	select {
	case err := <-cleanupResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not finish")
	}
}

type interposingCleanupStore struct {
	*PostgresStore
	beforeConfirm func() error
	afterConfirm  func()
}

func (s *interposingCleanupStore) ConfirmCleanup(ctx context.Context, id uuid.UUID) (UploadSession, error) {
	if s.beforeConfirm != nil {
		before := s.beforeConfirm
		s.beforeConfirm = nil
		if err := before(); err != nil {
			return UploadSession{}, err
		}
	}
	u, err := s.PostgresStore.ConfirmCleanup(ctx, id)
	if err == nil && s.afterConfirm != nil {
		s.afterConfirm()
	}
	return u, err
}
