package websocket

import (
	"encoding/binary"
	"io"
	"net"
	"time"

	"github.com/saramand9/kpro/transport/internal/outbound"
)

const maxServerFrameHeader = 10

type sessionBatchWriter struct {
	items   []outbound.Item[wsFrame]
	headers [][maxServerFrameHeader]byte
	buffers net.Buffers
	writev  net.Buffers
}

func newSessionBatchWriter(config Config) *sessionBatchWriter {
	return &sessionBatchWriter{
		items:   make([]outbound.Item[wsFrame], 0, config.MaxWriteBatchFrames),
		headers: make([][maxServerFrameHeader]byte, config.MaxWriteBatchFrames),
		buffers: make(net.Buffers, 0, config.MaxWriteBatchFrames*2),
	}
}

func (w *sessionBatchWriter) writeAvailable(s *session, first *outbound.Item[wsFrame]) (bool, error) {
	items := s.sendQueue.DrainAvailable(first, w.items[:0], s.server.config.MaxWriteBatchFrames)
	defer func() {
		clear(items)
		clear(w.buffers[:cap(w.buffers)])
		w.writev = nil
		w.items = items[:0]
		w.buffers = w.buffers[:0]
	}()
	writeCount := len(items)
	closeAfterWrite := false
	for index := range items {
		if items[index].Value.opcode == opClose {
			writeCount = index + 1
			closeAfterWrite = true
			break
		}
	}

	for start := 0; start < writeCount; {
		if s.closeRequested.Load() {
			doneWebSocketItems(items[start:])
			return s.directCloseComplete.Load(), nil
		}
		end := start
		batchBytes := int64(0)
		buffers := w.buffers[:0]
		for end < writeCount {
			frame := items[end].Value
			frameBytes := webSocketFrameWireBytes(len(frame.payload))
			if end > start && batchBytes > s.server.config.MaxWriteBatchBytes-frameBytes {
				break
			}
			header := appendServerFrameHeader(w.headers[end][:0], frame.opcode, len(frame.payload))
			buffers = append(buffers, header)
			if len(frame.payload) > 0 {
				buffers = append(buffers, frame.payload)
			}
			batchBytes += frameBytes
			end++
		}

		s.writeMu.Lock()
		if s.closeRequested.Load() {
			s.writeMu.Unlock()
			doneWebSocketItems(items[start:])
			return s.directCloseComplete.Load(), nil
		}
		if s.closing {
			s.writeMu.Unlock()
			doneWebSocketItems(items[start:])
			return true, nil
		}
		if closeAfterWrite && end == writeCount {
			s.closing = true
		}
		if s.server.config.WriteTimeout > 0 {
			if err := s.raw.SetWriteDeadline(time.Now().Add(s.server.config.WriteTimeout)); err != nil {
				s.writeMu.Unlock()
				doneWebSocketItems(items[start:])
				return false, err
			}
		}
		// Keep the mutable net.Buffers receiver on the reusable writer so its
		// slice header does not escape once per submission.
		w.writev = buffers
		written, err := w.writev.WriteTo(s.raw)
		s.writeMu.Unlock()
		if err != nil {
			doneWebSocketItems(items[start:])
			return false, err
		}
		if written != batchBytes {
			doneWebSocketItems(items[start:])
			return false, io.ErrShortWrite
		}
		for index := start; index < end; index++ {
			items[index].Done()
		}
		start = end
	}
	if writeCount < len(items) {
		doneWebSocketItems(items[writeCount:])
	}
	return closeAfterWrite, nil
}

func appendServerFrameHeader(dst []byte, opcode byte, payloadBytes int) []byte {
	dst = append(dst, 0x80|opcode)
	switch {
	case payloadBytes < 126:
		return append(dst, byte(payloadBytes))
	case payloadBytes <= 0xffff:
		dst = append(dst, 126, 0, 0)
		binary.BigEndian.PutUint16(dst[len(dst)-2:], uint16(payloadBytes))
		return dst
	default:
		dst = append(dst, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(dst[len(dst)-8:], uint64(payloadBytes))
		return dst
	}
}

func webSocketFrameWireBytes(payloadBytes int) int64 {
	headerBytes := 2
	if payloadBytes >= 126 && payloadBytes <= 0xffff {
		headerBytes = 4
	} else if payloadBytes > 0xffff {
		headerBytes = 10
	}
	return int64(headerBytes) + int64(payloadBytes)
}

func doneWebSocketItems(items []outbound.Item[wsFrame]) {
	for index := range items {
		items[index].Done()
	}
}
