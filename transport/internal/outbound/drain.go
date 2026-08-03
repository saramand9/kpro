package outbound

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
