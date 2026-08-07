package transport

import "time"

// OutboundSnapshot is a process-lifetime aggregate of one message transport's
// outbound queues. Current fields include encoding reservations, queued
// writes, and writer-owned writes. Counter fields never use connection IDs,
// peer addresses, or other unbounded dimensions.
//
// A successful write result means the transport's complete write call
// returned successfully. It is not peer acknowledgement or durable delivery.
type OutboundSnapshot struct {
	Sessions          int64
	PendingWrites     int64
	PendingBytes      int64
	WaitingWriters    int64
	SaturatedSessions int64

	AcceptedWrites uint64
	AcceptedBytes  uint64

	SucceededWrites uint64
	SucceededBytes  uint64
	FailedWrites    uint64
	FailedBytes     uint64
	DroppedWrites   uint64
	DroppedBytes    uint64

	DrainSucceeded uint64
	DrainDeadline  uint64
	DrainCanceled  uint64
	DrainFailed    uint64

	DrainDurationTotal time.Duration
	LastDrainDuration  time.Duration
	MaxDrainDuration   time.Duration
}

// OutboundSnapshotter is the optional capability implemented by message
// transports that expose bounded aggregate write and drain statistics.
type OutboundSnapshotter interface {
	OutboundSnapshot() OutboundSnapshot
}

// ComponentOutboundSnapshot discovers aggregate outbound statistics without
// widening Component or MessageServer. Unsupported components return
// supported=false.
func ComponentOutboundSnapshot(component Component) (snapshot OutboundSnapshot, supported bool) {
	if component == nil || isNilComponent(component) {
		return OutboundSnapshot{}, false
	}
	provider, ok := component.(OutboundSnapshotter)
	if !ok {
		return OutboundSnapshot{}, false
	}
	return provider.OutboundSnapshot(), true
}
