package tcp

import (
	"errors"

	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/configcheck"
)

var _ transportapi.Validator = (*Server)(nil)

// Validate checks TCP startup invariants before server goroutines are created.
func (s *Server) Validate() error {
	if s == nil {
		return errors.New("tcp server is nil")
	}
	return errors.Join(
		s.Descriptor().Validate(),
		configcheck.ListenAddress("TCP listen address", s.config.Addr),
		configcheck.TypedNil("Codec", s.rawConfig.Codec),
		configcheck.NonNegativeInt("MaxConnections", s.rawConfig.MaxConnections),
		configcheck.NonNegativeInt("SendQueueSize", s.rawConfig.SendQueueSize),
		configcheck.NonNegativeInt64("MaxPendingBytes", s.rawConfig.MaxPendingBytes),
		configcheck.NonNegativeInt("MaxWriteBatchFrames", s.rawConfig.MaxWriteBatchFrames),
		configcheck.NonNegativeInt64("MaxWriteBatchBytes", s.rawConfig.MaxWriteBatchBytes),
		configcheck.NonNegativeDuration("MaxWriteBatchDelay", s.rawConfig.MaxWriteBatchDelay),
		configcheck.NonNegativeInt64("MaxRetainedBufferBytes", s.rawConfig.MaxRetainedBufferBytes),
		configcheck.NonNegativeInt("MaxReadBufferSize", s.rawConfig.MaxReadBufferSize),
		configcheck.NonNegativeDuration("ReadTimeout", s.rawConfig.ReadTimeout),
		configcheck.NonNegativeDuration("WriteTimeout", s.rawConfig.WriteTimeout),
		s.rawConfig.ProxyProtocol.Validate(),
		s.rawConfig.HandlerErrorPolicy.Validate(),
	)
}
