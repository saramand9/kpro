package outbound

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDrainQueuesSealsLaterQueuesBeforeWaitingForSlowWriter(t *testing.T) {
	first := New[int](Config{MaxFrames: 2, MaxBytes: 20})
	second := New[int](Config{MaxFrames: 2, MaxBytes: 20})
	defer first.Close()
	defer second.Close()
	commitDrainValue(t, first, 1)
	item := <-first.Items()
	defer item.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- DrainQueues(ctx, []*Queue[int]{first, second}) }()

	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !second.Snapshot().Closed {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("later queue still accepts writes while the first writer is pending")
		}
	}
	if _, err := second.Reserve(1); !errors.Is(err, ErrClosed) {
		t.Fatalf("later queue reserve error = %v, want %v", err, ErrClosed)
	}
	select {
	case err := <-result:
		t.Fatalf("drain returned before the writer completed: %v", err)
	default:
	}
	item.Done()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestDrainQueuesPreservesContextAndLaterWriteErrors(t *testing.T) {
	first := New[int](Config{MaxFrames: 2, MaxBytes: 20})
	second := New[int](Config{MaxFrames: 2, MaxBytes: 20})
	defer first.Close()
	defer second.Close()
	commitDrainValue(t, first, 1)
	commitDrainValue(t, second, 2)
	writeErr := errors.New("second writer failed")
	item := <-second.Items()
	item.Complete(writeErr)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := DrainQueues(ctx, []*Queue[int]{first, second})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, writeErr) {
		t.Fatalf("drain error = %v, want both cancellation and write failure", err)
	}
	if !first.Snapshot().Closed || !second.Snapshot().Closed {
		t.Fatal("cancellation left a queue accepting writes")
	}
}
