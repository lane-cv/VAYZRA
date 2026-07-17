package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLoginCreatesSpecSessionAndRecordsSuccess(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc, stores := newTestService(t, now)
	result, raw, err := svc.Login(context.Background(), LoginInput{
		Username: " Student01 ", Password: "Long Temporary Password 42!", IP: net.ParseIP("127.0.0.1"), UserAgent: "test-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || !result.User.MustChangePassword {
		t.Fatal("missing session or first-login gate")
	}
	if result.Session.IdleExpiresAt != now.Add(7*24*time.Hour) {
		t.Fatal(result.Session.IdleExpiresAt)
	}
	if result.Session.AbsoluteExpiresAt != now.Add(30*24*time.Hour) {
		t.Fatal(result.Session.AbsoluteExpiresAt)
	}
	if got := stores.events[len(stores.events)-1]; !got.Success || got.Username != "student01" || got.UserID == nil {
		t.Fatalf("bad login event: %#v", got)
	}
}

func TestLoginUsesGenericErrorAndRecordsSafeFailureCategories(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc, stores := newTestService(t, now)
	stores.users["disabled"] = stores.userWithPassword(t, "disabled", StatusDisabled, false)

	for _, tc := range []struct{ username, password, reason string }{
		{"missing", "Long Temporary Password 42!", "unknown_user"},
		{"student01", "incorrect password", "invalid_password"},
		{"disabled", "Long Temporary Password 42!", "disabled"},
	} {
		_, raw, err := svc.Login(context.Background(), LoginInput{Username: tc.username, Password: tc.password, UserAgent: "test-agent"})
		if raw != "" || !errors.Is(err, ErrInvalidCredentials) || err.Error() != ErrInvalidCredentials.Error() {
			t.Fatalf("%s: raw=%q err=%v", tc.reason, raw, err)
		}
		got := stores.events[len(stores.events)-1]
		if got.Success || got.Username != tc.username || got.Reason != tc.reason {
			t.Fatalf("%s: event=%#v", tc.reason, got)
		}
	}

}

func TestAuthenticateRejectsExpiredAndRevokedSessionsAndTouchesAtMostEveryFiveMinutes(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc, stores := newTestService(t, now)
	_, raw, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Authenticate(context.Background(), raw)
	if err != nil || result.User.Username != "student01" || stores.touchCount != 0 {
		t.Fatalf("first authentication=%#v, %v touches=%d", result, err, stores.touchCount)
	}
	stores.now = now.Add(5*time.Minute - time.Nanosecond)
	if _, err := svc.Authenticate(context.Background(), raw); err != nil || stores.touchCount != 0 {
		t.Fatalf("early touch err=%v count=%d", err, stores.touchCount)
	}
	stores.now = now.Add(5 * time.Minute)
	result, err = svc.Authenticate(context.Background(), raw)
	if err != nil || stores.touchCount != 1 || result.Session.IdleExpiresAt != stores.now.Add(7*24*time.Hour) {
		t.Fatalf("touch result=%#v err=%v count=%d", result, err, stores.touchCount)
	}

	_, idleRaw, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	idleSession, err := svc.Authenticate(context.Background(), idleRaw)
	if err != nil {
		t.Fatal(err)
	}
	stores.now = idleSession.Session.IdleExpiresAt
	if _, err := svc.Authenticate(context.Background(), idleRaw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("idle expiry error=%v", err)
	}

	_, absoluteRaw, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	absoluteSession, err := svc.Authenticate(context.Background(), absoluteRaw)
	if err != nil {
		t.Fatal(err)
	}
	stores.now = absoluteSession.Session.AbsoluteExpiresAt
	if _, err := svc.Authenticate(context.Background(), absoluteRaw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("absolute expiry error=%v", err)
	}
	_, raw, err = svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	hash := tokenHash(raw)
	stores.sessions[hash].RevokedAt = &stores.now
	if _, err := svc.Authenticate(context.Background(), raw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked error=%v", err)
	}
}

func TestChangePasswordVerifiesCurrentRevokesSessionsAndReplacesCurrentSession(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc, stores := newTestService(t, now)
	_, rawA, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	_, rawB, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	_, replacement, err := svc.ChangePassword(context.Background(), ChangePasswordInput{
		SessionToken: rawA, CurrentPassword: "Long Temporary Password 42!", NewPassword: "Changed Temporary Password 42!",
	})
	if err != nil || replacement == "" || replacement == rawA {
		t.Fatalf("change err=%v replacement=%q", err, replacement)
	}
	if stores.users["student01"].MustChangePassword || stores.rotationCalls != 1 {
		t.Fatalf("user=%#v rotations=%d", stores.users["student01"], stores.rotationCalls)
	}
	if _, err := svc.Authenticate(context.Background(), rawA); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("old current token error=%v", err)
	}
	if _, err := svc.Authenticate(context.Background(), rawB); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("other token error=%v", err)
	}
	if _, err := svc.Authenticate(context.Background(), replacement); err != nil {
		t.Fatalf("replacement token error=%v", err)
	}
}

func TestChangePasswordRejectsInvalidCurrentPasswordAndLogoutOperationsRevoke(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc, _ := newTestService(t, now)
	_, rawA, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	_, rawB, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ChangePassword(context.Background(), ChangePasswordInput{SessionToken: rawA, CurrentPassword: "wrong password", NewPassword: "Changed Temporary Password 42!"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong current password error=%v", err)
	}
	if err := svc.LogoutOthers(context.Background(), rawA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(context.Background(), rawA); err != nil {
		t.Fatalf("current session error=%v", err)
	}
	if _, err := svc.Authenticate(context.Background(), rawB); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("other session error=%v", err)
	}
	if err := svc.Logout(context.Background(), rawA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(context.Background(), rawA); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("logged-out session error=%v", err)
	}
}

type testStores struct {
	users           map[string]User
	sessions        map[[32]byte]*Session
	events          []LoginEvent
	now             time.Time
	touchCount      int
	revokeAllCount  int
	rotationCalls   int
	rotationErr     error
	eventErr        error
	logoutOthersErr error
	hasher          PasswordHasher
}

func newTestService(t *testing.T, now time.Time) (*Service, *testStores) {
	t.Helper()
	h := NewPasswordHasher(Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	s := &testStores{users: make(map[string]User), sessions: make(map[[32]byte]*Session), now: now, hasher: h}
	s.users["student01"] = s.userWithPassword(t, "student01", StatusActive, true)
	svc, err := NewService(ServiceConfig{Users: testUserStore{s}, Sessions: testSessionStore{s}, LoginEvents: testEventStore{s}, PasswordRotations: testPasswordRotationStore{s}, Hasher: h, Now: func() time.Time { return s.now }})
	if err != nil {
		t.Fatal(err)
	}
	return svc, s
}

func (s *testStores) userWithPassword(t *testing.T, username string, status Status, mustChange bool) User {
	t.Helper()
	hash, err := s.hasher.Hash("Long Temporary Password 42!")
	if err != nil {
		t.Fatal(err)
	}
	return User{ID: uuid.New(), Username: username, Status: status, PasswordHash: hash, MustChangePassword: mustChange}
}

type testUserStore struct{ s *testStores }

func (f testUserStore) FindByUsername(_ context.Context, username string) (User, error) {
	u, ok := f.s.users[normalizeUsername(username)]
	if !ok {
		return User{}, ErrNotFound
	}
	return u, nil
}
func (f testUserStore) FindByID(_ context.Context, id uuid.UUID) (User, error) {
	for _, u := range f.s.users {
		if u.ID == id {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}
func (testUserStore) Create(context.Context, CreateUserParams) (User, error) {
	return User{}, errors.New("not implemented")
}
func (f testUserStore) UpdatePassword(_ context.Context, id uuid.UUID, hash string, mustChange bool) error {
	for key, u := range f.s.users {
		if u.ID == id {
			u.PasswordHash, u.MustChangePassword = hash, mustChange
			f.s.users[key] = u
			return nil
		}
	}
	return ErrNotFound
}
func (testUserStore) SetStatus(context.Context, uuid.UUID, Status) error {
	return errors.New("not implemented")
}
func (testUserStore) ListStudents(context.Context, int, uuid.UUID) ([]User, error) {
	return nil, errors.New("not implemented")
}

type testSessionStore struct{ s *testStores }

func (f testSessionStore) Create(_ context.Context, p CreateSessionParams) error {
	session := &Session{ID: p.ID, UserID: p.UserID, TokenHash: p.TokenHash, UserAgent: p.UserAgent, IP: p.IP, CreatedAt: p.CreatedAt, LastSeenAt: p.LastSeenAt, IdleExpiresAt: p.IdleExpiresAt, AbsoluteExpiresAt: p.AbsoluteExpiresAt}
	f.s.sessions[p.TokenHash] = session
	return nil
}
func (f testSessionStore) FindActiveByTokenHash(_ context.Context, hash [32]byte, now time.Time) (Session, User, error) {
	session, ok := f.s.sessions[hash]
	if !ok || session.RevokedAt != nil || !session.IdleExpiresAt.After(now) || !session.AbsoluteExpiresAt.After(now) {
		return Session{}, User{}, ErrNotFound
	}
	user, err := testUserStore{f.s}.FindByID(context.Background(), session.UserID)
	return *session, user, err
}
func (f testSessionStore) Touch(_ context.Context, id uuid.UUID, lastSeen, idle time.Time) error {
	for _, session := range f.s.sessions {
		if session.ID == id && session.RevokedAt == nil {
			session.LastSeenAt, session.IdleExpiresAt = lastSeen, idle
			if session.IdleExpiresAt.After(session.AbsoluteExpiresAt) {
				session.IdleExpiresAt = session.AbsoluteExpiresAt
			}
			f.s.touchCount++
			return nil
		}
	}
	return ErrNotFound
}
func (f testSessionStore) Revoke(_ context.Context, id uuid.UUID, reason string) error {
	for _, session := range f.s.sessions {
		if session.ID == id && session.RevokedAt == nil {
			now := f.s.now
			session.RevokedAt, session.RevokeReason = &now, &reason
			return nil
		}
	}
	return ErrNotFound
}
func (f testSessionStore) RevokeAllForUser(_ context.Context, userID uuid.UUID, reason string) error {
	f.s.revokeAllCount++
	for _, session := range f.s.sessions {
		if session.UserID == userID && session.RevokedAt == nil {
			now := f.s.now
			session.RevokedAt, session.RevokeReason = &now, &reason
		}
	}
	return nil
}
func (f testSessionStore) RevokeAllExceptForUser(_ context.Context, userID, exceptID uuid.UUID, now time.Time, reason string) error {
	if f.s.logoutOthersErr != nil {
		return f.s.logoutOthersErr
	}
	retained := false
	for _, session := range f.s.sessions {
		if session.ID == exceptID && session.UserID == userID && session.RevokedAt == nil && session.IdleExpiresAt.After(now) && session.AbsoluteExpiresAt.After(now) {
			retained = true
			break
		}
	}
	if !retained {
		return ErrNotFound
	}
	for _, session := range f.s.sessions {
		if session.UserID == userID && session.ID != exceptID && session.RevokedAt == nil {
			revokedAt := f.s.now
			session.RevokedAt, session.RevokeReason = &revokedAt, &reason
		}
	}
	return nil
}

type testEventStore struct{ s *testStores }

func (f testEventStore) RecordLoginEvent(_ context.Context, event LoginEvent) error {
	if f.s.eventErr != nil {
		return f.s.eventErr
	}
	f.s.events = append(f.s.events, event)
	return nil
}

func tokenHash(raw string) [32]byte { return sha256.Sum256([]byte(raw)) }

type testPasswordRotationStore struct{ s *testStores }

func (f testPasswordRotationStore) RotatePassword(_ context.Context, params PasswordRotationParams) error {
	f.s.rotationCalls++
	if f.s.rotationErr != nil {
		return f.s.rotationErr
	}
	for key, user := range f.s.users {
		if user.ID != params.UserID {
			continue
		}
		if user.PasswordHash != params.ExpectedPasswordHash || params.ReplacementSession.UserID != user.ID {
			return ErrConflict
		}
		user.PasswordHash = params.PasswordHash
		user.MustChangePassword = params.MustChangePassword
		f.s.users[key] = user
		for _, session := range f.s.sessions {
			if session.UserID == user.ID && session.RevokedAt == nil {
				now := f.s.now
				reason := "password changed"
				session.RevokedAt, session.RevokeReason = &now, &reason
			}
		}
		p := params.ReplacementSession
		f.s.sessions[p.TokenHash] = &Session{ID: p.ID, UserID: p.UserID, TokenHash: p.TokenHash, UserAgent: p.UserAgent, IP: p.IP, CreatedAt: p.CreatedAt, LastSeenAt: p.LastSeenAt, IdleExpiresAt: p.IdleExpiresAt, AbsoluteExpiresAt: p.AbsoluteExpiresAt}
		return nil
	}
	return ErrNotFound
}
