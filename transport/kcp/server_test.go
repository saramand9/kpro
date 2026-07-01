package kcp

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
	kcpgo "github.com/xtaci/kcp-go"
)

func TestKCPEchoDispatch(t *testing.T) {
	server, addr, stop := startKCPTestServer(t, Config{
		Addr:         freeKCPTestAddr(t),
		Protocol:     protocol.KProV1(),
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}, func(ctx core.Context) error {
		if ctx.Transport() != core.TransportKCP {
			t.Fatalf("transport = %s, want %s", ctx.Transport(), core.TransportKCP)
		}
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	})
	defer stop()

	conn := dialKCPTest(t, addr)
	defer conn.Close()

	want := core.Message{ID: core.MsgEcho, Seq: 7, Data: []byte("hello kcp")}
	writeKCPTestMessage(t, conn, want)
	got := readKCPTestMessage(t, conn)
	if got.ID != want.ID || got.Seq != want.Seq || string(got.Data) != string(want.Data) {
		t.Fatalf("response = id=%d seq=%d data=%q", got.ID, got.Seq, string(got.Data))
	}
	waitForKCPActiveCount(t, server, 1)
}

func TestKCPHeartbeatBypassesRouter(t *testing.T) {
	var handled atomic.Int32
	_, addr, stop := startKCPTestServer(t, Config{
		Addr:         freeKCPTestAddr(t),
		Protocol:     protocol.KProV1(),
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
	}, func(core.Context) error {
		handled.Add(1)
		return nil
	})
	defer stop()

	conn := dialKCPTest(t, addr)
	defer conn.Close()

	writeKCPTestMessage(t, conn, core.Message{ID: core.MsgHeartbeat, Seq: 9})
	got := readKCPTestMessage(t, conn)
	if got.ID != core.MsgPong || got.Seq != 9 {
		t.Fatalf("heartbeat response = id=%d seq=%d", got.ID, got.Seq)
	}
	if handled.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", handled.Load())
	}
}

func TestKCPOversizedFrameClosesWithoutDispatch(t *testing.T) {
	var handled atomic.Int32
	server, addr, stop := startKCPTestServer(t, Config{
		Addr:         freeKCPTestAddr(t),
		Protocol:     protocol.KProV1(),
		MaxFrameSize: 1,
		ReadTimeout:  time.Second,
		WriteTimeout: 50 * time.Millisecond,
	}, func(core.Context) error {
		handled.Add(1)
		return nil
	})
	defer stop()

	conn := dialKCPTest(t, addr)
	defer conn.Close()

	writeKCPTestMessage(t, conn, core.Message{ID: core.MsgEcho, Seq: 1, Data: []byte("xx")})
	assertKCPConnectionClosed(t, conn)
	waitForKCPActiveCount(t, server, 0)
	if handled.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", handled.Load())
	}
}

func startKCPTestServer(t *testing.T, config Config, handler core.Handler) (*Server, string, func()) {
	t.Helper()
	server := New(config)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(ctx, handler)
	}()
	time.Sleep(50 * time.Millisecond)

	stop := func() {
		cancel()
		_ = server.Shutdown(context.Background())
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("kcp server stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("kcp server did not stop")
		}
	}
	return server, config.Addr, stop
}

func freeKCPTestAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen free udp port: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().String()
}

func dialKCPTest(t *testing.T, addr string) *kcpgo.UDPSession {
	t.Helper()
	conn, err := kcpgo.DialWithOptions(addr, nil, 0, 0)
	if err != nil {
		t.Fatalf("dial kcp: %v", err)
	}
	applySessionConfig(conn, normalizeConfig(Config{}))
	return conn
}

func writeKCPTestMessage(t *testing.T, conn *kcpgo.UDPSession, msg core.Message) {
	t.Helper()
	frame, err := protocol.KProV1().Encode(msg)
	if err != nil {
		t.Fatalf("encode message: %v", err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write kcp message: %v", err)
	}
}

func readKCPTestMessage(t *testing.T, conn *kcpgo.UDPSession) core.Message {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	result, err := protocol.KProV1().Read(conn, 64*1024)
	if err != nil {
		t.Fatalf("read kcp message: %v", err)
	}
	return result.Message
}

func assertKCPConnectionClosed(t *testing.T, conn *kcpgo.UDPSession) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if n, err := conn.Read(buf[:]); err == nil {
		t.Fatalf("read %d bytes from kcp connection, want close/error", n)
	}
}

func waitForKCPActiveCount(t *testing.T, server *Server, want int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := atomic.LoadInt64(&server.active); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active kcp sessions = %d, want %d", atomic.LoadInt64(&server.active), want)
}
