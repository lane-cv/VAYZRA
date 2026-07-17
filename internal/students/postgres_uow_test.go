package students

import (
	"context"
	"crypto/sha256"
	"net"
	"strings"
	"testing"
	"time"

	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresStudentAuditMetadataNeverPersistsSecrets(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE users CASCADE"); err != nil {
		t.Fatal(err)
	}
	users := auth.NewPostgresUserStore(pool)
	admin, err := users.Create(ctx, auth.CreateUserParams{Username: "audit_admin", DisplayName: "Teacher", Role: auth.RoleAdmin, Status: auth.StatusActive, PasswordHash: "hash", MustChangePassword: false})
	if err != nil {
		t.Fatal(err)
	}
	actor := Principal{User: admin, RequestID: "audit-proof", IP: net.ParseIP("192.0.2.10")}
	svc := NewService(users, NewPostgresUnitOfWork(pool), testHasher(), time.Now)
	student, err := svc.Create(ctx, actor, CreateInput{Username: "audit_student", DisplayName: "Student", TemporaryPassword: "Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, actor, student.ID, "Replacement Password 42!"); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetStatus(ctx, actor, student.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	records, err := audit.NewPostgresWriter(pool).List(ctx, 10, 0)
	if err != nil || len(records) != 3 {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	for _, record := range records {
		for key := range record.Metadata {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "hash") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
				t.Fatalf("unsafe metadata action=%q key=%q", record.Action, key)
			}
		}
	}
}
func TestPostgresUnitOfWorkRollsBackStatusAndSessionsWhenAuditInsertFails(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE users CASCADE"); err != nil {
		t.Fatal(err)
	}
	users := auth.NewPostgresUserStore(pool)
	admin, err := users.Create(ctx, auth.CreateUserParams{Username: "transaction_admin", DisplayName: "Teacher", Role: auth.RoleAdmin, Status: auth.StatusActive, PasswordHash: "hash", MustChangePassword: false})
	if err != nil {
		t.Fatal(err)
	}
	student, err := users.Create(ctx, auth.CreateUserParams{Username: "transaction_student", DisplayName: "Student", Role: auth.RoleStudent, Status: auth.StatusActive, PasswordHash: "hash", MustChangePassword: false})
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewPostgresSessionStore(pool)
	now := time.Now().UTC()
	hash := sha256.Sum256([]byte("session-for-rollback-test"))
	if err := sessions.Create(ctx, auth.CreateSessionParams{UserID: student.ID, TokenHash: hash, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION reject_test_audit_insert() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'test audit failure'; END; $$ LANGUAGE plpgsql; CREATE TRIGGER reject_test_audit_insert BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_test_audit_insert();`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TRIGGER IF EXISTS reject_test_audit_insert ON audit_logs; DROP FUNCTION IF EXISTS reject_test_audit_insert()")
	})
	svc := NewService(users, NewPostgresUnitOfWork(pool), testHasher(), time.Now)
	err = svc.SetStatus(ctx, Principal{User: admin, RequestID: "request-rollback", IP: net.ParseIP("192.0.2.4")}, student.ID, auth.StatusDisabled)
	if err == nil {
		t.Fatal("expected failed audit insert")
	}
	updated, err := users.FindByID(ctx, student.ID)
	if err != nil || updated.Status != auth.StatusActive {
		t.Fatalf("student=%#v err=%v", updated, err)
	}
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, "SELECT revoked_at FROM sessions WHERE user_id = $1", student.ID).Scan(&revokedAt); err != nil || revokedAt != nil {
		t.Fatalf("revokedAt=%v err=%v", revokedAt, err)
	}
}
