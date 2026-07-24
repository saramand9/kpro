package redisstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStreamAddReadGroupAndAck(t *testing.T) {
	ctx := context.Background()
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	stream := client.Stream("events")
	if stream.Key() != "test:events" {
		t.Fatalf("stream key=%q", stream.Key())
	}
	id, err := stream.Add(ctx, map[string]any{"type": "created", "id": "a1"})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := stream.Read(ctx, "0", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != id || messages[0].Values["type"] != "created" {
		t.Fatalf("messages=%#v", messages)
	}
	if err := stream.CreateGroup(ctx, "workers", "0"); err != nil {
		t.Fatal(err)
	}
	if err := stream.CreateGroup(ctx, "workers", "0"); err != nil {
		t.Fatalf("existing group should be ok: %v", err)
	}
	groupMessages, err := stream.ReadGroup(ctx, "workers", "c1", ">", 10, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(groupMessages) != 1 {
		t.Fatalf("group messages=%#v", groupMessages)
	}
	acked, err := stream.Ack(ctx, "workers", groupMessages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if acked != 1 {
		t.Fatalf("acked=%d, want 1", acked)
	}
}

func TestStreamRetentionAndExplicitTrim(t *testing.T) {
	ctx := context.Background()
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	stream := client.StreamWithOptions("bounded", StreamOptions{MaxLen: 2})
	for i := 0; i < 4; i++ {
		if _, err := stream.Add(ctx, map[string]any{"sequence": i}); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := stream.Read(ctx, "0", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("retained messages=%d, want 2", len(messages))
	}
	removed, err := stream.TrimMaxLen(ctx, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("trimmed=%d, want 1", removed)
	}

	invalid := client.StreamWithOptions("invalid", StreamOptions{MaxLen: 2, Limit: 1})
	if _, err := invalid.Add(ctx, map[string]any{"value": 1}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid options error=%v, want ErrInvalidConfig", err)
	}
	if _, err := stream.TrimMaxLen(ctx, -1, false); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("negative trim error=%v, want ErrInvalidConfig", err)
	}
}
