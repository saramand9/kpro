package outbound

import (
	"sync"
	"testing"
	"time"
)

func TestDrainAvailableIncludesFirstAndPreservesFIFO(t *testing.T) {
	queue := New[int](Config{MaxFrames: 6, MaxBytes: 60})
	for value := 1; value <= 5; value++ {
		commitDrainValue(t, queue, value)
	}

	first := <-queue.Items()
	storage := make([]Item[int], 0, 3)
	batch := queue.DrainAvailable(&first, storage, 3)
	if first.active || first.queue != nil {
		t.Fatalf("first item was not moved: %+v", first)
	}
	if len(batch) != 3 {
		t.Fatalf("batch length = %d, want 3", len(batch))
	}
	for index, want := range []int{1, 2, 3} {
		if batch[index].Value != want {
			t.Fatalf("batch[%d] = %d, want %d", index, batch[index].Value, want)
		}
		batch[index].Done()
	}

	for _, want := range []int{4, 5} {
		item := <-queue.Items()
		if item.Value != want {
			t.Fatalf("remaining item = %d, want %d", item.Value, want)
		}
		item.Done()
	}
	if snapshot := queue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after batch completion = %+v", snapshot)
	}
}

func TestDrainAvailableLimitOneLeavesQueuedItemsUntouched(t *testing.T) {
	queue := New[int](Config{MaxFrames: 2, MaxBytes: 10})
	commitDrainValue(t, queue, 1)
	commitDrainValue(t, queue, 2)
	first := <-queue.Items()

	batch := queue.DrainAvailable(&first, nil, 1)
	if len(batch) != 1 || batch[0].Value != 1 {
		t.Fatalf("batch = %+v, want first item only", batch)
	}
	remaining := <-queue.Items()
	if remaining.Value != 2 {
		t.Fatalf("remaining item = %d, want 2", remaining.Value)
	}
	batch[0].Done()
	remaining.Done()
}

func TestDrainAvailableReturnsWithoutWaitingForMoreItems(t *testing.T) {
	queue := New[int](Config{MaxFrames: 2, MaxBytes: 10})
	commitDrainValue(t, queue, 1)
	first := <-queue.Items()
	result := make(chan []Item[int], 1)
	go func() {
		result <- queue.DrainAvailable(&first, nil, 2)
	}()

	select {
	case batch := <-result:
		if len(batch) != 1 || batch[0].Value != 1 {
			t.Fatalf("batch = %+v, want first item only", batch)
		}
		batch[0].Done()
	case <-time.After(time.Second):
		t.Fatal("DrainAvailable blocked waiting for another item")
	}
}

func TestDrainAvailableRejectsInvalidUse(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "zero limit",
			run: func(t *testing.T) {
				queue, first := newDrainQueueAndFirst(t)
				defer first.Done()
				queue.DrainAvailable(&first, nil, 0)
			},
		},
		{
			name: "negative limit",
			run: func(t *testing.T) {
				queue, first := newDrainQueueAndFirst(t)
				defer first.Done()
				queue.DrainAvailable(&first, nil, -1)
			},
		},
		{
			name: "non-empty destination",
			run: func(t *testing.T) {
				queue, first := newDrainQueueAndFirst(t)
				defer first.Done()
				queue.DrainAvailable(&first, []Item[int]{{}}, 1)
			},
		},
		{
			name: "nil first",
			run: func(t *testing.T) {
				queue := New[int](Config{MaxFrames: 1, MaxBytes: 10})
				queue.DrainAvailable(nil, nil, 1)
			},
		},
		{
			name: "inactive first",
			run: func(t *testing.T) {
				queue := New[int](Config{MaxFrames: 1, MaxBytes: 10})
				queue.DrainAvailable(&Item[int]{}, nil, 1)
			},
		},
		{
			name: "foreign first",
			run: func(t *testing.T) {
				_, first := newDrainQueueAndFirst(t)
				defer first.Done()
				foreign := New[int](Config{MaxFrames: 1, MaxBytes: 10})
				foreign.DrainAvailable(&first, nil, 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("DrainAvailable did not panic")
				}
			}()
			test.run(t)
		})
	}
}

func TestConcurrentDrainAndCloseReleaseEveryItem(t *testing.T) {
	const itemCount = 64
	for iteration := 0; iteration < 100; iteration++ {
		queue := New[int](Config{MaxFrames: itemCount, MaxBytes: itemCount})
		for value := 0; value < itemCount; value++ {
			commitDrainValue(t, queue, value)
		}
		first := <-queue.Items()
		start := make(chan struct{})
		batchResult := make(chan []Item[int], 1)
		var group sync.WaitGroup
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			batchResult <- queue.DrainAvailable(&first, nil, itemCount)
		}()
		close(start)
		queue.Close()
		group.Wait()
		batch := <-batchResult
		if len(batch) == 0 || batch[0].Value != 0 {
			t.Fatalf("iteration %d batch lost first item: %+v", iteration, batch)
		}
		seen := make([]bool, itemCount)
		for index := range batch {
			value := batch[index].Value
			if value < 0 || value >= itemCount || seen[value] {
				t.Fatalf("iteration %d invalid or duplicate batch value %d", iteration, value)
			}
			if value != index {
				t.Fatalf("iteration %d batch[%d] = %d, want contiguous FIFO prefix", iteration, index, value)
			}
			seen[value] = true
			batch[index].Done()
		}

		if snapshot := queue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
			t.Fatalf("iteration %d snapshot after close and Done = %+v", iteration, snapshot)
		}
	}
}

func BenchmarkOutboundQueueDrainAvailable(b *testing.B) {
	const batchSize = 64
	queue := New[[]byte](Config{MaxFrames: batchSize, MaxBytes: batchSize * 1024})
	payload := make([]byte, 1024)
	batch := make([]Item[[]byte], 0, batchSize)
	b.ReportAllocs()
	b.SetBytes(batchSize * int64(len(payload)))
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		for range batchSize {
			reservation, err := queue.Reserve(int64(len(payload)))
			if err != nil {
				b.Fatal(err)
			}
			if err := reservation.Commit(payload, int64(len(payload))); err != nil {
				b.Fatal(err)
			}
		}
		first := <-queue.Items()
		batch = queue.DrainAvailable(&first, batch[:0], batchSize)
		if len(batch) != batchSize {
			b.Fatalf("batch length = %d, want %d", len(batch), batchSize)
		}
		for index := range batch {
			batch[index].Done()
		}
	}
}

func commitDrainValue(t testing.TB, queue *Queue[int], value int) {
	t.Helper()
	reservation, err := queue.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Commit(value, 1); err != nil {
		t.Fatal(err)
	}
}

func newDrainQueueAndFirst(t testing.TB) (*Queue[int], Item[int]) {
	t.Helper()
	queue := New[int](Config{MaxFrames: 1, MaxBytes: 10})
	commitDrainValue(t, queue, 1)
	return queue, <-queue.Items()
}
