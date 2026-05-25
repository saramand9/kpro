package protocol

import (
	"io"

	"github.com/saramand9/bignet/kpro/core"
)

// ReadResult contains a decoded message and its wire size.
type ReadResult struct {
	Message core.Message
	Bytes   int
}

// StreamProtocol describes a protocol carried by stream transports such as TCP.
type StreamProtocol interface {
	Name() string
	Read(r io.Reader, maxBodySize uint32) (ReadResult, error)
	Encode(msg core.Message) ([]byte, error)
}

// PacketProtocol describes a protocol carried by packet transports such as UDP.
type PacketProtocol interface {
	Name() string
	DecodePacket(data []byte) (core.Message, error)
	EncodePacket(msg core.Message) ([]byte, error)
}
