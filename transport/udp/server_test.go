// File: transport/udp/server_test.go
// Purpose: Implements the UDP transport adapter for KPro packet sessions.

package udp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

func TestListenAndServeRejectsSecondStartAndStopsWithNilContext(t *testing.T) {
	server := New(Config{Addr: "127.0.0.1:0"})
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe(nil, func(core.Context) error { return nil }) }()
	select {
	case <-server.Ready():
	case <-time.After(time.Second):
		t.Fatal("UDP Ready() did not close")
	}
	server.mu.Lock()
	started := server.conn != nil
	server.mu.Unlock()
	if !started {
		t.Fatal("UDP Ready() closed without a published socket")
	}
	if err := server.ListenAndServe(context.Background(), nil); !errors.Is(err, core.ErrAlreadyStarted) {
		t.Fatalf("second ListenAndServe() error = %v, want %v", err, core.ErrAlreadyStarted)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("first ListenAndServe() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP server did not stop")
	}
}

func TestUDPRejectsNilHandlerWithoutBinding(t *testing.T) {
	server := New(Config{Addr: "127.0.0.1:0"})
	err := server.ListenAndServe(context.Background(), nil)
	if !errors.Is(err, core.ErrNoHandler) {
		t.Fatalf("ListenAndServe() error = %v, want %v", err, core.ErrNoHandler)
	}
	select {
	case <-server.Ready():
		t.Fatal("Ready() closed after nil handler startup failure")
	default:
	}
	server.mu.Lock()
	conn := server.conn
	server.mu.Unlock()
	if conn != nil {
		t.Fatal("nil handler startup published a UDP socket")
	}
}

func TestPacketSessionSendUsesQueueAndWriteLoop(t *testing.T) {
	serverConn := listenUDPTestConn(t)
	defer serverConn.Close()
	clientConn := listenUDPTestConn(t)
	defer clientConn.Close()

	server := New(Config{
		Codec:         codec.KProV1(),
		SendQueueSize: 1,
	})
	server.conn = serverConn

	remote := clientConn.LocalAddr().(*net.UDPAddr)
	session, ok := server.getOrCreateSession(remote)
	if !ok {
		t.Fatal("create UDP packet session failed")
	}
	defer session.Close()

	msg := core.Message{ID: 123, Seq: 7, Data: []byte("queued")}
	if err := session.Send(msg); err != nil {
		t.Fatalf("send UDP packet through queue: %v", err)
	}

	buf := make([]byte, 128)
	if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set UDP read deadline: %v", err)
	}
	n, _, err := clientConn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("read queued UDP packet: %v", err)
	}
	got, err := codec.KProV1().DecodePacket(buf[:n])
	if err != nil {
		t.Fatalf("decode queued UDP packet: %v", err)
	}
	if got.ID != msg.ID || got.Seq != msg.Seq || string(got.Data) != string(msg.Data) {
		t.Fatalf("queued UDP packet = id=%d seq=%d data=%q, want id=%d seq=%d data=%q", got.ID, got.Seq, string(got.Data), msg.ID, msg.Seq, string(msg.Data))
	}
}

func TestPacketSessionSendQueueFullIsPerSession(t *testing.T) {
	serverConn := listenUDPTestConn(t)
	defer serverConn.Close()

	server := New(Config{
		Codec:         codec.KProV1(),
		SendQueueSize: 1,
	})
	server.conn = serverConn

	a := newUDPTestPacketSession(server, "127.0.0.1:10001")
	defer a.Close()
	b := newUDPTestPacketSession(server, "127.0.0.1:10002")
	defer b.Close()

	if err := a.Send(core.Message{ID: 1, Seq: 1}); err != nil {
		t.Fatalf("first send to session A failed: %v", err)
	}
	if err := a.Send(core.Message{ID: 1, Seq: 2}); !errors.Is(err, core.ErrSendQueueFull) {
		t.Fatalf("second send to full session A = %v, want %v", err, core.ErrSendQueueFull)
	}
	if err := b.Send(core.Message{ID: 1, Seq: 1}); err != nil {
		t.Fatalf("send to independent session B failed: %v", err)
	}

	select {
	case <-a.Done():
		t.Fatal("session A was closed by send queue full")
	default:
	}
	select {
	case <-b.Done():
		t.Fatal("session B was closed unexpectedly")
	default:
	}
}

func TestServerClosesIdleUDPSessions(t *testing.T) {
	serverConn := listenUDPTestConn(t)
	defer serverConn.Close()

	server := New(Config{
		Codec:              codec.KProV1(),
		SendQueueSize:      1,
		SessionIdleTimeout: time.Millisecond,
		CleanupInterval:    time.Hour,
	})
	server.conn = serverConn

	remote, err := net.ResolveUDPAddr("udp", "127.0.0.1:10003")
	if err != nil {
		t.Fatalf("resolve UDP remote: %v", err)
	}
	session, ok := server.getOrCreateSession(remote)
	if !ok {
		t.Fatal("create UDP session failed")
	}
	server.closeIdleSessions(time.Now().Add(time.Second))

	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("idle UDP session was not closed")
	}

	server.mu.Lock()
	_, exists := server.sessions[remote.String()]
	server.mu.Unlock()
	if exists {
		t.Fatal("idle UDP session was not removed from server")
	}
}

func TestQuiescePreservesIdleUDPSessionsUntilShutdown(t *testing.T) {
	serverConn := listenUDPTestConn(t)
	server := New(Config{
		Codec:              codec.KProV1(),
		SendQueueSize:      1,
		SessionIdleTimeout: time.Millisecond,
		CleanupInterval:    time.Hour,
	})
	server.conn = serverConn
	remote := resolveUDPTestAddr(t, "127.0.0.1:10013")
	session, ok := server.getOrCreateSession(remote)
	if !ok {
		t.Fatal("create UDP session failed")
	}

	if err := server.Quiesce(context.Background()); err != nil {
		t.Fatalf("Quiesce() error = %v", err)
	}
	server.closeIdleSessions(time.Now().Add(time.Second))
	select {
	case <-session.Done():
		t.Fatal("idle cleanup closed UDP session after Quiesce")
	default:
	}

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not close quiesced UDP session")
	}
}

func TestServerRejectsNewSessionWhenMaxSessionsReached(t *testing.T) {
	serverConn := listenUDPTestConn(t)
	defer serverConn.Close()

	server := New(Config{
		Codec:         codec.KProV1(),
		MaxSessions:   1,
		SendQueueSize: 1,
	})
	server.conn = serverConn

	firstRemote := resolveUDPTestAddr(t, "127.0.0.1:10004")
	first, ok := server.getOrCreateSession(firstRemote)
	if !ok {
		t.Fatal("create first UDP session failed")
	}
	defer first.Close()

	secondRemote := resolveUDPTestAddr(t, "127.0.0.1:10005")
	if _, ok := server.getOrCreateSession(secondRemote); ok {
		t.Fatal("created UDP session after MaxSessions was reached")
	}
}

func TestServerRejectsNewSessionAfterShutdown(t *testing.T) {
	serverConn := listenUDPTestConn(t)

	server := New(Config{
		Codec:         codec.KProV1(),
		SendQueueSize: 1,
	})
	server.conn = serverConn
	if err := server.Shutdown(nil); err != nil {
		t.Fatalf("shutdown UDP server: %v", err)
	}

	remote := resolveUDPTestAddr(t, "127.0.0.1:10006")
	if _, ok := server.getOrCreateSession(remote); ok {
		t.Fatal("created UDP session after shutdown")
	}
}

func TestPacketSessionSendAfterCloseReturnsErrClosed(t *testing.T) {
	serverConn := listenUDPTestConn(t)
	defer serverConn.Close()

	server := New(Config{
		Codec:         codec.KProV1(),
		SendQueueSize: 1,
	})
	server.conn = serverConn
	session := newUDPTestPacketSession(server, "127.0.0.1:10007")
	if err := session.Close(); err != nil {
		t.Fatalf("close UDP session: %v", err)
	}

	err := session.Send(core.Message{ID: 1, Seq: 1})
	if !errors.Is(err, core.ErrClosed) {
		t.Fatalf("send after close = %v, want %v", err, core.ErrClosed)
	}
}

func TestServerRejectsDatagramLargerThanMaxPacketSize(t *testing.T) {
	packetCodec := codec.KProV1()
	oversizedPrefix, err := packetCodec.EncodePacket(core.Message{ID: 101, Seq: 1, Data: []byte("x")})
	if err != nil {
		t.Fatalf("encode oversized prefix: %v", err)
	}
	validPacket, err := packetCodec.EncodePacket(core.Message{ID: 102, Seq: 2, Data: []byte("y")})
	if err != nil {
		t.Fatalf("encode boundary packet: %v", err)
	}
	if len(validPacket) != len(oversizedPrefix) {
		t.Fatalf("test packet lengths differ: valid=%d oversized-prefix=%d", len(validPacket), len(oversizedPrefix))
	}

	server := New(Config{
		Addr:          "127.0.0.1:0",
		Codec:         packetCodec,
		MaxPacketSize: len(validPacket),
	})
	received := make(chan uint32, 2)
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(context.Background(), func(ctx core.Context) error {
			received <- ctx.Message().ID
			return nil
		})
	}()
	select {
	case <-server.Ready():
	case err := <-errCh:
		t.Fatalf("UDP server stopped before readiness: %v", err)
	case <-time.After(time.Second):
		t.Fatal("UDP server did not become ready")
	}
	t.Cleanup(func() {
		if err := server.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown UDP server: %v", err)
		}
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("UDP server exit: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("UDP server did not stop")
		}
	})

	server.mu.Lock()
	remote := *server.conn.LocalAddr().(*net.UDPAddr)
	server.mu.Unlock()
	client := listenUDPTestConn(t)
	defer client.Close()

	oversized := append(append([]byte(nil), oversizedPrefix...), 0)
	if _, err := client.WriteToUDP(oversized, &remote); err != nil {
		t.Fatalf("write oversized datagram: %v", err)
	}
	if _, err := client.WriteToUDP(validPacket, &remote); err != nil {
		t.Fatalf("write boundary datagram: %v", err)
	}

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case id := <-received:
			if id == 101 {
				t.Fatal("handler received truncated oversized datagram")
			}
			if id == 102 {
				return
			}
			t.Fatalf("handler received unexpected message id %d", id)
		case err := <-errCh:
			t.Fatalf("UDP server stopped while reading datagrams: %v", err)
		case <-deadline.C:
			t.Fatal("boundary-sized datagram was not handled")
		}
	}
}

func listenUDPTestConn(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen UDP test conn: %v", err)
	}
	return conn
}

func resolveUDPTestAddr(t *testing.T, remote string) *net.UDPAddr {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", remote)
	if err != nil {
		t.Fatalf("resolve UDP remote %s: %v", remote, err)
	}
	return addr
}

func newUDPTestPacketSession(server *Server, remote string) *packetSession {
	addr, err := net.ResolveUDPAddr("udp", remote)
	if err != nil {
		panic(err)
	}
	session := &packetSession{
		id:        sessionID(addr),
		key:       addr.String(),
		server:    server,
		remote:    addr,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	session.lastActive.Store(server.activityTick(time.Now()))
	return session
}
