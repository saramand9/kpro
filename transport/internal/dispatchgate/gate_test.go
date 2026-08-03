package dispatchgate

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestStopRejectsNewDispatchAndWaitsForAdmittedWork(t *testing.T) {
	gate := New()
	if !gate.Begin() {
		t.Fatal("open gate rejected dispatch")
	}
	gate.Stop()
	if gate.Begin() {
		t.Fatal("stopped gate admitted dispatch")
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Wait(waitCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want canceled", err)
	}

	gate.End()
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() after End error = %v", err)
	}
	gate.Stop()
}

func TestConcurrentStopAndDispatchLeavesGateIdle(t *testing.T) {
	gate := New()
	start := make(chan struct{})
	var group sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			for attempt := 0; attempt < 1000; attempt++ {
				if !gate.Begin() {
					return
				}
				gate.End()
			}
		}()
	}
	close(start)
	gate.Stop()
	group.Wait()
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if gate.Begin() {
		t.Fatal("stopped gate admitted dispatch after concurrent drain")
	}
}

func TestOpenGateDispatchIsAllocationFree(t *testing.T) {
	gate := New()
	allocations := testing.AllocsPerRun(1000, func() {
		if !gate.Begin() {
			panic("open gate rejected dispatch")
		}
		gate.End()
	})
	if allocations != 0 {
		t.Fatalf("Begin/End allocations = %v, want 0", allocations)
	}
}

func BenchmarkGateBeginEnd(b *testing.B) {
	gate := New()
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		if !gate.Begin() {
			b.Fatal("open gate rejected dispatch")
		}
		gate.End()
	}
}
