// File: core/session.go
// Purpose: Defines shared KPro primitives used by transports, routers, and middleware.

package core

import "net"

// Transport identifies the protocol family that produced a session or invocation.
type Transport string

const (
	// TransportTCP identifies TCP message sessions.
	TransportTCP Transport = "tcp"
	// TransportKCP identifies KCP message sessions.
	TransportKCP Transport = "kcp"
	// TransportQUIC identifies QUIC message stream sessions.
	TransportQUIC Transport = "quic"
	// TransportUDP identifies UDP packet sessions.
	TransportUDP Transport = "udp"
	// TransportWebSocket identifies WebSocket message sessions.
	TransportWebSocket Transport = "websocket"
	// TransportHTTP identifies native HTTP requests.
	TransportHTTP Transport = "http"
	// TransportHTTPS identifies native HTTPS requests.
	TransportHTTPS Transport = "https"
	// TransportHTTP3 identifies native HTTP/3 requests.
	TransportHTTP3 Transport = "http3"
	// TransportSSE identifies Server-Sent Events streams.
	TransportSSE Transport = "sse"
	// TransportGRPC identifies gRPC unary or stream calls.
	TransportGRPC Transport = "grpc"
)

// Session is the common handle used by business code.
// Connection-oriented transports map it to a connection. Packet transports can
// map it to a peer address or a virtual session.
type Session interface {
	// ID returns a stable identifier for the connection or virtual peer session.
	ID() uint64
	// Transport returns the protocol family that owns the session.
	Transport() Transport
	// RemoteAddr returns the peer network address when available.
	RemoteAddr() net.Addr
	// Send encodes and admits a message response to the transport write path.
	// Queue-backed implementations can return before the write itself finishes;
	// use SessionSendWithReceipt when the optional completion result is needed.
	Send(Message) error
	// Close aborts the connection or virtual session. Use
	// SessionDrainOutbound before Close when the optional terminal drain
	// capability is required.
	Close() error
}
