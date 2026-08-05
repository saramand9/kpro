package tcp

import (
	"net"

	"github.com/saramand9/kpro/codec"
	"github.com/saramand9/kpro/core"
	"github.com/saramand9/kpro/transport/internal/bufferpool"
)

const maxInlineFrameHeader = codec.FrameHeaderSize

type outboundFrame struct {
	header    [maxInlineFrameHeader]byte
	headerLen uint8
	payload   []byte
	pool      *bufferpool.Pool
	owned     *core.BaseContext
}

func (f *outboundFrame) appendBuffers(dst net.Buffers) net.Buffers {
	if f.headerLen > 0 {
		dst = append(dst, f.header[:f.headerLen])
	}
	if len(f.payload) > 0 {
		dst = append(dst, f.payload)
	}
	return dst
}

func (f *outboundFrame) wireBytes() int64 {
	return int64(f.headerLen) + int64(len(f.payload))
}

func (f *outboundFrame) release() {
	if f.pool != nil {
		f.pool.Put(f.payload)
	} else if f.owned != nil {
		core.ReleaseTransportContext(f.owned)
	}
	f.payload = nil
	f.pool = nil
	f.owned = nil
}
