package redisx

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestCaptchaVerifyIsOneTimeAndRejectsWrongAnswers(t *testing.T) {
	rdb, _ := startRedis(t)
	store := NewCaptchaStore(rdb, []byte("test-captcha-secret"))
	store.random = bytes.NewReader(bytes.Repeat([]byte{0}, 512))
	challenge, err := store.Create(context.Background())
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

	challenge, err = store.Create(context.Background())
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
	store.random = bytes.NewReader(bytes.Repeat([]byte{0}, 512))
	challenge, err := store.Create(context.Background())
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
