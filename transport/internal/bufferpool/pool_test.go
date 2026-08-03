package bufferpool

import (
	"math"
	"math/bits"
	"testing"
)

func TestPoolReusesAndZeroesClassBuffer(t *testing.T) {
	pool := New(Config{MaxRetainedBytes: 1024, MaxPooledSize: 1024})
	buffer := pool.Get(100)
	for index := range buffer[:cap(buffer)] {
		buffer[:cap(buffer)][index] = 0xff
	}
	pool.Put(buffer)
	if got := pool.RetainedBytes(); got != 128 {
		t.Fatalf("RetainedBytes() = %d, want 128", got)
	}

	reused := pool.Get(100)
	if got := pool.RetainedBytes(); got != 0 {
		t.Fatalf("RetainedBytes() after Get = %d, want 0", got)
	}
	for index, value := range reused[:cap(reused)] {
		if value != 0 {
			t.Fatalf("reused[%d] = %d, want zero", index, value)
		}
	}
}

func TestPoolUninitializedPathPreservesReusableBytes(t *testing.T) {
	pool := New(Config{MaxRetainedBytes: 1024, MaxPooledSize: 1024})
	buffer := pool.Get(4)
	copy(buffer, "data")
	pool.Put(buffer)
	reused := pool.GetUninitialized(4)
	if string(reused) != "data" {
		t.Fatalf("GetUninitialized() = %q, want reusable contents", reused)
	}
}

func TestPoolNeverExceedsRetainedByteLimit(t *testing.T) {
	pool := New(Config{MaxRetainedBytes: 512, MaxPooledSize: 1024})
	first := pool.Get(200)
	second := pool.Get(200)
	third := pool.Get(200)
	pool.Put(first)
	pool.Put(second)
	pool.Put(third)
	if got := pool.RetainedBytes(); got > 512 {
		t.Fatalf("RetainedBytes() = %d, exceeds 512", got)
	}
}

func TestPoolDropsOversizedAndForeignCapacityBuffers(t *testing.T) {
	pool := New(Config{MaxRetainedBytes: 4096, MaxPooledSize: 1024})
	pool.Put(make([]byte, 2048))
	pool.Put(make([]byte, 10, 300))
	if got := pool.RetainedBytes(); got != 0 {
		t.Fatalf("RetainedBytes() = %d, want 0", got)
	}
}

func TestDisabledPoolDoesNotRetain(t *testing.T) {
	pool := New(Config{MaxRetainedBytes: 0})
	buffer := pool.Get(32)
	pool.Put(buffer)
	if got := pool.RetainedBytes(); got != 0 {
		t.Fatalf("RetainedBytes() = %d, want 0", got)
	}
}

func TestPoolCapsExtremeClassSizeWithoutShiftOverflow(t *testing.T) {
	pool := New(Config{MaxRetainedBytes: 1, MaxPooledSize: math.MaxInt})
	if len(pool.classes) == 0 {
		t.Fatal("New() produced no size classes")
	}
	highest := int(1) << (bits.UintSize - 2)
	if got := pool.classes[len(pool.classes)-1].size; got != highest {
		t.Fatalf("largest class = %d, want %d", got, highest)
	}
	if class := pool.classFor(math.MaxInt); class != nil {
		t.Fatalf("classFor(MaxInt) = %+v, want direct allocation", class)
	}
}
