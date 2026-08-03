package tcp

import (
	"io"
	"net"
	"time"

	"github.com/saramand9/kpro/transport/internal/outbound"
)

type sessionBatchWriter struct {
	items   []outbound.Item[outboundFrame]
	buffers net.Buffers
	writev  net.Buffers
}

func newSessionBatchWriter(config Config) *sessionBatchWriter {
	return &sessionBatchWriter{
		items:   make([]outbound.Item[outboundFrame], 0, config.MaxWriteBatchFrames),
		buffers: make(net.Buffers, 0, config.MaxWriteBatchFrames*2),
	}
}

func (w *sessionBatchWriter) writeAvailable(s *session, first *outbound.Item[outboundFrame]) error {
	items := s.sendQueue.DrainAvailable(first, w.items[:0], s.server.config.MaxWriteBatchFrames)
	defer func() {
		// Item and net.Buffers backing arrays are kept for reuse. Clear their
		// complete used/capacity regions so an idle session does not retain the
		// payloads from its previous batch outside the queue/pool budgets.
		clear(items)
		clear(w.buffers[:cap(w.buffers)])
		w.writev = nil
		w.items = items[:0]
		w.buffers = w.buffers[:0]
	}()
	for start := 0; start < len(items); {
		end := start
		batchBytes := int64(0)
		buffers := w.buffers[:0]
		for end < len(items) {
			frameBytes := items[end].Value.wireBytes()
			if end > start && batchBytes > s.server.config.MaxWriteBatchBytes-frameBytes {
				break
			}
			buffers = items[end].Value.appendBuffers(buffers)
			batchBytes += frameBytes
			end++
		}

		if s.server.config.WriteTimeout > 0 {
			if err := s.raw.SetWriteDeadline(time.Now().Add(s.server.config.WriteTimeout)); err != nil {
				doneOutboundItems(items[start:])
				return err
			}
		}
		// net.Buffers.WriteTo mutates its slice receiver. Keep that receiver on
		// the reusable writer rather than taking the address of a local slice
		// header, which otherwise escapes once per submission.
		w.writev = buffers
		written, err := w.writev.WriteTo(s.raw)
		if err != nil {
			doneOutboundItems(items[start:])
			return err
		}
		if written != batchBytes {
			doneOutboundItems(items[start:])
			return io.ErrShortWrite
		}
		for index := start; index < end; index++ {
			items[index].Done()
		}
		start = end
	}
	return nil
}

func doneOutboundItems(items []outbound.Item[outboundFrame]) {
	for index := range items {
		items[index].Done()
	}
}
