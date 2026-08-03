// Package outbound provides the shared bounded queue used by message
// transports. It deliberately remains internal until the public transport
// flow-control contract is settled.
package outbound

import (
	"context"
	"errors"
	"math"
	"sync"
)

var (
	// ErrClosed reports an admission attempt after the queue was closed.
	ErrClosed = errors.New("outbound queue closed")
	// ErrFull reports a frame-count or pending-byte budget rejection.
	ErrFull = errors.New("outbound queue full")
	// ErrInvalidSize reports a negative reservation or encoded size.
	ErrInvalidSize = errors.New("outbound size must not be negative")
	// ErrTooManyWaiters reports that the per-queue writable-wait bound was
	// reached.
	ErrTooManyWaiters = errors.New("outbound writable waiter limit reached")
)

// Config defines hard admission limits and byte-watermark hysteresis.
type Config struct {
	MaxFrames          int
	MaxBytes           int64
	MaxWaiters         int
	LowWatermarkBytes  int64
	HighWatermarkBytes int64
}

// Snapshot is a point-in-time view of queue admission state.
type Snapshot struct {
	Closed             bool
	Writable           bool
	AboveHighWatermark bool
	PendingFrames      int
	MaxFrames          int
	PendingBytes       int64
	MaxBytes           int64
	WaitingWriters     int
	MaxWaitingWriters  int
	LowWatermarkBytes  int64
	HighWatermarkBytes int64
}

// Queue accounts reservations, queued frames, and the frame currently being
// written. Capacity is released only when Item.Done is called.
type Queue[T any] struct {
	mu sync.Mutex

	items              chan Item[T]
	closed             bool
	pendingFrames      int
	pendingBytes       int64
	aboveHighWatermark bool
	maxFrames          int
	maxBytes           int64
	lowWatermarkBytes  int64
	highWatermarkBytes int64
	writableChanged    chan struct{}
	waitingWriters     int
	maxWaiters         int
}

// Reservation holds frame and estimated-byte capacity while a caller encodes
// a message. A reservation must not be copied after first use and must be
// committed or canceled exactly once.
type Reservation[T any] struct {
	queue          *Queue[T]
	estimatedBytes int64
	active         bool
}

// Item is one admitted value. A received item must not be copied, and Done must
// be called after the write attempt whether the attempt succeeds or fails.
type Item[T any] struct {
	Value T

	queue  *Queue[T]
	bytes  int64
	active bool
}

// New creates a bounded queue. Invalid internal configuration is a programmer
// error and panics during transport construction.
func New[T any](config Config) *Queue[T] {
	config = normalizeConfig(config)
	return &Queue[T]{
		items:              make(chan Item[T], config.MaxFrames),
		maxFrames:          config.MaxFrames,
		maxBytes:           config.MaxBytes,
		maxWaiters:         config.MaxWaiters,
		lowWatermarkBytes:  config.LowWatermarkBytes,
		highWatermarkBytes: config.HighWatermarkBytes,
	}
}

// ByteLimit derives a saturating byte budget from the configured frame count
// and expected maximum encoded frame size.
func ByteLimit(maxFrames int, maxFrameBytes int64) int64 {
	if maxFrames <= 0 || maxFrameBytes <= 0 {
		panic("outbound: byte limit inputs must be positive")
	}
	if maxFrameBytes > math.MaxInt64/int64(maxFrames) {
		return math.MaxInt64
	}
	return int64(maxFrames) * maxFrameBytes
}

// EstimateBytes returns a saturating payload-plus-overhead estimate suitable
// for reserving capacity before a codec allocates the encoded frame.
func EstimateBytes(payloadBytes int, overheadBytes int64) int64 {
	if payloadBytes < 0 || overheadBytes < 0 {
		panic("outbound: size estimate inputs must not be negative")
	}
	payload := int64(payloadBytes)
	if payload > math.MaxInt64-overheadBytes {
		return math.MaxInt64
	}
	return payload + overheadBytes
}

// Reserve atomically claims one frame slot and estimated byte capacity before
// encoding begins.
func (q *Queue[T]) Reserve(estimatedBytes int64) (Reservation[T], error) {
	if estimatedBytes < 0 {
		return Reservation[T]{}, ErrInvalidSize
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return Reservation[T]{}, ErrClosed
	}
	if q.pendingFrames >= q.maxFrames || exceeds(q.pendingBytes, estimatedBytes, q.maxBytes) {
		return Reservation[T]{}, ErrFull
	}
	q.pendingFrames++
	q.pendingBytes += estimatedBytes
	q.updateWatermarkLocked()
	return Reservation[T]{queue: q, estimatedBytes: estimatedBytes, active: true}, nil
}

// Commit reconciles the estimate against the encoded size and publishes the
// item. It cannot succeed after Queue.Close has linearized.
func (r *Reservation[T]) Commit(value T, actualBytes int64) error {
	if r == nil || !r.active || r.queue == nil {
		panic("outbound: commit of inactive reservation")
	}
	if actualBytes < 0 {
		r.Cancel()
		return ErrInvalidSize
	}

	q := r.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		q.releaseLocked(r.estimatedBytes)
		r.active = false
		return ErrClosed
	}
	delta := actualBytes - r.estimatedBytes
	if delta > 0 && exceeds(q.pendingBytes, delta, q.maxBytes) {
		q.releaseLocked(r.estimatedBytes)
		r.active = false
		return ErrFull
	}
	q.pendingBytes += delta
	q.updateWatermarkLocked()
	q.notifyWritableLocked()
	item := Item[T]{Value: value, queue: q, bytes: actualBytes, active: true}
	select {
	case q.items <- item:
		r.active = false
		return nil
	default:
		// Reserved frame accounting makes this unreachable unless queue state is
		// corrupted. Treat it as overload while preserving accounting safety.
		q.releaseLocked(actualBytes)
		r.active = false
		return ErrFull
	}
}

// Cancel releases a reservation after encoding fails.
func (r *Reservation[T]) Cancel() {
	if r == nil || !r.active || r.queue == nil {
		return
	}
	r.queue.mu.Lock()
	if r.active {
		r.queue.releaseLocked(r.estimatedBytes)
		r.active = false
	}
	r.queue.mu.Unlock()
}

// Items returns the receive-only write queue.
func (q *Queue[T]) Items() <-chan Item[T] {
	return q.items
}

// WaitWritable waits until the advisory writable predicate is true. It does
// not reserve capacity, so a concurrent sender may consume capacity before the
// caller's next Reserve.
func (q *Queue[T]) WaitWritable(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	registered := false
	defer func() {
		if !registered {
			return
		}
		q.mu.Lock()
		q.waitingWriters--
		q.mu.Unlock()
	}()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return ErrClosed
		}
		if q.writableLocked() {
			q.mu.Unlock()
			return nil
		}
		if !registered {
			if q.waitingWriters >= q.maxWaiters {
				q.mu.Unlock()
				return ErrTooManyWaiters
			}
			q.waitingWriters++
			registered = true
		}
		if q.writableChanged == nil {
			q.writableChanged = make(chan struct{})
		}
		changed := q.writableChanged
		q.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Done releases the item's frame and exact encoded-byte capacity.
func (i *Item[T]) Done() {
	if i == nil || i.queue == nil {
		return
	}
	i.queue.mu.Lock()
	if i.active {
		i.queue.releaseLocked(i.bytes)
		i.active = false
	}
	i.queue.mu.Unlock()
}

// Close rejects future reservations and releases every item still queued.
// An item already owned by a writer remains pending until that writer calls
// Done, so snapshots continue to reflect the true retained bytes.
func (q *Queue[T]) Close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.notifyAllWaitersLocked()
	drained := make([]Item[T], 0, len(q.items))
	for {
		select {
		case item := <-q.items:
			drained = append(drained, item)
		default:
			q.mu.Unlock()
			for index := range drained {
				drained[index].Done()
			}
			return
		}
	}
}

// Snapshot returns current limits, accounting, and hysteresis state.
func (q *Queue[T]) Snapshot() Snapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	return Snapshot{
		Closed:             q.closed,
		Writable:           q.writableLocked(),
		AboveHighWatermark: q.aboveHighWatermark,
		PendingFrames:      q.pendingFrames,
		MaxFrames:          q.maxFrames,
		PendingBytes:       q.pendingBytes,
		MaxBytes:           q.maxBytes,
		WaitingWriters:     q.waitingWriters,
		MaxWaitingWriters:  q.maxWaiters,
		LowWatermarkBytes:  q.lowWatermarkBytes,
		HighWatermarkBytes: q.highWatermarkBytes,
	}
}

func (q *Queue[T]) releaseLocked(bytes int64) {
	if q.pendingFrames <= 0 || bytes < 0 || q.pendingBytes < bytes {
		panic("outbound: capacity released without matching reservation")
	}
	q.pendingFrames--
	q.pendingBytes -= bytes
	q.updateWatermarkLocked()
	q.notifyWritableLocked()
}

func (q *Queue[T]) writableLocked() bool {
	return !q.closed &&
		!q.aboveHighWatermark &&
		q.pendingFrames < q.maxFrames &&
		q.pendingBytes < q.maxBytes
}

func (q *Queue[T]) notifyWritableLocked() {
	if q.writableChanged == nil || !q.writableLocked() {
		return
	}
	close(q.writableChanged)
	q.writableChanged = nil
}

func (q *Queue[T]) notifyAllWaitersLocked() {
	if q.writableChanged == nil {
		return
	}
	close(q.writableChanged)
	q.writableChanged = nil
}

func (q *Queue[T]) updateWatermarkLocked() {
	if q.aboveHighWatermark {
		if q.pendingBytes <= q.lowWatermarkBytes {
			q.aboveHighWatermark = false
		}
		return
	}
	if q.pendingBytes >= q.highWatermarkBytes {
		q.aboveHighWatermark = true
	}
}

func exceeds(current, additional, maximum int64) bool {
	return additional > maximum-current
}

func normalizeConfig(config Config) Config {
	if config.MaxFrames <= 0 {
		panic("outbound: MaxFrames must be positive")
	}
	if config.MaxBytes <= 0 {
		panic("outbound: MaxBytes must be positive")
	}
	if config.MaxWaiters < 0 {
		panic("outbound: MaxWaiters must not be negative")
	}
	if config.MaxWaiters == 0 {
		config.MaxWaiters = config.MaxFrames
	}
	defaultWatermarks := config.HighWatermarkBytes == 0
	if defaultWatermarks {
		config.HighWatermarkBytes = config.MaxBytes - config.MaxBytes/5
		if config.HighWatermarkBytes == 0 {
			config.HighWatermarkBytes = config.MaxBytes
		}
	}
	if defaultWatermarks && config.LowWatermarkBytes == 0 && config.HighWatermarkBytes > 1 {
		config.LowWatermarkBytes = config.MaxBytes / 2
	}
	if config.LowWatermarkBytes < 0 ||
		config.HighWatermarkBytes <= 0 ||
		config.HighWatermarkBytes > config.MaxBytes ||
		config.LowWatermarkBytes >= config.HighWatermarkBytes {
		panic("outbound: require 0 <= low watermark < high watermark <= MaxBytes")
	}
	return config
}
