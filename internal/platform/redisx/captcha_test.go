package redisx

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestCaptchaVerifyIsOneTimeAndRejectsWrongAnswers(t *testing.T) {
	rdb, _ := startRedis(t)
	store := NewCaptchaStore(rdb, []byte("test-captcha-secret"))
	store.random = bytes.NewReader(bytes.Repeat([]byte{0}, 1024))
	challenge, err := store.Create(context.Background(), "192.0.2.4")
	if err != nil {
		t.Fatal(err)
	}
	if challenge.ID == "" || len(challenge.PNG) == 0 || len(challenge.PNG) > 50*1024 {
		t.Fatalf("challenge=%#v", challenge)
	}
	if ok, err := store.Verify(context.Background(), challenge.ID, "WRONG"); err != nil || ok {
		t.Fatalf("wrong answer accepted=%t err=%v", ok, err)
	}
	if ok, err := store.Verify(context.Background(), challenge.ID, "AAAAA"); err != nil || ok {
		t.Fatalf("wrong-answer challenge replay accepted=%t err=%v", ok, err)
	}

	challenge, err = store.Create(context.Background(), "192.0.2.4")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Verify(context.Background(), challenge.ID, "AAAAA"); err != nil || !ok {
		t.Fatalf("correct answer accepted=%t err=%v", ok, err)
	}
	if ok, err := store.Verify(context.Background(), challenge.ID, "AAAAA"); err != nil || ok {
		t.Fatalf("replay accepted=%t err=%v", ok, err)
	}
}

func TestCaptchaExpiresAndFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	rdb, mini := startRedis(t)
	store := NewCaptchaStore(rdb, []byte("test-captcha-secret"))
	store.random = bytes.NewReader(bytes.Repeat([]byte{0}, 1024))
	challenge, err := store.Create(context.Background(), "192.0.2.4")
	if err != nil {
		t.Fatal(err)
	}
	mini.FastForward(5*time.Minute + time.Second)
	if ok, err := store.Verify(context.Background(), challenge.ID, "AAAAA"); err != nil || ok {
		t.Fatalf("expired challenge accepted=%t err=%v", ok, err)
	}
	_ = rdb.Close()
	if ok, err := store.Verify(context.Background(), "missing", "AAAAA"); err == nil || ok {
		t.Fatalf("redis-down verification accepted=%t err=%v", ok, err)
	}
}

func TestCaptchaCreateAtomicallyEnforcesPseudonymousIPAndGlobalLimits(t *testing.T) {
	rdb, _ := startRedis(t)
	store := NewCaptchaStoreWithPolicy(rdb, []byte("test-captcha-secret"), CaptchaPolicy{Window: time.Minute, PerIP: 2, Global: 3})
	store.random = bytes.NewReader(bytes.Repeat([]byte{0}, 4096))
	for range 2 {
		if _, err := store.Create(context.Background(), "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Create(context.Background(), "192.0.2.4"); err != ErrCaptchaRateLimited {
		t.Fatalf("same IP error=%v", err)
	}
	if _, err := store.Create(context.Background(), "198.51.100.9"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), "203.0.113.8"); err != ErrCaptchaRateLimited {
		t.Fatalf("global error=%v", err)
	}
	keys, err := rdb.Keys(context.Background(), "hl:captcha:*").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if bytes.Contains([]byte(key), []byte("192.0.2.4")) || bytes.Contains([]byte(key), []byte("198.51.100.9")) {
			t.Fatalf("plaintext IP in key %q", key)
		}
	}
}

func TestCaptchaCreateParallelRequestsCannotExceedGlobalCap(t *testing.T) {
	rdb, _ := startRedis(t)
	store := NewCaptchaStoreWithPolicy(rdb, []byte("test-captcha-secret"), CaptchaPolicy{Window: time.Minute, PerIP: 100, Global: 7})
	var accepted int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Create(context.Background(), "198.51.100.1"); err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			} else if err != ErrCaptchaRateLimited {
				t.Errorf("create: %v", err)
			}
		}()
	}
	wg.Wait()
	if accepted != 7 {
		t.Fatalf("accepted=%d want=7", accepted)
	}
}

func TestCaptchaCreateFailsClosedBeforeRenderingWhenRedisIsUnavailable(t *testing.T) {
	rdb, mini := startRedis(t)
	store := NewCaptchaStore(rdb, []byte("test-captcha-secret"))
	rendered := false
	store.render = func(string, io.Reader) ([]byte, error) { rendered = true; return nil, nil }
	mini.Close()
	if _, err := store.Create(context.Background(), "192.0.2.4"); err == nil || err == ErrCaptchaRateLimited {
		t.Fatalf("error=%v", err)
	}
	if rendered {
		t.Fatal("rendered image while Redis was unavailable")
	}
}
