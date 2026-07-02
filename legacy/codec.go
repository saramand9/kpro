package legacy

import (
	"io"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

const (
	FrameMagic      = protocol.FrameMagic
	FrameVersion    = protocol.FrameVersion
	FrameHeaderSize = protocol.FrameHeaderSize
)

const (
	MsgHeartbeat = core.MsgHeartbeat
	MsgPong      = core.MsgPong
	MsgEcho      = core.MsgEcho
	MsgError     = core.MsgError
)

type Message = core.Message

func readFrame(r io.Reader, maxSize uint32) (Message, error) {
	result, err := protocol.KProV1().Read(r, maxSize)
	return result.Message, err
}

func packFrame(msg Message) []byte {
	buf, _ := protocol.KProV1().Encode(msg)
	return buf
}

func writeFull(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
