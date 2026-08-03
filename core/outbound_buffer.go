// File: core/outbound_buffer.go
// Purpose: Defines the optional transport-owned outbound buffer capability.

package core

import "errors"

// OutboundBuffer is a session-owned payload reservation. Callers fill Bytes,
// then transfer the reservation to the session with Commit. Commit uses the
// reserved Bytes as the message payload; Message.Data is ignored, while the
// remaining Message fields describe the frame. Release abandons an
// uncommitted reservation and is safe to defer. Commit and Release consume the
// buffer; callers must not retain or mutate Bytes after either call.
// OutboundBuffer is not safe for concurrent use.
//
// The capability is optional so the small Session contract and third-party
// transports remain compatible.
type OutboundBuffer interface {
	Bytes() []byte
	Commit(Message) error
	Release()
}

// OutboundBufferAllocator is implemented by sessions that can reserve queue
// capacity and payload storage before an application encodes a response. A
// failed reservation returns a nil buffer.
type OutboundBufferAllocator interface {
	ReserveOutboundBuffer(size int) (OutboundBuffer, error)
}

// SessionReserveOutboundBuffer reserves an owned outbound payload when the
// session exposes the optional capability. The returned bool is false when the
// transport does not support the fast path. A successful reservation must be
// committed or released by the caller.
func SessionReserveOutboundBuffer(session Session, size int) (buffer OutboundBuffer, supported bool, err error) {
	if session == nil {
		return nil, false, nil
	}
	allocator, ok := session.(OutboundBufferAllocator)
	if !ok {
		return nil, false, nil
	}
	buffer, err = allocator.ReserveOutboundBuffer(size)
	if errors.Is(err, ErrOutboundBufferUnsupported) {
		return nil, false, nil
	}
	return buffer, true, err
}
