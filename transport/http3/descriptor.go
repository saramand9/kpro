package http3

import (
	"github.com/saramand9/bignet/kpro/core"
	transportapi "github.com/saramand9/bignet/kpro/transport"
)

// Descriptor advertises HTTP/3's native request and TLS capabilities.
func (*Endpoint) Descriptor() transportapi.Descriptor {
	return transportapi.Descriptor{
		Name:         "http3",
		Protocol:     core.TransportHTTP3,
		Mode:         transportapi.ModeNative,
		Capabilities: transportapi.CapabilityStream | transportapi.CapabilityRequestResponse | transportapi.CapabilityTLS | transportapi.CapabilityMiddleware,
	}
}
