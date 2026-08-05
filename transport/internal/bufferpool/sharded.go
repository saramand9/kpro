package bufferpool

// ShardedPool partitions one hard retained-byte budget across independent
// pools. Callers must consistently select a shard for related Get/Put traffic;
// this removes cross-session contention without weakening the global bound.
type ShardedPool struct {
	shards []*Pool
}

// NewSharded constructs up to shardCount independent pools. The shard count is
// capped so every shard can retain at least one largest pooled buffer; tiny
// budgets therefore remain useful instead of being fragmented into disabled
// shards.
func NewSharded(config Config, shardCount int) *ShardedPool {
	if shardCount <= 0 {
		shardCount = 1
	}
	maxPooledSize := normalizedMaxPooledSize(config.MaxPooledSize)
	maxRetained := max(config.MaxRetainedBytes, 0)
	if maxRetained == 0 {
		shardCount = 1
	} else if maxUseful := maxRetained / int64(maxPooledSize); maxUseful < 1 {
		shardCount = 1
	} else if int64(shardCount) > maxUseful {
		shardCount = int(maxUseful)
	}

	pool := &ShardedPool{shards: make([]*Pool, shardCount)}
	baseBudget := maxRetained / int64(shardCount)
	remainder := maxRetained % int64(shardCount)
	for index := range pool.shards {
		budget := baseBudget
		if int64(index) < remainder {
			budget++
		}
		shardConfig := config
		shardConfig.MaxRetainedBytes = budget
		pool.shards[index] = New(shardConfig)
	}
	return pool
}

// Shard returns the stable pool selected by key.
func (p *ShardedPool) Shard(key uint64) *Pool {
	if p == nil || len(p.shards) == 0 {
		return nil
	}
	return p.shards[key%uint64(len(p.shards))]
}

// RetainedBytes reports bytes retained across all shards.
func (p *ShardedPool) RetainedBytes() int64 {
	if p == nil {
		return 0
	}
	var total int64
	for _, shard := range p.shards {
		total += shard.RetainedBytes()
	}
	return total
}

// ShardCount reports the effective number of independent pools.
func (p *ShardedPool) ShardCount() int {
	if p == nil {
		return 0
	}
	return len(p.shards)
}
