package fasthttpx

import (
	"errors"
	"fmt"

	"github.com/saramand9/kpro/transport"
	"github.com/saramand9/kpro/transport/internal/configcheck"
)

var (
	_ transport.NativeRunner       = (*Endpoint)(nil)
	_ transport.MiddlewareTarget   = (*Endpoint)(nil)
	_ transport.ReadinessProvider  = (*Endpoint)(nil)
	_ transport.CapabilityProvider = (*Endpoint)(nil)
	_ transport.Validator          = (*Endpoint)(nil)
)

// Validate checks startup invariants without binding the listener.
func (e *Endpoint) Validate() error {
	if e == nil {
		return errors.New("fasthttp endpoint is nil")
	}
	validationErr := errors.Join(
		e.Descriptor().Validate(),
		configcheck.ListenAddress("fasthttp listen address", e.config.Addr),
		configcheck.TypedNil("Logger", e.rawConfig.Logger),
		configcheck.NonNegativeDuration("ReadTimeout", e.rawConfig.ReadTimeout),
		configcheck.NonNegativeDuration("WriteTimeout", e.rawConfig.WriteTimeout),
		configcheck.NonNegativeDuration("IdleTimeout", e.rawConfig.IdleTimeout),
		configcheck.NonNegativeDuration("ShutdownTimeout", e.rawConfig.ShutdownTimeout),
		configcheck.NonNegativeInt("ReadBufferSize", e.rawConfig.ReadBufferSize),
		configcheck.NonNegativeInt("WriteBufferSize", e.rawConfig.WriteBufferSize),
		configcheck.NonNegativeInt64("MaxBodyBytes", e.rawConfig.MaxBodyBytes),
		configcheck.NonNegativeInt("Concurrency", e.rawConfig.Concurrency),
		configcheck.NonNegativeInt("MaxConnsPerIP", e.rawConfig.MaxConnsPerIP),
		configcheck.NonNegativeInt("MaxRequestsPerConn", e.rawConfig.MaxRequestsPerConn),
		e.rawConfig.ProxyProtocol.Validate(),
	)
	if e.config.MaxBodyBytes > int64(maxInt()) {
		validationErr = errors.Join(validationErr, fmt.Errorf(
			"MaxBodyBytes %d exceeds platform int capacity",
			e.config.MaxBodyBytes,
		))
	}
	if e.config.TLS {
		validationErr = errors.Join(validationErr, configcheck.TLSKeyPair(
			"fasthttp HTTPS",
			e.config.CertFile,
			e.config.KeyFile,
		))
	}
	return validationErr
}

func maxInt() int { return int(^uint(0) >> 1) }
