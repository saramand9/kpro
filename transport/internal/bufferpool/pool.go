// Package bufferpool provides a bounded retained-byte pool for transport-owned
// payload buffers. Queue limits bound buffers in use; this pool separately
// bounds memory retained after completed writes.
package bufferpool

import (
	"math/bits"
	"sync/atomic"
)

const (
	defaultMinClassSize = 16
	defaultMaxClassSize = 64 * 1024
	maxClassEntries     = 256
)

// Config controls size classes and the maximum memory retained while idle.
type Config struct {
	MaxRetainedBytes int64
	MaxPooledSize    int
}

// Pool reuses power-of-two byte slices without allowing idle retained memory
// to exceed MaxRetainedBytes. It is safe for concurrent use.
type Pool struct {
	classes     []sizeClass
	maxRetained int64
	retained    atomic.Int64
}

type sizeClass struct {
	size  int
	items chan []byte
}

// New constructs a bounded pool. A non-positive retained-byte limit disables
// retention while preserving the allocation API.
func New(config Config) *Pool {
	maxSize := normalizedMaxPooledSize(config.MaxPooledSize)

	pool := &Pool{maxRetained: max(config.MaxRetainedBytes, 0)}
	for size := defaultMinClassSize; ; size <<= 1 {
		capacity := 1
		if pool.maxRetained > 0 {
			capacity = int(min(pool.maxRetained/int64(size), maxClassEntries))
			if capacity < 1 {
				capacity = 1
			}
		}
		pool.classes = append(pool.classes, sizeClass{size: size, items: make(chan []byte, capacity)})
		if size == maxSize {
			break
		}
	}
	return pool
}

func normalizedMaxPooledSize(size int) int {
	if size <= 0 {
		size = defaultMaxClassSize
	}
	if size < defaultMinClassSize {
		size = defaultMinClassSize
	}
	return nextPowerOfTwo(size)
}

// Get returns a zeroed slice of exactly size bytes. Buffers larger than the
// largest class are allocated directly and will not be retained by Put.
func (p *Pool) Get(size int) []byte {
	return p.get(size, true)
}

// GetUninitialized returns transport-internal scratch storage without clearing
// a reused buffer. Callers must overwrite every returned byte before exposing
// or sending it. Use Get for application-visible buffers.
func (p *Pool) GetUninitialized(size int) []byte {
	return p.get(size, false)
}

func (p *Pool) get(size int, clearReused bool) []byte {
	if size < 0 {
		panic("bufferpool: negative size")
	}
	if size == 0 {
		return []byte{}
	}
	class := p.classFor(size)
	if class == nil {
		return make([]byte, size)
	}
	select {
	case buffer := <-class.items:
		p.retained.Add(-int64(class.size))
		buffer = buffer[:size]
		if clearReused {
			// Application-visible buffers retain their size-class capacity. Clear
			// the complete allocation so reslicing cannot expose bytes written by
			// a previous owner beyond the new visible length.
			clear(buffer[:cap(buffer)])
		}
		return buffer
	default:
		return make([]byte, size, class.size)
	}
}

// Put returns a class-sized buffer when the retained-byte bound permits it.
// Buffers with foreign capacities or oversized buffers are discarded.
func (p *Pool) Put(buffer []byte) {
	if p == nil || p.maxRetained <= 0 || cap(buffer) == 0 {
		return
	}
	class := p.classFor(cap(buffer))
	if class == nil || class.size != cap(buffer) {
		return
	}
	bytes := int64(class.size)
	for {
		retained := p.retained.Load()
		if retained > p.maxRetained-bytes {
			return
		}
		if p.retained.CompareAndSwap(retained, retained+bytes) {
			break
		}
	}
	buffer = buffer[:class.size]
	select {
	case class.items <- buffer:
	default:
		p.retained.Add(-bytes)
	}
}

// RetainedBytes reports the current idle memory retained by the pool.
func (p *Pool) RetainedBytes() int64 {
	if p == nil {
		return 0
	}
	return p.retained.Load()
}

func (p *Pool) classFor(size int) *sizeClass {
	if p == nil || size <= 0 {
		return nil
	}
	classSize := nextPowerOfTwo(max(size, defaultMinClassSize))
	if classSize < size {
		return nil
	}
	index := bits.TrailingZeros(uint(classSize)) - bits.TrailingZeros(uint(defaultMinClassSize))
	if index < 0 || index >= len(p.classes) {
		return nil
	}
	return &p.classes[index]
}

func nextPowerOfTwo(value int) int {
	if value <= 1 {
		return 1
	}
	// int cannot represent the next power of two above its highest positive
	// power. Cap internal pooling there; larger requests remain valid direct
	// allocations and are discarded by Put.
	highest := int(1) << (bits.UintSize - 2)
	if value >= highest {
		return highest
	}
	return 1 << bits.Len(uint(value-1))
}
