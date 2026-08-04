package outbound

import (
	"time"
)

// DrainAvailable moves first into dst and then non-blockingly appends queued
// items in FIFO order, up to maxItems in total. It is intended for the queue's
// single writer after that writer has received first from Items. The returned
// batch always contains first, and the caller must call Done on every returned
// item after the corresponding write attempt.
//
// dst must have length zero, though its capacity may be reused. first becomes
// inactive after the move and must not be used again. maxItems must be
// positive. The item-count limit is strict; callers that also impose a byte
// limit can split the returned local batch without putting items back into the
// queue or changing FIFO order.
func (q *Queue[T]) DrainAvailable(first *Item[T], dst []Item[T], maxItems int) []Item[T] {
	if q == nil {
		panic("outbound: drain from nil queue")
	}
	if maxItems <= 0 {
		panic("outbound: drain max items must be positive")
	}
	if len(dst) != 0 {
		panic("outbound: drain destination must be empty")
	}
	if first == nil || !first.active || first.queue != q {
		panic("outbound: drain requires an active first item from this queue")
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	dst = append(dst, *first)
	*first = Item[T]{}
	for len(dst) < maxItems {
		select {
		case item := <-q.items:
			dst = append(dst, item)
		default:
			return dst
		}
	}
	return dst
}

// DrainAvailableWait is DrainAvailable with a bounded coalescing window. It
// waits only when no additional item is immediately available, then drains
// the remainder without blocking.
func (q *Queue[T]) DrainAvailableWait(first *Item[T], dst []Item[T], maxItems int, wait time.Duration) []Item[T] {
	if wait <= 0 {
		return q.DrainAvailable(first, dst, maxItems)
	}
	if q == nil {
		panic("outbound: drain from nil queue")
	}
	if maxItems <= 0 {
		panic("outbound: drain max items must be positive")
	}
	if len(dst) != 0 {
		panic("outbound: drain destination must be empty")
	}
	if first == nil || !first.active || first.queue != q {
		panic("outbound: drain requires an active first item from this queue")
	}

	q.mu.Lock()
	dst = append(dst, *first)
	*first = Item[T]{}
	for len(dst) < maxItems {
		select {
		case item := <-q.items:
			dst = append(dst, item)
			q.mu.Unlock()
			return q.drainRemaining(dst, maxItems)
		default:
		}
		q.mu.Unlock()
		polls := int(wait / time.Microsecond)
		if polls < 1 {
			polls = 1
		}
		for poll := 0; poll < polls; poll++ {
			select {
			case item := <-q.items:
				dst = append(dst, item)
				return q.drainRemaining(dst, maxItems)
			default:
			}
		}
		return dst
	}
	q.mu.Unlock()
	return dst
}

func (q *Queue[T]) drainRemaining(dst []Item[T], maxItems int) []Item[T] {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(dst) < maxItems {
		select {
		case item := <-q.items:
			dst = append(dst, item)
		default:
			return dst
		}
	}
	return dst
}
