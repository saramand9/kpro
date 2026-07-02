// File: examples/internal/wire/kpro.go
// Purpose: Provides runnable KPro example code.

package wire

import (
	"io"

	"github.com/saramand9/bignet/kpro/core"
	"github.com/saramand9/bignet/kpro/protocol"
)

// MaxFrameSize is the example wire helper frame limit.
const MaxFrameSize = 64 * 1024

// WriteMessage encodes and writes one KPro message.
func WriteMessage(w io.Writer, msg core.Message) error {
	frame, err := protocol.KProV1().Encode(msg)
	if err != nil {
		return err
	}
	_, err = w.Write(frame)
	return err
}

// ReadMessage reads and decodes one KPro message.
func ReadMessage(r io.Reader) (core.Message, error) {
	result, err := protocol.KProV1().Read(r, MaxFrameSize)
	return result.Message, err
}
