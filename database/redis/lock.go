package redisstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const releaseLockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`

const refreshLockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0`

// Lock represents a single-acquisition Redis lease identified by key and token.
//
// The token protects Release and Refresh from changing a lease now owned by a
// different Lock. It is not a fencing token: a holder paused past the TTL can
// still resume while a newer holder is active. Use this helper only when that
// overlap is safe, or add an application-level monotonic fence at the protected
// resource.
type Lock struct {
	client *Client
	key    string

	mu       sync.RWMutex
	token    string
	ttl      time.Duration
	acquired bool
}

// NewLock creates a single-acquisition Redis lock helper.
//
// A Lock may retry after an unsuccessful Acquire, but it cannot acquire again
// after its first successful acquisition. Create a new Lock for each lease.
func NewLock(client *Client, key string, ttl time.Duration) *Lock {
	if client != nil {
		key = client.key(key, "")
	}
	return &Lock{client: client, key: key, ttl: ttl}
}

// Key returns the Redis lock key.
func (l *Lock) Key() string {
	if l == nil {
		return ""
	}
	return l.key
}

// Token returns the lock ownership token.
func (l *Lock) Token() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.token
}

// Acquire attempts to acquire the lock. After the first successful call, this
// Lock cannot acquire another lease, even after Release or expiry; construct a
// new Lock for the next acquisition.
//
// A transport error can leave the SET NX outcome unknown: Redis might hold the
// generated token even though the caller did not receive the reply. Callers
// must not treat the error as proof that no lease exists or start
// non-idempotent work through another owner; reconcile or wait for the
// configured TTL.
func (l *Lock) Acquire(ctx context.Context) (bool, error) {
	redisClient := l.redis()
	if redisClient == nil {
		return false, sql.ErrConnDone
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.acquired {
		return false, ErrLockAlreadyAcquired
	}
	if l.ttl <= 0 {
		return false, ErrInvalidConfig
	}

	token, err := randomToken()
	if err != nil {
		return false, err
	}
	ok, err := redisClient.SetNX(contextOrBackground(ctx), l.key, token, l.ttl).Result()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrLockNotAcquired
	}
	l.token = token
	l.acquired = true
	return true, nil
}

// Release releases the lock if this token still owns it.
func (l *Lock) Release(ctx context.Context) (bool, error) {
	redisClient := l.redis()
	if redisClient == nil {
		return false, sql.ErrConnDone
	}
	token, acquired := l.acquisition()
	if !acquired {
		return false, ErrLockTokenMismatch
	}
	result, err := redisClient.Eval(contextOrBackground(ctx), releaseLockScript, []string{l.key}, token).Int()
	if err != nil {
		return false, err
	}
	if result == 0 {
		return false, ErrLockTokenMismatch
	}
	return true, nil
}

// Refresh extends the lock TTL if this token still owns it.
func (l *Lock) Refresh(ctx context.Context, ttl time.Duration) (bool, error) {
	redisClient := l.redis()
	if redisClient == nil {
		return false, sql.ErrConnDone
	}
	if ttl <= 0 {
		return false, ErrInvalidConfig
	}
	token, acquired := l.acquisition()
	if !acquired {
		return false, ErrLockTokenMismatch
	}
	result, err := redisClient.Eval(contextOrBackground(ctx), refreshLockScript, []string{l.key}, token, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	if result == 0 {
		return false, ErrLockTokenMismatch
	}
	l.mu.Lock()
	l.ttl = ttl
	l.mu.Unlock()
	return true, nil
}

func (l *Lock) acquisition() (string, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.token, l.acquired
}

func (l *Lock) redis() *redis.Client {
	if l == nil || l.client == nil {
		return nil
	}
	return l.client.Redis()
}

func randomToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
