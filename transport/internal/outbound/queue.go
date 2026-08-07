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

// Completion is notified exactly once when an admitted item finishes its
// transport write or is discarded. It is intentionally structural so the
// internal queue does not depend on core.
type Completion interface {
	Complete(error) bool
}

// Queue accounts reservations, queued frames, and the frame currently being
// written. Capacity is released only when Item.Complete or Item.Done is called.
type Queue[T any] struct {
	mu sync.Mutex

	items              chan Item[T]
	finalize           func(T)
	tracker            *queueTracker
	sealed             bool
	closed             bool
	drainErr           error
	pendingFrames      int
	pendingBytes       int64
	aboveHighWatermark bool
	maxFrames          int
	maxBytes           int64
	lowWatermarkBytes  int64
	highWatermarkBytes int64
	writableChanged    chan struct{}
	drainedChanged     chan struct{}
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

// Item is one admitted value. A received item must not be copied. Call Done
// after a successful write or Complete with the write error after failure.
type Item[T any] struct {
	Value T

	queue      *Queue[T]
	bytes      int64
	active     bool
	completion Completion
}

// New creates a bounded queue. Invalid internal configuration is a programmer
// error and panics during transport construction.
func New[T any](config Config) *Queue[T] {
	return newQueue[T](config, nil, nil)
}

// NewWithFinalizer creates a bounded queue that invokes finalize exactly once
// when an admitted item is completed or discarded by Close. The callback runs
// after queue accounting is released and without holding the queue lock.
func NewWithFinalizer[T any](config Config, finalize func(T)) *Queue[T] {
	return newQueue(config, nil, finalize)
}

// NewTracked creates a queue that contributes to one transport-level tracker.
func NewTracked[T any](config Config, tracker *Tracker) *Queue[T] {
	return newQueue[T](config, tracker, nil)
}

// NewTrackedWithFinalizer combines transport-level tracking with finalization.
func NewTrackedWithFinalizer[T any](config Config, tracker *Tracker, finalize func(T)) *Queue[T] {
	return newQueue(config, tracker, finalize)
}

func newQueue[T any](config Config, tracker *Tracker, finalize func(T)) *Queue[T] {
	config = normalizeConfig(config)
	queue := &Queue[T]{
		items:              make(chan Item[T], config.MaxFrames),
		finalize:           finalize,
		tracker:            tracker.openQueue(),
		maxFrames:          config.MaxFrames,
		maxBytes:           config.MaxBytes,
		maxWaiters:         config.MaxWaiters,
		lowWatermarkBytes:  config.LowWatermarkBytes,
		highWatermarkBytes: config.HighWatermarkBytes,
	}
	return queue
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
	if q.sealed {
		return Reservation[T]{}, ErrClosed
	}
	if q.pendingFrames >= q.maxFrames || exceeds(q.pendingBytes, estimatedBytes, q.maxBytes) {
		return Reservation[T]{}, ErrFull
	}
	q.pendingFrames++
	q.pendingBytes += estimatedBytes
	q.tracker.addPending(1, estimatedBytes)
	q.updateWatermarkLocked()
	return Reservation[T]{queue: q, estimatedBytes: estimatedBytes, active: true}, nil
}

// Commit reconciles the estimate against the encoded size and publishes the
// item. It cannot succeed after Queue.Close has linearized.
func (r *Reservation[T]) Commit(value T, actualBytes int64) error {
	return r.CommitWithCompletion(value, actualBytes, nil)
}

// CommitWithCompletion reconciles the estimate, publishes the item, and
// attaches a completion notification without changing the legacy Commit path.
func (r *Reservation[T]) CommitWithCompletion(value T, actualBytes int64, completion Completion) error {
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
	if q.sealed {
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
	q.tracker.addPending(0, delta)
	q.updateWatermarkLocked()
	q.notifyWritableLocked()
	item := Item[T]{
		Value:      value,
		queue:      q,
		bytes:      actualBytes,
		active:     true,
		completion: completion,
	}
	select {
	case q.items <- item:
		q.tracker.recordAccepted(actualBytes)
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
		q.tracker.addWaiting(-1)
		q.mu.Unlock()
	}()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q.mu.Lock()
		if q.sealed {
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
			q.tracker.addWaiting(1)
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

// Done records a successful transport write and releases the item's capacity.
func (i *Item[T]) Done() {
	i.Complete(nil)
}

// Complete records the transport write result and releases the item's frame
// and exact encoded-byte capacity. Only the first call has an effect.
func (i *Item[T]) Complete(err error) {
	outcome := writeSucceeded
	if err != nil {
		outcome = writeFailed
	}
	i.complete(err, outcome)
}

func (i *Item[T]) complete(err error, outcome writeOutcome) {
	if i == nil || i.queue == nil {
		return
	}
	queue := i.queue
	queue.mu.Lock()
	released := false
	var value T
	var completion Completion
	if i.active {
		value = i.Value
		completion = i.completion
		queue.recordDrainErrorLocked(err)
		queue.tracker.recordWrite(outcome, i.bytes)
		i.active = false
		released = true
	}
	if released && completion == nil {
		// Preserve the single-lock legacy completion path. Only callers that
		// explicitly request a receipt pay for ordering its notification before
		// the final drain signal.
		queue.releaseLocked(i.bytes)
		queue.mu.Unlock()
		if queue.finalize != nil {
			queue.finalize(value)
		}
		return
	}
	queue.mu.Unlock()
	if !released {
		return
	}
	var firstPanic any
	if completion != nil {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					firstPanic = recovered
				}
			}()
			completion.Complete(err)
		}()
	}
	queue.mu.Lock()
	queue.releaseLocked(i.bytes)
	queue.mu.Unlock()
	if queue.finalize != nil {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil && firstPanic == nil {
					firstPanic = recovered
				}
			}()
			queue.finalize(value)
		}()
	}
	if firstPanic != nil {
		panic(firstPanic)
	}
}

// Seal rejects future reservations while preserving all accepted items for the
// writer. It is the admission boundary used by graceful outbound drain.
func (q *Queue[T]) Seal() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if !q.sealed {
		q.sealed = true
		q.notifyAllWaitersLocked()
	}
	q.mu.Unlock()
}

// WaitDrained waits for reservations, queued items, and writer-owned items to
// complete. It returns the first transport write error recorded by the queue.
func (q *Queue[T]) WaitDrained(ctx context.Context) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		q.mu.Lock()
		if q.pendingFrames == 0 {
			err := q.drainErr
			q.mu.Unlock()
			return err
		}
		if q.drainedChanged == nil {
			q.drainedChanged = make(chan struct{})
		}
		changed := q.drainedChanged
		q.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			q.mu.Lock()
			if q.pendingFrames == 0 {
				err := q.drainErr
				q.mu.Unlock()
				return err
			}
			q.mu.Unlock()
			return ctx.Err()
		}
	}
}

// Close rejects future reservations and releases every item still queued.
// An item already owned by a writer remains pending until that writer calls
// Complete, so snapshots continue to reflect the true retained bytes.
func (q *Queue[T]) Close() {
	q.CloseWithError(ErrClosed)
}

// CloseWithError terminally closes the queue and completes queued items with
// err. A nil error is normalized to ErrClosed because discarded writes cannot
// be reported as successful.
func (q *Queue[T]) CloseWithError(err error) {
	if q == nil {
		return
	}
	if err == nil {
		err = ErrClosed
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.sealed = true
	q.closed = true
	q.setAboveHighWatermarkLocked(false)
	q.tracker.closeQueue()
	q.notifyAllWaitersLocked()
	drained := make([]Item[T], 0, len(q.items))
	for {
		select {
		case item := <-q.items:
			drained = append(drained, item)
		default:
			q.mu.Unlock()
			completeAll(drained, err, writeDropped)
			return
		}
	}
}

// completeAll completes every item even if a user-supplied internal callback
// panics, then propagates the first panic after queue accounting is consistent.
func completeAll[T any](items []Item[T], err error, outcome writeOutcome) {
	var firstPanic any
	for index := range items {
		func(item *Item[T]) {
			defer func() {
				if recovered := recover(); recovered != nil && firstPanic == nil {
					firstPanic = recovered
				}
			}()
			item.complete(err, outcome)
		}(&items[index])
	}
	if firstPanic != nil {
		panic(firstPanic)
	}
}

// Snapshot returns current limits, accounting, and hysteresis state.
func (q *Queue[T]) Snapshot() Snapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	return Snapshot{
		Closed:             q.sealed,
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
	q.tracker.addPending(-1, -bytes)
	q.updateWatermarkLocked()
	q.notifyWritableLocked()
	if q.pendingFrames == 0 && q.drainedChanged != nil {
		close(q.drainedChanged)
		q.drainedChanged = nil
	}
}

func (q *Queue[T]) writableLocked() bool {
	return !q.sealed &&
		!q.aboveHighWatermark &&
		q.pendingFrames < q.maxFrames &&
		q.pendingBytes < q.maxBytes
}

func (q *Queue[T]) recordDrainErrorLocked(err error) {
	if err != nil && q.drainErr == nil {
		q.drainErr = err
	}
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
	if q.closed {
		q.setAboveHighWatermarkLocked(false)
		return
	}
	if q.aboveHighWatermark {
		if q.pendingBytes <= q.lowWatermarkBytes {
			q.setAboveHighWatermarkLocked(false)
		}
		return
	}
	if q.pendingBytes >= q.highWatermarkBytes {
		q.setAboveHighWatermarkLocked(true)
	}
}

func (q *Queue[T]) setAboveHighWatermarkLocked(above bool) {
	if q.aboveHighWatermark == above {
		return
	}
	q.aboveHighWatermark = above
	if above {
		q.tracker.addSaturated(1)
	} else {
		q.tracker.addSaturated(-1)
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
