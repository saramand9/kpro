package grpcx

import (
	"errors"

	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/configcheck"
)

var _ transportapi.Validator = (*Server)(nil)

// Validate checks gRPC startup invariants before listening.
func (s *Server) Validate() error {
	runtimeErr := s.validateRuntime()
	if s == nil {
		return runtimeErr
	}
	return errors.Join(
		runtimeErr,
		configcheck.ListenAddress("gRPC listen address", s.addr),
	)
}

func (s *Server) validateRuntime() error {
	if s == nil {
		return errors.New("grpc server is nil")
	}
	return errors.Join(
		s.Descriptor().Validate(),
		configcheck.NonNegativeDuration("ShutdownTimeout", s.shutdownTimeout),
		s.proxyProtocol.Validate(),
	)
}
