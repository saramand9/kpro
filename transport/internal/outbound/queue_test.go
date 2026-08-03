package outbound

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestQueueEnforcesFrameAndByteLimits(t *testing.T) {
	queue := New[string](Config{MaxFrames: 2, MaxBytes: 10, LowWatermarkBytes: 3, HighWatermarkBytes: 7})

	first, err := queue.Reserve(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Commit("first", 6); err != nil {
		t.Fatal(err)
	}
	second, err := queue.Reserve(4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Reserve(1); !errors.Is(err, ErrFull) {
		t.Fatalf("third Reserve() error = %v, want %v", err, ErrFull)
	}
	if err := second.Commit("second", 5); !errors.Is(err, ErrFull) {
		t.Fatalf("oversized reconciliation error = %v, want %v", err, ErrFull)
	}

	snapshot := queue.Snapshot()
	if snapshot.PendingFrames != 1 || snapshot.PendingBytes != 6 {
		t.Fatalf("snapshot after rejection = %+v", snapshot)
	}
	item := <-queue.Items()
	item.Done()
	if snapshot := queue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after write = %+v", snapshot)
	}
}

func TestQueueWatermarkUsesHysteresis(t *testing.T) {
	queue := New[int](Config{MaxFrames: 4, MaxBytes: 10, LowWatermarkBytes: 3, HighWatermarkBytes: 7})
	first := reserveAndCommit(t, queue, 3, 1)
	second := reserveAndCommit(t, queue, 4, 2)
	if snapshot := queue.Snapshot(); !snapshot.AboveHighWatermark || snapshot.Writable {
		t.Fatalf("snapshot at high watermark = %+v", snapshot)
	}
	first.Done()
	if snapshot := queue.Snapshot(); !snapshot.AboveHighWatermark || snapshot.Writable {
		t.Fatalf("snapshot between watermarks = %+v", snapshot)
	}
	second.Done()
	if snapshot := queue.Snapshot(); snapshot.AboveHighWatermark || !snapshot.Writable {
		t.Fatalf("snapshot at low watermark = %+v", snapshot)
	}
}

func TestWaitWritableUsesWatermarkHysteresis(t *testing.T) {
	queue := New[int](Config{
		MaxFrames:          4,
		MaxBytes:           10,
		LowWatermarkBytes:  3,
		HighWatermarkBytes: 7,
	})
	first := reserveAndCommit(t, queue, 3, 1)
	second := reserveAndCommit(t, queue, 4, 2)
	result := make(chan error, 1)
	go func() { result <- queue.WaitWritable(context.Background()) }()
	waitForWaitingWriters(t, queue, 1)

	first.Done()
	select {
	case err := <-result:
		t.Fatalf("WaitWritable() returned above low watermark: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	second.Done()
	if err := waitWritableResult(t, result); err != nil {
		t.Fatalf("WaitWritable() error = %v", err)
	}
}

func TestWaitWritableReturnsImmediatelyWithoutNotificationAllocation(t *testing.T) {
	queue := New[int](Config{MaxFrames: 1, MaxBytes: 10})
	if err := queue.WaitWritable(nil); err != nil {
		t.Fatalf("WaitWritable() error = %v", err)
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.writableChanged != nil || queue.waitingWriters != 0 {
		t.Fatalf("immediate wait allocated notification state: channel=%v waiting=%d", queue.writableChanged, queue.waitingWriters)
	}
}

func TestWaitWritableWakesForFrameCapacity(t *testing.T) {
	queue := New[int](Config{MaxFrames: 1, MaxBytes: 10})
	item := reserveAndCommit(t, queue, 1, 1)
	result := make(chan error, 1)
	go func() { result <- queue.WaitWritable(context.Background()) }()
	waitForWaitingWriters(t, queue, 1)

	item.Done()
	if err := waitWritableResult(t, result); err != nil {
		t.Fatalf("WaitWritable() error = %v", err)
	}
}

func TestWaitWritableWakesWhenCommitShrinksReservation(t *testing.T) {
	queue := New[int](Config{
		MaxFrames:          2,
		MaxBytes:           10,
		LowWatermarkBytes:  3,
		HighWatermarkBytes: 7,
	})
	reservation, err := queue.Reserve(7)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- queue.WaitWritable(context.Background()) }()
	waitForWaitingWriters(t, queue, 1)

	if err := reservation.Commit(1, 2); err != nil {
		t.Fatal(err)
	}
	if err := waitWritableResult(t, result); err != nil {
		t.Fatalf("WaitWritable() error = %v", err)
	}
	item := <-queue.Items()
	item.Done()
}

func TestWaitWritableWakesWhenReservationIsCanceled(t *testing.T) {
	queue := New[int](Config{
		MaxFrames:          2,
		MaxBytes:           10,
		LowWatermarkBytes:  3,
		HighWatermarkBytes: 7,
	})
	reservation, err := queue.Reserve(7)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- queue.WaitWritable(context.Background()) }()
	waitForWaitingWriters(t, queue, 1)

	reservation.Cancel()
	if err := waitWritableResult(t, result); err != nil {
		t.Fatalf("WaitWritable() error = %v", err)
	}
}

func TestWaitWritableHonorsContextAndWaiterLimit(t *testing.T) {
	queue := New[int](Config{MaxFrames: 1, MaxBytes: 10, MaxWaiters: 1})
	item := reserveAndCommit(t, queue, 1, 1)
	first := make(chan error, 1)
	go func() { first <- queue.WaitWritable(context.Background()) }()
	waitForWaitingWriters(t, queue, 1)

	if err := queue.WaitWritable(context.Background()); !errors.Is(err, ErrTooManyWaiters) {
		t.Fatalf("overflow WaitWritable() error = %v, want %v", err, ErrTooManyWaiters)
	}
	queue.Close()
	if err := waitWritableResult(t, first); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed WaitWritable() error = %v, want %v", err, ErrClosed)
	}
	snapshot := queue.Snapshot()
	if snapshot.WaitingWriters != 0 || snapshot.MaxWaitingWriters != 1 || snapshot.PendingFrames != 1 {
		t.Fatalf("closed writer-owned snapshot = %+v", snapshot)
	}
	item.Done()
	if snapshot := queue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after writer completion = %+v", snapshot)
	}
}

func TestWaitWritableContextCancellationReleasesWaiter(t *testing.T) {
	queue := New[int](Config{MaxFrames: 1, MaxBytes: 10})
	item := reserveAndCommit(t, queue, 1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- queue.WaitWritable(ctx) }()
	waitForWaitingWriters(t, queue, 1)

	cancel()
	if err := waitWritableResult(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitWritable() error = %v, want %v", err, context.Canceled)
	}
	waitForWaitingWriters(t, queue, 0)

	reuseCtx, reuseCancel := context.WithCancel(context.Background())
	reuseResult := make(chan error, 1)
	go func() { reuseResult <- queue.WaitWritable(reuseCtx) }()
	waitForWaitingWriters(t, queue, 1)
	reuseCancel()
	if err := waitWritableResult(t, reuseResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("reused waiter slot error = %v, want %v", err, context.Canceled)
	}
	item.Done()
}

func TestWaitWritableBroadcastsOnRecoveryAndClose(t *testing.T) {
	tests := []struct {
		name    string
		close   bool
		wantErr error
	}{
		{name: "recovery"},
		{name: "close", close: true, wantErr: ErrClosed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queue := New[int](Config{MaxFrames: 1, MaxBytes: 10, MaxWaiters: 4})
			item := reserveAndCommit(t, queue, 1, 1)
			results := make(chan error, 4)
			for range 4 {
				go func() { results <- queue.WaitWritable(context.Background()) }()
			}
			waitForWaitingWriters(t, queue, 4)
			if test.close {
				queue.Close()
			} else {
				item.Done()
			}
			for range 4 {
				err := waitWritableResult(t, results)
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("WaitWritable() error = %v, want %v", err, test.wantErr)
				}
			}
			if test.close {
				item.Done()
			}
			waitForWaitingWriters(t, queue, 0)
		})
	}
}

func TestWaitWritableWakesWhenCommitExpansionIsRejected(t *testing.T) {
	queue := New[int](Config{
		MaxFrames:          2,
		MaxBytes:           10,
		LowWatermarkBytes:  3,
		HighWatermarkBytes: 7,
	})
	reservation, err := queue.Reserve(7)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- queue.WaitWritable(context.Background()) }()
	waitForWaitingWriters(t, queue, 1)

	if err := reservation.Commit(1, 11); !errors.Is(err, ErrFull) {
		t.Fatalf("Commit() error = %v, want %v", err, ErrFull)
	}
	if err := waitWritableResult(t, result); err != nil {
		t.Fatalf("WaitWritable() error = %v", err)
	}
	if snapshot := queue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after rejected expansion = %+v", snapshot)
	}
}

func TestWaitWritableDoesNotLoseWakeups(t *testing.T) {
	for iteration := 0; iteration < 200; iteration++ {
		queue := New[int](Config{MaxFrames: 1, MaxBytes: 10})
		item := reserveAndCommit(t, queue, 1, iteration)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result := make(chan error, 1)
		go func() { result <- queue.WaitWritable(ctx) }()
		waitForWaitingWriters(t, queue, 1)
		item.Done()
		if err := waitWritableResult(t, result); err != nil {
			cancel()
			t.Fatalf("iteration %d WaitWritable() error = %v", iteration, err)
		}
		cancel()
	}
}

func TestWaitWritableReleaseRegistrationRace(t *testing.T) {
	for iteration := 0; iteration < 500; iteration++ {
		queue := New[int](Config{MaxFrames: 1, MaxBytes: 10})
		item := reserveAndCommit(t, queue, 1, iteration)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result := make(chan error, 1)
		go func() { result <- queue.WaitWritable(ctx) }()
		item.Done()
		if err := waitWritableResult(t, result); err != nil {
			cancel()
			t.Fatalf("iteration %d WaitWritable() error = %v", iteration, err)
		}
		cancel()
	}
}

func TestDefaultHighWatermarkDoesNotOverflow(t *testing.T) {
	queue := New[int](Config{MaxFrames: 1, MaxBytes: math.MaxInt64})
	snapshot := queue.Snapshot()
	want := int64(math.MaxInt64 - math.MaxInt64/5)
	if snapshot.HighWatermarkBytes != want || snapshot.HighWatermarkBytes <= 0 {
		t.Fatalf("default high watermark = %d, want %d", snapshot.HighWatermarkBytes, want)
	}
	if snapshot.MaxWaitingWriters != 1 {
		t.Fatalf("default max waiting writers = %d, want 1", snapshot.MaxWaitingWriters)
	}
}

func TestExplicitZeroLowWatermarkIsPreserved(t *testing.T) {
	queue := New[int](Config{
		MaxFrames:          1,
		MaxBytes:           10,
		LowWatermarkBytes:  0,
		HighWatermarkBytes: 2,
	})
	if got := queue.Snapshot().LowWatermarkBytes; got != 0 {
		t.Fatalf("low watermark = %d, want explicit zero", got)
	}
}

func TestCloseRejectsInflightCommitAndFutureReservations(t *testing.T) {
	queue := New[int](Config{MaxFrames: 2, MaxBytes: 10})
	reservation, err := queue.Reserve(5)
	if err != nil {
		t.Fatal(err)
	}
	queue.Close()
	if err := reservation.Commit(1, 5); !errors.Is(err, ErrClosed) {
		t.Fatalf("Commit() error = %v, want %v", err, ErrClosed)
	}
	if _, err := queue.Reserve(1); !errors.Is(err, ErrClosed) {
		t.Fatalf("Reserve() error = %v, want %v", err, ErrClosed)
	}
	if snapshot := queue.Snapshot(); !snapshot.Closed || snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("closed snapshot = %+v", snapshot)
	}
}

func TestConcurrentCloseLeavesNoSuccessfulPostCloseAdmission(t *testing.T) {
	queue := New[int](Config{MaxFrames: 64, MaxBytes: 1 << 20})
	start := make(chan struct{})
	var group sync.WaitGroup
	for producer := 0; producer < 32; producer++ {
		group.Add(1)
		go func(value int) {
			defer group.Done()
			<-start
			reservation, err := queue.Reserve(1)
			if err != nil {
				return
			}
			_ = reservation.Commit(value, 1)
		}(producer)
	}
	close(start)
	queue.Close()
	group.Wait()

	for attempt := 0; attempt < 1000; attempt++ {
		if _, err := queue.Reserve(1); !errors.Is(err, ErrClosed) {
			t.Fatalf("post-close Reserve() error = %v, want %v", err, ErrClosed)
		}
	}
	if snapshot := queue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("closed snapshot = %+v", snapshot)
	}
}

func BenchmarkOutboundQueueRoundTrip(b *testing.B) {
	queue := New[[]byte](Config{MaxFrames: 64, MaxBytes: 64 << 10})
	payload := make([]byte, 1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		reservation, err := queue.Reserve(int64(len(payload)))
		if err != nil {
			b.Fatal(err)
		}
		if err := reservation.Commit(payload, int64(len(payload))); err != nil {
			b.Fatal(err)
		}
		item := <-queue.Items()
		item.Done()
	}
}

func reserveAndCommit[T any](t *testing.T, queue *Queue[T], size int64, value T) Item[T] {
	t.Helper()
	reservation, err := queue.Reserve(size)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Commit(value, size); err != nil {
		t.Fatal(err)
	}
	return <-queue.Items()
}

func waitForWaitingWriters[T any](t *testing.T, queue *Queue[T], want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if queue.Snapshot().WaitingWriters == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiting writers = %d, want %d", queue.Snapshot().WaitingWriters, want)
}

func waitWritableResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WaitWritable()")
		return nil
	}
}
