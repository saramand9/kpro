// File: core/outbound_state.go
// Purpose: Defines the optional outbound flow-control view exposed by message sessions.

package core

import "context"

// OutboundState is a point-in-time view of a session's pending writes.
// PendingWrites and PendingBytes include reservations being encoded, queued
// writes, and a write currently owned by the transport writer.
type OutboundState struct {
	Closed             bool
	Writable           bool
	AboveHighWatermark bool
	PendingWrites      int
	MaxPendingWrites   int
	PendingBytes       int64
	MaxPendingBytes    int64
	WaitingWriters     int
	MaxWaitingWriters  int
	LowWatermarkBytes  int64
	HighWatermarkBytes int64
}

// OutboundStateProvider is the optional capability implemented by sessions
// that expose outbound flow-control state. Session remains a small compatible
// interface for third-party transports.
type OutboundStateProvider interface {
	OutboundState() OutboundState
}

// OutboundWritableWaiter is the optional capability implemented by sessions
// that support context-cancelable waiting for advisory outbound writability.
type OutboundWritableWaiter interface {
	WaitOutboundWritable(context.Context) error
}

// SessionOutboundState returns the current outbound state when session exposes
// the optional capability. Writable is an advisory high/low-watermark signal;
// Send remains authoritative and can still reject at a hard count or byte cap.
func SessionOutboundState(session Session) (OutboundState, bool) {
	if session == nil {
		return OutboundState{}, false
	}
	provider, ok := session.(OutboundStateProvider)
	if !ok {
		return OutboundState{}, false
	}
	return provider.OutboundState(), true
}

// SessionWaitOutboundWritable waits when session exposes the optional
// capability. A nil context means context.Background. Success is advisory and
// does not reserve capacity for the caller's next Send.
func SessionWaitOutboundWritable(ctx context.Context, session Session) (supported bool, err error) {
	if session == nil {
		return false, nil
	}
	waiter, ok := session.(OutboundWritableWaiter)
	if !ok {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return true, waiter.WaitOutboundWritable(ctx)
}
