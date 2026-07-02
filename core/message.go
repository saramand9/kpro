// File: core/message.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

const (
	// MsgHeartbeat asks a message transport to respond with MsgPong.
	MsgHeartbeat uint32 = 1
	// MsgPong is the built-in heartbeat response.
	MsgPong uint32 = 2
	// MsgEcho is the built-in echo message used by examples and tests.
	MsgEcho uint32 = 100
	// MsgError carries framework or handler errors back to a peer.
	MsgError uint32 = 0xffffffff
)

// Message is the business-level packet used by handlers.
// Transport and protocol implementations translate their wire format into it.
type Message struct {
	// Flags carries protocol-specific bit flags. The default KPro protocol stores
	// this in the frame header and leaves interpretation to higher layers.
	Flags uint8
	// ID is the application message id used by Router.Handle and Endpoint.Handle.
	ID uint32
	// Seq is the caller-provided sequence number copied into replies and errors.
	Seq uint64
	// Data is the message payload after wire decoding.
	Data []byte

	// Headers carries optional logical headers for message-style protocols.
	Headers map[string]string
	// Meta carries optional protocol or application metadata.
	Meta map[string]interface{}
}
