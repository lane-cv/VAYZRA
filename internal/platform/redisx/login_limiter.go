package redisx

import (
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
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
	redisCooldown          = 2 * time.Second
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
	rdb    *redis.Client
	policy Policy

	localMu          sync.Mutex
	local            *localLRU
	now              func() time.Time
	breakerUntil     time.Time
	warnAfter        time.Time
	degradationCount atomic.Uint64
}

// NewLoginLimiter constructs a limiter. A random secret keeps accidental,
// non-production uses private; production wiring always supplies a persistent
// application-specific secret from configuration.
func NewLoginLimiter(rdb *redis.Client, policy Policy) *LoginLimiter {
	policy = normalizedPolicy(policy)
	if len(policy.Secret) == 0 {
		policy.Secret = make([]byte, 32)
		if _, err := rand.Read(policy.Secret); err != nil {
			panic("read login limiter secret")
		}
	}
	policy.Secret = append([]byte(nil), policy.Secret...)
	return &LoginLimiter{rdb: rdb, policy: policy, local: newLocalLRU(fallbackMaxKeys), now: time.Now}
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

// Allow fails over to bounded local state while preserving a recently observed
// account challenge requirement. The circuit prevents connection-attempt storms.
func (l *LoginLimiter) Allow(ctx context.Context, username, ip string) (Decision, error) {
	keys := l.keys(username, ip)
	if pending := l.localChallenge(keys.account); pending {
		if !l.redisAvailable() {
			return Decision{Allowed: true, ChallengeRequired: true}, nil
		}
	}
	if l.rdb == nil || !l.redisAvailable() {
		decision := l.allowFallback("f:" + keys.account + ":" + keys.ip)
		if l.localChallenge(keys.account) {
			decision.ChallengeRequired = true
		}
		return decision, nil
	}
	result, err := allowScript.Run(ctx, l.rdb, []string{keys.account, keys.ip, keys.accountLock, keys.ipLock}, challengeFailures).Int64Slice()
	if err != nil || len(result) != 3 {
		l.tripBreaker("allow")
		decision := l.allowFallback("f:" + keys.account + ":" + keys.ip)
		if l.localChallenge(keys.account) {
			decision.ChallengeRequired = true
		}
		return decision, nil
	}
	l.resetBreaker()
	if result[0] == 0 {
		return Decision{RetryAfter: time.Duration(result[1]) * time.Millisecond}, nil
	}
	if result[2] == 1 {
		l.setLocalChallenge(keys.account)
	}
	return Decision{Allowed: true, ChallengeRequired: result[2] == 1 || l.localChallenge(keys.account)}, nil
}

func (l *LoginLimiter) RecordFailure(ctx context.Context, username, ip string) error {
	keys := l.keys(username, ip)
	if l.rdb == nil || !l.redisAvailable() {
		return nil
	}
	counts, err := recordFailureScript.Run(ctx, l.rdb, []string{keys.account, keys.ip, keys.accountLock, keys.ipLock}, l.policy.Window.Milliseconds(), l.policy.Lockout.Milliseconds(), l.policy.AccountFailures, l.policy.IPFailures).Int64Slice()
	if err != nil {
		l.tripBreaker("record_failure")
		return nil
	}
	l.resetBreaker()
	if len(counts) >= 1 && counts[0] >= challengeFailures {
		l.setLocalChallenge(keys.account)
	}
	return nil
}

func (l *LoginLimiter) RecordSuccess(ctx context.Context, username, ip string) error {
	keys := l.keys(username, ip)
	l.clearLocalChallenge(keys.account)
	if l.rdb == nil || !l.redisAvailable() {
		return nil
	}
	if err := l.rdb.Del(ctx, keys.account).Err(); err != nil {
		l.tripBreaker("record_success")
	} else {
		l.resetBreaker()
	}
	return nil
}

func (l *LoginLimiter) breakerOpen() bool {
	l.localMu.Lock()
	defer l.localMu.Unlock()
	return l.breakerUntil.After(l.now())
}
func (l *LoginLimiter) redisAvailable() bool { return !l.breakerOpen() }
func (l *LoginLimiter) resetBreaker() {
	l.localMu.Lock()
	l.breakerUntil = time.Time{}
	l.localMu.Unlock()
}
func (l *LoginLimiter) tripBreaker(operation string) {
	now := l.now()
	l.degradationCount.Add(1)
	l.localMu.Lock()
	l.breakerUntil = now.Add(redisCooldown)
	warn := !l.warnAfter.After(now)
	if warn {
		l.warnAfter = now.Add(time.Minute)
	}
	l.localMu.Unlock()
	if warn {
		log.Printf("level=warn event=login_limiter_redis_unavailable metric=login_limiter_redis_errors_total operation=%s", operation)
	}
}
func (l *LoginLimiter) DegradationCount() uint64 { return l.degradationCount.Load() }

type localEntry struct {
	key     string
	expires time.Time
	element *list.Element
}
type localLRU struct {
	entries  map[string]*localEntry
	order    *list.List
	capacity int
}

func newLocalLRU(capacity int) *localLRU {
	return &localLRU{entries: make(map[string]*localEntry), order: list.New(), capacity: capacity}
}
func (lru *localLRU) Len() int { return len(lru.entries) }
func (lru *localLRU) get(key string, now time.Time) bool {
	e, ok := lru.entries[key]
	if !ok {
		return false
	}
	if !e.expires.After(now) {
		lru.order.Remove(e.element)
		delete(lru.entries, key)
		return false
	}
	lru.order.MoveToFront(e.element)
	return true
}
func (lru *localLRU) set(key string, expiry time.Time) {
	if e, ok := lru.entries[key]; ok {
		e.expires = expiry
		lru.order.MoveToFront(e.element)
		return
	}
	if len(lru.entries) >= lru.capacity {
		back := lru.order.Back()
		if back != nil {
			old := back.Value.(*localEntry)
			delete(lru.entries, old.key)
			lru.order.Remove(back)
		}
	}
	e := &localEntry{key: key, expires: expiry}
	e.element = lru.order.PushFront(e)
	lru.entries[key] = e
}
func (lru *localLRU) delete(key string) {
	if e, ok := lru.entries[key]; ok {
		lru.order.Remove(e.element)
		delete(lru.entries, key)
	}
}
func (l *LoginLimiter) allowFallback(key string) Decision {
	now := l.now()
	l.localMu.Lock()
	defer l.localMu.Unlock()
	if l.local.get(key, now) {
		return Decision{RetryAfter: fallbackWindow}
	}
	l.local.set(key, now.Add(fallbackWindow))
	return Decision{Allowed: true}
}
func (l *LoginLimiter) localChallenge(account string) bool {
	l.localMu.Lock()
	defer l.localMu.Unlock()
	return l.local.get("c:"+account, l.now())
}
func (l *LoginLimiter) setLocalChallenge(account string) {
	l.localMu.Lock()
	l.local.set("c:"+account, l.now().Add(l.policy.Window))
	l.localMu.Unlock()
}
func (l *LoginLimiter) clearLocalChallenge(account string) {
	l.localMu.Lock()
	l.local.delete("c:" + account)
	l.localMu.Unlock()
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
