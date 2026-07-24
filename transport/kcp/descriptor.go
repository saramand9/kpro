package kcp

import (
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
)

// Descriptor advertises the reliable stream exposed by KCP.
func (*Server) Descriptor() transportapi.Descriptor {
	return transportapi.Descriptor{
		Name:         "kcp",
		Protocol:     core.TransportKCP,
		Mode:         transportapi.ModeMessage,
		Capabilities: transportapi.CapabilitySessions | transportapi.CapabilityStream | transportapi.CapabilityRequestResponse | transportapi.CapabilityMiddleware,
	}
}
