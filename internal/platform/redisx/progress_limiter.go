package redisx

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ProgressWriteLimiter applies both a session budget and an account budget.
type ProgressWriteLimiter interface {
	AllowProgressWrite(context.Context, uuid.UUID, uuid.UUID) (ProgressDecision, error)
}

type ProgressDecision = ResourceDecision

type ProgressLimitPolicy struct {
	Secret           []byte
	Window           time.Duration
	MaxWrites        int64 // retained as a compatibility alias for SessionMaxWrites
	SessionMaxWrites int64
	AccountMaxWrites int64
	LocalMaxKeys     int
}

type ProgressLimiter struct {
	rdb          *redis.Client
	policy       ProgressLimitPolicy
	localMu      sync.Mutex
	local        *boundedCounterLRU
	degradations atomic.Uint64
	warnMu       sync.Mutex
	warnAfter    time.Time
	logCategory  func(string)
}

func NewProgressWriteLimiter(rdb *redis.Client, policy ProgressLimitPolicy) *ProgressLimiter {
	return NewProgressWriteLimiterWithLog(rdb, policy, nil)
}

func NewProgressWriteLimiterWithLog(
	rdb *redis.Client,
	policy ProgressLimitPolicy,
	logCategory func(string),
) *ProgressLimiter {
	if policy.Window <= 0 {
		policy.Window = time.Minute
	}
	if policy.SessionMaxWrites <= 0 {
		policy.SessionMaxWrites = policy.MaxWrites
	}
	if policy.SessionMaxWrites <= 0 {
		policy.SessionMaxWrites = 60
	}
	if policy.AccountMaxWrites <= 0 {
		policy.AccountMaxWrites = 120
	}
	if policy.LocalMaxKeys <= 0 || policy.LocalMaxKeys > resourceFallbackMaxKeys {
		policy.LocalMaxKeys = resourceFallbackMaxKeys
	}
	policy.Secret = normalizedResourceSecret(policy.Secret)
	return &ProgressLimiter{
		rdb: rdb, policy: policy,
		local:       newBoundedCounterLRU(policy.LocalMaxKeys),
		logCategory: logCategory,
	}
}

var progressWriteScript = redis.NewScript(`
local session = redis.call('INCR', KEYS[1])
if session == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local sessionTTL = redis.call('PTTL', KEYS[1])
local account = redis.call('INCR', KEYS[2])
if account == 1 then redis.call('PEXPIRE', KEYS[2], ARGV[1]) end
local accountTTL = redis.call('PTTL', KEYS[2])
local retry = sessionTTL
if accountTTL > retry then retry = accountTTL end
if session > tonumber(ARGV[2]) or account > tonumber(ARGV[3]) then return {0, retry} end
return {1, retry}
`)

func (l *ProgressLimiter) AllowProgressWrite(ctx context.Context, sessionID, accountID uuid.UUID) (ProgressDecision, error) {
	if sessionID == uuid.Nil || accountID == uuid.Nil {
		return ProgressDecision{}, nil
	}
	sessionKey := resourceKey(l.policy.Secret, "progress-session", sessionID)
	accountKey := resourceKey(l.policy.Secret, "progress-account", accountID)
	if l.rdb != nil {
		result, err := progressWriteScript.Run(ctx, l.rdb, []string{sessionKey, accountKey}, l.policy.Window.Milliseconds(), l.policy.SessionMaxWrites, l.policy.AccountMaxWrites).Int64Slice()
		if err == nil && len(result) == 2 && result[1] >= 0 {
			return resourceResult(result), nil
		}
		l.degraded("allow")
	}
	l.localMu.Lock()
	defer l.localMu.Unlock()
	return l.local.allow([]string{sessionKey, accountKey}, []int64{l.policy.SessionMaxWrites, l.policy.AccountMaxWrites}, time.Now(), l.policy.Window), nil
}

func (l *ProgressLimiter) LocalKeyCount() int {
	l.localMu.Lock()
	defer l.localMu.Unlock()
	return l.local.Len()
}
func (l *ProgressLimiter) DegradationCount() uint64 { return l.degradations.Load() }
func (l *ProgressLimiter) degraded(operation string) {
	l.degradations.Add(1)
	now := time.Now()
	l.warnMu.Lock()
	warn := !l.warnAfter.After(now)
	if warn {
		l.warnAfter = now.Add(time.Minute)
	}
	l.warnMu.Unlock()
	if warn {
		if l.logCategory != nil {
			l.logCategory(operation)
			return
		}
		log.Printf("level=warn event=progress_limiter_redis_unavailable metric=progress_limiter_redis_errors_total operation=%s", operation)
	}
}
