package integration_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresUserStoreRejectsDuplicateUsernameAndAdmin(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAuthTables(t, pool)
	store := auth.NewPostgresUserStore(pool)

	admin, err := store.Create(ctx, auth.CreateUserParams{
		Username: " Admin_One ", DisplayName: "Administrator", Role: auth.RoleAdmin,
		PasswordHash: "hash", MustChangePassword: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if admin.Username != "admin_one" {
		t.Fatalf("username = %q", admin.Username)
	}

	_, err = store.Create(ctx, auth.CreateUserParams{
		Username: "ADMIN_ONE", DisplayName: "Duplicate", Role: auth.RoleStudent, PasswordHash: "hash",
	})
	if !errors.Is(err, auth.ErrConflict) {
		t.Fatalf("duplicate username error = %v", err)
	}

	_, err = store.Create(ctx, auth.CreateUserParams{
		Username: "admin_two", DisplayName: "Second admin", Role: auth.RoleAdmin, PasswordHash: "hash",
	})
	if !errors.Is(err, auth.ErrConflict) {
		t.Fatalf("second admin error = %v", err)
	}
}

func TestPostgresUserStoreFindsUpdatesAndListsStudents(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAuthTables(t, pool)
	store := auth.NewPostgresUserStore(pool)

	studentA := createUser(t, store, "student_a", auth.RoleStudent)
	studentB := createUser(t, store, "student_b", auth.RoleStudent)
	_ = createUser(t, store, "administrator", auth.RoleAdmin)

	found, err := store.FindByUsername(ctx, " STUDENT_A ")
	if err != nil || found.ID != studentA.ID {
		t.Fatalf("FindByUsername() = %#v, %v", found, err)
	}
	if err := store.UpdatePassword(ctx, studentA.ID, "new-hash", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(ctx, studentA.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	updated, err := store.FindByID(ctx, studentA.ID)
	if err != nil || updated.PasswordHash != "new-hash" || updated.MustChangePassword || updated.Status != auth.StatusDisabled {
		t.Fatalf("updated user = %#v, %v", updated, err)
	}

	students, err := store.ListStudents(ctx, 1, uuid.Nil)
	if err != nil || len(students) != 1 {
		t.Fatalf("first page = %#v, %v", students, err)
	}
	firstID := students[0].ID
	students, err = store.ListStudents(ctx, 10, firstID)
	if err != nil || len(students) != 1 || students[0].ID == firstID {
		t.Fatalf("next page = %#v, %v", students, err)
	}
	if (firstID != studentA.ID && firstID != studentB.ID) || (students[0].ID != studentA.ID && students[0].ID != studentB.ID) {
		t.Fatalf("unexpected students: first=%s next=%s", firstID, students[0].ID)
	}
	_, err = store.FindByID(ctx, uuid.New())
	if !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("missing user error = %v", err)
	}
}

func TestPostgresSessionStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAuthTables(t, pool)
	store := auth.NewPostgresUserStore(pool)
	user := createUser(t, store, "student_session", auth.RoleStudent)
	sessionStore := auth.NewPostgresSessionStore(pool)
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	hash := sha256.Sum256([]byte("session token"))
	params := auth.CreateSessionParams{
		UserID: user.ID, TokenHash: hash, UserAgent: "test-agent", CreatedAt: now,
		LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour),
	}
	if err := sessionStore.Create(ctx, params); err != nil {
		t.Fatal(err)
	}
	session, foundUser, err := sessionStore.FindActiveByTokenHash(ctx, hash, now.Add(time.Minute))
	if err != nil || foundUser.ID != user.ID {
		t.Fatalf("FindActiveByTokenHash() = %#v, %#v, %v", session, foundUser, err)
	}
	if err := sessionStore.Touch(ctx, session.ID, now.Add(10*time.Minute), now.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.Revoke(ctx, session.ID, "logout"); err != nil {
		t.Fatal(err)
	}
	_, _, err = sessionStore.FindActiveByTokenHash(ctx, hash, now.Add(11*time.Minute))
	if !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("revoked session error = %v", err)
	}
	if err := sessionStore.RevokeAllForUser(ctx, user.ID, "password changed"); err != nil {
		t.Fatal(err)
	}
}

func createUser(t *testing.T, store auth.UserStore, username string, role auth.Role) auth.User {
	t.Helper()
	user, err := store.Create(context.Background(), auth.CreateUserParams{
		Username: username, DisplayName: username, Role: role, PasswordHash: "hash", MustChangePassword: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func resetAuthTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "TRUNCATE TABLE users CASCADE"); err != nil {
		t.Fatalf("reset auth tables: %v", err)
	}
}
