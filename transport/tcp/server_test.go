package tcp

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
)

func TestTCPRejectsNilHandlerWithoutBinding(t *testing.T) {
	server := New(Config{Addr: freeTCPTestAddr(t)})
	err := server.ListenAndServe(context.Background(), nil)
	if !errors.Is(err, core.ErrNoHandler) {
		t.Fatalf("ListenAndServe() error = %v, want %v", err, core.ErrNoHandler)
	}
	select {
	case <-server.Ready():
		t.Fatal("Ready() closed after nil handler startup failure")
	default:
	}
	server.lifecycleMu.Lock()
	listener := server.ln
	server.lifecycleMu.Unlock()
	if listener != nil {
		t.Fatal("nil handler startup published a TCP listener")
	}
}

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

func TestTCPHandlerPanicIsObservedAndClosesOnlyAffectedSession(t *testing.T) {
	wantErr := &tcpHandlerPanicTestError{marker: "tcp handler panic"}
	observed := make(chan transportapi.HandlerErrorEvent, 1)
	var handled atomic.Int32
	server, addr, stop := startTCPTestServer(t, Config{
		Addr:         freeTCPTestAddr(t),
		Codec:        codec.KProV1(),
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		OnHandlerError: func(event transportapi.HandlerErrorEvent) {
			observed <- event
		},
	}, func(core.Context) error {
		handled.Add(1)
		panic(wantErr)
	})
	defer stop()

	failedConn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial failed tcp session: %v", err)
	}
	defer failedConn.Close()
	failedMessage := core.Message{ID: testMsgEcho, Seq: 23, Data: []byte("panic")}
	writeTCPTestMessage(t, failedConn, failedMessage)

	select {
	case event := <-observed:
		if !errors.Is(event.Err, core.ErrRecovered) {
			t.Fatalf("observed error = %v, want %v", event.Err, core.ErrRecovered)
		}
		if !errors.Is(event.Err, wantErr) {
			t.Fatalf("observed error = %v, want original panic %v", event.Err, wantErr)
		}
		var typedErr *tcpHandlerPanicTestError
		if !errors.As(event.Err, &typedErr) || typedErr != wantErr {
			t.Fatalf("observed typed error = %v, want %v", typedErr, wantErr)
		}
		if event.Transport != core.TransportTCP ||
			event.ConnID == 0 ||
			event.RemoteAddr != failedConn.LocalAddr().String() ||
			event.MessageID != failedMessage.ID ||
			event.Sequence != failedMessage.Seq {
			t.Fatalf("handler error event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("handler panic was not observed")
	}
	assertTCPConnectionClosed(t, failedConn)
	waitForTCPActiveCount(t, server, 0)

	healthyConn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial healthy tcp session after panic: %v", err)
	}
	defer healthyConn.Close()
	writeTCPTestMessage(t, healthyConn, core.Message{ID: core.MsgHeartbeat, Seq: 29})
	_ = healthyConn.SetReadDeadline(time.Now().Add(time.Second))
	result, err := codec.KProV1().Read(healthyConn, DefaultMaxFrameSize)
	if err != nil {
		t.Fatalf("read heartbeat response after panic: %v", err)
	}
	if result.Message.ID != core.MsgPong || result.Message.Seq != 29 || len(result.Message.Data) != 0 {
		t.Fatalf("heartbeat response after panic = %+v", result.Message)
	}
	if got := handled.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
	waitForTCPActiveCount(t, server, 1)
}

func startTCPTestServer(t *testing.T, config Config, handler core.Handler) (*Server, string, func()) {
	t.Helper()
	server := New(config)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(ctx, handler)
	}()
	select {
	case <-server.Ready():
	case <-time.After(time.Second):
		t.Fatal("TCP Ready() did not close")
	}
	server.lifecycleMu.Lock()
	listener := server.ln
	server.lifecycleMu.Unlock()
	if listener == nil {
		t.Fatal("TCP Ready() closed without a published listener")
	}

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

func writeTCPTestMessage(t *testing.T, conn net.Conn, message core.Message) {
	t.Helper()
	frame, err := codec.KProV1().Encode(message)
	if err != nil {
		t.Fatalf("encode tcp test message: %v", err)
	}
	for len(frame) > 0 {
		written, err := conn.Write(frame)
		if err != nil {
			t.Fatalf("write tcp test message: %v", err)
		}
		frame = frame[written:]
	}
}

type tcpHandlerPanicTestError struct {
	marker string
}

func (e *tcpHandlerPanicTestError) Error() string {
	return e.marker
}
