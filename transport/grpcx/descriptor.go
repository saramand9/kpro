package grpcx

import (
	"github.com/saramand9/kpro/core"
	transportapi "github.com/saramand9/kpro/transport"
)

// Descriptor advertises gRPC unary and streaming capabilities. TLS remains an
// application-supplied grpc.ServerOption and is therefore not assumed here.
func (*Server) Descriptor() transportapi.Descriptor {
	return transportapi.Descriptor{
		Name:     string(core.TransportGRPC),
		Protocol: core.TransportGRPC,
		Mode:     transportapi.ModeNative,
		Capabilities: transportapi.CapabilityStream | transportapi.CapabilityRequestResponse |
			transportapi.CapabilityServerStreaming | transportapi.CapabilityBidirectionalStreaming |
			transportapi.CapabilityMiddleware,
	}
}
