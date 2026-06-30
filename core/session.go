package core

import "net"

type Transport string

const (
	TransportTCP       Transport = "tcp"
	TransportUDP       Transport = "udp"
	TransportWebSocket Transport = "websocket"
	TransportHTTP      Transport = "http"
	TransportHTTPS     Transport = "https"
	TransportSSE       Transport = "sse"
)

// Session is the common handle used by business code.
// Connection-oriented transports map it to a connection. Packet transports can
// map it to a peer address or a virtual session.
type Session interface {
	ID() uint64
	Transport() Transport
	RemoteAddr() net.Addr
	Send(Message) error
	Close() error
}
