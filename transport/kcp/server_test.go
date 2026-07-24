package kcp

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
	kcpgo "github.com/xtaci/kcp-go"
)

func TestKCPEchoDispatch(t *testing.T) {
	server, addr, stop := startKCPTestServer(t, Config{
		Addr:         freeKCPTestAddr(t),
		Codec:        codec.KProV1(),
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

	want := core.Message{ID: testMsgEcho, Seq: 7, Data: []byte("hello kcp")}
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
		Codec:        codec.KProV1(),
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
		Codec:        codec.KProV1(),
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

	writeKCPTestMessage(t, conn, core.Message{ID: testMsgEcho, Seq: 1, Data: []byte("xx")})
	assertKCPConnectionClosed(t, conn)
	waitForKCPActiveCount(t, server, 0)
	if handled.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", handled.Load())
	}
}

func TestKCPHandlerErrorIsObservedAndClosesSessionByDefault(t *testing.T) {
	wantErr := errors.New("kcp handler failed")
	observed := make(chan transportapi.HandlerErrorEvent, 1)
	server, addr, stop := startKCPTestServer(t, Config{
		Addr:           freeKCPTestAddr(t),
		ReadTimeout:    time.Second,
		WriteTimeout:   time.Second,
		OnHandlerError: func(event transportapi.HandlerErrorEvent) { observed <- event },
	}, func(core.Context) error { return wantErr })
	defer stop()

	conn := dialKCPTest(t, addr)
	defer conn.Close()
	writeKCPTestMessage(t, conn, core.Message{ID: testMsgEcho, Seq: 17})
	select {
	case event := <-observed:
		if !errors.Is(event.Err, wantErr) || event.Transport != core.TransportKCP || event.MessageID != testMsgEcho || event.Sequence != 17 {
			t.Fatalf("handler error event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("handler error was not observed")
	}
	assertKCPConnectionClosed(t, conn)
	waitForKCPActiveCount(t, server, 0)
}

func TestKCPHandlerErrorContinuePolicyKeepsSessionUsable(t *testing.T) {
	wantErr := errors.New("retryable handler failure")
	observed := make(chan transportapi.HandlerErrorEvent, 1)
	var calls atomic.Int32
	server, addr, stop := startKCPTestServer(t, Config{
		Addr:               freeKCPTestAddr(t),
		ReadTimeout:        time.Second,
		WriteTimeout:       time.Second,
		HandlerErrorPolicy: transportapi.HandlerErrorContinue,
		OnHandlerError:     func(event transportapi.HandlerErrorEvent) { observed <- event },
	}, func(ctx core.Context) error {
		if calls.Add(1) == 1 {
			return wantErr
		}
		msg := ctx.Message()
		return ctx.Reply(msg.ID, msg.Data)
	})
	defer stop()

	conn := dialKCPTest(t, addr)
	defer conn.Close()
	writeKCPTestMessage(t, conn, core.Message{ID: testMsgEcho, Seq: 1})
	select {
	case event := <-observed:
		if !errors.Is(event.Err, wantErr) {
			t.Fatalf("observed error = %v, want %v", event.Err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("handler error was not observed")
	}
	writeKCPTestMessage(t, conn, core.Message{ID: testMsgEcho, Seq: 2, Data: []byte("still open")})
	if got := readKCPTestMessage(t, conn); got.Seq != 2 || string(got.Data) != "still open" {
		t.Fatalf("response after handler error = %+v", got)
	}
	waitForKCPActiveCount(t, server, 1)
}

func TestKCPSendQueueFullReturnsOverloadBeforeClosedState(t *testing.T) {
	server := New(Config{Codec: codec.KProV1()})
	session := &session{server: server, sendQueue: make(chan []byte), done: make(chan struct{})}
	// Prevent this focused unit from dereferencing a real KCP session while
	// retaining the Send path's close-on-overload behavior.
	session.closeOnce.Do(func() {})
	err := session.Send(core.Message{ID: testMsgEcho})
	if !errors.Is(err, core.ErrSendQueueFull) {
		t.Fatalf("Send() error = %v, want %v", err, core.ErrSendQueueFull)
	}
	if errors.Is(err, core.ErrClosed) {
		t.Fatalf("Send() error = %v, must report overload rather than prior closed state", err)
	}
}

func TestKCPRejectsNilHandlerWithoutBinding(t *testing.T) {
	server := New(Config{Addr: freeKCPTestAddr(t)})
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
		t.Fatal("nil handler startup published a KCP listener")
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
	select {
	case <-server.Ready():
	case <-time.After(time.Second):
		t.Fatal("KCP Ready() did not close")
	}
	server.lifecycleMu.Lock()
	listener := server.ln
	server.lifecycleMu.Unlock()
	if listener == nil {
		t.Fatal("KCP Ready() closed without a published listener")
	}

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
	frame, err := codec.KProV1().Encode(msg)
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
	result, err := codec.KProV1().Read(conn, 64*1024)
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
