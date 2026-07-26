package redisx

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestProviderTestLimiterUsesIndependentScopedBudget(t *testing.T) {
	limiter := NewSearchLimiter(nil, ResourceLimitPolicy{
		Secret: []byte("provider-test-limiter-secret"),
		Window: time.Minute, MaxRequests: 1,
	})
	accountID := uuid.New()
	search, err := limiter.AllowSearch(context.Background(), accountID)
	if err != nil || !search.Allowed {
		t.Fatalf("search=%#v err=%v", search, err)
	}
	first, err := limiter.AllowProviderTest(context.Background(), accountID)
	if err != nil || !first.Allowed {
		t.Fatalf("first provider test=%#v err=%v", first, err)
	}
	second, err := limiter.AllowProviderTest(context.Background(), accountID)
	if err != nil || second.Allowed || second.RetryAfter <= 0 {
		t.Fatalf("second provider test=%#v err=%v", second, err)
	}
}
