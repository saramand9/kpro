package codec

import (
	"encoding/binary"
	"io"

	"github.com/saramand9/kpro/core"
)

// WriteKProV1Header writes a KPro V1 header into fixed-size transport storage
// without growing dst.
func WriteKProV1Header(dst []byte, msg core.Message) error {
	if len(dst) < FrameHeaderSize {
		return io.ErrShortBuffer
	}
	if uint64(len(msg.Data)) > uint64(^uint32(0)) {
		return ErrFrameTooLarge
	}
	binary.BigEndian.PutUint16(dst[0:2], FrameMagic)
	dst[2] = FrameVersion
	dst[3] = msg.Flags
	binary.BigEndian.PutUint32(dst[4:8], msg.ID)
	binary.BigEndian.PutUint64(dst[8:16], msg.Seq)
	binary.BigEndian.PutUint32(dst[16:20], uint32(len(msg.Data))) // #nosec G115 -- Payload length is checked against MaxUint32 before encoding.
	return nil
}
