package tcp

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
)

func BenchmarkSessionSendContiguous(b *testing.B) {
	server := New(Config{
		Codec:         benchmarkContiguousCodec{StreamCodec: codec.KProV1()},
		MaxFrameSize:  1024,
		SendQueueSize: 1,
	})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	message := core.Message{ID: 7, Seq: 9, Data: make([]byte, 1024)}
	b.ReportAllocs()
	b.SetBytes(int64(len(message.Data)))
	b.ResetTimer()
	for range b.N {
		if err := session.Send(message); err != nil {
			b.Fatal(err)
		}
		item := <-session.sendQueue.Items()
		item.Done()
	}
}

func BenchmarkSessionSendSegmented(b *testing.B) {
	server := New(Config{MaxFrameSize: 1024, SendQueueSize: 1})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	message := core.Message{ID: 7, Seq: 9, Data: make([]byte, 1024)}
	b.ReportAllocs()
	b.SetBytes(int64(len(message.Data)))
	b.ResetTimer()
	for range b.N {
		if err := session.Send(message); err != nil {
			b.Fatal(err)
		}
		item := <-session.sendQueue.Items()
		item.Done()
	}
}

func BenchmarkReserveOutboundBufferCommit(b *testing.B) {
	server := New(Config{MaxFrameSize: 1024, SendQueueSize: 1})
	session := &session{server: server, sendQueue: newSessionOutboundQueue(server.config), done: make(chan struct{})}
	message := core.Message{ID: 7, Seq: 9}
	b.ReportAllocs()
	b.SetBytes(1024)
	b.ResetTimer()
	for range b.N {
		buffer, err := session.ReserveOutboundBuffer(1024)
		if err != nil {
			b.Fatal(err)
		}
		copy(buffer.Bytes(), benchmarkPayload)
		if err := buffer.Commit(message); err != nil {
			b.Fatal(err)
		}
		item := <-session.sendQueue.Items()
		item.Done()
	}
}

func BenchmarkTCPSessionBatchWrite16x1KiB(b *testing.B) {
	const (
		frameCount  = 16
		payloadSize = 1024
	)
	server := New(Config{
		SendQueueSize:       frameCount,
		MaxPendingBytes:     frameCount * (payloadSize + codec.FrameHeaderSize),
		MaxWriteBatchFrames: frameCount,
		MaxWriteBatchBytes:  frameCount * (payloadSize + codec.FrameHeaderSize),
	})
	session := &session{
		server:    server,
		raw:       batchDiscardConn{},
		sendQueue: newSessionOutboundQueue(server.config),
		done:      make(chan struct{}),
	}
	frame := outboundFrame{headerLen: codec.FrameHeaderSize, payload: make([]byte, payloadSize)}
	writer := newSessionBatchWriter(server.config)
	b.ReportAllocs()
	b.SetBytes(frameCount * payloadSize)
	b.ResetTimer()
	for range b.N {
		for range frameCount {
			reservation, err := session.sendQueue.Reserve(frame.wireBytes())
			if err != nil {
				b.Fatal(err)
			}
			if err := reservation.Commit(frame, frame.wireBytes()); err != nil {
				b.Fatal(err)
			}
		}
		first := <-session.sendQueue.Items()
		if err := writer.writeAvailable(session, &first); err != nil {
			b.Fatal(err)
		}
	}
}

var benchmarkPayload = make([]byte, 1024)

type benchmarkContiguousCodec struct {
	codec.StreamCodec
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
