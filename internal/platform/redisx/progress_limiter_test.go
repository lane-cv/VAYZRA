package redisx

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestProgressLimiterCapsOneSessionAndIsolatesOthers(t *testing.T) {
	rdb, _ := startRedis(t)
	limiter := NewProgressWriteLimiter(rdb, ProgressLimitPolicy{Secret: []byte("progress-test-secret"), Window: time.Minute, MaxWrites: 2})
	first, second := uuid.New(), uuid.New()
	for range 2 {
		decision, err := limiter.AllowProgressWrite(context.Background(), first, first)
		if err != nil || !decision.Allowed {
			t.Fatalf("allowed decision=%#v err=%v", decision, err)
		}
	}
	decision, err := limiter.AllowProgressWrite(context.Background(), first, first)
	if err != nil || decision.Allowed || decision.RetryAfter <= 0 {
		t.Fatalf("capped decision=%#v err=%v", decision, err)
	}
	decision, err = limiter.AllowProgressWrite(context.Background(), second, second)
	if err != nil || !decision.Allowed {
		t.Fatalf("other session decision=%#v err=%v", decision, err)
	}
}

func TestProgressLimiterUsesPseudonymousKeysAndFailsOpen(t *testing.T) {
	rdb, mini := startRedis(t)
	limiter := NewProgressWriteLimiter(rdb, ProgressLimitPolicy{Secret: []byte("progress-test-secret"), Window: time.Minute, MaxWrites: 1})
	sessionID := uuid.New()
	if _, err := limiter.AllowProgressWrite(context.Background(), sessionID, sessionID); err != nil {
		t.Fatal(err)
	}
	for _, key := range mini.Keys() {
		if strings.Contains(key, sessionID.String()) {
			t.Fatalf("raw session id leaked in Redis key %q", key)
		}
	}
	if err := rdb.Close(); err != nil {
		t.Fatal(err)
	}
	decision, err := limiter.AllowProgressWrite(context.Background(), sessionID, sessionID)
	if err != nil || !decision.Allowed || limiter.DegradationCount() == 0 {
		t.Fatalf("outage decision=%#v err=%v degradations=%d", decision, err, limiter.DegradationCount())
	}
}

func TestProgressLimiterUnavailableClientFailsOpen(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: 0, Dialer: func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }})
	t.Cleanup(func() { _ = rdb.Close() })
	limiter := NewProgressWriteLimiter(rdb, ProgressLimitPolicy{Secret: []byte("progress-test-secret")})
	decision, err := limiter.AllowProgressWrite(context.Background(), uuid.New(), uuid.New())
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}
