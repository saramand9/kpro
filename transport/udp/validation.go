package udp

import (
	"errors"

	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/configcheck"
)

var _ transportapi.Validator = (*Server)(nil)

// Validate checks UDP startup invariants before serving.
func (s *Server) Validate() error {
	if s == nil {
		return errors.New("udp server is nil")
	}
	return errors.Join(
		s.Descriptor().Validate(),
		configcheck.ListenAddress("UDP listen address", s.config.Addr),
		configcheck.TypedNil("Codec", s.rawConfig.Codec),
		configcheck.NonNegativeInt("MaxPacketSize", s.rawConfig.MaxPacketSize),
		configcheck.NonNegativeInt("MaxSessions", s.rawConfig.MaxSessions),
		configcheck.NonNegativeInt("SendQueueSize", s.rawConfig.SendQueueSize),
		configcheck.NonNegativeDuration("SessionIdleTimeout", s.rawConfig.SessionIdleTimeout),
		configcheck.NonNegativeDuration("CleanupInterval", s.rawConfig.CleanupInterval),
		s.rawConfig.HandlerErrorPolicy.Validate(),
	)
}
