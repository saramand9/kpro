package outbound

import (
	"sync/atomic"
	"testing"
)

func TestItemDoneRunsFinalizerOnceOutsideQueueLock(t *testing.T) {
	var finalized atomic.Int32
	var queue *Queue[int]
	queue = NewWithFinalizer(Config{MaxFrames: 1, MaxBytes: 8}, func(value int) {
		if value != 7 {
			t.Errorf("finalized value = %d, want 7", value)
		}
		_ = queue.Snapshot()
		finalized.Add(1)
	})
	reservation, err := queue.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Commit(7, 1); err != nil {
		t.Fatal(err)
	}
	item := <-queue.Items()
	item.Done()
	item.Done()
	if got := finalized.Load(); got != 1 {
		t.Fatalf("finalizer calls = %d, want 1", got)
	}
}

func TestQueueCloseFinalizesQueuedItems(t *testing.T) {
	var sum atomic.Int32
	queue := NewWithFinalizer(Config{MaxFrames: 3, MaxBytes: 8}, func(value int) {
		sum.Add(int32(value))
	})
	for value := 1; value <= 3; value++ {
		reservation, err := queue.Reserve(1)
		if err != nil {
			t.Fatal(err)
		}
		if err := reservation.Commit(value, 1); err != nil {
			t.Fatal(err)
		}
	}
	queue.Close()
	if got := sum.Load(); got != 6 {
		t.Fatalf("finalized sum = %d, want 6", got)
	}
	if snapshot := queue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after Close = %+v", snapshot)
	}
}

func TestQueueCloseCompletesRemainingItemsAfterFinalizerPanic(t *testing.T) {
	var finalized atomic.Int32
	queue := NewWithFinalizer(Config{MaxFrames: 3, MaxBytes: 8}, func(value int) {
		finalized.Add(1)
		if value == 1 {
			panic("finalizer failed")
		}
	})
	for value := 1; value <= 3; value++ {
		reservation, err := queue.Reserve(1)
		if err != nil {
			t.Fatal(err)
		}
		if err := reservation.Commit(value, 1); err != nil {
			t.Fatal(err)
		}
	}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		queue.Close()
	}()
	if recovered == nil {
		t.Fatal("Queue.Close() did not propagate finalizer panic")
	}
	if got := finalized.Load(); got != 3 {
		t.Fatalf("finalizer calls = %d, want 3", got)
	}
	if snapshot := queue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after panicking finalizer = %+v", snapshot)
	}
}
