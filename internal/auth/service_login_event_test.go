package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLoginEventWriteFailureReturnsGenericErrorAndRevokesIssuedSession(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc, stores := newTestService(t, now)
	stores.eventErr = errors.New("event store unavailable")
	_, raw, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if raw != "" || !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("raw=%q error=%v", raw, err)
	}
	for _, session := range stores.sessions {
		if session.RevokedAt == nil {
			t.Fatal("login event failure left a usable issued session")
		}
	}
}

func TestAuthenticateRejectsSessionIssuedBeforeUserWasDisabled(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc, stores := newTestService(t, now)
	_, raw, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	user := stores.users["student01"]
	user.Status = StatusDisabled
	stores.users["student01"] = user
	if _, err := svc.Authenticate(context.Background(), raw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error=%v", err)
	}
}
