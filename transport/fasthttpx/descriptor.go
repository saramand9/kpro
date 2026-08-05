package fasthttpx

import (
	"github.com/saramand9/kpro/transport"
)

// Descriptor advertises an HTTP/1.1-native request/response endpoint.
func (e *Endpoint) Descriptor() transport.Descriptor {
	capabilities := transport.CapabilityStream |
		transport.CapabilityRequestResponse |
		transport.CapabilityMiddleware
	if e != nil && e.config.TLS {
		capabilities |= transport.CapabilityTLS
	}
	return transport.Descriptor{
		Name:         e.Name(),
		Protocol:     e.transport,
		Mode:         transport.ModeNative,
		Capabilities: capabilities,
	}
}
