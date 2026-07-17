package redisx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestLoginLimiterBlocksRepeatedFailures(t *testing.T) {
	rdb, _ := startRedis(t)
	limiter := NewLoginLimiter(rdb, Policy{Secret: []byte("test-limiter-secret"), Window: 15 * time.Minute, AccountFailures: 5, IPFailures: 20, Lockout: 15 * time.Minute})
	ctx := context.Background()
	for range 5 {
		if err := limiter.RecordFailure(ctx, " Student01 ", "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	d, err := limiter.Allow(ctx, "student01", "192.0.2.4")
	if err != nil || d.Allowed || d.RetryAfter <= 0 {
		t.Fatalf("decision=%#v err=%v", d, err)
	}
}

func TestLoginLimiterChallengesThenClearsOnlyAccountFailuresOnSuccess(t *testing.T) {
	rdb, _ := startRedis(t)
	limiter := NewLoginLimiter(rdb, testPolicy())
	ctx := context.Background()
	for range 3 {
		if err := limiter.RecordFailure(ctx, "student01", "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	decision, err := limiter.Allow(ctx, "student01", "192.0.2.4")
	if err != nil || !decision.Allowed || !decision.ChallengeRequired {
		t.Fatalf("before success decision=%#v err=%v", decision, err)
	}
	if err := limiter.RecordSuccess(ctx, "student01", "192.0.2.4"); err != nil {
		t.Fatal(err)
	}
	decision, err = limiter.Allow(ctx, "student01", "192.0.2.4")
	if err != nil || !decision.Allowed || decision.ChallengeRequired {
		t.Fatalf("after success decision=%#v err=%v", decision, err)
	}
}

func TestLoginLimiterUsesBoundedFallbackWhenRedisIsUnavailable(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: 0, DialTimeout: time.Millisecond})
	t.Cleanup(func() { _ = rdb.Close() })
	limiter := NewLoginLimiter(rdb, testPolicy())
	ctx := context.Background()
	first, err := limiter.Allow(ctx, "student01", "192.0.2.4")
	if err != nil || !first.Allowed {
		t.Fatalf("first decision=%#v err=%v", first, err)
	}
	second, err := limiter.Allow(ctx, "student01", "192.0.2.4")
	if err != nil || second.Allowed || second.RetryAfter <= 0 {
		t.Fatalf("second decision=%#v err=%v", second, err)
	}
}

func TestLoginLimiterDoesNotUseRawUsernameOrIPInRedisKeys(t *testing.T) {
	rdb, mini := startRedis(t)
	limiter := NewLoginLimiter(rdb, testPolicy())
	if err := limiter.RecordFailure(context.Background(), " Student01 ", "192.0.2.4"); err != nil {
		t.Fatal(err)
	}
	for _, key := range mini.Keys() {
		if strings.Contains(strings.ToLower(key), "student01") || strings.Contains(key, "192.0.2.4") {
			t.Fatalf("raw identity leaked in key %q", key)
		}
	}
}

func startRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mini
}

func testPolicy() Policy {
	return Policy{Secret: []byte("test-limiter-secret"), Window: 15 * time.Minute, AccountFailures: 5, IPFailures: 20, Lockout: 15 * time.Minute}
}
