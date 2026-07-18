package redisx

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestProgressLimiterAtomicallyIsolatesSessionAndAccountBudgets(t *testing.T) {
	rdb, _ := startRedis(t)
	limiter := NewProgressWriteLimiter(rdb, ProgressLimitPolicy{Secret: []byte("dual-progress-secret"), Window: time.Minute, SessionMaxWrites: 2, AccountMaxWrites: 3})
	account, otherAccount := uuid.New(), uuid.New()
	firstSession, secondSession := uuid.New(), uuid.New()
	for _, session := range []uuid.UUID{firstSession, firstSession, secondSession} {
		decision, err := limiter.AllowProgressWrite(context.Background(), session, account)
		if err != nil || !decision.Allowed {
			t.Fatalf("allowed session=%s decision=%#v err=%v", session, decision, err)
		}
	}
	decision, err := limiter.AllowProgressWrite(context.Background(), secondSession, account)
	if err != nil || decision.Allowed || decision.RetryAfter <= 0 {
		t.Fatalf("account cap decision=%#v err=%v", decision, err)
	}
	decision, err = limiter.AllowProgressWrite(context.Background(), uuid.New(), otherAccount)
	if err != nil || !decision.Allowed {
		t.Fatalf("isolated account decision=%#v err=%v", decision, err)
	}
}

func TestSearchLimiterAccountIsolationAndDefaults(t *testing.T) {
	rdb, _ := startRedis(t)
	limiter := NewSearchLimiter(rdb, ResourceLimitPolicy{Secret: []byte("search-secret")})
	first, second := uuid.New(), uuid.New()
	for range 30 {
		decision, err := limiter.AllowSearch(context.Background(), first)
		if err != nil || !decision.Allowed {
			t.Fatalf("allowed decision=%#v err=%v", decision, err)
		}
	}
	decision, err := limiter.AllowSearch(context.Background(), first)
	if err != nil || decision.Allowed || decision.RetryAfter <= 0 {
		t.Fatalf("capped decision=%#v err=%v", decision, err)
	}
	decision, err = limiter.AllowSearch(context.Background(), second)
	if err != nil || !decision.Allowed {
		t.Fatalf("isolated decision=%#v err=%v", decision, err)
	}
}

func TestResourceLimitersUseBoundedOutageState(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: 0, Dialer: func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }})
	t.Cleanup(func() { _ = rdb.Close() })
	search := NewSearchLimiter(rdb, ResourceLimitPolicy{Secret: []byte("search-secret"), LocalMaxKeys: 8})
	progress := NewProgressWriteLimiter(rdb, ProgressLimitPolicy{Secret: []byte("progress-secret"), LocalMaxKeys: 8})
	for range 64 {
		account, session := uuid.New(), uuid.New()
		_, _ = search.AllowSearch(context.Background(), account)
		_, _ = progress.AllowProgressWrite(context.Background(), session, account)
	}
	if search.LocalKeyCount() > 8 || progress.LocalKeyCount() > 8 {
		t.Fatalf("outage keys search=%d progress=%d", search.LocalKeyCount(), progress.LocalKeyCount())
	}
	if search.DegradationCount() == 0 || progress.DegradationCount() == 0 {
		t.Fatalf("missing degradation metrics search=%d progress=%d", search.DegradationCount(), progress.DegradationCount())
	}
}
