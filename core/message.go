package core

const (
	MsgHeartbeat uint32 = 1
	MsgPong      uint32 = 2
	MsgEcho      uint32 = 100
	MsgError     uint32 = 0xffffffff
)

// Message is the business-level packet used by handlers.
// Transport and protocol implementations translate their wire format into it.
type Message struct {
	Flags uint8
	ID    uint32
	Seq   uint64
	Data  []byte

	Headers map[string]string
	Meta    map[string]interface{}
}
