package tcp

import (
	"sync/atomic"

	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/internal/outbound"
)

const outboundBufferActive uint32 = 0

var _ core.OutboundBufferAllocator = (*session)(nil)

type sessionOutboundBuffer struct {
	state       atomic.Uint32
	session     *session
	reservation outbound.Reservation[outboundFrame]
	payload     []byte
}

func (s *session) ReserveOutboundBuffer(size int) (core.OutboundBuffer, error) {
	if size < 0 {
		return nil, core.ErrInvalidOutboundBuffer
	}
	select {
	case <-s.done:
		return nil, core.ErrClosed
	default:
	}
	headerSize, ok := segmentedHeaderSize(s.server.config.Codec)
	if !ok {
		return nil, core.ErrOutboundBufferUnsupported
	}
	if err := validateSegmentedPayloadSize(s.server.config.Codec, size); err != nil {
		return nil, err
	}
	reservation, err := s.sendQueue.Reserve(outbound.EstimateBytes(size, int64(headerSize)))
	if err != nil {
		return nil, s.outboundError(err)
	}
	reservationTransferred := false
	defer func() {
		if !reservationTransferred {
			reservation.Cancel()
		}
	}()
	pool := s.outboundPayloadPool()
	payload := pool.Get(size)
	reservationTransferred = true
	return &sessionOutboundBuffer{
		session:     s,
		reservation: reservation,
		payload:     payload,
	}, nil
}

func (b *sessionOutboundBuffer) Bytes() []byte {
	if b == nil || b.state.Load() != outboundBufferActive {
		return nil
	}
	return b.payload[:len(b.payload):len(b.payload)]
}

func (b *sessionOutboundBuffer) Commit(msg core.Message) error {
	if b == nil || !b.state.CompareAndSwap(outboundBufferActive, 1) {
		return core.ErrInvalidOutboundBuffer
	}
	session := b.session
	payload := b.payload
	pool := session.outboundPayloadPool()
	b.payload = nil
	payloadOwned := true
	defer func() {
		if payloadOwned {
			b.reservation.Cancel()
			pool.Put(payload)
		}
		b.reservation = outbound.Reservation[outboundFrame]{}
		b.session = nil
	}()
	msg.Data = payload
	frame, err := session.segmentedFrame(msg, payload)
	if err != nil {
		return err
	}
	if err := b.reservation.Commit(frame, frame.wireBytes()); err != nil {
		payloadOwned = false
		frame.release()
		return session.outboundError(err)
	}
	payloadOwned = false
	return nil
}

func (b *sessionOutboundBuffer) Release() {
	if b == nil || !b.state.CompareAndSwap(outboundBufferActive, 1) {
		return
	}
	b.reservation.Cancel()
	b.session.outboundPayloadPool().Put(b.payload)
	b.payload = nil
	b.reservation = outbound.Reservation[outboundFrame]{}
	b.session = nil
}
