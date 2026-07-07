package redisstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/saramand9/bignet/kpro/database/common/lifecycle"
)

// Client owns a Redis client and exposes capability helpers.
type Client struct {
	name      string
	redis     *redis.Client
	owns      bool
	keyPrefix string
}

var _ lifecycle.Resource = (*Client)(nil)

// Open creates a Redis adapter client and verifies it with PING.
func Open(ctx context.Context, config Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	redisClient := config.Client
	owns := false
	if redisClient == nil {
		redisClient = redis.NewClient(redisOptions(config))
		owns = true
	}
	client := &Client{
		name:      config.Name,
		redis:     redisClient,
		owns:      owns,
		keyPrefix: config.KeyPrefix,
	}
	pingCtx, cancel := context.WithTimeout(ctx, config.PingTimeout)
	defer cancel()
	if err := client.Ping(pingCtx); err != nil {
		if owns {
			_ = redisClient.Close()
		}
		return nil, err
	}
	return client, nil
}

// Redis returns the underlying go-redis client.
func (c *Client) Redis() *redis.Client {
	if c == nil {
		return nil
	}
	return c.redis
}

// Cache returns a typed JSON cache helper.
func Cache[T any](client *Client, prefix ...string) *CacheView[T] {
	return NewCache[T](client, prefix...)
}

// Cache returns a typed JSON cache helper.
func (c *Client) Cache(prefix ...string) *CacheView[any] {
	return NewCache[any](c, prefix...)
}

// Lock returns a Redis lock helper for key.
func (c *Client) Lock(key string, ttl time.Duration) *Lock {
	return NewLock(c, key, ttl)
}

// Stream returns a Redis stream helper for key.
func (c *Client) Stream(key string) *Stream {
	return NewStream(c, key)
}

// ZSet returns a Redis sorted-set helper for key.
func (c *Client) ZSet(key string) *ZSet {
	return NewZSet(c, key)
}

// Name implements lifecycle.Resource.
func (c *Client) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// Kind implements lifecycle.Resource.
func (c *Client) Kind() lifecycle.Kind {
	return lifecycle.KindRedis
}

// Ping implements lifecycle.Resource.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.redis == nil {
		return fmt.Errorf("%w: redis client is nil", lifecycle.ErrUnavailable)
	}
	return c.redis.Ping(ctx).Err()
}

// Health implements lifecycle.Resource.
func (c *Client) Health(ctx context.Context) lifecycle.Health {
	return lifecycle.CheckHealth(ctx, c)
}

// Close implements lifecycle.Resource.
func (c *Client) Close() error {
	return c.Shutdown(context.Background())
}

// Shutdown releases the Redis client when this adapter opened it.
func (c *Client) Shutdown(ctx context.Context) error {
	if c == nil || c.redis == nil || !c.owns {
		return nil
	}
	return c.redis.Close()
}

func (c *Client) key(key string, prefix string) string {
	if c == nil {
		return key
	}
	selectedPrefix := c.keyPrefix + strings.TrimSpace(prefix)
	if selectedPrefix == "" {
		return key
	}
	return selectedPrefix + key
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
