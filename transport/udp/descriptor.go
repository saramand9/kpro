package udp

import (
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
)

// Descriptor advertises UDP's virtual-session datagram capabilities.
func (*Server) Descriptor() transportapi.Descriptor {
	return transportapi.Descriptor{
		Name:         "udp",
		Protocol:     core.TransportUDP,
		Mode:         transportapi.ModeMessage,
		Capabilities: transportapi.CapabilitySessions | transportapi.CapabilityDatagram | transportapi.CapabilityRequestResponse | transportapi.CapabilityMiddleware,
	}
}
