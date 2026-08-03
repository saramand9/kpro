package core

import (
	"errors"
	"net"
	"testing"
)

type outboundBufferTestSession struct {
	buffer OutboundBuffer
	err    error
}

func (s *outboundBufferTestSession) ID() uint64           { return 1 }
func (s *outboundBufferTestSession) Transport() Transport { return TransportTCP }
func (s *outboundBufferTestSession) RemoteAddr() net.Addr { return nil }
func (s *outboundBufferTestSession) Send(Message) error   { return nil }
func (s *outboundBufferTestSession) Close() error         { return nil }
func (s *outboundBufferTestSession) ReserveOutboundBuffer(int) (OutboundBuffer, error) {
	return s.buffer, s.err
}

type outboundBufferStub struct{}

func (*outboundBufferStub) Bytes() []byte        { return nil }
func (*outboundBufferStub) Commit(Message) error { return nil }
func (*outboundBufferStub) Release()             {}

func TestSessionReserveOutboundBufferCapability(t *testing.T) {
	want := &outboundBufferStub{}
	session := &outboundBufferTestSession{buffer: want}
	got, supported, err := SessionReserveOutboundBuffer(session, 4)
	if err != nil || !supported || got != want {
		t.Fatalf("SessionReserveOutboundBuffer() = (%v, %v, %v)", got, supported, err)
	}

	wantErr := errors.New("reserve failed")
	session.err = wantErr
	if _, supported, err := SessionReserveOutboundBuffer(session, 4); !supported || !errors.Is(err, wantErr) {
		t.Fatalf("failed SessionReserveOutboundBuffer() = (supported=%v, err=%v)", supported, err)
	}
}

func TestSessionReserveOutboundBufferUnsupported(t *testing.T) {
	var session Session
	if buffer, supported, err := SessionReserveOutboundBuffer(session, 4); buffer != nil || supported || err != nil {
		t.Fatalf("nil SessionReserveOutboundBuffer() = (%v, %v, %v)", buffer, supported, err)
	}

	session = &outboundBufferTestSession{err: ErrOutboundBufferUnsupported}
	if buffer, supported, err := SessionReserveOutboundBuffer(session, 4); buffer != nil || supported || err != nil {
		t.Fatalf("unsupported allocator SessionReserveOutboundBuffer() = (%v, %v, %v)", buffer, supported, err)
	}
}
