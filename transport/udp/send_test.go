package udp

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

type blockingPacketCodec struct {
	codec.PacketCodec
	started chan struct{}
	release chan struct{}
}

func TestSendWithReceiptCompletesOnWriterResult(t *testing.T) {
	server := newUDPOutboundTestServer(t, Config{SendQueueSize: 1})
	session := newUDPTestPacketSession(server, "127.0.0.1:10000")
	receipt, supported, err := core.SessionSendWithReceipt(session, core.Message{ID: 1, Data: []byte("udp")})
	if err != nil || !supported || receipt == nil {
		t.Fatalf("SessionSendWithReceipt() = (%p, %v, %v)", receipt, supported, err)
	}
	select {
	case <-receipt.Done():
		t.Fatal("receipt completed at queue admission")
	default:
	}
	item := <-session.sendQueue.Items()
	item.Done()
	if err := receipt.Wait(context.Background()); err != nil {
		t.Fatalf("receipt error = %v", err)
	}
	if supported, err := core.SessionDrainOutbound(nil, session); !supported || err != nil {
		t.Fatalf("SessionDrainOutbound() = (%v, %v)", supported, err)
	}
	if receipt, err := session.SendWithReceipt(core.Message{}); receipt != nil || !errors.Is(err, core.ErrClosed) {
		t.Fatalf("SendWithReceipt() after drain = (%p, %v)", receipt, err)
	}
}

func TestDatagramSizeErrorPreservesQueuedReply(t *testing.T) {
	// Some platforms have no distinct packet-size error.
	if error(errMessageTooLong) == nil {
		t.Skip("platform does not identify datagrams exceeding the path limit")
	}
	peer := listenUDPTestConn(t)
	defer peer.Close()
	server := newUDPOutboundTestServer(t, Config{SendQueueSize: 2, MaxPendingBytes: 128 << 10})
	s := newUDPTestPacketSession(server, peer.LocalAddr().String())
	defer s.Close()
	server.sessions[s.key] = s
	oversize, err := s.SendWithReceipt(core.Message{ID: 1, Data: make([]byte, 64<<10)})
	if err != nil {
		t.Fatalf("queue first packet: %v", err)
	}
	reply, err := s.SendWithReceipt(core.Message{ID: 2, Seq: 3, Data: []byte("reply")})
	if err != nil {
		t.Fatalf("queue second packet: %v", err)
	}
	go s.writeLoop()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := oversize.Wait(ctx); !errors.Is(err, errMessageTooLong) {
		t.Fatalf("first receipt = %v, want datagram size error", err)
	}
	if err := reply.Wait(ctx); err != nil || s.OutboundState().Closed {
		t.Fatalf("second receipt = %v, session closed = %t", err, s.OutboundState().Closed)
	}
	if current, ok := server.getOrCreateSession(s.remote); !ok || current != s {
		t.Fatal("the next datagram would be assigned to a different virtual session")
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var buf [128]byte
	n, _, err := peer.ReadFromUDP(buf[:])
	if err != nil {
		t.Fatal(err)
	}
	msg, err := codec.KProV1().DecodePacket(buf[:n])
	if err != nil || msg.ID != 2 || msg.Seq != 3 || string(msg.Data) != "reply" {
		t.Fatalf("received packet = %+v, error = %v", msg, err)
	}
	if err := s.DrainOutbound(ctx); !errors.Is(err, errMessageTooLong) {
		t.Fatalf("drain error = %v, want the failed datagram's write result", err)
	}
}

func TestClosedSocketFailsRemainingUDPReceipts(t *testing.T) {
	server := newUDPOutboundTestServer(t, Config{SendQueueSize: 2})
	s := newUDPTestPacketSession(server, "127.0.0.1:10000")
	defer s.Close()
	first, err := s.SendWithReceipt(core.Message{ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SendWithReceipt(core.Message{ID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.udpConn().Close(); err != nil {
		t.Fatal(err)
	}
	s.writeLoop()
	if err := first.Wait(t.Context()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("first receipt = %v, want socket error", err)
	}
	if err := second.Wait(t.Context()); !errors.Is(err, core.ErrClosed) || !s.OutboundState().Closed {
		t.Fatalf("pending receipt = %v, session closed = %t", err, s.OutboundState().Closed)
	}
}

func (c *blockingPacketCodec) EncodePacket(msg core.Message) ([]byte, error) {
	close(c.started)
	<-c.release
	return c.PacketCodec.EncodePacket(msg)
}

func TestSendCannotCommitAfterConcurrentClose(t *testing.T) {
	blocking := &blockingPacketCodec{
		PacketCodec: codec.KProV1(),
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	server := newUDPOutboundTestServer(t, Config{Codec: blocking, SendQueueSize: 1})
	session := newUDPTestPacketSession(server, "127.0.0.1:10001")
	server.mu.Lock()
	server.sessions[session.key] = session
	server.mu.Unlock()

	errCh := make(chan error, 1)
	go func() { errCh <- session.Send(core.Message{}) }()
	<-blocking.started
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	close(blocking.release)
	if err := <-errCh; !errors.Is(err, core.ErrClosed) {
		t.Fatalf("Send() error = %v, want %v", err, core.ErrClosed)
	}
	if snapshot := session.sendQueue.Snapshot(); !snapshot.Closed || snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("closed outbound snapshot = %+v", snapshot)
	}
}

func TestSendEnforcesPendingByteLimit(t *testing.T) {
	server := newUDPOutboundTestServer(t, Config{SendQueueSize: 2, MaxPendingBytes: 25})
	session := newUDPTestPacketSession(server, "127.0.0.1:10002")
	server.mu.Lock()
	server.sessions[session.key] = session
	server.mu.Unlock()

	if err := session.Send(core.Message{}); err != nil {
		t.Fatal(err)
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 1 || snapshot.PendingBytes != codec.FrameHeaderSize || !snapshot.AboveHighWatermark {
		t.Fatalf("outbound snapshot = %+v", snapshot)
	}
	if err := session.Send(core.Message{}); !errors.Is(err, core.ErrSendQueueFull) {
		t.Fatalf("second Send() error = %v, want %v", err, core.ErrSendQueueFull)
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.Closed || snapshot.PendingFrames != 1 || snapshot.PendingBytes != codec.FrameHeaderSize {
		t.Fatalf("UDP queue-full policy changed: %+v", snapshot)
	}
	_ = session.Close()
}

func TestPendingByteConfigDefaultsAndValidation(t *testing.T) {
	server := New(Config{MaxPacketSize: 100, SendQueueSize: 2})
	if want := int64(200); server.config.MaxPendingBytes != want {
		t.Fatalf("MaxPendingBytes = %d, want %d", server.config.MaxPendingBytes, want)
	}
	if err := New(Config{MaxPendingBytes: -1}).Validate(); err == nil || !strings.Contains(err.Error(), "MaxPendingBytes") {
		t.Fatalf("negative MaxPendingBytes validation error = %v", err)
	}
}

func TestWaitOutboundWritableCapabilityAndBound(t *testing.T) {
	server := newUDPOutboundTestServer(t, Config{SendQueueSize: 1})
	session := newUDPTestPacketSession(server, "127.0.0.1:10003")
	if supported, err := core.SessionWaitOutboundWritable(nil, session); !supported || err != nil {
		t.Fatalf("immediate SessionWaitOutboundWritable() = (%v, %v)", supported, err)
	}
	reservation, err := session.sendQueue.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Cancel()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- session.WaitOutboundWritable(ctx) }()
	waitForOutboundWaiters(t, session.OutboundState, 1)
	if state := session.OutboundState(); state.WaitingWriters != 1 || state.MaxWaitingWriters != 1 {
		t.Fatalf("outbound waiter state = %+v", state)
	}
	if err := session.WaitOutboundWritable(context.Background()); !errors.Is(err, core.ErrTooManyOutboundWaiters) {
		t.Fatalf("overflow WaitOutboundWritable() error = %v", err)
	}
	if session.OutboundState().Closed {
		t.Fatal("waiter overload closed UDP session")
	}
	cancel()
	if err := waitOutboundResult(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled WaitOutboundWritable() error = %v", err)
	}
	session.sendQueue.Close()
	if supported, err := core.SessionWaitOutboundWritable(context.Background(), session); !supported || !errors.Is(err, core.ErrClosed) {
		t.Fatalf("closed SessionWaitOutboundWritable() = (%v, %v)", supported, err)
	}
}

func waitForOutboundWaiters(t *testing.T, state func() core.OutboundState, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if state().WaitingWriters == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiting writers = %d, want %d", state().WaitingWriters, want)
}

func waitOutboundResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for outbound result")
		return nil
	}
}

func newUDPOutboundTestServer(t *testing.T, config Config) *Server {
	t.Helper()
	server := New(config)
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	server.conn = conn
	server.mu.Unlock()
	t.Cleanup(func() { _ = server.Shutdown(nil) })
	return server
}
