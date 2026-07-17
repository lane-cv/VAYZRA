package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLogoutOthersMapsStaleConditionalMutationToUnauthenticated(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc, stores := newTestService(t, now)
	_, raw, err := svc.Login(context.Background(), LoginInput{Username: "student01", Password: "Long Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	stores.logoutOthersErr = ErrNotFound
	if err := svc.LogoutOthers(context.Background(), raw); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error=%v", err)
	}
}
