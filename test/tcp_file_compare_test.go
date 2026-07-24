// File: test/tcp_file_compare_test.go
// Purpose: Exercises KPro protocol and transport behavior through integration tests.

package test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/tcp"
)

const (
	msgTraceEcho      uint32 = 300
	msgTraceSync      uint32 = 301
	msgTraceSlowAsync uint32 = 302
	msgTraceFastAsync uint32 = 303
	msgTraceSyncA     uint32 = 304
	msgTraceAsyncB    uint32 = 305
	msgTraceSyncC     uint32 = 306
	msgTraceQueueFull uint32 = 307
	msgTraceInflight  uint32 = 308
	msgTraceAsyncFull uint32 = 309
	msgTraceIsolated  uint32 = 310
	msgTraceUnknown   uint32 = 399
)

func TestTCPEchoFileTrace(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	replied := trace.log("server_replied.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgTraceEcho, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	msg := core.Message{ID: msgTraceEcho, Seq: 1, Data: []byte("echo")}
	sendTraced(t, sent, conn, msg)
	readTraced(t, clientReceived, conn)

	assertTraceFileEqual(t, sent.path, received.path)
	assertTraceFileEqual(t, sent.path, replied.path)
	assertTraceFileEqual(t, sent.path, clientReceived.path)
}

func TestTCPSyncFileTraceOrder(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	replied := trace.log("server_replied.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgTraceSync, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	for seq, payload := range []string{"A", "B", "C"} {
		msg := core.Message{ID: msgTraceSync, Seq: uint64(seq + 1), Data: []byte(payload)}
		sendTraced(t, sent, conn, msg)
	}
	for i := 0; i < 3; i++ {
		readTraced(t, clientReceived, conn)
	}

	assertTraceFileEqual(t, sent.path, received.path)
	assertTraceFileEqual(t, sent.path, replied.path)
	assertTraceFileEqual(t, sent.path, clientReceived.path)
}

func TestTCPAsyncFileTraceAllowsOutOfOrderResponses(t *testing.T) {
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

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          2,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleAsync(msgTraceSlowAsync, func(ctx core.Context) error {
			close(slowStarted)
			<-releaseSlow
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
		router.HandleAsync(msgTraceFastAsync, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	slow := core.Message{ID: msgTraceSlowAsync, Seq: 1, Data: []byte("slow")}
	fast := core.Message{ID: msgTraceFastAsync, Seq: 2, Data: []byte("fast")}
	sendTraced(t, sent, conn, slow)
	waitForClosed(t, slowStarted, "slow async handler did not start")
	sendTraced(t, sent, conn, fast)

	first := readTraced(t, clientReceived, conn)
	if first.Seq != fast.Seq {
		t.Fatalf("expected fast async response first, got seq=%d", first.Seq)
	}
	release()
	second := readTraced(t, clientReceived, conn)
	if second.Seq != slow.Seq {
		t.Fatalf("expected slow async response second, got seq=%d", second.Seq)
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expected := trace.expected("expected_client_received.ndjson", []core.Message{fast, slow})
	assertTraceFileEqual(t, expected, clientReceived.path)
	assertTraceFileEqual(t, expected, replied.path)
}

func TestTCPMixedSyncAsyncFileTraceOrder(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	replied := trace.log("server_replied.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	syncStarted := make(chan struct{})
	releaseSync := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseSync)
		})
	}
	defer release()

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          2,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgTraceSyncA, func(ctx core.Context) error {
			close(syncStarted)
			<-releaseSync
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
		router.HandleAsync(msgTraceAsyncB, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
		router.HandleSync(msgTraceSyncC, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	msgA := core.Message{ID: msgTraceSyncA, Seq: 1, Data: []byte("sync-A")}
	msgB := core.Message{ID: msgTraceAsyncB, Seq: 2, Data: []byte("async-B")}
	msgC := core.Message{ID: msgTraceSyncC, Seq: 3, Data: []byte("sync-C")}

	sendTraced(t, sent, conn, msgA)
	waitForClosed(t, syncStarted, "sync A handler did not start")
	sendTraced(t, sent, conn, msgB)
	sendTraced(t, sent, conn, msgC)

	first := readTraced(t, clientReceived, conn)
	if first.Seq != msgB.Seq {
		t.Fatalf("expected async B response first, got seq=%d", first.Seq)
	}
	release()
	second := readTraced(t, clientReceived, conn)
	third := readTraced(t, clientReceived, conn)
	if second.Seq != msgA.Seq || third.Seq != msgC.Seq {
		t.Fatalf("expected response order B,A,C; got first=%d second=%d third=%d", first.Seq, second.Seq, third.Seq)
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expected := trace.expected("expected_client_received.ndjson", []core.Message{msgB, msgA, msgC})
	assertTraceFileEqual(t, expected, clientReceived.path)
	assertTraceFileEqual(t, expected, replied.path)
}

func TestTCPSyncQueueFullFileTraceError(t *testing.T) {
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

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         1,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgTraceQueueFull, func(ctx core.Context) error {
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

	conn := dialTCP(t, addr)
	defer conn.Close()

	msgA := core.Message{ID: msgTraceQueueFull, Seq: 1, Data: []byte("A")}
	msgB := core.Message{ID: msgTraceQueueFull, Seq: 2, Data: []byte("B")}
	msgC := core.Message{ID: msgTraceQueueFull, Seq: 3, Data: []byte("C")}

	sendTraced(t, sent, conn, msgA)
	waitForClosed(t, firstStarted, "first sync handler did not start")
	sendTraced(t, sent, conn, msgB)
	sendTraced(t, sent, conn, msgC)

	first := readTraced(t, clientReceived, conn)
	if first.ID != core.MsgError || first.Seq != msgC.Seq || string(first.Data) != core.PublicErrorMessage(core.ErrDispatchQueueFull) {
		t.Fatalf("expected queue-full error for seq=%d, got id=%d seq=%d data=%q", msgC.Seq, first.ID, first.Seq, string(first.Data))
	}
	release()
	second := readTraced(t, clientReceived, conn)
	third := readTraced(t, clientReceived, conn)
	if second.Seq != msgA.Seq || third.Seq != msgB.Seq {
		t.Fatalf("expected response order C-error,A,B; got first=%d second=%d third=%d", first.Seq, second.Seq, third.Seq)
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expectedHandled := trace.expected("expected_server_handled.ndjson", []core.Message{msgA, msgB})
	assertTraceFileEqual(t, expectedHandled, handled.path)
	expectedClientReceived := trace.expected("expected_client_received.ndjson", []core.Message{
		{ID: core.MsgError, Seq: msgC.Seq, Data: []byte(core.PublicErrorMessage(core.ErrDispatchQueueFull))},
		msgA,
		msgB,
	})
	assertTraceFileEqual(t, expectedClientReceived, clientReceived.path)
}

func TestTCPNoHandlerFileTraceError(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {})
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	msg := core.Message{ID: msgTraceUnknown, Seq: 1, Data: []byte("unknown")}
	sendTraced(t, sent, conn, msg)
	resp := readTraced(t, clientReceived, conn)
	if resp.ID != core.MsgError || resp.Seq != msg.Seq || string(resp.Data) != core.PublicErrorMessage(core.ErrNoHandler) {
		t.Fatalf("expected no-handler error, got id=%d seq=%d data=%q", resp.ID, resp.Seq, string(resp.Data))
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expected := trace.expected("expected_client_received.ndjson", []core.Message{
		{ID: core.MsgError, Seq: msg.Seq, Data: []byte(core.PublicErrorMessage(core.ErrNoHandler))},
	})
	assertTraceFileEqual(t, expected, clientReceived.path)
}

func TestTCPMaxInflightFileTraceError(t *testing.T) {
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

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 1,
	}, received, func(router *core.Router) {
		router.HandleSync(msgTraceInflight, func(ctx core.Context) error {
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

	conn := dialTCP(t, addr)
	defer conn.Close()

	msgA := core.Message{ID: msgTraceInflight, Seq: 1, Data: []byte("A")}
	msgB := core.Message{ID: msgTraceInflight, Seq: 2, Data: []byte("B")}
	sendTraced(t, sent, conn, msgA)
	waitForClosed(t, firstStarted, "first sync handler did not start")
	sendTraced(t, sent, conn, msgB)

	first := readTraced(t, clientReceived, conn)
	if first.ID != core.MsgError || first.Seq != msgB.Seq || string(first.Data) != core.PublicErrorMessage(core.ErrTooManyInflight) {
		t.Fatalf("expected inflight error for seq=%d, got id=%d seq=%d data=%q", msgB.Seq, first.ID, first.Seq, string(first.Data))
	}
	release()
	second := readTraced(t, clientReceived, conn)
	if second.Seq != msgA.Seq {
		t.Fatalf("expected seq=%d after inflight error, got seq=%d", msgA.Seq, second.Seq)
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expectedHandled := trace.expected("expected_server_handled.ndjson", []core.Message{msgA})
	assertTraceFileEqual(t, expectedHandled, handled.path)
	expectedClientReceived := trace.expected("expected_client_received.ndjson", []core.Message{
		{ID: core.MsgError, Seq: msgB.Seq, Data: []byte(core.PublicErrorMessage(core.ErrTooManyInflight))},
		msgA,
	})
	assertTraceFileEqual(t, expectedClientReceived, clientReceived.path)
}

func TestTCPAsyncQueueFullFileTraceError(t *testing.T) {
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

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        1,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleAsync(msgTraceAsyncFull, func(ctx core.Context) error {
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

	conn := dialTCP(t, addr)
	defer conn.Close()

	msgA := core.Message{ID: msgTraceAsyncFull, Seq: 1, Data: []byte("A")}
	msgB := core.Message{ID: msgTraceAsyncFull, Seq: 2, Data: []byte("B")}
	msgC := core.Message{ID: msgTraceAsyncFull, Seq: 3, Data: []byte("C")}
	sendTraced(t, sent, conn, msgA)
	waitForClosed(t, firstStarted, "first async handler did not start")
	sendTraced(t, sent, conn, msgB)
	sendTraced(t, sent, conn, msgC)

	first := readTraced(t, clientReceived, conn)
	if first.ID != core.MsgError || first.Seq != msgC.Seq || string(first.Data) != core.PublicErrorMessage(core.ErrDispatchQueueFull) {
		t.Fatalf("expected async queue-full error for seq=%d, got id=%d seq=%d data=%q", msgC.Seq, first.ID, first.Seq, string(first.Data))
	}
	release()
	second := readTraced(t, clientReceived, conn)
	third := readTraced(t, clientReceived, conn)
	if second.Seq != msgA.Seq || third.Seq != msgB.Seq {
		t.Fatalf("expected response order C-error,A,B; got first=%d second=%d third=%d", first.Seq, second.Seq, third.Seq)
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expectedHandled := trace.expected("expected_server_handled.ndjson", []core.Message{msgA, msgB})
	assertTraceFileEqual(t, expectedHandled, handled.path)
	expectedClientReceived := trace.expected("expected_client_received.ndjson", []core.Message{
		{ID: core.MsgError, Seq: msgC.Seq, Data: []byte(core.PublicErrorMessage(core.ErrDispatchQueueFull))},
		msgA,
		msgB,
	})
	assertTraceFileEqual(t, expectedClientReceived, clientReceived.path)
}

func TestTCPMultiConnectionSyncIsolationFileTrace(t *testing.T) {
	trace := newTCPTrace(t)
	sentOne := trace.log("client1_sent.ndjson")
	sentTwo := trace.log("client2_sent.ndjson")
	received := trace.log("server_received.ndjson")
	replied := trace.log("server_replied.ndjson")
	receivedOne := trace.log("client1_received.ndjson")
	receivedTwo := trace.log("client2_received.ndjson")

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseFirst)
		})
	}
	defer release()

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgTraceIsolated, func(ctx core.Context) error {
			msg := ctx.Message()
			if msg.Seq == 1 {
				close(firstStarted)
				<-releaseFirst
			}
			if err := replied.appendMessage(msg); err != nil {
				return err
			}
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	connOne := dialTCP(t, addr)
	defer connOne.Close()
	connTwo := dialTCP(t, addr)
	defer connTwo.Close()

	msgOne := core.Message{ID: msgTraceIsolated, Seq: 1, Data: []byte("conn-1-blocked")}
	msgTwo := core.Message{ID: msgTraceIsolated, Seq: 2, Data: []byte("conn-2-fast")}
	sendTraced(t, sentOne, connOne, msgOne)
	waitForClosed(t, firstStarted, "first connection sync handler did not start")
	sendTraced(t, sentTwo, connTwo, msgTwo)

	respTwo := readTraced(t, receivedTwo, connTwo)
	if respTwo.Seq != msgTwo.Seq {
		t.Fatalf("expected connection 2 to return first, got seq=%d", respTwo.Seq)
	}
	release()
	respOne := readTraced(t, receivedOne, connOne)
	if respOne.Seq != msgOne.Seq {
		t.Fatalf("expected connection 1 response after release, got seq=%d", respOne.Seq)
	}

	expectedReceived := trace.expected("expected_server_received.ndjson", []core.Message{msgOne, msgTwo})
	assertTraceFileEqual(t, expectedReceived, received.path)
	expectedReplied := trace.expected("expected_server_replied.ndjson", []core.Message{msgTwo, msgOne})
	assertTraceFileEqual(t, expectedReplied, replied.path)
	assertTraceFileEqual(t, sentOne.path, receivedOne.path)
	assertTraceFileEqual(t, sentTwo.path, receivedTwo.path)
}

func TestTCPHeartbeatFileTraceBypassesRouter(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startTCPTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {})
	defer stop()

	conn := dialTCP(t, addr)
	defer conn.Close()

	msg := core.Message{ID: core.MsgHeartbeat, Seq: 1, Data: []byte("ping")}
	sendTraced(t, sent, conn, msg)
	resp := readTraced(t, clientReceived, conn)
	if resp.ID != core.MsgPong || resp.Seq != msg.Seq || len(resp.Data) != 0 {
		t.Fatalf("expected pong seq=%d, got id=%d seq=%d data=%q", msg.Seq, resp.ID, resp.Seq, string(resp.Data))
	}

	assertTraceFileEqual(t, trace.expected("expected_server_received.ndjson", nil), received.path)
	expectedClientReceived := trace.expected("expected_client_received.ndjson", []core.Message{
		{ID: core.MsgPong, Seq: msg.Seq},
	})
	assertTraceFileEqual(t, expectedClientReceived, clientReceived.path)
}

type tcpTrace struct {
	t   *testing.T
	dir string
}

type tcpTraceLog struct {
	mu    sync.Mutex
	path  string
	index int
}

type tcpTraceRecord struct {
	Index   int    `json:"index"`
	Seq     uint64 `json:"seq"`
	MsgID   uint32 `json:"msg_id"`
	Payload string `json:"payload"`
}

func newTCPTrace(t *testing.T) *tcpTrace {
	t.Helper()
	dir, err := os.MkdirTemp("", "kpro-tcp-trace-*")
	if err != nil {
		t.Fatalf("create trace dir: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("preserved TCP trace dir: %s", dir)
			return
		}
		_ = os.RemoveAll(dir)
	})
	return &tcpTrace{t: t, dir: dir}
}

func (tr *tcpTrace) log(name string) *tcpTraceLog {
	tr.t.Helper()
	path := filepath.Join(tr.dir, name)
	if err := os.WriteFile(path, nil, 0644); err != nil {
		tr.t.Fatalf("create trace file %s: %v", path, err)
	}
	return &tcpTraceLog{path: path}
}

func (tr *tcpTrace) expected(name string, messages []core.Message) string {
	tr.t.Helper()
	path := filepath.Join(tr.dir, name)
	var buf bytes.Buffer
	for i, msg := range messages {
		writeTraceRecord(tr.t, &buf, tcpTraceRecord{
			Index:   i + 1,
			Seq:     msg.Seq,
			MsgID:   msg.ID,
			Payload: string(msg.Data),
		})
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		tr.t.Fatalf("write expected trace %s: %v", path, err)
	}
	return path
}

func (l *tcpTraceLog) appendMessage(msg core.Message) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.index++
	file, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	return appendTraceRecord(file, tcpTraceRecord{
		Index:   l.index,
		Seq:     msg.Seq,
		MsgID:   msg.ID,
		Payload: string(msg.Data),
	})
}

func writeTraceRecord(t *testing.T, dst interface {
	Write([]byte) (int, error)
}, record tcpTraceRecord) {
	t.Helper()
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal trace record: %v", err)
	}
	line = append(line, '\n')
	if _, err := dst.Write(line); err != nil {
		t.Fatalf("write trace record: %v", err)
	}
}

func appendTraceRecord(dst interface {
	Write([]byte) (int, error)
}, record tcpTraceRecord) error {
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	_, err = dst.Write(line)
	return err
}

func startTCPTraceServer(t *testing.T, routerConfig core.RouterConfig, received *tcpTraceLog, register func(*core.Router)) (string, func()) {
	t.Helper()

	addr := "127.0.0.1:" + freeTCPPort(t)
	router := core.NewRouter(routerConfig)
	register(router)

	transport := tcp.New(tcp.Config{
		Addr:          addr,
		Codec:         codec.KProV1(),
		ReadTimeout:   5 * time.Second,
		WriteTimeout:  5 * time.Second,
		SendQueueSize: 16,
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

	stop := func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = transport.Shutdown(shutdownCtx)
		_ = router.Shutdown(shutdownCtx)
		select {
		case <-errCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("tcp trace server did not stop")
		}
	}
	return addr, stop
}

func sendTraced(t *testing.T, log *tcpTraceLog, conn net.Conn, msg core.Message) {
	t.Helper()
	if err := log.appendMessage(msg); err != nil {
		t.Fatalf("append sent trace: %v", err)
	}
	writeMessage(t, conn, msg)
}

func readTraced(t *testing.T, log *tcpTraceLog, conn net.Conn) core.Message {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msg := readMessage(t, conn)
	if err := log.appendMessage(msg); err != nil {
		t.Fatalf("append received trace: %v", err)
	}
	return msg
}

func assertTraceFileEqual(t *testing.T, wantPath, gotPath string) {
	t.Helper()
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read trace file %s: %v", wantPath, err)
	}
	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("read trace file %s: %v", gotPath, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("trace files differ\nwant: %s\n%s\ngot: %s\n%s", wantPath, string(want), gotPath, string(got))
	}
}

func waitForClosed(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func readTraceRecords(t *testing.T, path string) []tcpTraceRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace file %s: %v", path, err)
	}
	defer file.Close()

	var records []tcpTraceRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record tcpTraceRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode trace record %s: %v", path, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan trace file %s: %v", path, err)
	}
	return records
}
