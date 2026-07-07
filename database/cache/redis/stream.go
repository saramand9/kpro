package redisstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Message is one Redis stream message.
type Message struct {
	ID     string
	Values map[string]any
}

// Stream provides stream operations for one Redis key.
type Stream struct {
	client *Client
	key    string
}

// NewStream creates a stream helper.
func NewStream(client *Client, key string) *Stream {
	return &Stream{client: client, key: key}
}

// Key returns the resolved Redis stream key.
func (s *Stream) Key() string {
	if s == nil {
		return ""
	}
	return s.redisKey()
}

// Add appends a message to the stream and returns the generated id.
func (s *Stream) Add(ctx context.Context, values map[string]any) (string, error) {
	redisClient := s.redis()
	if redisClient == nil {
		return "", sql.ErrConnDone
	}
	return redisClient.XAdd(contextOrBackground(ctx), &redis.XAddArgs{
		Stream: s.redisKey(),
		Values: values,
	}).Result()
}

// Read reads messages after id. Use "0" to read from the beginning or "$" for new messages.
func (s *Stream) Read(ctx context.Context, id string, count int64, block time.Duration) ([]Message, error) {
	redisClient := s.redis()
	if redisClient == nil {
		return nil, sql.ErrConnDone
	}
	if id == "" {
		id = "0"
	}
	streams, err := redisClient.XRead(contextOrBackground(ctx), &redis.XReadArgs{
		Streams: []string{s.redisKey(), id},
		Count:   count,
		Block:   block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return []Message{}, nil
	}
	if err != nil {
		return nil, err
	}
	return streamMessages(streams), nil
}

// CreateGroup creates a consumer group. Existing groups are treated as success.
func (s *Stream) CreateGroup(ctx context.Context, group string, start string) error {
	redisClient := s.redis()
	if redisClient == nil {
		return sql.ErrConnDone
	}
	if start == "" {
		start = "0"
	}
	err := redisClient.XGroupCreateMkStream(contextOrBackground(ctx), s.redisKey(), group, start).Err()
	if err != nil && strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}

// ReadGroup reads messages for a consumer group.
func (s *Stream) ReadGroup(ctx context.Context, group string, consumer string, id string, count int64, block time.Duration) ([]Message, error) {
	redisClient := s.redis()
	if redisClient == nil {
		return nil, sql.ErrConnDone
	}
	if id == "" {
		id = ">"
	}
	streams, err := redisClient.XReadGroup(contextOrBackground(ctx), &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{s.redisKey(), id},
		Count:    count,
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return []Message{}, nil
	}
	if err != nil {
		return nil, err
	}
	return streamMessages(streams), nil
}

// Ack acknowledges message ids for group.
func (s *Stream) Ack(ctx context.Context, group string, ids ...string) (int64, error) {
	redisClient := s.redis()
	if redisClient == nil {
		return 0, sql.ErrConnDone
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return redisClient.XAck(contextOrBackground(ctx), s.redisKey(), group, ids...).Result()
}

func (s *Stream) redis() *redis.Client {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Redis()
}

func (s *Stream) redisKey() string {
	if s == nil {
		return ""
	}
	if s.client == nil {
		return s.key
	}
	return s.client.key(s.key, "")
}

func streamMessages(streams []redis.XStream) []Message {
	messages := make([]Message, 0)
	for _, stream := range streams {
		for _, message := range stream.Messages {
			messages = append(messages, Message{ID: message.ID, Values: message.Values})
		}
	}
	return messages
}
