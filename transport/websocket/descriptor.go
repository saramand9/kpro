package websocket

import (
	"github.com/saramand9/bignet/kpro/core"
	transportapi "github.com/saramand9/bignet/kpro/transport"
)

// Descriptor advertises WebSocket message-session behavior.
func (*Server) Descriptor() transportapi.Descriptor {
	return transportapi.Descriptor{
		Name:     "websocket",
		Protocol: core.TransportWebSocket,
		Mode:     transportapi.ModeMessage,
		Capabilities: transportapi.CapabilitySessions | transportapi.CapabilityStream |
			transportapi.CapabilityRequestResponse | transportapi.CapabilityBidirectionalStreaming |
			transportapi.CapabilityMiddleware,
	}
}
