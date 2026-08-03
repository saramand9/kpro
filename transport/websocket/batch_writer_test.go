package websocket

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppendServerFrameHeaderMatchesWriteFrame(t *testing.T) {
	for _, size := range []int{0, 125, 126, 65535, 65536} {
		t.Run(string(rune(size)), func(t *testing.T) {
			payload := make([]byte, size)
			var encoded bytes.Buffer
			if err := writeFrame(&encoded, opBinary, payload); err != nil {
				t.Fatal(err)
			}
			var storage [maxServerFrameHeader]byte
			header := appendServerFrameHeader(storage[:0], opBinary, size)
			if !bytes.Equal(header, encoded.Bytes()[:len(header)]) {
				t.Fatalf("header = %x, want %x", header, encoded.Bytes()[:len(header)])
			}
			if got := int64(len(header) + size); webSocketFrameWireBytes(size) != got {
				t.Fatalf("webSocketFrameWireBytes(%d) = %d, want %d", size, webSocketFrameWireBytes(size), got)
			}
		})
	}
}

func TestSessionBatchWriterPreservesFrameOrder(t *testing.T) {
	frames := []wsFrame{
		{opcode: opBinary, payload: []byte("a")},
		{opcode: opBinary, payload: []byte("bb")},
		{opcode: opPong, payload: []byte("ccc")},
	}
	session, peer := newBatchWriterTestSession(t, frames)
	defer session.raw.Close()
	defer peer.Close()
	writer := newSessionBatchWriter(session.server.config)
	first := <-session.sendQueue.Items()
	result := make(chan struct {
		closeAfter bool
		err        error
	}, 1)
	go func() {
		closeAfter, err := writer.writeAvailable(session, &first)
		result <- struct {
			closeAfter bool
			err        error
		}{closeAfter: closeAfter, err: err}
	}()

	want := encodeServerFrames(t, frames)
	got := make([]byte, len(want))
	if _, err := io.ReadFull(peer, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("batched wire = %x, want %x", got, want)
	}
	select {
	case writeResult := <-result:
		if writeResult.err != nil || writeResult.closeAfter {
			t.Fatalf("writeAvailable() = (close=%v, err=%v)", writeResult.closeAfter, writeResult.err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batch writer")
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after batch = %+v", snapshot)
	}
	assertWebSocketBatchWriterReleasedReferences(t, writer, len(frames))
}

func TestSessionBatchWriterStopsAtCloseFrame(t *testing.T) {
	frames := []wsFrame{
		{opcode: opBinary, payload: []byte("before")},
		{opcode: opClose},
		{opcode: opBinary, payload: []byte("after")},
	}
	session, peer := newBatchWriterTestSession(t, frames)
	defer session.raw.Close()
	defer peer.Close()
	writer := newSessionBatchWriter(session.server.config)
	first := <-session.sendQueue.Items()
	result := make(chan bool, 1)
	errResult := make(chan error, 1)
	go func() {
		closeAfter, err := writer.writeAvailable(session, &first)
		result <- closeAfter
		errResult <- err
	}()

	want := encodeServerFrames(t, frames[:2])
	got := make([]byte, len(want))
	if _, err := io.ReadFull(peer, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("close batch wire = %x, want %x", got, want)
	}
	if err := <-errResult; err != nil {
		t.Fatal(err)
	}
	if closeAfter := <-result; !closeAfter {
		t.Fatal("writeAvailable did not report close frame")
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after close batch = %+v", snapshot)
	}
	assertWebSocketBatchWriterReleasedReferences(t, writer, len(frames))
}

func TestSessionBatchWriterDropsDataAfterDirectClose(t *testing.T) {
	frames := []wsFrame{{opcode: opBinary, payload: []byte("after")}}
	session, peer := newBatchWriterTestSession(t, frames)
	defer session.raw.Close()
	defer peer.Close()

	closeResult := make(chan error, 1)
	go func() {
		closeResult <- session.writeWebSocketFrame(opClose, closePayload(1000, "done"))
	}()
	wantClose := encodeServerFrames(t, []wsFrame{{opcode: opClose, payload: closePayload(1000, "done")}})
	gotClose := make([]byte, len(wantClose))
	if _, err := io.ReadFull(peer, gotClose); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotClose, wantClose) {
		t.Fatalf("direct close wire = %x, want %x", gotClose, wantClose)
	}
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}

	writer := newSessionBatchWriter(session.server.config)
	first := <-session.sendQueue.Items()
	closeAfter, err := writer.writeAvailable(session, &first)
	if err != nil || !closeAfter {
		t.Fatalf("writeAvailable() after close = (close=%v, err=%v)", closeAfter, err)
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after dropped batch = %+v", snapshot)
	}
	assertWebSocketBatchWriterReleasedReferences(t, writer, len(frames))

	if err := peer.SetReadDeadline(time.Now().Add(25 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var unexpected [1]byte
	if _, err := peer.Read(unexpected[:]); err == nil {
		t.Fatalf("received data frame byte %#x after close", unexpected[0])
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("read after close error = %v, want timeout", err)
	}
}

func TestSessionBatchWriterRejectsShortWriteWithoutError(t *testing.T) {
	frames := []wsFrame{{opcode: opBinary, payload: []byte("payload")}}
	server := New(Config{SendQueueSize: 1, MaxPendingBytes: 1024, WriteTimeout: time.Second})
	session := &session{
		server:    server,
		raw:       &batchShortWriteConn{},
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	reservation, err := session.sendQueue.Reserve(int64(len(frames[0].payload)))
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.Commit(frames[0], int64(len(frames[0].payload))); err != nil {
		t.Fatal(err)
	}

	writer := newSessionBatchWriter(server.config)
	first := <-session.sendQueue.Items()
	if _, err := writer.writeAvailable(session, &first); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAvailable() error = %v, want io.ErrShortWrite", err)
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after short write = %+v", snapshot)
	}
	assertWebSocketBatchWriterReleasedReferences(t, writer, 1)
}

func TestSessionBatchWriterYieldsToWaitingDirectCloseBetweenByteBatches(t *testing.T) {
	frames := []wsFrame{
		{opcode: opBinary},
		{opcode: opBinary},
	}
	connection := newBatchInterleaveConn()
	server := New(Config{
		SendQueueSize:       len(frames),
		MaxPendingBytes:     1024,
		MaxWriteBatchFrames: len(frames),
		MaxWriteBatchBytes:  2,
		WriteTimeout:        time.Second,
	})
	session := &session{
		server:    server,
		raw:       connection,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	for _, frame := range frames {
		reservation, err := session.sendQueue.Reserve(0)
		if err != nil {
			t.Fatal(err)
		}
		if err := reservation.Commit(frame, 0); err != nil {
			t.Fatal(err)
		}
	}

	writer := newSessionBatchWriter(server.config)
	first := <-session.sendQueue.Items()
	writeResult := make(chan struct {
		closeAfter bool
		err        error
	}, 1)
	go func() {
		closeAfter, err := writer.writeAvailable(session, &first)
		writeResult <- struct {
			closeAfter bool
			err        error
		}{closeAfter: closeAfter, err: err}
	}()
	select {
	case <-connection.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first data batch did not start")
	}

	closeBody := closePayload(1000, "done")
	closeResult := make(chan error, 1)
	go func() { closeResult <- session.writeWebSocketFrame(opClose, closeBody) }()
	deadline := time.Now().Add(time.Second)
	for !session.closeRequested.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !session.closeRequested.Load() {
		t.Fatal("direct close request was not published")
	}
	close(connection.releaseFirst)

	select {
	case result := <-writeResult:
		if result.err != nil {
			t.Fatalf("writeAvailable() = (close=%v, err=%v)", result.closeAfter, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("batch writer did not yield to direct close")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("direct close did not complete")
	}

	want := append(
		encodeServerFrames(t, frames[:1]),
		encodeServerFrames(t, []wsFrame{{opcode: opClose, payload: closeBody}})...,
	)
	if got := connection.bytes(); !bytes.Equal(got, want) {
		t.Fatalf("interleaved close wire = %x, want %x", got, want)
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after interleaved close = %+v", snapshot)
	}
	assertWebSocketBatchWriterReleasedReferences(t, writer, len(frames))
}

func assertWebSocketBatchWriterReleasedReferences(t testing.TB, writer *sessionBatchWriter, itemCount int) {
	t.Helper()
	for index, item := range writer.items[:itemCount] {
		if item.Value.payload != nil {
			t.Fatalf("writer item %d retained a payload", index)
		}
	}
	for index, buffer := range writer.buffers[:cap(writer.buffers)] {
		if buffer != nil {
			t.Fatalf("writer buffer %d retained a payload", index)
		}
	}
}

func newBatchWriterTestSession(t testing.TB, frames []wsFrame) (*session, net.Conn) {
	t.Helper()
	server := New(Config{
		SendQueueSize:       len(frames),
		MaxPendingBytes:     1 << 20,
		MaxWriteBatchFrames: len(frames),
		MaxWriteBatchBytes:  1 << 20,
		WriteTimeout:        time.Second,
	})
	serverConn, peer := net.Pipe()
	session := &session{
		server:    server,
		raw:       serverConn,
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	for _, frame := range frames {
		reservation, err := session.sendQueue.Reserve(int64(len(frame.payload)))
		if err != nil {
			t.Fatal(err)
		}
		if err := reservation.Commit(frame, int64(len(frame.payload))); err != nil {
			t.Fatal(err)
		}
	}
	return session, peer
}

func encodeServerFrames(t testing.TB, frames []wsFrame) []byte {
	t.Helper()
	var encoded bytes.Buffer
	for _, frame := range frames {
		if err := writeFrame(&encoded, frame.opcode, frame.payload); err != nil {
			t.Fatal(err)
		}
	}
	return encoded.Bytes()
}

type batchShortWriteConn struct{}

func (*batchShortWriteConn) Read([]byte) (int, error)          { return 0, io.EOF }
func (*batchShortWriteConn) Write(payload []byte) (int, error) { return max(len(payload)-1, 0), nil }
func (*batchShortWriteConn) Close() error                      { return nil }
func (*batchShortWriteConn) LocalAddr() net.Addr               { return nil }
func (*batchShortWriteConn) RemoteAddr() net.Addr              { return nil }
func (*batchShortWriteConn) SetDeadline(time.Time) error       { return nil }
func (*batchShortWriteConn) SetReadDeadline(time.Time) error   { return nil }
func (*batchShortWriteConn) SetWriteDeadline(time.Time) error  { return nil }

type batchInterleaveConn struct {
	firstStarted chan struct{}
	releaseFirst chan struct{}
	writes       atomic.Int32
	mu           sync.Mutex
	wire         bytes.Buffer
}

func newBatchInterleaveConn() *batchInterleaveConn {
	return &batchInterleaveConn{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
}

func (c *batchInterleaveConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *batchInterleaveConn) Write(payload []byte) (int, error) {
	if c.writes.Add(1) == 1 {
		close(c.firstStarted)
		<-c.releaseFirst
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wire.Write(payload)
}
func (*batchInterleaveConn) Close() error                     { return nil }
func (*batchInterleaveConn) LocalAddr() net.Addr              { return nil }
func (*batchInterleaveConn) RemoteAddr() net.Addr             { return nil }
func (*batchInterleaveConn) SetDeadline(time.Time) error      { return nil }
func (*batchInterleaveConn) SetReadDeadline(time.Time) error  { return nil }
func (*batchInterleaveConn) SetWriteDeadline(time.Time) error { return nil }

func (c *batchInterleaveConn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.wire.Bytes())
}
