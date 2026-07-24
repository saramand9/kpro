// File: test/ratelimit_transport_test.go
// Purpose: Exercises KPro protocol and transport behavior through integration tests.

package test

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/middleware"
	"github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/tcp"
	"github.com/saramand9/kpro/transport/udp"
	wstransport "github.com/saramand9/kpro/transport/websocket"
	"golang.org/x/time/rate"
)

func TestTCPConnRateLimit(t *testing.T) {
	addr := "127.0.0.1:" + freeTCPPort(t)
	stop := startRateLimitedCoreTransport(t, tcp.New(tcp.Config{
		Addr:  addr,
		Codec: codec.KProV1(),
	}))
	defer stop()

	conn1 := dialTCP(t, addr)
	defer conn1.Close()

	writeMessage(t, conn1, core.Message{ID: testMsgEcho, Seq: 1, Data: []byte("tcp-1")})
	assertEchoMessage(t, readMessage(t, conn1), 1, "tcp-1")

	writeMessage(t, conn1, core.Message{ID: testMsgEcho, Seq: 2, Data: []byte("tcp-2")})
	assertRateLimitedMessage(t, readMessage(t, conn1), 2)

	conn2 := dialTCP(t, addr)
	defer conn2.Close()

	writeMessage(t, conn2, core.Message{ID: testMsgEcho, Seq: 1, Data: []byte("tcp-other")})
	assertEchoMessage(t, readMessage(t, conn2), 1, "tcp-other")
}

func TestUDPConnRateLimit(t *testing.T) {
	addr := "127.0.0.1:" + freeUDPPort(t)
	stop := startRateLimitedCoreTransport(t, udp.New(udp.Config{
		Addr:          addr,
		Codec:         codec.KProV1(),
		MaxPacketSize: 2048,
	}))
	defer stop()

	conn1 := dialRateLimitUDPClient(t, addr)
	defer conn1.Close()

	writeUDPMessage(t, conn1, core.Message{ID: testMsgEcho, Seq: 1, Data: []byte("udp-1")})
	assertEchoMessage(t, readUDPMessage(t, conn1), 1, "udp-1")

	writeUDPMessage(t, conn1, core.Message{ID: testMsgEcho, Seq: 2, Data: []byte("udp-2")})
	assertRateLimitedMessage(t, readUDPMessage(t, conn1), 2)

	conn2 := dialRateLimitUDPClient(t, addr)
	defer conn2.Close()

	writeUDPMessage(t, conn2, core.Message{ID: testMsgEcho, Seq: 1, Data: []byte("udp-other")})
	assertEchoMessage(t, readUDPMessage(t, conn2), 1, "udp-other")
}

func TestWebSocketConnRateLimit(t *testing.T) {
	addr := "127.0.0.1:" + freeTCPPort(t)
	stop := startRateLimitedCoreTransport(t, wstransport.New(wstransport.Config{
		Addr:  addr,
		Path:  "/ws",
		Codec: codec.KProV1(),
	}))
	defer stop()

	conn1, reader1 := dialRateLimitWebSocket(t, "ws://"+addr+"/ws")
	defer conn1.Close()

	writeWebSocketMessage(t, conn1, core.Message{ID: testMsgEcho, Seq: 1, Data: []byte("ws-1")})
	assertEchoMessage(t, readWebSocketMessage(t, reader1), 1, "ws-1")

	writeWebSocketMessage(t, conn1, core.Message{ID: testMsgEcho, Seq: 2, Data: []byte("ws-2")})
	assertRateLimitedMessage(t, readWebSocketMessage(t, reader1), 2)

	conn2, reader2 := dialRateLimitWebSocket(t, "ws://"+addr+"/ws")
	defer conn2.Close()

	writeWebSocketMessage(t, conn2, core.Message{ID: testMsgEcho, Seq: 1, Data: []byte("ws-other")})
	assertEchoMessage(t, readWebSocketMessage(t, reader2), 1, "ws-other")
}

func startRateLimitedCoreTransport(t *testing.T, srv transport.Server) func() {
	t.Helper()

	router := core.NewRouter(core.RouterConfig{
		SyncQueueSize:         8,
		AsyncQueueSize:        8,
		MaxInflightPerSession: 8,
	})
	router.Use(middleware.ConnRateLimit(middleware.RateLimitConfig{
		Rate:  rate.Limit(0.001),
		Burst: 1,
	}))
	router.Handle(testMsgEcho, func(ctx core.Context) error {
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx, router.Dispatch)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("%s rate-limit server exited early: %v", srv.Name(), err)
	case <-time.After(50 * time.Millisecond):
	}

	return func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdownCancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = router.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s rate-limit server did not stop", srv.Name())
		}
	}
}

func dialRateLimitUDPClient(t *testing.T, addr string) *net.UDPConn {
	t.Helper()

	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve UDP addr: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		t.Fatalf("dial UDP addr: %v", err)
	}
	return conn
}

func writeUDPMessage(t *testing.T, conn *net.UDPConn, msg core.Message) {
	t.Helper()

	packet, err := codec.KProV1().EncodePacket(msg)
	if err != nil {
		t.Fatalf("encode UDP packet: %v", err)
	}
	if _, err := conn.Write(packet); err != nil {
		t.Fatalf("write UDP packet: %v", err)
	}
}

func readUDPMessage(t *testing.T, conn *net.UDPConn) core.Message {
	t.Helper()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read UDP packet: %v", err)
	}
	msg, err := codec.KProV1().DecodePacket(buf[:n])
	if err != nil {
		t.Fatalf("decode UDP packet: %v", err)
	}
	return msg
}

func dialRateLimitWebSocket(t *testing.T, rawURL string) (net.Conn, *bufio.Reader) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, reader, err := dialRawWebSocket(rawURL)
		if err == nil {
			return conn, reader
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("dial websocket %s: %v", rawURL, lastErr)
	return nil, nil
}

func writeWebSocketMessage(t *testing.T, conn net.Conn, msg core.Message) {
	t.Helper()

	packet, err := codec.KProV1().EncodePacket(msg)
	if err != nil {
		t.Fatalf("encode websocket packet: %v", err)
	}
	if err := writeRawWebSocketBinary(conn, packet); err != nil {
		t.Fatalf("write websocket packet: %v", err)
	}
}

func readWebSocketMessage(t *testing.T, reader *bufio.Reader) core.Message {
	t.Helper()

	packet, err := readRawWebSocketBinary(reader, 64*1024)
	if err != nil {
		t.Fatalf("read websocket packet: %v", err)
	}
	msg, err := codec.KProV1().DecodePacket(packet)
	if err != nil {
		t.Fatalf("decode websocket packet: %v", err)
	}
	return msg
}

func assertEchoMessage(t *testing.T, msg core.Message, seq uint64, body string) {
	t.Helper()

	if msg.ID != testMsgEcho || msg.Seq != seq || string(msg.Data) != body {
		t.Fatalf("got message id=%d seq=%d data=%q, want echo seq=%d data=%q",
			msg.ID, msg.Seq, string(msg.Data), seq, body)
	}
}

func assertRateLimitedMessage(t *testing.T, msg core.Message, seq uint64) {
	t.Helper()

	if msg.ID != core.MsgError || msg.Seq != seq || string(msg.Data) != middleware.ErrRateLimited.Error() {
		t.Fatalf("got message id=%d seq=%d data=%q, want rate-limit error seq=%d",
			msg.ID, msg.Seq, string(msg.Data), seq)
	}
}
