package redisx

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultWindow          = 15 * time.Minute
	defaultLockout         = 15 * time.Minute
	defaultAccountFailures = 5
	defaultIPFailures      = 20
	challengeFailures      = 3
	fallbackWindow         = 10 * time.Second
	fallbackMaxKeys        = 4096
)

// Decision is the result of checking a login request before password work.
type Decision struct {
	Allowed           bool
	RetryAfter        time.Duration
	ChallengeRequired bool
}

// Policy determines the Redis rate-limit windows. Secret must be supplied by
// production configuration and is used only to pseudonymize Redis keys.
type Policy struct {
	Secret          []byte
	Window          time.Duration
	AccountFailures int
	IPFailures      int
	Lockout         time.Duration
}

// Limiter is consumed by the authentication HTTP boundary.
type Limiter interface {
	Allow(context.Context, string, string) (Decision, error)
	RecordFailure(context.Context, string, string) error
	RecordSuccess(context.Context, string, string) error
}

// LoginLimiter keeps the shared counters in Redis and only retains a bounded
// ten-second fallback state when Redis cannot be reached.
type LoginLimiter struct {
	rdb    redis.UniversalClient
	policy Policy

	fallbackMu sync.Mutex
	fallback   map[string]time.Time
}

// NewLoginLimiter constructs a limiter. A random secret keeps accidental,
// non-production uses private; production wiring always supplies a persistent
// application-specific secret from configuration.
func NewLoginLimiter(rdb redis.UniversalClient, policy Policy) *LoginLimiter {
	policy = normalizedPolicy(policy)
	if len(policy.Secret) == 0 {
		policy.Secret = make([]byte, 32)
		if _, err := rand.Read(policy.Secret); err != nil {
			panic("read login limiter secret")
		}
	}
	policy.Secret = append([]byte(nil), policy.Secret...)
	return &LoginLimiter{rdb: rdb, policy: policy, fallback: make(map[string]time.Time)}
}

func normalizedPolicy(policy Policy) Policy {
	if policy.Window <= 0 {
		policy.Window = defaultWindow
	}
	if policy.Lockout <= 0 {
		policy.Lockout = defaultLockout
	}
	if policy.AccountFailures <= 0 {
		policy.AccountFailures = defaultAccountFailures
	}
	if policy.IPFailures <= 0 {
		policy.IPFailures = defaultIPFailures
	}
	return policy
}

var recordFailureScript = redis.NewScript(`
local account = redis.call('INCR', KEYS[1])
if redis.call('PTTL', KEYS[1]) < 0 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local ip = redis.call('INCR', KEYS[2])
if redis.call('PTTL', KEYS[2]) < 0 then redis.call('PEXPIRE', KEYS[2], ARGV[1]) end
if account >= tonumber(ARGV[3]) then redis.call('PSETEX', KEYS[3], ARGV[2], '1') end
if ip >= tonumber(ARGV[4]) then redis.call('PSETEX', KEYS[4], ARGV[2], '1') end
return {account, ip}
`)

var allowScript = redis.NewScript(`
local accountLock = redis.call('PTTL', KEYS[3])
local ipLock = redis.call('PTTL', KEYS[4])
local retry = accountLock
if ipLock > retry then retry = ipLock end
if retry > 0 then return {0, retry, 0} end
local account = tonumber(redis.call('GET', KEYS[1])) or 0
if account >= tonumber(ARGV[1]) then return {1, 0, 1} end
return {1, 0, 0}
`)

// Allow returns a safe local fallback decision rather than making Redis an
// availability dependency. Redis errors are observed internally and are not
// returned to HTTP clients.
func (l *LoginLimiter) Allow(ctx context.Context, username, ip string) (Decision, error) {
	keys := l.keys(username, ip)
	if l.rdb == nil {
		return l.allowFallback(keys.account + ":" + keys.ip), nil
	}
	result, err := allowScript.Run(ctx, l.rdb, []string{keys.account, keys.ip, keys.accountLock, keys.ipLock}, challengeFailures).Int64Slice()
	if err != nil || len(result) != 3 {
		l.redisUnavailable("allow")
		return l.allowFallback(keys.account + ":" + keys.ip), nil
	}
	if result[0] == 0 {
		return Decision{RetryAfter: time.Duration(result[1]) * time.Millisecond}, nil
	}
	return Decision{Allowed: true, ChallengeRequired: result[2] == 1}, nil
}

// RecordFailure increments account and IP counters together in one Redis
// script. A Redis outage is intentionally non-fatal because Allow's bounded
// local fallback protects the next request.
func (l *LoginLimiter) RecordFailure(ctx context.Context, username, ip string) error {
	if l.rdb == nil {
		l.redisUnavailable("record_failure")
		return nil
	}
	keys := l.keys(username, ip)
	_, err := recordFailureScript.Run(ctx, l.rdb, []string{keys.account, keys.ip, keys.accountLock, keys.ipLock}, l.policy.Window.Milliseconds(), l.policy.Lockout.Milliseconds(), l.policy.AccountFailures, l.policy.IPFailures).Result()
	if err != nil {
		l.redisUnavailable("record_failure")
		return nil
	}
	return nil
}

// RecordSuccess clears only account-scoped failures. Shared IP state remains
// intact so a successful account cannot reset an attacker's IP reputation.
func (l *LoginLimiter) RecordSuccess(ctx context.Context, username, ip string) error {
	if l.rdb == nil {
		l.redisUnavailable("record_success")
		return nil
	}
	if err := l.rdb.Del(ctx, l.keys(username, ip).account).Err(); err != nil {
		l.redisUnavailable("record_success")
	}
	return nil
}

type limiterKeys struct {
	account, ip, accountLock, ipLock string
}

func (l *LoginLimiter) keys(username, ip string) limiterKeys {
	account := l.keyHash("account", normalizeUsername(username))
	ipKey := l.keyHash("ip", canonicalIP(ip))
	return limiterKeys{
		account:     "hl:login:account:" + account,
		ip:          "hl:login:ip:" + ipKey,
		accountLock: "hl:login:account-lock:" + account,
		ipLock:      "hl:login:ip-lock:" + ipKey,
	}
}

func (l *LoginLimiter) keyHash(kind, value string) string {
	h := hmac.New(sha256.New, l.policy.Secret)
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeUsername(username string) string { return strings.ToLower(strings.TrimSpace(username)) }

func canonicalIP(value string) string {
	address, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return "invalid"
	}
	return address.Unmap().String()
}

func (l *LoginLimiter) allowFallback(key string) Decision {
	now := time.Now()
	l.fallbackMu.Lock()
	defer l.fallbackMu.Unlock()
	for candidate, expiresAt := range l.fallback {
		if !expiresAt.After(now) {
			delete(l.fallback, candidate)
		}
	}
	if expiresAt, ok := l.fallback[key]; ok {
		return Decision{RetryAfter: time.Until(expiresAt)}
	}
	if len(l.fallback) >= fallbackMaxKeys {
		var oldest string
		var oldestExpiry time.Time
		for candidate, expiresAt := range l.fallback {
			if oldest == "" || expiresAt.Before(oldestExpiry) {
				oldest, oldestExpiry = candidate, expiresAt
			}
		}
		delete(l.fallback, oldest)
	}
	l.fallback[key] = now.Add(fallbackWindow)
	return Decision{Allowed: true}
}

func (l *LoginLimiter) redisUnavailable(operation string) {
	// Deliberately structured and credential-free: usernames, IPs, answers,
	// Redis URLs, and request bodies must never reach logs or metrics.
	log.Printf("level=warn event=login_limiter_redis_unavailable metric=login_limiter_redis_errors_total operation=%s", operation)
}
