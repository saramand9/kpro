// File: test/tcp_dispatch_test.go
// Purpose: Exercises KPro protocol and transport behavior through integration tests.

package test

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/server"
	"github.com/saramand9/kpro/transport/tcp"
)

const (
	msgBlock uint32 = 201
	msgFast  uint32 = 202
)

func TestTCPAsyncHandlerCanReturnBeforeBlockedSyncHandler(t *testing.T) {
	port := freeTCPPort(t)
	addr := "127.0.0.1:" + port

	app := server.New(server.Config{
		Name: "tcp-dispatch-test",
		Router: core.RouterConfig{
			SyncQueueSize:         4,
			AsyncWorkers:          1,
			AsyncQueueSize:        4,
			MaxInflightPerSession: 4,
		},
	})

	syncStarted := make(chan struct{})
	releaseSync := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseSync)
		})
	}

	app.Mount(tcp.New(tcp.Config{Addr: addr, Codec: codec.KProV1()})).
		HandleSync(msgBlock, func(ctx core.Context) error {
			close(syncStarted)
			<-releaseSync
			return ctx.Reply(msgBlock, []byte("sync"))
		}).
		HandleAsync(msgFast, func(ctx core.Context) error {
			return ctx.Reply(msgFast, []byte("async"))
		})

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.ListenAndServe()
	}()
	defer func() {
		release()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = app.Shutdown(ctx)
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not stop")
		}
	}()

	conn := dialTCP(t, addr)
	defer conn.Close()

	writeMessage(t, conn, core.Message{ID: msgBlock, Seq: 1, Data: []byte("sync")})
	select {
	case <-syncStarted:
	case <-time.After(time.Second):
		t.Fatal("sync handler did not start")
	}

	writeMessage(t, conn, core.Message{ID: msgFast, Seq: 2, Data: []byte("async")})
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	msg := readMessage(t, conn)
	if msg.ID != msgFast || msg.Seq != 2 || string(msg.Data) != "async" {
		t.Fatalf("got first response id=%d seq=%d data=%q", msg.ID, msg.Seq, string(msg.Data))
	}

	release()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	msg = readMessage(t, conn)
	if msg.ID != msgBlock || msg.Seq != 1 || string(msg.Data) != "sync" {
		t.Fatalf("got second response id=%d seq=%d data=%q", msg.ID, msg.Seq, string(msg.Data))
	}
}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func dialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			return conn
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("dial %s: %v", addr, lastErr)
	return nil
}

func writeMessage(t *testing.T, conn net.Conn, msg core.Message) {
	t.Helper()
	frame, err := codec.KProV1().Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readMessage(t *testing.T, conn net.Conn) core.Message {
	t.Helper()
	result, err := codec.KProV1().Read(conn, 64*1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return result.Message
}
