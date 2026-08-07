package outbound

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTrackedQueueReportsCurrentAndTerminalResults(t *testing.T) {
	var tracker Tracker
	queue := NewTracked[int](Config{
		MaxFrames:          2,
		MaxBytes:           10,
		LowWatermarkBytes:  3,
		HighWatermarkBytes: 6,
	}, &tracker)
	if got := tracker.Snapshot(); got.Sessions != 1 || got.PendingWrites != 0 {
		t.Fatalf("initial tracker snapshot = %+v", got)
	}

	reservation, err := queue.Reserve(6)
	if err != nil {
		t.Fatal(err)
	}
	if got := tracker.Snapshot(); got.PendingWrites != 1 || got.PendingBytes != 6 || got.SaturatedSessions != 1 || got.AcceptedWrites != 0 {
		t.Fatalf("reserved tracker snapshot = %+v", got)
	}
	if err := reservation.Commit(7, 5); err != nil {
		t.Fatal(err)
	}
	if got := tracker.Snapshot(); got.PendingWrites != 1 || got.PendingBytes != 5 || got.SaturatedSessions != 1 || got.AcceptedWrites != 1 || got.AcceptedBytes != 5 {
		t.Fatalf("accepted tracker snapshot = %+v", got)
	}
	item := <-queue.Items()
	item.Done()
	item.Complete(errors.New("duplicate completion must be ignored"))
	if got := tracker.Snapshot(); got.PendingWrites != 0 || got.PendingBytes != 0 || got.SaturatedSessions != 0 || got.SucceededWrites != 1 || got.SucceededBytes != 5 || got.FailedWrites != 0 {
		t.Fatalf("successful tracker snapshot = %+v", got)
	}
	queue.Close()
	if got := tracker.Snapshot(); got.Sessions != 0 {
		t.Fatalf("closed tracker snapshot = %+v", got)
	}
}

func TestTrackedQueueDistinguishesWriteFailureAndQueuedDrop(t *testing.T) {
	var tracker Tracker
	queue := NewTracked[int](Config{MaxFrames: 3, MaxBytes: 30}, &tracker)
	for value := 1; value <= 3; value++ {
		reservation, err := queue.Reserve(5)
		if err != nil {
			t.Fatal(err)
		}
		if err := reservation.Commit(value, 5); err != nil {
			t.Fatal(err)
		}
	}

	writerOwned := <-queue.Items()
	wantErr := errors.New("injected writer failure")
	writerOwned.Complete(wantErr)
	queue.CloseWithError(ErrClosed)
	got := tracker.Snapshot()
	if got.Sessions != 0 || got.PendingWrites != 0 || got.PendingBytes != 0 {
		t.Fatalf("terminal current snapshot = %+v", got)
	}
	if got.AcceptedWrites != 3 || got.AcceptedBytes != 15 {
		t.Fatalf("accepted totals = %+v", got)
	}
	if got.FailedWrites != 1 || got.FailedBytes != 5 || got.DroppedWrites != 2 || got.DroppedBytes != 10 || got.SucceededWrites != 0 {
		t.Fatalf("terminal result totals = %+v", got)
	}
}

func TestTrackedQueueReportsBoundedWritableWaiters(t *testing.T) {
	var tracker Tracker
	queue := NewTracked[int](Config{MaxFrames: 1, MaxBytes: 1}, &tracker)
	reservation, err := queue.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Commit(1, 1); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() { result <- queue.WaitWritable(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for tracker.Snapshot().WaitingWriters != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := tracker.Snapshot().WaitingWriters; got != 1 {
		t.Fatalf("waiting writers = %d, want 1", got)
	}
	item := <-queue.Items()
	item.Done()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("writable waiter did not wake")
	}
	if got := tracker.Snapshot().WaitingWriters; got != 0 {
		t.Fatalf("waiting writers after wake = %d", got)
	}
	queue.Close()
}

func TestTrackerRecordsBoundedDrainOutcomesAndDurations(t *testing.T) {
	var tracker Tracker
	wantErr := errors.New("drain failed")
	tracker.RecordDrain(5*time.Millisecond, nil)
	tracker.RecordDrain(7*time.Millisecond, context.DeadlineExceeded)
	tracker.RecordDrain(3*time.Millisecond, context.Canceled)
	tracker.RecordDrain(11*time.Millisecond, wantErr)

	got := tracker.Snapshot()
	if got.DrainSucceeded != 1 || got.DrainDeadline != 1 || got.DrainCanceled != 1 || got.DrainFailed != 1 {
		t.Fatalf("drain outcomes = %+v", got)
	}
	if got.DrainDurationTotal != 26*time.Millisecond || got.LastDrainDuration != 11*time.Millisecond || got.MaxDrainDuration != 11*time.Millisecond {
		t.Fatalf("drain durations = %+v", got)
	}
}

func TestTrackerAggregatesConcurrentQueueShards(t *testing.T) {
	const (
		queueCount     = trackerShardCount * 2
		writesPerQueue = 100
	)
	var tracker Tracker
	queues := make([]*Queue[int], queueCount)
	shards := make(map[*queueTracker]struct{}, trackerShardCount)
	for index := range queues {
		queues[index] = NewTracked[int](Config{MaxFrames: 1, MaxBytes: 1}, &tracker)
		shards[queues[index].tracker] = struct{}{}
	}
	if len(shards) != trackerShardCount {
		t.Fatalf("assigned queue shards = %d, want %d", len(shards), trackerShardCount)
	}

	var group sync.WaitGroup
	for _, queue := range queues {
		group.Add(1)
		go func(queue *Queue[int]) {
			defer group.Done()
			for write := 0; write < writesPerQueue; write++ {
				reservation, err := queue.Reserve(1)
				if err != nil {
					t.Errorf("Reserve() error = %v", err)
					return
				}
				if err := reservation.Commit(write, 1); err != nil {
					t.Errorf("Commit() error = %v", err)
					return
				}
				item := <-queue.Items()
				item.Done()
			}
		}(queue)
	}
	group.Wait()

	wantWrites := uint64(queueCount * writesPerQueue)
	snapshot := tracker.Snapshot()
	if snapshot.Sessions != queueCount || snapshot.PendingWrites != 0 || snapshot.PendingBytes != 0 ||
		snapshot.AcceptedWrites != wantWrites || snapshot.AcceptedBytes != wantWrites ||
		snapshot.SucceededWrites != wantWrites || snapshot.SucceededBytes != wantWrites {
		t.Fatalf("aggregate sharded snapshot = %+v, want sessions=%d writes=%d", snapshot, queueCount, wantWrites)
	}
	for _, queue := range queues {
		queue.Close()
	}
	if snapshot := tracker.Snapshot(); snapshot.Sessions != 0 {
		t.Fatalf("sessions after shard close = %d, want 0", snapshot.Sessions)
	}
}

func BenchmarkTrackedOutboundQueueRoundTrip(b *testing.B) {
	var tracker Tracker
	queue := NewTracked[int](Config{MaxFrames: 1, MaxBytes: 1}, &tracker)
	b.Cleanup(queue.Close)
	b.ReportAllocs()
	for iteration := 0; iteration < b.N; iteration++ {
		reservation, err := queue.Reserve(1)
		if err != nil {
			b.Fatal(err)
		}
		if err := reservation.Commit(iteration, 1); err != nil {
			b.Fatal(err)
		}
		item := <-queue.Items()
		item.Done()
	}
}

func BenchmarkOutboundQueueParallel(b *testing.B) {
	b.Run("untracked", func(b *testing.B) {
		benchmarkOutboundQueueParallel(b, nil)
	})
	b.Run("tracked", func(b *testing.B) {
		var tracker Tracker
		benchmarkOutboundQueueParallel(b, &tracker)
	})
}

func benchmarkOutboundQueueParallel(b *testing.B, tracker *Tracker) {
	b.ReportAllocs()
	b.RunParallel(func(parallel *testing.PB) {
		queue := NewTracked[int](Config{MaxFrames: 1, MaxBytes: 1}, tracker)
		defer queue.Close()
		for parallel.Next() {
			reservation, err := queue.Reserve(1)
			if err != nil {
				b.Fatal(err)
			}
			if err := reservation.Commit(1, 1); err != nil {
				b.Fatal(err)
			}
			item := <-queue.Items()
			item.Done()
		}
	})
}
