package kcp

import (
	"errors"

	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/configcheck"
)

var _ transportapi.Validator = (*Server)(nil)

// Validate checks KCP startup invariants before serving.
func (s *Server) Validate() error {
	if s == nil {
		return errors.New("kcp server is nil")
	}
	validationErr := errors.Join(
		s.Descriptor().Validate(),
		configcheck.ListenAddress("KCP listen address", s.config.Addr),
		configcheck.TypedNil("Codec", s.rawConfig.Codec),
		configcheck.NonNegativeInt("MaxConnections", s.rawConfig.MaxConnections),
		configcheck.NonNegativeInt("SendQueueSize", s.rawConfig.SendQueueSize),
		configcheck.NonNegativeInt64("MaxPendingBytes", s.rawConfig.MaxPendingBytes),
		configcheck.NonNegativeDuration("ReadTimeout", s.rawConfig.ReadTimeout),
		configcheck.NonNegativeDuration("WriteTimeout", s.rawConfig.WriteTimeout),
		s.rawConfig.HandlerErrorPolicy.Validate(),
		configcheck.NonNegativeInt("NoDelay", s.rawConfig.NoDelay),
		configcheck.NonNegativeInt("Interval", s.rawConfig.Interval),
		configcheck.NonNegativeInt("Resend", s.rawConfig.Resend),
		configcheck.NonNegativeInt("NoCongestion", s.rawConfig.NoCongestion),
		configcheck.NonNegativeInt("SendWindow", s.rawConfig.SendWindow),
		configcheck.NonNegativeInt("ReceiveWindow", s.rawConfig.ReceiveWindow),
		configcheck.IntRangeIfSet("MTU", s.rawConfig.MTU, 50, 1500),
		configcheck.NonNegativeInt("DataShards", s.rawConfig.DataShards),
		configcheck.NonNegativeInt("ParityShards", s.rawConfig.ParityShards),
	)
	dataShards := s.rawConfig.DataShards
	parityShards := s.rawConfig.ParityShards
	if dataShards >= 0 && parityShards >= 0 {
		if (dataShards == 0) != (parityShards == 0) {
			validationErr = errors.Join(validationErr, errors.New("DataShards and ParityShards must both be zero or both be positive"))
		}
		if dataShards > 0 && parityShards > 0 &&
			(dataShards > 256 || parityShards > 256 || dataShards > 256-parityShards) {
			validationErr = errors.Join(validationErr, errors.New("DataShards plus ParityShards cannot exceed 256"))
		}
	}
	return validationErr
}
