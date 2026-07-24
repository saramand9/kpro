package httpx

import (
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
)

// Descriptor advertises the endpoint's configured HTTP protocol.
func (e *Endpoint) Descriptor() transportapi.Descriptor {
	protocol := e.transport
	if protocol == "" {
		protocol = core.TransportHTTP
	}
	capabilities := transportapi.CapabilityStream | transportapi.CapabilityRequestResponse | transportapi.CapabilityMiddleware
	if protocol == core.TransportHTTPS || protocol == core.TransportHTTP3 || e.config.TLS {
		capabilities |= transportapi.CapabilityTLS
	}
	return transportapi.Descriptor{
		Name:         e.Name(),
		Protocol:     protocol,
		Mode:         transportapi.ModeNative,
		Capabilities: capabilities,
	}
}
