package wire

import (
	"io"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

const MaxFrameSize = 64 * 1024

func WriteMessage(w io.Writer, msg core.Message) error {
	frame, err := protocol.KProV1().Encode(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(frame)
	return err
}

func ReadMessage(r io.Reader) (core.Message, error) {
	result, err := protocol.KProV1().Read(r, MaxFrameSize)
	return result.Message, err
}
