package redisx

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ProgressWriteLimiter limits progress mutations independently from login
// throttling. A backend failure deliberately fails open: authorization and
// monotonicity remain PostgreSQL-enforced.
type ProgressWriteLimiter interface {
	AllowProgressWrite(context.Context, uuid.UUID) (ProgressDecision, error)
}

type ProgressDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type ProgressLimitPolicy struct {
	Secret    []byte
	Window    time.Duration
	MaxWrites int64
}

type ProgressLimiter struct {
	rdb    *redis.Client
	policy ProgressLimitPolicy

	degradations atomic.Uint64
	mu           sync.Mutex
	warnAfter    time.Time
}

func NewProgressWriteLimiter(rdb *redis.Client, policy ProgressLimitPolicy) *ProgressLimiter {
	if policy.Window <= 0 {
		policy.Window = time.Minute
	}
	if policy.MaxWrites <= 0 {
		policy.MaxWrites = 60
	}
	if len(policy.Secret) == 0 {
		policy.Secret = make([]byte, 32)
		if _, err := rand.Read(policy.Secret); err != nil {
			panic("read progress limiter secret")
		}
	}
	policy.Secret = append([]byte(nil), policy.Secret...)
	return &ProgressLimiter{rdb: rdb, policy: policy}
}

var progressWriteScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local ttl = redis.call('PTTL', KEYS[1])
if count > tonumber(ARGV[2]) then return {0, ttl} end
return {1, ttl}
`)

func (l *ProgressLimiter) AllowProgressWrite(ctx context.Context, sessionID uuid.UUID) (ProgressDecision, error) {
	if sessionID == uuid.Nil {
		return ProgressDecision{}, nil
	}
	if l.rdb == nil {
		return ProgressDecision{Allowed: true}, nil
	}
	result, err := progressWriteScript.Run(ctx, l.rdb, []string{l.key(sessionID)}, l.policy.Window.Milliseconds(), l.policy.MaxWrites).Int64Slice()
	if err != nil || len(result) != 2 || result[1] < 0 {
		l.degraded("allow")
		return ProgressDecision{Allowed: true}, nil
	}
	if result[0] == 0 {
		return ProgressDecision{RetryAfter: time.Duration(result[1]) * time.Millisecond}, nil
	}
	return ProgressDecision{Allowed: true}, nil
}

func (l *ProgressLimiter) key(sessionID uuid.UUID) string {
	h := hmac.New(sha256.New, l.policy.Secret)
	_, _ = h.Write([]byte("progress-session"))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(sessionID.String()))
	return "hl:progress:session:" + hex.EncodeToString(h.Sum(nil))
}

func (l *ProgressLimiter) degraded(operation string) {
	l.degradations.Add(1)
	now := time.Now()
	l.mu.Lock()
	warn := !l.warnAfter.After(now)
	if warn {
		l.warnAfter = now.Add(time.Minute)
	}
	l.mu.Unlock()
	if warn {
		log.Printf("level=warn event=progress_limiter_redis_unavailable metric=progress_limiter_redis_errors_total operation=%s", operation)
	}
}

func (l *ProgressLimiter) DegradationCount() uint64 { return l.degradations.Load() }
