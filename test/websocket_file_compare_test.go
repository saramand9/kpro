// File: test/websocket_file_compare_test.go
// Purpose: Exercises KPro protocol and transport behavior through integration tests.

package test

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	wstransport "github.com/saramand9/kpro/transport/websocket"
)

const (
	msgWSTraceEcho      uint32 = 501
	msgWSTraceSlowAsync uint32 = 502
	msgWSTraceFastAsync uint32 = 503
	msgWSTraceQueueFull uint32 = 504
	msgWSTraceUnknown   uint32 = 599
)

func TestWebSocketEchoFileTrace(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	replied := trace.log("server_replied.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startWebSocketTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgWSTraceEcho, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	client := dialWebSocketTraceClient(t, "ws://"+addr+"/ws")
	defer client.close()

	msg := core.Message{ID: msgWSTraceEcho, Seq: 1, Data: []byte("ws-echo")}
	sendWebSocketTraced(t, sent, client, msg)
	readWebSocketTraced(t, clientReceived, client)

	assertTraceFileEqual(t, sent.path, received.path)
	assertTraceFileEqual(t, sent.path, replied.path)
	assertTraceFileEqual(t, sent.path, clientReceived.path)
}

func TestWebSocketFragmentedMessageFileTrace(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	replied := trace.log("server_replied.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startWebSocketTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgWSTraceEcho, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	client := dialWebSocketTraceClient(t, "ws://"+addr+"/ws")
	defer client.close()

	msg := core.Message{ID: msgWSTraceEcho, Seq: 1, Data: []byte("ws-fragmented-echo")}
	sendWebSocketFragmentedTraced(t, sent, client, msg)
	readWebSocketTraced(t, clientReceived, client)

	assertTraceFileEqual(t, sent.path, received.path)
	assertTraceFileEqual(t, sent.path, replied.path)
	assertTraceFileEqual(t, sent.path, clientReceived.path)
}
func TestWebSocketAsyncFileTraceAllowsOutOfOrderResponses(t *testing.T) {
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

	addr, stop := startWebSocketTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          2,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleAsync(msgWSTraceSlowAsync, func(ctx core.Context) error {
			close(slowStarted)
			<-releaseSlow
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
		router.HandleAsync(msgWSTraceFastAsync, func(ctx core.Context) error {
			if err := replied.appendMessage(ctx.Message()); err != nil {
				return err
			}
			msg := ctx.Message()
			return ctx.Reply(msg.ID, msg.Data)
		})
	})
	defer stop()

	client := dialWebSocketTraceClient(t, "ws://"+addr+"/ws")
	defer client.close()

	slow := core.Message{ID: msgWSTraceSlowAsync, Seq: 1, Data: []byte("slow")}
	fast := core.Message{ID: msgWSTraceFastAsync, Seq: 2, Data: []byte("fast")}
	sendWebSocketTraced(t, sent, client, slow)
	waitForClosed(t, slowStarted, "slow WebSocket async handler did not start")
	sendWebSocketTraced(t, sent, client, fast)

	first := readWebSocketTraced(t, clientReceived, client)
	if first.Seq != fast.Seq {
		t.Fatalf("expected fast WebSocket async response first, got seq=%d", first.Seq)
	}
	release()
	second := readWebSocketTraced(t, clientReceived, client)
	if second.Seq != slow.Seq {
		t.Fatalf("expected slow WebSocket async response second, got seq=%d", second.Seq)
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expected := trace.expected("expected_client_received.ndjson", []core.Message{fast, slow})
	assertTraceFileEqual(t, expected, clientReceived.path)
	assertTraceFileEqual(t, expected, replied.path)
}

func TestWebSocketSyncQueueFullFileTraceError(t *testing.T) {
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

	addr, stop := startWebSocketTraceServer(t, core.RouterConfig{
		SyncQueueSize:         1,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {
		router.HandleSync(msgWSTraceQueueFull, func(ctx core.Context) error {
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

	client := dialWebSocketTraceClient(t, "ws://"+addr+"/ws")
	defer client.close()

	msgA := core.Message{ID: msgWSTraceQueueFull, Seq: 1, Data: []byte("A")}
	msgB := core.Message{ID: msgWSTraceQueueFull, Seq: 2, Data: []byte("B")}
	msgC := core.Message{ID: msgWSTraceQueueFull, Seq: 3, Data: []byte("C")}
	sendWebSocketTraced(t, sent, client, msgA)
	waitForClosed(t, firstStarted, "first WebSocket sync handler did not start")
	sendWebSocketTraced(t, sent, client, msgB)
	sendWebSocketTraced(t, sent, client, msgC)

	first := readWebSocketTraced(t, clientReceived, client)
	if first.ID != core.MsgError || first.Seq != msgC.Seq || string(first.Data) != core.PublicErrorMessage(core.ErrDispatchQueueFull) {
		t.Fatalf("expected WebSocket queue-full error for seq=%d, got id=%d seq=%d data=%q", msgC.Seq, first.ID, first.Seq, string(first.Data))
	}
	release()
	second := readWebSocketTraced(t, clientReceived, client)
	third := readWebSocketTraced(t, clientReceived, client)
	if second.Seq != msgA.Seq || third.Seq != msgB.Seq {
		t.Fatalf("expected WebSocket response order C-error,A,B; got first=%d second=%d third=%d", first.Seq, second.Seq, third.Seq)
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

func TestWebSocketNoHandlerFileTraceError(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startWebSocketTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {})
	defer stop()

	client := dialWebSocketTraceClient(t, "ws://"+addr+"/ws")
	defer client.close()

	msg := core.Message{ID: msgWSTraceUnknown, Seq: 1, Data: []byte("unknown")}
	sendWebSocketTraced(t, sent, client, msg)
	resp := readWebSocketTraced(t, clientReceived, client)
	if resp.ID != core.MsgError || resp.Seq != msg.Seq || string(resp.Data) != core.PublicErrorMessage(core.ErrNoHandler) {
		t.Fatalf("expected WebSocket no-handler error, got id=%d seq=%d data=%q", resp.ID, resp.Seq, string(resp.Data))
	}

	assertTraceFileEqual(t, sent.path, received.path)
	expected := trace.expected("expected_client_received.ndjson", []core.Message{
		{ID: core.MsgError, Seq: msg.Seq, Data: []byte(core.PublicErrorMessage(core.ErrNoHandler))},
	})
	assertTraceFileEqual(t, expected, clientReceived.path)
}

func TestWebSocketHeartbeatFileTraceBypassesRouter(t *testing.T) {
	trace := newTCPTrace(t)
	sent := trace.log("client_sent.ndjson")
	received := trace.log("server_received.ndjson")
	clientReceived := trace.log("client_received.ndjson")

	addr, stop := startWebSocketTraceServer(t, core.RouterConfig{
		SyncQueueSize:         4,
		AsyncWorkers:          1,
		AsyncQueueSize:        4,
		MaxInflightPerSession: 4,
	}, received, func(router *core.Router) {})
	defer stop()

	client := dialWebSocketTraceClient(t, "ws://"+addr+"/ws")
	defer client.close()

	msg := core.Message{ID: core.MsgHeartbeat, Seq: 1, Data: []byte("ping")}
	sendWebSocketTraced(t, sent, client, msg)
	resp := readWebSocketTraced(t, clientReceived, client)
	if resp.ID != core.MsgPong || resp.Seq != msg.Seq || len(resp.Data) != 0 {
		t.Fatalf("expected WebSocket pong seq=%d, got id=%d seq=%d data=%q", msg.Seq, resp.ID, resp.Seq, string(resp.Data))
	}

	assertTraceFileEqual(t, trace.expected("expected_server_received.ndjson", nil), received.path)
	expectedClientReceived := trace.expected("expected_client_received.ndjson", []core.Message{{ID: core.MsgPong, Seq: msg.Seq}})
	assertTraceFileEqual(t, expectedClientReceived, clientReceived.path)
}

type webSocketTraceClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func startWebSocketTraceServer(t *testing.T, routerConfig core.RouterConfig, received *tcpTraceLog, register func(*core.Router)) (string, func()) {
	t.Helper()

	addr := "127.0.0.1:" + freeTCPPort(t)
	router := core.NewRouter(routerConfig)
	register(router)

	transport := wstransport.New(wstransport.Config{
		Addr:       addr,
		Path:       "/ws",
		Codec:      codec.KProV1(),
		MaxMessage: 64 * 1024,
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
			t.Fatalf("websocket trace server did not stop")
		}
	}
	return addr, stop
}

func dialWebSocketTraceClient(t *testing.T, rawURL string) *webSocketTraceClient {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, reader, err := dialRawWebSocket(rawURL)
		if err == nil {
			return &webSocketTraceClient{conn: conn, reader: reader}
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("dial websocket %s: %v", rawURL, lastErr)
	return nil
}

func (c *webSocketTraceClient) close() {
	_ = c.conn.Close()
}

func sendWebSocketTraced(t *testing.T, log *tcpTraceLog, client *webSocketTraceClient, msg core.Message) {
	t.Helper()
	if err := log.appendMessage(msg); err != nil {
		t.Fatalf("append WebSocket sent trace: %v", err)
	}
	packet, err := codec.KProV1().EncodePacket(msg)
	if err != nil {
		t.Fatalf("encode WebSocket packet: %v", err)
	}
	if err := writeRawWebSocketBinary(client.conn, packet); err != nil {
		t.Fatalf("write WebSocket frame: %v", err)
	}
}

func sendWebSocketFragmentedTraced(t *testing.T, log *tcpTraceLog, client *webSocketTraceClient, msg core.Message) {
	t.Helper()
	if err := log.appendMessage(msg); err != nil {
		t.Fatalf("append fragmented WebSocket sent trace: %v", err)
	}
	packet, err := codec.KProV1().EncodePacket(msg)
	if err != nil {
		t.Fatalf("encode fragmented WebSocket packet: %v", err)
	}
	if err := writeRawWebSocketBinaryFragments(client.conn, packet); err != nil {
		t.Fatalf("write fragmented WebSocket frame: %v", err)
	}
}
func readWebSocketTraced(t *testing.T, log *tcpTraceLog, client *webSocketTraceClient) core.Message {
	t.Helper()
	_ = client.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	packet, err := readRawWebSocketBinary(client.reader, 64*1024)
	if err != nil {
		t.Fatalf("read WebSocket frame: %v", err)
	}
	msg, err := codec.KProV1().DecodePacket(packet)
	if err != nil {
		t.Fatalf("decode WebSocket packet: %v", err)
	}
	if err := log.appendMessage(msg); err != nil {
		t.Fatalf("append WebSocket received trace: %v", err)
	}
	return msg
}

func dialRawWebSocket(rawURL string) (net.Conn, *bufio.Reader, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, time.Second)
	if err != nil {
		return nil, nil, err
	}
	key, err := rawWebSocketKey()
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	_, err = fmt.Fprintf(conn,
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n",
		path,
		u.Host,
		key,
	)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("websocket upgrade failed: %s", resp.Status)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != rawWebSocketAccept(key) {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("websocket accept key mismatch")
	}
	return conn, reader, nil
}

func writeRawWebSocketBinary(w io.Writer, payload []byte) error {
	return writeRawWebSocketFrame(w, 0x2, payload, true)
}

func writeRawWebSocketBinaryFragments(w io.Writer, payload []byte) error {
	if len(payload) < 3 {
		return writeRawWebSocketBinary(w, payload)
	}
	first := len(payload) / 3
	second := (len(payload) * 2) / 3
	if first == 0 {
		first = 1
	}
	if second <= first {
		second = first + 1
	}
	if err := writeRawWebSocketFrame(w, 0x2, payload[:first], false); err != nil {
		return err
	}
	if err := writeRawWebSocketFrame(w, 0x0, payload[first:second], false); err != nil {
		return err
	}
	return writeRawWebSocketFrame(w, 0x0, payload[second:], true)
}

func writeRawWebSocketFrame(w io.Writer, opcode byte, payload []byte, fin bool) error {
	headerByte := opcode
	if fin {
		headerByte |= 0x80
	}
	header := []byte{headerByte}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 0xffff:
		header = append(header, 0x80|126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(length))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(length))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(masked)
	return err
}

func readRawWebSocketBinary(r *bufio.Reader, maxSize int) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	opcode := header[0] & 0x0f
	if opcode == 0x8 {
		return nil, io.EOF
	}
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if maxSize > 0 && length > uint64(maxSize) {
		return nil, fmt.Errorf("websocket payload too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return nil, err
		}
	}
	payload := make([]byte, int(length))
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	if opcode != 0x2 && opcode != 0x1 {
		return nil, fmt.Errorf("unexpected websocket opcode: %d", opcode)
	}
	return payload, nil
}

func rawWebSocketKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

func rawWebSocketAccept(key string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	sum := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(sum[:])
}
