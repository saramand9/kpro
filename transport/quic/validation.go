package quic

import (
	"errors"
	"fmt"

	quicgo "github.com/quic-go/quic-go"
	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/configcheck"
)

var _ transportapi.Validator = (*Server)(nil)

// Validate checks QUIC TLS and descriptor invariants before opening a socket.
func (s *Server) Validate() error {
	if s == nil {
		return errors.New("quic server is nil")
	}
	validationErr := errors.Join(
		s.Descriptor().Validate(),
		configcheck.ListenAddress("QUIC listen address", s.config.Addr),
		configcheck.TypedNil("Codec", s.rawConfig.Codec),
		configcheck.NonNegativeInt("MaxStreams", s.rawConfig.MaxStreams),
		configcheck.NonNegativeInt("MaxConnections", s.rawConfig.MaxConnections),
		configcheck.NonNegativeInt("SendQueueSize", s.rawConfig.SendQueueSize),
		configcheck.NonNegativeDuration("ReadTimeout", s.rawConfig.ReadTimeout),
		configcheck.NonNegativeDuration("WriteTimeout", s.rawConfig.WriteTimeout),
		configcheck.NonNegativeDuration("HandshakeIdleTimeout", s.rawConfig.HandshakeIdleTimeout),
		configcheck.NonNegativeDuration("IdleTimeout", s.rawConfig.IdleTimeout),
		s.rawConfig.HandlerErrorPolicy.Validate(),
		validateQUICVersions(s.config.QUICConfig),
	)
	if s.config.TLSConfig == nil {
		validationErr = errors.Join(validationErr, configcheck.TLSKeyPair("QUIC", s.config.CertFile, s.config.KeyFile))
	} else {
		validationErr = errors.Join(validationErr, configcheck.TLSConfig("QUIC", s.config.TLSConfig))
	}
	effectiveNextProtos := s.config.NextProtos
	if s.config.TLSConfig != nil && len(s.config.TLSConfig.NextProtos) > 0 {
		effectiveNextProtos = s.config.TLSConfig.NextProtos
	}
	validationErr = errors.Join(validationErr, configcheck.ALPN("NextProtos", effectiveNextProtos))
	return validationErr
}

func validateQUICVersions(config *quicgo.Config) error {
	if config == nil {
		return nil
	}
	var validationErr error
	for index, version := range config.Versions {
		if version != quicgo.Version1 && version != quicgo.Version2 {
			validationErr = errors.Join(validationErr, fmt.Errorf(
				"QUICConfig.Versions[%d] 0x%x is unsupported",
				index,
				uint32(version),
			))
		}
	}
	return validationErr
}
