package integration_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestPostgresPasswordRotationRollsBackAndSerializes(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAuthTables(t, pool)
	users := auth.NewPostgresUserStore(pool)
	sessions := auth.NewPostgresSessionStore(pool)
	user, err := users.Create(ctx, auth.CreateUserParams{Username: "rotation_student", DisplayName: "rotation", Role: auth.RoleStudent, PasswordHash: "old-password-hash", MustChangePassword: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	oldHash := tokenHashForIntegration("existing-session")
	if err := sessions.Create(ctx, auth.CreateSessionParams{ID: uuid.New(), UserID: user.ID, TokenHash: oldHash, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	rollback := auth.PasswordRotationParams{UserID: user.ID, ExpectedPasswordHash: "old-password-hash", PasswordHash: "new-password-hash", MustChangePassword: false, ReplacementSession: auth.CreateSessionParams{ID: uuid.New(), UserID: user.ID, TokenHash: oldHash, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour)}}
	if err := sessions.RotatePassword(ctx, rollback); err == nil {
		t.Fatal("expected duplicate token insertion to fail")
	}
	unchanged, err := users.FindByID(ctx, user.ID)
	if err != nil || unchanged.PasswordHash != "old-password-hash" || !unchanged.MustChangePassword {
		t.Fatalf("rollback user=%#v err=%v", unchanged, err)
	}
	if _, _, err := sessions.FindActiveByTokenHash(ctx, oldHash, now.Add(time.Minute)); err != nil {
		t.Fatalf("rollback revoked existing session: %v", err)
	}

	first := auth.PasswordRotationParams{UserID: user.ID, ExpectedPasswordHash: "old-password-hash", PasswordHash: "new-password-hash-a", MustChangePassword: false, ReplacementSession: auth.CreateSessionParams{ID: uuid.New(), UserID: user.ID, TokenHash: tokenHashForIntegration("replacement-a"), CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour)}}
	second := first
	second.PasswordHash = "new-password-hash-b"
	second.ReplacementSession.ID = uuid.New()
	second.ReplacementSession.TokenHash = tokenHashForIntegration("replacement-b")

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, params := range []auth.PasswordRotationParams{first, second} {
		params := params
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- sessions.RotatePassword(ctx, params)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	conflicts := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, auth.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("rotation error=%v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	updated, err := users.FindByID(ctx, user.ID)
	if err != nil || updated.PasswordHash == "old-password-hash" || updated.MustChangePassword {
		t.Fatalf("updated user=%#v err=%v", updated, err)
	}
	active := 0
	for _, hash := range [][32]byte{first.ReplacementSession.TokenHash, second.ReplacementSession.TokenHash} {
		if _, _, err := sessions.FindActiveByTokenHash(ctx, hash, now.Add(time.Minute)); err == nil {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active replacement sessions=%d", active)
	}
}

func tokenHashForIntegration(raw string) [32]byte { return sha256.Sum256([]byte(raw)) }

func TestPostgresSessionPredicatesAndLoginEventPersistence(t *testing.T) {
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	resetAuthTables(t, pool)
	users := auth.NewPostgresUserStore(pool)
	sessions := auth.NewPostgresSessionStore(pool)
	user := createUser(t, users, "session_predicates", auth.RoleStudent)
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	firstHash := tokenHashForIntegration("predicate-first")
	secondHash := tokenHashForIntegration("predicate-second")
	firstID, secondID := uuid.New(), uuid.New()
	for _, params := range []auth.CreateSessionParams{
		{ID: firstID, UserID: user.ID, TokenHash: firstHash, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour)},
		{ID: secondID, UserID: user.ID, TokenHash: secondHash, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(2 * time.Hour)},
	} {
		if err := sessions.Create(ctx, params); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.Touch(ctx, firstID, now.Add(time.Minute), now.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	first, _, err := sessions.FindActiveByTokenHash(ctx, firstHash, now.Add(time.Minute))
	if err != nil || !first.LastSeenAt.Equal(now) {
		t.Fatalf("early touch session=%#v err=%v", first, err)
	}
	if err := sessions.Touch(ctx, firstID, now.Add(5*time.Minute), now.Add(95*time.Minute)); err != nil {
		t.Fatal(err)
	}
	first, _, err = sessions.FindActiveByTokenHash(ctx, firstHash, now.Add(5*time.Minute))
	if err != nil || !first.LastSeenAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("throttled touch session=%#v err=%v", first, err)
	}
	if err := sessions.RevokeAllExceptForUser(ctx, user.ID, firstID, "logout others"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sessions.FindActiveByTokenHash(ctx, firstHash, now.Add(6*time.Minute)); err != nil {
		t.Fatalf("retained session error=%v", err)
	}
	if _, _, err := sessions.FindActiveByTokenHash(ctx, secondHash, now.Add(6*time.Minute)); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("other session error=%v", err)
	}
	if err := sessions.RecordLoginEvent(ctx, auth.LoginEvent{UserID: &user.ID, Username: " SESSION_PREDICATES ", Success: true, Reason: "success", UserAgent: "integration", OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	var username, reason string
	var success bool
	if err := pool.QueryRow(ctx, `SELECT username, success, reason FROM login_events WHERE user_id = $1`, user.ID).Scan(&username, &success, &reason); err != nil {
		t.Fatal(err)
	}
	if username != "session_predicates" || !success || reason != "success" {
		t.Fatalf("login event username=%q success=%v reason=%q", username, success, reason)
	}
}
