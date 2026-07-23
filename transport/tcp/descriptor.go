package tcp

import (
	"github.com/saramand9/bignet/kpro/core"
	transportapi "github.com/saramand9/bignet/kpro/transport"
)

// Descriptor advertises TCP's stable capabilities to orchestration and tools.
func (*Server) Descriptor() transportapi.Descriptor {
	return transportapi.Descriptor{
		Name:         "tcp",
		Protocol:     core.TransportTCP,
		Mode:         transportapi.ModeMessage,
		Capabilities: transportapi.CapabilitySessions | transportapi.CapabilityStream | transportapi.CapabilityRequestResponse | transportapi.CapabilityMiddleware,
	}
}
