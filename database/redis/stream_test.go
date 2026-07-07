package redisstore

import (
	"context"
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
