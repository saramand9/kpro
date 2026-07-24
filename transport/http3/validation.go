package http3

import (
	"errors"
	"fmt"

	quicgo "github.com/quic-go/quic-go"
	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/configcheck"
)

var _ transportapi.Validator = (*Endpoint)(nil)

// Validate checks HTTP/3 TLS invariants before starting QUIC.
func (e *Endpoint) Validate() error {
	if e == nil {
		return errors.New("http3 endpoint is nil")
	}
	validationErr := errors.Join(
		e.Descriptor().Validate(),
		configcheck.ListenAddress("HTTP/3 listen address", e.config.Addr),
		configcheck.NonNegativeDuration("HandshakeTimeout", e.rawConfig.HandshakeTimeout),
		configcheck.NonNegativeDuration("ReadTimeout", e.rawConfig.ReadTimeout),
		configcheck.NonNegativeDuration("WriteTimeout", e.rawConfig.WriteTimeout),
		configcheck.NonNegativeDuration("IdleTimeout", e.rawConfig.IdleTimeout),
		configcheck.NonNegativeDuration("ShutdownTimeout", e.rawConfig.ShutdownTimeout),
		configcheck.NonNegativeInt("MaxHeaderBytes", e.rawConfig.MaxHeaderBytes),
		configcheck.NonNegativeInt64("MaxBodyBytes", e.rawConfig.MaxBodyBytes),
		configcheck.NonNegativeInt("MaxConnections", e.rawConfig.MaxConnections),
		validateQUICVersions(e.config.QUICConfig),
	)
	if e.config.TLSConfig == nil {
		validationErr = errors.Join(validationErr, configcheck.TLSKeyPair("HTTP/3", e.config.CertFile, e.config.KeyFile))
	} else {
		validationErr = errors.Join(validationErr, configcheck.TLSConfig("HTTP/3", e.config.TLSConfig))
	}
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
