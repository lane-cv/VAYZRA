package redisx

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"image/png"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestLimiterKeepsChallengePendingAcrossRedisOutage(t *testing.T) {
	rdb, _ := startRedis(t)
	limiter := NewLoginLimiter(rdb, testPolicy())
	for range 3 {
		if err := limiter.RecordFailure(context.Background(), "student01", "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	if err := rdb.Close(); err != nil {
		t.Fatal(err)
	}
	decision, err := limiter.Allow(context.Background(), "student01", "192.0.2.4")
	if err != nil || !decision.Allowed || !decision.ChallengeRequired {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}

func TestLimiterCircuitBreakerRecoversAndLocalStateRemainsBounded(t *testing.T) {
	rdb, _ := startRedis(t)
	limiter := NewLoginLimiter(rdb, testPolicy())
	now := time.Now()
	limiter.now = func() time.Time { return now }
	if err := rdb.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := limiter.Allow(context.Background(), "one", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if !limiter.breakerOpen() {
		t.Fatal("expected breaker to open")
	}
	for i := 0; i < fallbackMaxKeys+10; i++ {
		if _, err := limiter.Allow(context.Background(), "user", "192.0.2."+string(rune(i))); err != nil {
			t.Fatal(err)
		}
	}
	if limiter.fallback.Len() > fallbackMaxKeys {
		t.Fatalf("local state=%d", limiter.fallback.Len())
	}
	now = now.Add(redisCooldown + time.Millisecond)
	if limiter.breakerOpen() {
		t.Fatal("expected probe window to reopen")
	}
}

func TestLimiterPermitsOneRedisProbeAfterCooldown(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", MaxRetries: 0,
		Dialer: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("offline") },
	})
	t.Cleanup(func() { _ = rdb.Close() })
	limiter := NewLoginLimiter(rdb, testPolicy())
	now := time.Now()
	limiter.now = func() time.Time { return now }
	if _, err := limiter.Allow(context.Background(), "student01", "192.0.2.4"); err != nil {
		t.Fatal(err)
	}
	before := limiter.DegradationCount()
	now = now.Add(redisCooldown + time.Millisecond)

	start := make(chan struct{})
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			_, _ = limiter.Allow(context.Background(), fmt.Sprintf("probe-%d", i), fmt.Sprintf("198.51.100.%d", i+1))
		}(i)
	}
	close(start)
	wait.Wait()
	if got := limiter.DegradationCount() - before; got != 1 {
		t.Fatalf("redis probes after cooldown = %d, want 1", got)
	}
}

func TestLimiterFallbackReturnsRemainingRetryAfter(t *testing.T) {
	limiter := NewLoginLimiter(nil, testPolicy())
	now := time.Now()
	limiter.now = func() time.Time { return now }
	if decision, err := limiter.Allow(context.Background(), "student01", "192.0.2.4"); err != nil || !decision.Allowed {
		t.Fatalf("first decision=%#v err=%v", decision, err)
	}
	now = now.Add(3 * time.Second)
	decision, err := limiter.Allow(context.Background(), "student01", "192.0.2.4")
	if err != nil || decision.Allowed || decision.RetryAfter != 7*time.Second {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}

func TestLimiterLocalChallengeExpiresWithRedisAccountWindow(t *testing.T) {
	rdb, mini := startRedis(t)
	limiter := NewLoginLimiter(rdb, testPolicy())
	now := time.Now()
	limiter.now = func() time.Time { return now }
	for _, advance := range []time.Duration{0, 7 * time.Minute, 7 * time.Minute} {
		if advance > 0 {
			mini.FastForward(advance)
			now = now.Add(advance)
		}
		if err := limiter.RecordFailure(context.Background(), "student01", "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	keys := limiter.keys("student01", "192.0.2.4")
	limiter.localMu.Lock()
	expires, ok := limiter.challenges.getExpiry("c:"+keys.account, now)
	limiter.localMu.Unlock()
	if !ok || expires.Sub(now) < 59*time.Second || expires.Sub(now) > time.Minute {
		t.Fatalf("local challenge remaining=%s exists=%t, want about 1m", expires.Sub(now), ok)
	}

	mini.FastForward(time.Minute + time.Millisecond)
	now = now.Add(time.Minute + time.Millisecond)
	if err := rdb.Close(); err != nil {
		t.Fatal(err)
	}
	decision, err := limiter.Allow(context.Background(), "student01", "192.0.2.4")
	if err != nil || decision.ChallengeRequired {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}

func TestLimiterDoesNotRenewLocalChallengeOnAuthoritativeAllow(t *testing.T) {
	rdb, _ := startRedis(t)
	limiter := NewLoginLimiter(rdb, testPolicy())
	now := time.Now()
	limiter.now = func() time.Time { return now }
	for range 3 {
		if err := limiter.RecordFailure(context.Background(), "student01", "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(limiter.policy.Window - time.Second)
	if decision, err := limiter.Allow(context.Background(), "student01", "192.0.2.4"); err != nil || !decision.ChallengeRequired {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
	if err := rdb.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	decision, err := limiter.Allow(context.Background(), "student01", "192.0.2.4")
	if err != nil || decision.ChallengeRequired {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}

func TestLimiterClearsLocalChallengeWhenRedisSaysNoChallenge(t *testing.T) {
	rdb, _ := startRedis(t)
	limiter := NewLoginLimiter(rdb, testPolicy())
	for range 3 {
		if err := limiter.RecordFailure(context.Background(), "student01", "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	keys := limiter.keys("student01", "192.0.2.4")
	if err := rdb.Del(context.Background(), keys.account).Err(); err != nil {
		t.Fatal(err)
	}
	decision, err := limiter.Allow(context.Background(), "student01", "192.0.2.4")
	if err != nil || decision.ChallengeRequired || limiter.localChallenge(keys.account) {
		t.Fatalf("decision=%#v local=%t err=%v", decision, limiter.localChallenge(keys.account), err)
	}
}

func TestCaptchaGlyphGeometryVariesWithControlledRandomness(t *testing.T) {
	lowRandom := bytes.NewReader(bytes.Repeat([]byte{0}, 256))
	highRandom := bytes.NewReader(bytes.Repeat([]byte{255}, 256))
	_, low, err := renderCaptchaImage("ABCDE", lowRandom)
	if err != nil {
		t.Fatal(err)
	}
	_, high, err := renderCaptchaImage("ABCDE", highRandom)
	if err != nil {
		t.Fatal(err)
	}
	if len(low) != 5 || len(high) != 5 || reflect.DeepEqual(low, high) {
		t.Fatalf("glyph transforms did not vary: low=%#v high=%#v", low, high)
	}
	if low[0].x == high[0].x && low[0].y == high[0].y && low[0].shear == high[0].shear && low[0].rotation == high[0].rotation && low[0].scaleX == high[0].scaleX && low[0].scaleY == high[0].scaleY {
		t.Fatalf("first glyph geometry did not change: low=%#v high=%#v", low[0], high[0])
	}
}

func TestCaptchaRenderingIsRandomValidPNGAndBounded(t *testing.T) {
	first, err := renderCaptcha("ABCDE", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderCaptcha("ABCDE", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(second) || len(first) == 0 || len(first) > 50*1024 {
		t.Fatal("captcha was not randomized and bounded")
	}
	if _, err := png.DecodeConfig(bytes.NewReader(first)); err != nil {
		t.Fatalf("invalid png: %v", err)
	}
}
