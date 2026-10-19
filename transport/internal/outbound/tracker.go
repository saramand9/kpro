package outbound

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	transportapi "github.com/saramand9/kpro/transport"
)

type writeOutcome uint8

const (
	writeSucceeded writeOutcome = iota
	writeFailed
	writeDropped
	trackerShardCount = 64
	trackerCachePad   = 64
)

// Tracker aggregates queue state and process-lifetime results for one
// transport server. Queue hot-path counters are sharded once per queue so
// unrelated sessions do not contend on one cache line. Snapshot collection
// performs the cold-path aggregation. The zero value is ready for use.
type Tracker struct {
	queueCursor atomic.Uint64
	queueShards [trackerShardCount]queueTracker

	drainSucceeded atomic.Uint64
	drainDeadline  atomic.Uint64
	drainCanceled  atomic.Uint64
	drainFailed    atomic.Uint64

	drainDurationTotal atomic.Int64
	lastDrainDuration  atomic.Int64
	maxDrainDuration   atomic.Int64
}

// Padding leaves at least one full cache line between the active fields of
// adjacent shards regardless of the containing struct's base alignment.
type queueTracker struct {
	_ [trackerCachePad]byte

	sessions          atomic.Int64
	pendingWrites     atomic.Int64
	pendingBytes      atomic.Int64
	waitingWriters    atomic.Int64
	saturatedSessions atomic.Int64

	acceptedWrites atomic.Uint64
	acceptedBytes  atomic.Uint64

	succeededWrites atomic.Uint64
	succeededBytes  atomic.Uint64
	failedWrites    atomic.Uint64
	failedBytes     atomic.Uint64
	droppedWrites   atomic.Uint64
	droppedBytes    atomic.Uint64

	_ [trackerCachePad]byte
}

func (t *Tracker) openQueue() *queueTracker {
	if t == nil {
		return nil
	}
	index := (t.queueCursor.Add(1) - 1) & (trackerShardCount - 1)
	shard := &t.queueShards[index]
	shard.sessions.Add(1)
	return shard
}

func (t *queueTracker) closeQueue() {
	if t != nil {
		t.sessions.Add(-1)
	}
}

func (t *queueTracker) addPending(writes int64, bytes int64) {
	if t == nil {
		return
	}
	if writes != 0 {
		t.pendingWrites.Add(writes)
	}
	if bytes != 0 {
		t.pendingBytes.Add(bytes)
	}
}

func (t *queueTracker) addWaiting(delta int64) {
	if t != nil {
		t.waitingWriters.Add(delta)
	}
}

func (t *queueTracker) addSaturated(delta int64) {
	if t != nil {
		t.saturatedSessions.Add(delta)
	}
}

func (t *queueTracker) recordAccepted(bytes int64) {
	if t == nil || bytes < 0 {
		return
	}
	t.acceptedWrites.Add(1)
	t.acceptedBytes.Add(uint64(bytes))
}

func (t *queueTracker) recordWrite(outcome writeOutcome, bytes int64) {
	if t == nil || bytes < 0 {
		return
	}
	size := uint64(bytes)
	switch outcome {
	case writeSucceeded:
		t.succeededWrites.Add(1)
		t.succeededBytes.Add(size)
	case writeFailed:
		t.failedWrites.Add(1)
		t.failedBytes.Add(size)
	case writeDropped:
		t.droppedWrites.Add(1)
		t.droppedBytes.Add(size)
	}
}

// RecordDrain records one completed transport-level drain attempt.
func (t *Tracker) RecordDrain(duration time.Duration, err error) {
	if t == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	nanos := int64(duration)
	t.drainDurationTotal.Add(nanos)
	for {
		maximum := t.maxDrainDuration.Load()
		if nanos <= maximum || t.maxDrainDuration.CompareAndSwap(maximum, nanos) {
			break
		}
	}
	t.lastDrainDuration.Store(nanos)
	switch {
	case err == nil:
		t.drainSucceeded.Add(1)
	case errors.Is(err, context.DeadlineExceeded):
		t.drainDeadline.Add(1)
	case errors.Is(err, context.Canceled):
		t.drainCanceled.Add(1)
	default:
		t.drainFailed.Add(1)
	}
}

// Snapshot returns the bounded public transport view.
func (t *Tracker) Snapshot() transportapi.OutboundSnapshot {
	if t == nil {
		return transportapi.OutboundSnapshot{}
	}
	snapshot := transportapi.OutboundSnapshot{}
	for index := range t.queueShards {
		shard := &t.queueShards[index]
		snapshot.Sessions += shard.sessions.Load()
		snapshot.PendingWrites += shard.pendingWrites.Load()
		snapshot.PendingBytes += shard.pendingBytes.Load()
		snapshot.WaitingWriters += shard.waitingWriters.Load()
		snapshot.SaturatedSessions += shard.saturatedSessions.Load()
		snapshot.AcceptedWrites += shard.acceptedWrites.Load()
		snapshot.AcceptedBytes += shard.acceptedBytes.Load()
		snapshot.SucceededWrites += shard.succeededWrites.Load()
		snapshot.SucceededBytes += shard.succeededBytes.Load()
		snapshot.FailedWrites += shard.failedWrites.Load()
		snapshot.FailedBytes += shard.failedBytes.Load()
		snapshot.DroppedWrites += shard.droppedWrites.Load()
		snapshot.DroppedBytes += shard.droppedBytes.Load()
	}
	snapshot.DrainSucceeded = t.drainSucceeded.Load()
	snapshot.DrainDeadline = t.drainDeadline.Load()
	snapshot.DrainCanceled = t.drainCanceled.Load()
	snapshot.DrainFailed = t.drainFailed.Load()
	snapshot.DrainDurationTotal = time.Duration(t.drainDurationTotal.Load())
	snapshot.LastDrainDuration = time.Duration(t.lastDrainDuration.Load())
	snapshot.MaxDrainDuration = time.Duration(t.maxDrainDuration.Load())
	return snapshot
}
