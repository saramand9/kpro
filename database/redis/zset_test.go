package redisstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestZSetLeaderboardOperations(t *testing.T) {
	ctx := context.Background()
	client, server := openTestRedis(t)
	defer server.Close()
	defer client.Close()

	board := client.ZSet("scores")
	if board.Key() != "test:scores" {
		t.Fatalf("zset key=%q", board.Key())
	}
	if err := board.Add(ctx, "ada", 10); err != nil {
		t.Fatal(err)
	}
	if err := board.Add(ctx, "linus", 20); err != nil {
		t.Fatal(err)
	}
	score, err := board.Increment(ctx, "ada", 15)
	if err != nil {
		t.Fatal(err)
	}
	if score != 25 {
		t.Fatalf("score=%v, want 25", score)
	}
	score, err = board.Score(ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if score != 25 {
		t.Fatalf("score=%v, want 25", score)
	}
	ordered, err := board.RevRange(ctx, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ordered, []string{"ada", "linus"}) {
		t.Fatalf("ordered=%#v", ordered)
	}
	scored, err := board.RevRangeWithScores(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(scored) != 1 || scored[0].Member != "ada" || scored[0].Score != 25 {
		t.Fatalf("scored=%#v", scored)
	}
	rank, err := board.RevRank(ctx, "linus")
	if err != nil {
		t.Fatal(err)
	}
	if rank == nil || *rank != 1 {
		t.Fatalf("rank=%v, want 1", rank)
	}
	removed, err := board.Remove(ctx, "linus")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d, want 1", removed)
	}
	if _, err := board.Score(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing score err=%v, want ErrNotFound", err)
	}
}
