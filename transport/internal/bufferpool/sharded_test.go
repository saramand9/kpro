package bufferpool

import (
	"sync"
	"testing"
)

func TestShardedPoolUsesStableIndependentShards(t *testing.T) {
	pool := NewSharded(Config{MaxRetainedBytes: 1024, MaxPooledSize: 128}, 8)
	if got := pool.ShardCount(); got != 8 {
		t.Fatalf("ShardCount() = %d, want 8", got)
	}
	if pool.Shard(3) != pool.Shard(11) {
		t.Fatal("equivalent keys did not select the same shard")
	}
	if pool.Shard(3) == pool.Shard(4) {
		t.Fatal("distinct adjacent keys selected the same shard")
	}
}

func TestShardedPoolCapsShardCountForSmallBudget(t *testing.T) {
	pool := NewSharded(Config{MaxRetainedBytes: 1024, MaxPooledSize: 1024}, 64)
	if got := pool.ShardCount(); got != 1 {
		t.Fatalf("ShardCount() = %d, want 1", got)
	}
	buffer := pool.Shard(99).GetUninitialized(32)
	pool.Shard(99).Put(buffer)
	if got := pool.RetainedBytes(); got != 32 {
		t.Fatalf("RetainedBytes() = %d, want 32", got)
	}
}

func TestShardedPoolNeverExceedsGlobalRetainedBudget(t *testing.T) {
	const budget = 4096
	pool := NewSharded(Config{MaxRetainedBytes: budget, MaxPooledSize: 256}, 16)
	var wait sync.WaitGroup
	for shard := uint64(0); shard < uint64(pool.ShardCount()); shard++ {
		shard := shard
		wait.Add(1)
		go func() {
			defer wait.Done()
			selected := pool.Shard(shard)
			for range 128 {
				selected.Put(selected.GetUninitialized(256))
			}
		}()
	}
	wait.Wait()
	if got := pool.RetainedBytes(); got > budget {
		t.Fatalf("RetainedBytes() = %d, exceeds %d", got, budget)
	}
}
