package redisstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/redis/go-redis/v9"
)

// ScoredMember is a sorted-set member with score.
type ScoredMember struct {
	Member string
	Score  float64
}

// ZSet provides sorted-set operations for one Redis key.
type ZSet struct {
	client *Client
	key    string
}

// NewZSet creates a sorted-set helper.
func NewZSet(client *Client, key string) *ZSet {
	return &ZSet{client: client, key: key}
}

// Key returns the resolved Redis sorted-set key.
func (z *ZSet) Key() string {
	if z == nil {
		return ""
	}
	return z.redisKey()
}

// Add adds or updates a member score.
func (z *ZSet) Add(ctx context.Context, member string, score float64) error {
	redisClient := z.redis()
	if redisClient == nil {
		return sql.ErrConnDone
	}
	return redisClient.ZAdd(contextOrBackground(ctx), z.redisKey(), redis.Z{Member: member, Score: score}).Err()
}

// Increment increments a member score and returns the new score.
func (z *ZSet) Increment(ctx context.Context, member string, delta float64) (float64, error) {
	redisClient := z.redis()
	if redisClient == nil {
		return 0, sql.ErrConnDone
	}
	return redisClient.ZIncrBy(contextOrBackground(ctx), z.redisKey(), delta, member).Result()
}

// Score returns a member score or ErrNotFound when missing.
func (z *ZSet) Score(ctx context.Context, member string) (float64, error) {
	redisClient := z.redis()
	if redisClient == nil {
		return 0, sql.ErrConnDone
	}
	score, err := redisClient.ZScore(contextOrBackground(ctx), z.redisKey(), member).Result()
	if errors.Is(err, redis.Nil) {
		return 0, ErrNotFound
	}
	return score, err
}

// Range returns members ordered by low score to high score.
func (z *ZSet) Range(ctx context.Context, start int64, stop int64) ([]string, error) {
	redisClient := z.redis()
	if redisClient == nil {
		return nil, sql.ErrConnDone
	}
	return redisClient.ZRange(contextOrBackground(ctx), z.redisKey(), start, stop).Result()
}

// RevRange returns members ordered by high score to low score.
func (z *ZSet) RevRange(ctx context.Context, start int64, stop int64) ([]string, error) {
	redisClient := z.redis()
	if redisClient == nil {
		return nil, sql.ErrConnDone
	}
	return redisClient.ZRevRange(contextOrBackground(ctx), z.redisKey(), start, stop).Result()
}

// RevRangeWithScores returns scored members ordered by high score to low score.
func (z *ZSet) RevRangeWithScores(ctx context.Context, start int64, stop int64) ([]ScoredMember, error) {
	redisClient := z.redis()
	if redisClient == nil {
		return nil, sql.ErrConnDone
	}
	values, err := redisClient.ZRevRangeWithScores(contextOrBackground(ctx), z.redisKey(), start, stop).Result()
	if err != nil {
		return nil, err
	}
	members := make([]ScoredMember, 0, len(values))
	for _, value := range values {
		member, _ := value.Member.(string)
		members = append(members, ScoredMember{Member: member, Score: value.Score})
	}
	return members, nil
}

// RevRank returns a member rank ordered by high score to low score, or nil when missing.
func (z *ZSet) RevRank(ctx context.Context, member string) (*int64, error) {
	redisClient := z.redis()
	if redisClient == nil {
		return nil, sql.ErrConnDone
	}
	rank, err := redisClient.ZRevRank(contextOrBackground(ctx), z.redisKey(), member).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rank, nil
}

// Remove removes members and returns the number of removed members.
func (z *ZSet) Remove(ctx context.Context, members ...string) (int64, error) {
	redisClient := z.redis()
	if redisClient == nil {
		return 0, sql.ErrConnDone
	}
	if len(members) == 0 {
		return 0, nil
	}
	values := make([]any, 0, len(members))
	for _, member := range members {
		values = append(values, member)
	}
	return redisClient.ZRem(contextOrBackground(ctx), z.redisKey(), values...).Result()
}

func (z *ZSet) redis() *redis.Client {
	if z == nil || z.client == nil {
		return nil
	}
	return z.client.Redis()
}

func (z *ZSet) redisKey() string {
	if z == nil {
		return ""
	}
	if z.client == nil {
		return z.key
	}
	return z.client.key(z.key, "")
}
