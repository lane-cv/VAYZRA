package redisx

import (
	"container/list"
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

const resourceFallbackMaxKeys = 4096

type ResourceDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type SearchRateLimiter interface {
	AllowSearch(context.Context, uuid.UUID) (ResourceDecision, error)
}

type ResourceLimitPolicy struct {
	Secret       []byte
	Window       time.Duration
	MaxRequests  int64
	LocalMaxKeys int
}

type SearchLimiter struct {
	rdb          *redis.Client
	policy       ResourceLimitPolicy
	localMu      sync.Mutex
	local        *boundedCounterLRU
	degradations atomic.Uint64
	warnMu       sync.Mutex
	warnAfter    time.Time
}

func NewSearchLimiter(rdb *redis.Client, policy ResourceLimitPolicy) *SearchLimiter {
	if policy.Window <= 0 {
		policy.Window = time.Minute
	}
	if policy.MaxRequests <= 0 {
		policy.MaxRequests = 30
	}
	if policy.LocalMaxKeys <= 0 || policy.LocalMaxKeys > resourceFallbackMaxKeys {
		policy.LocalMaxKeys = resourceFallbackMaxKeys
	}
	policy.Secret = normalizedResourceSecret(policy.Secret)
	return &SearchLimiter{rdb: rdb, policy: policy, local: newBoundedCounterLRU(policy.LocalMaxKeys)}
}

var resourceCounterScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local ttl = redis.call('PTTL', KEYS[1])
if count > tonumber(ARGV[2]) then return {0, ttl} end
return {1, ttl}
`)

func (l *SearchLimiter) AllowSearch(ctx context.Context, accountID uuid.UUID) (ResourceDecision, error) {
	if accountID == uuid.Nil {
		return ResourceDecision{}, nil
	}
	key := resourceKey(l.policy.Secret, "search-account", accountID)
	if l.rdb != nil {
		result, err := resourceCounterScript.Run(ctx, l.rdb, []string{key}, l.policy.Window.Milliseconds(), l.policy.MaxRequests).Int64Slice()
		if err == nil && len(result) == 2 && result[1] >= 0 {
			return resourceResult(result), nil
		}
		l.degraded("search")
	}
	return l.allowLocal(key), nil
}

func (l *SearchLimiter) allowLocal(key string) ResourceDecision {
	l.localMu.Lock()
	defer l.localMu.Unlock()
	return l.local.allow([]string{key}, []int64{l.policy.MaxRequests}, time.Now(), l.policy.Window)
}
func (l *SearchLimiter) LocalKeyCount() int {
	l.localMu.Lock()
	defer l.localMu.Unlock()
	return l.local.Len()
}
func (l *SearchLimiter) DegradationCount() uint64 { return l.degradations.Load() }
func (l *SearchLimiter) degraded(operation string) {
	l.degradations.Add(1)
	now := time.Now()
	l.warnMu.Lock()
	warn := !l.warnAfter.After(now)
	if warn {
		l.warnAfter = now.Add(time.Minute)
	}
	l.warnMu.Unlock()
	if warn {
		log.Printf("level=warn event=resource_limiter_redis_unavailable operation=%s", operation)
	}
}

func resourceResult(result []int64) ResourceDecision {
	if result[0] == 0 {
		return ResourceDecision{RetryAfter: time.Duration(result[1]) * time.Millisecond}
	}
	return ResourceDecision{Allowed: true}
}

func normalizedResourceSecret(secret []byte) []byte {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			panic("read resource limiter secret")
		}
	}
	return append([]byte(nil), secret...)
}

func resourceKey(secret []byte, kind string, id uuid.UUID) string {
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(id.String()))
	return "hl:" + kind + ":" + hex.EncodeToString(h.Sum(nil))
}

type boundedCounterEntry struct {
	key     string
	count   int64
	expires time.Time
	element *list.Element
}
type boundedCounterLRU struct {
	entries  map[string]*boundedCounterEntry
	order    *list.List
	capacity int
}

func newBoundedCounterLRU(capacity int) *boundedCounterLRU {
	return &boundedCounterLRU{entries: make(map[string]*boundedCounterEntry), order: list.New(), capacity: capacity}
}
func (l *boundedCounterLRU) Len() int { return len(l.entries) }
func (l *boundedCounterLRU) increment(key string, now time.Time, window time.Duration) (int64, time.Time) {
	if entry, ok := l.entries[key]; ok {
		if entry.expires.After(now) {
			entry.count++
			l.order.MoveToFront(entry.element)
			return entry.count, entry.expires
		}
		l.order.Remove(entry.element)
		delete(l.entries, key)
	}
	if len(l.entries) >= l.capacity {
		if back := l.order.Back(); back != nil {
			old := back.Value.(*boundedCounterEntry)
			delete(l.entries, old.key)
			l.order.Remove(back)
		}
	}
	entry := &boundedCounterEntry{key: key, count: 1, expires: now.Add(window)}
	entry.element = l.order.PushFront(entry)
	l.entries[key] = entry
	return entry.count, entry.expires
}
func (l *boundedCounterLRU) allow(keys []string, limits []int64, now time.Time, window time.Duration) ResourceDecision {
	allowed := true
	var retry time.Duration
	for i, key := range keys {
		count, expiry := l.increment(key, now, window)
		if count > limits[i] {
			allowed = false
			if remaining := expiry.Sub(now); remaining > retry {
				retry = remaining
			}
		}
	}
	if !allowed {
		return ResourceDecision{RetryAfter: retry}
	}
	return ResourceDecision{Allowed: true}
}
