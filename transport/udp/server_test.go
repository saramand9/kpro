package udp

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

func TestPacketSessionSendUsesQueueAndWriteLoop(t *testing.T) {
	serverConn := listenUDPTestConn(t)
	defer serverConn.Close()
	clientConn := listenUDPTestConn(t)
	defer clientConn.Close()

	server := New(Config{
		Protocol:      protocol.KProV1(),
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
	got, err := protocol.KProV1().DecodePacket(buf[:n])
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
		Protocol:      protocol.KProV1(),
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
		Protocol:           protocol.KProV1(),
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

func listenUDPTestConn(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen UDP test conn: %v", err)
	}
	return conn
}

func newUDPTestPacketSession(server *Server, remote string) *packetSession {
	addr, err := net.ResolveUDPAddr("udp", remote)
	if err != nil {
		panic(err)
	}
	return &packetSession{
		id:         sessionID(addr),
		key:        addr.String(),
		server:     server,
		remote:     addr,
		sendQueue:  make(chan []byte, server.config.SendQueueSize),
		done:       make(chan struct{}),
		lastActive: time.Now(),
	}
}
