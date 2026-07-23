package sse

import (
	"github.com/saramand9/bignet/kpro/core"
	transportapi "github.com/saramand9/bignet/kpro/transport"
)

// Descriptor advertises SSE's native server-streaming capabilities.
func (e *Endpoint) Descriptor() transportapi.Descriptor {
	capabilities := transportapi.CapabilityStream | transportapi.CapabilityServerStreaming | transportapi.CapabilityMiddleware
	if e.config.TLS {
		capabilities |= transportapi.CapabilityTLS
	}
	return transportapi.Descriptor{
		Name:         e.Name(),
		Protocol:     core.TransportSSE,
		Mode:         transportapi.ModeNative,
		Capabilities: capabilities,
	}
}
