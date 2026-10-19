package tcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/core"
)

func TestSessionBatchWriterPreservesFrameOrder(t *testing.T) {
	frames := []outboundFrame{
		{payload: []byte("a")},
		{payload: []byte("bb")},
		{payload: []byte("ccc")},
	}
	session, peer := newTCPBatchWriterTestSession(t, frames)
	defer session.raw.Close()
	defer peer.Close()
	writer := newSessionBatchWriter(session.server.config)
	first := <-session.sendQueue.Items()
	result := make(chan error, 1)
	go func() { result <- writer.writeAvailable(session, &first) }()

	want := []byte("abbccc")
	got := make([]byte, len(want))
	if _, err := io.ReadFull(peer, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("batched wire = %q, want %q", got, want)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batch writer")
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after batch = %+v", snapshot)
	}
	assertTCPBatchWriterReleasedReferences(t, writer, len(frames))
}

func TestSessionBatchWriterHonorsFrameLimitUnderBacklog(t *testing.T) {
	for _, limit := range []int{1, 2, DefaultMaxWriteBatchFrames} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			config := New(Config{SendQueueSize: limit*2 + 1, MaxWriteBatchFrames: limit}).config
			conn := &batchRecordingConn{}
			s := &session{server: &Server{config: config}, raw: conn, sendQueue: newSessionOutboundQueue(config)}
			total := limit*2 + 1
			for i := 0; i < total; i++ {
				frame := outboundFrame{payload: []byte{byte(i)}}
				reservation, err := s.sendQueue.Reserve(frame.wireBytes())
				if err != nil {
					t.Fatal(err)
				}
				if err := reservation.Commit(frame, frame.wireBytes()); err != nil {
					t.Fatal(err)
				}
			}
			writer := newSessionBatchWriter(config)
			for written := 0; written < total; {
				first := <-s.sendQueue.Items()
				if err := writer.writeAvailable(s, &first); err != nil {
					t.Fatal(err)
				}
				written += min(limit, total-written)
				if got := len(conn.payload); got != written {
					t.Fatalf("wrote %d frames, want %d", got, written)
				}
				if got := s.sendQueue.Snapshot().PendingFrames; got != total-written {
					t.Fatalf("pending frames = %d, want %d", got, total-written)
				}
			}
			for i, value := range conn.payload {
				if value != byte(i) {
					t.Fatalf("frame %d = %d", i, value)
				}
			}
		})
	}
}

type batchRecordingConn struct {
	batchShortWriteConn
	payload []byte
}

func (c *batchRecordingConn) Write(payload []byte) (int, error) {
	c.payload = append(c.payload, payload...)
	return len(payload), nil
}

func TestSessionBatchWriterReleasesEveryItemOnWriteError(t *testing.T) {
	frames := []outboundFrame{
		{payload: []byte("a")},
		{payload: []byte("b")},
	}
	session, peer := newTCPBatchWriterTestSession(t, frames)
	peer.Close()
	defer session.raw.Close()
	writer := newSessionBatchWriter(session.server.config)
	first := <-session.sendQueue.Items()
	if err := writer.writeAvailable(session, &first); err == nil {
		t.Fatal("writeAvailable() error = nil")
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after failed batch = %+v", snapshot)
	}
	assertTCPBatchWriterReleasedReferences(t, writer, len(frames))
}

func TestSessionBatchWriterRejectsShortWriteWithoutError(t *testing.T) {
	server := New(Config{SendQueueSize: 1, MaxPendingBytes: 1024, WriteTimeout: time.Second})
	session := &session{
		server:    server,
		raw:       &batchShortWriteConn{},
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	frame := outboundFrame{payload: []byte("payload")}
	receipt := core.NewWriteReceipt()
	reservation, err := session.sendQueue.Reserve(frame.wireBytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.CommitWithCompletion(frame, frame.wireBytes(), receipt); err != nil {
		t.Fatal(err)
	}

	writer := newSessionBatchWriter(server.config)
	first := <-session.sendQueue.Items()
	if err := writer.writeAvailable(session, &first); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAvailable() error = %v, want io.ErrShortWrite", err)
	}
	if err := receipt.Wait(context.Background()); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("write receipt error = %v, want io.ErrShortWrite", err)
	}
	if snapshot := session.sendQueue.Snapshot(); snapshot.PendingFrames != 0 || snapshot.PendingBytes != 0 {
		t.Fatalf("snapshot after short write = %+v", snapshot)
	}
	assertTCPBatchWriterReleasedReferences(t, writer, 1)
}

func assertTCPBatchWriterReleasedReferences(t testing.TB, writer *sessionBatchWriter, itemCount int) {
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

func newTCPBatchWriterTestSession(t testing.TB, frames []outboundFrame) (*session, net.Conn) {
	t.Helper()
	server := New(Config{
		SendQueueSize:       len(frames),
		MaxPendingBytes:     1 << 20,
		MaxWriteBatchFrames: len(frames),
		MaxWriteBatchBytes:  3,
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
		reservation, err := session.sendQueue.Reserve(frame.wireBytes())
		if err != nil {
			t.Fatal(err)
		}
		if err := reservation.Commit(frame, frame.wireBytes()); err != nil {
			t.Fatal(err)
		}
	}
	return session, peer
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
