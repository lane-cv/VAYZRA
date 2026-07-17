package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewServiceRejectsMissingLoginEventStore(t *testing.T) {
	stores := &testStores{users: make(map[string]User), sessions: make(map[[32]byte]*Session)}
	_, err := NewService(ServiceConfig{Users: testUserStore{stores}, Sessions: testSessionStore{stores}, PasswordRotations: testPasswordRotationStore{stores}})
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error=%v", err)
	}
}

func TestUnknownUserLoginUsesAValidDummyHashForConfiguredSaltLength(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	h := NewPasswordHasher(Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 24, KeyLength: 32})
	stores := &testStores{users: make(map[string]User), sessions: make(map[[32]byte]*Session), now: now, hasher: h}
	svc, err := NewService(ServiceConfig{Users: testUserStore{stores}, Sessions: testSessionStore{stores}, LoginEvents: testEventStore{stores}, PasswordRotations: testPasswordRotationStore{stores}, Hasher: h, Now: func() time.Time { return stores.now }})
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	svc.compare = func(encoded, password string) error {
		called++
		_, salt, _, err := parsePHC(encoded)
		if err != nil || len(salt) != 24 {
			t.Fatalf("dummy hash is not valid for configured salt length: salt=%d err=%v", len(salt), err)
		}
		return h.Compare(encoded, password)
	}
	_, _, err = svc.Login(context.Background(), LoginInput{Username: "missing", Password: "Long Temporary Password 42!"})
	if !errors.Is(err, ErrInvalidCredentials) || called != 1 {
		t.Fatalf("error=%v comparisons=%d", err, called)
	}
}

func TestChangePasswordUsesAtomicRotationAndDoesNotIssueOnFailure(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc, stores := newTestService(t, now)
	_, raw, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	stores.rotationErr = errors.New("insert replacement failed")
	_, replacement, err := svc.ChangePassword(context.Background(), ChangePasswordInput{SessionToken: raw, CurrentPassword: "Long Temporary Password 42!", NewPassword: "Changed Temporary Password 42!"})
	if replacement != "" || err == nil || stores.rotationCalls != 1 {
		t.Fatalf("replacement=%q error=%v rotations=%d", replacement, err, stores.rotationCalls)
	}
	if !stores.users["student01"].MustChangePassword {
		t.Fatal("failed atomic rotation changed user state")
	}
}
