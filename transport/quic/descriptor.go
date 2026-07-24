package quic

import (
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
)

// Descriptor advertises QUIC's TLS-protected bidirectional stream sessions.
func (*Server) Descriptor() transportapi.Descriptor {
	return transportapi.Descriptor{
		Name:     "quic",
		Protocol: core.TransportQUIC,
		Mode:     transportapi.ModeMessage,
		Capabilities: transportapi.CapabilitySessions | transportapi.CapabilityStream |
			transportapi.CapabilityRequestResponse | transportapi.CapabilityBidirectionalStreaming |
			transportapi.CapabilityTLS | transportapi.CapabilityMiddleware,
	}
}
