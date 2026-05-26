package test

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
	"github.com/saramand9/bignet/kpro/transport/udp"
)

const (
	msgUDPTraceEcho      uint32 = 401
	msgUDPTraceSlowAsync uint32 = 402
	msgUDPTraceFastAsync uint32 = 403
	msgUDPTraceQueueFull uint32 = 404
	msgUDPTraceUnknown   uint32 = 499
)

func TestUDPEchoFileTrace(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	replied := trace.log("server_replied.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startUDPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgUDPTraceEcho, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	client := dialUDPTraceClient(t, addr)
	defer client.close()

	msg := core.Message{ID: msgUDPTraceEcho, Seq: 1, Data: []byte("udp-echo")}
	sendUDPTraced(t, sent, client, msg)
	readUDPTraced(t, clientReceived, client)

	assertTraceFileEqual(t, sent.path, received.path)
	assertTraceFileEqual(t, sent.path, replied.path)
	assertTraceFileEqual(t, sent.path, clientReceived.path)
}

func TestUDPAsyncFileTraceAllowsOutOfOrderResponses(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	replied := trace.log("server_replied.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseSlow)
		})
	}
	defer release()

	addr, stop := startUDPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          2,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleAsync(msgUDPTraceSlowAsync, func(ctx core.Context) error {
			close(slowStarted)
			<-releaseSlow
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
		router.HandleAsync(msgUDPTraceFastAsync, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	client := dialUDPTraceClient(t, addr)
	defer client.close()

	slow := core.Message{ID: msgUDPTraceSlowAsync, Seq: 1, Data: []byte("slow")}
	fast := core.Message{ID: msgUDPTraceFastAsync, Seq: 2, Data: []byte("fast")}
	sendUDPTraced(t, sent, client, slow)
	waitForClosed(t, slowStarted, "slow UDP async handler did not start")
	sendUDPTraced(t, sent, client, fast)

	first := readUDPTraced(t, clientReceived, client)
	if first.Seq != fast.Seq {
		t.Fatalf("expected fast UDP async response first, got seq=%d", first.Seq)
	}
	release()
	second := readUDPTraced(t, clientReceived, client)
	if second.Seq != slow.Seq {
		t.Fatalf("expected slow UDP async response second, got seq=%d", second.Seq)
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expected := trace.expected("expected_client_received.ndjson", []core.Message{fast, slow})
	assertTraceFileEqual(t, expected, clientReceived.path)
	assertTraceFileEqual(t, expected, replied.path)
}

func TestUDPSyncQueueFullFileTraceError(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	handled := trace.log("server_handled.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseFirst)
		})
	}
	defer release()

	addr, stop := startUDPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         1,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgUDPTraceQueueFull, func(ctx core.Context) error {
			if err := handled.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			if msg.Seq == 1 {
				close(firstStarted)
				<-releaseFirst
			}
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	client := dialUDPTraceClient(t, addr)
	defer client.close()

	msgA := core.Message{ID: msgUDPTraceQueueFull, Seq: 1, Data: []byte("A")}
	msgB := core.Message{ID: msgUDPTraceQueueFull, Seq: 2, Data: []byte("B")}
	msgC := core.Message{ID: msgUDPTraceQueueFull, Seq: 3, Data: []byte("C")}
	sendUDPTraced(t, sent, client, msgA)
	waitForClosed(t, firstStarted, "first UDP sync handler did not start")
	sendUDPTraced(t, sent, client, msgB)
	sendUDPTraced(t, sent, client, msgC)

	first := readUDPTraced(t, clientReceived, client)
	if first.ID != core.MsgError || first.Seq != msgC.Seq || string(first.Data) != core.ErrDispatchQueueFull.Error() {
		t.Fatalf("expected UDP queue-full error for seq=%d, got id=%d seq=%d data=%q", msgC.Seq, first.ID, first.Seq, string(first.Data))
	}
	release()
	second := readUDPTraced(t, clientReceived, client)
	third := readUDPTraced(t, clientReceived, client)
	if second.Seq != msgA.Seq || third.Seq != msgB.Seq {
		t.Fatalf("expected UDP response order C-error,A,B; got first=%d second=%d third=%d", first.Seq, second.Seq, third.Seq)
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expectedHandled := trace.expected("expected_server_handled.ndjson", []core.Message{msgA, msgB})
	assertTraceFileEqual(t, expectedHandled, handled.path)
	expectedClientReceived := trace.expected("expected_client_received.ndjson", []core.Message{
		{ID: core.MsgError, Seq: msgC.Seq, Data: []byte(core.ErrDispatchQueueFull.Error())},
		msgA,
		msgB,
	})
	assertTraceFileEqual(t, expectedClientReceived, clientReceived.path)
}

func TestUDPNoHandlerFileTraceError(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startUDPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {})
	defer stop()

	client := dialUDPTraceClient(t, addr)
	defer client.close()

	msg := core.Message{ID: msgUDPTraceUnknown, Seq: 1, Data: []byte("unknown")}
	sendUDPTraced(t, sent, client, msg)
	resp := readUDPTraced(t, clientReceived, client)
	if resp.ID != core.MsgError || resp.Seq != msg.Seq || string(resp.Data) != core.ErrNoHandler.Error() {
		t.Fatalf("expected UDP no-handler error, got id=%d seq=%d data=%q", resp.ID, resp.Seq, string(resp.Data))
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expected := trace.expected("expected_client_received.ndjson", []core.Message{
		{ID: core.MsgError, Seq: msg.Seq, Data: []byte(core.ErrNoHandler.Error())},
	})
	assertTraceFileEqual(t, expected, clientReceived.path)
}

func TestUDPHeartbeatFileTraceBypassesRouter(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startUDPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {})
	defer stop()

	client := dialUDPTraceClient(t, addr)
	defer client.close()

	msg := core.Message{ID: core.MsgHeartbeat, Seq: 1, Data: []byte("ping")}
	sendUDPTraced(t, sent, client, msg)
	resp := readUDPTraced(t, clientReceived, client)
	if resp.ID != core.MsgPong || resp.Seq != msg.Seq || len(resp.Data) != 0 {
		t.Fatalf("expected UDP pong seq=%d, got id=%d seq=%d data=%q", msg.Seq, resp.ID, resp.Seq, string(resp.Data))
	}

	assertTraceFileEqual(t, trace.expected("expected_server_received.ndjson", nil), received.path)
	expectedClientReceived := trace.expected("expected_client_received.ndjson", []core.Message{{ID: core.MsgPong, Seq: msg.Seq}})
	assertTraceFileEqual(t, expectedClientReceived, clientReceived.path)
}

type udpTraceClient struct {
	conn *net.UDPConn
}

func startUDPTraceServer(t *testing.T, routerConfig core.RouterConfig, received *tcpTraceLog, register func(*core.Router)) (string, func()) {
	t.Helper()

	addr := "127.0.0.1:" + freeUDPPort(t)
	router := core.NewRouter(routerConfig)
	register(router)

	transport := udp.New(udp.Config{
		Addr:          addr,
		Protocol:      protocol.KProV1(),
		MaxPacketSize: 2048,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- transport.ListenAndServe(ctx, func(ctx core.Context) error {
			if err := received.appendMessage(ctx.Message()); err != nil {
				return err
			}
			return router.Dispatch(ctx)
		})
	}()
	select {
	case err := <-errCh:
		t.Fatalf("udp trace server exited early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = transport.Shutdown(shutdownCtx)
		_ = router.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("udp trace server did not stop")
		}
	}
	return addr, stop
}

func freeUDPPort(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen free UDP port: %v", err)
	}
	defer conn.Close()
	return strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)
}

func dialUDPTraceClient(t *testing.T, addr string) *udpTraceClient {
	t.Helper()
	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatalf("resolve UDP addr: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		t.Fatalf("dial UDP addr: %v", err)
	}
	return &udpTraceClient{conn: conn}
}

func (c *udpTraceClient) close() {
	_ = c.conn.Close()
}

func sendUDPTraced(t *testing.T, log *tcpTraceLog, client *udpTraceClient, msg core.Message) {
	t.Helper()
	if err := log.appendMessage(msg); err != nil {
		t.Fatalf("append UDP sent trace: %v", err)
	}
	packet, err := protocol.KProV1().EncodePacket(msg)
	if err != nil {
		t.Fatalf("encode UDP packet: %v", err)
	}
	if _, err := client.conn.Write(packet); err != nil {
		t.Fatalf("write UDP packet: %v", err)
	}
}

func readUDPTraced(t *testing.T, log *tcpTraceLog, client *udpTraceClient) core.Message {
	t.Helper()
	_ = client.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, err := client.conn.Read(buf)
	if err != nil {
		t.Fatalf("read UDP packet: %v", err)
	}
	msg, err := protocol.KProV1().DecodePacket(buf[:n])
	if err != nil {
		t.Fatalf("decode UDP packet: %v", err)
	}
	if err := log.appendMessage(msg); err != nil {
		t.Fatalf("append UDP received trace: %v", err)
	}
	return msg
}
