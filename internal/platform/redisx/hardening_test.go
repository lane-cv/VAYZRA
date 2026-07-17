package redisx

import (
	"bytes"
	"context"
	"crypto/rand"
	"image/png"
	"testing"
	"time"
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
	if limiter.local.Len() > fallbackMaxKeys {
		t.Fatalf("local state=%d", limiter.local.Len())
	}
	now = now.Add(redisCooldown + time.Millisecond)
	if limiter.breakerOpen() {
		t.Fatal("expected probe window to reopen")
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
