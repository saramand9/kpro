package redisstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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

// Lock represents a Redis lock identified by key and token.
type Lock struct {
	client *Client
	key    string
	token  string
	ttl    time.Duration
}

// NewLock creates a Redis lock helper.
func NewLock(client *Client, key string, ttl time.Duration) *Lock {
	return &Lock{client: client, key: key, token: randomToken(), ttl: ttl}
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
	return l.token
}

// Acquire attempts to acquire the lock.
func (l *Lock) Acquire(ctx context.Context) (bool, error) {
	redisClient := l.redis()
	if redisClient == nil {
		return false, sql.ErrConnDone
	}
	if l.ttl <= 0 {
		return false, ErrInvalidConfig
	}
	ok, err := redisClient.SetNX(contextOrBackground(ctx), l.key, l.token, l.ttl).Result()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrLockNotAcquired
	}
	return true, nil
}

// Release releases the lock if this token still owns it.
func (l *Lock) Release(ctx context.Context) (bool, error) {
	redisClient := l.redis()
	if redisClient == nil {
		return false, sql.ErrConnDone
	}
	result, err := redisClient.Eval(contextOrBackground(ctx), releaseLockScript, []string{l.key}, l.token).Int()
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
	result, err := redisClient.Eval(contextOrBackground(ctx), refreshLockScript, []string{l.key}, l.token, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	if result == 0 {
		return false, ErrLockTokenMismatch
	}
	l.ttl = ttl
	return true, nil
}

func (l *Lock) redis() *redis.Client {
	if l == nil || l.client == nil {
		return nil
	}
	return l.client.Redis()
}

func randomToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}
