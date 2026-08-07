package tcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

func TestTCPFaultShortWriteCompletesEveryReceipt(t *testing.T) {
	connection := &chunkedFaultConn{maxChunk: 2, failAfter: -1}
	server := New(Config{
		SendQueueSize:       3,
		MaxPendingBytes:     1024,
		MaxWriteBatchFrames: 3,
		MaxWriteBatchBytes:  1024,
		WriteTimeout:        time.Second,
	})
	session := &session{
		server:    server,
		raw:       connection,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}

	frames := []outboundFrame{
		{payload: []byte("abc")},
		{payload: []byte("defg")},
		{payload: []byte("hijkl")},
	}
	receipts := enqueueTCPFaultFrames(t, session, frames)
	first := <-session.sendQueue.Items()
	writer := newSessionBatchWriter(server.config)
	if err := writer.writeAvailable(session, &first); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAvailable() error = %v, want %v", err, io.ErrShortWrite)
	}
	if got, want := connection.Bytes(), []byte("abdehi"); !bytes.Equal(got, want) {
		t.Fatalf("short-write output = %q, want %q", got, want)
	}
	if calls := connection.WriteCalls(); calls != len(frames) {
		t.Fatalf("Write() calls = %d, want %d gathered slices", calls, len(frames))
	}
	for index, receipt := range receipts {
		if err := receipt.Wait(context.Background()); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("receipt %d error = %v, want %v", index, err, io.ErrShortWrite)
		}
	}
	assertTCPFaultQueueDrained(t, session)
}

func TestTCPFaultPartialProgressErrorCompletesEveryReceipt(t *testing.T) {
	wantErr := errors.New("injected write failure")
	connection := &chunkedFaultConn{
		maxChunk:  3,
		failAfter: 4,
		failErr:   wantErr,
	}
	server := New(Config{
		SendQueueSize:       3,
		MaxPendingBytes:     1024,
		MaxWriteBatchFrames: 3,
		MaxWriteBatchBytes:  1024,
		WriteTimeout:        time.Second,
	})
	session := &session{
		server:    server,
		raw:       connection,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}

	receipts := enqueueTCPFaultFrames(t, session, []outboundFrame{
		{payload: []byte("abc")},
		{payload: []byte("defg")},
		{payload: []byte("hijkl")},
	})
	first := <-session.sendQueue.Items()
	writer := newSessionBatchWriter(server.config)
	if err := writer.writeAvailable(session, &first); !errors.Is(err, wantErr) {
		t.Fatalf("writeAvailable() error = %v, want %v", err, wantErr)
	}
	if got := len(connection.Bytes()); got != connection.failAfter {
		t.Fatalf("bytes written before failure = %d, want %d", got, connection.failAfter)
	}
	for index, receipt := range receipts {
		if err := receipt.Wait(context.Background()); !errors.Is(err, wantErr) {
			t.Fatalf("receipt %d error = %v, want %v", index, err, wantErr)
		}
	}
	assertTCPFaultQueueDrained(t, session)
}

func TestTCPFaultSlowPeerBoundsDrainAndCanLaterComplete(t *testing.T) {
	serverConn, peer := net.Pipe()
	defer peer.Close()
	server := New(Config{
		SendQueueSize:   1,
		MaxPendingBytes: 128 * 1024,
		WriteTimeout:    2 * time.Second,
	})
	atomic.StoreInt64(&server.active, 1)
	session := &session{
		id:        1,
		server:    server,
		raw:       serverConn,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	server.sessions.Store(session.id, session)
	go session.writeLoop()
	defer session.Close()

	want := core.Message{ID: 401, Seq: 9, Data: bytes.Repeat([]byte("s"), 32*1024)}
	receipt, err := session.SendWithReceipt(want)
	if err != nil {
		t.Fatalf("SendWithReceipt() error = %v", err)
	}
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err = session.DrainOutbound(drainCtx)
	cancelDrain()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DrainOutbound() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if _, completed := receipt.Result(); completed {
		t.Fatal("write receipt completed while the peer was not reading")
	}

	readResult := make(chan error, 1)
	go func() {
		result, readErr := server.config.Codec.Read(peer, server.config.MaxFrameSize)
		if readErr == nil && (result.Message.ID != want.ID || result.Message.Seq != want.Seq || !bytes.Equal(result.Message.Data, want.Data)) {
			readErr = errors.New("drained frame did not match the admitted message")
		}
		readResult <- readErr
	}()
	if err := receipt.Wait(context.Background()); err != nil {
		t.Fatalf("write receipt after peer recovery = %v", err)
	}
	if err := <-readResult; err != nil {
		t.Fatalf("peer read after recovery = %v", err)
	}
	if err := session.DrainOutbound(context.Background()); err != nil {
		t.Fatalf("second DrainOutbound() error = %v", err)
	}
	assertTCPFaultQueueDrained(t, session)
}

func TestTCPFaultWriteAndDrainDeadlineOrdering(t *testing.T) {
	t.Run("transport write deadline wins", func(t *testing.T) {
		session, peer := newBlockedTCPFaultSession(t, 30*time.Millisecond)
		defer peer.Close()
		defer session.Close()
		receipt, err := session.SendWithReceipt(core.Message{
			ID:   410,
			Data: bytes.Repeat([]byte("w"), 32*1024),
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		drainErr := session.DrainOutbound(ctx)
		if drainErr == nil || !os.IsTimeout(drainErr) {
			t.Fatalf("DrainOutbound() error = %v, want transport timeout", drainErr)
		}
		if receiptErr := receipt.Wait(context.Background()); receiptErr == nil || !os.IsTimeout(receiptErr) {
			t.Fatalf("receipt error = %v, want transport timeout", receiptErr)
		}
		assertTCPFaultQueueDrained(t, session)
	})

	t.Run("drain deadline wins", func(t *testing.T) {
		session, peer := newBlockedTCPFaultSession(t, 2*time.Second)
		defer session.Close()
		receipt, err := session.SendWithReceipt(core.Message{
			ID:   411,
			Data: bytes.Repeat([]byte("d"), 32*1024),
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		drainErr := session.DrainOutbound(ctx)
		cancel()
		if !errors.Is(drainErr, context.DeadlineExceeded) {
			t.Fatalf("DrainOutbound() error = %v, want %v", drainErr, context.DeadlineExceeded)
		}
		if _, completed := receipt.Result(); completed {
			t.Fatal("receipt completed before the blocked transport write was released")
		}
		if err := peer.Close(); err != nil {
			t.Fatal(err)
		}
		if receiptErr := receipt.Wait(context.Background()); receiptErr == nil {
			t.Fatal("receipt error = nil after peer close")
		}
		waitForTCPFaultQueueDrain(t, session)
	})
}

func TestTCPFaultHalfClosePreservesCompletedReply(t *testing.T) {
	handlerResult := make(chan error, 1)
	server, addr, stop := startTCPTestServer(t, Config{
		Addr:          freeTCPTestAddr(t),
		Codec:         codec.KProV1(),
		ReadTimeout:   time.Second,
		WriteTimeout:  time.Second,
		SendQueueSize: 4,
	}, func(ctx core.Context) error {
		message := ctx.Message()
		receipt, supported, err := core.SessionSendWithReceipt(ctx.Session(), core.Message{
			ID:   421,
			Seq:  message.Seq,
			Data: []byte("reply-after-half-close"),
		})
		if err == nil && !supported {
			err = errors.New("TCP session does not support write receipts")
		}
		if err == nil {
			waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			err = receipt.Wait(waitCtx)
			cancel()
		}
		handlerResult <- err
		return err
	})
	defer stop()

	remote, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTCP("tcp", nil, remote)
	if err != nil {
		t.Fatalf("dial TCP server: %v", err)
	}
	defer connection.Close()
	writeTCPTestMessage(t, connection, core.Message{ID: 420, Seq: 17, Data: []byte("request")})
	if err := connection.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite() error = %v", err)
	}
	if err := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	result, err := codec.KProV1().Read(connection, DefaultMaxFrameSize)
	if err != nil {
		t.Fatalf("read response after CloseWrite: %v", err)
	}
	if result.Message.ID != 421 || result.Message.Seq != 17 || string(result.Message.Data) != "reply-after-half-close" {
		t.Fatalf("half-close response = %+v", result.Message)
	}
	select {
	case err := <-handlerResult:
		if err != nil {
			t.Fatalf("handler result = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not observe write completion")
	}
	assertTCPConnectionClosed(t, connection)
	waitForTCPActiveCountWithin(t, server, 0, 2*time.Second)
}

func TestTCPFaultResetReleasesSessionAndOutboundCapacity(t *testing.T) {
	sessionReady := make(chan core.Session, 1)
	server, addr, stop := startTCPTestServer(t, Config{
		Addr:          freeTCPTestAddr(t),
		Codec:         codec.KProV1(),
		ReadTimeout:   time.Second,
		WriteTimeout:  time.Second,
		SendQueueSize: 4,
	}, func(ctx core.Context) error {
		sessionReady <- ctx.Session()
		return nil
	})
	defer stop()

	remote, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTCP("tcp", nil, remote)
	if err != nil {
		t.Fatalf("dial TCP server: %v", err)
	}
	writeTCPTestMessage(t, connection, core.Message{ID: 430, Seq: 1})
	var accepted core.Session
	select {
	case accepted = <-sessionReady:
	case <-time.After(2 * time.Second):
		_ = connection.Close()
		t.Fatal("handler did not receive the request before reset")
	}
	if err := connection.SetLinger(0); err != nil {
		_ = connection.Close()
		t.Fatalf("SetLinger(0) error = %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("reset close error = %v", err)
	}
	select {
	case <-core.SessionDone(accepted):
	case <-time.After(2 * time.Second):
		t.Fatal("server session did not close after peer reset")
	}
	waitForTCPActiveCountWithin(t, server, 0, 2*time.Second)
	if err := accepted.Send(core.Message{ID: 431}); !errors.Is(err, core.ErrClosed) {
		t.Fatalf("Send() after reset error = %v, want %v", err, core.ErrClosed)
	}
	state, ok := core.SessionOutboundState(accepted)
	if !ok || !state.Closed || state.PendingWrites != 0 || state.PendingBytes != 0 {
		t.Fatalf("outbound state after reset = (%+v, %v)", state, ok)
	}
}

func enqueueTCPFaultFrames(t *testing.T, session *session, frames []outboundFrame) []*core.WriteReceipt {
	t.Helper()
	receipts := make([]*core.WriteReceipt, len(frames))
	for index, frame := range frames {
		receipt := core.NewWriteReceipt()
		reservation, err := session.sendQueue.Reserve(frame.wireBytes())
		if err != nil {
			t.Fatal(err)
		}
		if err := reservation.CommitWithCompletion(frame, frame.wireBytes(), receipt); err != nil {
			t.Fatal(err)
		}
		receipts[index] = receipt
	}
	return receipts
}

func newBlockedTCPFaultSession(t *testing.T, writeTimeout time.Duration) (*session, net.Conn) {
	t.Helper()
	serverConn, peer := net.Pipe()
	server := New(Config{
		SendQueueSize:   1,
		MaxPendingBytes: 128 * 1024,
		WriteTimeout:    writeTimeout,
	})
	atomic.StoreInt64(&server.active, 1)
	session := &session{
		id:        1,
		server:    server,
		raw:       serverConn,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	server.sessions.Store(session.id, session)
	go session.writeLoop()
	return session, peer
}

func assertTCPFaultQueueDrained(t *testing.T, session *session) {
	t.Helper()
	snapshot := session.sendQueue.Snapshot()
	if snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("outbound queue after fault = %+v", snapshot)
	}
}

func waitForTCPFaultQueueDrain(t *testing.T, session *session) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := session.sendQueue.Snapshot()
		if snapshot.PendingFrames == 0 && snapshot.PendingBytes == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	assertTCPFaultQueueDrained(t, session)
}

func waitForTCPActiveCountWithin(t *testing.T, server *Server, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := atomic.LoadInt64(&server.active); got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("active TCP sessions = %d, want %d", atomic.LoadInt64(&server.active), want)
}

type chunkedFaultConn struct {
	mu         sync.Mutex
	maxChunk   int
	failAfter  int
	failErr    error
	written    []byte
	writeCalls int
}

func (c *chunkedFaultConn) Read([]byte) (int, error) { return 0, io.EOF }

func (c *chunkedFaultConn) Write(payload []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeCalls++
	if c.failErr != nil && len(c.written) >= c.failAfter {
		return 0, c.failErr
	}
	count := len(payload)
	if c.maxChunk > 0 && count > c.maxChunk {
		count = c.maxChunk
	}
	if c.failErr != nil && len(c.written)+count > c.failAfter {
		count = c.failAfter - len(c.written)
	}
	c.written = append(c.written, payload[:count]...)
	if c.failErr != nil && len(c.written) >= c.failAfter {
		return count, c.failErr
	}
	return count, nil
}

func (c *chunkedFaultConn) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.written...)
}

func (c *chunkedFaultConn) WriteCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeCalls
}

func (*chunkedFaultConn) Close() error                     { return nil }
func (*chunkedFaultConn) LocalAddr() net.Addr              { return tcpFaultAddr("local") }
func (*chunkedFaultConn) RemoteAddr() net.Addr             { return tcpFaultAddr("remote") }
func (*chunkedFaultConn) SetDeadline(time.Time) error      { return nil }
func (*chunkedFaultConn) SetReadDeadline(time.Time) error  { return nil }
func (*chunkedFaultConn) SetWriteDeadline(time.Time) error { return nil }

type tcpFaultAddr string

func (a tcpFaultAddr) Network() string { return "fault" }
func (a tcpFaultAddr) String() string  { return string(a) }
