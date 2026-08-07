package websocket

import (
	"io"
	"net"
	"testing"
	"time"
)

func BenchmarkWebSocketSessionBatchWrite16x1KiB(b *testing.B) {
	const (
		frameCount  = 16
		payloadSize = 1024
	)
	server := New(Config{
		SendQueueSize:       frameCount,
		MaxPendingBytes:     frameCount * payloadSize,
		MaxWriteBatchFrames: frameCount,
		MaxWriteBatchBytes:  frameCount * (payloadSize + 4),
	})
	session := &session{
		server:    server,
		raw:       batchDiscardConn{},
		sendQueue: newSessionOutboundQueue(server.config, &server.outboundStats),
		done:      make(chan struct{}),
	}
	frame := wsFrame{opcode: opBinary, payload: make([]byte, payloadSize)}
	writer := newSessionBatchWriter(server.config)
	b.ReportAllocs()
	b.SetBytes(frameCount * payloadSize)
	b.ResetTimer()
	for range b.N {
		for range frameCount {
			reservation, err := session.sendQueue.Reserve(payloadSize)
			if err != nil {
				b.Fatal(err)
			}
			if err := reservation.Commit(frame, payloadSize); err != nil {
				b.Fatal(err)
			}
		}
		first := <-session.sendQueue.Items()
		if closeAfter, err := writer.writeAvailable(session, &first); err != nil || closeAfter {
			b.Fatalf("writeAvailable() = (close=%v, err=%v)", closeAfter, err)
		}
	}
}

type batchDiscardConn struct{}

func (batchDiscardConn) Read([]byte) (int, error)          { return 0, io.EOF }
func (batchDiscardConn) Write(payload []byte) (int, error) { return len(payload), nil }
func (batchDiscardConn) Close() error                      { return nil }
func (batchDiscardConn) LocalAddr() net.Addr               { return nil }
func (batchDiscardConn) RemoteAddr() net.Addr              { return nil }
func (batchDiscardConn) SetDeadline(time.Time) error       { return nil }
func (batchDiscardConn) SetReadDeadline(time.Time) error   { return nil }
func (batchDiscardConn) SetWriteDeadline(time.Time) error  { return nil }
