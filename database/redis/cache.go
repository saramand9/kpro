package redisstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/saramand9/bignet/kpro/database/views"
)

// CacheView provides typed JSON cache operations backed by Redis.
type CacheView[T any] struct {
	client *Client
	prefix string
}

var _ views.Cache[any] = (*CacheView[any])(nil)

// NewCache creates a typed cache helper.
func NewCache[T any](client *Client, prefix ...string) *CacheView[T] {
	cache := &CacheView[T]{client: client}
	if len(prefix) > 0 {
		cache.prefix = prefix[0]
	}
	return cache
}

// Get implements views.Cache.
func (c *CacheView[T]) Get(ctx context.Context, key string) (T, bool, error) {
	var value T
	redisClient := c.redis()
	if redisClient == nil {
		return value, false, sql.ErrConnDone
	}
	data, err := redisClient.Get(contextOrBackground(ctx), c.key(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, false, err
	}
	return value, true, nil
}

// Set implements views.Cache.
func (c *CacheView[T]) Set(ctx context.Context, key string, value T, ttl time.Duration) error {
	redisClient := c.redis()
	if redisClient == nil {
		return sql.ErrConnDone
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return redisClient.Set(contextOrBackground(ctx), c.key(key), data, ttl).Err()
}

// Delete implements views.Cache.
func (c *CacheView[T]) Delete(ctx context.Context, key string) error {
	redisClient := c.redis()
	if redisClient == nil {
		return sql.ErrConnDone
	}
	return redisClient.Del(contextOrBackground(ctx), c.key(key)).Err()
}

// SetNX stores value only when key does not already exist.
func (c *CacheView[T]) SetNX(ctx context.Context, key string, value T, ttl time.Duration) (bool, error) {
	redisClient := c.redis()
	if redisClient == nil {
		return false, sql.ErrConnDone
	}
	data, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	return redisClient.SetNX(contextOrBackground(ctx), c.key(key), data, ttl).Result()
}

// Exists reports whether key exists.
func (c *CacheView[T]) Exists(ctx context.Context, key string) (bool, error) {
	redisClient := c.redis()
	if redisClient == nil {
		return false, sql.ErrConnDone
	}
	count, err := redisClient.Exists(contextOrBackground(ctx), c.key(key)).Result()
	return count > 0, err
}

func (c *CacheView[T]) key(key string) string {
	if c == nil || c.client == nil {
		return key
	}
	return c.client.key(key, c.prefix)
}

func (c *CacheView[T]) redis() *redis.Client {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Redis()
}
