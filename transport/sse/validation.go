package sse

import (
	"errors"

	transportapi "github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/configcheck"
)

var _ transportapi.Validator = (*Endpoint)(nil)

// Validate checks SSE path and TLS invariants before serving.
func (e *Endpoint) Validate() error {
	if e == nil {
		return errors.New("sse endpoint is nil")
	}
	validationErr := errors.Join(
		e.Descriptor().Validate(),
		configcheck.ListenAddress("SSE listen address", e.config.Addr),
		configcheck.NonNegativeDuration("WriteTimeout", e.rawConfig.WriteTimeout),
		configcheck.NonNegativeInt("MaxStreams", e.rawConfig.MaxStreams),
		configcheck.NonNegativeDuration("HandshakeTimeout", e.rawConfig.HandshakeTimeout),
		configcheck.NonNegativeDuration("IdleTimeout", e.rawConfig.IdleTimeout),
		configcheck.NonNegativeDuration("ShutdownTimeout", e.rawConfig.ShutdownTimeout),
	)
	validationErr = errors.Join(validationErr, configcheck.HTTPPath("SSE path", e.config.Path))
	if e.config.TLS {
		validationErr = errors.Join(validationErr, configcheck.TLSKeyPair("TLS SSE", e.config.CertFile, e.config.KeyFile))
	}
	return validationErr
}
