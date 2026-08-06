package websocket

import (
	"errors"

	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/configcheck"
)

var _ transportapi.Validator = (*Server)(nil)

// Validate checks WebSocket routing invariants before serving.
func (s *Server) Validate() error {
	if s == nil {
		return errors.New("websocket server is nil")
	}
	validationErr := errors.Join(
		s.Descriptor().Validate(),
		configcheck.ListenAddress("WebSocket listen address", s.config.Addr),
		configcheck.TypedNil("Codec", s.rawConfig.Codec),
		configcheck.NonNegativeInt("MaxMessage", s.rawConfig.MaxMessage),
		configcheck.NonNegativeInt("MaxConnections", s.rawConfig.MaxConnections),
		configcheck.NonNegativeInt("SendQueueSize", s.rawConfig.SendQueueSize),
		configcheck.NonNegativeInt64("MaxPendingBytes", s.rawConfig.MaxPendingBytes),
		configcheck.NonNegativeInt("MaxWriteBatchFrames", s.rawConfig.MaxWriteBatchFrames),
		configcheck.NonNegativeInt64("MaxWriteBatchBytes", s.rawConfig.MaxWriteBatchBytes),
		configcheck.NonNegativeDuration("ReadTimeout", s.rawConfig.ReadTimeout),
		configcheck.NonNegativeDuration("WriteTimeout", s.rawConfig.WriteTimeout),
		configcheck.NonNegativeDuration("HandshakeTimeout", s.rawConfig.HandshakeTimeout),
		s.rawConfig.ProxyProtocol.Validate(),
		s.rawConfig.HandlerErrorPolicy.Validate(),
	)
	validationErr = errors.Join(validationErr, configcheck.HTTPPath("WebSocket path", s.config.Path))
	return validationErr
}
