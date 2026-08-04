// File: codec/codec.go
// Purpose: Defines or tests KPro wire codec encoding and decoding.

package codec

import (
	"io"

	"github.com/saramand9/kpro/core"
)

// ReadResult contains a decoded message and its wire size.
type ReadResult struct {
	// Message is the decoded business message.
	Message core.Message
	// Bytes is the number of wire bytes consumed for the message.
	Bytes int
}

// StreamCodec describes a protocol carried by stream transports such as TCP.
type StreamCodec interface {
	// Name returns a stable protocol identifier for diagnostics.
	Name() string
	// Read reads exactly one message from r, enforcing maxBodySize.
	Read(r io.Reader, maxBodySize uint32) (ReadResult, error)
	// Encode serializes one message into stream frame bytes.
	Encode(msg core.Message) ([]byte, error)
}

// ReusableStreamCodec is an optional receive contract for stream codecs that
// can decode a frame into caller-owned scratch storage. The returned message
// must be consumed before the next ReadInto call; transports use this contract
// only while the current handler invocation is synchronous.
type ReusableStreamCodec interface {
	StreamCodec
	ReadInto(r io.Reader, maxBodySize uint32, scratch []byte) (ReadResult, []byte, error)
}

// PacketCodec describes a protocol carried by packet transports such as UDP.
type PacketCodec interface {
	// Name returns a stable protocol identifier for diagnostics.
	Name() string
	// DecodePacket decodes one complete packet payload into a message.
	DecodePacket(data []byte) (core.Message, error)
	// EncodePacket serializes one message into one packet payload.
	EncodePacket(msg core.Message) ([]byte, error)
}
