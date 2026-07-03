package tcp

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/codec"
	"github.com/saramand9/bignet/kpro/core"
)

func TestPartialFrameReadTimeoutClosesConnection(t *testing.T) {
	var handled atomic.Int32
	server, addr, stop := startTCPTestServer(t, Config{
		Addr:         freeTCPTestAddr(t),
		Codec:        codec.KProV1(),
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	}, func(core.Context) error {
		handled.Add(1)
		return nil
	})
	defer stop()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial tcp server: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte{0x4b, 0x50}); err != nil {
		t.Fatalf("write partial frame: %v", err)
	}
	assertTCPConnectionClosed(t, conn)
	waitForTCPActiveCount(t, server, 0)
	if got := handled.Load(); got != 0 {
		t.Fatalf("handler calls = %d, want 0", got)
	}
}

func TestOversizedFrameClosesConnectionWithoutDispatch(t *testing.T) {
	var handled atomic.Int32
	server, addr, stop := startTCPTestServer(t, Config{
		Addr:         freeTCPTestAddr(t),
		Codec:        codec.KProV1(),
		MaxFrameSize: 1,
		ReadTimeout:  time.Second,
		WriteTimeout: 50 * time.Millisecond,
	}, func(core.Context) error {
		handled.Add(1)
		return nil
	})
	defer stop()

	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial tcp server: %v", err)
	}
	defer conn.Close()

	frame, err := codec.KProV1().Encode(core.Message{ID: testMsgEcho, Seq: 1, Data: []byte("xx")})
	if err != nil {
		t.Fatalf("encode oversized frame: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write oversized frame: %v", err)
	}
	assertTCPConnectionClosed(t, conn)
	waitForTCPActiveCount(t, server, 0)
	if got := handled.Load(); got != 0 {
		t.Fatalf("handler calls = %d, want 0", got)
	}
}

func startTCPTestServer(t *testing.T, config Config, handler core.Handler) (*Server, string, func()) {
	t.Helper()
	server := New(config)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(ctx, handler)
	}()
	waitForTCPTestServer(t, config.Addr)

	stop := func() {
		cancel()
		_ = server.Shutdown(context.Background())
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("tcp server stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("tcp server did not stop")
		}
	}
	return server, config.Addr, stop
}

func freeTCPTestAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free tcp port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitForTCPTestServer(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 20*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tcp server did not start on %s: %v", addr, lastErr)
}

func assertTCPConnectionClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	if err == nil {
		t.Fatalf("read %d bytes from connection, want close/error", n)
	}
	if err != io.EOF {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			t.Fatalf("connection did not close before read deadline")
		}
	}
}

func waitForTCPActiveCount(t *testing.T, server *Server, want int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := atomic.LoadInt64(&server.active); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active tcp sessions = %d, want %d", atomic.LoadInt64(&server.active), want)
}
